package linux

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
		defaultRoute("0.0.0.0/0", "192.0.2.1", 2, 100),
		defaultRoute("0.0.0.0/0", "", 5, 5),
	}}
	list := []link{uplink("wlp0s20f3", 2, "192.0.2.185/24"), tunnelLink("wg0", 5, "203.0.113.2/32")}
	var listErr error
	m := &netMonitor{
		routes: table,
		links:  func() ([]link, error) { return list, listErr },
		kinds:  newLinkKinds(sysfs()),
	}

	first, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if first.Epoch != 1 || first.DefaultV4 == nil || first.DefaultV4.Iface != "wlp0s20f3" || len(first.Connected) != 1 {
		t.Errorf("first snapshot %+v", first)
	}
	if first.DefaultV4.Kind != osnet.LinkWiFi {
		t.Errorf("the default through wlp0s20f3 is %s, want LinkWiFi", kindName(first.DefaultV4.Kind))
	}
	again, _ := m.Snapshot()
	if again.Epoch != first.Epoch {
		t.Errorf("epoch moved from %d to %d without a change", first.Epoch, again.Epoch)
	}

	// The gateway changes: the Reconciler has to look again.
	table.routes[0].Gateway = addr("192.0.2.2")
	changed, _ := m.Snapshot()
	if changed.Epoch != first.Epoch+1 || changed.DefaultV4.Gateway != addr("192.0.2.2") {
		t.Errorf("after the gateway changed: %+v", changed)
	}

	table.err = errors.New("dump failed")
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
		defaultRoute("0.0.0.0/0", "192.0.2.1", 3, 100),
		{Dst: netip.MustParsePrefix("::/0"), Gateway: addr("fe80::1%wlp0s20f3"), IfIndex: 2, Metric: 1024},
	}}
	list := []link{uplink("wlp0s20f3", 2), uplink("enp0s31f6", 3)}
	m := &netMonitor{
		routes: table,
		links:  func() ([]link, error) { return list, nil },
		kinds:  newLinkKinds(sysfs()),
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
