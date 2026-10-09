package reconciler

import (
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/osnet/fake"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// The scenarios here are the Linux routing table: the kernel tells the routes of a
// prefix apart by their metric, each has an interface and a next hop of its own,
// tunnel devices take on-link routes in both families, and a link-local router
// is named with its interface. The machine is the one of newEnv with its
// interface called en0, index 1.

// ipRoute is a route as ip route shows it, with the metric: the same prefix can
// have several routes in this table.
func ipRoute(rt osnet.Route) string {
	line := rt.Dst.String()
	if rt.Gateway.IsValid() {
		line += " via " + rt.Gateway.WithZone("").String()
	}
	return fmt.Sprintf("%s dev %s metric %d", line, rt.Iface, rt.Metric)
}

// ipRoutes lists the routes of programs, the system's default routes left out.
func (e *env) ipRoutes() []string {
	e.t.Helper()
	routes, err := e.host.Routes.Dump()
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, rt := range routes {
		if rt.Static && rt.Dst.Bits() != 0 {
			out = append(out, ipRoute(rt))
		}
	}
	return out
}

func (e *env) checkIPRoutes(want ...string) {
	e.t.Helper()
	checkLines(e.t, "routing table", e.ipRoutes(), want)
}

// ipOps is the operation log of the route table with the metric, the
// operations that failed marked as such.
func (e *env) ipOps() []string {
	var out []string
	for _, op := range e.host.Routes.Ops() {
		if op.Kind == fake.OpMark {
			continue
		}
		line := string(op.Kind) + " " + ipRoute(op.Route)
		if op.Err != nil {
			line += " failed"
		}
		out = append(out, line)
	}
	return out
}

func (e *env) noFailedOps() {
	e.t.Helper()
	for _, line := range e.ipOps() {
		if strings.HasSuffix(line, " failed") {
			e.t.Errorf("the kernel refused: %s", line)
		}
	}
}

func (e *env) expectConflict(dst string, owner tunnel.OwnerID, detail string) {
	e.t.Helper()
	if rr := e.routeReport(dst, owner); rr.State != tunnel.RouteFailed || rr.Overridden || rr.Detail != detail {
		e.t.Errorf("%s of %s: %+v\nwant a conflict: failed, not overridden, %q", dst, owner, rr, detail)
	}
}

// expectLinuxOverridden is expectOverridden that also demands the flag of the
// report.
func (e *env) expectLinuxOverridden(dst string, owner tunnel.OwnerID, detail string) {
	e.t.Helper()
	e.expectOverridden(dst, owner, detail)
	if !e.routeReport(dst, owner).Overridden {
		e.t.Errorf("%s of %s is not marked as overridden", dst, owner)
	}
}

func withGateways(in tunnel.Intent, v4, v6 string) tunnel.Intent {
	if v4 != "" {
		in.Gateway = ip(v4)
	}
	if v6 != "" {
		in.GatewayV6 = ip(v6)
	}
	return in
}

// A tunnel device is a point-to-point layer 3 device. Whatever a tunnel
// announced, its routes are bound to the device in both families: a route via
// the gateway needs the gateway to be on the device's subnet and of the
// destination's family, an on-link route needs nothing. The rule that holds an
// IPv6 route back because only an IPv4 gateway was announced is for Ethernet-like
// adapters.
func TestLinuxTunnelRoutesAreOnLinkWhateverTheTunnelAnnounced(t *testing.T) {
	for _, tt := range []struct{ name, v4, v6 string }{
		{"no gateway, as WireGuard", "", ""},
		{"an IPv4 gateway, as OpenVPN announces", "10.8.0.1", ""},
		{"both gateways", "10.8.0.1", "fd08::1"},
		{"an IPv6 gateway only", "", "fd08::1"},
		{"a gateway that is not on the subnet of the device", "172.31.255.1", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, linuxCase)
			e.addTunnel("tun0", "10.8.0.6/24")

			e.announce(withGateways(up("vpn", 1, "tun0", tunnel.RoleSplit, "10.50.0.0/16", "192.168.77.0/24", "2001:db8:50::/48"), tt.v4, tt.v6))

			e.checkIPRoutes("10.50.0.0/16 dev tun0 metric 5", "192.168.77.0/24 dev tun0 metric 5", "2001:db8:50::/48 dev tun0 metric 5")
			for _, rr := range e.r.Report().Routes {
				if rr.State != tunnel.RouteInstalled || rr.Via != "tun0" {
					t.Errorf("%+v", rr)
				}
			}
			routes, _ := e.host.Routes.Dump()
			for _, rt := range routes {
				if rt.Static && rt.Dst.Bits() != 0 && (rt.Gateway.IsValid() || rt.IfIndex != e.host.ifIndex("tun0")) {
					t.Errorf("a tunnel route must be on-link and name the device: %+v", rt)
				}
			}
			e.r.Withdraw("vpn")
			e.checkIPRoutes()
		})
	}
}

// The rule is a property of the keying, not of the intent: the same tunnel keeps
// its IPv6 routes on Linux and has them held back on the Windows adapter that
// needs a next hop.
func TestLinuxDoesNotHoldBackTheRoutesOfAFamilyThatHasNoGateway(t *testing.T) {
	in := withGateways(up("vpn", 1, "tun0", tunnel.RoleSplit, "10.50.0.0/16", "2001:db8:50::/48"), "10.8.0.1", "")
	ns := osnet.NetState{Interfaces: []osnet.Interface{{Name: "tun0", Index: 7, Up: true, Tunnel: true}}}

	for keying, want := range map[RouteKeying]tunnel.RouteState{
		KeyLinux:                    tunnel.RoutePending,
		KeyByPrefixInterfaceNextHop: tunnel.RouteBlocked,
	} {
		for _, p := range computeFor(keying, []tunnel.Intent{in}, ns).Routes {
			if p.Route.Dst.Addr().Is6() && p.State != want {
				t.Errorf("keying %d: %s is %v, want %v", keying, p.Route.Dst, p.State, want)
			}
		}
	}
	for _, p := range computeFor(KeyLinux, []tunnel.Intent{in}, ns).Routes {
		if p.Route.IfIndex != 7 || p.Route.Metric != tunnelMetric || p.Route.Gateway.IsValid() {
			t.Errorf("the route of a tunnel is on-link, names the device and has the metric of tunnels: %+v", p.Route)
		}
	}
}

// The product's goal on Linux: the WireGuard full tunnel and the OpenVPN split
// tunnel up together, each reachable, with the resolvers of their devices.
func TestLinuxFullAndSplitTunnelsTogether(t *testing.T) {
	e := newEnv(t, linuxCase)
	e.addTunnel("wg0", "10.6.0.2/24")
	e.addTunnel("tun0", "10.8.0.6/24")
	wg := nameserver(endpoints(up("wg", 1, "wg0", tunnel.RoleFull, "0.0.0.0/0", "::/0"), "203.0.113.10"), "192.168.50.1", ".")
	ovpn := nameserver(endpoints(withGateways(up("ovpn", 2, "tun0", tunnel.RoleSplit, "192.168.1.0/24"), "10.8.0.1", ""), "198.51.100.7"), "192.168.1.1", "asus.lan")
	e.announce(state(wg, tunnel.StateConnecting))
	e.announce(state(ovpn, tunnel.StateConnecting))
	e.checkIPRoutes() // nothing captures an endpoint yet

	e.announce(wg)
	e.announce(ovpn)

	e.checkIPRoutes(
		"0.0.0.0/1 dev wg0 metric 5",
		"128.0.0.0/1 dev wg0 metric 5",
		"192.168.1.0/24 dev tun0 metric 5",
		"198.51.100.7/32 via 192.168.51.1 dev en0 metric 1",
		"203.0.113.10/32 via 192.168.51.1 dev en0 metric 1",
		"::/1 dev wg0 metric 5",
		"8000::/1 dev wg0 metric 5",
	)
	for addr, want := range map[string]string{
		"8.8.8.8":         "wg0",
		"192.168.1.5":     "tun0",
		"192.168.51.77":   "en0",
		"203.0.113.10":    "en0",
		"198.51.100.7":    "en0",
		"2001:4860::8888": "wg0",
	} {
		if got := e.lookup(addr).Iface; got != want {
			t.Errorf("%s leaves through %s, want %s", addr, got, want)
		}
	}
	e.noFailedOps()
	for _, rr := range e.r.Report().Routes {
		if rr.State != tunnel.RouteInstalled {
			t.Errorf("not installed: %+v", rr)
		}
	}
	// Each resolver entry is written for the device that reaches its servers.
	want := map[string][]osnet.DNSEntry{
		"wg":   {{Servers: ips("192.168.50.1"), MatchDomains: []string{"."}, Order: 1, Iface: "wg0"}},
		"ovpn": {{Servers: ips("192.168.1.1"), MatchDomains: []string{"asus.lan"}, Order: 2, Iface: "tun0"}},
	}
	got := e.host.DNS.All()
	for owner, entries := range want {
		if !slices.EqualFunc(got[owner], entries, entryEqual) {
			t.Errorf("DNS of %s: got %+v, want %+v", owner, got[owner], entries)
		}
	}

	e.r.Withdraw("wg")
	e.r.Withdraw("ovpn")
	e.checkIPRoutes()
	if owned, _ := e.host.DNS.Owned(); len(owned) != 0 {
		t.Errorf("resolver entries left: %v", owned)
	}
}

// Two tunnels that want one prefix: the better one gets it, the other waits and
// is promoted when the first goes. The kernel takes one route per destination and
// metric, so the loser's route goes before the winner's comes.
func TestLinuxShadowedRoutesAreHandedOverWithoutARefusal(t *testing.T) {
	e := newEnv(t, linuxCase)
	e.addTunnel("tun0", "10.1.0.2/24")
	e.addTunnel("wg0", "10.2.0.2/24")
	low := up("low", 2, "tun0", tunnel.RoleSplit, "10.0.0.0/8", "172.16.0.0/12")
	high := up("high", 1, "wg0", tunnel.RoleSplit, "10.0.0.0/8")

	e.announce(low)
	e.checkIPRoutes("10.0.0.0/8 dev tun0 metric 5", "172.16.0.0/12 dev tun0 metric 5")
	e.announce(high)

	e.checkIPRoutes("10.0.0.0/8 dev wg0 metric 5", "172.16.0.0/12 dev tun0 metric 5")
	if rr := e.routeReport("10.0.0.0/8", "low"); rr.State != tunnel.RouteShadowed || rr.ShadowedBy != "high" {
		t.Errorf("report: %+v", rr)
	}
	e.noFailedOps()

	e.r.Withdraw("high")

	e.checkIPRoutes("10.0.0.0/8 dev tun0 metric 5", "172.16.0.0/12 dev tun0 metric 5")
	e.noFailedOps()
}

// A tunnel that moves to another device while it keeps its routes (the engine
// made a new one before the old one was gone): the old route is deleted first,
// the kernel would refuse the new one beside it.
func TestLinuxRouteMovesToAnotherDeviceOfTheSameOwner(t *testing.T) {
	e := newEnv(t, linuxCase)
	e.addTunnel("tun0", "10.8.0.6/24")
	e.addTunnel("tun1", "10.8.1.6/24")
	e.announce(up("ovpn", 1, "tun0", tunnel.RoleSplit, "10.50.0.0/16"))
	e.checkIPRoutes("10.50.0.0/16 dev tun0 metric 5")

	e.announce(up("ovpn", 1, "tun1", tunnel.RoleSplit, "10.50.0.0/16"))

	e.checkIPRoutes("10.50.0.0/16 dev tun1 metric 5")
	e.expectInstalled("10.50.0.0/16", "ovpn")
	e.noFailedOps()
	if e.r.dirty || e.r.failedPasses != 0 {
		t.Errorf("nothing is left to do: dirty %v, failed passes %d", e.r.dirty, e.r.failedPasses)
	}
	ops := e.ipOps()
	del := slices.Index(ops, "delete 10.50.0.0/16 dev tun0 metric 5")
	add := slices.Index(ops, "add 10.50.0.0/16 dev tun1 metric 5")
	if del < 0 || add < 0 || del > add {
		t.Errorf("the old route goes before the new one comes: %v", ops)
	}
	got := e.journalFor(kindRoute, "10.50.0.0/16")
	if want := []string{"pending", "applied", "removed", "pending", "applied"}; !slices.Equal(got, want) {
		t.Errorf("journal: %v, want %v", got, want)
	}
}

// A route of another program with a lower metric carries the traffic instead of
// ours; one with a higher metric does not. Neither is touched, and the report
// tells the first case from a route that failed.
func TestLinuxForeignRouteOfALowerMetricOverridesOurs(t *testing.T) {
	e := newEnv(t, linuxCase)
	e.addTunnel("wg0", "10.6.0.2/24")
	e.addForeignVPN(0)
	e.foreignDefault(1)

	e.announce(wgIntent2())

	for _, dst := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		e.expectLinuxOverridden(dst, "wg", e.overriddenBy(dst, "10.9.0.1", 1, tunnelMetric))
	}
	for _, dst := range []string{"::/1", "8000::/1"} {
		e.expectLinuxOverridden(dst, "wg", e.overriddenBy(dst, "fd09::1", 1, tunnelMetric))
	}
	if rr := e.routeReport("203.0.113.10/32", "wg"); rr.State != tunnel.RouteInstalled || rr.Overridden {
		t.Errorf("the endpoint's route has no rival: %+v", rr)
	}
	if rt := e.lookup("8.8.8.8"); rt.Iface != foreignAdapter {
		t.Fatalf("setup: the traffic should leave through the other VPN, it leaves through %s", rt.Iface)
	}
	// Both are in the table, ours with the metric it was given.
	e.checkIPRoutes(
		"0.0.0.0/1 via 10.9.0.1 dev gui-tap metric 1",
		"0.0.0.0/1 dev wg0 metric 5",
		"128.0.0.0/1 via 10.9.0.1 dev gui-tap metric 1",
		"128.0.0.0/1 dev wg0 metric 5",
		"203.0.113.10/32 via 192.168.51.1 dev en0 metric 1",
		"::/1 via fd09::1 dev gui-tap metric 1",
		"::/1 dev wg0 metric 5",
		"8000::/1 via fd09::1 dev gui-tap metric 1",
		"8000::/1 dev wg0 metric 5",
	)
	for _, line := range e.ipOps() {
		if strings.Contains(line, "gui-tap") || strings.HasPrefix(line, "delete") {
			t.Errorf("the table was written for the foreign route or deleted from: %s", line)
		}
	}
	e.expectSettled()
	if n := strings.Count(e.logs.String(), "used instead of ours"); n != 4 {
		t.Errorf("the log says it %d times, want once per route:\n%s", n, e.logs)
	}

	// The other VPN goes: ours is in use again, and nothing had to be done.
	for _, dst := range []string{"0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1"} {
		e.host.Routes.Remove(pfx(dst)) // the lowest metric: theirs
	}
	e.change(osnet.ChangeRoute)
	for _, dst := range []string{"0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1"} {
		e.expectInstalled(dst, "wg")
		if e.routeReport(dst, "wg").Overridden {
			t.Errorf("%s is still marked as overridden", dst)
		}
	}
	if n := strings.Count(e.logs.String(), "our route is in use again"); n != 4 {
		t.Errorf("logged %d times that the routes are back:\n%s", n, e.logs)
	}
	if rt := e.lookup("8.8.8.8"); rt.Iface != "wg0" {
		t.Errorf("traffic leaves through %s", rt.Iface)
	}
}

// The other VPN connects after ours: nothing about the interfaces changes, only
// the table, and a route event is enough to change the verdict.
func TestLinuxForeignVPNConnectingLaterOverridesAndDisconnectingRestores(t *testing.T) {
	e := newEnv(t, linuxCase)
	e.addTunnel("wg0", "10.6.0.2/24")
	e.addForeignVPN(0)
	e.announce(wgIntent2())
	e.expectInstalled("0.0.0.0/1", "wg")
	before := e.r.netState

	e.foreignDefault(1)
	e.change(osnet.ChangeRoute)

	if after, _ := e.host.Net.Snapshot(); netChanged(before, after) {
		t.Fatal("setup: the network state changed, the route event alone is the point of this test")
	}
	e.expectLinuxOverridden("0.0.0.0/1", "wg", e.overriddenBy("0.0.0.0/1", "10.9.0.1", 1, tunnelMetric))
	e.expectLinuxOverridden("8000::/1", "wg", e.overriddenBy("8000::/1", "fd09::1", 1, tunnelMetric))

	e.host.DestroyInterface(foreignAdapter) // its routes go with it
	e.change(osnet.ChangeRoute)

	e.expectInstalled("0.0.0.0/1", "wg")
	e.expectInstalled("8000::/1", "wg")
	if rt := e.lookup("8.8.8.8"); rt.Iface != "wg0" {
		t.Errorf("traffic leaves through %s", rt.Iface)
	}
}

// wgIntent2 is the WireGuard full tunnel of the owner on a Linux device.
func wgIntent2() tunnel.Intent {
	return nameserver(endpoints(up("wg", 1, "wg0", tunnel.RoleFull, "0.0.0.0/0", "::/0"), "203.0.113.10"), "192.168.50.1", ".")
}

func TestLinuxForeignRouteOfAHigherMetricLosesToOurs(t *testing.T) {
	e := newEnv(t, linuxCase)
	e.addTunnel("wg0", "10.6.0.2/24")
	e.addForeignVPN(0)
	e.foreignDefault(100)

	e.announce(wgIntent2())

	for _, dst := range []string{"0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1"} {
		e.expectInstalled(dst, "wg")
		if e.routeReport(dst, "wg").Overridden {
			t.Errorf("%s is marked as overridden", dst)
		}
	}
	if rt := e.lookup("8.8.8.8"); rt.Iface != "wg0" {
		t.Errorf("traffic leaves through %s", rt.Iface)
	}
	e.expectSettled()
	if strings.Contains(e.logs.String(), "used instead of ours") {
		t.Errorf("nothing was overridden:\n%s", e.logs)
	}
	e.r.Withdraw("wg")
	e.checkIPRoutes(
		"0.0.0.0/1 via 10.9.0.1 dev gui-tap metric 100",
		"128.0.0.0/1 via 10.9.0.1 dev gui-tap metric 100",
		"::/1 via fd09::1 dev gui-tap metric 100",
		"8000::/1 via fd09::1 dev gui-tap metric 100",
	)
}

// A foreign route with the destination and the metric of ours cannot lie beside
// it, whatever interface it leaves through: that is a conflict, found before the
// kernel is asked and not retried, and not a route that is in use instead of ours.
func TestLinuxForeignRouteWithTheMetricOfOursIsAConflict(t *testing.T) {
	e := newEnv(t, linuxCase)
	e.addTunnel("tun0", "10.8.0.6/24")
	e.addForeignVPN(0)
	e.foreignRoute("10.50.0.0/16", tunnelMetric)

	e.announce(up("ovpn", 1, "tun0", tunnel.RoleSplit, "10.50.0.0/16", "10.51.0.0/16"))

	e.expectConflict("10.50.0.0/16", "ovpn", "held by another program via 10.9.0.1")
	e.expectInstalled("10.51.0.0/16", "ovpn")
	e.noFailedOps()
	if ops := e.ipOps(); slices.ContainsFunc(ops, func(op string) bool { return strings.HasPrefix(op, "add 10.50.0.0/16") }) {
		t.Errorf("the kernel was asked to add a route it must refuse: %v", ops)
	}
	e.expectSettled()
	if e.owns("10.50.0.0/16") {
		t.Error("the foreign route is not ours")
	}
	e.r.Withdraw("ovpn")
	e.checkIPRoutes("10.50.0.0/16 via 10.9.0.1 dev gui-tap metric 5")
}

// A route of another program that is the route we would add is as good as ours.
// It is left alone when we go.
func TestLinuxIdenticalForeignRouteIsAcceptedAndKept(t *testing.T) {
	e := newEnv(t, linuxCase)
	e.addTunnel("tun0", "10.8.0.6/24")
	e.host.Inject(osnet.Route{Dst: pfx("10.50.0.0/16"), Iface: "tun0", Metric: tunnelMetric, Static: true})

	e.announce(up("ovpn", 1, "tun0", tunnel.RoleSplit, "10.50.0.0/16"))

	e.expectInstalled("10.50.0.0/16", "ovpn")
	if slices.Contains(e.ipOps(), "add 10.50.0.0/16 dev tun0 metric 5") {
		t.Error("the route was added although it existed")
	}
	if e.owns("10.50.0.0/16") {
		t.Error("the route is not ours")
	}
	e.r.Withdraw("ovpn")
	e.checkIPRoutes("10.50.0.0/16 dev tun0 metric 5")
}

// A profile that routes the subnet of the tunnel itself: the kernel's own route
// for that subnet lies beside ours with the metric 0, through the same device. It
// leads where ours does and is no rival.
func TestLinuxRouteToTheSubnetOfTheTunnelItselfIsNotOverriddenByTheKernelsOwn(t *testing.T) {
	e := newEnv(t, linuxCase)
	e.addTunnel("wg0", "10.6.0.2/24")

	e.announce(up("wg", 1, "wg0", tunnel.RoleSplit, "10.6.0.0/24", "10.7.0.0/24"))

	e.expectInstalled("10.6.0.0/24", "wg")
	e.expectInstalled("10.7.0.0/24", "wg")
	if e.routeReport("10.6.0.0/24", "wg").Overridden {
		t.Error("marked as overridden by the route of the kernel")
	}
	e.checkIPRoutes("10.6.0.0/24 dev wg0 metric 5", "10.7.0.0/24 dev wg0 metric 5")
	e.expectSettled()
	e.r.Withdraw("wg")
	e.checkIPRoutes()
	if routes, _ := e.host.Routes.Dump(); !slices.ContainsFunc(routes, func(rt osnet.Route) bool { return rt.Dst == pfx("10.6.0.0/24") && rt.Metric == 0 }) {
		t.Error("the route of the kernel is gone")
	}
}

// The same route through another router of the same device does lead elsewhere.
func TestLinuxRivalOnTheSameInterfaceWithAnotherNextHopStillCounts(t *testing.T) {
	e := newEnv(t, linuxCase)
	e.addTunnel("wg0", "10.6.0.2/24")
	e.host.Inject(osnet.Route{Dst: pfx("203.0.113.10/32"), Gateway: ip("192.168.51.254"), Iface: "en0", Metric: 0, Static: true})

	e.announce(endpoints(up("wg", 1, "wg0", tunnel.RoleFull, "0.0.0.0/0"), "203.0.113.10"))

	want := "overridden by 203.0.113.10/32 via 192.168.51.254 (interface 1): effective metric 0, ours 1"
	e.expectLinuxOverridden("203.0.113.10/32", "wg", want)
}

// The bypass route of an endpoint beside the route someone else added for the
// same host through the same router: the metrics differ, so these are two
// routes, and ours is not taken for theirs.
func TestLinuxRoutesToOneHostWithAnotherMetricAreTwoRoutes(t *testing.T) {
	e := newEnv(t, linuxCase)
	e.addTunnel("wg0", "10.6.0.2/24")
	theirs := osnet.Route{Dst: pfx("203.0.113.10/32"), Gateway: ip("192.168.51.1"), Iface: "en0", Static: true} // metric 0
	e.host.Inject(theirs)

	e.announce(endpoints(up("wg", 1, "wg0", tunnel.RoleFull, "0.0.0.0/0"), "203.0.113.10"))

	e.checkIPRoutes(
		"0.0.0.0/1 dev wg0 metric 5",
		"128.0.0.0/1 dev wg0 metric 5",
		"203.0.113.10/32 via 192.168.51.1 dev en0 metric 0",
		"203.0.113.10/32 via 192.168.51.1 dev en0 metric 1",
	)
	if !e.owns("203.0.113.10/32") {
		t.Fatal("the bypass route is ours")
	}
	// It leads where theirs does: no rival, nothing to report.
	e.expectInstalled("203.0.113.10/32", "wg")
	before := len(e.ipOps())
	e.change(osnet.ChangeHeartbeat)
	if after := e.ipOps(); len(after) != before {
		t.Errorf("a pass on an unchanged table did something: %v", after[before:])
	}

	e.r.Withdraw("wg")

	// Only ours goes; the delete names the metric.
	e.checkIPRoutes("203.0.113.10/32 via 192.168.51.1 dev en0 metric 0")
	var removed []osnet.Route
	for _, op := range e.host.Routes.Ops() {
		if op.Kind == fake.OpDelete && op.Err == nil && op.Removed.Dst == pfx("203.0.113.10/32") {
			removed = append(removed, op.Removed)
		}
	}
	if len(removed) != 1 || removed[0].Metric != bypassMetric {
		t.Errorf("deleted %+v", removed)
	}
}

// Two routes with one destination and metric: a program can append one to ours
// (ip route append). The table cannot show both; ours is the one that stays, the
// one we have to find again to see that it is there and to delete it. The
// foreign ones are on interfaces that sort before and after ours.
func TestLinuxAnAppendedRouteDoesNotHideOurs(t *testing.T) {
	for _, iface := range []string{"dummy0", "wlan0"} {
		t.Run(iface, func(t *testing.T) {
			e := newEnv(t, linuxCase)
			e.addTunnel("wg0", "10.6.0.2/24")
			e.host.Routes.AddInterface(osnet.Interface{Name: iface, Up: true, Addrs: pfxs("192.168.77.4/24")})
			e.announce(endpoints(up("wg", 1, "wg0", tunnel.RoleFull, "0.0.0.0/0"), "203.0.113.10"))
			theirs := osnet.Route{Dst: pfx("203.0.113.10/32"), Gateway: ip("192.168.77.1"), Iface: iface, Metric: bypassMetric, Static: true}

			e.host.Routes.Append(theirs)

			if got := e.ipRoutes(); !slices.Contains(got, "203.0.113.10/32 via 192.168.77.1 dev "+iface+" metric 1") || len(got) != 4 {
				t.Fatalf("setup: both routes should be in the table: %v", got)
			}
			e.change(osnet.ChangeHeartbeat)
			e.change(osnet.ChangeRoute)
			if strings.Contains(e.logs.String(), "changed by another program") {
				t.Errorf("our route was taken for a changed one:\n%s", e.logs)
			}
			if !e.owns("203.0.113.10/32") {
				t.Fatal("the route is not ours any more")
			}
			e.expectInstalled("203.0.113.10/32", "wg")

			e.r.Withdraw("wg")

			e.checkIPRoutes("203.0.113.10/32 via 192.168.77.1 dev " + iface + " metric 1")
		})
	}
}

// The default route moves to another router, as when the machine joins another
// network. The bypass route of the endpoint has the same destination and metric
// as before, so the old one has to go before the new one can come; the tunnel's
// own routes are not touched.
func TestLinuxEndpointBypassFollowsAGatewayChange(t *testing.T) {
	e := newEnv(t, linuxCase)
	e.addTunnel("wg0", "10.6.0.2/24")
	e.announce(endpoints(up("wg", 1, "wg0", tunnel.RoleFull, "0.0.0.0/0"), "203.0.113.10"))
	e.checkIPRoutes("0.0.0.0/1 dev wg0 metric 5", "128.0.0.0/1 dev wg0 metric 5", "203.0.113.10/32 via 192.168.51.1 dev en0 metric 1")
	var rebinds atomic.Int32
	e.r.SetRebind(func() { rebinds.Add(1) })

	e.host.MoveNetwork("en0", pfx("10.20.30.7/24"), ip("10.20.30.1"))
	start := len(e.ipOps()) // after the system's own default route
	e.change(osnet.ChangeRoute)

	e.checkIPRoutes("0.0.0.0/1 dev wg0 metric 5", "128.0.0.0/1 dev wg0 metric 5", "203.0.113.10/32 via 10.20.30.1 dev en0 metric 1")
	e.noFailedOps()
	want := []string{
		"delete 203.0.113.10/32 via 192.168.51.1 dev en0 metric 1",
		"add 203.0.113.10/32 via 10.20.30.1 dev en0 metric 1",
	}
	if got := e.ipOps()[start:]; !slices.Equal(got, want) {
		t.Errorf("operations: %v\nwant: %v", got, want)
	}
	if rebinds.Load() != 1 {
		t.Errorf("rebinds: %d", rebinds.Load())
	}
	if e.r.dirty || len(e.r.Report().Stale) != 0 {
		t.Errorf("nothing is left to do: dirty %v, stale %+v", e.r.dirty, e.r.Report().Stale)
	}
}

// A cable is plugged in while Wi-Fi stays up, to the same router: the default
// route of the lower metric moves to the other device. The bypass route names its
// device, so it moves with it.
func TestLinuxEndpointBypassFollowsTheDefaultRouteToAnotherInterface(t *testing.T) {
	e := newEnv(t, linuxCase)
	e.addTunnel("wg0", "10.6.0.2/24")
	e.announce(endpoints(up("wg", 1, "wg0", tunnel.RoleFull, "0.0.0.0/0"), "203.0.113.10"))
	start := len(e.ipOps())
	e.host.SetDefaultMetric("eth1", 50)

	e.host.AddPhysical("eth1", pfx("192.168.51.136/24"), ip("192.168.51.1"))
	e.change(osnet.ChangeRoute)

	e.checkIPRoutes("0.0.0.0/1 dev wg0 metric 5", "128.0.0.0/1 dev wg0 metric 5", "203.0.113.10/32 via 192.168.51.1 dev eth1 metric 1")
	e.noFailedOps()
	var removed, added uint32
	for _, op := range e.host.Routes.Ops()[start:] {
		switch {
		case op.Kind == fake.OpDelete && op.Err == nil && op.Removed.Dst == pfx("203.0.113.10/32"):
			removed = op.Removed.IfIndex
		case op.Kind == fake.OpAdd && op.Err == nil && op.Route.Dst == pfx("203.0.113.10/32"):
			added = op.Route.IfIndex
		}
	}
	if removed != e.host.ifIndex("en0") || added != e.host.ifIndex("eth1") {
		t.Errorf("the route through interface %d should go and one through %d come, got %d and %d", e.host.ifIndex("en0"), e.host.ifIndex("eth1"), removed, added)
	}
	if n := count(e.opLines(), "add 0.0.0.0/1"); n != 1 {
		t.Errorf("the tunnel's own routes must not be touched, 0.0.0.0/1 was added %d times", n)
	}
}

// Every router announces itself as fe80::1, so the address is told apart by its
// interface. The route of the endpoint is journaled without the zone, with the
// index of the interface and the metric, and the Reconciler does not take the
// router of another interface for it.
func TestLinuxBypassRouteThroughALinkLocalRouter(t *testing.T) {
	e := newEnv(t, linuxCase)
	e.host.Inject(osnet.Route{Dst: defaultV6Route, Gateway: ip("fe80::1"), Iface: "en0", Metric: 100, Static: true})
	e.host.Sync()
	e.addTunnel("wg0", "10.6.0.2/24")
	if ns, _ := e.host.Net.Snapshot(); ns.DefaultV6 == nil || ns.DefaultV6.Gateway.Zone() != "en0" {
		t.Fatalf("setup: the network state should name the router with its interface: %+v", ns.DefaultV6)
	}

	e.announce(endpoints(up("wg", 1, "wg0", tunnel.RoleFull, "0.0.0.0/0", "::/0"), "2001:db8::10", "203.0.113.10"))

	e.checkIPRoutes(
		"0.0.0.0/1 dev wg0 metric 5",
		"128.0.0.0/1 dev wg0 metric 5",
		"203.0.113.10/32 via 192.168.51.1 dev en0 metric 1",
		"::/1 dev wg0 metric 5",
		"2001:db8::10/128 via fe80::1 dev en0 metric 1",
		"8000::/1 dev wg0 metric 5",
	)
	rt, ok := e.host.Routes.Get(pfx("2001:db8::10/128"))
	if !ok || rt.IfIndex != e.host.ifIndex("en0") || rt.Metric != bypassMetric {
		t.Errorf("bypass route: %+v", rt)
	}
	rec := e.unresolvedRecord("2001:db8::10/128")
	if rec.Gateway != "fe80::1" || rec.IfIndex != e.host.ifIndex("en0") || rec.Metric != bypassMetric || rec.Iface != "en0" {
		t.Errorf("journal record: %+v", rec)
	}
	if rr := e.routeReport("2001:db8::10/128", "wg"); rr.State != tunnel.RouteInstalled {
		t.Errorf("report: %+v", rr)
	}
	before := len(e.ipOps())
	e.change(osnet.ChangeHeartbeat)
	if after := e.ipOps(); len(after) != before {
		t.Errorf("a pass on an unchanged table did something: %v", after[before:])
	}
	if stale := e.r.Report().Stale; len(stale) != 0 {
		t.Errorf("stale: %+v", stale)
	}

	// The daemon dies: the tunnel goes with it, the route through the router stays,
	// and the next start deletes it by what the journal says.
	e.crash("wg0")
	e.checkIPRoutes("203.0.113.10/32 via 192.168.51.1 dev en0 metric 1", "2001:db8::10/128 via fe80::1 dev en0 metric 1")
	e.newReconciler()
	e.checkIPRoutes()
	if left := e.unresolved(); len(left) != 0 {
		t.Errorf("the journal still lists %+v", left)
	}
}

func (e *env) unresolvedRecord(key string) record {
	e.t.Helper()
	for _, rec := range e.unresolved() {
		if rec.Key == key {
			return rec
		}
	}
	e.t.Fatalf("no unresolved record for %s in %+v", key, e.unresolved())
	return record{}
}

// pendingLinuxRoute is what the journal holds when the daemon dies between the
// record and the add: the route as it was asked for.
func (e *env) pendingLinuxRoute(dst string, rt osnet.Route) record {
	rec := record{Owner: "wg", Kind: kindRoute, Key: dst, State: statePending}
	rt.Dst = pfx(dst)
	e.k.keying.stamp(&rec, e.host.index(rt))
	return rec
}

// Everything the last run journaled is removed at start-up, the routes of the
// tunnels went with their devices, resolver entries are swept by their marker,
// and routes that are not in the journal are left alone and reported.
func TestLinuxCrashRecoveryRemovesWhatTheLastRunLeft(t *testing.T) {
	e := newEnv(t, linuxCase)
	e.addTunnel("wg0", "10.6.0.2/24")
	e.addTunnel("tun0", "10.8.0.6/24")
	e.announce(wgIntent2())
	e.announce(nameserver(endpoints(up("asus", 2, "tun0", tunnel.RoleSplit, "192.168.1.0/24"), "198.51.100.7"), "192.168.1.1", "asus.lan"))
	healthy := osnet.Route{Dst: pfx("8.8.4.4/32"), Gateway: ip("192.168.51.1"), Iface: "en0", Static: true}
	broken := osnet.Route{Dst: pfx("9.9.9.9/32"), Gateway: ip("10.99.0.1"), Iface: "en0", Static: true}
	e.host.Inject(healthy)
	e.host.Inject(broken)
	journaled := len(e.unresolved())

	e.crash("wg0", "tun0")

	e.checkIPRoutes(
		"8.8.4.4/32 via 192.168.51.1 dev en0 metric 0",
		"9.9.9.9/32 via 10.99.0.1 dev en0 metric 0",
		"198.51.100.7/32 via 192.168.51.1 dev en0 metric 1",
		"203.0.113.10/32 via 192.168.51.1 dev en0 metric 1",
	)
	r2 := e.newReconciler()

	e.checkIPRoutes("8.8.4.4/32 via 192.168.51.1 dev en0 metric 0", "9.9.9.9/32 via 10.99.0.1 dev en0 metric 0")
	if owned, _ := e.host.DNS.Owned(); len(owned) != 0 {
		t.Errorf("resolver entries left: %v", owned)
	}
	if left := e.unresolved(); len(left) != 0 {
		t.Errorf("the journal still lists %+v", left)
	}
	rep := r2.Report()
	if len(rep.Stale) != 1 || dstOf(rep.Stale[0].Key) != "9.9.9.9/32" || rep.Stale[0].Owned {
		t.Errorf("only the broken foreign route is reported, as not ours: %+v", rep.Stale)
	}
	// Two bypass routes, the five routes of the tunnels, two resolver entries.
	if journaled != 9 {
		t.Errorf("the last run journaled %d records, want 9", journaled)
	}
	e.r = r2
	if err := e.removeStale("9.9.9.9/32"); err != nil {
		t.Fatal(err)
	}
	e.checkIPRoutes("8.8.4.4/32 via 192.168.51.1 dev en0 metric 0")
}

// The interface a route of the journal went through is gone when the daemon
// starts again, and the route with it. A route that looks like it on another
// interface is not the route of the journal.
func TestLinuxRecoveryWhenTheInterfaceOfARouteIsGone(t *testing.T) {
	setup := func(t *testing.T) *env {
		e := newEnv(t, linuxCase)
		e.addTunnel("wg0", "10.6.0.2/24")
		e.announce(endpoints(up("wg", 1, "wg0", tunnel.RoleFull, "0.0.0.0/0"), "203.0.113.10"))
		e.crash("wg0")
		e.host.DestroyInterface("en0") // a USB adapter pulled out: its routes go with it
		return e
	}
	t.Run("nothing replaces it", func(t *testing.T) {
		e := setup(t)

		e.newReconciler()

		e.checkIPRoutes()
		if left := e.unresolved(); len(left) != 0 {
			t.Errorf("unresolved: %+v", left)
		}
		if note := removedNote(e, "203.0.113.10/32"); note != "already gone" {
			t.Errorf("journal note: %q", note)
		}
	})
	t.Run("another interface has a route like it", func(t *testing.T) {
		e := setup(t)
		e.host.AddPhysical("eth1", pfx("192.168.51.136/24"), ip("192.168.51.1"))
		e.host.Inject(osnet.Route{Dst: pfx("203.0.113.10/32"), Gateway: ip("192.168.51.1"), Iface: "eth1", Metric: bypassMetric, Static: true})

		e.newReconciler()

		e.checkIPRoutes("203.0.113.10/32 via 192.168.51.1 dev eth1 metric 1")
		if note := removedNote(e, "203.0.113.10/32"); note != "changed by another program, left in place" {
			t.Errorf("journal note: %q", note)
		}
		if left := e.unresolved(); len(left) != 0 {
			t.Errorf("unresolved: %+v", left)
		}
	})
}

// The kernel gave the index of the interface to another one. The route on it is
// told from ours by the name of the interface.
func TestLinuxRecoveryWhenTheIndexOfAnInterfaceWasReused(t *testing.T) {
	for _, tt := range []struct {
		name  string
		iface string
		note  string
		want  []string
	}{
		{"the same interface", "en0", "removed after restart", nil},
		{"another interface with the index", "usb0", "changed by another program, left in place", []string{"203.0.113.10/32 via 192.168.51.1 dev usb0 metric 1"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, linuxCase)
			e.addTunnel("wg0", "10.6.0.2/24")
			e.announce(endpoints(up("wg", 1, "wg0", tunnel.RoleFull, "0.0.0.0/0"), "203.0.113.10"))
			index := e.host.ifIndex("en0")
			e.crash("wg0")
			e.host.DestroyInterface("en0")
			e.host.Routes.AddInterface(osnet.Interface{Name: tt.iface, Index: int(index), Up: true, Addrs: pfxs("192.168.51.185/24")})
			e.host.Sync()
			e.host.Inject(osnet.Route{Dst: pfx("203.0.113.10/32"), Gateway: ip("192.168.51.1"), Iface: tt.iface, Metric: bypassMetric, Static: true})
			if got := e.host.ifIndex(tt.iface); got != index {
				t.Fatalf("setup: the index is %d, want %d", got, index)
			}

			e.newReconciler()

			e.checkIPRoutes(tt.want...)
			if note := removedNote(e, "203.0.113.10/32"); note != tt.note {
				t.Errorf("journal note %q, want %q", note, tt.note)
			}
		})
	}
}

// removedNote is the note of the record that closed the route to dst.
func removedNote(e *env, dst string) string {
	e.t.Helper()
	for _, rec := range e.journalFile() {
		if rec.State == stateRemoved && rec.Kind == kindRoute && rec.Key == dst && rec.Note != "" {
			return rec.Note
		}
	}
	return ""
}

func removedNotes(e *env) []string {
	var notes []string
	for _, rec := range e.journalFile() {
		if rec.State == stateRemoved && rec.Kind == kindRoute && rec.Note != "" && rec.Note != "removed after restart" {
			notes = append(notes, rec.Note)
		}
	}
	return notes
}

// A crash after the journal write and before the add, or before the
// confirmation, leaves a pending record that has only what the route was asked
// to be.
func TestLinuxRecoveryFromAPendingRecord(t *testing.T) {
	e := newEnv(t, linuxCase)
	bypass := osnet.Route{Dst: pfx("203.0.113.10/32"), Gateway: ip("192.168.51.1"), Iface: "en0", Metric: bypassMetric, Static: true}
	e.host.Inject(bypass) // crashed right after the add
	e.crash()
	writeJournal(t, e.journal,
		e.pendingLinuxRoute("203.0.113.10/32", bypass),
		e.pendingLinuxRoute("198.51.100.7/32", osnet.Route{Gateway: ip("192.168.51.1"), Iface: "en0", Metric: bypassMetric}), // never added
	)

	e.newReconciler()

	e.checkIPRoutes()
	if left := e.unresolved(); len(left) != 0 {
		t.Errorf("unresolved: %+v", left)
	}
}

// Journals written for the other routing tables name no route of this one, and are
// not turned into deletes.
func TestLinuxJournalOfAnotherPlatformIsNeverTurnedIntoDeletes(t *testing.T) {
	windows := `{"seq":1,"time":"2026-10-07T12:00:00Z","owner":"wg","kind":"route","key":"203.0.113.10/32","state":"applied","gateway":"192.168.51.1","iface":"Ethernet","ifindex":12,"fingerprint":"192.168.51.1|12|1|0x0"}` + "\n"
	for name, journal := range map[string]string{"macOS": macosJournal, "Windows": windows} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, linuxCase)
			e.host.Inject(osnet.Route{Dst: pfx("203.0.113.10/32"), Gateway: ip("192.168.51.1"), Iface: "en0", Metric: bypassMetric, Static: true})
			e.host.Inject(osnet.Route{Dst: pfx("198.51.100.7/32"), Gateway: ip("192.168.51.1"), Iface: "en0", Metric: bypassMetric, Static: true})
			e.crash()
			if err := os.WriteFile(e.journal, []byte(journal), 0o600); err != nil {
				t.Fatal(err)
			}

			e.newReconciler()

			e.checkIPRoutes("198.51.100.7/32 via 192.168.51.1 dev en0 metric 1", "203.0.113.10/32 via 192.168.51.1 dev en0 metric 1")
			if left := e.unresolved(); len(left) != 0 {
				t.Errorf("unresolved: %+v", left)
			}
			if !slices.Contains(removedNotes(e), "written for another kind of routing table, left in place") {
				t.Errorf("journal notes: %v", removedNotes(e))
			}
		})
	}
}

// A route that somebody else changed since is theirs: with the key of the journal
// (destination and metric) but another router.
func TestLinuxRecoveryLeavesARouteThroughAnotherRouter(t *testing.T) {
	e := newEnv(t, linuxCase)
	e.addTunnel("wg0", "10.6.0.2/24")
	e.announce(endpoints(up("wg", 1, "wg0", tunnel.RoleFull, "0.0.0.0/0"), "203.0.113.10"))
	e.crash("wg0")
	e.host.Inject(osnet.Route{Dst: pfx("203.0.113.10/32"), Gateway: ip("192.168.51.77"), Iface: "en0", Metric: bypassMetric, Static: true})

	e.newReconciler()

	e.checkIPRoutes("203.0.113.10/32 via 192.168.51.77 dev en0 metric 1")
	if !slices.Contains(removedNotes(e), "changed by another program, left in place") {
		t.Errorf("journal notes: %v", removedNotes(e))
	}
}

// What the routes of the kernel (the connected ones) and the other programs are
// like when the daemon is started on a machine that was never touched: a stale
// route is told apart by its interface, next hop and metric.
func TestLinuxStaleRoutesAreNamedByInterfaceNextHopAndMetric(t *testing.T) {
	e := newEnv(t, linuxCase)
	e.addTunnel("tun0", "10.8.0.6/24")
	e.host.Inject(osnet.Route{Dst: pfx("198.51.100.88/32"), Gateway: ip("10.99.0.1"), Iface: "en0", Static: true})
	e.host.Inject(osnet.Route{Dst: pfx("198.51.100.88/32"), Gateway: ip("10.99.0.2"), Iface: "en0", Static: true, Metric: 7})
	e.announce(up("ovpn", 1, "tun0", tunnel.RoleSplit, "10.50.0.0/16"))

	stale := e.r.Report().Stale
	if len(stale) != 2 || stale[0].Key == stale[1].Key {
		t.Fatalf("two stale routes need two names: %+v", stale)
	}
	if want := fmt.Sprintf("198.51.100.88/32@%d/10.99.0.1/0", e.host.ifIndex("en0")); stale[0].Key != want {
		t.Errorf("key %q, want %q", stale[0].Key, want)
	}
	if err := e.r.RemoveStale(stale[0].Key); err != nil {
		t.Fatal(err)
	}

	e.checkIPRoutes("10.50.0.0/16 dev tun0 metric 5", "198.51.100.88/32 via 10.99.0.2 dev en0 metric 7")
	if rest := e.r.Report().Stale; len(rest) != 1 || rest[0].Key != stale[1].Key {
		t.Errorf("stale after the removal: %+v", rest)
	}
}

// findStale on a Linux table: the route of an endpoint that leaves through another
// interface than the default route is stale although the router is the same; the
// peer of a point-to-point device is on no subnet it lists.
func TestLinuxFindStale(t *testing.T) {
	ns := osnet.NetState{
		Interfaces: []osnet.Interface{
			{Name: "eth0", Index: 2, Up: true, Addrs: pfxs("192.168.51.185/24")},
			{Name: "eth1", Index: 3, Up: true, Addrs: pfxs("192.168.51.136/24")},
			{Name: "tun0", Index: 4, Up: true, Tunnel: true, Addrs: pfxs("10.8.0.6/32")},
		},
		DefaultV4: &osnet.Nexthop{Gateway: ip("192.168.51.1"), Iface: "eth1"},
	}
	endpoint := pfx("203.0.113.10/32")
	tests := []struct {
		name string
		rt   osnet.Route
		want string
	}{
		{"endpoint on the interface of the default route", osnet.Route{Dst: endpoint, Gateway: ip("192.168.51.1"), Iface: "eth1", IfIndex: 3, Metric: 1}, ""},
		{"endpoint on another interface of the same router", osnet.Route{Dst: endpoint, Gateway: ip("192.168.51.1"), Iface: "eth0", IfIndex: 2, Metric: 1}, "interface is not the default route's"},
		{"endpoint through another router", osnet.Route{Dst: endpoint, Gateway: ip("192.168.51.2"), Iface: "eth1", IfIndex: 3, Metric: 1}, "gateway is not the default route's"},
		{"router on no subnet of the interface", osnet.Route{Dst: pfx("198.51.100.5/32"), Gateway: ip("10.99.0.1"), Iface: "eth1", IfIndex: 3}, "gateway is not on the subnet of eth1"},
		{"router behind a point-to-point device", osnet.Route{Dst: pfx("198.51.100.5/32"), Gateway: ip("10.8.0.5"), Iface: "tun0", IfIndex: 4}, ""},
		{"link-local router of IPv6, with its zone", osnet.Route{Dst: pfx("2001:db8::5/128"), Gateway: ip("fe80::1").WithZone("eth0"), Iface: "eth0", IfIndex: 2}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.rt.Static = true
			table := map[routeKey]osnet.Route{KeyLinux.key(tt.rt): tt.rt}

			got := findStale(KeyLinux, table, ns, map[netip.Prefix]bool{endpoint: true}, func(osnet.Route) bool { return false })

			switch {
			case tt.want == "" && len(got) != 0:
				t.Errorf("reported as stale: %+v", got)
			case tt.want != "" && (len(got) != 1 || got[0].Reason != tt.want):
				t.Errorf("got %+v, want %q", got, tt.want)
			}
		})
	}
}

// The resolver entry names the device of the tunnel. Moving the tunnel to
// another device writes it again; a pass that changes nothing does not.
func TestLinuxResolverEntriesNameTheTunnelDevice(t *testing.T) {
	e := newEnv(t, linuxCase)
	e.addTunnel("tun0", "10.8.0.6/24")
	e.addTunnel("tun1", "10.8.1.6/24")
	in := nameserver(withGateways(up("ovpn", 1, "tun0", tunnel.RoleSplit, "192.168.1.0/24"), "10.8.0.1", ""), "192.168.1.1", "asus.lan")

	e.announce(in)

	if got := e.host.DNS.Entries("ovpn"); len(got) != 1 || got[0].Iface != "tun0" {
		t.Fatalf("entries: %+v", got)
	}
	if rep := e.dnsReport("ovpn"); rep.State != tunnel.RouteInstalled {
		t.Errorf("report: %+v", rep)
	}
	applies := count(dnsOps(e), "apply")
	e.change(osnet.ChangeHeartbeat)
	e.announce(in)
	if n := count(dnsOps(e), "apply"); n != applies {
		t.Errorf("a pass that changed nothing wrote the entries again (%d, was %d)", n, applies)
	}

	in.Iface = "tun1"
	e.announce(in)

	if got := e.host.DNS.Entries("ovpn"); len(got) != 1 || got[0].Iface != "tun1" {
		t.Errorf("entries after the move: %+v", got)
	}
	if n := count(dnsOps(e), "apply"); n != applies+1 {
		t.Errorf("the entry should be written once more, applies %d, was %d", n, applies)
	}

	e.r.Withdraw("ovpn")
	if owned, _ := e.host.DNS.Owned(); len(owned) != 0 {
		t.Errorf("resolver entries left: %v", owned)
	}
}

// The entry of an earlier run is swept by its marker, whatever device it was
// written for.
func TestLinuxRecoverySweepsResolverEntriesOfAnyDevice(t *testing.T) {
	e := newEnv(t, linuxCase)
	e.host.DNS.Leave("ghost", osnet.DNSEntry{Servers: ips("10.0.0.53"), MatchDomains: []string{"corp.lan"}, Iface: "tun9"})
	e.crash()

	e.newReconciler()

	if owned, _ := e.host.DNS.Owned(); len(owned) != 0 {
		t.Errorf("marked entries survived: %v", owned)
	}
}

// Where the system keeps no resolver settings per interface, the entry names none:
// macOS writes the same entries it always did.
func TestResolverEntriesNameNoDeviceOnMacOS(t *testing.T) {
	e := newEnv(t, macosCase)
	e.addTunnel("utun11", "10.8.0.6/24")

	e.announce(asusIntent())

	if got := e.host.DNS.Entries("asus"); len(got) != 1 || got[0].Iface != "" {
		t.Errorf("entries: %+v", got)
	}
	if got := e.dnsReport("asus"); got.State != tunnel.RouteInstalled {
		t.Errorf("report: %+v", got)
	}
}

func TestEntryEqualComparesTheInterface(t *testing.T) {
	a := osnet.DNSEntry{Servers: ips("10.0.0.53"), MatchDomains: []string{"corp.lan"}, Order: 1, Iface: "tun0"}
	b := a
	if !entryEqual(a, b) {
		t.Error("equal entries differ")
	}
	b.Iface = "tun1"
	if entryEqual(a, b) {
		t.Error("entries for another interface are equal")
	}
}

func TestLinuxInterfaceNamesAreAtMostFifteenBytes(t *testing.T) {
	in := up("ovpn", 1, "plaitway-ovpn-01", tunnel.RoleSplit, "10.50.0.0/16")
	if len(in.Iface) != 16 {
		t.Fatalf("setup: %d bytes", len(in.Iface))
	}
	if err := validateIntent(KeyLinux, in); err == nil {
		t.Error("accepted a name of 16 bytes")
	}
	in.Iface = in.Iface[:15]
	if err := validateIntent(KeyLinux, in); err != nil {
		t.Errorf("refused a name of 15 bytes: %v", err)
	}
	if KeyLinux.maxIfaceName() != 15 {
		t.Errorf("limit %d", KeyLinux.maxIfaceName())
	}
}

// A route report tells an overridden route from one that failed, on every table
// that can hold both routes.
func TestOverriddenRouteIsMarkedInTheReport(t *testing.T) {
	for _, k := range []keyingCase{linuxCase, windowsCase} {
		t.Run(k.name, func(t *testing.T) {
			e := newEnv(t, k)
			e.addTunnel("utun10", "10.6.0.2/24")
			e.addForeignVPN(foreignAdapterMetric)
			e.foreignDefault(foreignRouteMetric)

			e.announce(up("wg", 1, "utun10", tunnel.RoleFull, "0.0.0.0/0"))

			rr := e.routeReport("0.0.0.0/1", "wg")
			if rr.State != tunnel.RouteFailed || !rr.Overridden {
				t.Errorf("report: %+v", rr)
			}
			e.host.Routes.Remove(pfx("0.0.0.0/1"))
			e.change(osnet.ChangeRoute)
			if rr := e.routeReport("0.0.0.0/1", "wg"); rr.State != tunnel.RouteInstalled || rr.Overridden {
				t.Errorf("report after the other route went: %+v", rr)
			}
		})
	}
	// A conflict is a failure that is not an override.
	e := newEnv(t, macosCase)
	e.addTunnel("utun11", "10.8.0.6/24")
	e.host.Inject(osnet.Route{Dst: pfx("192.168.1.0/24"), Iface: "en0", Static: true})
	e.announce(asusIntent())
	if rr := e.routeReport("192.168.1.0/24", "asus"); rr.State != tunnel.RouteFailed || rr.Overridden {
		t.Errorf("report: %+v", rr)
	}
}

// Rebuilding from scratch with a Linux table: everything of ours is removed and
// put back, a route of another program with the same prefix stays.
func TestLinuxResyncRebuildsFromScratch(t *testing.T) {
	e := newEnv(t, linuxCase)
	e.addTunnel("wg0", "10.6.0.2/24")
	e.host.Inject(osnet.Route{Dst: pfx("0.0.0.0/1"), Gateway: ip("192.168.51.254"), Iface: "en0", Metric: 300, Static: true})
	e.announce(wgIntent2())
	want := e.ipRoutes()

	if err := e.r.Resync(); err != nil {
		t.Fatal(err)
	}

	e.checkIPRoutes(want...)
	e.noFailedOps()
}

// The Linux table through the scenarios that do not depend on how a platform
// keys its routes: the same machine-level behavior as on the others.
func TestScenariosThatHoldOnLinux(t *testing.T) {
	for name, scenario := range map[string]func(*testing.T, keyingCase){
		"AnnounceInstallsBypassBeforeReturning":      testAnnounceInstallsBypassBeforeReturning,
		"PrioritiesShadowAndPromote":                 testPrioritiesShadowAndPromote,
		"StandbyFullTunnelTakesOverTheDefaultRoute":  testStandbyFullTunnelTakesOverTheDefaultRoute,
		"LocalSubnetConflict":                        testLocalSubnetConflict,
		"ReconnectingTunnelKeepsItsRoutes":           testReconnectingTunnelKeepsItsRoutes,
		"TunnelInterfaceVanishesAndComesBack":        testTunnelInterfaceVanishesAndComesBack,
		"ReapplyingChangesNothing":                   testReapplyingChangesNothing,
		"ApplyAndTeardownOrder":                      testApplyAndTeardownOrder,
		"StaleBypassIsRepairedBeforeRebind":          testStaleBypassIsRepairedBeforeRebind,
		"ForeignEndpointRouteThroughAnotherRouter":   testForeignEndpointRouteThroughAnotherRouterIsStale,
		"RebindRules":                                testRebindRules,
		"RebindIgnoresWhatIsNotTheUnderlay":          testRebindIgnoresWhatIsNotTheUnderlay,
		"AnnounceSeesTheTunnelInterfaceItJustGot":    testAnnounceSeesTheTunnelInterfaceItJustGotCreated,
		"WakeSchedulesASecondPass":                   testWakeSchedulesASecondPass,
		"RunRemovesEverythingOnExit":                 testRunRemovesEverythingOnExit,
		"ResyncRebuildsFromScratch":                  testResyncRebuildsFromScratch,
		"NonConvergenceTriggersReset":                testNonConvergenceTriggersReset,
		"UnreachableDoesNotTriggerReset":             testUnreachableDoesNotTriggerReset,
		"AnnounceRejectsUnsafeIdentifiers":           testAnnounceRejectsUnsafeIdentifiers,
		"UnusableDataDoesNotBreakATunnel":            testUnusableDataDoesNotBreakATunnel,
		"DNSIsWrittenAgainAfterANetworkChangeOrWake": testDNSIsWrittenAgainAfterANetworkChangeOrWake,
		"UnreachableWaitsForTheNextEvent":            testUnreachableWaitsForTheNextEvent,
		"PendingWorkIsRetriedByTimer":                testPendingWorkIsRetriedByTimer,
		"DeletingAMissingRouteIsSuccess":             testDeletingAMissingRouteIsSuccess,
		"RouteRemovedByAnotherProgramIsNotAnError":   testRouteRemovedByAnotherProgramIsNotAnError,
		"ExistsThatVanishedIsRetried":                testExistsThatVanishedIsRetried,
		"OtherAddErrorsAreFailures":                  testOtherAddErrorsAreFailures,
		"RouteThatDoesNotStayIsAFailure":             testRouteThatDoesNotStayIsAFailure,
		"RecoveryKeepsARouteItCouldNotDelete":        testRecoveryKeepsARouteItCouldNotDelete,
		"RecoverySweepsResolverEntries":              testRecoverySweepsResolverEntriesWithoutAJournal,
		"RandomSequences":                            testRandomSequences,
		"ConcurrentUse":                              testConcurrentUse,
	} {
		t.Run(name, func(t *testing.T) { scenario(t, linuxCase) })
	}
}

// What each table is, in one place.
func TestWhatEachKeyingDoes(t *testing.T) {
	tests := []struct {
		keying                                                     RouteKeying
		byInterface, keyedByMetric, tunnelGateway, interfaceMetric bool
		dnsPerInterface                                            bool
		maxName                                                    int
	}{
		{KeyByPrefix, false, false, false, false, false, 15},
		{KeyByPrefixInterfaceNextHop, true, false, true, true, false, 256},
		{KeyLinux, true, true, false, false, true, 15},
	}
	for _, tt := range tests {
		k := tt.keying
		if !k.valid() || k.byInterface() != tt.byInterface || k.keyedByMetric() != tt.keyedByMetric ||
			k.namesTunnelGateway() != tt.tunnelGateway || k.addsInterfaceMetric() != tt.interfaceMetric ||
			k.dnsPerInterface() != tt.dnsPerInterface || k.maxIfaceName() != tt.maxName {
			t.Errorf("keying %d does not behave as listed: %+v", k, tt)
		}
	}
	if RouteKeying(99).valid() {
		t.Error("an unknown keying is valid")
	}
}

func TestLinuxRouteKeyIsDestinationAndMetric(t *testing.T) {
	via := func(iface string, index uint32, gateway string, metric uint32) osnet.Route {
		rt := osnet.Route{Dst: pfx("203.0.113.10/32"), Iface: iface, IfIndex: index, Metric: metric}
		if gateway != "" {
			rt.Gateway = ip(gateway)
		}
		return rt
	}
	first := KeyLinux.key(via("eth0", 2, "192.168.51.1", 1))

	if got := KeyLinux.key(via("tun0", 7, "", 1)); got != first {
		t.Errorf("the interface and next hop are no part of the key: %+v and %+v", got, first)
	}
	if got := KeyLinux.key(via("eth0", 2, "192.168.51.1", 2)); got == first {
		t.Error("the metric is part of the key")
	}
	if got := KeyLinux.key(osnet.Route{Dst: pfx("203.0.113.77/24"), Metric: 1}); got.dst != pfx("203.0.113.0/24") {
		t.Errorf("the destination is masked: %v", got.dst)
	}
	if first.String() != "203.0.113.10/32 metric 1" {
		t.Errorf("text: %q", first)
	}
	if got := KeyLinux.identity(via("eth0", 2, "fe80::1", 1)); got != "203.0.113.10/32@2/fe80::1/1" {
		t.Errorf("identity %q", got)
	}
	low, high := first, KeyLinux.key(via("eth0", 2, "192.168.51.1", 9))
	if compareKeys(low, high) >= 0 || compareKeys(high, low) <= 0 || compareKeys(low, low) != 0 {
		t.Error("keys of one destination are ordered by metric")
	}
	// The tables that key by interface and next hop, or by the prefix alone, are as
	// they were.
	if got := KeyByPrefixInterfaceNextHop.key(via("eth0", 2, "192.168.51.1", 1)); got.metric != 0 || got.ifIndex != 2 {
		t.Errorf("Windows key %+v", got)
	}
	if got := KeyByPrefix.key(via("eth0", 2, "192.168.51.1", 1)); got != (routeKey{dst: pfx("203.0.113.10/32")}) {
		t.Errorf("macOS key %+v", got)
	}
}

func TestLinuxSameRoute(t *testing.T) {
	base := osnet.Route{Dst: pfx("203.0.113.10/32"), Gateway: ip("fe80::1"), Iface: "eth0", IfIndex: 4, Metric: 1, Static: true}
	base.Dst = pfx("2001:db8::10/128")
	with := func(change func(*osnet.Route)) osnet.Route {
		rt := base
		change(&rt)
		return rt
	}
	tests := []struct {
		name  string
		other osnet.Route
		same  bool
	}{
		{"identical", base, true},
		{"with the zone of the router", with(func(r *osnet.Route) { r.Gateway = r.Gateway.WithZone("eth0") }), true},
		{"flags and protocol are no part of it", with(func(r *osnet.Route) { r.Flags, r.Static = 0x80b, false }), true},
		{"another metric", with(func(r *osnet.Route) { r.Metric = 2 }), false},
		{"another interface", with(func(r *osnet.Route) { r.IfIndex = 5 }), false},
		{"another router", with(func(r *osnet.Route) { r.Gateway = ip("fe80::2") }), false},
		{"no router", with(func(r *osnet.Route) { r.Gateway = netip.Addr{} }), false},
		{"another prefix", with(func(r *osnet.Route) { r.Dst = pfx("2001:db8::11/128") }), false},
		{"a blackhole", with(func(r *osnet.Route) { r.Blackhole = true }), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := KeyLinux.same(base, tt.other); got != tt.same {
				t.Errorf("got %v, want %v", got, tt.same)
			}
		})
	}
}

// What the journal writes for a route on Linux names the route again: the key, and
// what a restart compares before it deletes.
func TestLinuxJournalRecordNamesTheRoute(t *testing.T) {
	rt := osnet.Route{Dst: pfx("2001:db8::10/128"), Gateway: ip("fe80::1").WithZone("eth0"), Iface: "eth0", IfIndex: 4, Metric: 1, Static: true, Flags: 0x807}
	rec := record{Kind: kindRoute, Key: rt.Dst.String(), State: stateApplied, Fingerprint: KeyLinux.fingerprint(rt)}
	KeyLinux.stamp(&rec, rt)

	if rec.Gateway != "fe80::1" || rec.Iface != "eth0" || rec.IfIndex != 4 || rec.Metric != 1 {
		t.Fatalf("record: %+v", rec)
	}
	key, err := KeyLinux.recordKey(rec)
	if err != nil || key != KeyLinux.key(rt) {
		t.Errorf("key %+v, %v; want %+v", key, err, KeyLinux.key(rt))
	}
	other := rec
	other.Metric = 2
	if rec.id() == other.id() {
		t.Error("the metric makes another record")
	}
	// A record of the other tables has no metric: it names no route here.
	windows := rec
	windows.Metric = 0
	if _, err := KeyLinux.recordKey(windows); err != errOtherTable {
		t.Errorf("a record without a metric: %v", err)
	}
	macos := record{Kind: kindRoute, Key: "2001:db8::10/128", Gateway: "fe80::1", Iface: "en0"}
	if _, err := KeyLinux.recordKey(macos); err != errOtherTable {
		t.Errorf("a record without an index: %v", err)
	}
	// The other tables read their records as they did.
	for _, k := range []RouteKeying{KeyByPrefix, KeyByPrefixInterfaceNextHop} {
		var written record
		k.stamp(&written, rt)
		if written.Metric != 0 {
			t.Errorf("keying %d writes a metric", k)
		}
	}

	// recognizes: the same route, and the ones that are not.
	tests := []struct {
		name   string
		mutate func(*osnet.Route)
		pend   bool // the record has no fingerprint yet
		want   bool
	}{
		{"as installed", func(*osnet.Route) {}, false, true},
		{"as installed, pending", func(*osnet.Route) {}, true, true},
		{"the interface has another name", func(r *osnet.Route) { r.Iface = "usb0" }, false, false},
		{"the interface has another name, pending", func(r *osnet.Route) { r.Iface = "usb0" }, true, false},
		{"the name is not known", func(r *osnet.Route) { r.Iface = "" }, true, true},
		{"another index", func(r *osnet.Route) { r.IfIndex = 5 }, true, false},
		{"another router", func(r *osnet.Route) { r.Gateway = ip("fe80::2") }, true, false},
		{"other flags", func(r *osnet.Route) { r.Flags = 0x80b }, false, false},
		{"other flags, pending", func(r *osnet.Route) { r.Flags = 0x80b }, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := rt
			tt.mutate(&actual)
			r := rec
			if tt.pend {
				r.Fingerprint = ""
			}
			if got := KeyLinux.recognizes(r, actual); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// A tunnel route to the very address of another tunnel's endpoint is replaced by
// the bypass route of that endpoint. They have different metrics, so the kernel
// holds both for a moment, and the bypass route has to be the one in use: it goes
// in first, the tunnel route after it.
func TestLinuxBypassRouteIsAddedBeforeTheTunnelRouteItReplaces(t *testing.T) {
	e := newEnv(t, linuxCase)
	e.addTunnel("tun0", "10.8.0.6/24")
	e.addTunnel("wg0", "10.6.0.2/24")
	e.announce(up("a", 1, "tun0", tunnel.RoleSplit, "203.0.113.10/32"))
	e.checkIPRoutes("203.0.113.10/32 dev tun0 metric 5")

	e.announce(endpoints(state(up("b", 2, "wg0", tunnel.RoleSplit), tunnel.StateConnecting), "203.0.113.10"))

	e.checkIPRoutes("203.0.113.10/32 via 192.168.51.1 dev en0 metric 1")
	e.noFailedOps()
	ops := e.ipOps()
	add := slices.Index(ops, "add 203.0.113.10/32 via 192.168.51.1 dev en0 metric 1")
	del := slices.Index(ops, "delete 203.0.113.10/32 dev tun0 metric 5")
	if add < 0 || del < 0 || add > del {
		t.Errorf("the bypass route goes in before the tunnel route goes out: %v", ops)
	}
	if rr := e.routeReport("203.0.113.10/32", "a"); rr.State != tunnel.RouteShadowed || rr.ShadowedBy != "b" {
		t.Errorf("report: %+v", rr)
	}
}
