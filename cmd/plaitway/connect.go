package main

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
)

const (
	defaultConnectTimeout = 60 * time.Second
	// maxCredentialAttempts is how often a person is asked again after the
	// server refused what was typed.
	maxCredentialAttempts = 3
)

func (a *app) connect(ctx context.Context, args []string) error {
	fs := a.flagSet("connect")
	username := fs.String("username", "", "user name, for a profile that asks for one; the password comes from a prompt or from stdin")
	noWait := fs.Bool("no-wait", false, "return as soon as the daemon has started connecting")
	timeout := fs.Duration("timeout", defaultConnectTimeout, "how long to wait for the daemon to make progress")
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
	if _, err := c.setEnabled(ctx, p.Id, true); err != nil {
		return err
	}
	if *noWait {
		fmt.Fprintf(a.stdout, "%s: connecting\n", clean(p.Name))
		return nil
	}
	return a.waitConnected(ctx, c, p, *username, *timeout)
}

// waitConnected follows the profile until it is up. A profile that asks for
// credentials is answered here; running connect again answers a request that an
// earlier run left open.
func (a *app) waitConnected(ctx context.Context, c *client, p *pb.Profile, username string, timeout time.Duration) error {
	answers := 0
	for {
		cur, err := c.awaitSettled(ctx, p, timeout)
		if err != nil {
			return err
		}
		switch cur.State {
		case pb.ProfileState_PROFILE_STATE_CONNECTED:
			fmt.Fprintln(a.stdout, clean(p.Name+": connected"+tunnelSummary(cur)))
			return nil
		case pb.ProfileState_PROFILE_STATE_FAILED:
			return failedError(p.Name, "failed to connect", cur.LastError)
		case pb.ProfileState_PROFILE_STATE_AWAITING_CREDENTIALS:
		default:
			return fmt.Errorf("%s: disconnected before it was up", p.Name)
		}

		if answers > 0 {
			// The server refused what was sent last time.
			refused := cmp.Or(cur.LastError, "credentials rejected")
			if a.prompt == nil || answers >= maxCredentialAttempts {
				return c.abandon(ctx, p, fmt.Errorf("%s: %s", p.Name, refused))
			}
			fmt.Fprintf(a.stderr, "%s: %s\n", clean(p.Name), clean(refused))
		}
		req, err := a.askCredentials(ctx, cur, &username)
		if err != nil {
			return c.abandon(ctx, p, err)
		}
		callCtx, cancel := callContext(ctx)
		_, err = c.api.ProvideCredentials(callCtx, req)
		cancel()
		switch {
		case status.Code(err) == codes.FailedPrecondition:
			// Another client, such as the menu bar app with the password from
			// its Keychain, answered first; the state tells how it went.
		case err != nil:
			return c.failure(err)
		default:
			answers++
		}
	}
}

func failedError(name, what, reason string) error {
	if reason == "" {
		return fmt.Errorf("%s: %s", name, what)
	}
	return fmt.Errorf("%s: %s: %s", name, what, reason)
}

// tunnelSummary is " (utun4, 10.8.0.2/24)" for a profile that is up.
func tunnelSummary(p *pb.Profile) string {
	st := p.GetStatus()
	var parts []string
	if st.GetInterfaceName() != "" {
		parts = append(parts, st.InterfaceName)
	}
	parts = append(parts, st.GetAddresses()...)
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// abandon stops connecting a profile that nobody can answer, as the app does
// when its credentials sheet is cancelled, and returns why.
func (c *client) abandon(ctx context.Context, p *pb.Profile, reason error) error {
	// The reason may be an interrupt, which has cancelled ctx.
	if _, err := c.setEnabled(context.WithoutCancel(ctx), p.Id, false); err != nil {
		return errors.Join(reason, fmt.Errorf("%s: could not stop connecting: %w", p.Name, err))
	}
	return reason
}

// awaitSettled watches the daemon until the profile has reached a state that
// needs a decision: connected, failed, asking for credentials or disconnected.
// The stream is closed before the caller does anything slow, such as asking a
// person: the daemon drops a watcher that stops reading.
func (c *client) awaitSettled(parent context.Context, p *pb.Profile, timeout time.Duration) (*pb.Profile, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	stream, err := c.api.WatchProfiles(ctx, &pb.WatchProfilesRequest{})
	if err != nil {
		return nil, c.failure(err)
	}
	last := p
	for {
		ev, err := stream.Recv()
		switch {
		case parent.Err() != nil:
			return nil, errInterrupted
		// The daemon sees the same deadline and may report it before ctx does.
		case ctx.Err() != nil, status.Code(err) == codes.DeadlineExceeded:
			return nil, fmt.Errorf("%s: still %s after %s", p.Name, stateName(last.State), timeout)
		case errors.Is(err, io.EOF):
			return nil, errors.New("the daemon closed the stream")
		case err != nil:
			return nil, c.failure(err)
		}

		var seen []*pb.Profile
		switch e := ev.Event.(type) {
		case *pb.ProfileEvent_Snapshot:
			seen = e.Snapshot.Profiles
		case *pb.ProfileEvent_Changed:
			seen = []*pb.Profile{e.Changed}
		case *pb.ProfileEvent_Removed:
			if e.Removed == p.Id {
				return nil, fmt.Errorf("%s: removed", p.Name)
			}
		}
		for _, q := range seen {
			if q.Id != p.Id {
				continue
			}
			last = q
			switch q.State {
			case pb.ProfileState_PROFILE_STATE_CONNECTED, pb.ProfileState_PROFILE_STATE_FAILED,
				pb.ProfileState_PROFILE_STATE_AWAITING_CREDENTIALS,
				pb.ProfileState_PROFILE_STATE_DISCONNECTED, pb.ProfileState_PROFILE_STATE_DISCONNECTING:
				return q, nil
			}
		}
	}
}

// askCredentials collects what the profile asks for. The password comes from
// the terminal, without echo, or from the first line of stdin; never from a
// flag or the environment, where other processes can read it.
func (a *app) askCredentials(ctx context.Context, p *pb.Profile, username *string) (*pb.ProvideCredentialsRequest, error) {
	req := &pb.ProvideCredentialsRequest{ProfileId: p.Id, Kind: p.GetCredentialRequest().GetKind()}
	var secret string
	switch req.Kind {
	case pb.CredentialKind_CREDENTIAL_KIND_USER_PASSWORD:
		if *username == "" {
			if a.prompt == nil {
				return nil, fmt.Errorf("%s: asks for a username, use -username", p.Name)
			}
			entered, err := a.prompt(ctx, "Username for "+clean(p.Name)+": ", false)
			if err != nil {
				return nil, err
			}
			if entered == "" {
				return nil, fmt.Errorf("%s: no username entered", p.Name)
			}
			*username = entered
		}
		req.Username = *username
		secret = "Password"
	case pb.CredentialKind_CREDENTIAL_KIND_KEY_PASSPHRASE:
		secret = "Key passphrase"
	default:
		return nil, fmt.Errorf("%s: asks for credentials this client does not know", p.Name)
	}

	var err error
	if a.prompt != nil {
		req.Password, err = a.prompt(ctx, secret+" for "+clean(p.Name)+": ", true)
	} else {
		req.Password, err = readLine(ctx, a.stdin)
		if errors.Is(err, io.EOF) {
			err = fmt.Errorf("%s: no %s on stdin", p.Name, strings.ToLower(secret))
		}
	}
	return req, err
}

// terminalPrompt asks a person at the terminal in. The question goes to out,
// not stdout, so that a command's output can be piped.
func terminalPrompt(in *os.File, out io.Writer) func(ctx context.Context, label string, secret bool) (string, error) {
	return func(ctx context.Context, label string, secret bool) (string, error) {
		fmt.Fprint(out, label)
		if secret {
			restore, err := disableEcho(in)
			if err != nil {
				return "", err
			}
			// The Enter that ends the answer is not echoed either.
			defer fmt.Fprintln(out)
			defer restore()
		}
		return readLine(ctx, in)
	}
}

// readLine reads one line without its line ending, and stops waiting when ctx
// ends. A last line without a line ending still counts; nothing at all is
// io.EOF.
func readLine(ctx context.Context, r io.Reader) (string, error) {
	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		line, err := bufio.NewReader(r).ReadString('\n')
		if err == io.EOF && line != "" {
			err = nil
		}
		done <- result{strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), err}
	}()
	select {
	case res := <-done:
		return res.line, res.err
	case <-ctx.Done():
		return "", errInterrupted
	}
}
