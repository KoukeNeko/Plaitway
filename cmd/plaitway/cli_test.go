package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const waitTimeout = 10 * time.Second

type jsonProfile struct {
	ID             string
	Name           string
	State          string
	DesiredEnabled bool
	LastError      string
	Status         *struct {
		InterfaceName  string
		Addresses      []string
		ConnectedSince string
	}
	Settings struct {
		AutoConnect       bool
		TunnelMode        string
		Priority          int
		ExcludePrivateIps bool
		OnDemand          struct{ Ethernet, Wifi bool }
	}
	Summary struct{ PublicKey string }
}

// profile is what "status -json" says about one profile.
func (d *testDaemon) profile(name string) jsonProfile {
	d.t.Helper()
	var out struct{ Profiles []jsonProfile }
	if err := json.Unmarshal([]byte(d.mustRun("", "status", "-json", name).stdout), &out); err != nil || len(out.Profiles) != 1 {
		d.t.Fatalf("status -json %s: %+v, %v", name, out, err)
	}
	return out.Profiles[0]
}

// waitState waits until a profile is in a state, as "status -json" names it.
func (d *testDaemon) waitState(name, state string) {
	d.t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for d.profile(name).State != state {
		if time.Now().After(deadline) {
			d.t.Fatalf("%s did not reach %s, it is %s", name, state, d.profile(name).State)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// running is a command that has not ended yet: logs -f and watch.
type running struct {
	t      *testing.T
	stdout *syncBuffer
	stderr *syncBuffer
	cancel context.CancelFunc
	done   chan int
}

func (d *testDaemon) start(args ...string) *running {
	d.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{t: d.t, stdout: &syncBuffer{}, stderr: &syncBuffer{}, cancel: cancel, done: make(chan int, 1)}
	a := d.app(strings.NewReader(""), r.stdout, r.stderr)
	go func() { r.done <- a.run(ctx, args) }()
	d.t.Cleanup(cancel)
	return r
}

// waitFor waits until the command has printed text.
func (r *running) waitFor(text string) {
	r.t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for !strings.Contains(r.stdout.String(), text) {
		if time.Now().After(deadline) {
			r.t.Fatalf("no %q in the output after %s:\n%s\nstderr: %s", text, waitTimeout, r.stdout, r.stderr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// wait returns the exit code of a command that ends by itself.
func (r *running) wait() int {
	r.t.Helper()
	select {
	case code := <-r.done:
		return code
	case <-time.After(waitTimeout):
		r.t.Fatal("the command did not end")
		return -1
	}
}

// interrupt ends the command as Ctrl-C does and returns its exit code.
func (r *running) interrupt() int {
	r.t.Helper()
	r.cancel()
	return r.wait()
}

func TestListAndStatusOfADaemonWithoutProfiles(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	if got := d.mustRun("", "list").stdout; got != "ID  NAME  KIND  STATE\n" {
		t.Errorf("list = %q", got)
	}
	// A script must find the key even when there is nothing in it.
	var out map[string][]any
	if err := json.Unmarshal([]byte(d.mustRun("", "status", "--json").stdout), &out); err != nil || out["profiles"] == nil || len(out["profiles"]) != 0 {
		t.Errorf("status --json = %v, %v; want an empty profiles list", out, err)
	}
}

func TestProfileLifecycle(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	homeID := d.importText("Home", wgProfile)
	d.importText("work", ovpnProfile)

	list := d.mustRun("", "list").stdout
	for _, want := range []string{homeID, "Home", "WireGuard", "work", "OpenVPN", "disconnected"} {
		if !strings.Contains(list, want) {
			t.Errorf("list lacks %q:\n%s", want, list)
		}
	}

	// A name matches whatever its case.
	r := d.mustRun("", "connect", "hOmE")
	if !strings.HasPrefix(r.stdout, "Home: connected (utun") || !strings.Contains(r.stdout, "10.6.0.2/32)") {
		t.Errorf("connect printed %q", r.stdout)
	}

	status := d.mustRun("", "status").stdout
	for _, want := range []string{"NAME", "UPTIME", "RX", "TX", "ERROR", "Home", "connected", "10.6.0.2/32", "utun"} {
		if !strings.Contains(status, want) {
			t.Errorf("status lacks %q:\n%s", want, status)
		}
	}

	p := d.profile("home")
	if p.State != "PROFILE_STATE_CONNECTED" || !p.DesiredEnabled || p.Status == nil ||
		!strings.HasPrefix(p.Status.InterfaceName, "utun") || p.Status.ConnectedSince == "" {
		t.Errorf("status --json home = %+v", p)
	}
	if got := d.mustRun("", "status", homeID).stdout; strings.Contains(got, "work") || !strings.Contains(got, "Home") {
		t.Errorf("status <id> shows the wrong profiles:\n%s", got)
	}

	// An id works as well as a name.
	if r := d.mustRun("", "disconnect", homeID); r.stdout != "Home: disconnected\n" {
		t.Errorf("disconnect printed %q", r.stdout)
	}
	if p := d.profile("Home"); p.State != "PROFILE_STATE_DISCONNECTED" || p.DesiredEnabled {
		t.Errorf("after disconnect: %+v", p)
	}
}

func TestRemove(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	d.importText("Home", wgProfile)
	d.importText("work", ovpnProfile)
	d.mustRun("", "connect", "work")

	if r := d.mustRun("", "remove", "WORK"); r.stdout != "removed work\n" {
		t.Errorf("remove printed %q", r.stdout)
	}
	list := d.mustRun("", "list").stdout
	if strings.Contains(list, "work") || !strings.Contains(list, "Home") {
		t.Errorf("list after remove:\n%s", list)
	}
	r := d.run("", "remove", "work")
	if r.code != exitFailure || !strings.Contains(r.stderr, `no profile named "work"`) {
		t.Errorf("remove of a removed profile: %+v", r)
	}
}

// The daemon keeps names unique without regard to case, so a name that fits two
// profiles cannot arise from it; see TestAmbiguousNameIsAnErrorForEveryCommand.
func TestNamesMatchWithoutCase(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	d.importText("VPN", wgProfile)
	d.importText("vpn", ovpnProfile) // becomes "vpn 2"

	if r := d.mustRun("", "disconnect", "vpn"); r.stdout != "VPN: disconnected\n" {
		t.Errorf("disconnect vpn: %q", r.stdout)
	}
	if r := d.mustRun("", "disconnect", "VPN 2"); r.stdout != "vpn 2: disconnected\n" {
		t.Errorf("disconnect VPN 2: %q", r.stdout)
	}
}

func TestConnectFailureNamesTheReason(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	d.importText("broken", ovpnProfile+"# fake: fail\n")

	r := d.run("", "connect", "broken")
	if r.code != exitFailure || r.stdout != "" ||
		!strings.Contains(r.stderr, "broken: failed to connect: connection failed: no response from the server (simulated)") {
		t.Errorf("connect: %+v", r)
	}
	if got := d.mustRun("", "status").stdout; !strings.Contains(got, "failed") || !strings.Contains(got, "no response from the server") {
		t.Errorf("status of a failed profile:\n%s", got)
	}
	// Connecting again starts a new attempt, and disconnect ends the profile.
	if r := d.run("", "connect", "broken"); r.code != exitFailure {
		t.Errorf("second connect: %+v", r)
	}
	d.mustRun("", "disconnect", "broken")
	if p := d.profile("broken"); p.State != "PROFILE_STATE_DISCONNECTED" || p.LastError != "" {
		t.Errorf("after disconnect: %+v", p)
	}
}

func TestConnectNoWaitReturnsBeforeTheProfileIsUp(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	d.importText("home", wgProfile)
	if r := d.mustRun("", "connect", "-no-wait", "home"); r.stdout != "home: connecting\n" {
		t.Errorf("connect -no-wait printed %q", r.stdout)
	}
	if p := d.profile("home"); p.State != "PROFILE_STATE_CONNECTING" {
		t.Errorf("state right after connect -no-wait: %s", p.State)
	}
	d.waitState("home", "PROFILE_STATE_CONNECTED")
}

func TestConnectCredentials(t *testing.T) {
	t.Parallel()
	// every case starts its own daemon with a profile that asks for credentials
	type prompted struct {
		label  string
		secret bool
	}
	setup := func(t *testing.T) *testDaemon {
		d := startDaemon(t)
		d.importText("cred", credentialsProfile)
		return d
	}
	stopped := func(t *testing.T, d *testDaemon) {
		t.Helper()
		if p := d.profile("cred"); p.State != "PROFILE_STATE_DISCONNECTED" || p.DesiredEnabled {
			t.Errorf("the profile is still being connected: %+v", p)
		}
	}
	// terminal gives the app a person who answers the prompts in turn.
	terminal := func(answers map[string][]string, asked *[]prompted) func(*app) {
		return func(a *app) {
			a.prompt = func(_ context.Context, label string, secret bool) (string, error) {
				*asked = append(*asked, prompted{label, secret})
				queue := answers[label]
				if len(queue) == 0 {
					return "", errors.New("asked more than expected: " + label)
				}
				answers[label] = queue[1:]
				return queue[0], nil
			}
		}
	}

	t.Run("password on stdin", func(t *testing.T) {
		t.Parallel()
		d := setup(t)
		r := d.run("s3cret\n", "connect", "-username", "alice", "cred")
		if r.code != 0 || !strings.HasPrefix(r.stdout, "cred: connected") {
			t.Errorf("connect: %+v", r)
		}
		if strings.Contains(r.stdout+r.stderr, "s3cret") {
			t.Errorf("the password was printed: %+v", r)
		}
	})

	t.Run("terminal asks for user name and password", func(t *testing.T) {
		t.Parallel()
		d := setup(t)
		var asked []prompted
		answers := map[string][]string{"Username for cred: ": {"alice"}, "Password for cred: ": {"s3cret"}}
		r := d.runWith(context.Background(), "", terminal(answers, &asked), "connect", "cred")
		want := []prompted{{"Username for cred: ", false}, {"Password for cred: ", true}}
		if r.code != 0 || len(asked) != 2 || asked[0] != want[0] || asked[1] != want[1] {
			t.Errorf("connect: %+v, asked %v", r, asked)
		}
	})

	t.Run("terminal asks only for the password after -username", func(t *testing.T) {
		t.Parallel()
		d := setup(t)
		var asked []prompted
		answers := map[string][]string{"Password for cred: ": {"s3cret"}}
		r := d.runWith(context.Background(), "", terminal(answers, &asked), "connect", "-username", "alice", "cred")
		if r.code != 0 || len(asked) != 1 || asked[0] != (prompted{"Password for cred: ", true}) {
			t.Errorf("connect: %+v, asked %v", r, asked)
		}
	})

	t.Run("terminal asks again when the server refuses", func(t *testing.T) {
		t.Parallel()
		d := setup(t)
		var asked []prompted
		// The fake server refuses the password "wrong" once.
		answers := map[string][]string{"Username for cred: ": {"alice"}, "Password for cred: ": {"wrong", "right"}}
		r := d.runWith(context.Background(), "", terminal(answers, &asked), "connect", "cred")
		if r.code != 0 || !strings.Contains(r.stderr, "cred: authentication failed") || len(asked) != 3 {
			t.Errorf("connect: %+v, asked %v", r, asked)
		}
	})

	t.Run("refusal without a terminal", func(t *testing.T) {
		t.Parallel()
		d := setup(t)
		r := d.run("wrong\n", "connect", "-username", "alice", "cred")
		if r.code != exitFailure || !strings.Contains(r.stderr, "cred: authentication failed") {
			t.Errorf("connect: %+v", r)
		}
		stopped(t, d)
	})

	t.Run("no user name without a terminal", func(t *testing.T) {
		t.Parallel()
		d := setup(t)
		r := d.run("s3cret\n", "connect", "cred")
		if r.code != exitFailure || !strings.Contains(r.stderr, "-username") {
			t.Errorf("connect: %+v", r)
		}
		stopped(t, d)
	})

	t.Run("no password on stdin", func(t *testing.T) {
		t.Parallel()
		d := setup(t)
		r := d.run("", "connect", "-username", "alice", "cred")
		if r.code != exitFailure || !strings.Contains(r.stderr, "no password on stdin") {
			t.Errorf("connect: %+v", r)
		}
		stopped(t, d)
	})

	t.Run("environment and flags never carry the password", func(t *testing.T) {
		t.Parallel()
		d := setup(t)
		withPassword := func(a *app) {
			socket := a.getenv
			a.getenv = func(name string) string {
				if strings.Contains(strings.ToLower(name), "pass") {
					return "from-the-environment"
				}
				return socket(name)
			}
		}
		r := d.runWith(context.Background(), "", withPassword, "connect", "-username", "alice", "cred")
		if r.code != exitFailure || !strings.Contains(r.stderr, "no password on stdin") {
			t.Errorf("connect with a password in the environment: %+v", r)
		}
		stopped(t, d)
		for _, flag := range []string{"-password", "--password", "-passphrase"} {
			if r := d.run("pw\n", "connect", flag, "x", "-username", "alice", "cred"); r.code != exitUsage {
				t.Errorf("connect %s: %+v", flag, r)
			}
		}
		if p := d.profile("cred"); p.DesiredEnabled {
			t.Errorf("a refused command line started the profile: %+v", p)
		}
	})

	t.Run("interrupt at the prompt stops the profile", func(t *testing.T) {
		t.Parallel()
		d := setup(t)
		interrupted := func(a *app) {
			a.prompt = func(context.Context, string, bool) (string, error) { return "", errInterrupted }
		}
		r := d.runWith(context.Background(), "", interrupted, "connect", "-username", "alice", "cred")
		if r.code != exitFailure || !strings.Contains(r.stderr, "interrupted") {
			t.Errorf("connect: %+v", r)
		}
		stopped(t, d)
	})

	t.Run("connect again answers a request that is still open", func(t *testing.T) {
		t.Parallel()
		d := setup(t)
		d.mustRun("", "connect", "-no-wait", "cred")
		d.waitState("cred", "PROFILE_STATE_AWAITING_CREDENTIALS")
		if r := d.run("pw\n", "connect", "-username", "alice", "cred"); r.code != 0 || !strings.HasPrefix(r.stdout, "cred: connected") {
			t.Errorf("connect: %+v", r)
		}
	})
}

func TestImport(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	d.importText("probe", wgProfile) // skips the test for a user who may not import
	d.mustRun("", "remove", "probe")
	dir := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("name from the file, warnings on stderr", func(t *testing.T) {
		path := write("Office VPN.ovpn", ovpnProfile+"up /bin/hook\n")
		r := d.run("", "import", path)
		if r.code != 0 || !strings.HasPrefix(r.stdout, "imported Office VPN (id ") ||
			!strings.Contains(r.stderr, "warning: line 4: up: removed") {
			t.Errorf("import: %+v", r)
		}
	})

	t.Run("name from the flag", func(t *testing.T) {
		path := write("x.conf", wgProfile)
		if r := d.run("", "import", path, "-name", "Given"); r.code != 0 || !strings.HasPrefix(r.stdout, "imported Given (id ") {
			t.Errorf("import: %+v", r)
		}
	})

	t.Run("json", func(t *testing.T) {
		path := write("j.ovpn", ovpnProfile+"up /bin/hook\n")
		r := d.run("", "import", "-json", path)
		var out struct {
			Profile  struct{ ID, Name, Kind string }
			Warnings []struct {
				Line      int
				Directive string
			}
		}
		if err := json.Unmarshal([]byte(r.stdout), &out); err != nil || r.code != 0 {
			t.Fatalf("import -json: %+v, %v", r, err)
		}
		if out.Profile.Name != "j" || out.Profile.Kind != "PROFILE_KIND_OPENVPN" || len(out.Warnings) != 1 ||
			out.Warnings[0].Line != 4 || out.Warnings[0].Directive != "up" {
			t.Errorf("import -json = %+v", out)
		}
		if r.stderr != "" {
			t.Errorf("import -json wrote to stderr: %q", r.stderr)
		}
	})

	t.Run("the daemon rejects what it must", func(t *testing.T) {
		path := write("bad.ovpn", ovpnProfile+"# fake: reject\n")
		r := d.run("", "import", path)
		if r.code != exitFailure || !strings.Contains(r.stderr, path+" was rejected: line 4: rejected by") {
			t.Errorf("import: %+v", r)
		}
	})

	t.Run("a file that cannot be a profile is not sent", func(t *testing.T) {
		for name, c := range map[string]struct{ path, want string }{
			"missing":   {filepath.Join(dir, "missing.ovpn"), "no such file"},
			"directory": {dir, "not a regular file"},
			"device":    {"/dev/null", "not a regular file"},
			"too large": {write("huge.ovpn", strings.Repeat("# padding\n", 120_000)), "larger than"},
			"binary":    {write("binary.ovpn", "client\x00\n"), "not a text file"},
		} {
			// Nobody listens on this socket, so the reason in the message is
			// the file's: the command ended before it asked the daemon.
			var stderr syncBuffer
			a := d.app(strings.NewReader(""), &syncBuffer{}, &stderr)
			a.socket = filepath.Join(shortDir(t), "nobody-listens.sock")
			if code := a.run(context.Background(), []string{"import", c.path}); code != exitFailure || !strings.Contains(stderr.String(), c.want) {
				t.Errorf("%s: exit %d, stderr %q, want %q", name, code, stderr.String(), c.want)
			}
		}
	})

	if got := d.mustRun("", "list").stdout; strings.Contains(got, "bad") || strings.Contains(got, "huge") || strings.Contains(got, "binary") {
		t.Errorf("a rejected import left a profile behind:\n%s", got)
	}
}

func TestLogs(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	d.importText("home", wgProfile)
	d.mustRun("", "connect", "home")
	d.mustRun("", "disconnect", "home")

	t.Run("a profile's history", func(t *testing.T) {
		out := d.mustRun("", "logs", "home").stdout
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) != 4 || !strings.Contains(lines[0], "INFO") || !strings.Contains(lines[0], "starting") ||
			!strings.Contains(lines[2], "connected on utun") || !strings.Contains(lines[3], "stopping") {
			t.Errorf("logs home:\n%s", out)
		}
	})

	t.Run("the last lines only", func(t *testing.T) {
		out := d.mustRun("", "logs", "-n", "1", "home").stdout
		if strings.Count(out, "\n") != 1 || !strings.Contains(out, "stopping") {
			t.Errorf("logs -n 1 home:\n%s", out)
		}
		if r := d.run("", "logs", "-n", "0", "home"); r.code != exitUsage {
			t.Errorf("logs -n 0: %+v", r)
		}
	})

	t.Run("the daemon's own log without a profile", func(t *testing.T) {
		if out := d.mustRun("", "logs").stdout; !strings.Contains(out, "listening") {
			t.Errorf("logs:\n%s", out)
		}
	})

	t.Run("json", func(t *testing.T) {
		out := d.mustRun("", "logs", "-json", "home").stdout
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) != 4 {
			t.Fatalf("logs -json: %d lines:\n%s", len(lines), out)
		}
		for _, line := range lines {
			var l struct{ Text, Level, Time string }
			if err := json.Unmarshal([]byte(line), &l); err != nil || l.Text == "" || l.Level != "LOG_LEVEL_INFO" || l.Time == "" {
				t.Errorf("line %q: %+v, %v", line, l, err)
			}
		}
	})

	t.Run("unknown profile", func(t *testing.T) {
		if r := d.run("", "logs", "nosuch"); r.code != exitFailure || !strings.Contains(r.stderr, `no profile named "nosuch"`) {
			t.Errorf("logs nosuch: %+v", r)
		}
	})

	t.Run("follow", func(t *testing.T) {
		follow := d.start("logs", "-f", "-n", "1", "home")
		follow.waitFor("stopping")
		d.mustRun("", "connect", "home")
		follow.waitFor("connected on utun")
		if code := follow.interrupt(); code != 0 {
			t.Errorf("logs -f ended with %d after an interrupt, stderr %q", code, follow.stderr)
		}
	})
}

func TestDiagnostics(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	d.importText("home", wgProfile)
	d.mustRun("", "connect", "home")

	out := d.mustRun("", "diagnostics").stdout
	for _, want := range []string{"daemon", testDaemonVersion, "privileged", "engine", "OpenVPN", "WireGuard", "default v4", "interfaces",
		"Owned routes", "PREFIX", "home", "Journal", "applied"} {
		if !strings.Contains(out, want) {
			t.Errorf("diagnostics lacks %q:\n%s", want, out)
		}
	}

	var parsed struct {
		Daemon      struct{ Version string }
		OwnedRoutes []struct{ Prefix, Owner string }
	}
	if err := json.Unmarshal([]byte(d.mustRun("", "diagnostics", "--json").stdout), &parsed); err != nil ||
		parsed.Daemon.Version != testDaemonVersion || len(parsed.OwnedRoutes) == 0 {
		t.Errorf("diagnostics --json = %+v, %v", parsed, err)
	}
}

// The daemon of these tests is built without a version.
const testDaemonVersion = "0.0.0-dev"

func TestResync(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	if r := d.mustRun("", "resync"); r.stdout != "resynced\n" {
		t.Errorf("resync printed %q", r.stdout)
	}
}

func TestWatchPrintsChangesNotCounters(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	d.importText("home", wgProfile)

	text := d.start("watch")
	text.waitFor("home  disconnected")
	asJSON := d.start("watch", "--json")
	asJSON.waitFor(`"snapshot"`)

	d.mustRun("", "connect", "home")
	text.waitFor("home  connected  utun")
	// The fake daemon moves the counters every second; they are no change.
	time.Sleep(2500 * time.Millisecond)
	d.mustRun("", "disconnect", "home")
	text.waitFor("home  disconnecting")
	d.mustRun("", "remove", "home")
	text.waitFor("home  removed")

	out := text.stdout.String()
	if n := strings.Count(out, "home  connected"); n != 1 {
		t.Errorf("watch printed %d connected lines:\n%s", n, out)
	}
	for _, want := range []string{"home  connecting", "home  disconnecting"} {
		if !strings.Contains(out, want) {
			t.Errorf("watch lacks %q:\n%s", want, out)
		}
	}
	if code := text.interrupt(); code != 0 {
		t.Errorf("watch ended with %d after an interrupt, stderr %q", code, text.stderr)
	}

	asJSON.waitFor(`"removed"`)
	if code := asJSON.interrupt(); code != 0 {
		t.Errorf("watch --json ended with %d after an interrupt", code)
	}
	lines := strings.Split(strings.TrimSpace(asJSON.stdout.String()), "\n")
	var changes int
	for i, line := range lines {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("line %d is not JSON: %q", i, line)
		}
		if (i == 0) != (event["snapshot"] != nil) {
			t.Errorf("line %d: the snapshot is the first event and only the first: %q", i, line)
		}
		if event["changed"] != nil {
			changes++
		}
	}
	if changes < 4 || changes > 6 {
		t.Errorf("watch --json printed %d changes, want about 5 (connecting, connected, disconnecting, disconnected, and the counters not at all):\n%s", changes, asJSON.stdout)
	}
}

func TestWatchEndsWhenTheDaemonStops(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	d.importText("home", wgProfile)
	watch := d.start("watch")
	watch.waitFor("home  disconnected")

	d.stop()
	if code := watch.wait(); code != exitFailure || !strings.Contains(watch.stderr.String(), "the daemon is unavailable") {
		t.Errorf("watch after the daemon stopped: exit %d, stderr %q", code, watch.stderr)
	}
}
