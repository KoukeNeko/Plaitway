package fake_test

import (
	"errors"
	"net/netip"
	"slices"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/osnet/fake"
)

// linuxTable has a wired interface and two tunnels, as a machine with a VPN does.
// Their indexes are the ones the table hands out: 1, 2 and 3.
func linuxTable() *fake.RouteTable {
	t := fake.NewRouteTable()
	t.LinuxKeying()
	t.AddInterface(osnet.Interface{Name: "eth0", Up: true, Addrs: []netip.Prefix{pfx("192.168.51.185/24")}})
	t.AddInterface(osnet.Interface{Name: "tun0", Up: true, Tunnel: true, Addrs: []netip.Prefix{pfx("10.6.0.2/24")}})
	t.AddInterface(osnet.Interface{Name: "wg0", Up: true, Tunnel: true, Addrs: []netip.Prefix{pfx("10.8.0.2/32")}})
	return t
}

func linuxIndex(t *testing.T, tbl *fake.RouteTable, name string) uint32 {
	t.Helper()
	for _, ifc := range tbl.Interfaces() {
		if ifc.Name == name {
			return uint32(ifc.Index)
		}
	}
	t.Fatalf("no interface %s", name)
	return 0
}

func dumpOf(t *testing.T, tbl *fake.RouteTable, dst string) []osnet.Route {
	t.Helper()
	routes, err := tbl.Dump()
	if err != nil {
		t.Fatal(err)
	}
	return slices.DeleteFunc(routes, func(r osnet.Route) bool { return r.Dst != pfx(dst) })
}

// The kernel refuses a second route with the destination and metric of one that
// exists, whatever interface and next hop it has; other metrics lie beside it.
func TestLinuxKeyingExistsByDestinationAndMetric(t *testing.T) {
	tbl := linuxTable()
	ours := osnet.Route{Dst: pfx("10.50.0.0/16"), Iface: "tun0", Metric: 5, Static: true}
	if err := tbl.Add(ours); err != nil {
		t.Fatal(err)
	}

	for name, rt := range map[string]osnet.Route{
		"the same route":            ours,
		"another interface":         {Dst: pfx("10.50.0.0/16"), Iface: "wg0", Metric: 5},
		"a next hop on another one": {Dst: pfx("10.50.0.0/16"), Gateway: ip("192.168.51.1"), Iface: "eth0", Metric: 5},
	} {
		if err := tbl.Add(rt); !errors.Is(err, osnet.ErrExists) {
			t.Errorf("%s: got %v, want ErrExists", name, err)
		}
	}
	for _, rt := range []osnet.Route{
		{Dst: pfx("10.50.0.0/16"), Iface: "wg0", Metric: 6},
		{Dst: pfx("10.50.0.0/16"), Gateway: ip("192.168.51.254"), Iface: "eth0", Metric: 0},
	} {
		if err := tbl.Add(rt); err != nil {
			t.Errorf("metric %d: %v", rt.Metric, err)
		}
	}

	routes := dumpOf(t, tbl, "10.50.0.0/16")
	var metrics []uint32
	for _, r := range routes {
		metrics = append(metrics, r.Metric)
	}
	if !slices.Equal(metrics, []uint32{0, 5, 6}) {
		t.Errorf("metrics in the table: %v", metrics)
	}
	if best, _ := tbl.Get(pfx("10.50.0.0/16")); best.Metric != 0 || best.Gateway != ip("192.168.51.254") {
		t.Errorf("Get should name the lowest metric: %+v", best)
	}
	if best, _ := tbl.Lookup(ip("10.50.1.1")); best.Metric != 0 {
		t.Errorf("Lookup should take the lowest metric: %+v", best)
	}

	// A longer prefix wins whatever its metric.
	if err := tbl.Add(osnet.Route{Dst: pfx("10.50.1.0/24"), Iface: "wg0", Metric: 9999}); err != nil {
		t.Fatal(err)
	}
	if best, _ := tbl.Lookup(ip("10.50.1.1")); best.Dst != pfx("10.50.1.0/24") {
		t.Errorf("the longer prefix should win: %+v", best)
	}
}

func TestLinuxKeyingDeleteNamesTheWholeRoute(t *testing.T) {
	tbl := linuxTable()
	eth := linuxIndex(t, tbl, "eth0")
	tun := linuxIndex(t, tbl, "tun0")
	gw := ip("192.168.51.1")
	for _, rt := range []osnet.Route{
		{Dst: pfx("203.0.113.10/32"), Gateway: gw, Iface: "eth0", Metric: 1},
		{Dst: pfx("203.0.113.10/32"), Gateway: gw, Iface: "eth0", Metric: 7},
		{Dst: pfx("203.0.113.10/32"), Iface: "tun0", Metric: 5},
	} {
		if err := tbl.Add(rt); err != nil {
			t.Fatal(err)
		}
	}

	for name, rt := range map[string]osnet.Route{
		"another metric":      {Dst: pfx("203.0.113.10/32"), Gateway: gw, Iface: "eth0", Metric: 2},
		"another next hop":    {Dst: pfx("203.0.113.10/32"), Gateway: ip("192.168.51.2"), Iface: "eth0", Metric: 1},
		"no next hop":         {Dst: pfx("203.0.113.10/32"), Iface: "eth0", Metric: 1},
		"another interface":   {Dst: pfx("203.0.113.10/32"), Gateway: gw, Iface: "tun0", Metric: 1},
		"another index":       {Dst: pfx("203.0.113.10/32"), Gateway: gw, IfIndex: tun, Metric: 1},
		"another destination": {Dst: pfx("203.0.113.11/32"), Gateway: gw, Iface: "eth0", Metric: 1},
	} {
		if err := tbl.Delete(rt); !errors.Is(err, osnet.ErrNotFound) {
			t.Errorf("%s: got %v, want ErrNotFound", name, err)
		}
	}
	if n := len(dumpOf(t, tbl, "203.0.113.10/32")); n != 3 {
		t.Fatalf("a delete that matched nothing removed routes: %d left", n)
	}

	// By index, which wins over the name; the removed route is the one that matched.
	if err := tbl.Delete(osnet.Route{Dst: pfx("203.0.113.10/32"), Gateway: gw, IfIndex: eth, Iface: "tun0", Metric: 7}); err != nil {
		t.Fatal(err)
	}
	// By name, when the index is not known.
	if err := tbl.Delete(osnet.Route{Dst: pfx("203.0.113.10/32"), Iface: "tun0", Metric: 5}); err != nil {
		t.Fatal(err)
	}
	left := dumpOf(t, tbl, "203.0.113.10/32")
	if len(left) != 1 || left[0].Metric != 1 || left[0].IfIndex != eth {
		t.Errorf("left: %+v", left)
	}
}

func TestLinuxKeyingDeleteWithoutAnInterface(t *testing.T) {
	tbl := linuxTable()
	for _, rt := range []osnet.Route{
		{Dst: pfx("10.50.0.0/16"), Iface: "tun0", Metric: 5},
		{Dst: pfx("10.50.0.0/16"), Iface: "wg0", Metric: 6},
		{Dst: pfx("10.51.0.0/16"), Iface: "tun0", Metric: 5},
	} {
		if err := tbl.Add(rt); err != nil {
			t.Fatal(err)
		}
	}
	tbl.Append(osnet.Route{Dst: pfx("10.51.0.0/16"), Iface: "wg0", Metric: 5})

	if err := tbl.Delete(osnet.Route{Dst: pfx("10.50.0.0/16"), Metric: 6}); err != nil {
		t.Errorf("one route has the metric 6: %v", err)
	}
	if err := tbl.Delete(osnet.Route{Dst: pfx("10.50.0.0/16"), Metric: 6}); !errors.Is(err, osnet.ErrNotFound) {
		t.Errorf("deleting it again: %v", err)
	}
	err := tbl.Delete(osnet.Route{Dst: pfx("10.51.0.0/16"), Metric: 5})
	if err == nil || errors.Is(err, osnet.ErrNotFound) {
		t.Errorf("two routes have the metric 5, the interface has to be named: %v", err)
	}
	if err := tbl.Delete(osnet.Route{Dst: pfx("10.51.0.0/16"), Iface: "wg0", Metric: 5}); err != nil {
		t.Error(err)
	}
}

// A route appended by a program to a destination and metric that has one is a
// second route, where one that replaces it is not. Delete names the one it
// means.
func TestLinuxKeyingAppendAndReplace(t *testing.T) {
	tbl := linuxTable()
	first := osnet.Route{Dst: pfx("203.0.113.10/32"), Gateway: ip("192.168.51.1"), Iface: "eth0", Metric: 1, Static: true}
	if err := tbl.Add(first); err != nil {
		t.Fatal(err)
	}
	tbl.Append(osnet.Route{Dst: pfx("203.0.113.10/32"), Iface: "tun0", Metric: 1, Static: true})

	if routes := dumpOf(t, tbl, "203.0.113.10/32"); len(routes) != 2 {
		t.Fatalf("an appended route is another route: %+v", routes)
	}
	if err := tbl.Delete(first); err != nil {
		t.Fatal(err)
	}
	left := dumpOf(t, tbl, "203.0.113.10/32")
	if len(left) != 1 || left[0].Iface != "tun0" || left[0].IfIndex != linuxIndex(t, tbl, "tun0") {
		t.Errorf("left: %+v", left)
	}
	// A route that replaces it takes its place, whatever interface and next hop it
	// has.
	tbl.Inject(osnet.Route{Dst: pfx("203.0.113.10/32"), Gateway: ip("192.168.51.1"), Iface: "eth0", Metric: 1})
	left = dumpOf(t, tbl, "203.0.113.10/32")
	if len(left) != 1 || left[0].Iface != "eth0" || left[0].Static {
		t.Errorf("left: %+v", left)
	}
}

func TestLinuxKeyingTunnelInterfacesTakeOnLinkRoutesOfBothFamilies(t *testing.T) {
	tbl := linuxTable() // tun0 has an IPv4 address only
	for _, dst := range []string{"0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1", "2001:db8::/32", "10.9.0.0/16"} {
		if err := tbl.Add(osnet.Route{Dst: pfx(dst), Iface: "tun0", Metric: 5, Static: true}); err != nil {
			t.Errorf("%s: %v", dst, err)
		}
	}
	rt, _ := tbl.Get(pfx("::/1"))
	if rt.IfIndex != linuxIndex(t, tbl, "tun0") || rt.Iface != "tun0" || rt.Gateway.IsValid() {
		t.Errorf("route: %+v", rt)
	}
}

func TestLinuxKeyingNeverPicksAnotherInterface(t *testing.T) {
	tbl := linuxTable()
	eth := linuxIndex(t, tbl, "eth0")
	tun := linuxIndex(t, tbl, "tun0")
	gw := ip("192.168.51.1")
	tests := []struct {
		name string
		rt   osnet.Route
		want error
	}{
		{"gateway on the named interface", osnet.Route{Dst: pfx("1.1.1.1/32"), Gateway: gw, Iface: "eth0", Metric: 1}, nil},
		{"gateway on another interface", osnet.Route{Dst: pfx("1.1.1.2/32"), Gateway: gw, Iface: "tun0", Metric: 1}, osnet.ErrUnreachable},
		{"gateway on another interface, by index", osnet.Route{Dst: pfx("1.1.1.3/32"), Gateway: gw, IfIndex: tun, Metric: 1}, osnet.ErrUnreachable},
		{"gateway on no subnet", osnet.Route{Dst: pfx("1.1.1.4/32"), Gateway: ip("10.99.0.1"), IfIndex: eth, Metric: 1}, osnet.ErrUnreachable},
		{"interface that does not exist", osnet.Route{Dst: pfx("1.1.1.5/32"), Iface: "tun9", Metric: 1}, osnet.ErrUnreachable},
		{"index that does not exist", osnet.Route{Dst: pfx("1.1.1.6/32"), IfIndex: 99, Iface: "eth0", Metric: 1}, osnet.ErrUnreachable},
		{"the index wins over the name", osnet.Route{Dst: pfx("1.1.1.7/32"), IfIndex: tun, Iface: "eth0", Metric: 1}, nil},
		{"no interface at all", osnet.Route{Dst: pfx("1.1.1.8/32"), Metric: 1}, osnet.ErrUnreachable},
		{"gateway alone is found on its subnet", osnet.Route{Dst: pfx("1.1.1.9/32"), Gateway: gw, Metric: 1}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tbl.Add(tt.rt); !errors.Is(err, tt.want) {
				t.Errorf("got %v, want %v", err, tt.want)
			}
		})
	}
	if rt, _ := tbl.Get(pfx("1.1.1.7/32")); rt.Iface != "tun0" || rt.IfIndex != tun {
		t.Errorf("the route should be on the interface of the index: %+v", rt)
	}
	if rt, _ := tbl.Get(pfx("1.1.1.9/32")); rt.Iface != "eth0" || rt.IfIndex != eth {
		t.Errorf("a gateway found by its subnet: %+v", rt)
	}

	tbl.SetUp("tun0", false)
	if err := tbl.Add(osnet.Route{Dst: pfx("1.1.1.10/32"), Iface: "tun0", Metric: 1}); !errors.Is(err, osnet.ErrUnreachable) {
		t.Errorf("an interface that is down: %v", err)
	}
}

// IPv6 routers are reached through their link-local address, which is on-link
// on every interface that is named. Dump says so with the interface as the zone,
// and the zone is no part of the identity.
func TestLinuxKeyingLinkLocalGateway(t *testing.T) {
	tbl := linuxTable()
	tbl.AddInterface(osnet.Interface{Name: "eth1", Up: true, Addrs: []netip.Prefix{pfx("192.168.77.4/24")}})
	bypass := osnet.Route{Dst: pfx("2001:db8::10/128"), Gateway: ip("fe80::1"), Iface: "eth0", Metric: 1}
	if err := tbl.Add(bypass); err != nil {
		t.Fatal(err)
	}
	// The router of the other interface has the same address.
	other := bypass
	other.Iface, other.Metric = "eth1", 2
	if err := tbl.Add(other); err != nil {
		t.Fatal(err)
	}

	routes := dumpOf(t, tbl, "2001:db8::10/128")
	if len(routes) != 2 || routes[0].Gateway.Zone() != "eth0" || routes[1].Gateway.Zone() != "eth1" {
		t.Fatalf("zones: %+v", routes)
	}
	// A zone or none, the key is the same.
	if err := tbl.Add(bypass); !errors.Is(err, osnet.ErrExists) {
		t.Errorf("adding again: %v", err)
	}
	zoned := bypass
	zoned.Gateway = zoned.Gateway.WithZone("eth0")
	if err := tbl.Delete(zoned); err != nil {
		t.Errorf("deleting with the zone: %v", err)
	}
	if err := tbl.Delete(other); err != nil {
		t.Errorf("deleting without it: %v", err)
	}
}

func TestLinuxKeyingInterfacesHaveIndexesThatAreNeverReused(t *testing.T) {
	tbl := linuxTable()
	first := linuxIndex(t, tbl, "tun0")
	if first == 0 {
		t.Fatal("no index")
	}
	tbl.DestroyInterface("tun0")
	tbl.AddInterface(osnet.Interface{Name: "tun0", Up: true, Tunnel: true, Addrs: []netip.Prefix{pfx("10.6.0.2/24")}})
	if second := linuxIndex(t, tbl, "tun0"); second == first {
		t.Errorf("a new interface got the index %d again", second)
	}
	// An index given by the test is kept, and the next free one is above it.
	tbl.AddInterface(osnet.Interface{Name: "dummy0", Index: 40, Up: true})
	tbl.AddInterface(osnet.Interface{Name: "dummy1", Up: true})
	if got := linuxIndex(t, tbl, "dummy1"); got != 41 {
		t.Errorf("dummy1 has the index %d, want 41", got)
	}
}

func TestLinuxKeyingConnectedRoutes(t *testing.T) {
	tbl := linuxTable()
	tbl.AddInterface(osnet.Interface{Name: "eth1", Up: true, Addrs: []netip.Prefix{pfx("192.168.77.4/24"), pfx("fd00:77::4/64")}})

	v4 := dumpOf(t, tbl, "192.168.77.0/24")
	v6 := dumpOf(t, tbl, "fd00:77::/64")
	if len(v4) != 1 || v4[0].Metric != 0 || v4[0].Static || v4[0].Gateway.IsValid() || v4[0].IfIndex != linuxIndex(t, tbl, "eth1") {
		t.Errorf("IPv4: %+v", v4)
	}
	if len(v6) != 1 || v6[0].Metric != 256 || v6[0].Static {
		t.Errorf("IPv6: %+v", v6)
	}
	// The kernel does not take a user route with the destination and metric of
	// the connected one, and does take one with another metric: a profile that
	// routes the tunnel's own subnet ends up beside the kernel's route.
	if err := tbl.Add(osnet.Route{Dst: pfx("10.6.0.0/24"), Iface: "tun0", Metric: 0}); !errors.Is(err, osnet.ErrExists) {
		t.Errorf("metric 0: %v", err)
	}
	if err := tbl.Add(osnet.Route{Dst: pfx("10.6.0.0/24"), Iface: "tun0", Metric: 5, Static: true}); err != nil {
		t.Errorf("metric 5: %v", err)
	}

	// Going down takes every route of the interface along; coming up brings the
	// connected ones back.
	tbl.SetUp("tun0", false)
	if routes := dumpOf(t, tbl, "10.6.0.0/24"); len(routes) != 0 {
		t.Errorf("routes of an interface that is down: %+v", routes)
	}
	tbl.SetUp("tun0", true)
	if routes := dumpOf(t, tbl, "10.6.0.0/24"); len(routes) != 1 || routes[0].Metric != 0 {
		t.Errorf("after coming up: %+v", routes)
	}
}

func TestLinuxKeyingRefusesAnotherKeyingOnTheSameTable(t *testing.T) {
	for name, prepare := range map[string]func(*fake.RouteTable){
		"a table with routes": func(tbl *fake.RouteTable) {
			tbl.AddInterface(osnet.Interface{Name: "eth0", Up: true, Addrs: []netip.Prefix{pfx("192.168.51.185/24")}})
		},
		"a Windows table": func(tbl *fake.RouteTable) { tbl.WindowsKeying() },
	} {
		t.Run(name, func(t *testing.T) {
			tbl := fake.NewRouteTable()
			prepare(tbl)
			defer func() {
				if recover() == nil {
					t.Error("LinuxKeying did not panic")
				}
			}()
			tbl.LinuxKeying()
		})
	}
}

func TestLinuxHostPicksTheDefaultRouteOfTheLowestMetric(t *testing.T) {
	h := fake.NewLinuxHost()
	h.AddPhysical("eth0", pfx("192.168.51.185/24"), ip("192.168.51.1"))
	h.SetDefaultMetric("wlan0", 600)
	h.AddPhysical("wlan0", pfx("192.168.51.186/24"), ip("192.168.51.1"))
	h.AddTunnel("wg0", pfx("10.6.0.2/32"))

	ns, _ := h.Net.Snapshot()
	if ns.DefaultV4 == nil || ns.DefaultV4.Iface != "eth0" || ns.DefaultV4.Gateway != ip("192.168.51.1") {
		t.Errorf("default: %+v", ns.DefaultV4)
	}
	if len(ns.Interfaces) != 3 || ns.Interfaces[0].Index == 0 || ns.Interfaces[0].Metric != 0 {
		t.Errorf("interfaces: %+v", ns.Interfaces)
	}
	if !slices.Contains(ns.Connected, pfx("192.168.51.0/24")) {
		t.Errorf("connected: %v", ns.Connected)
	}

	// The cable is pulled: the next default route takes over.
	h.Routes.SetUp("eth0", false)
	h.Sync()
	if ns, _ := h.Net.Snapshot(); ns.DefaultV4 == nil || ns.DefaultV4.Iface != "wlan0" {
		t.Errorf("default after the cable is pulled: %+v", ns.DefaultV4)
	}

	// Moving to another network replaces the default routes of that interface only.
	h.MoveNetwork("wlan0", pfx("10.20.30.7/24"), ip("10.20.30.1"))
	routes := dumpOf(t, h.Routes, "0.0.0.0/0")
	if len(routes) != 1 || routes[0].Gateway != ip("10.20.30.1") || routes[0].Metric != 600 {
		t.Errorf("default routes: %+v", routes)
	}
}

func TestLinuxHostReportsALinkLocalRouterWithItsInterface(t *testing.T) {
	h := fake.NewLinuxHost()
	h.AddPhysical("eth0", pfx("192.168.51.185/24"), ip("192.168.51.1"))
	h.AddPhysical("eth1", pfx("fd00:1::5/64"), ip("fe80::1"))

	ns, _ := h.Net.Snapshot()
	if ns.DefaultV6 == nil || ns.DefaultV6.Gateway.Zone() != "eth1" || ns.DefaultV6.Gateway.WithZone("") != ip("fe80::1") {
		t.Errorf("IPv6 default: %+v", ns.DefaultV6)
	}
}

func TestDNSKeepsTheInterfaceOfAnEntry(t *testing.T) {
	d := fake.NewDNS()
	entry := osnet.DNSEntry{Servers: []netip.Addr{ip("10.0.0.53")}, MatchDomains: []string{"corp.lan"}, Order: 2, Iface: "wg0"}
	if err := d.Apply("a", []osnet.DNSEntry{entry}); err != nil {
		t.Fatal(err)
	}
	d.Leave("b", entry)

	for name, got := range map[string][]osnet.DNSEntry{"Entries": d.Entries("a"), "All": d.All()["a"], "Leave": d.Entries("b")} {
		if len(got) != 1 || got[0].Iface != "wg0" {
			t.Errorf("%s: %+v", name, got)
		}
	}
}
