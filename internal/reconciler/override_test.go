package reconciler

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// On Windows a route of another program to the same prefix is a neighbour of
// ours, and the lowest effective metric (route plus interface) decides which of
// the two carries the traffic. These scenarios put the owner's OpenVPN GUI, which
// is connected with redirect-gateway def1, beside a Plaitway tunnel.

const (
	foreignAdapter = "gui-tap"
	// foreignAdapterMetric is low enough to beat a Plaitway tunnel together with
	// a low route metric.
	foreignAdapterMetric = 2
	foreignRouteMetric   = 1
	// ourTunnelMetric is what a Plaitway tunnel route adds up to: the metric of
	// its route and of its adapter.
	ourTunnelMetric = windowsTunnelMetric + tunnelAdapterMetric
	ourBypassMetric = windowsBypassMetric + physicalInterfaceMetric
)

// addForeignVPN connects the other program's adapter, with the metric it has.
func (e *env) addForeignVPN(interfaceMetric uint32) {
	e.host.AddInterface(osnet.Interface{
		Name: foreignAdapter, Up: true, Tunnel: true, Addrs: pfxs("10.9.0.2/24", "fd09::2/64"), Metric: interfaceMetric,
	})
	e.host.Sync()
}

// foreignRoute is a route of the other program through its adapter.
func (e *env) foreignRoute(dst string, metric uint32) osnet.Route {
	gateway := ip("10.9.0.1")
	if pfx(dst).Addr().Is6() {
		gateway = ip("fd09::1")
	}
	rt := osnet.Route{Dst: pfx(dst), Gateway: gateway, Iface: foreignAdapter, Static: true, Metric: metric}
	e.host.Inject(rt)
	return rt
}

// foreignDefault is what redirect-gateway def1 adds, in both families.
func (e *env) foreignDefault(metric uint32) {
	for _, dst := range []string{"0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1"} {
		e.foreignRoute(dst, metric)
	}
}

// overriddenBy is the detail of a route that the foreign route to dst outranks.
func (e *env) overriddenBy(dst, gateway string, theirs, ours uint64) string {
	return fmt.Sprintf("overridden by %s via %s (interface %d): effective metric %d, ours %d",
		dst, gateway, e.host.ifIndex(foreignAdapter), theirs, ours)
}

func (e *env) expectInstalled(dst string, owner tunnel.OwnerID) {
	e.t.Helper()
	if rr := e.routeReport(dst, owner); rr.State != tunnel.RouteInstalled || rr.Detail != "" {
		e.t.Errorf("%s of %s: %+v, want installed", dst, owner, rr)
	}
}

func (e *env) expectOverridden(dst string, owner tunnel.OwnerID, detail string) {
	e.t.Helper()
	if rr := e.routeReport(dst, owner); rr.State != tunnel.RouteFailed || rr.Detail != detail {
		e.t.Errorf("%s of %s: %+v\nwant state failed and %q", dst, owner, rr, detail)
	}
}

// settled checks that more passes change nothing and ask for no retry: the
// verdict is a state, not a failure to retry.
func (e *env) expectSettled() {
	e.t.Helper()
	before := e.opLines()
	for range 3 {
		e.change(osnet.ChangeHeartbeat)
		e.change(osnet.ChangeRoute)
	}
	if after := e.opLines(); !slices.Equal(before, after) {
		e.t.Errorf("a pass on an unchanged table did something: %v", after[len(before):])
	}
	if e.r.dirty || e.r.failedPasses != 0 || e.r.failStreak != 0 {
		e.t.Errorf("an overridden route is no failure to retry: dirty %v, failed %d, streak %d", e.r.dirty, e.r.failedPasses, e.r.failStreak)
	}
}

// The case that made the finding: the other VPN's default routes win by metric,
// all traffic goes through it, and Plaitway used to report its full tunnel as
// installed.
func TestForeignDefaultRoutesWinByMetric(t *testing.T) {
	e := newEnv(t, windowsCase)
	e.addTunnel("utun10", "10.6.0.2/24")
	e.addForeignVPN(foreignAdapterMetric)
	e.foreignDefault(foreignRouteMetric)

	e.announce(wgIntent())

	theirs := uint64(foreignAdapterMetric + foreignRouteMetric)
	e.expectOverridden("0.0.0.0/1", "wg", e.overriddenBy("0.0.0.0/1", "10.9.0.1", theirs, ourTunnelMetric))
	e.expectOverridden("128.0.0.0/1", "wg", e.overriddenBy("128.0.0.0/1", "10.9.0.1", theirs, ourTunnelMetric))
	e.expectOverridden("::/1", "wg", e.overriddenBy("::/1", "fd09::1", theirs, ourTunnelMetric))
	e.expectOverridden("8000::/1", "wg", e.overriddenBy("8000::/1", "fd09::1", theirs, ourTunnelMetric))
	if rr := e.routeReport("203.0.113.10/32", "wg"); rr.State != tunnel.RouteInstalled {
		t.Errorf("the endpoint's route has no rival: %+v", rr)
	}
	if rt := e.lookup("8.8.8.8"); rt.Iface != foreignAdapter {
		t.Fatalf("setup: the traffic should leave through the other VPN, it leaves through %s", rt.Iface)
	}

	// Ours stays where it is, untouched, and so does theirs.
	e.checkTable(
		"0.0.0.0/1 via 10.9.0.1 dev gui-tap",
		"0.0.0.0/1 dev utun10",
		"128.0.0.0/1 via 10.9.0.1 dev gui-tap",
		"128.0.0.0/1 dev utun10",
		"203.0.113.10/32 via 192.168.51.1 dev en0",
		"::/1 via fd09::1 dev gui-tap",
		"::/1 dev utun10",
		"8000::/1 via fd09::1 dev gui-tap",
		"8000::/1 dev utun10",
	)
	if ours, _ := e.host.Routes.Dump(); !slices.ContainsFunc(ours, func(rt osnet.Route) bool {
		return rt.Dst == pfx("0.0.0.0/1") && rt.Iface == "utun10" && rt.Metric == windowsTunnelMetric
	}) {
		t.Errorf("our metric was changed: %+v", ours)
	}
	for _, line := range e.opLines() {
		if strings.Contains(line, "10.9.0.1") || strings.Contains(line, "fd09::1") || strings.HasPrefix(line, "delete") {
			t.Errorf("the table was written for the foreign route or deleted from: %s", line)
		}
	}
	e.expectSettled()
	if n := strings.Count(e.logs.String(), "used instead of ours"); n != 4 {
		t.Errorf("the log says it %d times, want once per route:\n%s", n, e.logs)
	}

	// Withdrawing takes only ours.
	e.r.Withdraw("wg")
	e.checkTable(
		"0.0.0.0/1 via 10.9.0.1 dev gui-tap",
		"128.0.0.0/1 via 10.9.0.1 dev gui-tap",
		"::/1 via fd09::1 dev gui-tap",
		"8000::/1 via fd09::1 dev gui-tap",
	)
}

func TestOurRouteWinsAgainstAForeignRouteOfHigherMetric(t *testing.T) {
	e := newEnv(t, windowsCase)
	e.addTunnel("utun10", "10.6.0.2/24")
	e.addForeignVPN(foreignAdapterMetric)
	e.foreignDefault(ourTunnelMetric) // together with its adapter: 12 against 10

	e.announce(wgIntent())

	for _, dst := range []string{"0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1"} {
		e.expectInstalled(dst, "wg")
	}
	if rt := e.lookup("8.8.8.8"); rt.Iface != "utun10" {
		t.Errorf("traffic leaves through %s", rt.Iface)
	}
	e.expectSettled()
	if strings.Contains(e.logs.String(), "used instead of ours") {
		t.Errorf("nothing was overridden:\n%s", e.logs)
	}
}

// A tie is not a loss: the system breaks it in a way that cannot be read.
func TestEqualEffectiveMetricIsNotAVerdict(t *testing.T) {
	e := newEnv(t, windowsCase)
	e.addTunnel("utun10", "10.6.0.2/24")
	e.addForeignVPN(foreignAdapterMetric)
	e.foreignDefault(ourTunnelMetric - foreignAdapterMetric)

	e.announce(wgIntent())

	for _, dst := range []string{"0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1"} {
		e.expectInstalled(dst, "wg")
	}
	e.expectSettled()
}

// The other VPN connects after ours: nothing about the interfaces changes, only
// the table, and a route event is enough to change the verdict.
func TestForeignVPNConnectingLaterOverridesAndDisconnectingRestores(t *testing.T) {
	e := newEnv(t, windowsCase)
	e.addTunnel("utun10", "10.6.0.2/24")
	e.addForeignVPN(foreignAdapterMetric)
	e.announce(wgIntent())
	e.expectInstalled("0.0.0.0/1", "wg")
	before := e.r.netState

	e.foreignDefault(foreignRouteMetric)
	e.change(osnet.ChangeRoute)

	if after, _ := e.host.Net.Snapshot(); netChanged(before, after) {
		t.Fatal("setup: the network state changed, the route event alone is the point of this test")
	}
	theirs := uint64(foreignAdapterMetric + foreignRouteMetric)
	e.expectOverridden("0.0.0.0/1", "wg", e.overriddenBy("0.0.0.0/1", "10.9.0.1", theirs, ourTunnelMetric))
	if n := strings.Count(e.logs.String(), "used instead of ours"); n != 4 {
		t.Errorf("logged %d times", n)
	}

	for _, dst := range []string{"0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1"} {
		if !e.host.Routes.Remove(pfx(dst)) { // the lowest metric: theirs
			t.Fatalf("no route to %s", dst)
		}
	}
	e.change(osnet.ChangeRoute)

	e.expectInstalled("0.0.0.0/1", "wg")
	e.expectInstalled("::/1", "wg")
	if n := strings.Count(e.logs.String(), "our route is in use again"); n != 4 {
		t.Errorf("logged %d times that the routes are back:\n%s", n, e.logs)
	}
	if rt := e.lookup("8.8.8.8"); rt.Iface != "utun10" {
		t.Errorf("traffic leaves through %s", rt.Iface)
	}
}

// The adapter of the other VPN goes away with its routes.
func TestForeignVPNAdapterVanishing(t *testing.T) {
	e := newEnv(t, windowsCase)
	e.addTunnel("utun10", "10.6.0.2/24")
	e.addForeignVPN(foreignAdapterMetric)
	e.foreignDefault(foreignRouteMetric)
	e.announce(wgIntent())
	e.expectOverridden("::/1", "wg", e.overriddenBy("::/1", "fd09::1", foreignAdapterMetric+foreignRouteMetric, ourTunnelMetric))

	e.host.DestroyInterface(foreignAdapter)
	e.change(osnet.ChangeRoute)

	e.expectInstalled("0.0.0.0/1", "wg")
	e.expectInstalled("::/1", "wg")
}

// The interface metric is part of the comparison, and a change of it is a
// change of the network.
func TestInterfaceMetricDecidesTheVerdict(t *testing.T) {
	e := newEnv(t, windowsCase)
	e.addTunnel("utun10", "10.6.0.2/24")
	e.addForeignVPN(foreignAdapterMetric)
	e.foreignDefault(foreignRouteMetric)
	e.announce(wgIntent())
	e.expectOverridden("0.0.0.0/1", "wg", e.overriddenBy("0.0.0.0/1", "10.9.0.1", foreignRouteMetric+foreignAdapterMetric, ourTunnelMetric))

	e.host.Routes.SetInterfaceMetric(foreignAdapter, 50)
	e.host.Sync()
	e.change(osnet.ChangeRoute)
	e.expectInstalled("0.0.0.0/1", "wg")

	e.host.Routes.SetInterfaceMetric(foreignAdapter, 6)
	e.host.Sync()
	e.change(osnet.ChangeRoute)
	e.expectOverridden("0.0.0.0/1", "wg", e.overriddenBy("0.0.0.0/1", "10.9.0.1", foreignRouteMetric+6, ourTunnelMetric))

	// A faster adapter of ours changes the sum on our side: 5 and 1 against 7.
	e.host.Routes.SetInterfaceMetric("utun10", 1)
	e.host.Sync()
	e.change(osnet.ChangeRoute)
	e.expectInstalled("0.0.0.0/1", "wg")
}

// The host route that keeps the VPN server reachable is overridden by another
// program's host route to the same address, and the report says so: the tunnel
// would send its own packets into the other VPN.
func TestEndpointBypassRouteOverriddenByAForeignHostRoute(t *testing.T) {
	e := newEnv(t, windowsCase)
	e.addTunnel("utun10", "10.6.0.2/24")
	e.addForeignVPN(foreignAdapterMetric)
	e.foreignRoute("203.0.113.10/32", foreignRouteMetric)

	e.announce(wgIntent())

	want := e.overriddenBy("203.0.113.10/32", "10.9.0.1", foreignAdapterMetric+foreignRouteMetric, ourBypassMetric)
	e.expectOverridden("203.0.113.10/32", "wg", want)
	if rr := e.routeReport("203.0.113.10/32", "wg"); rr.Kind != tunnel.RouteBypass {
		t.Errorf("kind %v", rr.Kind)
	}
	e.expectInstalled("0.0.0.0/1", "wg") // no foreign default route
	e.expectSettled()

	e.host.Routes.Remove(pfx("203.0.113.10/32"))
	e.change(osnet.ChangeRoute)
	e.expectInstalled("203.0.113.10/32", "wg")
}

func TestForeignRoutesOfAnotherPrefixAreNoRival(t *testing.T) {
	e := newEnv(t, windowsCase)
	e.addTunnel("utun10", "10.6.0.2/24")
	e.addForeignVPN(foreignAdapterMetric)
	// More specific routes win for their own addresses, which is how every route of
	// another program is meant to work; a shorter one never wins.
	e.foreignRoute("10.0.0.0/8", 0)
	e.foreignRoute("192.168.77.0/24", 0)
	e.foreignRoute("203.0.113.0/24", 0)
	e.foreignRoute("::/0", 0)
	e.foreignRoute("2001:db8::/32", 0)

	e.announce(wgIntent())

	for _, dst := range []string{"0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1", "203.0.113.10/32"} {
		e.expectInstalled(dst, "wg")
	}
}

// Routes through an interface that is down carry nothing.
func TestForeignRouteOnAnInterfaceThatIsDownIsNoRival(t *testing.T) {
	e := newEnv(t, windowsCase)
	e.addTunnel("utun10", "10.6.0.2/24")
	e.addForeignVPN(foreignAdapterMetric)
	e.host.Routes.SetUp(foreignAdapter, false)
	e.foreignDefault(foreignRouteMetric)
	e.host.Sync()

	e.announce(wgIntent())

	e.expectInstalled("0.0.0.0/1", "wg")
	e.expectInstalled("::/1", "wg")
}

// While the network state has no interface metric, the comparison is made with
// what is there, and the detail says so.
func TestVerdictSaysWhenAnInterfaceMetricIsUnknown(t *testing.T) {
	t.Run("the other program's interface", func(t *testing.T) {
		e := newEnv(t, windowsCase)
		e.addTunnel("utun10", "10.6.0.2/24")
		// An interface that the network state does not list has no metric.
		e.host.Inject(osnet.Route{Dst: pfx("0.0.0.0/1"), Gateway: ip("10.9.0.1"), Iface: "ghost", IfIndex: 99, Static: true, Metric: 1})

		e.announce(wgIntent())

		want := fmt.Sprintf("overridden by 0.0.0.0/1 via 10.9.0.1 (interface 99): effective metric 1, ours %d; interface metric unknown", ourTunnelMetric)
		e.expectOverridden("0.0.0.0/1", "wg", want)
	})
	t.Run("our own interface", func(t *testing.T) {
		e := newEnv(t, windowsCase)
		e.addTunnel("utun10", "10.6.0.2/24")
		e.addForeignVPN(foreignAdapterMetric)
		e.foreignDefault(foreignRouteMetric)
		e.host.Routes.SetInterfaceMetric("utun10", 0)
		e.host.Sync()

		e.announce(wgIntent())

		want := e.overriddenBy("0.0.0.0/1", "10.9.0.1", foreignRouteMetric+foreignAdapterMetric, windowsTunnelMetric) + "; interface metric unknown"
		e.expectOverridden("0.0.0.0/1", "wg", want)
	})
}

// A list of routes that leaves out only what is not on the internet (the full
// tunnel of a profile with the private ranges taken out) beats our halves
// whatever the metric: every address has a more specific route.
func TestMoreSpecificRoutesThatCoverTheInternetOverrideTheHalves(t *testing.T) {
	e := newEnv(t, windowsCase)
	e.addTunnel("utun10", "10.6.0.2/24")
	e.addForeignVPN(500)
	for _, p := range allExcept(pfx("0.0.0.0/0"), privateV4) {
		e.foreignRoute(p.String(), 500)
	}
	e.foreignRoute("2000::/3", 500)

	e.announce(wgIntent())

	const prefix = "overridden by more specific routes, such as "
	for _, dst := range []string{"0.0.0.0/1", "128.0.0.0/1", "::/1"} {
		if rr := e.routeReport(dst, "wg"); rr.State != tunnel.RouteFailed || !strings.HasPrefix(rr.Detail, prefix) {
			t.Errorf("%s: %+v", dst, rr)
		}
	}
	if rr := e.routeReport("0.0.0.0/1", "wg"); !strings.HasPrefix(rr.Detail, prefix+"0.0.0.0/5 via 10.9.0.1 (interface") {
		t.Errorf("the first of the routes should be named: %q", rr.Detail)
	}
	// 8000::/1 holds no part of the global unicast range, so nothing in 2000::/3
	// can take its traffic.
	e.expectInstalled("8000::/1", "wg")
	e.expectSettled()
}

func TestMoreSpecificRoutesThatLeaveAGapAreNoOverride(t *testing.T) {
	tests := []struct {
		name   string
		routes []netip.Prefix
		// overridden are the halves that the routes do cover.
		overridden []string
	}{
		{"private networks of another program", pfxs("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"), nil},
		// The hole in 44.0.0.0/8 keeps the lower half out of reach; the upper half
		// is covered.
		{"everything but the private ranges and a /8", allExcept(pfx("0.0.0.0/0"), append(pfxs("44.0.0.0/8"), privateV4...)), []string{"128.0.0.0/1"}},
		{"half of the global unicast range", pfxs("2000::/4"), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, windowsCase)
			e.addTunnel("utun10", "10.6.0.2/24")
			e.addForeignVPN(500)
			for _, p := range tt.routes {
				e.foreignRoute(p.String(), 500)
			}

			e.announce(wgIntent())

			for _, dst := range []string{"0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1"} {
				rr := e.routeReport(dst, "wg")
				if covered := slices.Contains(tt.overridden, dst); covered != (rr.State == tunnel.RouteFailed) {
					t.Errorf("%s: %+v, overridden should be %v", dst, rr, covered)
				}
			}
		})
	}
}

// Routes the system derived from interface addresses are not a way out for the
// internet, and not a program's.
func TestConnectedRoutesAreNeverPiecesOfAForeignTunnel(t *testing.T) {
	pieces := allExcept(pfx("0.0.0.0/0"), privateV4)
	e := newEnv(t, windowsCase)
	e.addTunnel("utun10", "10.6.0.2/24")
	e.addForeignVPN(500)
	for _, p := range pieces {
		e.host.Inject(osnet.Route{Dst: p, Iface: foreignAdapter, Metric: 500}) // not Static
	}

	e.announce(wgIntent())

	e.expectInstalled("0.0.0.0/1", "wg")
}

// Whatever we installed ourselves, for any owner, is no rival of another route of
// ours.
func TestOurOwnRoutesAreNotForeign(t *testing.T) {
	e := newEnv(t, windowsCase)
	e.bothTunnels()
	e.announce(wgIntent())
	e.announce(asusIntent())
	e.foreignRoute("203.0.113.0/24", 0)

	table, err := e.r.dumpTable()
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	for _, rt := range e.r.foreignRoutes(table) {
		if rt.Static && rt.Dst.Bits() != 0 {
			seen = append(seen, routeLine(rt))
		}
	}

	if want := []string{"203.0.113.0/24 via 10.9.0.1 dev gui-tap"}; !slices.Equal(seen, want) {
		t.Errorf("foreign routes %v, want %v", seen, want)
	}
	for _, rr := range e.r.Report().Routes {
		if rr.State == tunnel.RouteFailed {
			t.Errorf("%+v", rr)
		}
	}
}

// macOS has no metrics, and a route of another program to the same prefix is a
// conflict that the table does not let both exist.
func TestMacOSHasNoMetricVerdict(t *testing.T) {
	e := newEnv(t, macosCase)
	e.addTunnel("utun11", "10.8.0.6/24")
	e.host.Inject(osnet.Route{Dst: pfx("192.168.1.0/24"), Iface: "en0", Static: true})

	e.announce(asusIntent())

	if rr := e.routeReport("192.168.1.0/24", "asus"); rr.State != tunnel.RouteFailed || rr.Detail != "held by another program via en0" {
		t.Errorf("%+v", rr)
	}
	if strings.Contains(e.logs.String(), "used instead of ours") {
		t.Errorf("the metric verdict ran on macOS:\n%s", e.logs)
	}
}
