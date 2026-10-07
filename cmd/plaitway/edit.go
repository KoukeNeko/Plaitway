package main

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/profile"
)

// editorWaitDelay bounds how long a command waits for an editor's output to
// drain once the editor itself has been stopped.
const editorWaitDelay = time.Second

// hidden stands in for a secret in the text that show prints.
const hidden = "[hidden]"

// secretBlocks are the inline blocks of an OpenVPN profile that hold a private
// key, a shared key or a login.
var secretBlocks = map[string]bool{
	"key": true, "tls-auth": true, "tls-crypt": true, "tls-crypt-v2": true, "pkcs12": true, "secret": true,
	"auth-user-pass": true, "http-proxy-user-pass": true,
}

// secretKeys are the WireGuard settings whose value is a secret, in lower case.
var secretKeys = map[string]bool{"privatekey": true, "presharedkey": true}

func (a *app) show(ctx context.Context, args []string) error {
	fs := a.flagSet("show")
	secrets := fs.Bool("secrets", false, "print private keys and inline key blocks instead of "+hidden)
	rest, err := a.parse(fs, args, 1, 1)
	if err != nil {
		return err
	}
	c, err := a.dial()
	if err != nil {
		return err
	}
	defer c.close()
	p, err := c.find(ctx, rest[0])
	if err != nil {
		return err
	}
	content, err := c.content(ctx, p.Id)
	if err != nil {
		return err
	}
	text := string(content)
	if !*secrets {
		text = maskSecrets(text)
	}
	_, err = io.WriteString(a.stdout, cleanText(text))
	return err
}

// maskSecrets replaces what a profile keeps secret by a placeholder: the value
// of PrivateKey and PresharedKey, also in a line that is commented out, the
// body of the inline blocks in secretBlocks, and a PEM private key or OpenVPN
// static key wherever it stands. A block ends where the daemon's parser ends it,
// at the first line that starts with its closing tag, and one that is never
// closed hides the rest of the text.
func maskSecrets(text string) string {
	var out strings.Builder
	var closing string // the start of the line that ends the block being hidden, in lower case
	for line := range strings.Lines(text) {
		body := strings.TrimRight(line, "\r\n")
		ending := line[len(body):]
		if closing != "" {
			if strings.HasPrefix(strings.ToLower(strings.TrimLeft(body, " \t")), closing) {
				closing = ""
				out.WriteString(line)
			}
			continue
		}
		if end, ok := secretBlockEnd(body); ok {
			closing = end
			newline := cmp.Or(ending, "\n")
			out.WriteString(body + newline + hidden + newline)
			continue
		}
		if i := strings.IndexByte(body, '='); i >= 0 && secretKeys[strings.ToLower(strings.Trim(body[:i], "#; \t"))] {
			out.WriteString(body[:i+1] + " " + hidden + ending)
			continue
		}
		out.WriteString(line)
	}
	return out.String()
}

// secretBlockEnd tells whether a line opens a block whose body is secret, and
// how the line that ends it starts, in lower case.
func secretBlockEnd(line string) (closing string, ok bool) {
	if tag := openedBlock(line); secretBlocks[tag] {
		return "</" + tag + ">", true
	}
	// A key may also be in PEM form inside a block that is not secret as a whole.
	label, found := strings.CutPrefix(strings.ToLower(strings.TrimSpace(line)), "-----begin ")
	if found && (strings.HasSuffix(label, "private key-----") || strings.Contains(label, "static key")) {
		return "-----end " + label, true
	}
	return "", false
}

// openedBlock is the tag of the block that a line opens, read as the daemon's
// parser reads it: the line holds that one word, and a # or ; where a word
// would start begins a comment. splitWords stops at a comment only at the start
// of a line.
func openedBlock(line string) string {
	words := splitWords(strings.TrimSpace(line))
	if i := slices.IndexFunc(words, func(w string) bool { return strings.HasPrefix(w, "#") || strings.HasPrefix(w, ";") }); i >= 0 {
		words = words[:i]
	}
	if len(words) != 1 {
		return ""
	}
	return blockTag(words[0])
}

func (c *client) content(ctx context.Context, id string) ([]byte, error) {
	ctx, cancel := callContext(ctx)
	defer cancel()
	resp, err := c.api.GetProfileContent(ctx, &pb.GetProfileContentRequest{Id: id})
	if err != nil {
		return nil, c.failure(err)
	}
	return resp.Content, nil
}

func (a *app) edit(ctx context.Context, args []string) (err error) {
	fs := a.flagSet("edit")
	reconnect := fs.Bool("reconnect", false, "restart the profile with the new text at once, if it is enabled")
	rest, err := a.parse(fs, args, 1, 1)
	if err != nil {
		return err
	}
	editor, err := editorCommand(a.getenv)
	if err != nil {
		return err
	}
	c, err := a.dial()
	if err != nil {
		return err
	}
	defer c.close()
	p, err := c.find(ctx, rest[0])
	if err != nil {
		return err
	}
	original, err := c.content(ctx, p.Id)
	if err != nil {
		return err
	}

	// A terminal that closes ends the program like Ctrl-C does, but without a
	// signal that main handles; the text of the profile must not stay behind.
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGHUP)
	defer stop()
	dir, err := os.MkdirTemp("", "plaitway-edit-")
	if err != nil {
		return err
	}
	defer func() {
		if removeErr := os.RemoveAll(dir); removeErr != nil {
			err = errors.Join(err, removeErr)
		}
	}()
	path := filepath.Join(dir, "profile"+profileExtension(p.Kind))
	if err := os.WriteFile(path, original, 0o600); err != nil {
		return err
	}

	for {
		if err := a.runEditor(ctx, editor, path); err != nil {
			return err
		}
		// What the daemon refuses, and a file that cannot be read back as
		// text, leave the edit in the file for another round.
		var rejected string
		edited, readErr := readText(path, profile.MaxContentSize)
		switch {
		case readErr != nil:
			rejected = readErr.Error()
		case bytes.Equal(edited, original):
			fmt.Fprintf(a.stdout, "%s: unchanged\n", clean(p.Name))
			return nil
		default:
			resp, reason, err := c.updateContent(ctx, p.Id, edited, *reconnect)
			if err != nil {
				return err
			}
			if reason == "" {
				a.printUpdated(resp, *reconnect)
				return nil
			}
			rejected = reason
		}

		fmt.Fprintf(a.stderr, "rejected: %s\n", clean(rejected))
		again, err := a.askEditAgain(ctx)
		if err != nil {
			return err
		}
		if !again {
			return errors.New("changes discarded")
		}
	}
}

func profileExtension(kind pb.ProfileKind) string {
	if kind == pb.ProfileKind_PROFILE_KIND_WIREGUARD {
		return ".conf"
	}
	return ".ovpn"
}

// editorCommand is the command that edits a file: $VISUAL, else $EDITOR, else
// vi. It may carry arguments.
func editorCommand(getenv func(string) string) ([]string, error) {
	command := cmp.Or(strings.TrimSpace(getenv("VISUAL")), strings.TrimSpace(getenv("EDITOR")), "vi")
	words := splitWords(command)
	if len(words) == 0 {
		return nil, fmt.Errorf("no editor in %q", command)
	}
	return words, nil
}

// runEditor lets a person change the file, with the terminal of this command.
// An interrupt stops the editor, since the file is about to go away.
func (a *app) runEditor(ctx context.Context, editor []string, path string) error {
	cmd := exec.CommandContext(ctx, editor[0], append(slices.Clone(editor[1:]), path)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = a.stdin, a.stdout, a.stderr
	cmd.WaitDelay = editorWaitDelay
	err := cmd.Run()
	switch {
	case ctx.Err() != nil:
		return errInterrupted
	case err != nil:
		return fmt.Errorf("editor %s: %w", editor[0], err)
	}
	return nil
}

// updateContent sends new text. A refusal of the text, which the person can
// answer by editing, comes back as its reason; any other failure as err.
func (c *client) updateContent(ctx context.Context, id string, content []byte, reconnect bool) (resp *pb.ImportProfileResponse, reason string, err error) {
	ctx, cancel := callContext(ctx)
	defer cancel()
	resp, err = c.api.UpdateProfileContent(ctx, &pb.UpdateProfileContentRequest{Id: id, Content: content, Reconnect: reconnect})
	switch {
	case status.Code(err) == codes.InvalidArgument:
		return nil, status.Convert(err).Message(), nil
	case err != nil:
		return nil, "", c.failure(err)
	}
	return resp, "", nil
}

func (a *app) printUpdated(resp *pb.ImportProfileResponse, reconnect bool) {
	for _, w := range resp.Warnings {
		fmt.Fprintln(a.stderr, "warning:", clean(importWarning(w)))
	}
	line := "updated " + clean(resp.Profile.Name)
	// A tunnel that is up keeps the text it started with.
	if resp.Profile.DesiredEnabled && !reconnect {
		line += " (applies on the next connection)"
	}
	fmt.Fprintln(a.stdout, line)
}

// askEditAgain asks whether to edit the refused text again. Without a terminal
// nobody can answer, so the text is dropped.
func (a *app) askEditAgain(ctx context.Context) (bool, error) {
	if a.prompt == nil {
		return false, nil
	}
	for {
		answer, err := a.prompt(ctx, "Edit again or discard? [E/d] ", false)
		switch {
		case errors.Is(err, io.EOF):
			return false, nil
		case err != nil:
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "", "e", "edit":
			return true, nil
		case "d", "discard":
			return false, nil
		}
	}
}
