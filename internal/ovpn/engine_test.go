package ovpn

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// requireNoEngineGoroutines fails when a goroutine of the engine, the
// management client or the child's output handling is still alive. The engine
// tests do not run in parallel, so any such goroutine is a leak.
func requireNoEngineGoroutines(t *testing.T) {
	t.Helper()
	var leaked []string
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		buf := make([]byte, 1<<20)
		buf = buf[:runtime.Stack(buf, true)]
		leaked = leaked[:0]
		for _, g := range strings.Split(string(buf), "\n\n") {
			if strings.Contains(g, "ovpn.(*engine)") || strings.Contains(g, "ovpn.(*mgmtConn)") ||
				strings.Contains(g, "ovpn.(*lineSink)") || strings.Contains(g, "os/exec.(*Cmd)") {
				leaked = append(leaked, g)
			}
		}
		if len(leaked) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines leaked:\n%s", strings.Join(leaked, "\n\n"))
		}
	}
}

func lastStatus(h *harness) tunnel.Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.statuses[len(h.statuses)-1]
}

func TestEngineComesUpAndStops(t *testing.T) {
	h := newHarness(t, harnessOpts{realProbe: true})
	h.start()

	up := h.waitFor("Up", h.stateIs(tunnel.StateUp))
	if up.Iface != "utun11" {
		t.Errorf("Iface = %q", up.Iface)
	}
	if want := []netip.Prefix{netip.MustParsePrefix("10.8.0.6/32")}; !reflect.DeepEqual(up.Addresses, want) {
		t.Errorf("Addresses = %v, want %v", up.Addresses, want)
	}
	if up.Remote != "192.0.2.1:1194" {
		t.Errorf("Remote = %q", up.Remote)
	}
	if up.Since.IsZero() {
		t.Error("Since is not set")
	}
	h.waitFor("byte counts", func(s tunnel.Status) bool { return s.Stats == tunnel.Stats{RxBytes: 1024, TxBytes: 2048} })

	// The Reconciler first hears the endpoints, then the tunnel.
	calls := h.net.snapshot()
	if len(calls) != 2 {
		t.Fatalf("Announce calls = %+v, want 2", calls)
	}
	wantConnecting := tunnel.Intent{
		Owner: "office", State: tunnel.StateConnecting,
		Endpoints: []netip.Addr{netip.MustParseAddr("192.0.2.1")},
	}
	if !reflect.DeepEqual(calls[0].intent, wantConnecting) {
		t.Errorf("first intent = %+v, want %+v", calls[0].intent, wantConnecting)
	}
	gotUp := calls[1].intent
	wantUp := tunnel.Intent{
		Owner: "office", State: tunnel.StateUp, Iface: "utun11", Role: tunnel.RoleSplit,
		UpSince:   gotUp.UpSince,
		Endpoints: []netip.Addr{netip.MustParseAddr("192.0.2.1")},
		Routes:    []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")},
	}
	if !reflect.DeepEqual(gotUp, wantUp) || gotUp.UpSince.IsZero() {
		t.Errorf("up intent = %+v, want %+v", gotUp, wantUp)
	}

	// What openvpn was started with.
	argv := strings.Split(strings.TrimSpace(h.readRecording("argv")), "\n")
	for _, flag := range []string{"--management-hold", "--management-query-passwords", "--management-up-down", "--route-noexec", "--persist-tun", "--disable-dco"} {
		found := false
		for _, a := range argv {
			found = found || a == flag
		}
		if !found {
			t.Errorf("argv lacks %s: %v", flag, argv)
		}
	}
	if got := h.readRecording("config-mode"); strings.TrimSpace(got) != "600" {
		t.Errorf("config mode = %q, want 600", got)
	}
	if got := h.readRecording("dir-mode"); strings.TrimSpace(got) != "700" {
		t.Errorf("workspace mode = %q, want 700", got)
	}
	if got := h.readRecording("config.copy"); got != asusLikeProfileCanonical(t) {
		t.Errorf("openvpn was given\n%s\nwant\n%s", got, asusLikeProfileCanonical(t))
	}
	cmds := h.readRecording("commands")
	for _, want := range []string{"state on", "bytecount 2", "log on", "hold release"} {
		if !strings.Contains(cmds, want+"\n") {
			t.Errorf("openvpn never received %q; got:\n%s", want, cmds)
		}
	}
	if !strings.Contains(h.logText(), "TCP connection established") {
		t.Errorf("openvpn's >LOG lines are not forwarded: %q", h.logText())
	}

	h.stop()
	if last := lastStatus(h); last.State != tunnel.StateDisconnected {
		t.Errorf("final status = %+v, want Disconnected", last)
	}
	calls = h.net.snapshot()
	if last := calls[len(calls)-1]; !last.withdraw || last.intent.Owner != "office" {
		t.Errorf("the last call to the Reconciler = %+v, want Withdraw(office)", last)
	}
	if !strings.Contains(h.readRecording("commands"), "signal SIGTERM") {
		t.Error("Stop did not ask openvpn to exit with signal SIGTERM")
	}
	h.requireProcessGone()
	h.requireWorkspaceGone()
	requireNoEngineGoroutines(t)
}

func asusLikeProfileCanonical(t *testing.T) string {
	t.Helper()
	p, err := Parse([]byte(asusLikeProfile))
	if err != nil {
		t.Fatal(err)
	}
	return string(p.Content)
}

func TestEngineWaitsForAnnounceBeforeSpawning(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	release := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	h.net.announceErr = func(n int, it tunnel.Intent) error {
		if n == 1 {
			once.Do(func() { close(entered) })
			<-release
		}
		return nil
	}
	h.start()
	<-entered
	time.Sleep(400 * time.Millisecond)
	if pid := h.readRecording("pid"); pid != "" {
		t.Fatalf("openvpn was started (pid %s) while the endpoints were still being announced", pid)
	}
	if s := lastStatus(h); s.State != tunnel.StateConnecting {
		t.Errorf("state while announcing = %v", s.State)
	}
	close(release)
	h.waitFor("Up", h.stateIs(tunnel.StateUp))
	h.stop()
	requireNoEngineGoroutines(t)
}

func TestEngineAnnounceFailureFailsWithoutSpawning(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.net.announceErr = func(n int, it tunnel.Intent) error { return errors.New("route table is locked") }
	h.start()
	failed := h.waitFor("Failed", h.stateIs(tunnel.StateFailed))
	if !strings.Contains(failed.Err, "route table is locked") || !strings.Contains(failed.Err, "announce endpoints") {
		t.Errorf("Err = %q", failed.Err)
	}
	select {
	case <-h.collect:
	case <-time.After(5 * time.Second):
		t.Fatal("the Status channel stayed open after the failure")
	}
	if h.readRecording("pid") != "" {
		t.Error("openvpn was spawned although the endpoints could not be announced")
	}
	calls := h.net.snapshot()
	if last := calls[len(calls)-1]; !last.withdraw {
		t.Errorf("a failed engine must withdraw its owner; calls = %+v", calls)
	}
	h.requireWorkspaceGone()
	h.stop() // idempotent after a failure
	requireNoEngineGoroutines(t)
}

func TestEngineAnnounceUpFailureStopsOpenVPN(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.net.announceErr = func(n int, it tunnel.Intent) error {
		if it.State == tunnel.StateUp {
			return errors.New("cannot bind routes")
		}
		return nil
	}
	h.start()
	failed := h.waitFor("Failed", h.stateIs(tunnel.StateFailed))
	if !strings.Contains(failed.Err, "announce routes") || !strings.Contains(failed.Err, "cannot bind routes") {
		t.Errorf("Err = %q", failed.Err)
	}
	<-h.collect
	h.requireProcessGone()
	h.requireWorkspaceGone()
	requireNoEngineGoroutines(t)
}

func TestEngineResolvesEveryHostBeforeAnnouncing(t *testing.T) {
	profile := "client\nremote a.example 1194\nremote b.example 443 udp\nremote 192.0.2.77 1194\nremote broken.example 1194\nhttp-proxy proxy.example 8080\n<ca>\nPEM\n</ca>\n"
	h := newHarness(t, harnessOpts{
		profile: profile,
		cfgMod: func(e *engine) {
			e.lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
				switch host {
				case "a.example":
					return []netip.Addr{netip.MustParseAddr("198.51.100.1"), netip.MustParseAddr("2001:db8::1")}, nil
				case "b.example":
					return []netip.Addr{netip.MustParseAddr("::ffff:198.51.100.2"), netip.MustParseAddr("198.51.100.1")}, nil
				case "proxy.example":
					return []netip.Addr{netip.MustParseAddr("203.0.113.8"), netip.MustParseAddr("0.0.0.0")}, nil
				}
				return nil, errors.New("no such host")
			}
		},
	})
	h.start()
	h.waitFor("Up", h.stateIs(tunnel.StateUp))
	first := h.net.snapshot()[0].intent
	want := []netip.Addr{
		netip.MustParseAddr("198.51.100.1"), netip.MustParseAddr("2001:db8::1"),
		netip.MustParseAddr("198.51.100.2"), netip.MustParseAddr("192.0.2.77"),
		netip.MustParseAddr("203.0.113.8"),
	}
	if first.State != tunnel.StateConnecting || !reflect.DeepEqual(first.Endpoints, want) {
		t.Errorf("first intent = %+v\nwant endpoints %v", first, want)
	}
	if !strings.Contains(h.logText(), "cannot resolve broken.example") {
		t.Errorf("the unresolved host was not reported: %q", h.logText())
	}
	h.stop()
}

// A server name that does not resolve, as at boot before the network is up, is
// no reason to give up: the engine keeps connecting, says why after a while,
// and carries on when the name resolves.
func TestEngineKeepsTryingWhenTheServerNameDoesNotResolve(t *testing.T) {
	var (
		attempts atomic.Int32
		resolves atomic.Bool
	)
	h := newHarness(t, harnessOpts{
		profile: "client\nremote vpn.example.test 1194\n<ca>\nPEM\n</ca>\n",
		cfgMod: func(e *engine) {
			e.retryWait = 10 * time.Millisecond
			e.stuckGrace = 200 * time.Millisecond
			e.lookup = func(context.Context, string) ([]netip.Addr, error) {
				attempts.Add(1)
				if !resolves.Load() {
					return nil, errors.New("no such host")
				}
				return addrs("192.0.2.5"), nil
			}
		},
	})
	h.start()

	stuck := h.waitFor("the reason", func(s tunnel.Status) bool { return s.Err != "" })
	if stuck.State != tunnel.StateConnecting || stuck.Err != "cannot resolve vpn.example.test: no such host" {
		t.Errorf("status = %+v, want Connecting with the reason", stuck)
	}
	h.mu.Lock()
	first := h.statuses[0]
	h.mu.Unlock()
	if first.Err != "" {
		t.Errorf("the reason was shown before the grace was over: %+v", first)
	}
	if n := attempts.Load(); n < 2 {
		t.Errorf("the name was looked up %d times, want it tried again", n)
	}
	if h.readRecording("pid") != "" {
		t.Error("openvpn was started without any endpoint")
	}
	for _, c := range h.net.snapshot() {
		t.Errorf("the Reconciler was told something although nothing resolved: %+v", c)
	}

	resolves.Store(true)
	up := h.waitFor("Up", h.stateIs(tunnel.StateUp))
	if up.Err != "" {
		t.Errorf("Err after connecting = %q", up.Err)
	}
	if h.seen(h.stateIs(tunnel.StateFailed)) {
		t.Error("the engine failed for a name that did not resolve")
	}
	if first := h.net.snapshot()[0].intent; !reflect.DeepEqual(first.Endpoints, addrs("192.0.2.5")) {
		t.Errorf("first intent = %+v", first)
	}
	h.stop()
	requireNoEngineGoroutines(t)
}

// The wait between two lookups grows to a limit; a network change ends it.
func TestEngineRebindEndsTheWaitForTheServerName(t *testing.T) {
	var attempts atomic.Int32
	h := newHarness(t, harnessOpts{
		profile: "client\nremote vpn.example.test 1194\n<ca>\nPEM\n</ca>\n",
		cfgMod: func(e *engine) {
			e.retryWait = time.Hour
			e.lookup = func(context.Context, string) ([]netip.Addr, error) {
				if attempts.Add(1) < 3 {
					return nil, errors.New("no such host")
				}
				return addrs("192.0.2.5"), nil
			}
		},
	})
	h.start()
	for deadline := time.Now().Add(5 * time.Second); attempts.Load() < 1; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the name was never looked up")
		}
	}
	h.eng.Rebind()
	for deadline := time.Now().Add(5 * time.Second); attempts.Load() < 2; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("a network change did not end the wait for the next lookup")
		}
	}
	h.eng.Rebind()
	h.waitFor("Up", h.stateIs(tunnel.StateUp))
	h.stop()
	requireNoEngineGoroutines(t)
}

func TestEngineStopDuringTheWaitForTheServerName(t *testing.T) {
	h := newHarness(t, harnessOpts{
		profile: "client\nremote vpn.example.test 1194\n<ca>\nPEM\n</ca>\n",
		cfgMod: func(e *engine) {
			e.retryWait = time.Hour
			e.lookup = func(context.Context, string) ([]netip.Addr, error) { return nil, errors.New("no such host") }
		},
	})
	h.start()
	h.waitFor("a status", func(tunnel.Status) bool { return true })
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	h.stop()
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Stop took %v during the wait", d)
	}
	if last := lastStatus(h); last.State != tunnel.StateDisconnected {
		t.Errorf("final status = %+v", last)
	}
	requireNoEngineGoroutines(t)
}

func TestEngineStopDuringResolution(t *testing.T) {
	started := make(chan struct{})
	h := newHarness(t, harnessOpts{
		profile: "client\nremote slow.example 1194\n<ca>\nPEM\n</ca>\n",
		cfgMod: func(e *engine) {
			e.lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
				close(started)
				<-ctx.Done()
				return nil, ctx.Err()
			}
		},
	})
	h.start()
	<-started
	h.stop()
	if last := lastStatus(h); last.State != tunnel.StateDisconnected {
		t.Errorf("final status = %+v", last)
	}
	if h.readRecording("pid") != "" {
		t.Error("openvpn was started after Stop")
	}
	requireNoEngineGoroutines(t)
}

func TestEngineCredentialFlow(t *testing.T) {
	const user, pass = `ali"ce`, `p"a\ss w0rd;#'`
	h := newHarness(t, harnessOpts{env: map[string]string{"OVPN_FAKE_PASSWORD": pass}})
	h.start()

	waiting := h.waitFor("AwaitingCredentials", h.stateIs(tunnel.StateAwaitingCredentials))
	if waiting.NeedsCredentials != tunnel.CredentialUserPassword {
		t.Errorf("NeedsCredentials = %v", waiting.NeedsCredentials)
	}
	if got := h.net.snapshot(); len(got) != 1 {
		t.Errorf("the tunnel was announced before credentials: %+v", got)
	}

	if err := h.eng.ProvideCredentials(tunnel.CredentialUserPassword, user, pass); err != nil {
		t.Fatal(err)
	}
	h.waitFor("Up", h.stateIs(tunnel.StateUp))

	// The management protocol quoting, exactly.
	cmds := h.readRecording("commands")
	for _, want := range []string{`username "Auth" "ali\"ce"`, `password "Auth" "p\"a\\ss w0rd;#'"`} {
		if !strings.Contains(cmds, want+"\n") {
			t.Errorf("openvpn never received %s; got:\n%s", want, cmds)
		}
	}
	if s := lastStatus(h); s.NeedsCredentials != tunnel.CredentialNone || s.Err != "" {
		t.Errorf("status after authenticating = %+v", s)
	}
	h.stop()
	requireNoEngineGoroutines(t)
}

func TestEngineCredentialsProvidedBeforeThePrompt(t *testing.T) {
	h := newHarness(t, harnessOpts{env: map[string]string{"OVPN_FAKE_PASSWORD": "early"}})
	if err := h.eng.ProvideCredentials(tunnel.CredentialUserPassword, "bob", "early"); err != nil {
		t.Fatal(err)
	}
	h.start()
	h.waitFor("Up", h.stateIs(tunnel.StateUp))
	if h.seen(h.stateIs(tunnel.StateAwaitingCredentials)) {
		t.Error("the engine asked for credentials it already had")
	}
	h.stop()
}

func TestEngineAuthFailureAsksAgain(t *testing.T) {
	h := newHarness(t, harnessOpts{env: map[string]string{"OVPN_FAKE_PASSWORD": "right"}})
	h.start()
	h.waitFor("AwaitingCredentials", h.stateIs(tunnel.StateAwaitingCredentials))

	if err := h.eng.ProvideCredentials(tunnel.CredentialUserPassword, "carol", "wrong"); err != nil {
		t.Fatal(err)
	}
	rejected := h.waitFor("a rejected password", func(s tunnel.Status) bool {
		return s.State == tunnel.StateAwaitingCredentials && s.Err == "authentication failed"
	})
	if rejected.NeedsCredentials != tunnel.CredentialUserPassword {
		t.Errorf("NeedsCredentials = %v", rejected.NeedsCredentials)
	}
	// Give the engine the chance to answer the next prompt with the rejected
	// password; it must not.
	time.Sleep(300 * time.Millisecond)
	if n := strings.Count(h.readRecording("commands"), "password \"Auth\""); n != 1 {
		t.Fatalf("the rejected password was sent %d times", n)
	}
	if s := lastStatus(h); s.State != tunnel.StateAwaitingCredentials {
		t.Fatalf("state after a rejection = %v, want AwaitingCredentials", s.State)
	}

	if err := h.eng.ProvideCredentials(tunnel.CredentialUserPassword, "carol", "right"); err != nil {
		t.Fatal(err)
	}
	up := h.waitFor("Up", h.stateIs(tunnel.StateUp))
	if up.Err != "" {
		t.Errorf("Err after a successful retry = %q", up.Err)
	}
	h.stop()
	requireNoEngineGoroutines(t)
}

func TestEngineKeyPassphrase(t *testing.T) {
	h := newHarness(t, harnessOpts{env: map[string]string{
		"OVPN_FAKE_PASSWORD": "open sesame",
		"OVPN_FAKE_PROMPT":   ">PASSWORD:Need 'Private Key' password",
	}})
	h.start()
	waiting := h.waitFor("AwaitingCredentials", h.stateIs(tunnel.StateAwaitingCredentials))
	if waiting.NeedsCredentials != tunnel.CredentialKeyPassphrase {
		t.Errorf("NeedsCredentials = %v", waiting.NeedsCredentials)
	}
	if err := h.eng.ProvideCredentials(tunnel.CredentialKeyPassphrase, "", "open sesame"); err != nil {
		t.Fatal(err)
	}
	h.waitFor("Up", h.stateIs(tunnel.StateUp))
	cmds := h.readRecording("commands")
	if !strings.Contains(cmds, `password "Private Key" "open sesame"`+"\n") || strings.Contains(cmds, "username") {
		t.Errorf("commands:\n%s", cmds)
	}
	h.stop()
}

func TestEngineRefusesCredentialsItCannotSupply(t *testing.T) {
	for name, prompt := range map[string]string{
		"static challenge": ">PASSWORD:Need 'Auth' username/password SC:1,Enter the code",
		"http proxy":       ">PASSWORD:Need 'HTTP Proxy' username/password",
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, harnessOpts{env: map[string]string{"OVPN_FAKE_PASSWORD": "x", "OVPN_FAKE_PROMPT": prompt}})
			h.start()
			failed := h.waitFor("Failed", h.stateIs(tunnel.StateFailed))
			if !strings.Contains(failed.Err, "cannot supply") {
				t.Errorf("Err = %q", failed.Err)
			}
			<-h.collect
			h.requireProcessGone()
			requireNoEngineGoroutines(t)
		})
	}
}

func TestProvideCredentialsValidates(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	tests := []struct {
		name       string
		kind       tunnel.CredentialKind
		user, pass string
	}{
		{"empty username", tunnel.CredentialUserPassword, "", "pw"},
		{"username with a line break", tunnel.CredentialUserPassword, "a\nsignal SIGTERM", "pw"},
		{"password with a line break", tunnel.CredentialUserPassword, "a", "p\nw"},
		{"password with a carriage return", tunnel.CredentialUserPassword, "a", "p\rw"},
		{"passphrase with a NUL", tunnel.CredentialKeyPassphrase, "", "p\x00w"},
		{"unknown kind", tunnel.CredentialNone, "a", "b"},
	}
	for _, tt := range tests {
		if err := h.eng.ProvideCredentials(tt.kind, tt.user, tt.pass); err == nil {
			t.Errorf("%s: ProvideCredentials succeeded", tt.name)
		}
	}
	// Nothing invalid was stored.
	if h.eng.userPass.set || h.eng.keyPass.set || len(h.eng.secrets) != 0 {
		t.Error("a rejected credential was stored")
	}
}

func TestEngineKeepsCredentialsAcrossReconnects(t *testing.T) {
	h := newHarness(t, harnessOpts{env: map[string]string{
		"OVPN_FAKE_PASSWORD": "pw", "OVPN_FAKE_REAUTH": "1",
	}})
	h.start()
	h.waitFor("AwaitingCredentials", h.stateIs(tunnel.StateAwaitingCredentials))
	if err := h.eng.ProvideCredentials(tunnel.CredentialUserPassword, "dave", "pw"); err != nil {
		t.Fatal(err)
	}
	h.waitFor("Up", h.stateIs(tunnel.StateUp))

	// A network change: the Reconciler repaired the routes and calls Rebind.
	before := len(h.net.snapshot())
	h.eng.Rebind()
	h.waitForNew("Reconnecting", h.stateIs(tunnel.StateReconnecting))
	h.waitForNew("Up again", h.stateIs(tunnel.StateUp))

	cmds := h.readRecording("commands")
	if !strings.Contains(cmds, "signal SIGUSR1\n") {
		t.Errorf("Rebind did not send signal SIGUSR1:\n%s", cmds)
	}
	if n := strings.Count(cmds, `password "Auth" "pw"`); n != 2 {
		t.Errorf("the password was sent %d times, want 2 (once per authentication):\n%s", n, cmds)
	}
	if n := strings.Count(cmds, `username "Auth" "dave"`); n != 2 {
		t.Errorf("the username was sent %d times, want 2", n)
	}
	// persist-tun: the interface and its routes were never torn down.
	if after := h.net.snapshot(); len(after) != before {
		t.Errorf("the engine talked to the Reconciler during a reconnect: %+v", after[before:])
	}
	// The user was not asked again after the first time.
	h.mu.Lock()
	asked := 0
	for _, s := range h.statuses {
		if s.State == tunnel.StateAwaitingCredentials {
			asked++
		}
	}
	h.mu.Unlock()
	if asked == 0 {
		t.Fatal("the first prompt was never reported")
	}
	idxUp := -1
	h.mu.Lock()
	for i, s := range h.statuses {
		if s.State == tunnel.StateUp && idxUp < 0 {
			idxUp = i
		}
	}
	for _, s := range h.statuses[idxUp:] {
		if s.State == tunnel.StateAwaitingCredentials {
			t.Errorf("asked for credentials again after connecting: %+v", s)
		}
	}
	h.mu.Unlock()
	h.stop()
	requireNoEngineGoroutines(t)
}

func TestEngineRebindBeforeConnectingIsHarmless(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.eng.Rebind() // before Start
	h.start()
	h.waitFor("Up", h.stateIs(tunnel.StateUp))
	if strings.Contains(h.readRecording("commands"), "SIGUSR1") {
		t.Error("a Rebind that predates openvpn was delivered to it")
	}
	h.stop()
}

func TestEngineUnexpectedExit(t *testing.T) {
	h := newHarness(t, harnessOpts{env: map[string]string{"OVPN_FAKE_CRASH": "after-up"}})
	h.start()
	h.waitFor("Up", h.stateIs(tunnel.StateUp))
	failed := h.waitFor("Failed", h.stateIs(tunnel.StateFailed))
	for _, want := range []string{"exit status 3", "Cannot allocate TUN/TAP dev dynamically", "ping-restart"} {
		if !strings.Contains(failed.Err, want) {
			t.Errorf("Err %q lacks %q", failed.Err, want)
		}
	}
	select {
	case <-h.collect:
	case <-time.After(5 * time.Second):
		t.Fatal("the Status channel stayed open")
	}
	calls := h.net.snapshot()
	if last := calls[len(calls)-1]; !last.withdraw {
		t.Errorf("the owner was not withdrawn after the crash: %+v", calls)
	}
	h.requireWorkspaceGone()
	h.requireProcessGone()
	h.stop()
	requireNoEngineGoroutines(t)
}

func TestEngineStartupFailureReportsOpenVPNsOutput(t *testing.T) {
	h := newHarness(t, harnessOpts{env: map[string]string{"OVPN_FAKE_CRASH": "early"}})
	h.start()
	failed := h.waitFor("Failed", h.stateIs(tunnel.StateFailed))
	for _, want := range []string{"exit status 1", "Options error", "Use --help"} {
		if !strings.Contains(failed.Err, want) {
			t.Errorf("Err %q lacks %q", failed.Err, want)
		}
	}
	<-h.collect
	h.requireWorkspaceGone()
	requireNoEngineGoroutines(t)
}

func TestEngineNoManagementSocket(t *testing.T) {
	h := newHarness(t, harnessOpts{
		env:    map[string]string{"OVPN_FAKE_NOMGMT": "1"},
		cfgMod: func(e *engine) { e.dialTimeout = 300 * time.Millisecond },
	})
	h.start()
	failed := h.waitFor("Failed", h.stateIs(tunnel.StateFailed))
	if !strings.Contains(failed.Err, "management socket") {
		t.Errorf("Err = %q", failed.Err)
	}
	<-h.collect
	h.requireProcessGone()
	h.requireWorkspaceGone()
	requireNoEngineGoroutines(t)
}

func TestEngineConnectedWithoutTunnelReport(t *testing.T) {
	h := newHarness(t, harnessOpts{
		env:    map[string]string{"OVPN_FAKE_NOUPDOWN": "1"},
		cfgMod: func(e *engine) { e.upReportGrace = 300 * time.Millisecond },
	})
	h.start()
	failed := h.waitFor("Failed", h.stateIs(tunnel.StateFailed))
	if !strings.Contains(failed.Err, "did not report its tunnel interface") {
		t.Errorf("Err = %q", failed.Err)
	}
	if h.seen(h.stateIs(tunnel.StateUp)) {
		t.Error("the engine claimed Up without an interface")
	}
	<-h.collect
	h.requireProcessGone()
	requireNoEngineGoroutines(t)
}

func TestEngineKillsAChildThatIgnoresSIGTERM(t *testing.T) {
	h := newHarness(t, harnessOpts{
		env:    map[string]string{"OVPN_FAKE_IGNORE_TERM": "1"},
		cfgMod: func(e *engine) { e.stopGrace = 400 * time.Millisecond },
	})
	h.start()
	h.waitFor("Up", h.stateIs(tunnel.StateUp))
	start := time.Now()
	h.stop()
	if d := time.Since(start); d < 300*time.Millisecond || d > 5*time.Second {
		t.Errorf("Stop took %v; it should wait out the grace period and then kill", d)
	}
	h.requireProcessGone()
	h.requireWorkspaceGone()
	requireNoEngineGoroutines(t)
}

func TestEngineStopWithExpiredContextKillsAtOnce(t *testing.T) {
	h := newHarness(t, harnessOpts{
		env:    map[string]string{"OVPN_FAKE_IGNORE_TERM": "1"},
		cfgMod: func(e *engine) { e.stopGrace = time.Minute },
	})
	h.start()
	h.waitFor("Up", h.stateIs(tunnel.StateUp))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := h.eng.Stop(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Stop = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Stop took %v with an expired context", d)
	}
	h.requireProcessGone()
	<-h.collect
	h.requireWorkspaceGone()
	requireNoEngineGoroutines(t)
}

func TestEngineStopIsIdempotentAndConcurrencySafe(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.start()
	h.waitFor("Up", h.stateIs(tunnel.StateUp))
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := contextWithTimeout(15 * time.Second)
			defer cancel()
			if err := h.eng.Stop(ctx); err != nil {
				t.Errorf("Stop: %v", err)
			}
		}()
	}
	wg.Wait()
	withdraws := 0
	for _, c := range h.net.snapshot() {
		if c.withdraw {
			withdraws++
		}
	}
	if withdraws != 1 {
		t.Errorf("Withdraw was called %d times, want once", withdraws)
	}
	if err := h.eng.ProvideCredentials(tunnel.CredentialUserPassword, "a", "b"); err == nil {
		t.Error("ProvideCredentials succeeded after Stop")
	}
	h.eng.Rebind() // must not block or panic after Stop
	requireNoEngineGoroutines(t)
}

func TestEngineStopBeforeStart(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.stop() // closes the channel, nothing to stop
	ctx, cancel := contextWithTimeout(time.Second)
	defer cancel()
	if err := h.eng.Start(ctx); err == nil {
		t.Error("Start after Stop succeeded")
	}
	if h.readRecording("pid") != "" {
		t.Error("openvpn ran")
	}
}

func TestEngineStartErrors(t *testing.T) {
	t.Run("binary missing", func(t *testing.T) {
		b := Backend(Config{Binary: "/nonexistent/openvpn", RunDir: shortTempDir(t)})
		eng, err := b.New(tunnel.Spec{Owner: "o", Content: []byte(minimalProfile)}, tunnel.Deps{Network: &fakeNetwork{}})
		if err != nil {
			t.Fatal(err)
		}
		err = eng.Start(context.Background())
		if err == nil || !strings.Contains(err.Error(), "openvpn not found at /nonexistent/openvpn") {
			t.Fatalf("Start = %v", err)
		}
	})
	t.Run("LZO profile on a binary without LZO", func(t *testing.T) {
		h := newHarness(t, harnessOpts{realProbe: true, env: map[string]string{"OVPN_FAKE_VERSION": "OpenVPN 2.7.7 x [LZ4]"}})
		err := h.eng.Start(context.Background())
		if err == nil || !strings.Contains(err.Error(), "LZO") {
			t.Fatalf("Start = %v", err)
		}
		if h.readRecording("pid") != "" {
			t.Error("openvpn ran")
		}
	})
	t.Run("started twice", func(t *testing.T) {
		h := newHarness(t, harnessOpts{})
		h.start()
		if err := h.eng.Start(context.Background()); err == nil {
			t.Error("a second Start succeeded")
		}
		h.stop()
	})
	t.Run("cancelled context", func(t *testing.T) {
		h := newHarness(t, harnessOpts{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := h.eng.Start(ctx); !errors.Is(err, context.Canceled) {
			t.Errorf("Start = %v", err)
		}
	})
	t.Run("workspace cannot be created", func(t *testing.T) {
		h := newHarness(t, harnessOpts{})
		blocker := filepath.Join(shortTempDir(t), "file")
		if err := os.WriteFile(blocker, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		h.eng.cfg.RunDir = filepath.Join(blocker, "sub") // a file in the way
		h.eng.dir = filepath.Join(h.eng.cfg.RunDir, "x")
		if err := h.eng.Start(context.Background()); err == nil {
			t.Fatal("Start succeeded")
		}
		select {
		case <-h.collect:
		case <-time.After(5 * time.Second):
			t.Fatal("the Status channel stayed open after a failed Start")
		}
		h.stop()
	})
}

func TestEngineLogsNeverContainCredentials(t *testing.T) {
	const secret = "s3cr3tPassw0rd"
	h := newHarness(t, harnessOpts{env: map[string]string{
		"OVPN_FAKE_PASSWORD":     secret,
		"OVPN_FAKE_ECHO_SECRETS": "1",
		"OVPN_FAKE_CRASH":        "after-up",
	}})
	h.start()
	h.waitFor("AwaitingCredentials", h.stateIs(tunnel.StateAwaitingCredentials))
	if err := h.eng.ProvideCredentials(tunnel.CredentialUserPassword, "erin", secret); err != nil {
		t.Fatal(err)
	}
	h.waitFor("Failed", h.stateIs(tunnel.StateFailed))
	<-h.collect

	if logs := h.logText(); strings.Contains(logs, secret) {
		t.Errorf("the password reached the log:\n%s", logs)
	} else if !strings.Contains(logs, "credential command") {
		t.Errorf("the echoed credential command should be replaced, got:\n%s", logs)
	}
	h.mu.Lock()
	for _, s := range h.statuses {
		if strings.Contains(s.Err, secret) {
			t.Errorf("the password reached a status: %+v", s)
		}
	}
	h.mu.Unlock()
	// The fake did receive it (so the test is not vacuous) but only through
	// the management socket, never on the command line or in the profile.
	if !strings.Contains(h.readRecording("commands"), secret) {
		t.Error("the fake never saw the password; the test proves nothing")
	}
	if strings.Contains(h.readRecording("argv"), secret) || strings.Contains(h.readRecording("config.copy"), secret) {
		t.Error("the password was written to the command line or the profile")
	}
}

func TestEngineForwardsChildOutputOnlyBeforeManagement(t *testing.T) {
	h := newHarness(t, harnessOpts{env: map[string]string{"OVPN_FAKE_STDOUT": "1"}})
	h.start()
	h.waitFor("Up", h.stateIs(tunnel.StateUp))
	logs := h.logText()
	// Before the management connection exists, stdout and stderr are the only
	// log; afterwards the same lines arrive as >LOG with their level.
	if !strings.Contains(logs, "OpenVPN 2.7.7 starting") || !strings.Contains(logs, "a warning on stderr before management") {
		t.Errorf("start-up output was not forwarded:\n%s", logs)
	}
	if strings.Contains(logs, "line on stdout after management") {
		t.Errorf("output that management also delivers was forwarded twice:\n%s", logs)
	}
	h.stop()
	// But everything is kept for failure reports.
	if tail := h.eng.ring.tail(10); !strings.Contains(strings.Join(tail, "\n"), "line on stdout after management") {
		t.Errorf("ring = %v", tail)
	}
}

func TestPublishNeverBlocksAndLatestWins(t *testing.T) {
	b := Backend(Config{Binary: "/x", RunDir: shortTempDir(t)})
	eng, err := b.New(tunnel.Spec{Owner: "o", Content: []byte(minimalProfile)}, tunnel.Deps{Network: &fakeNetwork{}})
	if err != nil {
		t.Fatal(err)
	}
	e := eng.(*engine)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			n := i
			e.update(func(s *tunnel.Status) { s.Stats = tunnel.Stats{RxBytes: uint64(n)} })
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publishing blocked on a reader that is not reading")
	}
	if got := (<-e.Status()).Stats.RxBytes; got != 99 {
		t.Errorf("the reader got snapshot %d, want the latest (99)", got)
	}
}

func TestEngineReportsProfileWarnings(t *testing.T) {
	profile := asusLikeProfile + "up /bin/sh\nscript-security 2\n"
	h := newHarness(t, harnessOpts{profile: profile})
	h.start()
	s := h.waitFor("Up", h.stateIs(tunnel.StateUp))
	if len(s.Warnings) != 2 || !strings.Contains(s.Warnings[0], "up") || !strings.Contains(s.Warnings[1], "script-security") {
		t.Errorf("Warnings = %v", s.Warnings)
	}
	// The stripped directives never reached openvpn.
	if cfg := h.readRecording("config.copy"); strings.Contains(cfg, "/bin/sh") || strings.Contains(cfg, "script-security") {
		t.Errorf("openvpn was given the dangerous directives:\n%s", cfg)
	}
	h.stop()
}

func TestEngineModes(t *testing.T) {
	tests := []struct {
		name   string
		mode   tunnel.Mode
		env    string
		role   tunnel.Role
		routes []string
	}{
		{"auto split", tunnel.ModeAuto, defaultUpdownEnv, tunnel.RoleSplit, []string{"192.168.1.0/24"}},
		{"force full", tunnel.ModeFull, defaultUpdownEnv, tunnel.RoleFull, []string{"192.168.1.0/24", "0.0.0.0/0", "::/0"}},
		{"auto follows a pushed redirect", tunnel.ModeAuto, defaultUpdownEnv + `\nroute_redirect_gateway_ipv4=1`, tunnel.RoleFull, []string{"192.168.1.0/24", "0.0.0.0/0", "::/0"}},
		{"force split drops a pushed redirect", tunnel.ModeSplit, defaultUpdownEnv + `\nroute_redirect_gateway_ipv4=1`, tunnel.RoleSplit, []string{"192.168.1.0/24"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, harnessOpts{
				env:  map[string]string{"OVPN_FAKE_UPDOWN": strings.ReplaceAll(tt.env, "\n", `\n`)},
				spec: tunnel.Spec{Owner: "m", Mode: tt.mode, Priority: 7},
			})
			h.start()
			h.waitFor("Up", h.stateIs(tunnel.StateUp))
			var up tunnel.Intent
			for _, c := range h.net.snapshot() {
				if c.intent.State == tunnel.StateUp {
					up = c.intent
				}
			}
			if up.Role != tt.role || up.Priority != 7 || !reflect.DeepEqual(up.Routes, mustPrefixes(tt.routes...)) {
				t.Errorf("intent = %+v, want role %v, routes %v", up, tt.role, tt.routes)
			}
			h.stop()
		})
	}
}

func TestEngineRebindReannouncesChangedServerAddresses(t *testing.T) {
	var (
		answer atomic.Pointer[[]netip.Addr]
		fail   atomic.Bool
	)
	set := func(ss ...string) { a := addrs(ss...); answer.Store(&a) }
	set("192.0.2.1")

	var h *harness
	var sigusr1BeforeAnnounce atomic.Bool
	h = newHarness(t, harnessOpts{
		profile: "client\nremote vpn.example.test 1194\nroute 192.168.1.0 255.255.255.0\n<ca>\nPEM\n</ca>\n",
		cfgMod: func(e *engine) {
			e.lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
				if fail.Load() {
					return nil, errors.New("temporary failure in name resolution")
				}
				return *answer.Load(), nil
			}
		},
	})
	h.net.announceErr = func(n int, it tunnel.Intent) error {
		if n >= 3 && strings.Contains(h.readRecording("commands"), "SIGUSR1") {
			sigusr1BeforeAnnounce.Store(true)
		}
		return nil
	}
	h.start()
	h.waitFor("Up", h.stateIs(tunnel.StateUp))
	if n := len(h.net.snapshot()); n != 2 {
		t.Fatalf("%d calls to the Reconciler before the rebind, want 2", n)
	}

	waitCalls := func(n int) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); len(h.net.snapshot()) < n; time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("the engine made %d calls to the Reconciler, want %d", len(h.net.snapshot()), n)
			}
		}
	}
	waitSignals := func(n int) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); strings.Count(h.readRecording("commands"), "signal SIGUSR1") < n; time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("openvpn received %d restarts, want %d", strings.Count(h.readRecording("commands"), "signal SIGUSR1"), n)
			}
		}
	}

	// The network changed and the name now resolves elsewhere.
	set("192.0.2.99")
	h.eng.Rebind()
	waitCalls(3)
	waitSignals(1)
	third := h.net.snapshot()[2].intent
	if third.State != tunnel.StateUp || third.Iface != "utun11" || len(third.Routes) != 1 {
		t.Errorf("the re-announcement must be the whole statement of the tunnel: %+v", third)
	}
	// The new answer, plus the server the tunnel was established with.
	if want := addrs("192.0.2.99", "192.0.2.1"); !reflect.DeepEqual(third.Endpoints, want) {
		t.Errorf("Endpoints = %v, want %v", third.Endpoints, want)
	}
	if sigusr1BeforeAnnounce.Load() {
		t.Error("openvpn was told to reconnect before the Reconciler heard the new addresses")
	}
	h.waitForNew("Up again", h.stateIs(tunnel.StateUp))

	// The same answer again: nothing to tell.
	h.eng.Rebind()
	waitSignals(2)
	if n := len(h.net.snapshot()); n != 3 {
		t.Errorf("an unchanged answer was announced again (%d calls)", n)
	}
	h.waitForNew("Up again", h.stateIs(tunnel.StateUp))

	// A failed lookup keeps what is known and still restarts the connection.
	fail.Store(true)
	h.eng.Rebind()
	waitSignals(3)
	if n := len(h.net.snapshot()); n != 3 {
		t.Errorf("a failed lookup changed the announcement (%d calls)", n)
	}
	if !strings.Contains(h.logText(), "keeping the previous server addresses") {
		t.Errorf("the failed lookup was not reported: %q", h.logText())
	}
	h.stop()
	requireNoEngineGoroutines(t)
}

// pauseRecord reads what the fake openvpn recorded about the pauses it
// announced (>HOLD:...:N): how long each lasted before the engine released it,
// and how often the engine sent it a restart signal, which openvpn ignores
// during a pause.
func pauseRecord(t *testing.T, recording string) (waits []time.Duration, signals int) {
	t.Helper()
	var announced int64
	for _, line := range strings.Split(strings.TrimSpace(recording), "\n") {
		kind, millis, _ := strings.Cut(line, " ")
		n, err := strconv.ParseInt(millis, 10, 64)
		if err != nil {
			t.Fatalf("bad line %q in the record of pauses", line)
		}
		switch kind {
		case "announce":
			announced = n
		case "release":
			waits = append(waits, time.Duration(n-announced)*time.Millisecond)
		case "signal-during-pause":
			signals++
		}
	}
	return waits, signals
}

// openvpn announces the pause before each new connection attempt and expects
// the management client to wait it out. Releasing it at once made it retry
// thousands of times a second against a server that refuses connections.
// While it fails to connect the engine stays Connecting, says why after the
// grace, and clears that when the tunnel comes up.
func TestEngineWaitsOutOpenVPNsPauseAndSaysWhyItCannotConnect(t *testing.T) {
	h := newHarness(t, harnessOpts{
		env:    map[string]string{"OVPN_FAKE_FAIL_LOOP": "1", "OVPN_FAKE_FAIL_CYCLES": "3"},
		cfgMod: func(e *engine) { e.stuckGrace = 500 * time.Millisecond },
	})
	h.start()

	// On a busy machine the grace can end before the first refusal has been logged, and the
	// status first says that the server does not answer: the concrete reason follows from the
	// next attempt, and is the one this test is about.
	const reason = "cannot reach 192.0.2.1:1194: connection refused"
	stuck := h.waitFor("the reason", func(s tunnel.Status) bool { return s.Err == reason })
	if stuck.State != tunnel.StateConnecting {
		t.Errorf("status = %+v, want Connecting with the reason", stuck)
	}
	up := h.waitFor("Up", h.stateIs(tunnel.StateUp))
	if up.Err != "" {
		t.Errorf("Err after connecting = %q", up.Err)
	}
	if h.seen(h.stateIs(tunnel.StateFailed)) {
		t.Error("the engine gave up")
	}

	waits, signals := pauseRecord(t, h.readRecording("holds"))
	if len(waits) != 3 || signals != 0 {
		t.Fatalf("%d pauses released, %d signals during them; want 3 and 0", len(waits), signals)
	}
	for i, w := range waits {
		if w < 900*time.Millisecond || w > 3*time.Second {
			t.Errorf("pause %d was released after %v, want the announced second", i, w)
		}
	}
	h.stop()
	requireNoEngineGoroutines(t)
}

// During a long pause a network change ends it (openvpn ignores a restart
// signal then) and Stop still ends openvpn.
func TestEnginePauseStaysResponsive(t *testing.T) {
	setup := func(t *testing.T) *harness {
		h := newHarness(t, harnessOpts{env: map[string]string{"OVPN_FAKE_FAIL_LOOP": "30"}})
		h.start()
		for deadline := time.Now().Add(10 * time.Second); !strings.Contains(h.readRecording("holds"), "announce"); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("openvpn never announced a pause")
			}
		}
		return h
	}

	t.Run("network change", func(t *testing.T) {
		h := setup(t)
		h.eng.Rebind()
		for deadline := time.Now().Add(5 * time.Second); !strings.Contains(h.readRecording("holds"), "release"); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("a network change did not end the pause")
			}
		}
		waits, signals := pauseRecord(t, h.readRecording("holds"))
		if signals != 0 || strings.Contains(h.readRecording("commands"), "SIGUSR1") {
			t.Errorf("the engine sent a restart signal that openvpn ignores during a pause (%d)", signals)
		}
		if len(waits) != 1 || waits[0] > 5*time.Second {
			t.Errorf("waits = %v", waits)
		}
		h.stop()
	})
	t.Run("stop", func(t *testing.T) {
		h := setup(t)
		start := time.Now()
		h.stop()
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("Stop took %v during a pause of 30 s", d)
		}
		if strings.Contains(h.readRecording("holds"), "release") {
			t.Error("the pause was released although the engine was stopped")
		}
		h.requireProcessGone()
		h.requireWorkspaceGone()
		requireNoEngineGoroutines(t)
	})
}

// After a server restart the session token is refused: openvpn logs a failed
// verification and reconnects with the user's password, which is as good as
// before. The same goes for a temporary rejection. Neither may look like a
// rejected password, ask the user for it again, or lose it.
func TestEngineSoftAuthFailureKeepsTheCredentials(t *testing.T) {
	const token = "SESS_ID_AT_s3cr3ttoken"
	for _, mode := range []string{"token", "temp"} {
		t.Run(mode, func(t *testing.T) {
			env := map[string]string{
				"OVPN_FAKE_PASSWORD": "pw", "OVPN_FAKE_REAUTH": "1", "OVPN_FAKE_SOFT_AUTHFAIL": mode,
			}
			if mode == "token" {
				env["OVPN_FAKE_AUTH_TOKEN"] = "1"
			}
			h := newHarness(t, harnessOpts{env: env})
			h.start()
			h.waitFor("AwaitingCredentials", h.stateIs(tunnel.StateAwaitingCredentials))
			if err := h.eng.ProvideCredentials(tunnel.CredentialUserPassword, "dave", "pw"); err != nil {
				t.Fatal(err)
			}
			h.waitFor("Up", h.stateIs(tunnel.StateUp))
			h.mu.Lock()
			skip := len(h.statuses)
			h.mu.Unlock()

			h.eng.Rebind() // the fake answers the restart with the failed verification
			h.waitForNew("Reconnecting", h.stateIs(tunnel.StateReconnecting))
			h.waitForNew("Up again", h.stateIs(tunnel.StateUp))

			h.mu.Lock()
			for _, s := range h.statuses[skip:] {
				if s.State == tunnel.StateAwaitingCredentials || s.NeedsCredentials != tunnel.CredentialNone || s.Err != "" {
					t.Errorf("a soft failure looked like a rejected password: %+v", s)
				}
			}
			h.mu.Unlock()
			if cmds := h.readRecording("commands"); strings.Count(cmds, `password "Auth" "pw"`) != 2 {
				t.Errorf("the saved password was not sent again after the failure:\n%s", cmds)
			}
			if logs := h.logText(); strings.Contains(logs, token) {
				t.Errorf("the session token reached the log:\n%s", logs)
			}
			h.stop()
			requireNoEngineGoroutines(t)
		})
	}
}

// openvpn reports pushed dns options neither in the UPDOWN environment nor
// anywhere else the engine can ask, only in the PUSH_REPLY it logs. The engine
// reads them there, and the profile's pull-filters apply: the log shows the
// reply as the server sent it.
func TestEngineGivesPushedDNSOptionsToTheReconciler(t *testing.T) {
	const push = "route-gateway 10.8.0.1,dns server 1 address 10.8.0.1,dns server 1 resolve-domains corp.example," +
		"dns server 2 address 10.8.0.99,dns search-domains lan.example,dhcp-option DNS 10.8.0.77,ifconfig 10.8.0.6 255.255.255.0"
	tests := []struct {
		name    string
		profile string
		want    []tunnel.DNSIntent
	}{
		{
			name:    "both servers",
			profile: minimalProfile,
			want: []tunnel.DNSIntent{
				{Servers: addrs("10.8.0.1"), MatchDomains: []string{"corp.example"}},
				{Servers: addrs("10.8.0.99"), MatchDomains: []string{"lan.example"}},
			},
		},
		{
			name:    "server 2 is pull-filtered",
			profile: minimalProfile + "pull-filter ignore \"dns server 2\"\n",
			want:    []tunnel.DNSIntent{{Servers: addrs("10.8.0.1"), MatchDomains: []string{"corp.example"}}},
		},
		{
			name:    "all dns options are pull-filtered, and openvpn's own dhcp-option remains",
			profile: minimalProfile + "pull-filter ignore \"dns \"\n",
			want:    nil, // the fake does not put dhcp-option into the environment; the real binary does
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, harnessOpts{profile: tt.profile, env: map[string]string{"OVPN_FAKE_PUSH": push}})
			h.start()
			h.waitFor("Up", h.stateIs(tunnel.StateUp))
			var up tunnel.Intent
			for _, c := range h.net.snapshot() {
				if c.intent.State == tunnel.StateUp {
					up = c.intent
				}
			}
			if !reflect.DeepEqual(up.DNS, tt.want) {
				t.Errorf("DNS = %+v\nwant %+v", up.DNS, tt.want)
			}
			h.stop()
		})
	}
}
