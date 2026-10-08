package ovpn

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// fakeDevice records what the engine asks of its device provider, and the
// moments at which it asks, in one list with the calls to the Reconciler.
type fakeDevice struct {
	adapter      string
	acquireErr   error
	configureErr error
	acquireGate  chan struct{} // when set, acquire waits for it to be closed

	mu         sync.Mutex
	events     []string
	configured []upInfo
}

func (d *fakeDevice) note(event string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.events = append(d.events, event)
}

func (d *fakeDevice) history() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.events)
}

func (d *fakeDevice) options() []string { return []string{"--dev-node", d.adapter} }
func (d *fakeDevice) name() string      { return d.adapter }

func (d *fakeDevice) acquire(ctx context.Context) error {
	d.note("acquire")
	if d.acquireGate != nil {
		select {
		case <-d.acquireGate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return d.acquireErr
}

func (d *fakeDevice) configure(_ context.Context, up upInfo) error {
	d.note("configure")
	d.mu.Lock()
	d.configured = append(d.configured, up)
	d.mu.Unlock()
	return d.configureErr
}

func (d *fakeDevice) release() { d.note("release") }

// A name every OS accepts as the device openvpn reports: these tests are about
// the engine, not about how an OS names its adapters.
const adapterNameForTests = "tunplaitway0"

func TestEngineGivesOpenVPNTheAdapterAndLearnsNothingFromItsName(t *testing.T) {
	device := &fakeDevice{adapter: adapterNameForTests}
	h := newHarness(t, harnessOpts{device: device})
	h.start()

	up := h.waitFor("Up", h.stateIs(tunnel.StateUp))
	// The fake openvpn reports utun11; the interface is the engine's own.
	if up.Iface != adapterNameForTests {
		t.Errorf("Status.Iface = %q, want the adapter %q", up.Iface, adapterNameForTests)
	}
	var upIntent tunnel.Intent
	for _, c := range h.net.snapshot() {
		if c.intent.State == tunnel.StateUp {
			upIntent = c.intent
		}
	}
	if upIntent.Iface != adapterNameForTests {
		t.Errorf("Intent.Iface = %q, want the adapter %q", upIntent.Iface, adapterNameForTests)
	}
	argv := strings.Split(strings.TrimSpace(h.readRecording("argv")), "\n")
	if i := slices.Index(argv, "--dev-node"); i < 0 || argv[i+1] != adapterNameForTests {
		t.Errorf("openvpn was not told to use the adapter: %v", argv)
	}
	if len(device.configured) != 1 {
		t.Fatalf("the device was configured %d times, want once", len(device.configured))
	}
	got := device.configured[0]
	if got.Iface != adapterNameForTests || len(got.Addresses) != 1 || got.Addresses[0].String() != "10.8.0.6/32" || got.Peer.String() != "10.8.0.5" {
		t.Errorf("configure was given %+v", got)
	}
	h.stop()
	if events := device.history(); !slices.Equal(events, []string{"acquire", "configure", "release"}) {
		t.Errorf("device calls = %v, want acquire, configure, release", events)
	}
}

// Routes through an interface need its address; the Reconciler must not hear of
// the tunnel before the interface has it.
func TestEngineConfiguresTheDeviceBeforeTheReconcilerHearsOfTheTunnel(t *testing.T) {
	device := &fakeDevice{adapter: adapterNameForTests}
	h := newHarness(t, harnessOpts{device: device})
	var configuredWhenAnnounced sync.Map
	h.net.announceErr = func(n int, it tunnel.Intent) error {
		device.mu.Lock()
		configuredWhenAnnounced.Store(n, len(device.configured))
		device.mu.Unlock()
		return nil
	}
	h.start()
	h.waitFor("Up", h.stateIs(tunnel.StateUp))
	connecting, _ := configuredWhenAnnounced.Load(1)
	tunnelUp, _ := configuredWhenAnnounced.Load(2)
	if connecting != 0 || tunnelUp != 1 {
		t.Errorf("devices configured at the first and second announcement: %v and %v, want 0 and 1", connecting, tunnelUp)
	}
	h.stop()
}

func TestEngineReleasesTheDeviceAfterWithdrawingTheOwner(t *testing.T) {
	device := &fakeDevice{adapter: adapterNameForTests}
	h := newHarness(t, harnessOpts{device: device})
	h.net.onWithdraw = func() { device.note("withdraw") }
	h.start()
	h.waitFor("Up", h.stateIs(tunnel.StateUp))
	h.stop()
	events := device.history()
	if i, j := slices.Index(events, "withdraw"), slices.Index(events, "release"); i < 0 || j < 0 || i > j {
		t.Errorf("events = %v: the routes through the adapter must be withdrawn before the adapter goes", events)
	}
	h.requireProcessGone()
}

func TestEngineDoesNotStartOpenVPNBeforeTheDeviceIsReady(t *testing.T) {
	device := &fakeDevice{adapter: adapterNameForTests, acquireGate: make(chan struct{})}
	h := newHarness(t, harnessOpts{device: device})
	h.start()
	for deadline := time.Now().Add(5 * time.Second); !slices.Contains(device.history(), "acquire"); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the engine never asked for the adapter")
		}
	}
	time.Sleep(300 * time.Millisecond)
	if pid := h.readRecording("pid"); pid != "" {
		t.Fatalf("openvpn was started (pid %s) while the adapter was still being made", pid)
	}
	close(device.acquireGate)
	h.waitFor("Up", h.stateIs(tunnel.StateUp))
	h.stop()
}

func TestEngineStopsWhileTheAdapterIsBeingMade(t *testing.T) {
	device := &fakeDevice{adapter: adapterNameForTests, acquireGate: make(chan struct{})}
	h := newHarness(t, harnessOpts{device: device})
	h.start()
	for deadline := time.Now().Add(5 * time.Second); !slices.Contains(device.history(), "acquire"); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the engine never asked for the adapter")
		}
	}
	start := time.Now()
	h.stop()
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Stop took %v while the adapter was being made", d)
	}
	if last := lastStatus(h); last.State != tunnel.StateDisconnected {
		t.Errorf("final status = %+v, want Disconnected rather than a failure", last)
	}
	if h.readRecording("pid") != "" {
		t.Error("openvpn was started")
	}
	if events := device.history(); events[len(events)-1] != "release" {
		t.Errorf("device calls = %v: what was started must be released", events)
	}
	requireNoEngineGoroutines(t)
}

func TestEngineFailsWhenTheAdapterCannotBeMade(t *testing.T) {
	device := &fakeDevice{adapter: adapterNameForTests, acquireErr: errors.New("tapctl create: exit status 1")}
	h := newHarness(t, harnessOpts{device: device})
	h.start()
	failed := h.waitFor("Failed", h.stateIs(tunnel.StateFailed))
	if !strings.Contains(failed.Err, "prepare the tunnel interface") || !strings.Contains(failed.Err, "tapctl create") {
		t.Errorf("Err = %q", failed.Err)
	}
	<-h.collect
	if h.readRecording("pid") != "" {
		t.Error("openvpn was started without an adapter")
	}
	if events := device.history(); events[len(events)-1] != "release" {
		t.Errorf("device calls = %v: a half-made adapter must be released", events)
	}
	h.requireWorkspaceGone()
	requireNoEngineGoroutines(t)
}

func TestEngineStopsOpenVPNWhenTheDeviceCannotBeConfigured(t *testing.T) {
	device := &fakeDevice{adapter: adapterNameForTests, configureErr: errors.New("duplicate address detected")}
	h := newHarness(t, harnessOpts{device: device})
	h.start()
	failed := h.waitFor("Failed", h.stateIs(tunnel.StateFailed))
	if !strings.Contains(failed.Err, "configure the tunnel interface") || !strings.Contains(failed.Err, "duplicate address") {
		t.Errorf("Err = %q", failed.Err)
	}
	<-h.collect
	if h.seen(h.stateIs(tunnel.StateUp)) {
		t.Error("the tunnel was reported Up although the interface has no address")
	}
	for _, c := range h.net.snapshot() {
		if c.intent.State == tunnel.StateUp {
			t.Errorf("the Reconciler was told of a tunnel whose interface could not be configured: %+v", c.intent)
		}
	}
	h.requireProcessGone()
	requireNoEngineGoroutines(t)
}

func TestOnLinkPrefix(t *testing.T) {
	tests := []struct {
		local, peer, want string
	}{
		{"10.8.0.6/32", "10.8.0.5", "10.8.0.6/30"},         // net30
		{"10.8.0.2/32", "10.8.0.1", "10.8.0.2/30"},         // p2p, the next one down
		{"10.8.0.6/32", "10.8.7.5", "10.8.0.6/21"},         // far apart: the smallest subnet with both
		{"10.8.0.6/24", "10.8.0.5", "10.8.0.6/24"},         // topology subnet: the netmask is known
		{"10.8.0.6/32", "", "10.8.0.6/32"},                 // no peer reported
		{"2001:db8::2/128", "10.8.0.5", "2001:db8::2/128"}, // not the family of the peer
	}
	for _, tt := range tests {
		var peer netip.Addr
		if tt.peer != "" {
			peer = netip.MustParseAddr(tt.peer)
		}
		if got := onLinkPrefix(netip.MustParsePrefix(tt.local), peer); got.String() != tt.want {
			t.Errorf("onLinkPrefix(%s, %s) = %s, want %s", tt.local, tt.peer, got, tt.want)
		}
	}
}

func TestAdapterNameIsStableAndTellsItsOwner(t *testing.T) {
	a := adapterName(ownAdapterPrefix, "office")
	if a != adapterName(ownAdapterPrefix, "office") {
		t.Error("the same profile got two names")
	}
	if a == adapterName(ownAdapterPrefix, "home") {
		t.Error("two profiles share a name")
	}
	if !strings.HasPrefix(a, ownAdapterPrefix) || !adapterNamePattern.MatchString(a) || len(a) > 32 {
		t.Errorf("name %q does not look like one of ours", a)
	}
}
