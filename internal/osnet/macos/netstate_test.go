package macos

import (
	"net/netip"
	"slices"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

func TestInterfaceClasses(t *testing.T) {
	tests := []struct {
		name     string
		tunnel   bool
		physical bool
	}{
		{"utun10", true, false},
		{"utun0", true, false},
		{"tun0", true, false},
		{"wg0", true, false},
		{"ppp0", true, false},
		{"ipsec0", true, false},
		{"awdl0", false, false},
		{"llw0", false, false},
		{"lo0", false, false},
		{"gif0", false, false},
		{"bridge100", false, false},
		{"anpi0", false, false},
		{"ap1", false, false},
		{"en0", false, true},
		{"en12", false, true},
		{"vmenet0", false, true},
		{"stf0", false, true},
		{"apple0", false, true}, // starts like ap, but is not ap<unit>
		{"wgx0", false, true},
		{"", false, false},
	}
	for _, tt := range tests {
		if got := isTunnel(tt.name); got != tt.tunnel {
			t.Errorf("isTunnel(%q) = %v", tt.name, got)
		}
		if got := isPhysical(tt.name); got != tt.physical {
			t.Errorf("isPhysical(%q) = %v", tt.name, got)
		}
	}
}

func ifaces(names ...string) []osnet.Interface {
	out := make([]osnet.Interface, len(names))
	for i, n := range names {
		out[i] = osnet.Interface{Name: n, Index: i + 1, Up: true, Tunnel: isTunnel(n)}
	}
	return out
}

// The table of the machine the fixtures come from: a WireGuard tunnel on utun10
// has only a scoped default, and so does bridge100, while en0 has the default.
func TestPickDefaultInTheRealTable(t *testing.T) {
	var routes []osnet.Route
	for _, name := range []string{"v4-default-scoped-bridge100", "v4-default-en0", "v4-default-scoped-utun10", "v6-default-scoped-utun0", "v4-net24-en0"} {
		r, ok := routeFromMessage(fixtureMessage(t, ribMessages, name), fixtureIfaceName)
		if !ok {
			t.Fatalf("%s is not a route", name)
		}
		routes = append(routes, r)
	}
	all := []osnet.Interface{
		{Name: "en0", Index: 14}, {Name: "bridge100", Index: 29}, {Name: "utun0", Index: 19}, {Name: "utun10", Index: 50},
	}
	got4 := pickDefault(routes, all, false)
	if want := (osnet.Nexthop{Gateway: netip.MustParseAddr("192.0.2.1"), Iface: "en0"}); got4 == nil || *got4 != want {
		t.Errorf("IPv4 default = %+v, want %+v", got4, want)
	}
	if got6 := pickDefault(routes, all, true); got6 != nil {
		t.Errorf("IPv6 default = %+v, want none: only tunnels have one", got6)
	}
}

func TestPickDefault(t *testing.T) {
	all := ifaces("lo0", "en0", "en1", "utun10", "utun11", "bridge100", "awdl0")
	gw4 := netip.MustParseAddr("192.0.2.1")
	gw6 := netip.MustParseAddr("fe80::1").WithZone("en0")
	tests := []struct {
		name   string
		routes []osnet.Route
		v6     bool
		want   *osnet.Nexthop
	}{
		{"no routes", nil, false, nil},
		{
			name:   "unscoped physical default",
			routes: []osnet.Route{routeOf("0.0.0.0/0", "192.0.2.1", "en0", false)},
			want:   &osnet.Nexthop{Gateway: gw4, Iface: "en0"},
		},
		{
			name: "tunnels, bridges and awdl never win",
			routes: []osnet.Route{
				routeOf("0.0.0.0/0", "", "utun10", false), routeOf("0.0.0.0/0", "", "utun11", true),
				routeOf("0.0.0.0/0", "", "bridge100", true), routeOf("0.0.0.0/0", "", "awdl0", true),
				routeOf("0.0.0.0/0", "", "lo0", true),
			},
			want: nil,
		},
		{
			name: "another VPN took the unscoped default: the scoped one is the way out",
			routes: []osnet.Route{
				routeOf("0.0.0.0/0", "", "utun11", false),
				routeOf("0.0.0.0/0", "192.0.2.1", "en0", true),
			},
			want: &osnet.Nexthop{Gateway: gw4, Iface: "en0"},
		},
		{
			name: "an unscoped default beats a scoped one on a lower interface",
			routes: []osnet.Route{
				routeOf("0.0.0.0/0", "192.0.2.1", "en0", true),
				routeOf("0.0.0.0/0", "192.0.2.254", "en1", false),
			},
			want: &osnet.Nexthop{Gateway: netip.MustParseAddr("192.0.2.254"), Iface: "en1"},
		},
		{
			name: "among scoped defaults the lowest interface index wins",
			routes: []osnet.Route{
				routeOf("0.0.0.0/0", "192.0.2.254", "en1", true),
				routeOf("0.0.0.0/0", "192.0.2.1", "en0", true),
			},
			want: &osnet.Nexthop{Gateway: gw4, Iface: "en0"},
		},
		{
			name:   "a default through the interface only",
			routes: []osnet.Route{routeOf("0.0.0.0/0", "", "en1", false)},
			want:   &osnet.Nexthop{Iface: "en1"},
		},
		{
			name: "blackhole defaults and routes of unknown interface are not next hops",
			routes: []osnet.Route{
				{Dst: netip.MustParsePrefix("0.0.0.0/0"), Blackhole: true, Iface: "en0"},
				{Dst: netip.MustParsePrefix("0.0.0.0/0"), Gateway: gw4},
			},
			want: nil,
		},
		{
			name: "only the default route counts",
			routes: []osnet.Route{
				routeOf("0.0.0.0/1", "192.0.2.1", "en0", false),
				routeOf("192.0.2.0/24", "", "en0", false),
			},
			want: nil,
		},
		{
			name:   "IPv6 default with link-local gateway",
			routes: []osnet.Route{{Dst: netip.MustParsePrefix("::/0"), Gateway: gw6, Iface: "en0"}},
			v6:     true,
			want:   &osnet.Nexthop{Gateway: gw6, Iface: "en0"},
		},
		{
			name:   "families do not mix",
			routes: []osnet.Route{routeOf("::/0", "", "en0", false)},
			v6:     false,
			want:   nil,
		},
		{
			name:   "interface that is gone is last",
			routes: []osnet.Route{routeOf("0.0.0.0/0", "192.0.2.9", "en7", true), routeOf("0.0.0.0/0", "192.0.2.1", "en1", true)},
			want:   &osnet.Nexthop{Gateway: gw4, Iface: "en1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pickDefault(tt.routes, all, tt.v6)
			if (got == nil) != (tt.want == nil) || got != nil && *got != *tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestConnectedSubnets(t *testing.T) {
	list := []osnet.Interface{
		{Name: "lo0", Up: true, Addrs: pfx("127.0.0.1/8", "::1/128", "fe80::1/64")},
		{Name: "en0", Up: true, Addrs: pfx("192.0.2.185/24", "fe80::aa:1d64:6acb:1ea/64", "2001:db8:1::5/64")},
		{Name: "en1", Up: false, Addrs: pfx("198.51.100.7/24")},
		{Name: "en2", Up: true, Addrs: pfx("169.254.3.4/16")},
		{Name: "en3", Up: true, Addrs: pfx("192.0.2.186/24")},
		{Name: "utun10", Up: true, Tunnel: true, Addrs: pfx("203.0.113.2/32")},
		{Name: "bridge100", Up: true, Addrs: pfx("192.168.139.3/23")},
	}
	got := connectedSubnets(list)
	want := pfx("192.0.2.0/24", "2001:db8:1::/64")
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func pfx(s ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(s))
	for i, p := range s {
		out[i] = netip.MustParsePrefix(p)
	}
	return out
}

func TestBuildNetState(t *testing.T) {
	list := []osnet.Interface{
		{Name: "en0", Index: 14, Up: true, Addrs: pfx("192.0.2.185/24")},
		{Name: "utun10", Index: 50, Up: true, Tunnel: true, Addrs: pfx("203.0.113.2/32")},
	}
	routes := []osnet.Route{
		routeOf("0.0.0.0/0", "192.0.2.1", "en0", false),
		routeOf("0.0.0.0/0", "", "utun10", true),
	}
	got := buildNetState(list, routes)
	if got.DefaultV4 == nil || got.DefaultV4.Iface != "en0" || got.DefaultV6 != nil {
		t.Errorf("defaults %+v %+v", got.DefaultV4, got.DefaultV6)
	}
	if !slices.Equal(got.Connected, pfx("192.0.2.0/24")) {
		t.Errorf("connected %v", got.Connected)
	}
	if len(got.Interfaces) != 2 {
		t.Errorf("interfaces %v", got.Interfaces)
	}
}

func TestEpochTracker(t *testing.T) {
	base := func() osnet.NetState {
		return osnet.NetState{
			Interfaces: []osnet.Interface{
				{Name: "en0", Index: 14, Up: true, Addrs: pfx("192.0.2.185/24")},
				{Name: "utun10", Index: 50, Up: true, Tunnel: true, Addrs: pfx("203.0.113.2/32")},
			},
			DefaultV4: &osnet.Nexthop{Gateway: netip.MustParseAddr("192.0.2.1"), Iface: "en0"},
			Connected: pfx("192.0.2.0/24"),
		}
	}
	steps := []struct {
		name   string
		mutate func(*osnet.NetState)
		epoch  uint64
	}{
		{"first state", func(*osnet.NetState) {}, 1},
		{"same state", func(*osnet.NetState) {}, 1},
		{"tunnel address changes", func(s *osnet.NetState) { s.Interfaces[1].Addrs = pfx("203.0.113.3/32") }, 1},
		{"tunnel interface goes", func(s *osnet.NetState) { s.Interfaces = s.Interfaces[:1] }, 1},
		{"physical address changes", func(s *osnet.NetState) { s.Interfaces[0].Addrs = pfx("192.0.2.186/24") }, 2},
		{"gateway changes", func(s *osnet.NetState) { s.DefaultV4.Gateway = netip.MustParseAddr("192.0.2.2") }, 3},
		{"default goes", func(s *osnet.NetState) { s.DefaultV4 = nil }, 4},
		{"IPv6 default appears", func(s *osnet.NetState) { s.DefaultV6 = &osnet.Nexthop{Iface: "en0"} }, 5},
		{"interface goes down", func(s *osnet.NetState) { s.Interfaces[0].Up = false }, 6},
		{"connected subnet changes", func(s *osnet.NetState) { s.Connected = pfx("198.51.100.0/24") }, 7},
		{"physical interface appears", func(s *osnet.NetState) {
			s.Interfaces = append(s.Interfaces, osnet.Interface{Name: "en1", Index: 10, Up: true})
		}, 8},
	}
	var tracker epochTracker
	state := base()
	for _, step := range steps {
		step.mutate(&state)
		// Each step starts from the state the previous one left, as separate values.
		snapshot := state
		snapshot.Interfaces = slices.Clone(state.Interfaces)
		if state.DefaultV4 != nil {
			next := *state.DefaultV4
			snapshot.DefaultV4 = &next
		}
		if got := tracker.stamp(snapshot).Epoch; got != step.epoch {
			t.Errorf("%s: epoch %d, want %d", step.name, got, step.epoch)
		}
	}
}

// A default route whose interface is classified differently is another state
// for whoever compares epochs.
func TestEpochTrackerSeesAKindChange(t *testing.T) {
	state := func(kind osnet.LinkKind) osnet.NetState {
		return osnet.NetState{DefaultV4: &osnet.Nexthop{Gateway: netip.MustParseAddr("192.0.2.1"), Iface: "en0", Kind: kind}}
	}
	var tracker epochTracker
	if got := tracker.stamp(state(osnet.LinkOther)).Epoch; got != 1 {
		t.Fatalf("first epoch %d", got)
	}
	if got := tracker.stamp(state(osnet.LinkOther)).Epoch; got != 1 {
		t.Errorf("same kind: epoch %d, want 1", got)
	}
	if got := tracker.stamp(state(osnet.LinkWiFi)).Epoch; got != 2 {
		t.Errorf("new kind: epoch %d, want 2", got)
	}
}
