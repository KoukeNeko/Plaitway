package linux

import (
	"net/netip"
	"slices"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// uplink is an interface that is up and can carry the default route.
func uplink(name string, index int, addrs ...string) link {
	l := link{Interface: osnet.Interface{Name: name, Index: index, Up: true}, Uplink: true}
	for _, a := range addrs {
		l.Addrs = append(l.Addrs, prefix(a))
	}
	return l
}

func tunnelLink(name string, index int, addrs ...string) link {
	l := uplink(name, index, addrs...)
	l.Tunnel, l.Uplink = true, false
	return l
}

func (l link) down() link { l.Up = false; return l }

func defaultRoute(dst, gateway string, ifIndex, metric uint32) osnet.Route {
	r := osnet.Route{Dst: prefix(dst), IfIndex: ifIndex, Metric: metric}
	if gateway != "" {
		r.Gateway = addr(gateway)
	}
	return r
}

func TestPickDefault(t *testing.T) {
	links := []link{
		{Interface: osnet.Interface{Name: "lo", Index: 1, Up: true}},
		uplink("eth0", 2), uplink("eth1", 3), uplink("wlan0", 4).down(),
		tunnelLink("wg0", 5), tunnelLink("tun0", 6),
		uplink("br0", 7),
		{Interface: osnet.Interface{Name: "ppp0", Index: 8, Up: true, Tunnel: true}, Uplink: true},
	}
	gw4, gw6 := addr("192.0.2.1"), addr("fe80::1%eth0")
	tests := []struct {
		name   string
		routes []osnet.Route
		v6     bool
		want   *osnet.Nexthop
	}{
		{"no routes", nil, false, nil},
		{
			name:   "one default",
			routes: []osnet.Route{defaultRoute("0.0.0.0/0", "192.0.2.1", 2, 100)},
			want:   &osnet.Nexthop{Gateway: gw4, Iface: "eth0"},
		},
		{
			name: "the lowest metric wins",
			routes: []osnet.Route{
				defaultRoute("0.0.0.0/0", "198.18.0.1", 3, 700),
				defaultRoute("0.0.0.0/0", "192.0.2.1", 2, 100),
				defaultRoute("0.0.0.0/0", "198.19.0.1", 7, 200),
			},
			want: &osnet.Nexthop{Gateway: gw4, Iface: "eth0"},
		},
		{
			name: "the lowest metric wins even from a higher index",
			routes: []osnet.Route{
				defaultRoute("0.0.0.0/0", "192.0.2.1", 2, 700),
				defaultRoute("0.0.0.0/0", "198.18.0.1", 3, 100),
			},
			want: &osnet.Nexthop{Gateway: addr("198.18.0.1"), Iface: "eth1"},
		},
		{
			name: "equal metrics: the lowest interface index",
			routes: []osnet.Route{
				defaultRoute("0.0.0.0/0", "198.18.0.1", 3, 100),
				defaultRoute("0.0.0.0/0", "192.0.2.1", 2, 100),
			},
			want: &osnet.Nexthop{Gateway: gw4, Iface: "eth0"},
		},
		{
			name: "equal metrics on one interface: the lowest gateway, so the pick is stable",
			routes: []osnet.Route{
				defaultRoute("0.0.0.0/0", "192.0.2.9", 2, 100),
				defaultRoute("0.0.0.0/0", "192.0.2.1", 2, 100),
			},
			want: &osnet.Nexthop{Gateway: gw4, Iface: "eth0"},
		},
		{
			name: "a tunnel never wins, whatever its metric",
			routes: []osnet.Route{
				defaultRoute("0.0.0.0/0", "", 5, 0),
				defaultRoute("0.0.0.0/0", "", 6, 1),
				defaultRoute("0.0.0.0/0", "192.0.2.1", 2, 100),
			},
			want: &osnet.Nexthop{Gateway: gw4, Iface: "eth0"},
		},
		{
			name:   "only tunnels, loopback and interfaces that are down: no way out",
			routes: []osnet.Route{defaultRoute("0.0.0.0/0", "", 5, 0), defaultRoute("0.0.0.0/0", "", 1, 0), defaultRoute("0.0.0.0/0", "192.0.2.1", 4, 1)},
			want:   nil,
		},
		{
			name:   "an interface the kernel did not list",
			routes: []osnet.Route{defaultRoute("0.0.0.0/0", "192.0.2.1", 99, 1)},
			want:   nil,
		},
		{
			name:   "a default route without a gateway is on-link",
			routes: []osnet.Route{defaultRoute("0.0.0.0/0", "", 2, 100)},
			want:   &osnet.Nexthop{Iface: "eth0"},
		},
		{
			name:   "a PPP link is a way out although the Reconciler counts it as a tunnel",
			routes: []osnet.Route{defaultRoute("0.0.0.0/0", "", 8, 0)},
			want:   &osnet.Nexthop{Iface: "ppp0"},
		},
		{
			name:   "a bridge is a way out",
			routes: []osnet.Route{defaultRoute("0.0.0.0/0", "172.17.0.254", 7, 0)},
			want:   &osnet.Nexthop{Gateway: addr("172.17.0.254"), Iface: "br0"},
		},
		{
			name: "a blackhole default is not a way out",
			routes: []osnet.Route{
				{Dst: prefix("0.0.0.0/0"), Blackhole: true, IfIndex: 2},
				defaultRoute("0.0.0.0/0", "198.18.0.1", 3, 700),
			},
			want: &osnet.Nexthop{Gateway: addr("198.18.0.1"), Iface: "eth1"},
		},
		{
			name: "only the default route counts",
			routes: []osnet.Route{
				defaultRoute("0.0.0.0/1", "192.0.2.1", 2, 0), defaultRoute("10.0.0.0/8", "192.0.2.1", 2, 0), defaultRoute("::/0", "fe80::1", 2, 0),
			},
			want: nil,
		},
		{
			name:   "IPv6 default over a link-local gateway keeps the zone",
			routes: []osnet.Route{{Dst: prefix("::/0"), Gateway: gw6, IfIndex: 2, Metric: 1024}},
			v6:     true,
			want:   &osnet.Nexthop{Gateway: gw6, Iface: "eth0"},
		},
		{
			name: "the families are separate",
			routes: []osnet.Route{
				defaultRoute("0.0.0.0/0", "192.0.2.1", 2, 100),
				{Dst: prefix("::/0"), Gateway: addr("fe80::2%eth1"), IfIndex: 3, Metric: 1024},
			},
			v6:   true,
			want: &osnet.Nexthop{Gateway: addr("fe80::2%eth1"), Iface: "eth1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pickDefault(tt.routes, links, tt.v6)
			if (got == nil) != (tt.want == nil) || got != nil && *got != *tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestConnectedSubnets(t *testing.T) {
	links := []link{
		{Interface: osnet.Interface{Name: "lo", Index: 1, Up: true, Addrs: []netip.Prefix{prefix("127.0.0.1/8"), prefix("::1/128")}}},
		uplink("eth0", 2, "192.0.2.2/24", "2001:db8:1::2/64", "fe80::1/64", "169.254.3.4/16"),
		uplink("eth1", 3, "192.0.2.77/24"),                       // the same subnet as eth0
		uplink("br0", 7, "172.17.0.1/16"),                        // docker0 and the bridges of virtual machines count
		uplink("wlan0", 4, "198.18.0.2/24").down(),               // down
		tunnelLink("wg0", 5, "10.9.0.2/32"),                      // a tunnel
		uplink("eth2", 8, "203.0.113.5/32", "2001:db8:5::5/128"), // a single address is a subnet too
	}
	want := []netip.Prefix{prefix("172.17.0.0/16"), prefix("192.0.2.0/24"), prefix("203.0.113.5/32"), prefix("2001:db8:1::/64"), prefix("2001:db8:5::5/128")}
	if got := connectedSubnets(links); !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := connectedSubnets(nil); len(got) != 0 {
		t.Errorf("got %v for no interfaces", got)
	}
}

func TestBuildNetState(t *testing.T) {
	links := []link{uplink("eth0", 2, "192.0.2.2/24"), tunnelLink("wg0", 5, "10.9.0.2/32")}
	routes := []osnet.Route{defaultRoute("0.0.0.0/0", "192.0.2.1", 2, 100), defaultRoute("0.0.0.0/0", "", 5, 5)}
	state := buildNetState(links, routes)
	if len(state.Interfaces) != 2 || state.Interfaces[1].Name != "wg0" || !state.Interfaces[1].Tunnel {
		t.Errorf("interfaces %+v", state.Interfaces)
	}
	if state.DefaultV4 == nil || state.DefaultV4.Iface != "eth0" || state.DefaultV6 != nil {
		t.Errorf("defaults %+v, %+v", state.DefaultV4, state.DefaultV6)
	}
	if !slices.Equal(state.Connected, []netip.Prefix{prefix("192.0.2.0/24")}) {
		t.Errorf("connected %v", state.Connected)
	}
	if state.Epoch != 0 {
		t.Errorf("epoch %d: that is the tracker's business", state.Epoch)
	}
}

func TestEpochTracker(t *testing.T) {
	base := func() ([]link, []osnet.Route) {
		return []link{uplink("eth0", 2, "192.0.2.2/24", "2001:db8:1::2/64"), uplink("eth1", 3, "198.18.0.2/24"), tunnelLink("wg0", 5, "10.9.0.2/32")},
			[]osnet.Route{defaultRoute("0.0.0.0/0", "192.0.2.1", 2, 100)}
	}
	var tracker epochTracker
	stamp := func(links []link, routes []osnet.Route) uint64 {
		return tracker.stamp(links, buildNetState(links, routes)).Epoch
	}
	links, routes := base()
	first := stamp(links, routes)
	if first != 1 {
		t.Fatalf("first epoch %d, want 1", first)
	}
	if again := stamp(links, routes); again != first {
		t.Errorf("epoch moved from %d to %d without a change", first, again)
	}

	moves := []struct {
		name  string
		edit  func(links []link, routes []osnet.Route) ([]link, []osnet.Route)
		moved bool
	}{
		{"a tunnel comes up with other addresses", func(l []link, r []osnet.Route) ([]link, []osnet.Route) {
			l[2] = tunnelLink("wg0", 5, "10.9.0.3/32", "10.9.1.0/24")
			return l, r
		}, false},
		{"a tunnel appears", func(l []link, r []osnet.Route) ([]link, []osnet.Route) {
			return append(l, tunnelLink("tun0", 9, "10.8.0.2/24")), r
		}, false},
		{"loopback gets an address", func(l []link, r []osnet.Route) ([]link, []osnet.Route) {
			return append(l, link{Interface: osnet.Interface{Name: "lo", Index: 1, Up: true, Addrs: []netip.Prefix{prefix("127.0.0.1/8")}}}), r
		}, false},
		{"the gateway changes", func(l []link, r []osnet.Route) ([]link, []osnet.Route) {
			r[0].Gateway = addr("192.0.2.2")
			return l, r
		}, true},
		{"the default moves to another interface", func(l []link, r []osnet.Route) ([]link, []osnet.Route) {
			r[0].IfIndex, r[0].Metric = 3, 50
			return l, r
		}, true},
		{"the default goes", func(l []link, r []osnet.Route) ([]link, []osnet.Route) { return l, nil }, true},
		{"an uplink gets an address", func(l []link, r []osnet.Route) ([]link, []osnet.Route) {
			l[1] = uplink("eth1", 3, "198.18.0.2/24", "198.19.0.2/24")
			return l, r
		}, true},
		{"an uplink goes down", func(l []link, r []osnet.Route) ([]link, []osnet.Route) {
			l[1] = l[1].down()
			return l, r
		}, true},
		{"an uplink appears", func(l []link, r []osnet.Route) ([]link, []osnet.Route) {
			return append(l, uplink("br0", 7)), r
		}, true},
	}
	for _, step := range moves {
		links, routes := base()
		links, routes = step.edit(links, routes)
		var tr epochTracker
		before := tr.stamp(mustBase(base), buildNetState(mustBase(base), mustRoutes(base))).Epoch
		got := tr.stamp(links, buildNetState(links, routes)).Epoch
		if moved := got != before; moved != step.moved {
			t.Errorf("%s: epoch %d -> %d, moved = %v, want %v", step.name, before, got, moved, step.moved)
		}
	}
}

func mustBase(base func() ([]link, []osnet.Route)) []link          { l, _ := base(); return l }
func mustRoutes(base func() ([]link, []osnet.Route)) []osnet.Route { _, r := base(); return r }
