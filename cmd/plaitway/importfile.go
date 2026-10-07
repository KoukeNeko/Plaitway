package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/profile"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// maxReferencedFileSize is generous for a certificate chain or a key, and small
// enough to rule out reading something that is not one.
const maxReferencedFileSize = 256 << 10

// inlineDirectives name a file and have an inline <tag> form. The menu bar app
// inlines the same ones (ProfileImporter.swift).
var inlineDirectives = map[string]bool{
	"ca": true, "cert": true, "key": true, "dh": true, "crl-verify": true, "extra-certs": true,
	"tls-auth": true, "tls-crypt": true, "tls-crypt-v2": true,
}

func (a *app) importProfile(ctx context.Context, args []string) error {
	fs := a.flagSet("import")
	name := fs.String("name", "", "name of the profile (default: the file name)")
	asJSON := fs.Bool("json", false, "print JSON")
	rest, err := a.parse(fs, args, 1, 1)
	if err != nil {
		return err
	}
	path := rest[0]
	content, origin, err := readProfile(path)
	if err != nil {
		return err
	}
	c, err := a.dial()
	if err != nil {
		return err
	}
	defer c.close()

	callCtx, cancel := callContext(ctx)
	defer cancel()
	resp, err := c.api.ImportProfile(callCtx, &pb.ImportProfileRequest{
		Name:           *name,
		Content:        content,
		SourceFilename: filepath.Base(path),
	})
	if status.Code(err) == codes.InvalidArgument {
		return fmt.Errorf("%s was rejected: %s", path, originalRejection(status.Convert(err).Message(), origin))
	}
	if err != nil {
		return c.failure(err)
	}
	for _, w := range resp.Warnings {
		w.Line = int32(originalLine(origin, int(w.Line)))
	}
	if *asJSON {
		return a.printJSON(resp)
	}
	for _, w := range resp.Warnings {
		fmt.Fprintln(a.stderr, "warning:", clean(importWarning(w)))
	}
	fmt.Fprintf(a.stdout, "imported %s (id %s)\n", clean(resp.Profile.Name), resp.Profile.Id)
	return nil
}

func importWarning(w *pb.ImportWarning) string {
	var parts []string
	if w.Line > 0 {
		parts = append(parts, fmt.Sprintf("line %d", w.Line))
	}
	if w.Directive != "" {
		parts = append(parts, w.Directive)
	}
	return strings.Join(append(parts, w.Message), ": ")
}

// readProfile reads a profile file for upload. The daemon takes content only,
// never a path, so an OpenVPN profile that names its certificates and keys
// gets those files read here, with the caller's own permissions, and put
// inline. Inlining moves lines, so origin tells for every line of the content
// which line of the file it stands for; it is nil when nothing moved.
func readProfile(path string) (content []byte, origin []int, err error) {
	data, err := readText(path, profile.MaxContentSize)
	if err != nil {
		return nil, nil, err
	}
	// The daemon decides the kind again from the content; this only tells
	// whether there are files to inline, and a wg-quick file has none.
	if kind, ok := profile.DetectKind(filepath.Base(path), data); !ok || kind != tunnel.KindOpenVPN {
		return data, nil, nil
	}
	dir, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return nil, nil, err
	}
	text, origin, err := inlineFiles(string(data), dir)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(text) > profile.MaxContentSize {
		return nil, nil, fmt.Errorf("%s is larger than %d bytes with its files inline", path, profile.MaxContentSize)
	}
	return []byte(text), origin, nil
}

// originalLine is the line of the file that line n of the content stands for.
func originalLine(origin []int, n int) int {
	if n >= 1 && n <= len(origin) {
		return origin[n-1]
	}
	return n
}

// leadingLine finds the line number a rejection starts with: "line 15: ...".
var leadingLine = regexp.MustCompile(`^line (\d+)`)

// originalRejection words a rejection by the daemon in the lines of the file.
func originalRejection(message string, origin []int) string {
	return leadingLine.ReplaceAllStringFunc(message, func(prefix string) string {
		n, err := strconv.Atoi(strings.TrimPrefix(prefix, "line "))
		if err != nil {
			return prefix
		}
		return "line " + strconv.Itoa(originalLine(origin, n))
	})
}

// readText reads a regular text file of at most limit bytes. A profile decides
// which files get read, so a device such as /dev/zero, which never ends, is
// refused before it is opened.
func readText(path string, limit int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, fmt.Errorf("%s is not a text file", path)
	}
	return data, nil
}

// inlineFiles replaces "ca file", "tls-auth file 1" and the like by their
// inline blocks. Relative paths are relative to dir, the directory of the
// profile, and only files inside it are read. Blocks that are already inline
// are left alone. origin[i] is the line of text that line i+1 of the result
// stands for.
func inlineFiles(text, dir string) (result string, origin []int, err error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	hasKeyDirection := false
	for _, line := range lines {
		if words := splitWords(strings.TrimSpace(line)); len(words) > 0 && strings.EqualFold(words[0], "key-direction") {
			hasKeyDirection = true
		}
	}

	var out []string
	add := func(original int, text string) {
		out = append(out, text)
		for range strings.Count(text, "\n") + 1 {
			origin = append(origin, original)
		}
	}
	openBlock := "" // the tag of the block the line is in
	for i, line := range lines {
		n := i + 1
		trimmed := strings.TrimSpace(line)
		if openBlock != "" {
			if strings.EqualFold(trimmed, "</"+openBlock+">") {
				openBlock = ""
			}
			add(n, line)
			continue
		}
		if tag := blockTag(trimmed); tag != "" {
			openBlock = tag
			add(n, line)
			continue
		}
		directive, file, keyDirection := fileReference(trimmed)
		if directive == "" {
			add(n, line)
			continue
		}

		pem, err := readKeyMaterial(directive, file, dir)
		if err != nil {
			return "", nil, err
		}
		add(n, "<"+directive+">")
		add(n, pem)
		add(n, "</"+directive+">")
		if keyDirection != "" && !hasKeyDirection {
			add(n, "key-direction "+keyDirection)
			hasKeyDirection = true
		}
	}
	return strings.Join(out, "\n"), origin, nil
}

// blockTag is the tag a line opens: "ca" for "<ca>". Lines like
// "<connection>" that carry arguments, and closing tags, open nothing.
func blockTag(line string) string {
	if !strings.HasPrefix(line, "<") || !strings.HasSuffix(line, ">") || strings.HasPrefix(line, "</") {
		return ""
	}
	tag := strings.ToLower(line[1 : len(line)-1])
	if strings.ContainsFunc(tag, unicode.IsSpace) {
		return ""
	}
	return tag
}

// fileReference reads a directive that names a file, with the key direction
// that may follow the file of tls-auth. It returns an empty directive for any
// other line.
func fileReference(line string) (directive, file, keyDirection string) {
	words := splitWords(line)
	if len(words) < 2 || !inlineDirectives[strings.ToLower(words[0])] {
		return "", "", ""
	}
	directive = strings.ToLower(words[0])
	// "dh none" switches Diffie-Hellman off; "[inline]" says the data follows.
	if (directive == "dh" && words[1] == "none") || words[1] == "[inline]" {
		return "", "", ""
	}
	if directive == "tls-auth" && len(words) >= 3 {
		keyDirection = words[2]
	}
	return directive, words[1], keyDirection
}

// splitWords splits a configuration line into words like openvpn does: double
// or single quotes group, a backslash escapes, and a line that starts with #
// or ; is a comment.
func splitWords(line string) []string {
	if line == "" || line[0] == '#' || line[0] == ';' {
		return nil
	}
	var (
		words   []string
		current strings.Builder
		inWord  bool
		quote   rune
		escaped bool
	)
	for _, r := range line {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			escaped, inWord = true, true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote, inWord = r, true
		case unicode.IsSpace(r):
			if inWord {
				words = append(words, current.String())
				current.Reset()
				inWord = false
			}
		default:
			current.WriteRune(r)
			inWord = true
		}
	}
	if inWord {
		words = append(words, current.String())
	}
	return words
}

// readKeyMaterial reads the PEM data a directive names. The file has to be
// inside dir, whatever symbolic links lead to it: a profile one is about to
// import must not be able to send any other file of the user to the daemon.
func readKeyMaterial(directive, file, dir string) (string, error) {
	path := file
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", directive, err)
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s %s is outside the directory of the profile; put the file next to the profile or its content inline", directive, file)
	}
	data, err := readText(resolved, maxReferencedFileSize)
	if err != nil {
		return "", fmt.Errorf("%s: %w", directive, err)
	}
	if !bytes.Contains(data, []byte("-----BEGIN")) {
		return "", errors.New(directive + ": " + file + " holds no certificate or key")
	}
	return strings.TrimSpace(strings.ReplaceAll(string(data), "\r\n", "\n")), nil
}
