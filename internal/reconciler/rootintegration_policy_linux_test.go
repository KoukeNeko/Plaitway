//go:build rootintegration && linux

package reconciler

// The machine of the author: another VPN (Cloudflare WARP, a wg-quick tunnel)
// routes with policy rules instead of default routes in the main table. See
// rootlab_linux_test.go for how to run these.

import (
	"slices"
	"strings"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/osnet/linux"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// policyState is everything the other VPN owns, as ip(8) shows it.
func policyState(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, family := range []string{"-4", "-6"} {
		b.WriteString(runIP(t, family, "rule", "show"))
		b.WriteString(runIP(t, family, "route", "show", "table", "51820"))
	}
	return b.String()
}

// The main table of the machine has a default route through the physical
// router. The other VPN's tunnel is a tunnel for the network state, and never
// the default route.
func TestKernelNetworkStateWithAForeignTunnelThatRoutesByPolicy(t *testing.T) {
	l := newLab(t)
	l.warp("wireguard")
	l.tun("tun0", "10.9.0.2/24", "fd09::2/64")
	r := l.reconciler()
	announce(t, r, fullTunnelOfWG())

	for name, ns := range map[string]osnet.NetState{
		"Snapshot": mustSnapshot(t, linux.NewNetMonitor(linux.NetMonitorOptions{})),
		"Report":   r.Report().Net,
	} {
		wgcf, ok := findInterface(ns, "wgcf")
		if !ok || !wgcf.Tunnel || !wgcf.Up {
			t.Errorf("%s: the foreign tunnel is %+v (present %v), want a tunnel that is up", name, wgcf, ok)
		}
		if ens, _ := findInterface(ns, "ens18"); ens.Tunnel || !ens.Up {
			t.Errorf("%s: the uplink is %+v", name, ens)
		}
		if ns.DefaultV4 == nil || ns.DefaultV4.Iface != "ens18" || ns.DefaultV4.Gateway != ip(routerV4) {
			t.Errorf("%s: the IPv4 default route is %+v, want the router of the uplink", name, ns.DefaultV4)
		}
		if ns.DefaultV6 == nil || ns.DefaultV6.Iface != "ens18" || ns.DefaultV6.Gateway.WithZone("") != ip(routerV6) {
			t.Errorf("%s: the IPv6 default route is %+v, want the router of the uplink", name, ns.DefaultV6)
		}
		if want := pfxs("192.168.50.0/24", "2001:db8:50::/64"); !slices.Equal(ns.Connected, want) {
			t.Errorf("%s: connected subnets %v, want %v: the tunnels' addresses are no local network", name, ns.Connected, want)
		}
	}
	if !strings.Contains(policyState(t), "32765:\tnot from all fwmark 0xca6c lookup 51820") {
		t.Errorf("setup: the rules of the other VPN are not there:\n%s", policyState(t))
	}
}

func mustSnapshot(t *testing.T, m osnet.NetMonitor) osnet.NetState {
	t.Helper()
	ns, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return ns
}

// With the other VPN up, a packet to an address that no route of the main table
// names goes into the other VPN: the default route of the main table is
// suppressed. A full tunnel of ours has to keep its own traffic out of both, and
// its routes win against the other VPN's catch-all because they are not
// suppressed; the packets prove it.
func TestKernelAmongAForeignTunnelThatRoutesByPolicy(t *testing.T) {
	setup := func(t *testing.T) (*lab, *Reconciler) {
		l := newLab(t)
		l.warp("tun")
		l.tun("tun0", "10.9.0.2/24", "fd09::2/64")
		return l, l.reconciler()
	}

	t.Run("the layout without us", func(t *testing.T) {
		l, _ := setup(t)
		l.expectRoute("203.0.113.10", decision{Dev: "wgcf"})
		l.expectRoute("2001:db8:ee::10", decision{Dev: "wgcf"})
		l.expectRoute("203.0.113.10", decision{Dev: "ens18", Via: routerV4}, "mark", "0xca6c")
		l.expectRoute("10.6.0.5", decision{Dev: "ens18", Via: routerV4})
		l.expectProbe("203.0.113.10", "wgcf")
		l.expectProbe("2001:db8:ee::10", "wgcf")
		l.expectProbe("10.6.0.5", "uplink")
		if got := l.probeTCP("203.0.113.10"); got != "wgcf" {
			t.Errorf("a connection to an address of the internet went through %s, want the other VPN", got)
		}
	})

	t.Run("a full tunnel and its endpoint", func(t *testing.T) {
		l, r := setup(t)
		before := policyState(t)

		announce(t, r, fullTunnelOfWG())

		l.expectOurs(fullTunnelRoutes...)
		expectInstalled(t, r, "wg", "0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1", v4Endpoint+"/32", v6Endpoint+"/128")
		// The bypass route is a host route through the physical router: a prefix
		// length above zero is not suppressed.
		l.expectRoute(v4Endpoint, decision{Dev: "ens18", Via: routerV4})
		l.expectRoute(v6Endpoint, decision{Dev: "ens18", Via: routerV6})
		l.expectProbe(v4Endpoint, "uplink")
		l.expectProbe(v6Endpoint, "uplink")
		if got := l.probeTCP(v4Endpoint); got != "uplink" {
			t.Errorf("a connection to the endpoint went through %s, want the uplink", got)
		}
		// Everything else goes into our tunnel, not into the other VPN: the halves are
		// longer than the default route that the policy sends the rest to.
		l.expectProbe("8.8.8.8", "tun0")
		l.expectProbe("2606:4700::1111", "tun0")
		l.expectRoute("8.8.8.8", decision{Dev: "tun0"}, "mark", "0xca6c")
		// The static route of a home network is more specific than a half: kept.
		l.expectProbe("10.6.0.5", "uplink")
		if ns := r.Report().Net; ns.DefaultV4 == nil || ns.DefaultV4.Iface != "ens18" {
			t.Errorf("the default route is %+v", ns.DefaultV4)
		}

		// Without the bypass route the endpoint's packets go the way the halves send
		// them, into the tunnel they are the traffic of. The probe would see it.
		runIP(t, "route", "del", "203.0.113.10/32", "dev", uplinkName)
		runIP(t, "-6", "route", "del", v6Endpoint+"/128", "dev", uplinkName)
		l.expectProbe(v4Endpoint, "tun0")
		l.expectProbe(v6Endpoint, "tun0")

		// And with nothing of ours, the other VPN takes them. The probe sees that too.
		r.Withdraw("wg")
		l.expectOurs()
		l.expectProbe(v4Endpoint, "wgcf")
		l.expectProbe(v6Endpoint, "wgcf")
		l.expectProbe("8.8.8.8", "wgcf")

		// What is ours is gone, and what is the other VPN's is as it was.
		if after := policyState(t); after != before {
			t.Errorf("the rules and the table of the other VPN changed:\nbefore:\n%s\nafter:\n%s", before, after)
		}
		if rt, ok := l.route("10.6.0.0/24", 0); !ok || rt.Gateway != ip(routerV4) || protocol(rt) != 4 {
			t.Errorf("the static route of the main table: %+v (present %v)", rt, ok)
		}
	})

	t.Run("a split tunnel beside it", func(t *testing.T) {
		l, r := setup(t)
		l.tun("tun1", "10.8.0.6/24", "fd08::6/64")

		announce(t, r, endpoints(up("office", 1, "tun1", tunnel.RoleSplit, "10.50.0.0/16", "2001:db8:77::/48"), "198.51.100.7", "2001:db8:ee::77"))

		l.expectOurs("10.50.0.0/16 dev tun1 metric 5", "2001:db8:77::/48 dev tun1 metric 5")
		l.expectProbe("10.50.3.4", "tun1")
		l.expectProbe("2001:db8:77::5", "tun1")
		// What the split tunnel leaves alone stays the other VPN's.
		l.expectProbe("8.8.8.8", "wgcf")
		l.expectProbe("10.6.0.5", "uplink")
		// None of our routes captures the endpoint of the split tunnel, so it gets no
		// host route, and the packets of the tunnel to it follow the other VPN's
		// catch-all, which the Reconciler cannot see. They reach the server, through
		// the other VPN.
		l.expectProbe("198.51.100.7", "wgcf")
		if rr := reportOf(t, r, "10.50.0.0/16", "office"); rr.State != tunnel.RouteInstalled {
			t.Errorf("report: %+v", rr)
		}
	})

	t.Run("both, and all of them withdrawn", func(t *testing.T) {
		l, r := setup(t)
		l.tun("tun1", "10.8.0.6/24")
		before := policyState(t)
		announce(t, r, fullTunnelOfWG())
		announce(t, r, endpoints(up("office", 2, "tun1", tunnel.RoleSplit, "10.50.0.0/16"), "198.51.100.7"))
		// The halves capture the endpoint of the split tunnel as well.
		l.expectOurs(append(slices.Clone(fullTunnelRoutes), "10.50.0.0/16 dev tun1 metric 5", "198.51.100.7/32 via 192.168.50.1 dev ens18 metric 1")...)

		r.Withdraw("office")
		r.Withdraw("wg")

		l.expectOurs()
		if after := policyState(t); after != before {
			t.Errorf("the rules and the table of the other VPN changed:\nbefore:\n%s\nafter:\n%s", before, after)
		}
		l.expectProbe("8.8.8.8", "wgcf")
		if err := r.stop(nil); err != nil {
			t.Errorf("stop: %v", err)
		}
		if after := policyState(t); after != before {
			t.Errorf("stopping changed the other VPN:\nbefore:\n%s\nafter:\n%s", before, after)
		}
	})
}
