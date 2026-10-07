package ovpn

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// These tests run the real openvpn binary and need no root: both ends use
// "--dev null", which opens no interface. They are skipped when the binary is
// absent. The binary is looked up in build/openvpn/bin or in
// $PLAITWAY_OPENVPN.

func realBinary(t *testing.T) string {
	t.Helper()
	path := os.Getenv("PLAITWAY_OPENVPN")
	if path == "" {
		abs, err := filepath.Abs(filepath.Join("..", "..", "build", "openvpn", "bin", "openvpn"))
		if err != nil {
			t.Fatal(err)
		}
		path = abs
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("real openvpn binary not found at %s", path)
	}
	if info := probeBinary(path, 10*time.Second); !info.available {
		t.Skipf("real openvpn binary unusable: %s", info.detail)
	}
	return path
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

type serverOpts struct {
	// pushes are "--push" values.
	pushes []string
	// password, when set, makes the server demand user "alice" with it, through
	// an auth-user-pass-verify script.
	password string
}

// loopbackServer is a real openvpn server on 127.0.0.1 with a throw-away PKI.
type loopbackServer struct {
	port int
	log  string
}

func startLoopbackServer(t *testing.T, bin string, p *pki, o serverOpts) *loopbackServer {
	t.Helper()
	dir := shortTempDir(t)
	files := map[string]string{"ca.crt": p.CA, "server.crt": p.ServerCert, "server.key": p.ServerKey}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	srv := &loopbackServer{port: freeTCPPort(t), log: filepath.Join(dir, "server.log")}
	args := []string{
		"--mode", "server", "--tls-server", "--dev", "null",
		"--server", "10.8.0.0", "255.255.255.0", "--topology", "subnet", "--keepalive", "10", "30",
		"--local", "127.0.0.1", "--port", fmt.Sprint(srv.port), "--proto", "tcp-server",
		"--ca", filepath.Join(dir, "ca.crt"), "--cert", filepath.Join(dir, "server.crt"), "--key", filepath.Join(dir, "server.key"),
		"--dh", "none", "--verb", "3", "--cd", dir,
		// What the owner's ASUS router speaks.
		"--cipher", "AES-128-CBC", "--data-ciphers", "AES-128-CBC", "--auth", "SHA1", "--comp-lzo", "yes",
	}
	for _, push := range o.pushes {
		args = append(args, "--push", push)
	}
	if o.password != "" {
		script := filepath.Join(dir, "auth.sh")
		body := fmt.Sprintf("#!/bin/sh\nuser=$(sed -n 1p \"$1\")\npass=$(sed -n 2p \"$1\")\n[ \"$user\" = alice ] && [ \"$pass\" = %s ] && exit 0\nexit 1\n", shellQuote(o.password))
		if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
		args = append(args, "--script-security", "2", "--auth-user-pass-verify", script, "via-file")
	}

	cmd := exec.Command(bin, args...)
	logFile, err := os.Create(srv.log)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		logFile.Close()
		if t.Failed() {
			t.Logf("server log:\n%s", srv.readLog())
		}
	})
	for deadline := time.Now().Add(10 * time.Second); !strings.Contains(srv.readLog(), "Listening for incoming TCP connection"); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the loopback server did not start:\n%s", srv.readLog())
		}
	}
	return srv
}

func (s *loopbackServer) readLog() string {
	data, _ := os.ReadFile(s.log)
	return string(data)
}

// asusLoopbackProfile is the owner's ASUS profile, pointed at the loopback
// server: comp-lzo, AES-128-CBC, auth SHA1, credentials asked at connect,
// pushes of redirect-gateway and dhcp-option filtered out, one local route.
func asusLoopbackProfile(p *pki, port int, device string) string {
	return fmt.Sprintf(`client
dev %s
proto tcp-client
remote 127.0.0.1 %d
float
nobind
sndbuf 0
rcvbuf 0
keepalive 10 30
comp-lzo yes
auth-user-pass
auth-nocache
auth SHA1
cipher AES-128-CBC
data-ciphers AES-128-CBC
ignore-unknown-option cipher data-ciphers
ignore-unknown-option block-outside-dns
remote-cert-tls server
pull-filter ignore "redirect-gateway"
pull-filter ignore "block-outside-dns"
pull-filter ignore "dhcp-option"
route 192.168.1.0 255.255.255.0
<ca>
%s</ca>
<cert>
%s</cert>
<key>
%s</key>
`, device, port, p.CA, p.ClientCert, p.ClientKey)
}

// openvpn must accept the ASUS profile unchanged under --verb 3: the options
// parse, the key material loads, and it gets as far as connecting. The profile
// keeps "dev tun"; no interface is opened before a connection exists.
func TestRealBinaryAcceptsASUSProfile(t *testing.T) {
	bin := realBinary(t)
	p := newPKI(t)
	closedPort := freeTCPPort(t)

	parsed, err := Parse([]byte(asusLoopbackProfile(p, closedPort, "tun")))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Warnings) != 0 {
		t.Fatalf("Parse warnings: %+v", parsed.Warnings)
	}
	dir := shortTempDir(t)
	config, socket := filepath.Join(dir, "profile.ovpn"), filepath.Join(dir, "m.sock")
	if err := os.WriteFile(config, parsed.Content, 0o600); err != nil {
		t.Fatal(err)
	}
	// The daemon's own command line, with the profile's verbosity of 3.
	args := append(buildArgs(probeBinary(bin, 10*time.Second), config, socket), "--verb", "3")
	cmd := exec.Command(bin, args...)
	logPath := filepath.Join(dir, "openvpn.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	defer func() {
		cmd.Process.Kill()
		<-exited
		logFile.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := dialMgmt(ctx, socket, exited)
	if err != nil {
		out, _ := os.ReadFile(logPath)
		t.Fatalf("%v\nopenvpn said:\n%s", err, out)
	}
	defer conn.Close()

	events := make(chan mgmtEvent, 256)
	go conn.readLoop(func(ev mgmtEvent) bool { events <- ev; return true })
	for _, c := range [][2]string{{"state", "state on"}, {"log", "log on"}, {"hold", "hold release"}} {
		if err := conn.Send(c[0], c[1]); err != nil {
			t.Fatal(err)
		}
	}

	var sawPrompt, sawConnect bool
	deadline := time.After(10 * time.Second)
	for !(sawPrompt && sawConnect) {
		select {
		case ev := <-events:
			switch ev := ev.(type) {
			case passwordPrompt:
				sawPrompt = ev.Type == "Auth" && ev.UserPass
				// Answer it, as the engine would; the connection then fails,
				// the port is closed.
				cmd, _ := credentialCommand("username", ev.Type, "alice")
				conn.Send("username", cmd)
				cmd, _ = credentialCommand("password", ev.Type, "secret")
				conn.Send("password", cmd)
			case stateEvent:
				sawConnect = sawConnect || ev.Name == "TCP_CONNECT"
			case fatalEvent:
				out, _ := os.ReadFile(logPath)
				t.Fatalf("openvpn gave up: %s\n%s", ev.Msg, out)
			}
		case <-exited:
			out, _ := os.ReadFile(logPath)
			t.Fatalf("openvpn exited early:\n%s", out)
		case <-deadline:
			out, _ := os.ReadFile(logPath)
			t.Fatalf("no password prompt or connection attempt (prompt %v, connect %v):\n%s", sawPrompt, sawConnect, out)
		}
	}
	out, _ := os.ReadFile(logPath)
	for _, bad := range []string{"Options error", "Unrecognized option", "Cannot load", "Cannot open"} {
		if strings.Contains(string(out), bad) {
			t.Errorf("openvpn complained (%q):\n%s", bad, out)
		}
	}
}

func realHarness(t *testing.T, bin string, profile string, spec tunnel.Spec) *harness {
	return newHarness(t, harnessOpts{binary: bin, profile: profile, spec: spec})
}

// The complete flow against a real openvpn server: the ASUS profile with its
// pull-filters, a wrong password then the right one, a network change
// (Rebind) that makes openvpn authenticate again, and Stop.
func TestRealBinarySplitTunnelEndToEnd(t *testing.T) {
	bin := realBinary(t)
	p := newPKI(t)
	srv := startLoopbackServer(t, bin, p, serverOpts{
		password: "correct horse",
		pushes: []string{
			"route 10.20.0.0 255.255.0.0",
			"redirect-gateway def1", // the profile ignores this
			"dhcp-option DNS 10.8.0.1",
			"dhcp-option DOMAIN corp.example", // and this
		},
	})
	h := realHarness(t, bin, asusLoopbackProfile(p, srv.port, "null"), tunnel.Spec{Owner: "asus", Priority: 5})
	h.start()

	waiting := h.waitFor("a request for credentials", h.stateIs(tunnel.StateAwaitingCredentials))
	if waiting.NeedsCredentials != tunnel.CredentialUserPassword {
		t.Errorf("NeedsCredentials = %v", waiting.NeedsCredentials)
	}
	if err := h.eng.ProvideCredentials(tunnel.CredentialUserPassword, "alice", "wrong horse"); err != nil {
		t.Fatal(err)
	}
	rejected := h.waitFor("a rejected password", func(s tunnel.Status) bool {
		return s.State == tunnel.StateAwaitingCredentials && s.Err == "authentication failed"
	})
	if rejected.NeedsCredentials != tunnel.CredentialUserPassword {
		t.Errorf("after a rejection NeedsCredentials = %v", rejected.NeedsCredentials)
	}

	if err := h.eng.ProvideCredentials(tunnel.CredentialUserPassword, "alice", "correct horse"); err != nil {
		t.Fatal(err)
	}
	up := h.waitFor("Up", h.stateIs(tunnel.StateUp))
	if up.Iface != "null" || up.Err != "" {
		t.Errorf("Up status = %+v", up)
	}
	if want := []netip.Prefix{netip.MustParsePrefix("10.8.0.2/24")}; len(up.Addresses) != 1 || !up.Addresses[0].Contains(want[0].Addr()) || up.Addresses[0].Bits() != 24 {
		t.Errorf("Addresses = %v, want %v", up.Addresses, want)
	}
	if up.Remote != fmt.Sprintf("127.0.0.1:%d", srv.port) {
		t.Errorf("Remote = %q", up.Remote)
	}

	var upIntent tunnel.Intent
	for _, c := range h.net.snapshot() {
		if c.intent.State == tunnel.StateUp {
			upIntent = c.intent
		}
	}
	wantIntent := tunnel.Intent{
		Owner: "asus", State: tunnel.StateUp, Iface: "null", Role: tunnel.RoleSplit, Priority: 5,
		UpSince:   upIntent.UpSince,
		Endpoints: []netip.Addr{netip.MustParseAddr("127.0.0.1")},
		// The profile's own route and the pushed one; the filtered redirect and
		// DNS are absent.
		Routes: mustPrefixes("192.168.1.0/24", "10.20.0.0/16"),
	}
	if !reflect.DeepEqual(upIntent, wantIntent) {
		t.Errorf("Up intent =\n%+v\nwant\n%+v", upIntent, wantIntent)
	}

	// openvpn counts bytes once the handshake traffic flows.
	h.waitFor("byte counts", func(s tunnel.Status) bool { return s.Stats.RxBytes > 0 && s.Stats.TxBytes > 0 })

	// A network change: the Reconciler repaired the routes and rebinds. openvpn
	// restarts its connection, drops the credentials (auth-nocache) and asks
	// again; the engine answers from memory.
	announced := len(h.net.snapshot())
	h.eng.Rebind()
	h.waitForNew("Reconnecting", h.stateIs(tunnel.StateReconnecting))
	h.waitForNew("Up again", h.stateIs(tunnel.StateUp))
	if after := h.net.snapshot(); len(after) != announced {
		t.Errorf("the engine talked to the Reconciler during the reconnect: %+v", after[announced:])
	}
	asked := 0
	h.mu.Lock()
	seenUp := false
	for _, s := range h.statuses {
		seenUp = seenUp || s.State == tunnel.StateUp
		if seenUp && s.State == tunnel.StateAwaitingCredentials {
			asked++
		}
	}
	h.mu.Unlock()
	if asked != 0 {
		t.Errorf("the user was asked for credentials again after connecting (%d times)", asked)
	}

	logs := h.logText()
	for _, secret := range []string{"correct horse", "wrong horse"} {
		if strings.Contains(logs, secret) {
			t.Errorf("a password reached the log:\n%s", logs)
		}
	}
	if strings.Contains(logs, `"alice"`) {
		t.Errorf("openvpn echoes the user name in its log; it must be redacted:\n%s", logs)
	}
	if !strings.Contains(logs, "Initialization Sequence Completed") {
		t.Errorf("openvpn's log is not forwarded:\n%s", logs)
	}

	h.stop()
	calls := h.net.snapshot()
	if last := calls[len(calls)-1]; !last.withdraw {
		t.Errorf("the last call to the Reconciler = %+v, want Withdraw", last)
	}
	h.requireProcessGone()
	h.requireWorkspaceGone()
	requireNoEngineGoroutines(t)
}

// A server that pushes redirect-gateway, DNS and a domain to a profile that
// does not filter them: a full tunnel with a catch-all resolver. ModeSplit
// takes the default route away again.
func TestRealBinaryPushedRedirectAndDNS(t *testing.T) {
	bin := realBinary(t)
	p := newPKI(t)
	srv := startLoopbackServer(t, bin, p, serverOpts{
		pushes: []string{
			"route 10.20.0.0 255.255.0.0",
			"redirect-gateway def1",
			"dhcp-option DNS 10.8.0.1",
			"dhcp-option DOMAIN corp.example",
		},
	})
	plain := strings.NewReplacer(
		"auth-user-pass\n", "", "auth-nocache\n", "",
		"pull-filter ignore \"redirect-gateway\"\n", "", "pull-filter ignore \"dhcp-option\"\n", "",
	).Replace(asusLoopbackProfile(p, srv.port, "null"))

	tests := []struct {
		name   string
		mode   tunnel.Mode
		role   tunnel.Role
		routes []string
		dns    []tunnel.DNSIntent
	}{
		{
			name: "auto follows the server", mode: tunnel.ModeAuto, role: tunnel.RoleFull,
			routes: []string{"192.168.1.0/24", "10.20.0.0/16", "0.0.0.0/0", "::/0"},
			dns:    []tunnel.DNSIntent{{Servers: []netip.Addr{netip.MustParseAddr("10.8.0.1")}, MatchDomains: []string{"."}}},
		},
		{
			name: "split mode drops the default route", mode: tunnel.ModeSplit, role: tunnel.RoleSplit,
			routes: []string{"192.168.1.0/24", "10.20.0.0/16"},
			dns:    []tunnel.DNSIntent{{Servers: []netip.Addr{netip.MustParseAddr("10.8.0.1")}, MatchDomains: []string{"corp.example"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := realHarness(t, bin, plain, tunnel.Spec{Owner: "pushed", Mode: tt.mode})
			h.start()
			h.waitFor("Up", h.stateIs(tunnel.StateUp))
			var got tunnel.Intent
			for _, c := range h.net.snapshot() {
				if c.intent.State == tunnel.StateUp {
					got = c.intent
				}
			}
			if got.Role != tt.role || !reflect.DeepEqual(got.Routes, mustPrefixes(tt.routes...)) || !reflect.DeepEqual(got.DNS, tt.dns) {
				t.Errorf("intent = %+v\nwant role %v routes %v dns %+v", got, tt.role, tt.routes, tt.dns)
			}
			h.stop()
			h.requireProcessGone()
		})
	}
}

// openvpn exiting on its own, here because the server vanished and the profile
// says not to retry, is reported as a failure with what openvpn printed.
func TestRealBinaryServerGoneFailsCleanly(t *testing.T) {
	bin := realBinary(t)
	p := newPKI(t)
	profile := strings.Replace(asusLoopbackProfile(p, freeTCPPort(t), "null"), "auth-user-pass\n", "", 1)
	profile = strings.Replace(profile, "nobind\n", "nobind\nconnect-retry-max 1\nresolv-retry 0\nconnect-timeout 2\n", 1)
	h := realHarness(t, bin, profile, tunnel.Spec{Owner: "gone"})
	h.start()
	failed := h.waitFor("Failed", h.stateIs(tunnel.StateFailed))
	if !strings.Contains(failed.Err, "openvpn exited") {
		t.Errorf("Err = %q", failed.Err)
	}
	select {
	case <-h.collect:
	case <-time.After(5 * time.Second):
		t.Fatal("the Status channel stayed open")
	}
	calls := h.net.snapshot()
	if last := calls[len(calls)-1]; !last.withdraw {
		t.Errorf("the owner was not withdrawn: %+v", calls)
	}
	h.requireProcessGone()
	h.requireWorkspaceGone()
	requireNoEngineGoroutines(t)
}

// openvpn blocks inside its credential query while it waits for the user; Stop
// must still end it, through the management interface.
func TestRealBinaryStopWhileAwaitingCredentials(t *testing.T) {
	bin := realBinary(t)
	p := newPKI(t)
	srv := startLoopbackServer(t, bin, p, serverOpts{password: "x"})
	h := realHarness(t, bin, asusLoopbackProfile(p, srv.port, "null"), tunnel.Spec{Owner: "waiting"})
	h.start()
	h.waitFor("a request for credentials", h.stateIs(tunnel.StateAwaitingCredentials))
	start := time.Now()
	h.stop()
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Stop took %v; openvpn should exit on SIGTERM without being killed", d)
	}
	h.requireProcessGone()
	h.requireWorkspaceGone()
	requireNoEngineGoroutines(t)
}

// Parse keeps lines to the length openvpn reads. This runs the real binary on
// both sides of the limit, so that a build with another limit shows up here.
func TestRealBinaryLineLimit(t *testing.T) {
	bin := realBinary(t)
	run := func(config []byte) string {
		path := filepath.Join(shortTempDir(t), "profile.ovpn")
		if err := os.WriteFile(path, config, 0o600); err != nil {
			t.Fatal(err)
		}
		// Without certificates openvpn stops at its option check, after reading every line.
		out, _ := exec.Command(bin, "--config", path, "--dev", "null").CombinedOutput()
		return string(out)
	}
	const limitMessage = "Maximum option line length"
	profile := func(lineBytes int) string {
		const name = "verify-x509-name "
		return "client\nremote 127.0.0.1 1194\n" + name + strings.Repeat("A", lineBytes-len(name)) + "\n"
	}

	parsed, err := Parse([]byte(profile(maxConfigLineBytes)))
	if err != nil {
		t.Fatal(err)
	}
	if out := run(parsed.Content); strings.Contains(out, limitMessage) {
		t.Errorf("openvpn refused the longest line Parse accepts:\n%s", out)
	}
	if out := run([]byte(profile(maxConfigLineBytes + 1))); !strings.Contains(out, limitMessage) {
		t.Errorf("openvpn read a line one byte longer than Parse accepts; the limit has moved:\n%s", out)
	}
	if _, err := Parse([]byte(profile(maxConfigLineBytes + 1))); err == nil {
		t.Error("Parse accepted a line openvpn refuses")
	}
}

// A server that refuses connections is retried at openvpn's own pace, not as
// fast as the refusals come (tens of thousands a second when the engine
// released every pause at once). The tunnel stays Connecting, and once it has
// taken too long the status says why.
func TestRealBinaryUnreachableServerIsNotHammered(t *testing.T) {
	bin := realBinary(t)
	p := newPKI(t)
	port := freeTCPPort(t)
	profile := strings.Replace(asusLoopbackProfile(p, port, "null"), "auth-user-pass\n", "", 1)
	h := newHarness(t, harnessOpts{
		binary:  bin,
		profile: profile,
		spec:    tunnel.Spec{Owner: "refused"},
		cfgMod:  func(e *engine) { e.stuckGrace = 2 * time.Second },
	})
	h.start()

	stuck := h.waitFor("the reason", func(s tunnel.Status) bool { return s.Err != "" })
	if want := fmt.Sprintf("cannot reach 127.0.0.1:%d: connection refused", port); stuck.State != tunnel.StateConnecting || stuck.Err != want {
		t.Errorf("status = %+v, want Connecting with %q", stuck, want)
	}
	time.Sleep(4 * time.Second)
	if n := strings.Count(h.logText(), "TCP: connect to"); n < 2 || n > 10 {
		t.Errorf("openvpn tried to connect %d times in about 6 seconds, want a few", n)
	}
	if last := lastStatus(h); last.State != tunnel.StateConnecting {
		t.Errorf("state = %v, want Connecting", last.State)
	}
	h.stop()
	h.requireProcessGone()
	requireNoEngineGoroutines(t)
}

// A server that pushes the dns options of openvpn 2.6 and later, with the old
// dhcp-option next to them as compatible servers do. openvpn drops the old
// ones from its environment and reports the new ones nowhere but its log; the
// engine reads that log, and the profile's pull-filter decides what counts.
func TestRealBinaryPushedNewStyleDNS(t *testing.T) {
	bin := realBinary(t)
	p := newPKI(t)
	srv := startLoopbackServer(t, bin, p, serverOpts{
		pushes: []string{
			"redirect-gateway def1",
			"dns server 1 address 10.8.0.1",
			"dns search-domains corp.example",
			"dhcp-option DNS 10.8.0.99",
		},
	})
	plain := strings.NewReplacer(
		"auth-user-pass\n", "", "auth-nocache\n", "",
		"pull-filter ignore \"redirect-gateway\"\n", "", "pull-filter ignore \"dhcp-option\"\n", "",
	).Replace(asusLoopbackProfile(p, srv.port, "null"))
	filtered := strings.Replace(plain, "route 192.168.1.0 255.255.255.0\n", "route 192.168.1.0 255.255.255.0\npull-filter ignore \"dns \"\n", 1)

	tests := []struct {
		name    string
		profile string
		mode    tunnel.Mode
		dns     []tunnel.DNSIntent
	}{
		{"full tunnel", plain, tunnel.ModeAuto, []tunnel.DNSIntent{{Servers: addrs("10.8.0.1"), MatchDomains: []string{"."}}}},
		{"split tunnel", plain, tunnel.ModeSplit, []tunnel.DNSIntent{{Servers: addrs("10.8.0.1"), MatchDomains: []string{"corp.example"}}}},
		// openvpn then keeps the dhcp-option, and reports it as before.
		{"dns options pull-filtered", filtered, tunnel.ModeAuto, []tunnel.DNSIntent{{Servers: addrs("10.8.0.99"), MatchDomains: []string{"."}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := realHarness(t, bin, tt.profile, tunnel.Spec{Owner: "newdns", Mode: tt.mode})
			h.start()
			h.waitFor("Up", h.stateIs(tunnel.StateUp))
			var got tunnel.Intent
			for _, c := range h.net.snapshot() {
				if c.intent.State == tunnel.StateUp {
					got = c.intent
				}
			}
			if !reflect.DeepEqual(got.DNS, tt.dns) {
				t.Errorf("DNS = %+v\nwant %+v", got.DNS, tt.dns)
			}
			h.stop()
			h.requireProcessGone()
		})
	}
}
