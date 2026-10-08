package fake_test

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/osnet/fake"
)

func windowsTable() *fake.RouteTable {
	tbl := fake.NewRouteTable()
	tbl.WindowsKeying()
	tbl.AddInterface(osnet.Interface{Name: "Ethernet", Index: 2, Up: true, Addrs: []netip.Prefix{pfx("192.168.1.20/24")}})
	tbl.AddInterface(osnet.Interface{Name: "Wi-Fi", Index: 23, Up: true, Addrs: []netip.Prefix{pfx("192.168.50.20/24")}})
	tbl.AddInterface(osnet.Interface{Name: "Plaitway", Index: 31, Up: true, Tunnel: true, Addrs: []netip.Prefix{pfx("10.6.0.2/24")}})
	return tbl
}

func TestWindowsKeyingSeveralRoutesPerPrefix(t *testing.T) {
	tbl := windowsTable()
	viaEthernet := osnet.Route{Dst: pfx("0.0.0.0/0"), Gateway: ip("192.168.1.1"), Static: true, Metric: 0}
	viaWiFi := osnet.Route{Dst: pfx("0.0.0.0/0"), Gateway: ip("192.168.50.1"), Static: true, Metric: 0}
	for _, r := range []osnet.Route{viaEthernet, viaWiFi} {
		if err := tbl.Add(r); err != nil {
			t.Fatalf("Add %+v: %v", r, err)
		}
	}
	routes, err := tbl.Dump()
	if err != nil {
		t.Fatal(err)
	}
	var defaults []osnet.Route
	for _, r := range routes {
		if r.Dst == pfx("0.0.0.0/0") {
			defaults = append(defaults, r)
		}
	}
	if len(defaults) != 2 || defaults[0].Iface != "Ethernet" || defaults[0].IfIndex != 2 || defaults[1].Iface != "Wi-Fi" || defaults[1].IfIndex != 23 {
		t.Fatalf("both defaults must be in the table with their interfaces: %+v", defaults)
	}

	// The key is destination, interface and next hop; the metric does not make
	// another key.
	slower := viaEthernet
	slower.Metric = 50
	if err := tbl.Add(slower); !errors.Is(err, osnet.ErrExists) {
		t.Errorf("same key with another metric: %v, want ErrExists", err)
	}
	if err := tbl.Add(viaEthernet); !errors.Is(err, osnet.ErrExists) {
		t.Errorf("same route twice: %v, want ErrExists", err)
	}
	// An interface-bound route and a gateway route to one prefix coexist.
	if err := tbl.Add(osnet.Route{Dst: pfx("0.0.0.0/0"), Iface: "Plaitway", Static: true}); err != nil {
		t.Errorf("on-link default through the tunnel: %v", err)
	}
}

func TestWindowsKeyingDelete(t *testing.T) {
	tbl := windowsTable()
	a := osnet.Route{Dst: pfx("203.0.113.0/24"), Gateway: ip("192.168.1.1"), Iface: "Ethernet", Static: true}
	b := osnet.Route{Dst: pfx("203.0.113.0/24"), Gateway: ip("192.168.50.1"), Iface: "Wi-Fi", Static: true}
	for _, r := range []osnet.Route{a, b} {
		if err := tbl.Add(r); err != nil {
			t.Fatal(err)
		}
	}
	// Without an interface, two matches are ambiguous: the table must not guess.
	if err := tbl.Delete(osnet.Route{Dst: a.Dst}); err == nil || errors.Is(err, osnet.ErrNotFound) {
		t.Errorf("ambiguous delete: %v", err)
	}
	// The gateway singles one out.
	if err := tbl.Delete(osnet.Route{Dst: a.Dst, Gateway: ip("192.168.50.1")}); err != nil {
		t.Fatal(err)
	}
	if got, ok := tbl.Get(a.Dst); !ok || got.Iface != "Ethernet" {
		t.Fatalf("the wrong route is left: %+v", got)
	}
	// The interface index names the interface just as well as its name.
	if err := tbl.Delete(osnet.Route{Dst: a.Dst, Gateway: a.Gateway, IfIndex: 2}); err != nil {
		t.Fatal(err)
	}
	if err := tbl.Delete(a); !errors.Is(err, osnet.ErrNotFound) {
		t.Errorf("second delete: %v, want ErrNotFound", err)
	}
	// Another interface is another key, even for the same prefix and gateway.
	if err := tbl.Add(a); err != nil {
		t.Fatal(err)
	}
	wrong := a
	wrong.Iface = "Wi-Fi"
	if err := tbl.Delete(wrong); !errors.Is(err, osnet.ErrNotFound) {
		t.Errorf("delete through the wrong interface: %v, want ErrNotFound", err)
	}
}

func TestWindowsKeyingPrefersTheLowestEffectiveMetric(t *testing.T) {
	tbl := windowsTable()
	ethernet := osnet.Route{Dst: pfx("0.0.0.0/0"), Gateway: ip("192.168.1.1"), Iface: "Ethernet", Static: true, Metric: 0}
	wifi := osnet.Route{Dst: pfx("0.0.0.0/0"), Gateway: ip("192.168.50.1"), Iface: "Wi-Fi", Static: true, Metric: 0}
	for _, r := range []osnet.Route{ethernet, wifi} {
		if err := tbl.Add(r); err != nil {
			t.Fatal(err)
		}
	}
	tbl.SetInterfaceMetric("Ethernet", 25)
	tbl.SetInterfaceMetric("Wi-Fi", 40)
	if best, _ := tbl.Lookup(ip("8.8.8.8")); best.Iface != "Ethernet" {
		t.Errorf("lookup went through %s, want Ethernet", best.Iface)
	}
	tbl.SetInterfaceMetric("Ethernet", 100)
	if best, _ := tbl.Lookup(ip("8.8.8.8")); best.Iface != "Wi-Fi" {
		t.Errorf("after the interface metric moved: %s, want Wi-Fi", best.Iface)
	}
	if got, _ := tbl.Get(pfx("0.0.0.0/0")); got.Iface != "Wi-Fi" {
		t.Errorf("Get returned %s, want Wi-Fi", got.Iface)
	}
	// A longer prefix beats any metric.
	if err := tbl.Add(osnet.Route{Dst: pfx("8.8.8.0/24"), Gateway: ip("192.168.1.1"), Iface: "Ethernet", Static: true, Metric: 9999}); err != nil {
		t.Fatal(err)
	}
	if best, _ := tbl.Lookup(ip("8.8.8.8")); best.Iface != "Ethernet" || best.Dst != pfx("8.8.8.0/24") {
		t.Errorf("longest prefix lost: %+v", best)
	}
	if !tbl.Remove(pfx("0.0.0.0/0")) {
		t.Fatal("Remove found no default")
	}
	if got, _ := tbl.Get(pfx("0.0.0.0/0")); got.Iface != "Ethernet" {
		t.Errorf("Remove took out %s first; it must take the preferred route (Wi-Fi)", got.Iface)
	}
}

func TestWindowsKeyingBlackholeAndConnectedRoutes(t *testing.T) {
	tbl := windowsTable()
	if err := tbl.Add(osnet.Route{Dst: pfx("2001:db8::/32"), Blackhole: true, Static: true}); err != nil {
		t.Fatal(err)
	}
	black, ok := tbl.Get(pfx("2001:db8::/32"))
	if !ok || !black.Blackhole || black.Iface != fake.WindowsLoopback || black.IfIndex != 1 {
		t.Errorf("blackhole = %+v, %v", black, ok)
	}
	connected, ok := tbl.Get(pfx("192.168.1.0/24"))
	if !ok || connected.Iface != "Ethernet" || connected.IfIndex != 2 || connected.Static {
		t.Errorf("connected route = %+v, %v", connected, ok)
	}
	tbl.SetAddrs("Ethernet", []netip.Prefix{pfx("10.1.1.5/24")})
	if _, ok := tbl.Get(pfx("192.168.1.0/24")); ok {
		t.Error("the connected route outlived its address")
	}
	if _, ok := tbl.Get(pfx("10.1.1.0/24")); !ok {
		t.Error("the new connected route is missing")
	}
	tbl.DestroyInterface("Ethernet")
	if _, ok := tbl.Get(pfx("10.1.1.0/24")); ok {
		t.Error("a route survived its interface")
	}
}

func TestWindowsKeyingNeedsAnEmptyTable(t *testing.T) {
	tbl := wifiTable()
	defer func() {
		if recover() == nil {
			t.Error("WindowsKeying on a table with routes did not panic")
		}
	}()
	tbl.WindowsKeying()
}

func TestWindowsHostReportsTheDefaultOfLowestMetric(t *testing.T) {
	h := fake.NewWindowsHost()
	h.AddPhysical("Ethernet", pfx("192.168.1.20/24"), ip("192.168.1.1"))
	h.AddPhysical("Wi-Fi", pfx("192.168.50.20/24"), ip("192.168.50.1"))
	h.Routes.SetInterfaceMetric("Ethernet", 25)
	h.Routes.SetInterfaceMetric("Wi-Fi", 40)
	h.Sync()
	state, err := h.Net.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if state.DefaultV4 == nil || state.DefaultV4.Iface != "Ethernet" {
		t.Fatalf("DefaultV4 = %+v, want the Ethernet gateway", state.DefaultV4)
	}
	h.Routes.SetInterfaceMetric("Ethernet", 90)
	h.Sync()
	if state, _ = h.Net.Snapshot(); state.DefaultV4 == nil || state.DefaultV4.Iface != "Wi-Fi" {
		t.Fatalf("DefaultV4 = %+v, want the Wi-Fi gateway after the metric moved", state.DefaultV4)
	}
}

func TestWindowsInterfaceMetricIsPartOfTheInterfaceAndTheNetState(t *testing.T) {
	h := fake.NewWindowsHost()
	h.Routes.AddInterface(osnet.Interface{Name: "Wi-Fi", Index: 23, Up: true, Addrs: []netip.Prefix{pfx("192.168.50.20/24")}, Metric: 40})
	h.AddPhysical("Ethernet", pfx("192.168.1.20/24"), ip("192.168.1.1"))

	metrics := func() map[string]uint32 {
		state, err := h.Net.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		out := make(map[string]uint32)
		for _, ifc := range state.Interfaces {
			out[ifc.Name] = ifc.Metric
		}
		return out
	}

	// A metric the interface was created with takes part in the comparison of routes
	// and is reported; one that was not given is unknown (zero).
	h.Sync()
	if got := metrics(); got["Wi-Fi"] != 40 || got["Ethernet"] != 0 {
		t.Errorf("metrics %v, want Wi-Fi 40 and Ethernet unknown", got)
	}
	wifi := osnet.Route{Dst: pfx("203.0.113.0/24"), Gateway: ip("192.168.50.1"), Iface: "Wi-Fi", Static: true}
	if got := h.Routes.EffectiveMetric(wifi); got != 40 {
		t.Errorf("effective metric %d, want 40", got)
	}

	h.Routes.SetInterfaceMetric("Ethernet", 25)
	if got := metrics(); got["Ethernet"] != 0 {
		t.Errorf("the NetState changed before the host synced: %v", got)
	}
	h.Sync()
	if got := metrics(); got["Ethernet"] != 25 {
		t.Errorf("metrics %v, want Ethernet 25 after the sync", got)
	}
}
