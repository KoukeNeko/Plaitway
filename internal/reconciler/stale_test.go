package reconciler

import (
	"context"
	"net/netip"
	"slices"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/osnet/fake"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

func TestFindStale(t *testing.T) {
	ns := osnet.NetState{
		Interfaces: []osnet.Interface{
			{Name: "en0", Up: true, Addrs: pfxs("192.168.51.185/24", "fd00::5/64")},
			{Name: "en1", Up: false, Addrs: pfxs("172.16.0.5/24")},
			// A point-to-point tunnel lists only its own address; OpenVPN's net30
			// topology is "10.8.0.6 --> 10.8.0.5", WireGuard's is a lone /32.
			{Name: "utun11", Up: true, Tunnel: true, Addrs: pfxs("10.8.0.6/32")},
			{Name: "utun12", Up: false, Tunnel: true, Addrs: pfxs("10.9.0.6/32")},
		},
		DefaultV4: &osnet.Nexthop{Gateway: ip("192.168.51.1"), Iface: "en0"},
		DefaultV6: &osnet.Nexthop{Gateway: ip("fe80::1"), Iface: "en0"},
	}
	host := func(dst, gw, iface string) osnet.Route {
		r := osnet.Route{Dst: pfx(dst), Iface: iface, Static: true}
		if gw != "" {
			r.Gateway = ip(gw)
		}
		return r
	}
	tests := []struct {
		name       string
		route      osnet.Route
		isEndpoint bool
		want       string // the reason, empty when the route is fine
	}{
		{"healthy", host("8.8.8.8/32", "192.168.51.1", "en0"), false, ""},
		{"gateway outside the subnet of its interface", host("8.8.8.8/32", "10.99.0.1", "en0"), false, "gateway is not on the subnet of en0"},
		{"interface is gone", host("8.8.8.8/32", "192.168.51.1", "en9"), false, "interface en9 is gone"},
		{"interface is down", host("8.8.8.8/32", "172.16.0.1", "en1"), false, "interface en1 is down"},
		{"no interface named, gateway on no subnet", host("8.8.8.8/32", "10.99.0.1", ""), false, "gateway is not on any connected subnet"},
		{"no interface named, gateway on a subnet", host("8.8.8.8/32", "192.168.51.1", ""), false, ""},
		{"IPv6 link-local gateways are always on link", host("2001:db8::1/128", "fe80::abcd", "en0"), false, ""},
		{"IPv6 global gateway on the subnet", host("2001:db8::1/128", "fd00::1", "en0"), false, ""},
		{"a foreign route may use another router", host("8.8.8.8/32", "192.168.51.254", "en0"), false, ""},
		{"the peer of a point-to-point tunnel is not on a subnet it lists", host("10.8.0.1/32", "10.8.0.5", "utun11"), false, ""},
		{"a tunnel that is down is still stale", host("10.9.0.1/32", "10.9.0.5", "utun12"), false, "interface utun12 is down"},
		{"a tunnel that is gone is still stale", host("10.7.0.1/32", "10.7.0.5", "utun9"), false, "interface utun9 is gone"},
		{"an endpoint's route must use the default nexthop", host("203.0.113.10/32", "192.168.51.254", "en0"), true, "gateway is not the default route's"},
		{"an endpoint's route through the default nexthop", host("203.0.113.10/32", "192.168.51.1", "en0"), true, ""},
		{"an IPv6 endpoint is held to the IPv6 default", host("2001:db8::10/128", "fd00::99", "en0"), true, "gateway is not the default route's"},
		// What is not a host route through a gateway is not looked at.
		{"net route", host("10.0.0.0/8", "10.99.0.1", "en0"), false, ""},
		{"interface-bound route", host("8.8.8.8/32", "", "en9"), false, ""},
		{"blackhole", osnet.Route{Dst: pfx("8.8.8.8/32"), Gateway: ip("10.99.0.1"), Iface: "en0", Static: true, Blackhole: true}, false, ""},
		{"scoped route", osnet.Route{Dst: pfx("8.8.8.8/32"), Gateway: ip("10.99.0.1"), Iface: "en0", Static: true, Scoped: true}, false, ""},
		{"not static", osnet.Route{Dst: pfx("8.8.8.8/32"), Gateway: ip("10.99.0.1"), Iface: "en0"}, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			endpoints := map[netip.Prefix]bool{tt.route.Dst: tt.isEndpoint}
			got := findStale(map[netip.Prefix]osnet.Route{tt.route.Dst: tt.route}, ns, endpoints, func(netip.Prefix) bool { return false })
			switch {
			case tt.want == "" && len(got) != 0:
				t.Errorf("reported as stale: %+v", got)
			case tt.want != "" && (len(got) != 1 || got[0].Reason != tt.want || got[0].Key != tt.route.Dst.String()):
				t.Errorf("got %+v, want reason %q", got, tt.want)
			}
		})
	}

	t.Run("owned routes are marked and the list is sorted", func(t *testing.T) {
		table := map[netip.Prefix]osnet.Route{}
		for _, r := range []osnet.Route{host("9.9.9.9/32", "10.99.0.1", "en0"), host("1.1.1.1/32", "10.99.0.1", "en0")} {
			table[r.Dst] = r
		}
		got := findStale(table, ns, nil, func(p netip.Prefix) bool { return p == pfx("1.1.1.1/32") })
		if len(got) != 2 || got[0].Key != "1.1.1.1/32" || !got[0].Owned || got[1].Key != "9.9.9.9/32" || got[1].Owned {
			t.Errorf("got %+v", got)
		}
	})
}

// lyingTable accepts routes and loses them at once, as a system service that
// rewrites the table behind our back would.
type lyingTable struct{ *fake.RouteTable }

func (l lyingTable) Add(rt osnet.Route) error {
	if err := l.RouteTable.Add(rt); err != nil {
		return err
	}
	l.RouteTable.Remove(rt.Dst)
	return nil
}

func TestRouteThatDoesNotStayIsAFailure(t *testing.T) {
	e := newEnv(t)
	e.addTunnel("utun10", "10.6.0.2/24")
	e.r.routes = lyingTable{e.host.Routes}

	e.announce(wgIntent())

	if rr := e.routeReport("203.0.113.10/32", "wg"); rr.State != tunnel.RouteFailed || rr.Detail != "not in the routing table after adding" {
		t.Errorf("report: %+v", rr)
	}
	if e.r.isOwned(pfx("203.0.113.10/32")) {
		t.Error("a route that is not there is considered ours")
	}
	// Pending, then the add; the read-back finds nothing, so it is never applied.
	if got := e.journalFor(kindRoute, "203.0.113.10/32"); !slices.Equal(got, []string{"pending 192.168.51.1", "removed 192.168.51.1"}) {
		t.Errorf("journal: %v", got)
	}
	if e.r.failStreak != 1 {
		t.Errorf("a route that vanishes counts as not converging, failStreak %d", e.r.failStreak)
	}
}

// deadMonitor's event stream ends while nobody asked it to.
type deadMonitor struct{ osnet.NetMonitor }

func (deadMonitor) Events(context.Context) <-chan osnet.Change {
	ch := make(chan osnet.Change)
	close(ch)
	return ch
}

func TestRunEndsWhenTheEventStreamDies(t *testing.T) {
	e := newEnv(t)
	e.bothTunnels()
	e.announce(asusIntent())
	e.r.monitor = deadMonitor{e.host.Net}

	err := e.r.Run(context.Background())

	if err == nil {
		t.Error("Run should report that it lost its events")
	}
	e.checkTable()
	if owned, _ := e.host.DNS.Owned(); len(owned) != 0 {
		t.Errorf("resolver entries left: %v", owned)
	}
}

// If the exit cannot remove everything, Run says so and the journal keeps what
// is left for the next start.
func TestRunReportsWhatItCouldNotRemove(t *testing.T) {
	e := newEnv(t)
	e.bothTunnels()
	stop := e.run()
	e.announce(wgIntent())
	e.host.Routes.InjectFault(fake.Fault{Op: fake.OpDelete, Dst: pfx("203.0.113.10/32"), Err: errBoom})
	e.host.DNS.Fail("remove", errBoom, 2) // the removal and the sweep both fail

	if err := stop(); err == nil {
		t.Fatal("Run reported nothing")
	}
	if left := e.unresolved(); len(left) == 0 {
		t.Fatal("the journal must still list what is left")
	}

	e.newReconciler() // the next start finds the journal and the marker
	e.checkTable()
	if left := e.unresolved(); len(left) != 0 {
		t.Errorf("the next start left %+v", left)
	}
}

func TestSweepSurvivesAResolverThatCannotBeListed(t *testing.T) {
	e := newEnv(t)
	e.host.DNS.Leave("ghost", osnet.DNSEntry{Servers: ips("10.0.0.53"), MatchDomains: []string{"corp.lan"}})
	e.host.DNS.Fail("owned", errBoom, 1)

	e.crash() // the daemon that left the entry is gone

	r := e.newReconciler() // logs the failure, starts anyway

	if err := r.Resync(); err != nil {
		t.Errorf("a later pass sweeps it: %v", err)
	}
	if owned, _ := e.host.DNS.Owned(); len(owned) != 0 {
		t.Errorf("owned: %v", owned)
	}
}
