package reconciler

import (
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/osnet/fake"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// An Ethernet-like adapter (tap-windows6) answers only for the tunnel's own
// address, so a tunnel route without a next hop sends traffic nowhere while the
// tunnel reports itself up. The routes of a tunnel that announced a gateway go
// through it on Windows; macOS routes are bound to the interface as always.

// withGateway gives the intent the next hop of the family the address belongs to.
func withGateway(in tunnel.Intent, gateway string) tunnel.Intent {
	switch {
	case gateway == "":
	case ip(gateway).Unmap().Is4():
		in.Gateway = ip(gateway)
	default:
		in.GatewayV6 = ip(gateway)
	}
	return in
}

// tapIntent is an OpenVPN full tunnel on a TAP adapter, with a route of its own
// besides the default routes of both families.
func tapIntent(gateway string) tunnel.Intent {
	in := up("ovpn", 1, "utun11", tunnel.RoleFull, "0.0.0.0/0", "::/0", "10.20.0.0/16")
	return withGateway(endpoints(in, "198.51.100.7"), gateway)
}

// addTap creates the adapter with the addresses of a dual-stack tunnel.
func (e *env) addTap() {
	e.host.AddTunnel("utun11", pfx("10.8.0.6/24"), pfx("fd00:8::2/64"))
	e.change(osnet.ChangeRoute)
}

func TestTunnelRoutesUseTheTunnelGateway(t *testing.T) {
	eachKeying(t, testTunnelRoutesUseTheTunnelGateway)
}

func testTunnelRoutesUseTheTunnelGateway(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	e.addTap()

	e.announce(tapIntent("10.8.0.1"))

	// The gateway is an IPv4 address: the IPv6 routes have no next hop of that
	// family and stay on-link.
	via := ""
	if k.windows() {
		via = " via 10.8.0.1"
	}
	e.checkTable(
		"0.0.0.0/1"+via+" dev utun11",
		"10.20.0.0/16"+via+" dev utun11",
		"128.0.0.0/1"+via+" dev utun11",
		"198.51.100.7/32 via 192.168.51.1 dev en0",
		"::/1 dev utun11",
		"8000::/1 dev utun11",
	)
	for _, prefix := range []string{"0.0.0.0/1", "10.20.0.0/16", "128.0.0.0/1", "::/1"} {
		if rr := e.routeReport(prefix, "ovpn"); rr.State != tunnel.RouteInstalled || rr.Via != "utun11" {
			t.Errorf("%s: %+v, want installed and reported through the interface", prefix, rr)
		}
	}
	if rt := e.lookup("8.8.8.8"); k.windows() && (rt.Gateway != ip("10.8.0.1") || rt.Iface != "utun11") {
		t.Errorf("traffic leaves through %+v, want the gateway on the adapter", rt)
	}
}

func TestTunnelWithoutAGatewayStaysOnLink(t *testing.T) {
	for _, gateway := range []string{"", "0.0.0.0", "::", "224.0.0.1", "127.0.0.1"} {
		t.Run("gateway "+gateway, func(t *testing.T) {
			e := newEnv(t, windowsCase)
			e.addTap()

			e.announce(tapIntent(gateway))

			e.checkTable(
				"0.0.0.0/1 dev utun11",
				"10.20.0.0/16 dev utun11",
				"128.0.0.0/1 dev utun11",
				"198.51.100.7/32 via 192.168.51.1 dev en0",
				"::/1 dev utun11",
				"8000::/1 dev utun11",
			)
		})
	}
}

// The routes of a family go through that family's gateway. A tap-windows6
// adapter answers only for its own addresses, so a route of a family that has no
// gateway is not installed (and says why), except the halves of a default route,
// which are installed on-link so that nothing leaks around the tunnel.
func TestTunnelGatewaysPerFamily(t *testing.T) {
	for _, tt := range []struct {
		name       string
		gateways   []string
		want       []string
		v4NotShown string // detail of 10.20.0.0/16 when it is not installed
	}{
		{"IPv6 only, global", []string{"fd00:8::1"}, []string{
			"0.0.0.0/1 dev utun11", "128.0.0.0/1 dev utun11", "198.51.100.7/32 via 192.168.51.1 dev en0",
			"::/1 via fd00:8::1 dev utun11", "8000::/1 via fd00:8::1 dev utun11",
		}, "no IPv4 gateway in the tunnel"},
		{"IPv6 only, link-local", []string{"fe80::1"}, []string{
			"0.0.0.0/1 dev utun11", "128.0.0.0/1 dev utun11", "198.51.100.7/32 via 192.168.51.1 dev en0",
			"::/1 via fe80::1 dev utun11", "8000::/1 via fe80::1 dev utun11",
		}, "no IPv4 gateway in the tunnel"},
		{"dual stack", []string{"10.8.0.1", "fd00:8::1"}, []string{
			"0.0.0.0/1 via 10.8.0.1 dev utun11", "10.20.0.0/16 via 10.8.0.1 dev utun11", "128.0.0.0/1 via 10.8.0.1 dev utun11",
			"198.51.100.7/32 via 192.168.51.1 dev en0",
			"::/1 via fd00:8::1 dev utun11", "8000::/1 via fd00:8::1 dev utun11",
		}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, windowsCase)
			e.addTap()
			intent := tapIntent("")
			for _, gateway := range tt.gateways {
				intent = withGateway(intent, gateway)
			}

			e.announce(intent)

			e.checkTable(tt.want...)
			if rr := e.routeReport("::/1", "ovpn"); rr.State != tunnel.RouteInstalled {
				t.Errorf("::/1: %+v", rr)
			}
			if tt.v4NotShown != "" {
				if rr := e.routeReport("10.20.0.0/16", "ovpn"); rr.State == tunnel.RouteInstalled || rr.Detail != tt.v4NotShown {
					t.Errorf("10.20.0.0/16: %+v, want it not installed with %q", rr, tt.v4NotShown)
				}
			}
			e.r.Withdraw("ovpn")
			e.checkTable()
		})
	}
}

func TestTunnelGatewayChoice(t *testing.T) {
	tests := []struct {
		name      string
		gateway   string
		gatewayV6 string
		dst       string
		want      string
	}{
		{"IPv4 gateway, IPv4 route", "10.8.0.1", "", "10.20.0.0/16", "10.8.0.1"},
		{"IPv6 gateway, IPv6 route", "", "fd00:8::1", "2001:db8::/32", "fd00:8::1"},
		{"IPv4 gateway only, IPv6 route", "10.8.0.1", "", "::/1", ""},
		{"IPv6 gateway only, IPv4 route", "", "fd00:8::1", "0.0.0.0/1", ""},
		{"both families, IPv4 route", "10.8.0.1", "fd00:8::1", "10.20.0.0/16", "10.8.0.1"},
		{"both families, IPv6 route", "10.8.0.1", "fd00:8::1", "::/1", "fd00:8::1"},
		{"no gateway", "", "", "10.20.0.0/16", ""},
		{"unspecified", "0.0.0.0", "", "10.20.0.0/16", ""},
		{"unspecified IPv6", "", "::", "::/1", ""},
		{"loopback", "127.0.0.1", "", "10.20.0.0/16", ""},
		{"multicast", "", "ff02::1", "::/1", ""},
		{"an IPv6 address in the IPv4 field is no IPv4 next hop", "fd00:8::1", "", "10.20.0.0/16", ""},
		{"IPv4 written as IPv6", "::ffff:10.8.0.1", "", "10.20.0.0/16", "10.8.0.1"},
		{"a zone is no part of the next hop", "", "fe80::1%tap0", "::/1", "fe80::1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var intent tunnel.Intent
			if tt.gateway != "" {
				intent.Gateway = ip(tt.gateway)
			}
			if tt.gatewayV6 != "" {
				intent.GatewayV6 = ip(tt.gatewayV6)
			}
			var want netip.Addr
			if tt.want != "" {
				want = ip(tt.want)
			}

			if got := tunnelGateway(intent, pfx(tt.dst)); got != want {
				t.Errorf("tunnelGateway = %v, want %v", got, want)
			}
		})
	}
}

// openvpn reconnects and the server pushes another gateway, or none: the routes
// follow, the new ones before the old ones are removed, so that a full tunnel
// never lets the traffic out between the two. macOS routes have no gateway and
// nothing happens there.
func TestGatewayChangeWhileUp(t *testing.T) { eachKeying(t, testGatewayChangeWhileUp) }

func testGatewayChangeWhileUp(t *testing.T, k keyingCase) {
	tunnelRoutes := []netip.Prefix{pfx("0.0.0.0/1"), pfx("128.0.0.0/1"), pfx("10.20.0.0/16")}
	e := newEnv(t, k)
	e.addTap()
	e.announce(tapIntent("10.8.0.1"))

	for _, gateway := range []string{"10.8.0.2", "", "10.8.0.1"} {
		before := e.host.Routes.Ops()
		inUse := make(map[netip.Prefix]int, len(tunnelRoutes))
		for _, dst := range tunnelRoutes {
			inUse[dst] = routesTo(e, dst)
		}

		e.announce(tapIntent(gateway))

		via := ""
		if k.windows() && gateway != "" {
			via = " via " + gateway
		}
		e.checkTable(
			"0.0.0.0/1"+via+" dev utun11",
			"10.20.0.0/16"+via+" dev utun11",
			"128.0.0.0/1"+via+" dev utun11",
			"198.51.100.7/32 via 192.168.51.1 dev en0",
			"::/1 dev utun11",
			"8000::/1 dev utun11",
		)
		if !k.windows() {
			if after := e.host.Routes.Ops(); len(after) != len(before) {
				t.Errorf("a gateway means nothing on macOS, yet the table was written: %v", e.opLines()[len(before):])
			}
			continue
		}
		for _, op := range e.host.Routes.Ops()[len(before):] {
			if op.Err != nil || !slices.Contains(tunnelRoutes, op.Route.Dst) {
				continue
			}
			switch op.Kind {
			case fake.OpAdd:
				inUse[op.Route.Dst]++
			case fake.OpDelete:
				if inUse[op.Removed.Dst]--; inUse[op.Removed.Dst] == 0 {
					t.Errorf("gateway %q: no route to %s between deleting the old one and adding the new one", gateway, op.Removed.Dst)
				}
			}
		}
		for _, rec := range e.unresolved() {
			if slices.Contains(tunnelRoutes, pfx(rec.Key)) && rec.Gateway != gateway {
				t.Errorf("journal still lists %s via %q after the change to %q", rec.Key, rec.Gateway, gateway)
			}
		}
	}
}

// routesTo counts the routes of the table to dst, ours or not.
func routesTo(e *env, dst netip.Prefix) int {
	routes, err := e.host.Routes.Dump()
	if err != nil {
		e.t.Fatal(err)
	}
	n := 0
	for _, rt := range routes {
		if rt.Dst == dst && rt.Static {
			n++
		}
	}
	return n
}

// A route to a destination that is not wanted any more does not wait for the
// replacement of another one: only a route that is replaced is kept until the
// new one is in.
func TestRoutesThatAreNotReplacedGoFirst(t *testing.T) {
	e := newEnv(t, windowsCase)
	e.addTap()
	e.announce(tapIntent("10.8.0.1"))
	start := len(e.host.Routes.Ops())

	in := tapIntent("10.8.0.2")
	in.Routes = slices.DeleteFunc(slices.Clone(in.Routes), func(p netip.Prefix) bool { return p == pfx("10.20.0.0/16") })
	e.announce(in)

	want := []string{"delete 10.20.0.0/16", "add 0.0.0.0/1 via 10.8.0.2", "add 128.0.0.0/1 via 10.8.0.2", "delete 128.0.0.0/1", "delete 0.0.0.0/1"}
	if ops := e.opLines()[start:]; !slices.Equal(ops, want) {
		t.Errorf("operations %v, want %v", ops, want)
	}
}

// The journal of a crashed run names the next hop of every route, which is what
// tells the route of that run from another program's route to the same prefix
// through the same adapter.
func TestRecoveryRemovesTheRoutesThroughTheGatewayOfTheCrashedRun(t *testing.T) {
	e := newEnv(t, windowsCase)
	e.addTap()
	e.announce(tapIntent("10.8.0.1"))
	neighbour := osnet.Route{Dst: pfx("10.20.0.0/16"), Gateway: ip("10.8.0.2"), Iface: "utun11", Static: true}
	e.host.Inject(neighbour)
	if left := e.unresolved(); !slices.ContainsFunc(left, func(r record) bool { return r.Key == "0.0.0.0/1" && r.Gateway == "10.8.0.1" && r.IfIndex != 0 }) {
		t.Fatalf("setup: the journal does not name the next hop: %+v", left)
	}

	e.crash() // the adapter stays, and its routes with it
	e.newReconciler()

	e.checkTable("10.20.0.0/16 via 10.8.0.2 dev utun11")
	if left := e.unresolved(); len(left) != 0 {
		t.Errorf("the journal still lists %+v", left)
	}
}

// A route of the crashed run that was replaced since, through the same adapter
// and next hop but with other attributes, belongs to somebody else now.
func TestRecoveryLeavesARouteThatWasChangedSince(t *testing.T) {
	e := newEnv(t, windowsCase)
	e.addTap()
	e.announce(tapIntent("10.8.0.1"))
	changed := osnet.Route{Dst: pfx("10.20.0.0/16"), Gateway: ip("10.8.0.1"), Iface: "utun11", Static: true, Metric: 99}
	e.host.Inject(changed)

	e.crash()
	e.newReconciler()

	e.checkTable("10.20.0.0/16 via 10.8.0.1 dev utun11")
	if rt, _ := e.host.Routes.Get(changed.Dst); rt.Metric != 99 {
		t.Errorf("the route was touched: %+v", rt)
	}
	if !strings.Contains(e.logs.String(), "changed by another program") {
		t.Errorf("the change was not noticed:\n%s", e.logs)
	}
}

// The next hop is part of what the journal remembers about a route.
func TestFingerprintAndKeyIncludeTheGateway(t *testing.T) {
	base := osnet.Route{Dst: pfx("0.0.0.0/1"), Gateway: ip("10.8.0.1"), Iface: "utun11", IfIndex: 31, Metric: windowsTunnelMetric, Static: true}
	other := base
	other.Gateway = ip("10.8.0.2")
	onLink := base
	onLink.Gateway = netip.Addr{}

	keying := KeyByPrefixInterfaceNextHop
	for _, rt := range []osnet.Route{other, onLink} {
		if keying.key(base) == keying.key(rt) {
			t.Errorf("a route via %v has the key of the one via %v", rt.Gateway, base.Gateway)
		}
		if keying.fingerprint(base) == keying.fingerprint(rt) {
			t.Errorf("a route via %v has the fingerprint of the one via %v", rt.Gateway, base.Gateway)
		}
		if keying.same(base, rt) {
			t.Errorf("a route via %v is the same as the one via %v", rt.Gateway, base.Gateway)
		}
	}
	var rec record
	keying.stamp(&rec, base)
	rec.Key = base.Dst.String()
	got, err := keying.recordKey(rec)
	if err != nil || got != keying.key(base) {
		t.Errorf("the record of the route reads back as %v (%v), want %v", got, err, keying.key(base))
	}
}

// A host route through the tunnel's gateway is not an endpoint's route and is
// never reported as stale, and it is deleted with the rest.
func TestTunnelHostRouteThroughTheGatewayIsNotStale(t *testing.T) {
	e := newEnv(t, windowsCase)
	e.addTap()
	in := withGateway(up("ovpn", 1, "utun11", tunnel.RoleSplit, "172.16.5.7/32", "10.20.0.0/16"), "10.8.0.1")

	e.announce(in)

	e.checkTable("10.20.0.0/16 via 10.8.0.1 dev utun11", "172.16.5.7/32 via 10.8.0.1 dev utun11")
	if stale := e.r.Report().Stale; len(stale) != 0 {
		t.Errorf("stale: %+v", stale)
	}
	e.r.Withdraw("ovpn")
	e.checkTable()
}
