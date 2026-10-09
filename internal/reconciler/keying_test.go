package reconciler

import (
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/osnet/fake"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// The scenarios here are what only a table keyed by prefix, interface and next
// hop can do. The scenarios of the other files run under both keyings.

// routesWithIndexes writes the installed route plans as "<prefix> if<index>
// metric <n> via <gateway>".
func routesWithIndexes(d Desired) []string {
	var out []string
	for _, p := range d.Routes {
		if !p.Install {
			continue
		}
		line := p.Route.Dst.String() + " if" + itoa(p.Route.IfIndex) + " metric " + itoa(p.Route.Metric)
		if p.Route.Gateway.IsValid() {
			line += " via " + p.Route.Gateway.String()
		}
		out = append(out, line)
	}
	return out
}

func itoa(n uint32) string { return strconv.FormatUint(uint64(n), 10) }

func windowsNet() osnet.NetState {
	return osnet.NetState{
		Interfaces: []osnet.Interface{
			{Name: "Ethernet", Index: 12, Up: true, Addrs: pfxs("192.168.51.185/24")},
			{Name: "Plaitway-wg", Index: 31, Up: true, Tunnel: true, Addrs: pfxs("10.6.0.2/32")},
		},
		DefaultV4: &osnet.Nexthop{Gateway: ip("192.168.51.1"), Iface: "Ethernet"},
		DefaultV6: &osnet.Nexthop{Gateway: ip("fe80::1"), Iface: "Ethernet"},
		Connected: pfxs("192.168.51.0/24"),
	}
}

func TestComputeNamesTheInterfaceAndMetricOnWindows(t *testing.T) {
	in := endpoints(up("wg", 1, "Plaitway-wg", tunnel.RoleFull, "0.0.0.0/0", "::/0", "10.9.0.0/16"), "203.0.113.10", "2001:db8::10")

	got := routesWithIndexes(computeFor(KeyByPrefixInterfaceNextHop, []tunnel.Intent{in}, windowsNet()))

	// Host routes first, through the physical interface and router of the
	// default route, then the tunnel's. The bypass metric is below the tunnel's.
	checkLines(t, "routes", got, []string{
		"203.0.113.10/32 if12 metric 1 via 192.168.51.1",
		"2001:db8::10/128 if12 metric 1 via fe80::1",
		"10.9.0.0/16 if31 metric 5",
		"0.0.0.0/1 if31 metric 5",
		"128.0.0.0/1 if31 metric 5",
		"::/1 if31 metric 5",
		"8000::/1 if31 metric 5",
	})
}

func TestComputeAddsNoIndexOrMetricOnMacOS(t *testing.T) {
	ns := windowsNet()
	in := endpoints(up("wg", 1, "Plaitway-wg", tunnel.RoleFull, "0.0.0.0/0", "10.9.0.0/16"), "203.0.113.10")

	for _, line := range routesWithIndexes(computeFor(KeyByPrefix, []tunnel.Intent{in}, ns)) {
		if !strings.Contains(line, " if0 metric 0") {
			t.Errorf("a macOS route carries an index or metric: %s", line)
		}
	}
}

// An interface that the network state does not list has no index. Its routes
// are planned all the same and wait.
func TestComputeLeavesTheIndexOutForAnUnknownInterface(t *testing.T) {
	ns := windowsNet()
	ns.Interfaces = ns.Interfaces[:1]
	in := up("wg", 1, "Plaitway-wg", tunnel.RoleSplit, "10.9.0.0/16")

	got := routesWithIndexes(computeFor(KeyByPrefixInterfaceNextHop, []tunnel.Intent{in}, ns))

	checkLines(t, "routes", got, []string{"10.9.0.0/16 if0 metric 5"})
}

func TestTunnelRoutesCarryTheAdapterIndexAndAMetric(t *testing.T) {
	e := newEnv(t, windowsCase)
	e.addTunnel("utun10", "10.6.0.2/24")
	e.announce(endpoints(up("wg", 1, "utun10", tunnel.RoleFull, "0.0.0.0/0"), "203.0.113.10"))

	routes, err := e.host.Routes.Dump()
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, rt := range routes {
		switch rt.Dst {
		case pfx("0.0.0.0/1"), pfx("128.0.0.0/1"):
			seen++
			if rt.IfIndex != e.host.ifIndex("utun10") || rt.Metric != tunnelMetric {
				t.Errorf("%s: %+v", rt.Dst, rt)
			}
		case pfx("203.0.113.10/32"):
			seen++
			if rt.IfIndex != e.host.ifIndex("en0") || rt.Metric != bypassMetric || rt.Gateway != ip("192.168.51.1") {
				t.Errorf("bypass route: %+v", rt)
			}
		}
	}
	if seen != 3 {
		t.Errorf("found %d of the 3 routes", seen)
	}
}

// Two routes to one prefix, ours through the tunnel and another program's
// through the physical interface, are two routes. Plaitway reports its own as
// installed, leaves the other alone when nothing changes, and when it goes it
// deletes its own by interface and next hop.
func TestRoutesToOnePrefixOnDifferentInterfacesAreNeighbours(t *testing.T) {
	e := newEnv(t, windowsCase)
	e.addTunnel("utun11", "10.8.0.6/24")
	e.host.Inject(osnet.Route{Dst: pfx("10.50.0.0/16"), Gateway: ip("192.168.51.254"), Iface: "en0", Static: true})
	e.host.Inject(osnet.Route{Dst: pfx("10.50.0.0/16"), Iface: "en0", Static: true})

	e.announce(up("asus", 1, "utun11", tunnel.RoleSplit, "10.50.0.0/16"))

	e.checkTable("10.50.0.0/16 dev en0", "10.50.0.0/16 via 192.168.51.254 dev en0", "10.50.0.0/16 dev utun11")
	if rr := e.routeReport("10.50.0.0/16", "asus"); rr.State != tunnel.RouteInstalled {
		t.Errorf("report: %+v", rr)
	}
	before := e.opLines()
	e.change(osnet.ChangeHeartbeat)
	if after := e.opLines(); !slices.Equal(before, after) {
		t.Errorf("a pass on an unchanged table did something: %v", after[len(before):])
	}

	e.r.Withdraw("asus")

	e.checkTable("10.50.0.0/16 dev en0", "10.50.0.0/16 via 192.168.51.254 dev en0")
	var removed []osnet.Route
	for _, op := range e.host.Routes.Ops() {
		if op.Kind == fake.OpDelete && op.Err == nil {
			removed = append(removed, op.Removed)
		}
	}
	if len(removed) != 1 || removed[0].IfIndex != e.host.ifIndex("utun11") {
		t.Errorf("only the tunnel's route may be deleted: %+v", removed)
	}
}

// Windows adds the interface's metric to the route's. When the sums are equal
// the table breaks the tie, and the Reconciler neither fights for the prefix nor
// disturbs the other route.
func TestMetricTieWithAnotherProgramsRoute(t *testing.T) {
	e := newEnv(t, windowsCase)
	e.addTunnel("utun11", "10.8.0.6/24")
	e.host.Routes.SetInterfaceMetric("en0", tunnelMetric+tunnelAdapterMetric)
	e.host.Sync()
	foreign := osnet.Route{Dst: pfx("10.50.0.0/16"), Gateway: ip("192.168.51.254"), Iface: "en0", Static: true}
	e.host.Inject(foreign)

	e.announce(up("asus", 1, "utun11", tunnel.RoleSplit, "10.50.0.0/16"))

	ours, _ := e.host.Routes.Get(pfx("10.50.0.0/16"))
	if a, b := e.host.Routes.EffectiveMetric(ours), e.host.Routes.EffectiveMetric(e.host.index(foreign)); a != b {
		t.Fatalf("setup: the metrics should tie, got %d and %d", a, b)
	}
	before := e.opLines()
	for range 3 {
		e.change(osnet.ChangeHeartbeat)
	}
	if after := e.opLines(); !slices.Equal(before, after) {
		t.Errorf("the Reconciler reacted to a tie: %v", after[len(before):])
	}
	if rr := e.routeReport("10.50.0.0/16", "asus"); rr.State != tunnel.RouteInstalled {
		t.Errorf("report: %+v", rr)
	}
	e.r.Withdraw("asus")
	e.checkTable("10.50.0.0/16 via 192.168.51.254 dev en0")
}

// Another program deletes our route to a prefix and puts its own for the same
// prefix on another interface. Ours is gone, not changed: it is installed again
// beside theirs, which stays as it is.
func TestRouteReplacedByAnotherProgramOnAnotherInterface(t *testing.T) {
	e := newEnv(t, windowsCase)
	e.addTunnel("utun11", "10.8.0.6/24")
	e.announce(up("asus", 1, "utun11", tunnel.RoleSplit, "10.50.0.0/16"))
	e.host.Routes.Remove(pfx("10.50.0.0/16"))
	theirs := osnet.Route{Dst: pfx("10.50.0.0/16"), Iface: "en0", Static: true}
	e.host.Inject(theirs)

	e.change(osnet.ChangeHeartbeat)

	e.checkTable("10.50.0.0/16 dev en0", "10.50.0.0/16 dev utun11")
	if strings.Contains(e.logs.String(), "changed by another program") {
		t.Errorf("a route that is gone was taken for a changed one:\n%s", e.logs)
	}
	got := e.journalFor(kindRoute, "10.50.0.0/16")
	if want := []string{"pending", "applied", "removed", "pending", "applied"}; !slices.Equal(got, want) {
		t.Errorf("journal: %v, want %v", got, want)
	}
	e.r.Withdraw("asus")
	e.checkTable("10.50.0.0/16 dev en0")
}

// The adapter of a tunnel is created again and gets another index: the routes
// move with it, the old ones are closed in the journal, and nothing of the old
// index stays.
func TestTunnelAdapterComesBackWithAnotherIndex(t *testing.T) {
	e := newEnv(t, windowsCase)
	e.addTunnel("utun11", "10.8.0.6/24")
	first := e.host.ifIndex("utun11")
	e.announce(up("asus", 1, "utun11", tunnel.RoleSplit, "10.50.0.0/16"))

	e.host.DestroyInterface("utun11")
	e.change(osnet.ChangeRoute)
	e.addTunnel("utun11", "10.8.0.6/24")

	second := e.host.ifIndex("utun11")
	if first == second {
		t.Fatalf("setup: the new adapter has the old index %d", first)
	}
	routes, _ := e.host.Routes.Dump()
	var found []uint32
	for _, rt := range routes {
		if rt.Dst == pfx("10.50.0.0/16") {
			found = append(found, rt.IfIndex)
		}
	}
	if !slices.Equal(found, []uint32{second}) {
		t.Errorf("routes to 10.50.0.0/16 through interfaces %v, want only %d", found, second)
	}
	if left := e.unresolved(); len(left) != 1 || left[0].IfIndex != second {
		t.Errorf("the journal should list only the new route: %+v", left)
	}
}

// Routes of an interface that went away went with it. That is not a foreign
// deletion, and it does not make the Reconciler retry in a loop: the route waits
// for the interface, which brings the next pass when it is back.
func TestVanishedInterfaceIsNotRetriedAndNotBlamedOnAnyone(t *testing.T) {
	e := newEnv(t, windowsCase)
	e.addTunnel("utun11", "10.8.0.6/24")
	e.announce(up("asus", 1, "utun11", tunnel.RoleSplit, "10.50.0.0/16"))

	e.host.DestroyInterface("utun11")
	e.change(osnet.ChangeRoute)
	opsAfterTheFirstPass := len(e.opLines())
	for range 3 {
		e.change(osnet.ChangeHeartbeat)
	}

	if rr := e.routeReport("10.50.0.0/16", "asus"); rr.State != tunnel.RoutePending || rr.Detail != "interface utun11 not found" {
		t.Errorf("report: %+v", rr)
	}
	if e.r.dirty || e.r.failStreak != 0 || e.r.failedPasses != 0 {
		t.Errorf("waiting for an interface is no failure: dirty %v, streak %d, failed %d", e.r.dirty, e.r.failStreak, e.r.failedPasses)
	}
	if n := len(e.opLines()); n != opsAfterTheFirstPass {
		t.Errorf("passes keep trying: %v", e.opLines()[opsAfterTheFirstPass:])
	}
	if strings.Contains(e.logs.String(), "another program") {
		t.Errorf("blamed somebody:\n%s", e.logs)
	}
	if left := e.unresolved(); len(left) != 0 {
		t.Errorf("the journal lists %+v", left)
	}
}

// The default route moves from one interface to another on the same router, as
// when a cable is plugged in while Wi-Fi stays up. The bypass route of a tunnel
// endpoint is bound to the interface it was added for, so it has to move: the
// old one is deleted by its key, the new one added, and the engines rebind.
func TestWindowsBypassFollowsTheDefaultRouteToAnotherInterface(t *testing.T) {
	e := newEnv(t, windowsCase)
	e.host.AddInterface(osnet.Interface{Name: "en1", Up: true, Addrs: pfxs("192.168.51.136/24")})
	e.host.Sync()
	e.host.AddTunnel("utun11", pfx("10.8.0.6/24"))
	var rebinds atomic.Int32
	e.r.SetRebind(func() { rebinds.Add(1) })
	ovpn := endpoints(up("ovpn", 1, "utun11", tunnel.RoleFull, "0.0.0.0/0"), "198.51.100.88")
	e.announce(ovpn)
	e.checkTable("0.0.0.0/1 dev utun11", "128.0.0.0/1 dev utun11", "198.51.100.88/32 via 192.168.51.1 dev en0")
	start := len(e.host.Routes.Ops())

	e.host.Routes.Remove(defaultV4Route)
	e.host.Inject(osnet.Route{Dst: defaultV4Route, Gateway: ip("192.168.51.1"), Iface: "en1", Static: true})
	e.host.Sync()
	e.change(osnet.ChangeRoute)

	e.checkTable("0.0.0.0/1 dev utun11", "128.0.0.0/1 dev utun11", "198.51.100.88/32 via 192.168.51.1 dev en1")
	var deletedIndex, addedIndex uint32
	for _, op := range e.host.Routes.Ops()[start:] {
		switch {
		case op.Kind == fake.OpDelete && op.Err == nil && op.Removed.Dst == pfx("198.51.100.88/32"):
			deletedIndex = op.Removed.IfIndex
		case op.Kind == fake.OpAdd && op.Err == nil && op.Route.Dst == pfx("198.51.100.88/32"):
			addedIndex = op.Route.IfIndex
		}
	}
	if deletedIndex != e.host.ifIndex("en0") || addedIndex != e.host.ifIndex("en1") {
		t.Errorf("the old route (interface %d) should be deleted and one through interface %d added, got %d and %d",
			e.host.ifIndex("en0"), e.host.ifIndex("en1"), deletedIndex, addedIndex)
	}
	if stale := e.r.Report().Stale; len(stale) != 0 {
		t.Errorf("stale routes left: %+v", stale)
	}
	if n := rebinds.Load(); n != 1 {
		t.Errorf("rebinds: %d", n)
	}
	if n := count(e.opLines(), "add 0.0.0.0/1"); n != 1 {
		t.Errorf("the tunnel's own routes must not be touched, 0.0.0.0/1 was added %d times", n)
	}
}

// A route of ours that stays on the old interface, after a change nobody told
// the Reconciler about, is stale: same router, wrong interface.
func TestFindStaleWhenTheDefaultRouteMovedToAnotherInterface(t *testing.T) {
	ns := osnet.NetState{
		Interfaces: []osnet.Interface{
			{Name: "en0", Index: 4, Up: true, Addrs: pfxs("192.168.51.185/24")},
			{Name: "en1", Index: 5, Up: true, Addrs: pfxs("192.168.51.136/24")},
		},
		DefaultV4: &osnet.Nexthop{Gateway: ip("192.168.51.1"), Iface: "en1"},
	}
	rt := osnet.Route{Dst: pfx("203.0.113.10/32"), Gateway: ip("192.168.51.1"), Iface: "en0", IfIndex: 4, Static: true}
	endpoints := map[netip.Prefix]bool{rt.Dst: true}
	for _, tt := range []struct {
		keying RouteKeying
		want   string
	}{
		{KeyByPrefix, ""}, // the kernel chose the interface, and follows the gateway
		{KeyByPrefixInterfaceNextHop, "interface is not the default route's"},
	} {
		table := map[routeKey]osnet.Route{tt.keying.key(rt): rt}

		got := findStale(tt.keying, table, ns, endpoints, func(osnet.Route) bool { return false })

		switch {
		case tt.want == "" && len(got) != 0:
			t.Errorf("keying %d: reported as stale: %+v", tt.keying, got)
		case tt.want != "" && (len(got) != 1 || got[0].Reason != tt.want):
			t.Errorf("keying %d: got %+v, want %q", tt.keying, got, tt.want)
		}
	}
}

// IPv6: the physical default route has a link-local router, and the bypass
// route of an IPv6 endpoint goes through it on the interface's index.
func TestWindowsBypassForAnIPv6Endpoint(t *testing.T) {
	e := newEnv(t, windowsCase)
	e.host.Inject(osnet.Route{Dst: defaultV6Route, Gateway: ip("fe80::1"), Iface: "en0", Static: true})
	e.host.Sync()
	e.addTunnel("utun10", "10.6.0.2/24")

	e.announce(endpoints(up("wg", 1, "utun10", tunnel.RoleFull, "0.0.0.0/0", "::/0"), "2001:db8::10"))

	e.checkTable(
		"0.0.0.0/1 dev utun10",
		"128.0.0.0/1 dev utun10",
		"::/1 dev utun10",
		"2001:db8::10/128 via fe80::1 dev en0",
		"8000::/1 dev utun10",
	)
	rt, ok := e.host.Routes.Get(pfx("2001:db8::10/128"))
	if !ok || rt.IfIndex != e.host.ifIndex("en0") || rt.Metric != bypassMetric {
		t.Errorf("bypass route: %+v", rt)
	}
	if got := e.journalFor(kindRoute, "2001:db8::10/128"); !slices.Equal(got, []string{"pending fe80::1", "applied fe80::1"}) {
		t.Errorf("journal: %v", got)
	}
}

// A crash that leaves the adapter of a tunnel behind leaves its routes too. They
// are in the journal, and recovery removes them by their key, not the route of
// another program to the same prefix through another interface.
func TestRecoveryRemovesTheRoutesOfAnAdapterThatSurvivedTheCrash(t *testing.T) {
	e := newEnv(t, windowsCase)
	e.bothTunnels()
	neighbour := osnet.Route{Dst: pfx("0.0.0.0/1"), Gateway: ip("192.168.51.254"), Iface: "en0", Static: true}
	e.host.Inject(neighbour)
	e.announce(wgIntent())
	e.checkTable(
		"0.0.0.0/1 via 192.168.51.254 dev en0",
		"0.0.0.0/1 dev utun10",
		"128.0.0.0/1 dev utun10",
		"203.0.113.10/32 via 192.168.51.1 dev en0",
		"::/1 dev utun10",
		"8000::/1 dev utun10",
	)

	e.crash() // the adapters stay

	e.newReconciler()

	e.checkTable("0.0.0.0/1 via 192.168.51.254 dev en0")
	if left := e.unresolved(); len(left) != 0 {
		t.Errorf("the journal still lists %+v", left)
	}
}

// What the macOS version wrote before there were interface indexes. These are
// the lines it wrote, byte for byte.
const macosJournal = `{"seq":1,"time":"2026-10-07T12:00:00Z","owner":"wg","kind":"route","key":"203.0.113.10/32","state":"pending","gateway":"192.168.51.1","iface":"en0"}
{"seq":2,"time":"2026-10-07T12:00:00Z","owner":"wg","kind":"route","key":"203.0.113.10/32","state":"applied","gateway":"192.168.51.1","iface":"en0","fingerprint":"192.168.51.1|en0|0x80b"}
{"seq":3,"time":"2026-10-07T12:00:00Z","owner":"wg","kind":"route","key":"198.51.100.7/32","state":"pending","gateway":"192.168.51.1","iface":"en0"}
`

// A journal written by the macOS version, found by a Windows one. It does not
// say which route was meant (no interface, only a name that is not a Windows
// adapter's), so it is not turned into deletes: the routes in the table stay,
// whoever they belong to. The macOS version, reading the same file, removes
// them, which is what makes this a test of the keying.
func TestOldMacOSJournalIsNeverTurnedIntoDeletesOnWindows(t *testing.T) {
	for _, tt := range []struct {
		k       keyingCase
		want    []string
		wantNot string
	}{
		{macosCase, nil, ""},
		{windowsCase, []string{
			"198.51.100.7/32 via 192.168.51.1 dev en0",
			"203.0.113.10/32 via 192.168.51.1 dev en0",
		}, "written for another kind of routing table, left in place"},
	} {
		t.Run(tt.k.name, func(t *testing.T) {
			e := newEnv(t, tt.k)
			e.host.Inject(osnet.Route{Dst: pfx("203.0.113.10/32"), Gateway: ip("192.168.51.1"), Iface: "en0", Static: true, Flags: 0x80b})
			e.host.Inject(osnet.Route{Dst: pfx("198.51.100.7/32"), Gateway: ip("192.168.51.1"), Iface: "en0", Static: true})
			e.crash()
			if err := os.WriteFile(e.journal, []byte(macosJournal), 0o600); err != nil {
				t.Fatal(err)
			}

			e.newReconciler()

			e.checkTable(tt.want...)
			if left := e.unresolved(); len(left) != 0 {
				t.Errorf("unresolved: %+v", left)
			}
			var notes []string
			for _, rec := range e.journalFile() {
				if rec.State == stateRemoved {
					notes = append(notes, rec.Note)
				}
			}
			if tt.wantNot != "" && !slices.Contains(notes, tt.wantNot) {
				t.Errorf("journal notes %v, want %q", notes, tt.wantNot)
			}
		})
	}
}

// The record of a macOS run is what it has always been: no field for the
// interface index.
func TestMacOSJournalLinesHaveNoInterfaceIndex(t *testing.T) {
	e := newEnv(t, macosCase)
	e.addTunnel("utun10", "10.6.0.2/24")
	e.announce(wgIntent())

	data, err := os.ReadFile(e.journal)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"key":"203.0.113.10/32"`) {
		t.Fatalf("setup: no route record in\n%s", data)
	}
	if strings.Contains(string(data), "ifindex") {
		t.Errorf("a macOS journal names an interface index:\n%s", data)
	}
}

// Windows interface names are up to 256 characters and the adapters Plaitway
// creates are longer than the 15 of IFNAMSIZ.
func TestInterfaceNameLengthDependsOnThePlatform(t *testing.T) {
	name := "Plaitway-ovpn-0123456789abcdef"
	in := up("ovpn", 1, name, tunnel.RoleSplit, "10.50.0.0/16")

	if err := validateIntent(KeyByPrefix, in); err == nil {
		t.Error("macOS accepted an interface name longer than IFNAMSIZ")
	}
	if err := validateIntent(KeyByPrefixInterfaceNextHop, in); err != nil {
		t.Errorf("Windows refused %q: %v", name, err)
	}
	in.Iface = strings.Repeat("a", maxIfaceNameWindows+1)
	if err := validateIntent(KeyByPrefixInterfaceNextHop, in); err == nil {
		t.Error("Windows accepted a name longer than it allows")
	}
}

func TestNewRefusesAnUnknownKeying(t *testing.T) {
	host := fake.NewHost()
	cfg := Config{Routes: host.Routes, DNS: host.DNS, Net: host.Net, JournalPath: t.TempDir() + "/journal", Keying: RouteKeying(99)}

	if _, err := New(cfg); err == nil {
		t.Error("accepted")
	}
}

// Where a prefix can have several routes, a stale one is named by more than its
// prefix: removing one of two routes to the same host must leave the other.
func TestStaleRoutesToOneHostAreNamedApart(t *testing.T) {
	e := newEnv(t, windowsCase)
	e.addTunnel("utun11", "10.8.0.6/24")
	e.host.Inject(osnet.Route{Dst: pfx("198.51.100.88/32"), Gateway: ip("10.99.0.1"), Iface: "en0", Static: true})
	e.host.Inject(osnet.Route{Dst: pfx("198.51.100.88/32"), Gateway: ip("10.99.0.2"), Iface: "en0", Static: true})
	e.announce(up("ovpn", 1, "utun11", tunnel.RoleSplit, "10.50.0.0/16"))

	stale := e.r.Report().Stale
	if len(stale) != 2 || stale[0].Key == stale[1].Key {
		t.Fatalf("two stale routes need two names: %+v", stale)
	}
	if err := e.r.RemoveStale(stale[0].Key); err != nil {
		t.Fatal(err)
	}

	e.checkTable("10.50.0.0/16 dev utun11", "198.51.100.88/32 via "+stale[1].Route.Gateway.String()+" dev en0")
	if rest := e.r.Report().Stale; len(rest) != 1 || rest[0].Key != stale[1].Key {
		t.Errorf("stale after the removal: %+v", rest)
	}
}
func TestSameRoute(t *testing.T) {
	base := osnet.Route{Dst: pfx("203.0.113.10/32"), Gateway: ip("192.168.51.1"), Iface: "en0", IfIndex: 4, Metric: 1, Static: true}
	with := func(change func(*osnet.Route)) osnet.Route {
		rt := base
		change(&rt)
		return rt
	}
	tests := []struct {
		name         string
		other        osnet.Route
		macOS, winOS bool
	}{
		{"identical", base, true, true},
		{"another metric", with(func(r *osnet.Route) { r.Metric = 9 }), true, true},
		{"another interface on the same router", with(func(r *osnet.Route) { r.Iface, r.IfIndex = "en1", 5 }), true, false},
		{"another router", with(func(r *osnet.Route) { r.Gateway = ip("192.168.51.2") }), false, false},
		{"another prefix", with(func(r *osnet.Route) { r.Dst = pfx("203.0.113.11/32") }), false, false},
		{"a blackhole", with(func(r *osnet.Route) { r.Blackhole = true }), false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := KeyByPrefix.same(base, tt.other); got != tt.macOS {
				t.Errorf("macOS: %v, want %v", got, tt.macOS)
			}
			if got := KeyByPrefixInterfaceNextHop.same(base, tt.other); got != tt.winOS {
				t.Errorf("Windows: %v, want %v", got, tt.winOS)
			}
		})
	}
}

// A table the Reconciler synced every record for before Linux existed keeps doing
// so: only the Linux table skips the sync of the records that follow a change.
func TestOnlyTheLinuxTableSyncsPendingRecordsOnly(t *testing.T) {
	for _, k := range []RouteKeying{KeyByPrefix, KeyByPrefixInterfaceNextHop} {
		if k.syncsPendingOnly() {
			t.Errorf("keying %d syncs pending records only, it syncs every record", k)
		}
	}
	if !KeyLinux.syncsPendingOnly() {
		t.Error("the Linux table syncs every record")
	}
}
