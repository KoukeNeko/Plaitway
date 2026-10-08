package windows

import (
	"math"
	"net/netip"
	"slices"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

func TestPickDefault(t *testing.T) {
	downEthernet := intelEthernet
	downEthernet.Up = false
	slowWiFi := realtekWiFi
	slowWiFi.Metric4 = 90
	tests := []struct {
		name     string
		adapters []adapter
		routes   []osnet.Route
		v6       bool
		want     *osnet.Nexthop
	}{
		{
			name:     "the lowest effective metric wins: interface metric 25 beats 40",
			adapters: []adapter{intelEthernet, realtekWiFi},
			routes:   []osnet.Route{defaultRoute("0.0.0.0/0", "10.20.0.1", realtekWiFi, 0), defaultRoute("0.0.0.0/0", "192.168.1.1", intelEthernet, 0)},
			want:     &osnet.Nexthop{Gateway: ip("192.168.1.1"), Iface: intelEthernet.Name, Kind: osnet.LinkEthernet},
		},
		{
			name:     "the route metric counts as well: 100 + 25 loses to 0 + 40",
			adapters: []adapter{intelEthernet, realtekWiFi},
			routes:   []osnet.Route{defaultRoute("0.0.0.0/0", "192.168.1.1", intelEthernet, 100), defaultRoute("0.0.0.0/0", "10.20.0.1", realtekWiFi, 0)},
			want:     &osnet.Nexthop{Gateway: ip("10.20.0.1"), Iface: realtekWiFi.Name, Kind: osnet.LinkWiFi},
		},
		{
			name:     "a VPN default with the best metric of all is not the way out",
			adapters: []adapter{intelEthernet, openVPNTap, wintun},
			routes: []osnet.Route{
				defaultRoute("0.0.0.0/0", "10.200.0.1", openVPNTap, 1),
				defaultRoute("0.0.0.0/0", "10.6.0.1", wintun, 0),
				defaultRoute("0.0.0.0/0", "192.168.1.1", intelEthernet, 0),
			},
			want: &osnet.Nexthop{Gateway: ip("192.168.1.1"), Iface: intelEthernet.Name, Kind: osnet.LinkEthernet},
		},
		{
			name:     "only tunnels",
			adapters: []adapter{openVPNTap},
			routes:   []osnet.Route{defaultRoute("0.0.0.0/0", "10.200.0.1", openVPNTap, 0)},
			want:     nil,
		},
		{
			name:     "an adapter that is down is skipped",
			adapters: []adapter{downEthernet, realtekWiFi},
			routes:   []osnet.Route{defaultRoute("0.0.0.0/0", "192.168.1.1", downEthernet, 0), defaultRoute("0.0.0.0/0", "10.20.0.1", realtekWiFi, 0)},
			want:     &osnet.Nexthop{Gateway: ip("10.20.0.1"), Iface: realtekWiFi.Name, Kind: osnet.LinkWiFi},
		},
		{
			name:     "equal metrics: the lowest interface index, so that the pick does not flap",
			adapters: []adapter{realtekWiFi, withMetrics(usbGigabit, 25, 25), intelEthernet},
			routes: []osnet.Route{
				defaultRoute("0.0.0.0/0", "10.20.0.1", realtekWiFi, 10),
				defaultRoute("0.0.0.0/0", "10.30.0.1", usbGigabit, 0),
				defaultRoute("0.0.0.0/0", "192.168.1.1", intelEthernet, 0),
			},
			want: &osnet.Nexthop{Gateway: ip("192.168.1.1"), Iface: intelEthernet.Name, Kind: osnet.LinkEthernet},
		},
		{
			name:     "the interface metric of the family decides: IPv6 metrics are separate",
			adapters: []adapter{withMetrics(intelEthernet, 25, 500), withMetrics(realtekWiFi, 40, 40)},
			routes:   []osnet.Route{defaultRoute("::/0", "fe80::1", intelEthernet, 0), defaultRoute("::/0", "fe80::2", realtekWiFi, 0)},
			v6:       true,
			want:     &osnet.Nexthop{Gateway: ip("fe80::2"), Iface: realtekWiFi.Name, Kind: osnet.LinkWiFi},
		},
		{
			name:     "the other family's defaults are not candidates",
			adapters: []adapter{intelEthernet},
			routes:   []osnet.Route{defaultRoute("0.0.0.0/0", "192.168.1.1", intelEthernet, 0)},
			v6:       true,
			want:     nil,
		},
		{
			name:     "only default routes count, and not blackholes",
			adapters: []adapter{intelEthernet, loopback},
			routes: []osnet.Route{
				defaultRoute("10.0.0.0/8", "192.168.1.1", intelEthernet, 0),
				{Dst: netip.MustParsePrefix("0.0.0.0/0"), Iface: loopback.Name, IfIndex: loopback.Index, Blackhole: true},
			},
			want: nil,
		},
		{
			name:     "an on-link default has no gateway",
			adapters: []adapter{wwan},
			routes:   []osnet.Route{defaultRoute("0.0.0.0/0", "", wwan, 0)},
			want:     &osnet.Nexthop{Iface: wwan.Name, Kind: osnet.LinkOther},
		},
		{
			name:     "a route of an adapter the list does not know is skipped",
			adapters: []adapter{intelEthernet},
			routes:   []osnet.Route{defaultRoute("0.0.0.0/0", "10.20.0.1", realtekWiFi, 0)},
			want:     nil,
		},
		{
			name:     "metrics near the top of uint32 do not wrap around",
			adapters: []adapter{intelEthernet, slowWiFi},
			routes: []osnet.Route{
				defaultRoute("0.0.0.0/0", "192.168.1.1", intelEthernet, math.MaxUint32),
				defaultRoute("0.0.0.0/0", "10.20.0.1", slowWiFi, 0),
			},
			want: &osnet.Nexthop{Gateway: ip("10.20.0.1"), Iface: slowWiFi.Name, Kind: osnet.LinkWiFi},
		},
		{
			name:     "a corporate VPN that is virtual but reports Ethernet is not the way out, whatever its metric",
			adapters: []adapter{intelEthernet, corporateVPN},
			routes: []osnet.Route{
				defaultRoute("0.0.0.0/0", "10.99.0.1", corporateVPN, 0),
				defaultRoute("0.0.0.0/0", "192.168.1.1", intelEthernet, 0),
			},
			want: &osnet.Nexthop{Gateway: ip("192.168.1.1"), Iface: intelEthernet.Name, Kind: osnet.LinkEthernet},
		},
		{
			name:     "when that VPN is all there is, there is no default",
			adapters: []adapter{corporateVPN},
			routes:   []osnet.Route{defaultRoute("0.0.0.0/0", "10.99.0.1", corporateVPN, 0)},
			want:     nil,
		},
		{
			name:     "a Hyper-V external switch holds the default route of the PC",
			adapters: []adapter{hyperVExternal, withAddrs(intelEthernet)},
			routes:   []osnet.Route{defaultRoute("0.0.0.0/0", "192.168.1.1", hyperVExternal, 0)},
			want:     &osnet.Nexthop{Gateway: ip("192.168.1.1"), Iface: hyperVExternal.Name, Kind: osnet.LinkOther},
		},
		{
			name:     "USB tethering is a way out, of the kind other",
			adapters: []adapter{usbNCM},
			routes:   []osnet.Route{defaultRoute("0.0.0.0/0", "192.168.42.129", usbNCM, 0)},
			want:     &osnet.Nexthop{Gateway: ip("192.168.42.129"), Iface: usbNCM.Name, Kind: osnet.LinkOther},
		},
		{
			name:     "Wi-Fi alone",
			adapters: []adapter{realtekWiFi},
			routes:   []osnet.Route{defaultRoute("0.0.0.0/0", "10.20.0.1", realtekWiFi, 0)},
			want:     &osnet.Nexthop{Gateway: ip("10.20.0.1"), Iface: realtekWiFi.Name, Kind: osnet.LinkWiFi},
		},
		{
			name:     "the Ethernet port of a docking station next to Wi-Fi",
			adapters: []adapter{realtekWiFi, dockEthernet},
			routes: []osnet.Route{
				defaultRoute("0.0.0.0/0", "10.20.0.1", realtekWiFi, 0),
				defaultRoute("0.0.0.0/0", "192.168.1.1", dockEthernet, 0),
			},
			want: &osnet.Nexthop{Gateway: ip("192.168.1.1"), Iface: dockEthernet.Name, Kind: osnet.LinkEthernet},
		},
		{
			name:     "a PC whose only link is the Bluetooth personal area network has no default",
			adapters: []adapter{bluetoothPAN},
			routes:   []osnet.Route{defaultRoute("0.0.0.0/0", "192.168.44.1", bluetoothPAN, 0)},
			want:     nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pickDefault(tt.routes, tt.adapters, tt.v6)
			switch {
			case got == nil && tt.want == nil:
			case got == nil || tt.want == nil || *got != *tt.want:
				t.Errorf("pickDefault = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func withMetrics(a adapter, v4, v6 uint32) adapter {
	a.Metric4, a.Metric6 = v4, v6
	return a
}

func TestConnectedSubnets(t *testing.T) {
	downWiFi := realtekWiFi
	downWiFi.Up = false
	got := connectedSubnets([]adapter{intelEthernet, downWiFi, openVPNTap, loopback, hyperVSwitch, bluetoothPAN})
	// The Ethernet adapter's link-local and host prefixes are not subnets; the
	// tunnel, the loopback and the adapter that is down are not physical or not up.
	want := pfx("172.21.128.0/20", "192.168.1.0/24", "2001:b011:c006:bf71::/64")
	if !slices.Equal(got, want) {
		t.Errorf("connected = %v, want %v", got, want)
	}
	if got := connectedSubnets(nil); len(got) != 0 {
		t.Errorf("no adapters: %v", got)
	}
	// Two adapters on one subnet list it once, as the network, not the address.
	second := usbGigabit
	second.Addrs = pfx("192.168.1.55/24")
	if got := connectedSubnets([]adapter{intelEthernet, second}); len(got) != 2 {
		t.Errorf("a shared subnet is listed twice: %v", got)
	}
}

func TestBuildNetState(t *testing.T) {
	adapters := []adapter{intelEthernet, openVPNTap, loopback, realtekWiFi}
	routes := []osnet.Route{
		defaultRoute("0.0.0.0/0", "10.200.0.1", openVPNTap, 1),
		defaultRoute("0.0.0.0/0", "192.168.1.1", intelEthernet, 0),
		defaultRoute("::/0", "fe80::1", intelEthernet, 0),
	}
	state := buildNetState(adapters, routes)
	if state.DefaultV4 == nil || state.DefaultV4.Iface != intelEthernet.Name || state.DefaultV4.Kind != osnet.LinkEthernet {
		t.Errorf("DefaultV4 = %+v", state.DefaultV4)
	}
	if state.DefaultV6 == nil || state.DefaultV6.Gateway != ip("fe80::1") {
		t.Errorf("DefaultV6 = %+v", state.DefaultV6)
	}
	names := make([]string, len(state.Interfaces))
	for i, ifc := range state.Interfaces {
		names[i] = ifc.Name
		if ifc.Name == openVPNTap.Name && !ifc.Tunnel {
			t.Error("the TAP adapter is not flagged as a tunnel")
		}
		if ifc.Name == intelEthernet.Name && (ifc.Tunnel || ifc.Index != 2 || !ifc.Up) {
			t.Errorf("Ethernet: %+v", ifc)
		}
	}
	if !slices.Equal(names, []string{loopback.Name, intelEthernet.Name, openVPNTap.Name, realtekWiFi.Name}) {
		t.Errorf("interfaces are not ordered by index: %v", names)
	}
	// Addresses are sorted, whatever order the system lists them in.
	for _, ifc := range state.Interfaces {
		if !slices.IsSortedFunc(ifc.Addrs, comparePrefix) {
			t.Errorf("%s: addresses not sorted: %v", ifc.Name, ifc.Addrs)
		}
	}
}

func TestEpochTracker(t *testing.T) {
	base := func() osnet.NetState {
		return buildNetState([]adapter{intelEthernet, openVPNTap},
			[]osnet.Route{defaultRoute("0.0.0.0/0", "192.168.1.1", intelEthernet, 0)})
	}
	var tracker epochTracker
	first := tracker.stamp(base())
	if first.Epoch != 1 {
		t.Fatalf("first epoch = %d, want 1", first.Epoch)
	}
	if again := tracker.stamp(base()); again.Epoch != 1 {
		t.Errorf("an identical state moved the epoch to %d", again.Epoch)
	}

	// A tunnel that comes and goes is not a change of the physical network.
	withTunnel := buildNetState([]adapter{intelEthernet, openVPNTap, wintun},
		[]osnet.Route{defaultRoute("0.0.0.0/0", "192.168.1.1", intelEthernet, 0), defaultRoute("10.0.0.0/8", "10.6.0.1", wintun, 0)})
	if got := tracker.stamp(withTunnel); got.Epoch != 1 {
		t.Errorf("a new tunnel moved the epoch to %d", got.Epoch)
	}

	changes := []struct {
		name  string
		state osnet.NetState
	}{
		{"the gateway changes", buildNetState([]adapter{intelEthernet, openVPNTap}, []osnet.Route{defaultRoute("0.0.0.0/0", "192.168.1.254", intelEthernet, 0)})},
		{"a physical adapter gets a new address", buildNetState([]adapter{withAddrs(intelEthernet, "192.168.7.5/24"), openVPNTap}, []osnet.Route{defaultRoute("0.0.0.0/0", "192.168.1.1", intelEthernet, 0)})},
		{"a physical adapter appears", buildNetState([]adapter{intelEthernet, openVPNTap, realtekWiFi}, []osnet.Route{defaultRoute("0.0.0.0/0", "192.168.1.1", intelEthernet, 0)})},
		{"the default goes away", buildNetState([]adapter{intelEthernet, openVPNTap}, nil)},
	}
	last := uint64(1)
	for _, tt := range changes {
		got := tracker.stamp(tt.state)
		if got.Epoch != last+1 {
			t.Errorf("%s: epoch %d, want %d", tt.name, got.Epoch, last+1)
		}
		last = got.Epoch
	}
}

func withAddrs(a adapter, addrs ...string) adapter {
	a.Addrs = pfx(addrs...)
	return a
}

// The subnet of a virtual network that is no way out is still on-link: a VPN
// route into it would cut the PC off from it.
func TestConnectedSubnetsKeepVirtualNetworksThatAreNoWayOut(t *testing.T) {
	got := connectedSubnets([]adapter{intelEthernet, corporateVPN, bluetoothPAN, openVPNTap})
	for _, want := range pfx("10.99.0.0/24", "192.168.1.0/24") {
		if !slices.Contains(got, want) {
			t.Errorf("connected = %v, lacks %v", got, want)
		}
	}
	if slices.Contains(got, netip.MustParsePrefix("10.200.0.0/16")) {
		t.Errorf("connected = %v includes the subnet of a known tunnel", got)
	}
}

// The metric is not what the Reconciler reacts to: the automatic metric moves
// with the speed of the link, and the defaults already show what that changes.
func TestEpochIgnoresTheInterfaceMetric(t *testing.T) {
	routes := []osnet.Route{defaultRoute("0.0.0.0/0", "192.168.1.1", intelEthernet, 0)}
	var tracker epochTracker
	first := tracker.stamp(buildNetState([]adapter{intelEthernet, realtekWiFi}, routes))
	slowed := tracker.stamp(buildNetState([]adapter{withMetrics(intelEthernet, 30, 30), realtekWiFi}, routes))
	if slowed.Epoch != first.Epoch {
		t.Errorf("a new interface metric moved the epoch from %d to %d", first.Epoch, slowed.Epoch)
	}
}
