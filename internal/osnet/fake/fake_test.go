package fake_test

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/osnet/fake"
)

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }
func ip(s string) netip.Addr    { return netip.MustParseAddr(s) }

func wifiTable() *fake.RouteTable {
	t := fake.NewRouteTable()
	t.AddInterface(osnet.Interface{Name: "en0", Up: true, Addrs: []netip.Prefix{pfx("192.168.51.185/24")}})
	t.AddInterface(osnet.Interface{Name: "utun10", Up: true, Tunnel: true, Addrs: []netip.Prefix{pfx("10.6.0.2/24")}})
	return t
}

func TestRouteTableKeyAndErrors(t *testing.T) {
	tbl := wifiTable()
	rt := osnet.Route{Dst: pfx("10.0.0.0/8"), Iface: "utun10", Static: true}
	if err := tbl.Add(rt); err != nil {
		t.Fatal(err)
	}
	if err := tbl.Add(rt); !errors.Is(err, osnet.ErrExists) {
		t.Errorf("adding twice: got %v, want ErrExists", err)
	}
	// Another gateway or interface does not make a different key: there is no metric.
	other := osnet.Route{Dst: pfx("10.0.0.0/8"), Gateway: ip("192.168.51.1"), Static: true}
	if err := tbl.Add(other); !errors.Is(err, osnet.ErrExists) {
		t.Errorf("same destination through another gateway: got %v, want ErrExists", err)
	}
	// A scoped route is another key.
	scoped := osnet.Route{Dst: pfx("10.0.0.0/8"), Iface: "en0", Scoped: true, Static: true}
	if err := tbl.Add(scoped); err != nil {
		t.Errorf("scoped route with the same destination: %v", err)
	}
	if err := tbl.Delete(osnet.Route{Dst: pfx("172.16.0.0/12")}); !errors.Is(err, osnet.ErrNotFound) {
		t.Errorf("deleting a missing route: got %v, want ErrNotFound", err)
	}
	// Delete compares the destination, not the gateway: the wrong gateway still deletes it.
	if err := tbl.Delete(osnet.Route{Dst: pfx("10.0.0.0/8"), Gateway: ip("1.2.3.4")}); err != nil {
		t.Fatal(err)
	}
	if _, ok := tbl.Get(pfx("10.0.0.0/8")); ok {
		t.Error("route still there after delete")
	}
	if _, ok := tbl.Lookup(ip("10.1.1.1")); ok {
		t.Error("the scoped route must not be used by an ordinary lookup")
	}
}

func TestRouteTableResolvesInterfaces(t *testing.T) {
	tbl := wifiTable()
	tests := []struct {
		name string
		rt   osnet.Route
		want error
	}{
		{"gateway on a connected subnet", osnet.Route{Dst: pfx("1.1.1.1/32"), Gateway: ip("192.168.51.1"), Iface: "en0"}, nil},
		{"gateway found without naming the interface", osnet.Route{Dst: pfx("1.1.1.2/32"), Gateway: ip("192.168.51.1")}, nil},
		{"gateway on no subnet", osnet.Route{Dst: pfx("1.1.1.3/32"), Gateway: ip("10.9.9.9"), Iface: "en0"}, osnet.ErrUnreachable},
		{"gateway on the wrong interface", osnet.Route{Dst: pfx("1.1.1.4/32"), Gateway: ip("192.168.51.1"), Iface: "utun10"}, osnet.ErrUnreachable},
		{"interface that does not exist", osnet.Route{Dst: pfx("1.1.1.5/32"), Iface: "utun99"}, osnet.ErrUnreachable},
		{"bound to an existing interface", osnet.Route{Dst: pfx("1.1.1.6/32"), Iface: "utun10"}, nil},
		{"no gateway and no interface", osnet.Route{Dst: pfx("1.1.1.7/32")}, osnet.ErrUnreachable},
		{"blackhole needs nothing", osnet.Route{Dst: pfx("1.1.1.8/32"), Blackhole: true}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tbl.Add(tt.rt); !errors.Is(err, tt.want) {
				t.Errorf("got %v, want %v", err, tt.want)
			}
		})
	}
	r, _ := tbl.Get(pfx("1.1.1.2/32"))
	if r.Iface != "en0" || r.Flags == 0 {
		t.Errorf("a resolved route should carry its interface and flags: %+v", r)
	}
}

// How stale routes are born: the interface keeps living, only its address
// changes, and the gateway route stays behind.
func TestGatewayRouteSurvivesAnAddressChange(t *testing.T) {
	tbl := wifiTable()
	bypass := osnet.Route{Dst: pfx("203.0.113.10/32"), Gateway: ip("192.168.51.1"), Iface: "en0", Static: true}
	bound := osnet.Route{Dst: pfx("10.0.0.0/8"), Iface: "utun10", Static: true}
	for _, r := range []osnet.Route{bypass, bound} {
		if err := tbl.Add(r); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := tbl.Get(pfx("192.168.51.0/24")); !ok {
		t.Fatal("the connected route of en0 is missing")
	}

	tbl.SetAddrs("en0", []netip.Prefix{pfx("10.20.30.7/24")})

	if _, ok := tbl.Get(pfx("192.168.51.0/24")); ok {
		t.Error("the old connected route should be gone")
	}
	if _, ok := tbl.Get(pfx("10.20.30.0/24")); !ok {
		t.Error("the new connected route is missing")
	}
	got, ok := tbl.Get(bypass.Dst)
	if !ok || got.Gateway != bypass.Gateway {
		t.Errorf("the gateway route must survive an address change, got %+v, %v", got, ok)
	}
	// A new route through the old gateway is refused: that is what makes the
	// leftover stale.
	if err := tbl.Add(osnet.Route{Dst: pfx("203.0.113.11/32"), Gateway: ip("192.168.51.1")}); !errors.Is(err, osnet.ErrUnreachable) {
		t.Errorf("old gateway after the address change: got %v, want ErrUnreachable", err)
	}
}

func TestInterfaceBoundRoutesVanishWithTheInterface(t *testing.T) {
	for _, tc := range []struct {
		name string
		kill func(*fake.RouteTable)
	}{
		{"destroy", func(tbl *fake.RouteTable) { tbl.DestroyInterface("utun10") }},
		{"down", func(tbl *fake.RouteTable) { tbl.SetUp("utun10", false) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tbl := wifiTable()
			for _, r := range []osnet.Route{
				{Dst: pfx("10.0.0.0/8"), Iface: "utun10", Static: true},
				{Dst: pfx("0.0.0.0/1"), Iface: "utun10", Static: true},
				{Dst: pfx("203.0.113.10/32"), Gateway: ip("192.168.51.1"), Iface: "en0", Static: true},
			} {
				if err := tbl.Add(r); err != nil {
					t.Fatal(err)
				}
			}
			tc.kill(tbl)
			for _, dst := range []string{"10.0.0.0/8", "0.0.0.0/1", "10.6.0.0/24"} {
				if _, ok := tbl.Get(pfx(dst)); ok {
					t.Errorf("%s survived", dst)
				}
			}
			if _, ok := tbl.Get(pfx("203.0.113.10/32")); !ok {
				t.Error("a route through another interface must stay")
			}
			if err := tbl.Add(osnet.Route{Dst: pfx("10.0.0.0/8"), Iface: "utun10"}); !errors.Is(err, osnet.ErrUnreachable) {
				t.Errorf("adding through the dead interface: got %v, want ErrUnreachable", err)
			}
		})
	}
}

func TestRouteTableLongestPrefixWins(t *testing.T) {
	tbl := wifiTable()
	for _, r := range []osnet.Route{
		{Dst: pfx("0.0.0.0/1"), Iface: "utun10"},
		{Dst: pfx("10.0.0.0/8"), Iface: "en0"},
		{Dst: pfx("10.1.0.0/16"), Iface: "utun10"},
	} {
		if err := tbl.Add(r); err != nil {
			t.Fatal(err)
		}
	}
	for addr, wantDst := range map[string]string{
		"10.1.2.3":     "10.1.0.0/16",
		"10.2.2.3":     "10.0.0.0/8",
		"1.2.3.4":      "0.0.0.0/1",
		"192.168.51.9": "192.168.51.0/24",
	} {
		got, ok := tbl.Lookup(ip(addr))
		if !ok || got.Dst != pfx(wantDst) {
			t.Errorf("%s: got %v %v, want %s", addr, got.Dst, ok, wantDst)
		}
	}
	if _, ok := tbl.Lookup(ip("200.1.1.1")); ok {
		t.Error("an address no route covers must not match")
	}
}

func TestRouteTableFaultsAndLog(t *testing.T) {
	tbl := wifiTable()
	boom := errors.New("boom")
	tbl.InjectFault(fake.Fault{Op: fake.OpAdd, Dst: pfx("10.0.0.0/8"), Err: osnet.ErrUnreachable})
	tbl.InjectFault(fake.Fault{Op: fake.OpDump, Err: boom, Times: 2})

	if err := tbl.Add(osnet.Route{Dst: pfx("172.16.0.0/12"), Iface: "utun10"}); err != nil {
		t.Errorf("a fault for another destination fired: %v", err)
	}
	rt := osnet.Route{Dst: pfx("10.0.0.0/8"), Iface: "utun10"}
	if err := tbl.Add(rt); !errors.Is(err, osnet.ErrUnreachable) {
		t.Errorf("first add: got %v, want the injected ErrUnreachable", err)
	}
	if err := tbl.Add(rt); err != nil {
		t.Errorf("the fault should fire once, then the add works: %v", err)
	}
	for range 2 {
		if _, err := tbl.Dump(); !errors.Is(err, boom) {
			t.Errorf("dump: got %v, want boom", err)
		}
	}
	if _, err := tbl.Dump(); err != nil {
		t.Errorf("dump after the faults ran out: %v", err)
	}

	tbl.Mark("here")
	if err := tbl.Delete(osnet.Route{Dst: pfx("10.0.0.0/8")}); err != nil {
		t.Fatal(err)
	}
	var kinds []fake.OpKind
	for _, op := range tbl.Ops() {
		kinds = append(kinds, op.Kind)
	}
	want := []fake.OpKind{fake.OpAdd, fake.OpAdd, fake.OpAdd, fake.OpMark, fake.OpDelete}
	if !slices.Equal(kinds, want) {
		t.Errorf("ops: got %v, want %v", kinds, want)
	}
	ops := tbl.Ops()
	if ops[1].Err == nil || ops[4].Removed.Dst != pfx("10.0.0.0/8") {
		t.Errorf("the log should keep the failure and what a delete removed: %+v", ops)
	}
}

func TestRouteTableDumpIsSortedAndCopied(t *testing.T) {
	tbl := wifiTable()
	tbl.Inject(osnet.Route{Dst: pfx("2001:db8::/32"), Iface: "en0"})
	tbl.Inject(osnet.Route{Dst: pfx("9.0.0.0/8"), Iface: "en0"})
	tbl.Inject(osnet.Route{Dst: pfx("1.0.0.0/8"), Iface: "en0"})
	routes, err := tbl.Dump()
	if err != nil {
		t.Fatal(err)
	}
	var dsts []netip.Prefix
	for _, r := range routes {
		dsts = append(dsts, r.Dst)
	}
	if !slices.IsSortedFunc(dsts, func(a, b netip.Prefix) int {
		if a.Addr().Is4() != b.Addr().Is4() {
			if a.Addr().Is4() {
				return -1
			}
			return 1
		}
		return a.Addr().Compare(b.Addr())
	}) {
		t.Errorf("dump is not sorted: %v", dsts)
	}
	routes[0].Iface = "changed"
	again, _ := tbl.Dump()
	if again[0].Iface == "changed" {
		t.Error("Dump must return a copy")
	}
}

func TestRouteTableIsRaceSafe(t *testing.T) {
	tbl := wifiTable()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dst := netip.PrefixFrom(netip.AddrFrom4([4]byte{10, byte(i), 0, 0}), 16)
			for range 200 {
				tbl.Add(osnet.Route{Dst: dst, Iface: "utun10"})
				tbl.Dump()
				tbl.Lookup(dst.Addr())
				tbl.Delete(osnet.Route{Dst: dst})
				tbl.SetAddrs("en0", []netip.Prefix{pfx("192.168.51.185/24")})
			}
		}()
	}
	wg.Wait()
}

func TestDNS(t *testing.T) {
	d := fake.NewDNS()
	entry := osnet.DNSEntry{Servers: []netip.Addr{ip("10.0.0.53")}, MatchDomains: []string{"corp.lan"}}

	if err := d.Apply("a", []osnet.DNSEntry{entry}); err != nil {
		t.Fatal(err)
	}
	d.Leave("orphan", entry)
	if keys, _ := d.Owned(); !slices.Equal(keys, []string{"a", "orphan"}) {
		t.Errorf("Owned: %v", keys)
	}
	got := d.Entries("a")
	got[0].Servers[0] = ip("1.1.1.1")
	if d.Entries("a")[0].Servers[0] != ip("10.0.0.53") {
		t.Error("Entries must return a copy")
	}

	if err := d.Apply("a", nil); err != nil {
		t.Fatal(err)
	}
	if keys, _ := d.Owned(); !slices.Equal(keys, []string{"orphan"}) {
		t.Errorf("an empty Apply is a Remove: %v", keys)
	}
	if err := d.Remove("nobody"); err != nil {
		t.Errorf("removing nothing is not an error: %v", err)
	}
	if err := d.Remove("orphan"); err != nil {
		t.Fatal(err)
	}

	boom := errors.New("boom")
	d.Fail("apply", boom, 2)
	for range 2 {
		if err := d.Apply("a", []osnet.DNSEntry{entry}); !errors.Is(err, boom) {
			t.Errorf("got %v, want boom", err)
		}
	}
	if err := d.Apply("a", []osnet.DNSEntry{entry}); err != nil {
		t.Errorf("after the faults ran out: %v", err)
	}
	if err := d.Flush(); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, op := range d.Ops() {
		kinds = append(kinds, op.Kind)
	}
	if want := []string{"apply", "apply", "remove", "remove", "apply", "flush"}; !slices.Equal(kinds, want) {
		t.Errorf("ops: got %v, want %v", kinds, want)
	}
}

func TestNetMonitorEpochAndEvents(t *testing.T) {
	m := fake.NewNetMonitor()
	ns := osnet.NetState{
		Interfaces: []osnet.Interface{{Name: "en0", Up: true, Addrs: []netip.Prefix{pfx("192.168.51.185/24")}}},
		DefaultV4:  &osnet.Nexthop{Gateway: ip("192.168.51.1"), Iface: "en0"},
	}
	m.Set(ns)
	first, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	m.Set(ns)
	same, _ := m.Snapshot()
	if same.Epoch != first.Epoch {
		t.Errorf("an unchanged state must keep its epoch: %d then %d", first.Epoch, same.Epoch)
	}
	ns.DefaultV4 = &osnet.Nexthop{Gateway: ip("192.168.51.2"), Iface: "en0"}
	m.Set(ns)
	next, _ := m.Snapshot()
	if next.Epoch != first.Epoch+1 {
		t.Errorf("a changed state advances the epoch once: %d then %d", first.Epoch, next.Epoch)
	}
	next.DefaultV4.Gateway = ip("9.9.9.9")
	if again, _ := m.Snapshot(); again.DefaultV4.Gateway == ip("9.9.9.9") {
		t.Error("Snapshot must return a copy")
	}

	ctx, cancel := context.WithCancel(context.Background())
	events := m.Events(ctx)
	m.Emit(osnet.Change{Reason: osnet.ChangeWake})
	select {
	case c := <-events:
		if c.Reason != osnet.ChangeWake || c.At.IsZero() {
			t.Errorf("event: %+v", c)
		}
	case <-time.After(time.Second):
		t.Fatal("no event")
	}
	boom := errors.New("boom")
	m.FailSnapshot(boom)
	if _, err := m.Snapshot(); !errors.Is(err, boom) {
		t.Errorf("got %v, want boom", err)
	}
	m.FailSnapshot(nil)
	cancel()
	select {
	case _, ok := <-events:
		if ok {
			t.Error("unexpected event after cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("the channel did not close when the context ended")
	}
	m.Emit(osnet.Change{Reason: osnet.ChangeRoute}) // must not panic with no subscriber left
}

func TestHostDerivesTheNetState(t *testing.T) {
	h := fake.NewHost()
	h.AddPhysical("en0", pfx("192.168.51.185/24"), ip("192.168.51.1"))
	h.AddTunnel("utun10", pfx("10.6.0.2/24"))
	if err := h.Routes.Add(osnet.Route{Dst: pfx("0.0.0.0/1"), Iface: "utun10", Static: true}); err != nil {
		t.Fatal(err)
	}
	h.Sync()

	ns, _ := h.Net.Snapshot()
	if ns.DefaultV4 == nil || ns.DefaultV4.Gateway != ip("192.168.51.1") || ns.DefaultV4.Iface != "en0" {
		t.Errorf("default: %+v", ns.DefaultV4)
	}
	if !slices.Equal(ns.Connected, []netip.Prefix{pfx("192.168.51.0/24")}) {
		t.Errorf("connected must list physical subnets only: %v", ns.Connected)
	}
	if len(ns.Interfaces) != 2 {
		t.Errorf("interfaces: %+v", ns.Interfaces)
	}

	bypass := osnet.Route{Dst: pfx("203.0.113.10/32"), Gateway: ip("192.168.51.1"), Iface: "en0", Static: true}
	if err := h.Routes.Add(bypass); err != nil {
		t.Fatal(err)
	}
	h.MoveNetwork("en0", pfx("10.20.30.7/24"), ip("10.20.30.1"))
	ns, _ = h.Net.Snapshot()
	if ns.DefaultV4.Gateway != ip("10.20.30.1") || !slices.Equal(ns.Connected, []netip.Prefix{pfx("10.20.30.0/24")}) {
		t.Errorf("after moving: %+v", ns)
	}
	if got, _ := h.Routes.Get(bypass.Dst); got.Gateway != ip("192.168.51.1") {
		t.Error("the leftover gateway route must still point at the old router")
	}
	if def, _ := h.Routes.Get(pfx("0.0.0.0/0")); def.Gateway != ip("10.20.30.1") {
		t.Errorf("the system default route should follow: %+v", def)
	}

	h.AddPhysical("en1", pfx("172.16.0.5/24"), netip.Addr{})
	h.DestroyInterface("en1")
	if ns, _ = h.Net.Snapshot(); len(ns.Interfaces) != 2 {
		t.Errorf("interfaces after destroying one: %+v", ns.Interfaces)
	}
}

func TestHostCarriesTheLinkKindOfTheDefault(t *testing.T) {
	h := fake.NewHost()
	h.AddPhysical("en0", pfx("192.168.51.185/24"), ip("192.168.51.1"))
	h.AddPhysical("en6", pfx("10.1.2.3/24"), netip.Addr{}) // no default route through it
	kindOf := func() osnet.LinkKind {
		t.Helper()
		ns, _ := h.Net.Snapshot()
		if ns.DefaultV4 == nil || ns.DefaultV4.Iface != "en0" {
			t.Fatalf("default: %+v", ns.DefaultV4)
		}
		return ns.DefaultV4.Kind
	}
	if got := kindOf(); got != osnet.LinkOther {
		t.Errorf("an interface nobody classified is %v, want LinkOther", got)
	}

	before, _ := h.Net.Snapshot()
	h.SetLinkKind("en0", osnet.LinkWiFi)
	if got := kindOf(); got != osnet.LinkWiFi {
		t.Errorf("kind after SetLinkKind = %v, want LinkWiFi", got)
	}
	if after, _ := h.Net.Snapshot(); after.Epoch != before.Epoch+1 {
		t.Errorf("a changed kind advances the epoch once: %d then %d", before.Epoch, after.Epoch)
	}

	// The kind of another interface is not the default's.
	h.SetLinkKind("en6", osnet.LinkEthernet)
	if got := kindOf(); got != osnet.LinkWiFi {
		t.Errorf("kind after classifying en6 = %v, want LinkWiFi", got)
	}

	// The kind belongs to the interface and survives the helpers that move it.
	h.MoveNetwork("en0", pfx("10.20.30.7/24"), ip("10.20.30.1"))
	if got := kindOf(); got != osnet.LinkWiFi {
		t.Errorf("kind after MoveNetwork = %v, want LinkWiFi", got)
	}
}

func TestHostTakesTheKindToTheNextDefault(t *testing.T) {
	h := fake.NewHost()
	h.SetLinkKind("en6", osnet.LinkEthernet) // before the adapter exists
	h.SetLinkKind("en0", osnet.LinkWiFi)
	h.AddPhysical("en0", pfx("192.168.51.185/24"), ip("192.168.51.1"))

	// Wi-Fi goes, the Ethernet adapter comes up: the primary network changes kind.
	h.DestroyInterface("en0")
	h.AddPhysical("en6", pfx("192.168.51.186/24"), ip("192.168.51.1"))
	ns, _ := h.Net.Snapshot()
	if ns.DefaultV4 == nil || ns.DefaultV4.Iface != "en6" || ns.DefaultV4.Kind != osnet.LinkEthernet {
		t.Errorf("default after the switch: %+v", ns.DefaultV4)
	}

	// IPv6 default through the same interface carries the same kind.
	if err := h.Routes.Add(osnet.Route{Dst: pfx("::/0"), Gateway: ip("fe80::1"), Iface: "en6", Static: true}); err != nil {
		t.Fatal(err)
	}
	h.Sync()
	ns, _ = h.Net.Snapshot()
	if ns.DefaultV6 == nil || ns.DefaultV6.Kind != osnet.LinkEthernet {
		t.Errorf("IPv6 default: %+v", ns.DefaultV6)
	}

	// No default, no kind to report.
	h.Routes.Remove(pfx("0.0.0.0/0"))
	h.Routes.Remove(pfx("::/0"))
	h.Sync()
	if ns, _ = h.Net.Snapshot(); ns.DefaultV4 != nil || ns.DefaultV6 != nil {
		t.Errorf("defaults after removing them: %+v %+v", ns.DefaultV4, ns.DefaultV6)
	}
}
