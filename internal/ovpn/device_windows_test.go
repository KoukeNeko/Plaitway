package ovpn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
	"github.com/KoukeNeko/Plaitway/internal/winiface"
)

// fakeAdapters stands in for the machine: tapctl, the list of interfaces and
// the IP Helper API, in one place so that the order of the calls shows.
type fakeAdapters struct {
	mu       sync.Mutex
	present  []string // the interfaces of the machine
	calls    []string
	createAt time.Duration // the adapter shows in the list this long after create
	appears  time.Time
	failOn   string // "create" or "remove": that call fails
	link     *fakeLink
}

func (f *fakeAdapters) record(call string) {
	f.calls = append(f.calls, call)
}

func (f *fakeAdapters) create(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("create " + name)
	if f.failOn == "create" {
		return errors.New("tapctl create: exit status 1")
	}
	f.present = append(f.present, name)
	f.appears = time.Now().Add(f.createAt)
	return nil
}

func (f *fakeAdapters) remove(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("remove " + name)
	if f.failOn == "remove" {
		return errors.New("tapctl delete: exit status 1")
	}
	f.present = slices.DeleteFunc(f.present, func(n string) bool { return n == name })
	return nil
}

func (f *fakeAdapters) interfaceNames() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.present), nil
}

func (f *fakeAdapters) findByName(name string) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Contains(f.present, name) || time.Now().Before(f.appears) {
		return 0, fmt.Errorf("find interface %q: %w", name, winiface.ErrNotFound)
	}
	return 4242, nil
}

func (f *fakeAdapters) linkOf(luid uint64) (configurableLink, error) {
	if luid != 4242 {
		return nil, fmt.Errorf("no interface with LUID %d", luid)
	}
	return f.link, nil
}

func (f *fakeAdapters) history() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// fakeLink records what is set on the adapter, in order.
type fakeLink struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]error
}

func (l *fakeLink) do(call string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, call)
	return l.fail[strings.Fields(call)[0]]
}

func (l *fakeLink) SetMTU(mtu uint32, family winiface.Family) error {
	return l.do(fmt.Sprintf("SetMTU %d %s", mtu, family))
}

func (l *fakeLink) SetAddresses(prefixes []netip.Prefix) error {
	return l.do(fmt.Sprintf("SetAddresses %v", prefixes))
}

func (l *fakeLink) SetInterfaceMetric(metric uint32) error {
	return l.do(fmt.Sprintf("SetInterfaceMetric %d", metric))
}

func (l *fakeLink) WaitUp(context.Context) error { return l.do("WaitUp") }

var prefixCounter atomic.Int32

// newFakeDevice makes a device of an engine kind with a prefix of its own, so
// that the once-per-process sweep of leftovers runs for it.
func newFakeDevice(t *testing.T, machine *fakeAdapters, owner tunnel.OwnerID) *adapterDevice {
	t.Helper()
	machine.link = &fakeLink{}
	return newAdapterDevice(adapterDeviceConfig{
		tool:           machine,
		prefix:         fmt.Sprintf("Plaitway-test-%d-", prefixCounter.Add(1)),
		owner:          owner,
		log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		interfaceNames: machine.interfaceNames,
		findByName:     machine.findByName,
		linkOf:         machine.linkOf,
		readyTimeout:   2 * time.Second,
	})
}

func TestAdapterDeviceMakesItsAdapterAndRemovesIt(t *testing.T) {
	machine := &fakeAdapters{present: []string{"Ethernet", "OpenVPN TAP-Windows6", "Wi-Fi"}}
	d := newFakeDevice(t, machine, "office")
	if got := d.options(); !slices.Equal(got, []string{"--dev-node", d.adapter}) || d.name() != d.adapter {
		t.Errorf("options = %v, name = %q", got, d.name())
	}
	if err := d.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(machine.present, d.adapter) {
		t.Fatalf("the adapter %q was not made: %v", d.adapter, machine.present)
	}
	d.release()
	d.release() // once only
	want := []string{"create " + d.adapter, "remove " + d.adapter}
	if got := machine.history(); !slices.Equal(got, want) {
		t.Errorf("tapctl calls = %v, want %v", got, want)
	}
	if !slices.Equal(machine.present, []string{"Ethernet", "OpenVPN TAP-Windows6", "Wi-Fi"}) {
		t.Errorf("the machine was left with %v", machine.present)
	}
}

// The adapters of other programs are the owner's: only the ones named like the
// engine's, and only those, are leftovers.
func TestAdapterDeviceRemovesOnlyLeftoversOfItsOwnKindAtTheFirstStart(t *testing.T) {
	machine := &fakeAdapters{}
	d := newFakeDevice(t, machine, "office")
	other := newFakeDevice(t, machine, "home")
	prefix := d.prefix
	machine.present = []string{
		"Ethernet", "OpenVPN TAP-Windows6", "OpenVPN Data Channel Offload",
		prefix + "deadbeef", strings.ToUpper(prefix) + "CAFEF00D",
		prefix + "short", prefix + "0123456789", "Plaitway-0123abcd", // the WireGuard engine's
	}
	if err := d.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	var removed []string
	for _, call := range machine.history() {
		if name, ok := strings.CutPrefix(call, "remove "); ok {
			removed = append(removed, name)
		}
	}
	slices.Sort(removed)
	want := []string{prefix + "deadbeef", strings.ToUpper(prefix) + "CAFEF00D"}
	slices.Sort(want)
	if !slices.Equal(removed, want) {
		t.Errorf("removed %v, want %v", removed, want)
	}
	for _, kept := range []string{"Ethernet", "OpenVPN TAP-Windows6", "OpenVPN Data Channel Offload", "Plaitway-0123abcd", prefix + "short"} {
		if !slices.Contains(machine.present, kept) {
			t.Errorf("%q was removed", kept)
		}
	}

	// A second adapter of the same kind in the same process is not swept: the
	// first one's adapter is in use.
	machine.mu.Lock()
	machine.present = append(machine.present, prefix+"feedface")
	machine.calls = nil
	machine.mu.Unlock()
	other.prefix = prefix
	other.adapter = adapterName(prefix, "home")
	if err := other.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, call := range machine.history() {
		if strings.HasPrefix(call, "remove ") {
			t.Errorf("the second start removed something: %v", machine.history())
		}
	}
}

func TestAdapterDeviceReplacesAnAdapterOfTheSameName(t *testing.T) {
	machine := &fakeAdapters{}
	d := newFakeDevice(t, machine, "office")
	machine.present = []string{strings.ToLower(d.adapter)}
	swept := new(sync.Once) // the sweep of leftovers would remove it as well; this is about the name
	swept.Do(func() {})
	sweptPrefixes.Store(d.prefix, swept)
	if err := d.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	history := machine.history()
	if len(history) < 2 || history[len(history)-2] != "remove "+d.adapter || history[len(history)-1] != "create "+d.adapter {
		t.Errorf("calls = %v, want the old adapter removed before the new one is made", history)
	}
}

func TestAdapterDeviceWaitsForTheAdapterToAppear(t *testing.T) {
	machine := &fakeAdapters{createAt: 600 * time.Millisecond}
	d := newFakeDevice(t, machine, "office")
	start := time.Now()
	if err := d.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(start); waited < 500*time.Millisecond {
		t.Errorf("acquire returned after %v, before the IP stack knew the adapter", waited)
	}
}

func TestAdapterDeviceGivesUpOnAnAdapterThatDoesNotAppear(t *testing.T) {
	machine := &fakeAdapters{createAt: time.Hour}
	d := newFakeDevice(t, machine, "office")
	d.readyTimeout = 300 * time.Millisecond
	err := d.acquire(context.Background())
	if err == nil || !strings.Contains(err.Error(), "did not appear") {
		t.Fatalf("acquire = %v", err)
	}
	d.release()
	if got := machine.history(); got[len(got)-1] != "remove "+d.adapter {
		t.Errorf("calls = %v: the half-made adapter must be removed", got)
	}
}

func TestAdapterDeviceStopsWaitingWhenTheContextEnds(t *testing.T) {
	machine := &fakeAdapters{createAt: time.Hour}
	d := newFakeDevice(t, machine, "office")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := d.acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acquire = %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("acquire did not stop with the context")
	}
}

func TestAdapterDeviceReportsWhatTapctlSays(t *testing.T) {
	machine := &fakeAdapters{failOn: "create"}
	d := newFakeDevice(t, machine, "office")
	err := d.acquire(context.Background())
	if err == nil || !strings.Contains(err.Error(), "create the adapter "+d.adapter) || !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("acquire = %v", err)
	}
	d.release() // nothing is there, so nothing is removed
	if got := machine.history(); slices.Contains(got, "remove "+d.adapter) {
		t.Errorf("an adapter that was never made was removed: %v", got)
	}
}

func TestAdapterDeviceReleaseWithoutAcquireDoesNothing(t *testing.T) {
	machine := &fakeAdapters{}
	d := newFakeDevice(t, machine, "office")
	d.release()
	if got := machine.history(); len(got) != 0 {
		t.Errorf("calls = %v", got)
	}
}

func upInfoFor(prefix string, peer string, mtu int, extra ...string) upInfo {
	up := upInfo{Iface: "x", Addresses: []netip.Prefix{netip.MustParsePrefix(prefix)}, MTU: mtu}
	if peer != "" {
		up.Peer = netip.MustParseAddr(peer)
	}
	for _, e := range extra {
		up.Addresses = append(up.Addresses, netip.MustParsePrefix(e))
	}
	return up
}

func readyDevice(t *testing.T) (*adapterDevice, *fakeAdapters) {
	machine := &fakeAdapters{}
	d := newFakeDevice(t, machine, "office")
	if err := d.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	return d, machine
}

func TestAdapterDeviceConfiguresMTUAddressesMetricThenWaitsForTheLink(t *testing.T) {
	tests := []struct {
		name string
		up   upInfo
		want []string
	}{
		{
			name: "net30: the address gets the subnet of its peer",
			up:   upInfoFor("10.8.0.6/32", "10.8.0.5", 1500),
			want: []string{"SetMTU 1500 IPv4", "SetAddresses [10.8.0.6/30]", "SetInterfaceMetric 5", "WaitUp"},
		},
		{
			name: "subnet: the netmask is used",
			up:   upInfoFor("10.8.0.2/24", "", 1400),
			want: []string{"SetMTU 1400 IPv4", "SetAddresses [10.8.0.2/24]", "SetInterfaceMetric 5", "WaitUp"},
		},
		{
			name: "both families",
			up:   upInfoFor("10.8.0.2/24", "", 1400, "fd00::2/64"),
			want: []string{"SetMTU 1400 IPv4", "SetMTU 1400 IPv6", "SetAddresses [10.8.0.2/24 fd00::2/64]", "SetInterfaceMetric 5", "WaitUp"},
		},
		{
			name: "IPv6 does not work below 1280, so its MTU is left alone",
			up:   upInfoFor("10.8.0.2/24", "", 1200, "fd00::2/64"),
			want: []string{"SetMTU 1200 IPv4", "SetAddresses [10.8.0.2/24 fd00::2/64]", "SetInterfaceMetric 5", "WaitUp"},
		},
		{
			name: "no MTU reported: the adapter keeps its own",
			up:   upInfoFor("10.8.0.2/24", "", 0),
			want: []string{"SetAddresses [10.8.0.2/24]", "SetInterfaceMetric 5", "WaitUp"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, machine := readyDevice(t)
			if err := d.configure(context.Background(), tt.up); err != nil {
				t.Fatal(err)
			}
			if got := machine.link.calls; !slices.Equal(got, tt.want) {
				t.Errorf("calls =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestAdapterDeviceConfigureFailures(t *testing.T) {
	t.Run("no address", func(t *testing.T) {
		d, _ := readyDevice(t)
		if err := d.configure(context.Background(), upInfo{}); err == nil || !strings.Contains(err.Error(), "no tunnel address") {
			t.Errorf("configure = %v", err)
		}
	})
	t.Run("before the adapter exists", func(t *testing.T) {
		machine := &fakeAdapters{}
		d := newFakeDevice(t, machine, "office")
		if err := d.configure(context.Background(), upInfoFor("10.8.0.2/24", "", 1500)); err == nil {
			t.Error("configure succeeded without an adapter")
		}
	})
	t.Run("the system refuses the address", func(t *testing.T) {
		d, machine := readyDevice(t)
		machine.link.fail = map[string]error{"SetAddresses": errors.New("duplicate address detected")}
		err := d.configure(context.Background(), upInfoFor("10.8.0.2/24", "", 1500))
		if err == nil || !strings.Contains(err.Error(), "duplicate address") {
			t.Fatalf("configure = %v", err)
		}
		if slices.Contains(machine.link.calls, "WaitUp") {
			t.Error("the engine waited for a link whose address was refused")
		}
	})
}

// tapctl is run with the rights of the daemon, so it is held to the same
// standard as openvpn.

func TestTapctlIsNotStartedForAContextThatHasEnded(t *testing.T) {
	tool := tapctl{path: installedOpenVPN(t), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	tool.path = tapctlPath(tool.path)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := tool.create(ctx, "Plaitway-ovpn-0123abcd"); !errors.Is(err, context.Canceled) {
		t.Errorf("create = %v, want the context's error before anything is started", err)
	}
}

func TestTapctlIsNotRunWhenItIsNotTrusted(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "runs")
	stub := writeStub(t, t.TempDir(), "tapctl", stubBehavior{Counter: counter})
	tool := tapctl{path: stub, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	err := tool.create(context.Background(), "Plaitway-ovpn-0123abcd")
	if err == nil || !strings.Contains(err.Error(), "tapctl is not trusted") {
		t.Fatalf("create = %v", err)
	}
	if err := tool.remove(context.Background(), "Plaitway-ovpn-0123abcd"); err == nil || !strings.Contains(err.Error(), "tapctl is not trusted") {
		t.Errorf("remove = %v", err)
	}
	if _, err := os.Stat(counter); err == nil {
		t.Error("the program ran although it is in a folder a user can write")
	}
}
