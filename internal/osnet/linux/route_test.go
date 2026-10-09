package linux

import (
	"errors"
	"net/netip"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// fixtureNames are the interfaces of the namespace the fixtures come from.
var fixtureNames = map[uint32]string{1: "lo", 2: "d0", 3: "d1", 4: "tun0", 5: "tap0", 6: "wg0", 7: "br0"}

func fixtureIfaceName(index uint32) string { return fixtureNames[index] }

func fixtureIfIndex(name string) (uint32, error) {
	for index, n := range fixtureNames {
		if n == name {
			return index, nil
		}
	}
	return 0, errNoSuchInterface
}

func prefix(s string) netip.Prefix { return netip.MustParsePrefix(s) }

// prefixes parses prefixes separated by spaces.
func prefixes(s string) []netip.Prefix {
	var out []netip.Prefix
	for _, p := range strings.Fields(s) {
		out = append(out, prefix(p))
	}
	return out
}

func routesOf(t testing.TB, name string) []osnet.Route {
	t.Helper()
	return routesFromMessage(fixtureRoute(t, name), fixtureIfaceName)
}

func TestRoutesFromRealMessages(t *testing.T) {
	tests := []struct {
		fixture string
		want    []osnet.Route
	}{
		{"v4-default-dhcp-d0", []osnet.Route{{Dst: prefix("0.0.0.0/0"), Gateway: addr("192.0.2.1"), Iface: "d0", IfIndex: 2, Metric: 100, Static: true, Flags: 0x11000}}},
		{"v4-default-d1", []osnet.Route{{Dst: prefix("0.0.0.0/0"), Gateway: addr("198.18.0.1"), Iface: "d1", IfIndex: 3, Metric: 700, Static: true, Flags: 0x10300}}},
		// What the kernel derives from an address is not Static.
		{"v4-peer-linkdown-tun0", []osnet.Route{{Dst: prefix("10.8.0.1/32"), Iface: "tun0", IfIndex: 4, Flags: 0x102fd}}},
		{"v4-net24-d0", []osnet.Route{{Dst: prefix("192.0.2.0/24"), Iface: "d0", IfIndex: 2, Flags: 0x102fd}}},
		{"v4-dev-tun0", []osnet.Route{{Dst: prefix("198.51.100.0/24"), Iface: "tun0", IfIndex: 4, Metric: 5, Static: true, Flags: 0x1c7fd}}},
		{"v4-gateway-d0", []osnet.Route{{Dst: prefix("198.51.100.64/28"), Gateway: addr("192.0.2.1"), Iface: "d0", IfIndex: 2, Metric: 5, Static: true, Flags: 0x1c700}}},
		{"v4-blackhole", []osnet.Route{{Dst: prefix("198.51.100.128/28"), Blackhole: true, Static: true, Flags: 0x6c700}}},
		{"v4-unreachable", []osnet.Route{{Dst: prefix("198.51.100.144/28"), Blackhole: true, Static: true, Flags: 0x70300}}},
		{"v4-multipath", []osnet.Route{
			{Dst: prefix("203.0.113.0/24"), Gateway: addr("192.0.2.1"), Iface: "d0", IfIndex: 2, Static: true, Flags: 0x10300},
			{Dst: prefix("203.0.113.0/24"), Gateway: addr("192.0.2.3"), Iface: "d0", IfIndex: 2, Static: true, Flags: 0x10300},
		}},
		{"v6-net64-d0", []osnet.Route{{Dst: prefix("2001:db8:1::/64"), Iface: "d0", IfIndex: 2, Metric: 256, Flags: 0x10200}}},
		{"v6-gateway-global", []osnet.Route{{Dst: prefix("2001:db8:9::/64"), Gateway: addr("2001:db8:1::1"), Iface: "d0", IfIndex: 2, Metric: 5, Static: true, Flags: 0x1c700}}},
		{"v6-dev-tun0", []osnet.Route{{Dst: prefix("2001:db8:a::/64"), Iface: "tun0", IfIndex: 4, Metric: 5, Static: true, Flags: 0x1c700}}},
		// A link-local gateway names its interface as its zone.
		{"v6-multipath", []osnet.Route{
			{Dst: prefix("2001:db8:b::/64"), Gateway: addr("fe80::1%d0"), Iface: "d0", IfIndex: 2, Metric: 1024, Static: true, Flags: 0x10300},
			{Dst: prefix("2001:db8:b::/64"), Gateway: addr("fe80::2%d0"), Iface: "d0", IfIndex: 2, Metric: 1024, Static: true, Flags: 0x10300},
		}},
		// IPv6 puts a blackhole on loopback.
		{"v6-blackhole", []osnet.Route{{Dst: prefix("2001:db8:ffff::/48"), Iface: "lo", IfIndex: 1, Metric: 1024, Blackhole: true, Static: true, Flags: 0x6c700}}},
		{"v6-linklocal-net64-d0", []osnet.Route{{Dst: prefix("fe80::/64"), Iface: "d0", IfIndex: 2, Metric: 256, Flags: 0x10200}}},
		{"v6-default-gateway-linklocal", []osnet.Route{{Dst: prefix("::/0"), Gateway: addr("fe80::1%d0"), Iface: "d0", IfIndex: 2, Metric: 5, Static: true, Flags: 0x1c700}}},
		// A route from a router advertisement is the kernel's, not an administrator's.
		{"v6-default-ra", []osnet.Route{{Dst: prefix("::/0"), Gateway: addr("fe80::1%d0"), Iface: "d0", IfIndex: 2, Metric: 1024, Flags: 0x10900}}},

		// Not in the main table, or bookkeeping of the kernel.
		{"v4-table100-gateway", nil},
		{"v4-local", nil},
		{"v4-broadcast", nil},
		{"v6-local", nil},
		{"v6-multicast-local", nil},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			got := routesOf(t, tt.fixture)
			if !slices.Equal(got, tt.want) {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestRoutesFromMessageSkipsWhatDumpDoesNotList(t *testing.T) {
	base := routeMsg{Family: afInet, DstLen: 24, Table: rtTableMain, Type: rtnUnicast, Dst: addr("192.0.2.0"), OIf: 2}
	if got := routesFromMessage(base, fixtureIfaceName); len(got) != 1 {
		t.Fatalf("the base route gives %+v", got)
	}
	tests := map[string]func(m *routeMsg){
		"another table":                   func(m *routeMsg) { m.Table = 100 },
		"a table above 255":               func(m *routeMsg) { m.Table = 1000 },
		"a cache entry":                   func(m *routeMsg) { m.Flags |= rtmFCloned },
		"local":                           func(m *routeMsg) { m.Type = 2 },
		"broadcast":                       func(m *routeMsg) { m.Type = 3 },
		"anycast":                         func(m *routeMsg) { m.Type = 4 },
		"multicast":                       func(m *routeMsg) { m.Type = 5 },
		"throw":                           func(m *routeMsg) { m.Type = 9 },
		"nat":                             func(m *routeMsg) { m.Type = 10 },
		"another family":                  func(m *routeMsg) { m.Family = 28 },
		"IPv6 next hop of IPv4":           func(m *routeMsg) { m.Via = true },
		"gateway of the wrong family":     func(m *routeMsg) { m.Gateway = addr("fe80::1") },
		"destination of the wrong family": func(m *routeMsg) { m.Dst = addr("2001:db8::") },
		"prefix length too long":          func(m *routeMsg) { m.DstLen = 40 },
		"no destination, but a prefix":    func(m *routeMsg) { m.Dst = netip.Addr{} },
		"a nexthop object the kernel did not spell out": func(m *routeMsg) { m.OIf, m.NHID = 0, true },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			m := base
			mutate(&m)
			if got := routesFromMessage(m, fixtureIfaceName); len(got) != 0 {
				t.Errorf("got %+v", got)
			}
		})
	}
}

func TestRoutesFromMessageDetails(t *testing.T) {
	t.Run("host bits of the destination are cleared", func(t *testing.T) {
		m := routeMsg{Family: afInet, DstLen: 24, Table: rtTableMain, Type: rtnUnicast, Dst: addr("192.0.2.77"), OIf: 2}
		if got := routesFromMessage(m, fixtureIfaceName); len(got) != 1 || got[0].Dst != prefix("192.0.2.0/24") {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("an interface the kernel no longer has is left unnamed", func(t *testing.T) {
		m := routeMsg{Family: afInet6, Table: rtTableMain, Type: rtnUnicast, Gateway: addr("fe80::1"), OIf: 99}
		got := routesFromMessage(m, fixtureIfaceName)
		if len(got) != 1 || got[0].Iface != "" || got[0].IfIndex != 99 || got[0].Gateway != addr("fe80::1") {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("a next hop through another family is skipped, the others stay", func(t *testing.T) {
		m := routeMsg{Family: afInet, DstLen: 24, Table: rtTableMain, Type: rtnUnicast, Dst: addr("192.0.2.0"), NextHops: []nextHop{
			{OIf: 2, Gateway: addr("192.0.2.1")}, {OIf: 2, Via: true}, {OIf: 3, Gateway: addr("198.18.0.1")},
		}}
		got := routesFromMessage(m, fixtureIfaceName)
		if len(got) != 2 || got[0].Gateway != addr("192.0.2.1") || got[1].Iface != "d1" {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("a global IPv6 gateway has no zone", func(t *testing.T) {
		m := routeMsg{Family: afInet6, Table: rtTableMain, Type: rtnUnicast, Gateway: addr("2001:db8:1::1"), OIf: 2}
		if got := routesFromMessage(m, fixtureIfaceName); len(got) != 1 || got[0].Gateway.Zone() != "" {
			t.Errorf("got %+v", got)
		}
	})
}

func TestSortRoutes(t *testing.T) {
	r := func(dst string, metric, ifIndex uint32, gateway string) osnet.Route {
		route := osnet.Route{Dst: prefix(dst), Metric: metric, IfIndex: ifIndex}
		if gateway != "" {
			route.Gateway = addr(gateway)
		}
		return route
	}
	want := []osnet.Route{
		r("0.0.0.0/0", 100, 2, "192.0.2.1"),
		r("0.0.0.0/0", 700, 3, "198.18.0.1"),
		r("10.0.0.0/8", 0, 4, ""),
		r("10.0.0.0/16", 0, 4, ""),
		r("10.0.0.0/16", 5, 2, ""),
		r("10.0.0.0/16", 5, 4, ""),
		r("192.0.2.0/24", 0, 2, ""),
		r("::/0", 5, 2, "fe80::1"),
		r("::/0", 5, 2, "fe80::2"),
		r("2001:db8::/32", 1024, 2, ""),
	}
	shuffled := slices.Clone(want)
	slices.Reverse(shuffled)
	shuffled[1], shuffled[6] = shuffled[6], shuffled[1]
	sortRoutes(shuffled)
	if !slices.Equal(shuffled, want) {
		t.Errorf("got  %+v\nwant %+v", shuffled, want)
	}
}

func TestNewRouteRequest(t *testing.T) {
	const (
		add    = nlmFCreate | nlmFExcl
		create = rtmNewRoute
		remove = rtmDelRoute
	)
	tests := []struct {
		name  string
		typ   uint16
		route osnet.Route
		want  routeRequest
	}{
		{
			name:  "add through a gateway",
			typ:   create,
			route: osnet.Route{Dst: prefix("198.51.100.0/24"), Gateway: addr("192.0.2.1"), IfIndex: 2, Metric: 5},
			want:  routeRequest{Type: create, Flags: add, Dst: prefix("198.51.100.0/24"), Gateway: addr("192.0.2.1"), OIf: 2, Metric: 5, RouteType: rtnUnicast, Protocol: RouteProtocol},
		},
		{
			name:  "add bound to an interface named by index",
			typ:   create,
			route: osnet.Route{Dst: prefix("198.51.100.0/24"), IfIndex: 4, Iface: "d0", Metric: 5},
			want:  routeRequest{Type: create, Flags: add, Dst: prefix("198.51.100.0/24"), OIf: 4, Metric: 5, RouteType: rtnUnicast, Protocol: RouteProtocol, Scope: rtScopeLink},
		},
		{
			name:  "add bound to an interface named by name",
			typ:   create,
			route: osnet.Route{Dst: prefix("198.51.100.0/24"), Iface: "tun0"},
			want:  routeRequest{Type: create, Flags: add, Dst: prefix("198.51.100.0/24"), OIf: 4, RouteType: rtnUnicast, Protocol: RouteProtocol, Scope: rtScopeLink},
		},
		{
			name:  "add through a gateway without naming the interface",
			typ:   create,
			route: osnet.Route{Dst: prefix("198.51.100.7/32"), Gateway: addr("192.0.2.1"), Metric: 1},
			want:  routeRequest{Type: create, Flags: add, Dst: prefix("198.51.100.7/32"), Gateway: addr("192.0.2.1"), Metric: 1, RouteType: rtnUnicast, Protocol: RouteProtocol},
		},
		{
			name:  "host bits are cleared",
			typ:   create,
			route: osnet.Route{Dst: prefix("198.51.100.77/24"), IfIndex: 2},
			want:  routeRequest{Type: create, Flags: add, Dst: prefix("198.51.100.0/24"), OIf: 2, RouteType: rtnUnicast, Protocol: RouteProtocol, Scope: rtScopeLink},
		},
		{
			name:  "default route over a link-local gateway",
			typ:   create,
			route: osnet.Route{Dst: prefix("::/0"), Gateway: addr("fe80::1%d0"), IfIndex: 2, Metric: 5},
			want:  routeRequest{Type: create, Flags: add, Dst: prefix("::/0"), Gateway: addr("fe80::1"), OIf: 2, Metric: 5, RouteType: rtnUnicast, Protocol: RouteProtocol},
		},
		{
			name:  "the zone of a link-local gateway names the interface",
			typ:   create,
			route: osnet.Route{Dst: prefix("::/0"), Gateway: addr("fe80::1%tun0")},
			want:  routeRequest{Type: create, Flags: add, Dst: prefix("::/0"), Gateway: addr("fe80::1"), OIf: 4, RouteType: rtnUnicast, Protocol: RouteProtocol},
		},
		{
			name:  "blackhole",
			typ:   create,
			route: osnet.Route{Dst: prefix("198.51.100.128/28"), Blackhole: true, Gateway: addr("192.0.2.1"), Iface: "d0"},
			want:  routeRequest{Type: create, Flags: add, Dst: prefix("198.51.100.128/28"), RouteType: rtnBlackhole, Protocol: RouteProtocol},
		},
		{
			name:  "delete names destination, interface, gateway and metric",
			typ:   remove,
			route: osnet.Route{Dst: prefix("198.51.100.0/24"), Gateway: addr("192.0.2.1"), IfIndex: 2, Metric: 5, Static: true, Flags: 0x1c700},
			want:  routeRequest{Type: remove, Dst: prefix("198.51.100.0/24"), Gateway: addr("192.0.2.1"), OIf: 2, Metric: 5, RouteType: rtnUnicast, Scope: rtScopeNowhere},
		},
		{
			name:  "delete of an interface-bound route leaves the scope open",
			typ:   remove,
			route: osnet.Route{Dst: prefix("198.51.100.0/24"), IfIndex: 4, Metric: 5},
			want:  routeRequest{Type: remove, Dst: prefix("198.51.100.0/24"), OIf: 4, Metric: 5, RouteType: rtnUnicast, Scope: rtScopeNowhere},
		},
		{
			name:  "delete of a blackhole",
			typ:   remove,
			route: osnet.Route{Dst: prefix("198.51.100.128/28"), Blackhole: true},
			want:  routeRequest{Type: remove, Dst: prefix("198.51.100.128/28"), RouteType: rtnBlackhole, Scope: rtScopeNowhere},
		},
		{
			name:  "delete of an unreachable route that Dump returned",
			typ:   remove,
			route: osnet.Route{Dst: prefix("198.51.100.144/28"), Blackhole: true, Flags: 0x70300},
			want:  routeRequest{Type: remove, Dst: prefix("198.51.100.144/28"), RouteType: rtnUnreachable, Scope: rtScopeNowhere},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := newRouteRequest(tt.typ, tt.route, fixtureIfIndex)
			if err != nil || got != tt.want {
				t.Errorf("got  %+v (err %v)\nwant %+v", got, err, tt.want)
			}
		})
	}
}

func TestNewRouteRequestRefuses(t *testing.T) {
	tests := []struct {
		name  string
		route osnet.Route
		want  error // nil: any error
	}{
		{"no destination", osnet.Route{Iface: "d0"}, nil},
		{"unknown interface", osnet.Route{Dst: prefix("198.51.100.0/24"), Iface: "nosuch"}, errNoSuchInterface},
		{"gateway of the other family", osnet.Route{Dst: prefix("198.51.100.0/24"), Gateway: addr("fe80::1"), IfIndex: 2}, nil},
		{"IPv6 destination, IPv4 gateway", osnet.Route{Dst: prefix("2001:db8::/32"), Gateway: addr("192.0.2.1"), IfIndex: 2}, nil},
		{"neither gateway nor interface", osnet.Route{Dst: prefix("198.51.100.0/24")}, nil},
		{"link-local gateway without an interface", osnet.Route{Dst: prefix("::/0"), Gateway: addr("fe80::1")}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, typ := range []uint16{rtmNewRoute, rtmDelRoute} {
				req, err := newRouteRequest(typ, tt.route, fixtureIfIndex)
				if err == nil || (tt.want != nil && !errors.Is(err, tt.want)) {
					t.Errorf("type %d: got %+v, err %v", typ, req, err)
				}
			}
		})
	}
}

// A route that Dump returned can be deleted: the request names what Dump
// reported, with the zone of a link-local gateway removed.
func TestDeleteRequestOfDumpedRoutes(t *testing.T) {
	for name := range routeMessages {
		for _, r := range routesOf(t, name) {
			req, err := newRouteRequest(rtmDelRoute, r, fixtureIfIndex)
			if err != nil {
				t.Errorf("%s: %v", name, err)
				continue
			}
			wantOIf := r.IfIndex
			if r.Blackhole {
				wantOIf = 0
			}
			if req.Dst != r.Dst || req.OIf != wantOIf || req.Metric != r.Metric || req.Gateway != r.Gateway.WithZone("") ||
				req.Protocol != 0 || req.Scope != rtScopeNowhere {
				t.Errorf("%s: request %+v for route %+v", name, req, r)
			}
		}
	}
}

func TestRouteError(t *testing.T) {
	other := errors.New("boom")
	tests := []struct {
		name string
		op   string
		err  error
		want error
	}{
		{"route exists", opAdd, syscall.EEXIST, osnet.ErrExists},
		{"route does not exist", opDelete, syscall.ESRCH, osnet.ErrNotFound},
		{"gateway unreachable (IPv4)", opAdd, syscall.ENETUNREACH, osnet.ErrUnreachable},
		{"gateway unreachable (IPv6)", opAdd, syscall.EHOSTUNREACH, osnet.ErrUnreachable},
		{"interface down", opAdd, syscall.ENETDOWN, osnet.ErrUnreachable},
		{"interface gone, add", opAdd, syscall.ENODEV, osnet.ErrUnreachable},
		{"interface gone, delete", opDelete, syscall.ENODEV, osnet.ErrNotFound},
		{"interface unknown, delete", opDelete, errNoSuchInterface, osnet.ErrNotFound},
		{"no such process on add", opAdd, syscall.ESRCH, osnet.ErrNotFound},
		{"explained by the kernel", opAdd, &netlinkErrorLike{syscall.EEXIST}, osnet.ErrExists},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := routeError(tt.op, prefix("198.51.100.0/24"), tt.err); !errors.Is(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
	for _, c := range []struct {
		op  string
		err error
	}{
		{opAdd, errNoSuchInterface},
		{opAdd, syscall.EINVAL},
		{opDelete, syscall.EPERM},
		{opAdd, other},
	} {
		got := routeError(c.op, prefix("198.51.100.0/24"), c.err)
		if !errors.Is(got, c.err) || errors.Is(got, osnet.ErrNotFound) || errors.Is(got, osnet.ErrExists) || errors.Is(got, osnet.ErrUnreachable) {
			t.Errorf("%s %v became %v", c.op, c.err, got)
		}
		if msg := got.Error(); msg[:len(c.op)] != c.op {
			t.Errorf("error %q does not start with the operation", msg)
		}
	}
}

// What an interface that is down or gone says keeps the errno and the kernel's
// words, which the Reconciler shows; IPv6 switched off is a matter of the
// host's settings only for IPv6 routes.
func TestRouteErrorKeepsWhatTheKernelSaid(t *testing.T) {
	got := routeError(opAdd, prefix("198.51.100.0/24"), &netlinkErrorLike{syscall.ENETDOWN})
	if !errors.Is(got, osnet.ErrUnreachable) || !errors.Is(got, syscall.ENETDOWN) || !strings.Contains(got.Error(), "(explained)") {
		t.Errorf("got %v", got)
	}
	v6 := routeError(opAdd, prefix("2001:db8::/32"), &netlinkErrorLike{syscall.EACCES})
	if !errors.Is(v6, errors.ErrUnsupported) || !errors.Is(v6, syscall.EACCES) || v6.Error() != "add route 2001:db8::/32: permission denied (explained)" {
		t.Errorf("got %q", v6)
	}
	if v4 := routeError(opAdd, prefix("198.51.100.0/24"), syscall.EACCES); errors.Is(v4, errors.ErrUnsupported) {
		t.Errorf("a refusal of an IPv4 route is not about IPv6: %v", v4)
	}
	if del := routeError(opDelete, prefix("2001:db8::/32"), syscall.EACCES); errors.Is(del, errors.ErrUnsupported) {
		t.Errorf("a refusal to delete says nothing about IPv6: %v", del)
	}
}

// netlinkErrorLike wraps an errno the way a kernel refusal does.
type netlinkErrorLike struct{ errno syscall.Errno }

func (e *netlinkErrorLike) Error() string { return e.errno.Error() + " (explained)" }
func (e *netlinkErrorLike) Unwrap() error { return e.errno }
