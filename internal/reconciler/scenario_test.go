package reconciler

import (
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/osnet/fake"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// An Announce in StateConnecting has installed the bypass route it needs when
// it returns, so the engine can start connecting right away.
func TestAnnounceInstallsBypassBeforeReturning(t *testing.T) {
	eachKeying(t, testAnnounceInstallsBypassBeforeReturning)
}

func testAnnounceInstallsBypassBeforeReturning(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	e.addTunnel("utun10", "10.6.0.2/24")
	e.announce(up("wg", 1, "utun10", tunnel.RoleFull, "0.0.0.0/0"))
	e.checkTable("0.0.0.0/1 dev utun10", "128.0.0.0/1 dev utun10")

	e.announce(state(asusIntent(), tunnel.StateConnecting))

	e.checkTable(
		"0.0.0.0/1 dev utun10",
		"128.0.0.0/1 dev utun10",
		"198.51.100.7/32 via 192.168.51.1 dev en0",
	)
	if got := e.journalFor(kindRoute, "198.51.100.7/32"); !slices.Equal(got, []string{"pending 192.168.51.1", "applied 192.168.51.1"}) {
		t.Errorf("the route is journaled before and after it is added: %v", got)
	}
	if rr := e.routeReport("198.51.100.7/32", "asus"); rr.State != tunnel.RouteInstalled || rr.Kind != tunnel.RouteBypass || rr.Via != "192.168.51.1" {
		t.Errorf("report: %+v", rr)
	}
}

// The goal scenario of the whole product: the WireGuard full tunnel and the
// OpenVPN split tunnel up together, each reachable.
func TestFullAndSplitTunnelsTogether(t *testing.T) { eachKeying(t, testFullAndSplitTunnelsTogether) }

func testFullAndSplitTunnelsTogether(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	e.bothTunnels()
	e.announce(state(wgIntent(), tunnel.StateConnecting))
	e.announce(state(asusIntent(), tunnel.StateConnecting))
	e.checkTable() // nothing captures an endpoint yet

	e.announce(wgIntent())
	e.announce(asusIntent())

	e.checkTable(
		"0.0.0.0/1 dev utun10",
		"128.0.0.0/1 dev utun10",
		"192.168.1.0/24 dev utun11",
		"198.51.100.7/32 via 192.168.51.1 dev en0",
		"203.0.113.10/32 via 192.168.51.1 dev en0",
		"::/1 dev utun10",
		"8000::/1 dev utun10",
	)
	for addr, want := range map[string]string{
		"8.8.8.8":         "utun10",
		"192.168.1.5":     "utun11",
		"192.168.51.77":   "en0",
		"203.0.113.10":    "en0",
		"198.51.100.7":    "en0",
		"2001:4860::8888": "utun10",
	} {
		if got := e.lookup(addr).Iface; got != want {
			t.Errorf("%s leaves through %s, want %s", addr, got, want)
		}
	}
	if gw := e.lookup("203.0.113.10").Gateway; gw != ip("192.168.51.1") {
		t.Errorf("an endpoint must go through the physical router, got %v", gw)
	}

	wantDNS := map[string][]osnet.DNSEntry{
		"wg":   {{Servers: ips("192.168.50.1"), MatchDomains: []string{"."}, Order: 1}},
		"asus": {{Servers: ips("192.168.1.1"), MatchDomains: []string{"asus.lan"}, Order: 2}},
	}
	got := e.host.DNS.All()
	for owner, want := range wantDNS {
		if len(got[owner]) != 1 || !entryEqual(got[owner][0], want[0]) {
			t.Errorf("DNS of %s: got %+v, want %+v", owner, got[owner], want)
		}
	}
	for _, rr := range e.r.Report().Routes {
		if rr.State != tunnel.RouteInstalled {
			t.Errorf("not installed: %+v", rr)
		}
	}
	if flushes := count(dnsOps(e), "flush"); flushes == 0 {
		t.Error("the resolver caches should be flushed after DNS changed")
	}
}

func dnsOps(e *env) []string {
	var out []string
	for _, op := range e.host.DNS.Ops() {
		out = append(out, op.Kind)
	}
	return out
}

func TestPrioritiesShadowAndPromote(t *testing.T) { eachKeying(t, testPrioritiesShadowAndPromote) }

func testPrioritiesShadowAndPromote(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	e.addTunnel("utun1", "10.1.0.2/24")
	e.addTunnel("utun2", "10.2.0.2/24")
	low := up("low", 2, "utun1", tunnel.RoleSplit, "10.0.0.0/8", "172.16.0.0/12")
	high := up("high", 1, "utun2", tunnel.RoleSplit, "10.0.0.0/8")

	e.announce(low)
	e.checkTable("10.0.0.0/8 dev utun1", "172.16.0.0/12 dev utun1")
	e.announce(high)
	e.checkTable("10.0.0.0/8 dev utun2", "172.16.0.0/12 dev utun1")
	if rr := e.routeReport("10.0.0.0/8", "low"); rr.State != tunnel.RouteShadowed || rr.ShadowedBy != "high" {
		t.Errorf("low should be shadowed by high: %+v", rr)
	}
	if rr := e.routeReport("10.0.0.0/8", "high"); rr.State != tunnel.RouteInstalled {
		t.Errorf("high should hold the prefix: %+v", rr)
	}

	e.r.Withdraw("high")
	e.checkTable("10.0.0.0/8 dev utun1", "172.16.0.0/12 dev utun1")
	if rr := e.routeReport("10.0.0.0/8", "low"); rr.State != tunnel.RouteInstalled {
		t.Errorf("the loser must be promoted when the winner goes: %+v", rr)
	}

	e.announce(high)
	e.checkTable("10.0.0.0/8 dev utun2", "172.16.0.0/12 dev utun1")
	e.r.Withdraw("high")
	e.r.Withdraw("low")
	e.checkTable()
}

func TestStandbyFullTunnelTakesOverTheDefaultRoute(t *testing.T) {
	eachKeying(t, testStandbyFullTunnelTakesOverTheDefaultRoute)
}

func testStandbyFullTunnelTakesOverTheDefaultRoute(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	e.addTunnel("utun10", "10.6.0.2/24")
	e.addTunnel("utun11", "10.8.0.6/24")
	wg := up("wg", 1, "utun10", tunnel.RoleFull, "0.0.0.0/0")
	ovpn := up("ovpn", 2, "utun11", tunnel.RoleFull, "0.0.0.0/0")

	e.announce(ovpn)
	e.checkTable("0.0.0.0/1 dev utun11", "128.0.0.0/1 dev utun11")
	e.announce(wg)
	e.checkTable("0.0.0.0/1 dev utun10", "128.0.0.0/1 dev utun10")
	if rr := e.routeReport("0.0.0.0/1", "ovpn"); rr.State != tunnel.RouteShadowed || rr.Detail != "standby" || rr.ShadowedBy != "wg" {
		t.Errorf("ovpn should be on standby: %+v", rr)
	}

	e.announce(state(wg, tunnel.StateFailed)) // the main tunnel goes down
	e.checkTable("0.0.0.0/1 dev utun11", "128.0.0.0/1 dev utun11")
	e.announce(wg)
	e.checkTable("0.0.0.0/1 dev utun10", "128.0.0.0/1 dev utun10")
}

// The remote site's router is 192.168.1.1, and so is the local one: the
// tunnel's route would hijack the local network and its nameserver would be the
// router next door.
func TestLocalSubnetConflict(t *testing.T) { eachKeying(t, testLocalSubnetConflict) }

func testLocalSubnetConflict(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	e.host.MoveNetwork("en0", pfx("192.168.1.50/24"), ip("192.168.1.1"))
	e.addTunnel("utun11", "10.8.0.6/24")

	e.announce(asusIntent())

	e.checkTable() // nothing was installed
	if rt, ok := e.host.Routes.Get(pfx("192.168.1.0/24")); !ok || rt.Iface != "en0" {
		t.Errorf("the local subnet's own route must be left alone: %+v %v", rt, ok)
	}
	if rr := e.routeReport("192.168.1.0/24", "asus"); rr.State != tunnel.RouteBlocked || rr.Detail != "local subnet 192.168.1.0/24" {
		t.Errorf("report: %+v", rr)
	}
	if dr := e.dnsReport("asus"); dr.State != tunnel.RouteBlocked {
		t.Errorf("a nameserver that is the local router must not be installed: %+v", dr)
	}
	if owned, _ := e.host.DNS.Owned(); len(owned) != 0 {
		t.Errorf("resolver entries were applied: %v", owned)
	}
	if ops := e.opLines(); slices.ContainsFunc(ops, func(l string) bool { return len(l) > 6 && l[:6] == "delete" }) {
		t.Errorf("nothing may be deleted: %v", ops)
	}

	// The local network changes to another subnet: the route and the DNS entry follow.
	e.host.MoveNetwork("en0", pfx("192.168.51.185/24"), ip("192.168.51.1"))
	e.change(osnet.ChangeRoute)
	e.checkTable("192.168.1.0/24 dev utun11")
	if got := e.host.DNS.Entries("asus"); len(got) != 1 {
		t.Errorf("DNS after the conflict is gone: %+v", got)
	}
}

func TestReconnectingTunnelKeepsItsRoutes(t *testing.T) {
	eachKeying(t, testReconnectingTunnelKeepsItsRoutes)
}

func testReconnectingTunnelKeepsItsRoutes(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	e.bothTunnels()
	e.announce(wgIntent())
	before := e.opLines()
	dnsBefore := dnsOps(e)

	e.announce(state(wgIntent(), tunnel.StateReconnecting))

	if after := e.opLines(); !slices.Equal(before, after) {
		t.Errorf("reconnecting changed the routes: %v", after[len(before):])
	}
	if !slices.Equal(dnsBefore, dnsOps(e)) {
		t.Error("reconnecting changed the DNS entries")
	}
}

func TestTunnelInterfaceVanishesAndComesBack(t *testing.T) {
	eachKeying(t, testTunnelInterfaceVanishesAndComesBack)
}

func testTunnelInterfaceVanishesAndComesBack(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	e.addTunnel("utun11", "10.8.0.6/24")
	e.announce(asusIntent())
	e.checkTable("192.168.1.0/24 dev utun11")

	// The kernel removes the routes with the interface; nothing is stale.
	e.host.DestroyInterface("utun11")
	e.change(osnet.ChangeRoute)
	e.checkTable()
	if rr := e.routeReport("192.168.1.0/24", "asus"); rr.State != tunnel.RoutePending {
		t.Errorf("the route should wait for its interface: %+v", rr)
	}
	if got := e.r.Report().Stale; len(got) != 0 {
		t.Errorf("stale: %+v", got)
	}

	e.addTunnel("utun11", "10.8.0.6/24")
	e.checkTable("192.168.1.0/24 dev utun11")
	if rr := e.routeReport("192.168.1.0/24", "asus"); rr.State != tunnel.RouteInstalled {
		t.Errorf("report: %+v", rr)
	}
}

// Applying the same intents twice changes nothing: not the table, not the DNS
// entries, not the journal.
func TestReapplyingChangesNothing(t *testing.T) { eachKeying(t, testReapplyingChangesNothing) }

func testReapplyingChangesNothing(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	e.bothTunnels()
	e.announce(wgIntent())
	e.announce(asusIntent())
	opsBefore, dnsBefore := e.opLines(), dnsOps(e)
	journalBefore := len(e.journalFile())
	tableBefore := e.table()
	reportBefore := e.r.Report()

	for range 3 {
		e.announce(asusIntent())
		e.change(osnet.ChangeHeartbeat)
		e.change(osnet.ChangeManual)
	}

	if after := e.opLines(); !slices.Equal(after, opsBefore) {
		t.Errorf("route operations on an unchanged network: %v", after[len(opsBefore):])
	}
	if after := dnsOps(e); !slices.Equal(after, dnsBefore) {
		t.Errorf("DNS operations on an unchanged network: %v", after[len(dnsBefore):])
	}
	if n := len(e.journalFile()); n != journalBefore {
		t.Errorf("the journal grew from %d to %d records", journalBefore, n)
	}
	checkLines(t, "table", e.table(), tableBefore)
	reportAfter := e.r.Report()
	if !slices.Equal(reportBefore.Routes, reportAfter.Routes) {
		t.Errorf("report changed:\n%+v\n%+v", reportBefore.Routes, reportAfter.Routes)
	}
}

// What was installed is removed in the reverse order: DNS, default halves,
// tunnel routes, bypass routes; and built in the opposite order.
func TestApplyAndTeardownOrder(t *testing.T) { eachKeying(t, testApplyAndTeardownOrder) }

func testApplyAndTeardownOrder(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	e.bothTunnels()
	// The DNS fake has its own log; mark the route log so both are in one sequence.
	e.r.dns = markingDNS{e.host.DNS, e.host.Routes}

	e.announce(wgIntent())
	want := []string{
		"add 203.0.113.10/32 via 192.168.51.1", // bypass
		"add 0.0.0.0/1",                        // default halves
		"add 128.0.0.0/1",
		"add ::/1",
		"add 8000::/1",
		"mark dns apply wg",
	}
	if got := e.opLines()[len(e.opLines())-len(want):]; !slices.Equal(got, want) {
		t.Errorf("apply order:\n got %v\nwant %v", got, want)
	}

	start := len(e.opLines())
	e.r.Withdraw("wg")
	wantDown := []string{
		"mark dns remove wg",
		"delete 8000::/1", // default halves, last applied first
		"delete ::/1",
		"delete 128.0.0.0/1",
		"delete 0.0.0.0/1",
		"delete 203.0.113.10/32", // bypass last
	}
	if got := e.opLines()[start:]; !slices.Equal(got, wantDown) {
		t.Errorf("teardown order:\n got %v\nwant %v", got, wantDown)
	}
}

type markingDNS struct {
	osnet.DNSConfigurator
	routes *fake.RouteTable
}

func (m markingDNS) Apply(owner string, entries []osnet.DNSEntry) error {
	m.routes.Mark("dns apply " + owner)
	return m.DNSConfigurator.Apply(owner, entries)
}

func (m markingDNS) Remove(owner string) error {
	m.routes.Mark("dns remove " + owner)
	return m.DNSConfigurator.Remove(owner)
}

// ACCEPTANCE TEST NUMBER ONE (research 6.5), the owner's real bug. An OpenVPN
// tunnel is up with a bypass /32 through the router G1. The network changes to
// another router G2 and the old /32 is left behind in the table, pointing at a
// router that is not there. The Reconciler must notice, delete the journaled
// stale route, install the new one through the new best default, and only then
// ask the engines to rebind.
func TestStaleBypassIsRepairedBeforeRebind(t *testing.T) {
	eachKeying(t, testStaleBypassIsRepairedBeforeRebind)
}

func testStaleBypassIsRepairedBeforeRebind(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	e.host.AddTunnel("utun11", pfx("10.8.0.6/24"))
	var rebinds atomic.Int32
	e.r.SetRebind(func() {
		e.host.Routes.Mark("rebind")
		rebinds.Add(1)
	})
	e.run()

	ovpn := endpoints(up("ovpn", 1, "utun11", tunnel.RoleFull, "0.0.0.0/0"), "198.51.100.88")
	e.announce(state(ovpn, tunnel.StateConnecting))
	e.announce(ovpn)
	e.checkTable(
		"0.0.0.0/1 dev utun11",
		"128.0.0.0/1 dev utun11",
		"198.51.100.88/32 via 192.168.51.1 dev en0",
	)

	// Another network: the interface stays up, only its address and router change.
	e.host.MoveNetwork("en0", pfx("10.20.30.7/24"), ip("10.20.30.1"))
	if stale, _ := e.host.Routes.Get(pfx("198.51.100.88/32")); stale.Gateway != ip("192.168.51.1") {
		t.Fatalf("the fake should have left the old /32 behind, got %+v", stale)
	}
	e.host.Net.Emit(osnet.Change{Reason: osnet.ChangeRoute})
	e.eventually("the rebind", func() bool { return rebinds.Load() == 1 })

	ops := e.opLines()
	del := slices.Index(ops, "delete 198.51.100.88/32")
	add := slices.Index(ops, "add 198.51.100.88/32 via 10.20.30.1")
	mark := slices.Index(ops, "mark rebind")
	if del < 0 || add < 0 || mark < 0 || !(del < add && add < mark) {
		t.Errorf("want delete the stale route, add the new one, then rebind; got %v", ops)
	}
	e.checkTable(
		"0.0.0.0/1 dev utun11",
		"128.0.0.0/1 dev utun11",
		"198.51.100.88/32 via 10.20.30.1 dev en0",
	)
	if got := e.lookup("198.51.100.88").Gateway; got != ip("10.20.30.1") {
		t.Errorf("traffic to the endpoint goes through %v", got)
	}
	if n := count(ops, "add 0.0.0.0/1"); n != 1 {
		t.Errorf("the tunnel's own routes must not be touched, 0.0.0.0/1 was added %d times", n)
	}
	if stale := e.r.Report().Stale; len(stale) != 0 {
		t.Errorf("stale routes left: %+v", stale)
	}
	wantJournal := []string{
		"pending 192.168.51.1", "applied 192.168.51.1", "removed 192.168.51.1",
		"pending 10.20.30.1", "applied 10.20.30.1",
	}
	if got := e.journalFor(kindRoute, "198.51.100.88/32"); !slices.Equal(got, wantJournal) {
		t.Errorf("journal:\n got %v\nwant %v", got, wantJournal)
	}

	e.host.Net.Emit(osnet.Change{Reason: osnet.ChangeHeartbeat})
	time.Sleep(30 * time.Millisecond)
	if n := rebinds.Load(); n != 1 {
		t.Errorf("rebind called %d times, want once", n)
	}
}

// The same bug when the leftover /32 was not added by Plaitway: it is reported,
// never deleted without the user's say-so, and installing ours waits for that.
func TestForeignStaleRouteIsReportedNotRemoved(t *testing.T) {
	eachKeying(t, testForeignStaleRouteIsReportedNotRemoved)
}

func testForeignStaleRouteIsReportedNotRemoved(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	e.host.AddTunnel("utun11", pfx("10.8.0.6/24"))
	// What an earlier OpenVPN Connect left behind, then the network changed.
	e.host.Inject(osnet.Route{Dst: pfx("198.51.100.88/32"), Gateway: ip("192.168.51.1"), Iface: "en0", Static: true})
	e.host.MoveNetwork("en0", pfx("10.20.30.7/24"), ip("10.20.30.1"))
	e.change(osnet.ChangeRoute)
	foreignOK := osnet.Route{Dst: pfx("8.8.4.4/32"), Gateway: ip("10.20.30.1"), Iface: "en0", Static: true}
	e.host.Inject(foreignOK)

	ovpn := endpoints(up("ovpn", 1, "utun11", tunnel.RoleFull, "0.0.0.0/0"), "198.51.100.88")
	e.announce(ovpn)

	if ops := e.opLines(); slices.Contains(ops, "delete 198.51.100.88/32") {
		t.Fatalf("a route we did not install was deleted: %v", ops)
	}
	stale := e.r.Report().Stale
	if len(stale) != 1 || dstOf(stale[0].Key) != "198.51.100.88/32" || stale[0].Owned || stale[0].Reason != "gateway is not on the subnet of en0" {
		t.Fatalf("stale: %+v", stale)
	}
	rr := e.routeReport("198.51.100.88/32", "ovpn")
	if k.windows() {
		// The foreign route goes through another next hop: another key, and no
		// conflict. Both routes are in the table until the user removes theirs, and
		// the foreign one has the lower metric.
		want := fmt.Sprintf("overridden by 198.51.100.88/32 via 192.168.51.1 (interface %d): effective metric 25, ours 26", e.host.ifIndex("en0"))
		if rr.State != tunnel.RouteFailed || rr.Detail != want {
			t.Errorf("the bypass is a route of its own next to the foreign one: %+v, want %q", rr, want)
		}
	} else if rr.State != tunnel.RouteFailed || rr.Detail != "held by another program via 192.168.51.1" {
		t.Errorf("the bypass cannot be installed over the foreign route: %+v", rr)
	}

	if err := e.removeStale("8.8.4.4/32"); err == nil {
		t.Error("a route that is not stale must not be removed")
	}
	if _, ok := e.host.Routes.Get(foreignOK.Dst); !ok {
		t.Error("the healthy foreign route was removed")
	}
	if err := e.removeStale("198.51.100.88/32"); err != nil {
		t.Fatal(err)
	}
	e.checkTable(
		"0.0.0.0/1 dev utun11",
		"8.8.4.4/32 via 10.20.30.1 dev en0",
		"128.0.0.0/1 dev utun11",
		"198.51.100.88/32 via 10.20.30.1 dev en0",
	)
	if stale := e.r.Report().Stale; len(stale) != 0 {
		t.Errorf("stale after removal: %+v", stale)
	}
	if err := e.removeStale("198.51.100.88/32"); err == nil {
		t.Error("removing it twice should say that it is not stale")
	}
}

// A foreign host route for a known endpoint whose gateway is still on the
// subnet but is not the default route's nexthop is stale too.
func TestForeignEndpointRouteThroughAnotherRouterIsStale(t *testing.T) {
	eachKeying(t, testForeignEndpointRouteThroughAnotherRouterIsStale)
}

func testForeignEndpointRouteThroughAnotherRouterIsStale(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	e.host.AddTunnel("utun11", pfx("10.8.0.6/24"))
	e.host.Inject(osnet.Route{Dst: pfx("198.51.100.88/32"), Gateway: ip("192.168.51.254"), Iface: "en0", Static: true})
	e.host.Inject(osnet.Route{Dst: pfx("8.8.4.4/32"), Gateway: ip("192.168.51.254"), Iface: "en0", Static: true})

	e.announce(endpoints(up("ovpn", 1, "utun11", tunnel.RoleFull, "0.0.0.0/0"), "198.51.100.88"))

	stale := e.r.Report().Stale
	if len(stale) != 1 || dstOf(stale[0].Key) != "198.51.100.88/32" || stale[0].Reason != "gateway is not the default route's" {
		t.Errorf("only the endpoint's route is held to the default nexthop: %+v", stale)
	}
}

func TestRebindRules(t *testing.T) { eachKeying(t, testRebindRules) }

func testRebindRules(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	var rebinds atomic.Int32
	e.r.SetRebind(func() { rebinds.Add(1) })
	e.addTunnel("utun11", "10.8.0.6/24")

	// No tunnel is up: nothing to rebind.
	e.host.MoveNetwork("en0", pfx("10.20.30.7/24"), ip("10.20.30.1"))
	e.change(osnet.ChangeRoute)
	if n := rebinds.Load(); n != 0 {
		t.Fatalf("rebind with no tunnel up: %d", n)
	}

	e.announce(endpoints(up("ovpn", 1, "utun11", tunnel.RoleFull, "0.0.0.0/0"), "198.51.100.88"))

	// A route event that changes nothing about the physical network.
	e.change(osnet.ChangeRoute)
	e.change(osnet.ChangeHeartbeat)
	if n := rebinds.Load(); n != 0 {
		t.Fatalf("rebind without a change of the underlay: %d", n)
	}

	// A tunnel interface appearing is not a change of the underlay either.
	e.addTunnel("utun12", "10.9.0.2/24")
	if n := rebinds.Load(); n != 0 {
		t.Fatalf("rebind for a new tunnel interface: %d", n)
	}

	e.host.MoveNetwork("en0", pfx("192.168.51.185/24"), ip("192.168.51.1"))
	e.change(osnet.ChangeRoute)
	if n := rebinds.Load(); n != 1 {
		t.Fatalf("rebind after the network changed: %d", n)
	}

	// The network goes away: nothing to rebind to until it is back.
	e.host.Routes.SetUp("en0", false)
	e.host.Sync()
	e.change(osnet.ChangeRoute)
	if n := rebinds.Load(); n != 1 {
		t.Fatalf("rebind with no network: %d", n)
	}
	if rr := e.routeReport("198.51.100.88/32", "ovpn"); rr.State != tunnel.RoutePending || rr.Detail != "no network" {
		t.Errorf("the bypass should wait for a network: %+v", rr)
	}
	e.host.Routes.SetUp("en0", true)
	e.host.MoveNetwork("en0", pfx("192.168.51.185/24"), ip("192.168.51.1"))
	e.change(osnet.ChangeRoute)
	if n := rebinds.Load(); n != 2 {
		t.Fatalf("rebind when the network is back: %d", n)
	}
	e.checkTable(
		"0.0.0.0/1 dev utun11",
		"128.0.0.0/1 dev utun11",
		"198.51.100.88/32 via 192.168.51.1 dev en0",
	)
}

// Every rebind makes every tunnel reconnect, so only what the tunnels' own
// traffic travels over may cause one: the default routes, the interface they
// leave through and its addresses. AirDrop, the bridges of virtual machines, a
// second interface that carries no default route, the link-local and privacy
// addresses that come and go: none of it is the underlay.
func TestRebindIgnoresWhatIsNotTheUnderlay(t *testing.T) {
	eachKeying(t, testRebindIgnoresWhatIsNotTheUnderlay)
}

func testRebindIgnoresWhatIsNotTheUnderlay(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	var rebinds atomic.Int32
	e.r.SetRebind(func() { rebinds.Add(1) })
	e.addTunnel("utun11", "10.8.0.6/24")
	e.announce(endpoints(up("ovpn", 1, "utun11", tunnel.RoleFull, "0.0.0.0/0"), "198.51.100.88"))

	// en0 gets its first global IPv6 address: that is a change of the network.
	e.host.Routes.SetAddrs("en0", pfxs("192.168.51.185/24", "fe80::aa:1d64:6acb:1ea/64", "2001:db8::1234/64"))
	e.host.Sync()
	e.change(osnet.ChangeRoute)
	if n := rebinds.Load(); n != 1 {
		t.Fatalf("setup: %d rebinds", n)
	}

	harmless := []struct {
		what string
		do   func()
	}{
		{"AirDrop starts", func() {
			e.host.AddInterface(osnet.Interface{Name: "awdl0", Up: true, Addrs: pfxs("fe80::a09b:b3ff:fe59:c744/64")})
		}},
		{"AirDrop stops", func() { e.host.Routes.SetUp("awdl0", false) }},
		{"a virtual machine starts", func() {
			e.host.AddInterface(osnet.Interface{Name: "bridge100", Up: true, Addrs: pfxs("192.168.139.3/23", "fe80::f84d:89ff:fe36:9b64/64")})
		}},
		{"a virtual machine stops", func() { e.host.DestroyInterface("bridge100") }},
		{"a second interface joins a network without taking the default route", func() {
			e.host.AddInterface(osnet.Interface{Name: "en5", Up: true, Addrs: pfxs("10.77.0.2/24")})
		}},
		{"the privacy address of en0 rotates", func() {
			e.host.Routes.SetAddrs("en0", pfxs("192.168.51.185/24", "fe80::aa:1d64:6acb:1ea/64", "2001:db8::5678/64"))
		}},
		{"the link-local address of en0 changes", func() {
			e.host.Routes.SetAddrs("en0", pfxs("192.168.51.185/24", "fe80::1:2:3:4/64", "2001:db8::5678/64"))
		}},
	}
	for _, h := range harmless {
		h.do()
		e.host.Sync()
		e.change(osnet.ChangeRoute)
		e.change(osnet.ChangeHeartbeat)
		if n := rebinds.Load(); n != 1 {
			t.Fatalf("%s: %d rebinds", h.what, n)
		}
	}

	e.host.MoveNetwork("en0", pfx("10.20.30.7/24"), ip("10.20.30.1"))
	e.change(osnet.ChangeRoute)
	if n := rebinds.Load(); n != 2 {
		t.Errorf("the network moved: %d rebinds", n)
	}
}

// The monitor sends no event for a tunnel interface, so the Reconciler learns
// of the one an engine has just created only by looking. A nameserver on the
// tunnel's own subnet is reachable through it, and must not wait for the
// heartbeat to be installed.
func TestAnnounceSeesTheTunnelInterfaceItJustGotCreated(t *testing.T) {
	eachKeying(t, testAnnounceSeesTheTunnelInterfaceItJustGotCreated)
}

func testAnnounceSeesTheTunnelInterfaceItJustGotCreated(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	e.host.AddTunnel("utun11", pfx("10.8.0.6/24")) // no change(): nobody says so

	e.announce(nameserver(up("ovpn", 1, "utun11", tunnel.RoleSplit, "192.168.1.0/24"), "10.8.0.1", "corp.example"))

	if dr := e.dnsReport("ovpn"); dr.State != tunnel.RouteInstalled {
		t.Errorf("report: %+v", dr)
	}
	if got := e.host.DNS.Entries("ovpn"); len(got) != 1 {
		t.Errorf("the resolver entry was not written: %v", got)
	}
}

// After a wake DHCP often finishes late and no event announces it; a second
// pass a few seconds later finds the new network.
func TestWakeSchedulesASecondPass(t *testing.T) { eachKeying(t, testWakeSchedulesASecondPass) }

func testWakeSchedulesASecondPass(t *testing.T, k keyingCase) {
	e := newEnv(t, k, func(c *Config) { c.WakeDelay = 150 * time.Millisecond })
	var rebinds atomic.Int32
	e.r.SetRebind(func() { rebinds.Add(1) })
	e.host.AddTunnel("utun11", pfx("10.8.0.6/24"))
	e.run()
	e.announce(endpoints(up("ovpn", 1, "utun11", tunnel.RoleFull, "0.0.0.0/0"), "198.51.100.88"))

	e.host.Net.Emit(osnet.Change{Reason: osnet.ChangeWake})
	e.eventually("the wake to be handled", func() bool { return e.r.Report().LastChange.Reason == osnet.ChangeWake })
	// The lease arrives after the wake event was handled; nobody says so.
	e.host.MoveNetwork("en0", pfx("10.20.30.7/24"), ip("10.20.30.1"))

	e.eventually("the second pass to repair the route", func() bool {
		return slices.Contains(e.table(), "198.51.100.88/32 via 10.20.30.1 dev en0")
	})
	e.eventually("the rebind", func() bool { return rebinds.Load() == 1 })
	if rep := e.r.Report(); rep.LastChange.Reason != osnet.ChangeWake {
		t.Errorf("the second pass is ours, not a network change: %+v", rep.LastChange)
	}
}

// Route events that changed nothing the Reconciler looks at must be cheap, but a
// heartbeat looks for drift: a route removed behind our back comes back.
func TestHeartbeatRepairsDrift(t *testing.T) { eachKeying(t, testHeartbeatRepairsDrift) }

func testHeartbeatRepairsDrift(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	e.bothTunnels()
	e.run()
	e.announce(asusIntent())
	e.checkTable("192.168.1.0/24 dev utun11")

	e.host.Routes.Remove(pfx("192.168.1.0/24"))
	e.host.Net.Emit(osnet.Change{Reason: osnet.ChangeRoute})
	if k.windows() {
		// The routes of other programs decide whether ours are in use, so every route
		// event is looked at (markOverridden), and the repair comes with it.
		e.eventually("the route event to repair the route", func() bool {
			return slices.Equal(e.table(), []string{"192.168.1.0/24 dev utun11"})
		})
		return
	}
	time.Sleep(30 * time.Millisecond)
	e.checkTable() // a route event alone does not re-read the table

	e.host.Net.Emit(osnet.Change{Reason: osnet.ChangeHeartbeat})
	e.eventually("the heartbeat to repair the route", func() bool {
		return slices.Equal(e.table(), []string{"192.168.1.0/24 dev utun11"})
	})
}

func TestRunRemovesEverythingOnExit(t *testing.T) { eachKeying(t, testRunRemovesEverythingOnExit) }

func testRunRemovesEverythingOnExit(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	e.bothTunnels()
	foreign := osnet.Route{Dst: pfx("8.8.4.4/32"), Gateway: ip("192.168.51.1"), Iface: "en0", Static: true}
	e.host.Inject(foreign)
	stop := e.run()
	e.announce(wgIntent())
	e.announce(asusIntent())
	if len(e.table()) < 6 || len(e.host.DNS.All()) != 2 {
		t.Fatalf("setup: %v %v", e.table(), e.host.DNS.All())
	}

	if err := stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	e.checkTable("8.8.4.4/32 via 192.168.51.1 dev en0")
	if owned, _ := e.host.DNS.Owned(); len(owned) != 0 {
		t.Errorf("resolver entries left: %v", owned)
	}
	if left := e.unresolved(); len(left) != 0 {
		t.Errorf("journal still lists %+v", left)
	}
	if err := e.r.Announce(wgIntent()); err == nil {
		t.Error("Announce after Run ended should fail")
	}
	e.r.Withdraw("wg") // must not panic or block
	if err := e.r.Resync(); err == nil {
		t.Error("Resync after Run ended should fail")
	}
}

func TestResyncRebuildsFromScratch(t *testing.T) { eachKeying(t, testResyncRebuildsFromScratch) }

func testResyncRebuildsFromScratch(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	e.bothTunnels()
	e.announce(wgIntent())
	e.announce(asusIntent())
	tableBefore := e.table()
	start := len(e.opLines())
	dnsStart := len(dnsOps(e))

	if err := e.r.Resync(); err != nil {
		t.Fatal(err)
	}

	checkLines(t, "table after Resync", e.table(), tableBefore)
	ops := e.opLines()[start:]
	adds, deletes := 0, 0
	for _, op := range ops {
		switch op[:3] {
		case "add":
			adds++
		case "del":
			deletes++
		}
	}
	if adds != len(tableBefore) || deletes != len(tableBefore) {
		t.Errorf("Resync should delete and add all %d routes, got %d deletes, %d adds: %v", len(tableBefore), deletes, adds, ops)
	}
	firstAdd, lastDelete := -1, -1
	for i, op := range ops {
		switch {
		case op[:3] == "add" && firstAdd < 0:
			firstAdd = i
		case op[:3] == "del":
			lastDelete = i
		}
	}
	if lastDelete > firstAdd {
		t.Errorf("everything is removed before anything is added again: %v", ops)
	}
	dns := dnsOps(e)[dnsStart:]
	if count(dns, "remove") != 2 || count(dns, "apply") != 2 {
		t.Errorf("DNS should be removed and applied again for both owners: %v", dns)
	}
	if got := e.host.DNS.All(); len(got) != 2 {
		t.Errorf("DNS after Resync: %v", got)
	}
}

// A pass that keeps failing is answered with a reset: everything is removed
// and rebuilt, not just the part that failed.
func TestNonConvergenceTriggersReset(t *testing.T) { eachKeying(t, testNonConvergenceTriggersReset) }

func testNonConvergenceTriggersReset(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	e.addTunnel("utun1", "10.1.0.2/24")
	e.addTunnel("utun2", "10.2.0.2/24")
	e.announce(up("a", 1, "utun1", tunnel.RoleSplit, "10.1.0.0/16"))
	e.host.Routes.InjectFault(fake.Fault{Op: fake.OpAdd, Dst: pfx("10.2.0.0/16"), Err: errBoom, Times: 3})

	e.announce(up("b", 2, "utun2", tunnel.RoleSplit, "10.2.0.0/16")) // fails: pass 1
	e.checkTable("10.1.0.0/16 dev utun1")
	if rr := e.routeReport("10.2.0.0/16", "b"); rr.State != tunnel.RouteFailed {
		t.Errorf("report: %+v", rr)
	}
	e.change(osnet.ChangeHeartbeat) // pass 2
	if n := count(e.opLines(), "delete 10.1.0.0/16"); n != 0 {
		t.Fatalf("a failing route does not justify a reset yet: %d deletes", n)
	}
	e.change(osnet.ChangeHeartbeat) // pass 3: reset

	e.checkTable("10.1.0.0/16 dev utun1", "10.2.0.0/16 dev utun2")
	ops := e.opLines()
	if n := count(ops, "delete 10.1.0.0/16"); n != 1 {
		t.Errorf("the reset removes what was fine too: %d deletes in %v", n, ops)
	}
	if n := count(ops, "add 10.1.0.0/16"); n != 2 {
		t.Errorf("and builds it again: %d adds", n)
	}
	if rr := e.routeReport("10.2.0.0/16", "b"); rr.State != tunnel.RouteInstalled {
		t.Errorf("report after the reset: %+v", rr)
	}
}

// Waiting for the network is not a failure and must never trigger a reset.
func TestUnreachableDoesNotTriggerReset(t *testing.T) {
	eachKeying(t, testUnreachableDoesNotTriggerReset)
}

func testUnreachableDoesNotTriggerReset(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	e.addTunnel("utun1", "10.1.0.2/24")
	e.addTunnel("utun2", "10.2.0.2/24")
	e.announce(up("a", 1, "utun1", tunnel.RoleSplit, "10.1.0.0/16"))
	e.host.Routes.InjectFault(fake.Fault{Op: fake.OpAdd, Dst: pfx("10.2.0.0/16"), Err: osnet.ErrUnreachable, Times: 5})

	e.announce(up("b", 2, "utun2", tunnel.RoleSplit, "10.2.0.0/16"))
	for range 4 {
		e.change(osnet.ChangeHeartbeat)
	}

	if n := count(e.opLines(), "delete 10.1.0.0/16"); n != 0 {
		t.Errorf("a reset happened while only waiting: %v", e.opLines())
	}
	e.change(osnet.ChangeHeartbeat)
	e.checkTable("10.1.0.0/16 dev utun1", "10.2.0.0/16 dev utun2")
}

func TestChangedIsCoalesced(t *testing.T) { eachKeying(t, testChangedIsCoalesced) }

func testChangedIsCoalesced(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	e.addTunnel("utun1", "10.1.0.2/24")
	drain := func() int {
		n := 0
		for {
			select {
			case <-e.r.Changed():
				n++
			default:
				return n
			}
		}
	}
	drain()
	e.announce(up("a", 1, "utun1", tunnel.RoleSplit, "10.1.0.0/16"))
	e.announce(up("a", 1, "utun1", tunnel.RoleSplit, "10.1.0.0/16", "10.3.0.0/16"))
	e.r.Withdraw("a")
	if n := drain(); n != 1 {
		t.Errorf("three changes should leave one signal, got %d", n)
	}
	if n := drain(); n != 0 {
		t.Errorf("signals after draining: %d", n)
	}
	e.announce(up("a", 1, "utun1", tunnel.RoleSplit, "10.1.0.0/16"))
	if n := drain(); n != 1 {
		t.Errorf("a new change must signal again, got %d", n)
	}
}

// The owner and the interface name end up in resolver keys and commands, so
// they are checked. Routes, endpoints and domains are data from servers: what
// cannot be used is left out by Compute, see TestUnusableDataDoesNotBreakATunnel.
func TestAnnounceRejectsUnsafeIdentifiers(t *testing.T) {
	eachKeying(t, testAnnounceRejectsUnsafeIdentifiers)
}

func testAnnounceRejectsUnsafeIdentifiers(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	e.addTunnel("utun1", "10.1.0.2/24")
	base := func() tunnel.Intent { return up("a", 1, "utun1", tunnel.RoleSplit, "10.1.0.0/16") }
	tests := []struct {
		name   string
		mutate func(*tunnel.Intent)
	}{
		{"owner with a path in it", func(in *tunnel.Intent) { in.Owner = "../evil" }},
		{"owner that looks like an option", func(in *tunnel.Intent) { in.Owner = "-interface" }},
		{"interface that looks like an option", func(in *tunnel.Intent) { in.Iface = "-ifscope" }},
		{"empty owner", func(in *tunnel.Intent) { in.Owner = "" }},
		{"interface name with a space", func(in *tunnel.Intent) { in.Iface = "utun1 -interface en0" }},
		{"no interface", func(in *tunnel.Intent) { in.Iface = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := base()
			tt.mutate(&in)
			if err := e.r.Announce(in); err == nil {
				t.Error("accepted")
			}
			e.checkTable()
			if len(e.r.Report().Routes) != 0 {
				t.Errorf("a rejected intent left state behind: %+v", e.r.Report().Routes)
			}
		})
	}
}

// A profile may point at a proxy on localhost, a server may push a bad route or
// a domain that is not one. None of that may break the announcement or reach the
// host.
func TestUnusableDataDoesNotBreakATunnel(t *testing.T) {
	eachKeying(t, testUnusableDataDoesNotBreakATunnel)
}

func testUnusableDataDoesNotBreakATunnel(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	e.addTunnel("utun10", "10.6.0.2/24")
	in := up("wg", 1, "utun10", tunnel.RoleFull, "0.0.0.0/0", "127.0.0.1/32", "224.0.0.0/4", "10.7.0.0/16")
	in.Endpoints = ips("127.0.0.1", "0.0.0.0", "169.254.1.1", "203.0.113.10")
	in.DNS = []tunnel.DNSIntent{{
		Servers:      ips("192.168.50.1"),
		MatchDomains: []string{"x.lan\nd.add ServerAddresses * 6.6.6.6", "a..lan", "good.lan"},
	}}

	e.announce(in)

	e.checkTable(
		"0.0.0.0/1 dev utun10",
		"10.7.0.0/16 dev utun10",
		"128.0.0.0/1 dev utun10",
		"203.0.113.10/32 via 192.168.51.1 dev en0",
	)
	if rr := e.routeReport("127.0.0.1/32", "wg"); rr.State != tunnel.RouteBlocked || rr.Detail != "special address range" {
		t.Errorf("report: %+v", rr)
	}
	got := e.host.DNS.Entries("wg")
	if len(got) != 1 || !slices.Equal(got[0].MatchDomains, []string{"good.lan"}) {
		t.Errorf("only the usable domain may be applied: %+v", got)
	}
}

func TestAnnounceKeepsItsOwnCopy(t *testing.T) { eachKeying(t, testAnnounceKeepsItsOwnCopy) }

func testAnnounceKeepsItsOwnCopy(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	e.addTunnel("utun1", "10.1.0.2/24")
	in := nameserver(up("a", 1, "utun1", tunnel.RoleSplit, "10.1.0.0/16"), "10.1.0.53", "a.lan")
	e.announce(in)

	in.Routes[0] = pfx("172.16.0.0/12") // the engine reuses its slices
	in.DNS[0].MatchDomains[0] = "evil.lan"
	e.change(osnet.ChangeHeartbeat)

	e.checkTable("10.1.0.0/16 dev utun1")
	if got := e.host.DNS.Entries("a"); got[0].MatchDomains[0] != "a.lan" {
		t.Errorf("DNS: %+v", got)
	}
}

// A change of the network or a wake may have disturbed the resolver entries, so
// they are written again and the caches flushed; an event that changed nothing
// does not touch them.
func TestDNSIsWrittenAgainAfterANetworkChangeOrWake(t *testing.T) {
	eachKeying(t, testDNSIsWrittenAgainAfterANetworkChangeOrWake)
}

func testDNSIsWrittenAgainAfterANetworkChangeOrWake(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	e.bothTunnels()
	e.announce(asusIntent())
	applies := func() int { return count(dnsOps(e), "apply") }
	flushes := func() int { return count(dnsOps(e), "flush") }
	baseApplies, baseFlushes := applies(), flushes()

	e.change(osnet.ChangeRoute)
	e.change(osnet.ChangeHeartbeat)
	if applies() != baseApplies || flushes() != baseFlushes {
		t.Fatalf("an unchanged network rewrote the DNS entries: %v", dnsOps(e))
	}

	e.host.MoveNetwork("en0", pfx("10.20.30.7/24"), ip("10.20.30.1"))
	e.change(osnet.ChangeRoute)
	if applies() != baseApplies+1 || flushes() != baseFlushes+1 {
		t.Errorf("after a network change: %d applies, %d flushes: %v", applies(), flushes(), dnsOps(e))
	}

	e.change(osnet.ChangeWake)
	if applies() != baseApplies+2 || flushes() != baseFlushes+2 {
		t.Errorf("after a wake: %d applies, %d flushes: %v", applies(), flushes(), dnsOps(e))
	}
	// Writing the same entries again is not a new journal entry.
	if got := e.journalFor(kindResolver, "asus"); !slices.Equal(got, []string{"pending", "applied"}) {
		t.Errorf("journal: %v", got)
	}
}

// Seen on hardware: Wi-Fi (en0, the best default) and Ethernet (en6) are on the
// same router's network, and the kernel binds the route to the endpoint to en6
// although it was added for en0. The route is ours all the same: it has to be
// reported as installed and still be removed and replaced when the router
// changes, otherwise it would stay behind as a stale route.
func TestBypassRouteStaysOursWhenTheKernelPicksAnotherInterface(t *testing.T) {
	eachKeying(t, testBypassRouteStaysOursWhenTheKernelPicksAnotherInterface)
}

func testBypassRouteStaysOursWhenTheKernelPicksAnotherInterface(t *testing.T, k keyingCase) {
	if k.windows() {
		t.Skip("a Windows route is bound to the interface it names; TestWindowsBypassFollowsTheDefaultRouteToAnotherInterface covers the move")
	}
	e := newEnv(t, k)
	// Ethernet joins the router's network next to Wi-Fi; the system keeps one default route.
	e.host.AddInterface(osnet.Interface{Name: "en6", Up: true, Addrs: pfxs("192.168.51.136/24")})
	e.host.Sync()
	e.host.Routes.KernelPicksInterface("en6")
	e.host.AddTunnel("utun11", pfx("10.8.0.6/24"))
	e.run()

	ovpn := endpoints(up("ovpn", 1, "utun11", tunnel.RoleFull, "0.0.0.0/0"), "198.51.100.88")
	e.announce(state(ovpn, tunnel.StateConnecting))
	e.announce(ovpn)

	if got, _ := e.host.Routes.Get(pfx("198.51.100.88/32")); got.Iface != "en6" {
		t.Fatalf("the fake kernel should have bound the route to en6, got %+v", got)
	}
	if rr := e.routeReport("198.51.100.88/32", "ovpn"); rr.State != tunnel.RouteInstalled {
		t.Fatalf("report: %+v, want installed", rr)
	}
	if got := e.journalFor(kindRoute, "198.51.100.88/32"); !slices.Equal(got, []string{"pending 192.168.51.1", "applied 192.168.51.1"}) {
		t.Errorf("journal: %v", got)
	}

	// The Ethernet cable is pulled and Wi-Fi joins another router: the route is
	// ours, so it is replaced rather than left behind.
	e.host.DestroyInterface("en6")
	e.host.Routes.KernelPicksInterface("")
	e.host.MoveNetwork("en0", pfx("10.20.30.7/24"), ip("10.20.30.1"))
	e.host.Net.Emit(osnet.Change{Reason: osnet.ChangeRoute})
	e.eventually("the route through the new router", func() bool {
		got, ok := e.host.Routes.Get(pfx("198.51.100.88/32"))
		return ok && got.Gateway == ip("10.20.30.1")
	})
	if stale := e.r.Report().Stale; len(stale) != 0 {
		t.Errorf("stale routes left: %+v", stale)
	}
}
