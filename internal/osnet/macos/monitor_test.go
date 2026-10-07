package macos

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// stubRoutes is a routing table that can be read and nothing else.
type stubRoutes struct {
	routes []osnet.Route
	err    error
}

func (s *stubRoutes) Dump() ([]osnet.Route, error) { return s.routes, s.err }
func (s *stubRoutes) Add(osnet.Route) error        { return errors.New("stubRoutes is read-only") }
func (s *stubRoutes) Delete(osnet.Route) error     { return errors.New("stubRoutes is read-only") }

func TestSnapshot(t *testing.T) {
	table := &stubRoutes{routes: []osnet.Route{
		routeOf("0.0.0.0/0", "192.0.2.1", "en0", false),
		routeOf("0.0.0.0/0", "", "utun10", true),
	}}
	list := []osnet.Interface{
		{Name: "en0", Index: 14, Up: true, Addrs: pfx("192.0.2.185/24")},
		{Name: "utun10", Index: 50, Up: true, Tunnel: true, Addrs: pfx("203.0.113.2/32")},
	}
	var listErr error
	kinds := newLinkKinds((&portsRunner{out: realHardwarePorts}).run, discardLog())
	m := &netMonitor{routes: table, interfaces: func() ([]osnet.Interface, error) { return list, listErr }, kinds: kinds}

	first, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if first.Epoch != 1 || first.DefaultV4 == nil || first.DefaultV4.Iface != "en0" || len(first.Connected) != 1 {
		t.Errorf("first snapshot %+v", first)
	}
	if first.DefaultV4.Kind != osnet.LinkWiFi {
		t.Errorf("the default through en0 is %s, want LinkWiFi", kindName(first.DefaultV4.Kind))
	}
	again, _ := m.Snapshot()
	if again.Epoch != first.Epoch {
		t.Errorf("epoch moved from %d to %d without a change", first.Epoch, again.Epoch)
	}

	// The gateway changes: the Reconciler has to look again.
	table.routes[0].Gateway = netip.MustParseAddr("192.0.2.2")
	changed, _ := m.Snapshot()
	if changed.Epoch != first.Epoch+1 || changed.DefaultV4.Gateway != netip.MustParseAddr("192.0.2.2") {
		t.Errorf("after the gateway changed: %+v", changed)
	}

	table.err = errors.New("sysctl failed")
	if _, err := m.Snapshot(); !errors.Is(err, table.err) {
		t.Errorf("route error is lost: %v", err)
	}
	table.err = nil
	listErr = errors.New("no interfaces")
	if _, err := m.Snapshot(); !errors.Is(err, listErr) {
		t.Errorf("interface error is lost: %v", err)
	}
}

// Each default has the kind of its own interface, and no default has no kind.
func TestSnapshotGivesEachDefaultItsKind(t *testing.T) {
	table := &stubRoutes{routes: []osnet.Route{
		routeOf("0.0.0.0/0", "192.0.2.1", "en6", false),
		{Dst: netip.MustParsePrefix("::/0"), Gateway: netip.MustParseAddr("fe80::1").WithZone("en0"), Iface: "en0"},
	}}
	list := []osnet.Interface{{Name: "en0", Index: 14, Up: true}, {Name: "en6", Index: 54, Up: true}}
	m := &netMonitor{
		routes:     table,
		interfaces: func() ([]osnet.Interface, error) { return list, nil },
		kinds:      newLinkKinds((&portsRunner{out: realHardwarePorts}).run, discardLog()),
	}
	state, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if state.DefaultV4 == nil || state.DefaultV4.Kind != osnet.LinkEthernet {
		t.Errorf("IPv4 default %+v, want an Ethernet one", state.DefaultV4)
	}
	if state.DefaultV6 == nil || state.DefaultV6.Kind != osnet.LinkWiFi {
		t.Errorf("IPv6 default %+v, want a Wi-Fi one", state.DefaultV6)
	}

	table.routes = nil
	if state, err = m.Snapshot(); err != nil || state.DefaultV4 != nil || state.DefaultV6 != nil {
		t.Errorf("without routes: %+v, %v", state, err)
	}
}
