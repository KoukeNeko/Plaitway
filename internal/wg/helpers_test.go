package wg

import (
	"bytes"
	"cmp"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/netip"
	"runtime/pprof"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/tun/tuntest"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

const (
	waitTimeout  = 10 * time.Second
	testPoll     = 20 * time.Millisecond
	testGrace    = 300 * time.Millisecond
	testRetryMin = 10 * time.Millisecond
	testRetryMax = 40 * time.Millisecond
	tunnelName   = "loopbackTun1" // what tuntest's channel TUN calls itself
	serverTunnel = "10.200.0.1"
	clientTunnel = "10.200.0.2"
)

type keyPair struct{ private, public string } // base64

func newKeyPair(t *testing.T) keyPair {
	t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return keyPair{
		private: base64.StdEncoding.EncodeToString(key.Bytes()),
		public:  base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()),
	}
}

// fixedKey is a syntactically valid key for tests that never run a device.
func fixedKey(fill byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	return pc.LocalAddr().(*net.UDPAddr).Port
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	eventuallyWithin(t, waitTimeout, what, cond)
}

func eventuallyWithin(t *testing.T, limit time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// leakedGoroutines lists goroutines of wireguard-go or of this package that
// are still running, apart from the one running the test.
func leakedGoroutines() []string {
	var dump bytes.Buffer
	pprof.Lookup("goroutine").WriteTo(&dump, 2)
	var leaked []string
	for _, stack := range strings.Split(dump.String(), "\n\n") {
		if strings.Contains(stack, "testing.tRunner") {
			continue
		}
		if strings.Contains(stack, "golang.zx2c4.com/wireguard") || strings.Contains(stack, "Plaitway/internal/wg") {
			leaked = append(leaked, stack)
		}
	}
	return leaked
}

// checkLeaks fails the test when engines it started leave goroutines behind.
// Call it first: cleanups run last to first, so it runs after the engines stop.
func checkLeaks(t *testing.T) {
	t.Cleanup(func() {
		deadline := time.Now().Add(5 * time.Second)
		for {
			leaked := leakedGoroutines()
			if len(leaked) == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Errorf("%d goroutines still running after Stop:\n%s", len(leaked), strings.Join(leaked, "\n\n"))
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}

// recorder is the daemon as an engine sees it: the Network, the Interfaces and
// the tunnel factory, recording what happened in order.
type recorder struct {
	mu     sync.Mutex
	events []event

	// Hooks and failures, set before Start.
	onAnnounce func(tunnel.Intent) error
	onWithdraw func()
	// onConfigure runs inside Configure and decides its result, as a slow
	// command that exec kills when ctx ends would.
	onConfigure  func(ctx context.Context) error
	configureErr error
	tunErr       error
	tun          *tuntest.ChannelTUN
	wrapTun      func(*tuntest.ChannelTUN) tun.Device
}

type event struct {
	kind   string // "tun", "configure", "announce", "withdraw"
	intent tunnel.Intent
	name   string
	addrs  []netip.Prefix
	mtu    int
}

func newRecorder() *recorder {
	return &recorder{tun: tuntest.NewChannelTUN()}
}

func (r *recorder) add(e event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) Announce(in tunnel.Intent) error {
	var err error
	if r.onAnnounce != nil {
		err = r.onAnnounce(in)
	}
	r.add(event{kind: "announce", intent: in})
	return err
}

func (r *recorder) Withdraw(tunnel.OwnerID) {
	if r.onWithdraw != nil {
		r.onWithdraw()
	}
	r.add(event{kind: "withdraw"})
}

func (r *recorder) Configure(ctx context.Context, name string, addrs []netip.Prefix, mtu int) error {
	r.add(event{kind: "configure", name: name, addrs: addrs, mtu: mtu})
	if r.onConfigure != nil {
		return r.onConfigure(ctx)
	}
	return r.configureErr
}

func (r *recorder) newTun(mtu int) (tun.Device, error) {
	if r.tunErr != nil {
		return nil, r.tunErr
	}
	r.add(event{kind: "tun", mtu: mtu})
	if r.wrapTun != nil {
		return r.wrapTun(r.tun), nil
	}
	return r.tun.TUN(), nil
}

// kinds is the sequence of events, an announcement as announce:connecting or
// announce:up.
func (r *recorder) kinds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var kinds []string
	for _, e := range r.events {
		kind := e.kind
		if kind == "announce" {
			kind += ":" + stateName(e.intent.State)
		}
		kinds = append(kinds, kind)
	}
	return kinds
}

func (r *recorder) find(kind string) []event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var found []event
	for _, e := range r.events {
		if e.kind == kind {
			found = append(found, e)
		}
	}
	return found
}

func (r *recorder) announces() []tunnel.Intent {
	var intents []tunnel.Intent
	for _, e := range r.find("announce") {
		intents = append(intents, e.intent)
	}
	return intents
}

func stateName(s tunnel.State) string {
	switch s {
	case tunnel.StateConnecting:
		return "connecting"
	case tunnel.StateUp:
		return "up"
	}
	return fmt.Sprintf("state%d", s)
}

type fakeMonitor struct{}

func (fakeMonitor) Snapshot() (osnet.NetState, error) { return osnet.NetState{}, nil }
func (fakeMonitor) Events(context.Context) <-chan osnet.Change {
	ch := make(chan osnet.Change)
	close(ch)
	return ch
}

type fakeResolver struct {
	mu    sync.Mutex
	addrs map[string][]netip.Addr
	err   error
	hang  bool          // a lookup waits until its context ends, as one behind a dead network does
	delay time.Duration // how long a lookup takes
	calls []time.Time
}

func (r *fakeResolver) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	r.calls = append(r.calls, time.Now())
	hang, delay, err, addrs := r.hang, r.delay, r.err, slices.Clone(r.addrs[host])
	r.mu.Unlock()
	if hang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	select {
	case <-time.After(delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	return addrs, nil
}

// setFailure makes lookups fail with err, or succeed again when err is nil.
func (r *fakeResolver) setFailure(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

func (r *fakeResolver) setHang(hang bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hang = hang
}

func (r *fakeResolver) setDelay(delay time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.delay = delay
}

func (r *fakeResolver) set(host string, addrs ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.addrs == nil {
		r.addrs = map[string][]netip.Addr{}
	}
	r.addrs[host] = nil
	for _, a := range addrs {
		r.addrs[host] = append(r.addrs[host], netip.MustParseAddr(a))
	}
}

func (r *fakeResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// callTimes is when each lookup started.
func (r *fakeResolver) callTimes() []time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// logSink keeps an engine's log lines; it never blocks.
type logSink struct {
	mu    sync.Mutex
	lines []string
}

func (l *logSink) log(level tunnel.LogLevel, text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf("%d %s", level, text))
}

func (l *logSink) has(substr string) bool { return l.count(substr) > 0 }

// count is the number of log lines that contain substr.
func (l *logSink) count(substr string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(slices.DeleteFunc(slices.Clone(l.lines), func(line string) bool { return !strings.Contains(line, substr) }))
}

// statusLog reads an engine's status channel until it closes.
type statusLog struct {
	mu   sync.Mutex
	all  []tunnel.Status
	done chan struct{}
}

func collectStatus(ch <-chan tunnel.Status) *statusLog {
	l := &statusLog{done: make(chan struct{})}
	go func() {
		defer close(l.done)
		for s := range ch {
			l.mu.Lock()
			l.all = append(l.all, s)
			l.mu.Unlock()
		}
	}()
	return l
}

// states is the sequence of states seen, each repeat folded into one.
func (l *statusLog) states() []tunnel.State {
	l.mu.Lock()
	defer l.mu.Unlock()
	var states []tunnel.State
	for _, s := range l.all {
		if len(states) == 0 || states[len(states)-1] != s.State {
			states = append(states, s.State)
		}
	}
	return states
}

func (l *statusLog) first() tunnel.Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.all) == 0 {
		return tunnel.Status{}
	}
	return l.all[0]
}

func (l *statusLog) last() tunnel.Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.all) == 0 {
		return tunnel.Status{}
	}
	return l.all[len(l.all)-1]
}

// node is one engine under test with everything it talks to.
type node struct {
	t      *testing.T
	rec    *recorder
	logs   *logSink
	eng    *engine
	status *statusLog
}

type nodeOptions struct {
	resolver       *fakeResolver
	mode           tunnel.Mode
	excludePrivate bool
	rec            *recorder
	// The pause after a failed lookup; the defaults are short.
	retryMin, retryMax time.Duration
}

// newNode creates an engine for content, with the status channel already read.
// The engine is stopped when the test ends.
func newNode(t *testing.T, name, content string, opts nodeOptions) *node {
	t.Helper()
	n := &node{t: t, rec: opts.rec, logs: &logSink{}}
	if n.rec == nil {
		n.rec = newRecorder()
	}
	cfg := Config{
		TunFactory:   n.rec.newTun,
		Interfaces:   n.rec,
		pollInterval: testPoll,
		stallGrace:   testGrace,
		retryMin:     cmp.Or(opts.retryMin, testRetryMin),
		retryMax:     cmp.Or(opts.retryMax, testRetryMax),
	}
	if opts.resolver != nil {
		cfg.lookup = opts.resolver.lookup
	}
	spec := tunnel.Spec{
		Owner: tunnel.OwnerID(name), Name: name, Content: []byte(content), Mode: opts.mode, Priority: 7,
		ExcludePrivateIPs: opts.excludePrivate,
	}
	deps := tunnel.Deps{Network: n.rec, Net: fakeMonitor{}, Log: n.logs.log}
	eng, err := newEngine(cfg.withDefaults(), spec, deps)
	if err != nil {
		t.Fatalf("newEngine(%s): %v", name, err)
	}
	n.eng = eng
	n.status = collectStatus(eng.Status())
	t.Cleanup(func() {
		if err := eng.Stop(context.Background()); err != nil {
			t.Errorf("Stop(%s): %v", name, err)
		}
		if t.Failed() {
			n.logs.mu.Lock()
			defer n.logs.mu.Unlock()
			t.Logf("%s log:\n%s", name, strings.Join(n.logs.lines, "\n"))
		}
	})
	return n
}

// start starts the engine, which has to return at once whatever the network
// and the interface setup are doing.
func (n *node) start() {
	n.t.Helper()
	started := make(chan error, 1)
	go func() { started <- n.eng.Start(context.Background()) }()
	select {
	case err := <-started:
		if err != nil {
			n.t.Fatalf("Start: %v", err)
		}
	case <-time.After(promptly):
		n.t.Fatal("Start did not return promptly")
	}
}

func (n *node) waitState(state tunnel.State) tunnel.Status {
	n.t.Helper()
	eventually(n.t, fmt.Sprintf("state %d", state), func() bool { return n.status.last().State == state })
	return n.status.last()
}

// stats reads the counters and handshake straight from the device.
func (n *node) stats() deviceStats {
	n.t.Helper()
	dump, err := n.eng.dev.IpcGet()
	if err != nil {
		n.t.Fatal(err)
	}
	stats, err := parseDeviceStats(dump)
	if err != nil {
		n.t.Fatal(err)
	}
	return stats
}

// allowedIPs reads the AllowedIPs of every peer straight from the device,
// sorted as text.
func (n *node) allowedIPs() []string {
	n.t.Helper()
	dump, err := n.eng.dev.IpcGet()
	if err != nil {
		n.t.Fatal(err)
	}
	var allowed []string
	for _, line := range strings.Split(dump, "\n") {
		if prefix, ok := strings.CutPrefix(line, "allowed_ip="); ok {
			allowed = append(allowed, prefix)
		}
	}
	slices.Sort(allowed)
	return allowed
}

// prefixStrings is prefixes as text, sorted, to compare with allowedIPs.
func prefixStrings(prefixes []netip.Prefix) []string {
	var text []string
	for _, p := range prefixes {
		text = append(text, p.String())
	}
	slices.Sort(text)
	return text
}

func serverProfile(server, client keyPair, port int) string {
	return fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = %s/24
ListenPort = %d

[Peer]
PublicKey = %s
AllowedIPs = %s/32
`, server.private, serverTunnel, port, client.public, clientTunnel)
}

func clientProfile(server, client keyPair, endpoint, extraInterface, extraPeer string) string {
	return fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = %s/24
%s

[Peer]
PublicKey = %s
Endpoint = %s
AllowedIPs = 10.200.0.0/24, 192.168.77.0/24
%s
`, client.private, clientTunnel, extraInterface, server.public, endpoint, extraPeer)
}

// pair is a server and a client engine connected over loopback UDP.
type pair struct {
	server, client *node
	port           int
}

type pairOptions struct {
	endpoint       func(port int) string // the client's Endpoint; default 127.0.0.1
	extraInterface string                // more lines for the client's [Interface]
	extraPeer      string                // more lines for the client's [Peer]
	client         nodeOptions
}

func newPair(t *testing.T, opts pairOptions) *pair {
	t.Helper()
	serverKey, clientKey := newKeyPair(t), newKeyPair(t)
	port := freeUDPPort(t)
	if opts.endpoint == nil {
		opts.endpoint = func(port int) string { return fmt.Sprintf("127.0.0.1:%d", port) }
	}
	return &pair{
		port:   port,
		server: newNode(t, "server", serverProfile(serverKey, clientKey, port), nodeOptions{}),
		client: newNode(t, "client", clientProfile(serverKey, clientKey, opts.endpoint(port), opts.extraInterface, opts.extraPeer), opts.client),
	}
}

// startServer starts the server end and waits until its device has its port. A
// client that shakes hands before that waits RekeyTimeout for its next try.
func (p *pair) startServer() {
	p.server.start()
	eventually(p.server.t, "the server to listen", func() bool {
		// The device exists once the Connecting intent is announced.
		if !slices.Contains(p.server.rec.kinds(), "announce:connecting") {
			return false
		}
		dump, err := p.server.eng.dev.IpcGet()
		return err == nil && strings.Contains(dump, fmt.Sprintf("listen_port=%d", p.port))
	})
}

// up starts both ends and waits until both report StateUp.
func (p *pair) up() {
	p.startServer()
	p.client.start()
	p.client.waitState(tunnel.StateUp)
	p.server.waitState(tunnel.StateUp)
}

func sendPacket(t *testing.T, from, to *tuntest.ChannelTUN, packet []byte) {
	t.Helper()
	select {
	case from.Outbound <- packet:
	case <-time.After(waitTimeout):
		t.Fatal("tunnel device did not read the packet")
	}
	select {
	case got := <-to.Inbound:
		if !bytes.Equal(got, packet) {
			t.Fatalf("packet changed in transit:\n got %x\nwant %x", got, packet)
		}
	case <-time.After(waitTimeout):
		t.Fatal("packet did not come out of the other tunnel")
	}
}
