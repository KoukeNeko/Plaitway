package windows

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
		defaultRoute("0.0.0.0/0", "192.168.1.1", intelEthernet, 0),
		defaultRoute("0.0.0.0/0", "10.200.0.1", openVPNTap, 1),
	}}
	list := []adapter{intelEthernet, openVPNTap}
	var listErr error
	m := &netMonitor{routes: table, adapters: func() ([]adapter, error) { return list, listErr }}

	first, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if first.Epoch != 1 || first.DefaultV4 == nil || first.DefaultV4.Iface != intelEthernet.Name || len(first.Connected) != 2 {
		t.Errorf("first snapshot %+v", first)
	}
	if first.DefaultV4.Kind != osnet.LinkEthernet {
		t.Errorf("the default through the Ethernet adapter is %s, want LinkEthernet", kindName(first.DefaultV4.Kind))
	}
	again, _ := m.Snapshot()
	if again.Epoch != first.Epoch {
		t.Errorf("epoch moved from %d to %d without a change", first.Epoch, again.Epoch)
	}

	// The gateway changes: the Reconciler has to look again.
	table.routes[0].Gateway = netip.MustParseAddr("192.168.1.2")
	changed, _ := m.Snapshot()
	if changed.Epoch != first.Epoch+1 || changed.DefaultV4.Gateway != netip.MustParseAddr("192.168.1.2") {
		t.Errorf("after the gateway changed: %+v", changed)
	}

	table.err = errors.New("GetIpForwardTable2 failed")
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
		defaultRoute("0.0.0.0/0", "10.30.0.1", usbGigabit, 0),
		defaultRoute("::/0", "fe80::1", realtekWiFi, 0),
	}}
	m := &netMonitor{routes: table, adapters: func() ([]adapter, error) {
		return []adapter{withMetrics(usbGigabit, 25, 25), withMetrics(realtekWiFi, 40, 40)}, nil
	}}
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

// The Reconciler ranks routes by route metric plus interface metric, so every
// interface carries the second half of the sum.
func TestSnapshotFillsTheInterfaceMetric(t *testing.T) {
	ipv6Only := adapter{LUID: 0x3001, Index: 70, Name: "Ethernet 10", Description: "Realtek PCIe GbE Family Controller", Type: ifTypeEthernet, Hardware: true, Up: true, Metric6: 35}
	down := intelEthernet
	down.Index, down.Name, down.Up, down.Metric4, down.Metric6 = 71, "Ethernet 11", false, 0, 0
	m := &netMonitor{routes: &stubRoutes{}, adapters: func() ([]adapter, error) {
		return []adapter{intelEthernet, withMetrics(realtekWiFi, 40, 55), openVPNTap, ipv6Only, down, loopback}, nil
	}}
	state, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]uint32{}
	for _, iface := range state.Interfaces {
		got[iface.Name] = iface.Metric
	}
	want := map[string]uint32{
		intelEthernet.Name: 25, // automatic metric of a gigabit link
		realtekWiFi.Name:   40, // the IPv4 one, not the IPv6 one (55)
		openVPNTap.Name:    25,
		ipv6Only.Name:      35, // no IPv4 stack: the IPv6 one
		down.Name:          0,  // an adapter that is down is not read
		loopback.Name:      75,
	}
	for name, metric := range want {
		if got[name] != metric {
			t.Errorf("metric of %q = %d, want %d", name, got[name], metric)
		}
	}
}
