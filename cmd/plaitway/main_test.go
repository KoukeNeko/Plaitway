package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const testVersion = "9.9.9-test"

// The binaries are built once, from the sources in the repository, into a
// temporary directory: the tests run the real daemon on its in-memory backend
// and, for a few tests, the client as a process.
var daemonBinary, clientBinary string

func TestMain(m *testing.M) { os.Exit(runTests(m)) }

func runTests(m *testing.M) int {
	dir, err := os.MkdirTemp("", "plaitway-cli-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)

	daemonBinary = filepath.Join(dir, "plaitwayd")
	clientBinary = filepath.Join(dir, "plaitway")
	for _, b := range [][]string{
		{"-o", daemonBinary, "../plaitwayd"},
		{"-o", clientBinary, "-ldflags", "-X main.version=" + testVersion, "."},
	} {
		if out, err := exec.Command("go", append([]string{"build"}, b...)...).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "go build %s: %v\n%s", strings.Join(b, " "), err, out)
			return 1
		}
	}
	return m.Run()
}

// syncBuffer is a buffer that a test may read while a command still writes to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// shortDir makes a directory whose path leaves room for a socket: macOS limits
// socket paths to 104 bytes and t.TempDir() is longer.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// testDaemon is a plaitwayd process on the in-memory backend. Its connections
// take a second, as they do for a person using the development daemon.
type testDaemon struct {
	t      *testing.T
	socket string
	cmd    *exec.Cmd
	logs   *syncBuffer
	exited chan struct{}
}

func startDaemon(t *testing.T) *testDaemon {
	t.Helper()
	dir := shortDir(t)
	d := &testDaemon{t: t, socket: filepath.Join(dir, "d.sock"), logs: &syncBuffer{}, exited: make(chan struct{})}
	d.cmd = exec.Command(daemonBinary, "-fake", "-socket", d.socket, "-state-dir", filepath.Join(dir, "state"))
	d.cmd.Stderr = d.logs
	if err := d.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		d.cmd.Wait()
		close(d.exited)
	}()
	t.Cleanup(func() {
		d.stop()
		if t.Failed() {
			t.Logf("daemon log:\n%s", d.logs)
		}
	})

	deadline := time.Now().Add(10 * time.Second)
	for {
		if c, err := net.Dial("unix", d.socket); err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the daemon did not listen within 10 seconds:\n%s", d.logs)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The daemon authorizes callers from their uid: this suite needs the user
	// running it to be the console user or an administrator.
	if r := d.run("", "list"); r.code != 0 && strings.Contains(r.stderr, "permission denied") {
		t.Skipf("the daemon refuses this user: %s", r.stderr)
	}
	return d
}

// stop ends the daemon the way launchd does.
func (d *testDaemon) stop() {
	d.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-d.exited:
	case <-time.After(10 * time.Second):
		d.cmd.Process.Kill()
		<-d.exited
	}
}

// result is what a command printed and returned.
type result struct {
	code   int
	stdout string
	stderr string
}

// run executes a command line in this process, with stdin as given and as if
// no terminal were attached.
func (d *testDaemon) run(stdin string, args ...string) result {
	d.t.Helper()
	return d.runWith(context.Background(), stdin, nil, args...)
}

// runWith is run with a context, and with a chance to change the app, for
// example to give it a terminal.
func (d *testDaemon) runWith(ctx context.Context, stdin string, tweak func(*app), args ...string) result {
	d.t.Helper()
	var stdout, stderr syncBuffer
	a := d.app(strings.NewReader(stdin), &stdout, &stderr)
	if tweak != nil {
		tweak(a)
	}
	code := a.run(ctx, args)
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// app is a client of this daemon, found through the environment variable.
func (d *testDaemon) app(stdin io.Reader, stdout, stderr io.Writer) *app {
	return &app{
		stdin:  stdin,
		stdout: stdout,
		stderr: stderr,
		getenv: func(name string) string {
			if name == "PLAITWAY_SOCKET" {
				return d.socket
			}
			return ""
		},
	}
}

// mustRun runs a command that has to succeed.
func (d *testDaemon) mustRun(stdin string, args ...string) result {
	d.t.Helper()
	r := d.run(stdin, args...)
	if r.code != 0 {
		d.t.Fatalf("plaitway %s: exit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), r.code, r.stdout, r.stderr)
	}
	return r
}

// importText imports profile text through a file, as a person would, and
// returns its id. A user who is not an administrator may not import, which
// ends the test as skipped.
func (d *testDaemon) importText(name, content string) string {
	d.t.Helper()
	path := filepath.Join(d.t.TempDir(), name+".conf")
	if strings.Contains(content, "remote") {
		path = filepath.Join(d.t.TempDir(), name+".ovpn")
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		d.t.Fatal(err)
	}
	r := d.run("", "import", "-json", "-name", name, path)
	if r.code != 0 && strings.Contains(r.stderr, "permission denied") {
		d.t.Skipf("the daemon does not let this user import profiles: %s", r.stderr)
	}
	if r.code != 0 {
		d.t.Fatalf("import %s: exit %d: %s", name, r.code, r.stderr)
	}
	var resp struct{ Profile struct{ ID string } }
	if err := json.Unmarshal([]byte(r.stdout), &resp); err != nil {
		d.t.Fatal(err)
	}
	return resp.Profile.ID
}

const (
	ovpnProfile = "client\nremote vpn.example.com 1194\nroute 10.1.0.0 255.255.0.0\n"
	wgProfile   = "[Interface]\nPrivateKey = k\nAddress = 10.6.0.2/32\nDNS = 10.6.0.1\n[Peer]\nEndpoint = 203.0.113.5:51820\nAllowedIPs = 0.0.0.0/0\n"
	// The fake backend asks for a user name and a password at connect.
	credentialsProfile = ovpnProfile + "# fake: needs-credentials\n"
)

// execClient runs the built binary, with the environment of a script.
func execClient(t *testing.T, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(clientBinary, args...)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		code = exit.ExitCode()
	default:
		t.Fatal(err)
	}
	return out.String(), errb.String(), code
}
