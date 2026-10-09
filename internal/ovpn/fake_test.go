package ovpn

import (
	"bufio"
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// The engine tests run against a fake "openvpn": a copy of the test binary (see
// writeStub) that plays openvpn. It opens the management interface like openvpn
// does, on a Unix socket or, as on Windows, a password protected loopback port
// announced on stdout; speaks the protocol with the transcripts OpenVPN 2.7
// writes; and records what it receives in OVPN_FAKE_DIR. Its behaviour is set
// with OVPN_FAKE_* settings kept next to the copy, because the engine gives the
// child a bare environment.

func init() { fakeOpenVPNMain = runFakeOpenVPN }

const defaultFakeVersion = "OpenVPN 2.7.7 aarch64-apple-darwin27.0.0 [SSL (OpenSSL)] [LZO] [LZ4] [MH/RECVDA] [AEAD]"

// defaultUpdownEnv is what the fake reports at >UPDOWN:UP unless told otherwise.
const defaultUpdownEnv = `dev=utun11
dev_type=tun
ifconfig_local=10.8.0.6
ifconfig_remote=10.8.0.5
route_net_gateway=192.168.51.1
route_vpn_gateway=10.8.0.5
route_network_1=192.168.1.0
route_netmask_1=255.255.255.0
route_gateway_1=10.8.0.5
trusted_ip=192.0.2.1
trusted_port=1194`

func runFakeOpenVPN() int {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "--version" {
		v := os.Getenv("OVPN_FAKE_VERSION")
		if v == "" {
			v = defaultFakeVersion
		}
		fmt.Println(v)
		fmt.Println("library versions: OpenSSL 3.5.9 29 Sep 2026, LZO 2.10")
		return 1
	}

	dir := os.Getenv("OVPN_FAKE_DIR")
	record := func(name, text string) {
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintln(f, text)
			f.Close()
		}
	}
	record("pid", fmt.Sprint(os.Getpid()))
	record("argv", strings.Join(args, "\n"))
	var config string
	var management []string // what follows --management: a socket and "unix", or an address, a port and a password file
	for i, a := range args {
		switch a {
		case "--config":
			config = args[i+1]
		case "--management":
			management = args[i+1 : i+4]
		}
	}
	if data, err := os.ReadFile(config); err == nil {
		os.WriteFile(filepath.Join(dir, "config.copy"), data, 0o600)
	}
	recordWorkspaceAccess(record, config)

	if os.Getenv("OVPN_FAKE_CRASH") == "early" {
		fmt.Fprintln(os.Stderr, "Options error: Unrecognized option or missing or extra parameter(s) in config:1: frobnicate (2.7.7)")
		fmt.Fprintln(os.Stderr, "Use --help for more information.")
		return 1
	}
	if os.Getenv("OVPN_FAKE_STDOUT") != "" {
		fmt.Println("2026-10-07 12:00:00 OpenVPN 2.7.7 starting")
		fmt.Fprintln(os.Stderr, "2026-10-07 12:00:00 a warning on stderr before management")
	}
	if os.Getenv("OVPN_FAKE_NOMGMT") != "" {
		time.Sleep(time.Minute)
		return 0
	}
	if os.Getenv("OVPN_FAKE_IGNORE_TERM") != "" {
		signal.Ignore(syscall.SIGTERM)
	}

	conn, cleanup, err := acceptManagement(management, record)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake:", err)
		return 2
	}
	defer cleanup()
	var password string // of the management interface, which openvpn echoes in one of its errors
	if management[1] != "unix" {
		stored, _ := os.ReadFile(management[2])
		password = strings.TrimSpace(string(stored))
	}
	f := &fakeOpenVPN{conn: conn, record: record, password: password, holds: make(chan struct{}, 64), creds: make(chan [2]string, 8),
		usr1: make(chan struct{}, 8), term: make(chan struct{}, 8)}
	return f.run()
}

type fakeOpenVPN struct {
	conn     net.Conn
	record   func(name, text string)
	wmu      sync.Mutex
	password string

	holds chan struct{} // one entry per "hold release" received
	creds chan [2]string
	usr1  chan struct{}
	term  chan struct{}
}

func (f *fakeOpenVPN) send(lines ...string) {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	for _, l := range lines {
		fmt.Fprintf(f.conn, "%s\r\n", l)
	}
}

func (f *fakeOpenVPN) state(name, desc, local, remote string) {
	port := ""
	if remote != "" {
		port = "1194"
	}
	f.send(fmt.Sprintf(">STATE:%d,%s,%s,%s,%s,%s,,", time.Now().Unix(), name, desc, local, remote, port))
}

// readCommands answers management commands the way openvpn does.
func (f *fakeOpenVPN) readCommands() {
	sc := bufio.NewScanner(f.conn)
	var user string
	for sc.Scan() {
		line := sc.Text()
		f.record("commands", line)
		fields, err := splitFields(line)
		if err != nil || len(fields) == 0 {
			f.send("ERROR: bad command")
			continue
		}
		if os.Getenv("OVPN_FAKE_ECHO_SECRETS") != "" {
			// What some openvpn builds do: echo the command into the log.
			f.send(fmt.Sprintf(">LOG:%d,D,MANAGEMENT: CMD '%s'", time.Now().Unix(), line))
		}
		if refused := os.Getenv("OVPN_FAKE_REFUSE"); refused != "" && fields[0] == refused {
			// What openvpn for Windows did once when the command followed the
			// password too closely: it took the password for the command.
			f.send(fmt.Sprintf("ERROR: unknown command [%s], enter 'help' for more options", cmp.Or(f.password, "x")))
			continue
		}
		switch fields[0] {
		case "state", "bytecount", "log":
			f.send("SUCCESS: ok")
		case "hold":
			f.send("SUCCESS: hold release succeeded")
			f.holds <- struct{}{}
		case "username":
			if len(fields) < 3 {
				f.send("ERROR: parameters")
				continue
			}
			user = fields[2]
			f.send("SUCCESS: 'Auth' username entered, but not yet verified")
		case "password":
			if len(fields) < 3 {
				f.send("ERROR: parameters")
				continue
			}
			f.send("SUCCESS: 'Auth' password entered, but not yet verified")
			f.creds <- [2]string{user, fields[2]}
		case "signal":
			f.send("SUCCESS: signal " + fields[1] + " thrown")
			switch fields[1] {
			case "SIGUSR1":
				f.usr1 <- struct{}{}
			case "SIGTERM":
				f.term <- struct{}{}
			}
		default:
			f.send("ERROR: unknown command")
		}
	}
}

func (f *fakeOpenVPN) run() int {
	go f.readCommands()
	f.send(">INFO:OpenVPN Management Interface Version 6 -- type 'help' for more info",
		">HOLD:Waiting for hold release:0")
	<-f.holds

	if wait := os.Getenv("OVPN_FAKE_FAIL_LOOP"); wait != "" {
		if !f.failLoop(wait) {
			return 0
		}
	}
	if os.Getenv("OVPN_FAKE_STDOUT") != "" {
		fmt.Println("2026-10-07 12:00:01 line on stdout after management")
		time.Sleep(50 * time.Millisecond)
	}
	f.state("CONNECTING", "", "", "")
	f.send(fmt.Sprintf(">LOG:%d,I,TCP connection established with [AF_INET]192.0.2.1:1194", time.Now().Unix()))
	f.state("WAIT", "", "", "")

	if want := os.Getenv("OVPN_FAKE_PASSWORD"); want != "" {
		if !f.authenticate(want) {
			return 1
		}
	}
	f.state("AUTH", "", "", "")
	f.state("GET_CONFIG", "", "", "")
	// What the server pushes, as openvpn logs it: one PUSH_REPLY per \n.
	for _, reply := range strings.Split(os.Getenv("OVPN_FAKE_PUSH"), `\n`) {
		if reply != "" {
			f.send(fmt.Sprintf(">LOG:%d,,PUSH: Received control message: 'PUSH_REPLY,%s'", time.Now().Unix(), reply))
		}
	}
	f.state("ASSIGN_IP", "", "10.8.0.6", "")
	f.state("ADD_ROUTES", "", "10.8.0.6", "")
	if os.Getenv("OVPN_FAKE_NOUPDOWN") == "" {
		env := defaultUpdownEnv
		if v := os.Getenv("OVPN_FAKE_UPDOWN"); v != "" {
			env = strings.ReplaceAll(v, `\n`, "\n")
		}
		f.send(">UPDOWN:UP")
		for _, l := range strings.Split(env, "\n") {
			f.send(">UPDOWN:ENV," + l)
		}
		f.send(">UPDOWN:ENV,END")
	}
	f.state("CONNECTED", "SUCCESS", "10.8.0.6", "192.0.2.1")
	f.send(">BYTECOUNT:1024,2048")
	if os.Getenv("OVPN_FAKE_AUTH_TOKEN") != "" {
		f.send(">PASSWORD:Auth-Token:SESS_ID_AT_s3cr3ttoken")
	}

	var crash <-chan time.Time
	if os.Getenv("OVPN_FAKE_CRASH") == "after-up" {
		crash = time.After(300 * time.Millisecond)
	}
	for {
		select {
		case <-crash:
			fmt.Fprintln(os.Stderr, "Inactivity timeout (--ping-exit), exiting")
			fmt.Fprintln(os.Stderr, "SIGUSR1[soft,ping-restart] received, process restarting")
			f.send(">FATAL:Cannot allocate TUN/TAP dev dynamically")
			time.Sleep(50 * time.Millisecond)
			return 3
		case <-f.usr1:
			switch os.Getenv("OVPN_FAKE_SOFT_AUTHFAIL") {
			case "token": // the server restarted and does not know the session token
				f.send(">PASSWORD:Verification Failed: 'Auth'")
			case "temp":
				f.send(">PASSWORD:Verification Failed: 'Auth' ['TEMP[backoff 1]:server busy']")
			}
			f.state("RECONNECTING", "SIGUSR1", "", "")
			time.Sleep(100 * time.Millisecond)
			f.state("CONNECTING", "", "", "")
			if want := os.Getenv("OVPN_FAKE_PASSWORD"); want != "" && os.Getenv("OVPN_FAKE_REAUTH") != "" {
				if !f.authenticate(want) {
					return 1
				}
			}
			f.state("GET_CONFIG", "", "", "")
			time.Sleep(100 * time.Millisecond)
			// persist-tun: no UPDOWN event on a restart, the interface stays.
			f.state("CONNECTED", "SUCCESS", "10.8.0.6", "192.0.2.1")
		case <-f.term:
			if os.Getenv("OVPN_FAKE_IGNORE_TERM") != "" {
				continue
			}
			f.state("EXITING", "SIGTERM", "", "")
			time.Sleep(20 * time.Millisecond)
			return 0
		}
	}
}

// authenticate prompts for credentials until the password equals want, as a
// server with auth-retry interact does. It reports false when it gives up.
func (f *fakeOpenVPN) authenticate(want string) bool {
	for attempts := 0; attempts < 5; attempts++ {
		prompt := ">PASSWORD:Need 'Auth' username/password"
		if p := os.Getenv("OVPN_FAKE_PROMPT"); p != "" {
			prompt = p
		}
		f.send(prompt)
		select {
		case got := <-f.creds:
			if os.Getenv("OVPN_FAKE_ECHO_SECRETS") != "" {
				fmt.Fprintf(os.Stderr, "auth attempt with password %s\n", got[1])
				fmt.Printf("auth attempt with password %s\n", got[1])
			}
			if got[1] == want {
				return true
			}
			f.send(">PASSWORD:Verification Failed: 'Auth'")
			f.state("RECONNECTING", "auth-failure", "", "")
			time.Sleep(50 * time.Millisecond)
		case <-time.After(30 * time.Second):
			return false
		}
	}
	return false
}

// failLoop plays an openvpn that cannot reach its server: every attempt fails
// at once, and is followed by the pause that the management client has to wait
// out (>HOLD:...:wait). It records when each pause is announced and when it is
// released, and ends after OVPN_FAKE_FAIL_CYCLES attempts (never when unset).
// It reports false when it was asked to exit.
func (f *fakeOpenVPN) failLoop(wait string) bool {
	cycles, _ := strconv.Atoi(os.Getenv("OVPN_FAKE_FAIL_CYCLES"))
	// The engine answers the start-up hold twice; the second answer must not
	// pass for the release of the first pause.
	time.Sleep(200 * time.Millisecond)
	for len(f.holds) > 0 {
		<-f.holds
	}
	millis := func() string { return strconv.FormatInt(time.Now().UnixMilli(), 10) }
	for i := 0; cycles == 0 || i < cycles; i++ {
		f.state("TCP_CONNECT", "", "", "")
		f.send(fmt.Sprintf(">LOG:%d,N,TCP: connect to [AF_INET]192.0.2.1:1194 failed: Connection refused", time.Now().Unix()),
			fmt.Sprintf(">LOG:%d,,SIGUSR1[connection failed(soft),connection-failed] received, process restarting", time.Now().Unix()))
		f.state("RECONNECTING", "connection-failed", "", "")
		f.record("holds", "announce "+millis())
		f.send(">HOLD:Waiting for hold release:" + wait)
		for released := false; !released; {
			select {
			case <-f.holds:
				f.record("holds", "release "+millis())
				released = true
			case <-f.usr1:
				// openvpn ignores the signal during the pause and says so.
				f.record("holds", "signal-during-pause "+millis())
			case <-f.term:
				f.state("EXITING", "SIGTERM", "", "")
				time.Sleep(20 * time.Millisecond)
				return false
			}
		}
	}
	return true
}

// --- the other side: what the daemon gives the engine ---

// harness runs an engine against the fake openvpn.
type harness struct {
	t      *testing.T
	dir    string // the fake's recordings
	runDir string
	eng    *engine
	net    *fakeNetwork
	binary string

	mu       sync.Mutex
	statuses []tunnel.Status
	logs     []logLine
	collect  chan struct{}
}

type logLine struct {
	level tunnel.LogLevel
	text  string
}

type harnessOpts struct {
	env     map[string]string // OVPN_FAKE_* knobs
	profile string
	spec    tunnel.Spec
	cfgMod  func(*engine)
	// realProbe makes the engine run "openvpn --version" on the fake. Without
	// it the engine is told it is a 2.7.7 with LZO, which saves a process
	// start per test; the probe has its own tests.
	realProbe bool
	// binary runs this executable instead of the fake.
	binary string
	// device replaces the stand-in that does nothing.
	device deviceProvider
	// realTrust leaves the checks of the binary and the way the engine gets its
	// tunnel interface as the daemon has them: the binary must be one the daemon
	// would run, and a profile that opens a device gets an adapter. For the tests
	// that run as administrator.
	realTrust bool
	// loopback makes the engine use the management port on loopback instead of
	// the OS default, so that systems with a socket test it as well. The address
	// of the peer is not checked there.
	loopback bool
}

const asusLikeProfile = `client
dev tun
proto tcp-client
remote 192.0.2.1 1194
nobind
comp-lzo yes
auth-user-pass
auth-nocache
auth SHA1
cipher AES-128-CBC
data-ciphers AES-128-CBC
pull-filter ignore "redirect-gateway"
pull-filter ignore "dhcp-option"
route 192.168.1.0 255.255.255.0
<ca>
-----BEGIN CERTIFICATE-----
ZmFrZQ==
-----END CERTIFICATE-----
</ca>
`

func newHarness(t *testing.T, o harnessOpts) *harness {
	t.Helper()
	h := &harness{t: t, dir: shortTempDir(t), net: &fakeNetwork{}, collect: make(chan struct{})}
	h.runDir = filepath.Join(shortTempDir(t), "run")

	if o.binary != "" {
		o.realProbe = true
	}
	// The race runtime sleeps a second at exit unless told otherwise, which
	// would make every Stop look slow.
	fakeEnv := map[string]string{"GORACE": "atexit_sleep_ms=0", "OVPN_FAKE": "1", "OVPN_FAKE_DIR": h.dir}
	for k, v := range o.env {
		fakeEnv[k] = v
	}
	h.binary = o.binary
	if h.binary == "" {
		h.binary = writeStub(t, h.dir, "openvpn", stubBehavior{FakeEnv: fakeEnv})
	}

	profile := o.profile
	if profile == "" {
		profile = asusLikeProfile
	}
	spec := o.spec
	if spec.Owner == "" {
		spec.Owner = "office"
	}
	spec.Content = []byte(profile)
	deps := tunnel.Deps{Network: h.net, Log: func(l tunnel.LogLevel, text string) {
		h.mu.Lock()
		h.logs = append(h.logs, logLine{l, text})
		h.mu.Unlock()
	}}
	logger := slog.New(slog.NewTextHandler(testLogWriter{t}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	// The fake and the binaries under test are not where the daemon would trust
	// one, and the tests do not make adapters (a profile that opens a device is
	// given a stand-in).
	b := newBackend(Config{Binary: h.binary, RunDir: h.runDir, Log: logger})
	if !o.realTrust {
		b.inspect = probeOnly
		b.trustBinary = func() (func(), error) { return func() {}, nil }
	}
	eng, err := b.newEngine(spec, deps)
	if err != nil {
		t.Fatal(err)
	}
	h.eng = eng.(*engine)
	switch {
	case o.device != nil:
		h.eng.device = o.device
	case !o.realTrust:
		h.eng.device = openvpnOwnedDevice{}
	}
	if o.loopback {
		h.eng.channel = newTCPChannel(h.eng.dir, nil)
	}
	h.eng.stopGrace = 3 * time.Second
	h.eng.dialTimeout = 5 * time.Second
	if !o.realProbe {
		h.eng.info = func() binaryInfo {
			return binaryInfo{available: true, version: "2.7.7", major: 2, minor: 7, lzo: true}
		}
	}
	if o.cfgMod != nil {
		o.cfgMod(h.eng)
	}
	go func() {
		defer close(h.collect)
		for s := range h.eng.Status() {
			h.mu.Lock()
			h.statuses = append(h.statuses, s)
			h.mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		ctx, cancel := contextWithTimeout(10 * time.Second)
		defer cancel()
		h.eng.Stop(ctx)
	})
	return h
}

func (h *harness) start() {
	h.t.Helper()
	ctx, cancel := contextWithTimeout(10 * time.Second)
	defer cancel()
	if err := h.eng.Start(ctx); err != nil {
		h.t.Fatalf("Start: %v", err)
	}
}

// waitFor returns the first status, in everything seen so far or arriving
// later, that satisfies pred.
func (h *harness) waitFor(what string, pred func(tunnel.Status) bool) tunnel.Status {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		h.mu.Lock()
		for _, s := range h.statuses {
			if pred(s) {
				h.mu.Unlock()
				return s
			}
		}
		h.mu.Unlock()
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s; statuses: %s\nlast log lines:\n%s\nlast output lines:\n%s", what, h.dumpStatuses(), h.logTail(25), strings.Join(h.eng.ring.tail(25), "\n"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForNew is waitFor restricted to statuses that arrive after the call.
func (h *harness) waitForNew(what string, pred func(tunnel.Status) bool) tunnel.Status {
	h.t.Helper()
	h.mu.Lock()
	skip := len(h.statuses)
	h.mu.Unlock()
	deadline := time.Now().Add(15 * time.Second)
	for {
		h.mu.Lock()
		for _, s := range h.statuses[skip:] {
			if pred(s) {
				h.mu.Unlock()
				return s
			}
		}
		h.mu.Unlock()
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s; statuses: %s\nlast log lines:\n%s\nlast output lines:\n%s", what, h.dumpStatuses(), h.logTail(25), strings.Join(h.eng.ring.tail(25), "\n"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *harness) dumpStatuses() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var parts []string
	for _, s := range h.statuses {
		parts = append(parts, fmt.Sprintf("{%d iface=%q err=%q need=%d}", s.State, s.Iface, s.Err, s.NeedsCredentials))
	}
	return strings.Join(parts, " ")
}

func (h *harness) stateIs(state tunnel.State) func(tunnel.Status) bool {
	return func(s tunnel.Status) bool { return s.State == state }
}

func (h *harness) seen(pred func(tunnel.Status) bool) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.statuses {
		if pred(s) {
			return true
		}
	}
	return false
}

// logTail is the last n lines of the log, for the report of a test that gave up.
func (h *harness) logTail(n int) string {
	lines := strings.Split(strings.TrimSpace(h.logText()), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}

func (h *harness) logText() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var b strings.Builder
	for _, l := range h.logs {
		b.WriteString(l.text + "\n")
	}
	return b.String()
}

func (h *harness) stop() {
	h.t.Helper()
	if testing.Verbose() {
		defer func() {
			h.t.Logf("statuses: %s", h.dumpStatuses())
			h.t.Logf("log:\n%s", h.logText())
		}()
	}
	ctx, cancel := contextWithTimeout(15 * time.Second)
	defer cancel()
	if err := h.eng.Stop(ctx); err != nil {
		h.t.Fatalf("Stop: %v", err)
	}
	select {
	case <-h.collect:
	case <-time.After(5 * time.Second):
		h.t.Fatal("the Status channel was not closed by Stop")
	}
}

func (h *harness) readRecording(name string) string {
	data, _ := os.ReadFile(filepath.Join(h.dir, name))
	return string(data)
}

func (h *harness) childPID() int {
	if c := h.eng.child.Load(); c != nil {
		return c.cmd.Process.Pid
	}
	return 0
}

// requireProcessGone fails unless the fake openvpn process no longer exists.
func (h *harness) requireProcessGone() {
	h.t.Helper()
	pid := h.childPID()
	if pid == 0 {
		return
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !processExists(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("process %d is still running after Stop", pid)
}

func (h *harness) requireWorkspaceGone() {
	h.t.Helper()
	if _, err := os.Stat(h.eng.dir); !os.IsNotExist(err) {
		h.t.Fatalf("workspace %s still exists (err %v)", h.eng.dir, err)
	}
}

func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// testLogWriter sends the backend's own diagnostics to the test log.
type testLogWriter struct{ t *testing.T }

func (w testLogWriter) Write(p []byte) (int, error) {
	if testing.Verbose() {
		w.t.Log(strings.TrimRight(string(p), "\n"))
	}
	return len(p), nil
}

// shellQuote quotes s for /bin/sh, whatever it contains.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// acceptManagement opens the management interface the way the command line
// asks and returns the first connection, after the password check when there is
// one.
func acceptManagement(management []string, record func(name, text string)) (net.Conn, func(), error) {
	if management[1] == "unix" {
		return acceptUnix(management[0])
	}
	return acceptLoopback(management[0], management[1], management[2], record)
}

func acceptUnix(socket string) (net.Conn, func(), error) {
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return nil, nil, fmt.Errorf("listen: %w", err)
	}
	os.Chmod(socket, 0o777) // openvpn creates it world-accessible
	cleanup := func() { ln.Close(); os.Remove(socket) }
	conn, err := ln.Accept()
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return conn, cleanup, nil
}

// acceptLoopback is openvpn's TCP management interface with a password file: it
// says where it listens on stdout, asks for the password on connect, and does
// not talk to a client that gives another.
func acceptLoopback(host, port, passwordFile string, record func(name, text string)) (net.Conn, func(), error) {
	password, err := os.ReadFile(passwordFile)
	if err != nil {
		return nil, nil, fmt.Errorf("read the password file: %w", err)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, nil, fmt.Errorf("listen: %w", err)
	}
	fmt.Printf("2026-10-07 12:00:00 MANAGEMENT: TCP Socket listening on [AF_INET]%s\n", ln.Addr())
	for {
		conn, err := ln.Accept()
		if err != nil {
			ln.Close()
			return nil, nil, err
		}
		if checkPassword(conn, strings.TrimSpace(string(password)), record) {
			return conn, func() { ln.Close() }, nil
		}
		conn.Close()
	}
}

func checkPassword(conn net.Conn, want string, record func(name, text string)) bool {
	fmt.Fprint(conn, "ENTER PASSWORD:")
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return false
	}
	if strings.TrimSpace(line) != want {
		record("bad-password", "refused")
		fmt.Fprint(conn, "ERROR: bad password\r\n")
		return false
	}
	fmt.Fprint(conn, "SUCCESS: password is correct\r\n")
	return true
}
