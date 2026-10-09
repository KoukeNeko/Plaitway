//go:build rootintegration && linux

package reconciler

// These tests drive the Reconciler against the real Linux kernel: the netlink
// routing table, the netlink monitor, real tun devices and real packets. They
// refuse to run anywhere but in a private network namespace, see rootlab_linux_test.go.

import (
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/osnet/linux"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

const (
	v4Endpoint = "203.0.113.10"
	v6Endpoint = "2001:db8:ee::10"
)

// A full tunnel: the two halves of the default route in both families, and the
// endpoint reachable through the physical router. Packets really go that way,
// and everything is gone again after Withdraw.
func TestKernelFullTunnel(t *testing.T) {
	l := newLab(t)
	l.tun("tun0", "10.9.0.2/24", "fd09::2/64")
	r := l.reconciler()
	full := nameserver(endpoints(up("wg", 1, "tun0", tunnel.RoleFull, "0.0.0.0/0", "::/0"), v4Endpoint, v6Endpoint), "10.9.0.1", ".")

	// Connecting: the endpoints are known, nothing captures them yet.
	announce(t, r, state(full, tunnel.StateConnecting))
	l.expectOurs()
	l.expectProbe(v4Endpoint, "uplink")

	announce(t, r, full)
	l.expectOurs(
		"0.0.0.0/1 dev tun0 metric 5",
		"128.0.0.0/1 dev tun0 metric 5",
		"203.0.113.10/32 via 192.168.50.1 dev ens18 metric 1",
		"::/1 dev tun0 metric 5",
		"8000::/1 dev tun0 metric 5",
		"2001:db8:ee::10/128 via fe80::1 dev ens18 metric 1",
	)
	expectInstalled(t, r, "wg", "0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1", v4Endpoint+"/32", v6Endpoint+"/128")

	// What the kernel does with them.
	l.expectRoute("8.8.8.8", decision{Dev: "tun0"})
	l.expectRoute("200.1.1.1", decision{Dev: "tun0"})
	l.expectRoute("2606:4700::1111", decision{Dev: "tun0"})
	l.expectRoute("a000::1", decision{Dev: "tun0"})
	l.expectRoute(v4Endpoint, decision{Dev: "ens18", Via: routerV4})
	l.expectRoute(v6Endpoint, decision{Dev: "ens18", Via: routerV6})
	l.expectRoute("192.168.50.77", decision{Dev: "ens18"})
	l.expectProbe(v4Endpoint, "uplink")
	l.expectProbe(v6Endpoint, "uplink")
	l.expectProbe("8.8.8.8", "tun0")
	l.expectProbe("2606:4700::1111", "tun0")

	// Every route carries the protocol, and the kernel agrees about what is ours.
	kernel4 := runIP(t, "-4", "route", "show", "proto", "199")
	kernel6 := runIP(t, "-6", "route", "show", "proto", "199")
	if n := strings.Count(kernel4, "\n") + strings.Count(kernel6, "\n"); n != 6 {
		t.Errorf("the kernel shows %d routes with proto 199, want 6:\n%s%s", n, kernel4, kernel6)
	}
	if entries := l.dns.Entries("wg"); len(entries) != 1 || entries[0].Iface != "tun0" {
		t.Errorf("resolver entries: %+v", entries)
	}
	if stale := r.Report().Stale; len(stale) != 0 {
		t.Errorf("stale routes: %+v", stale)
	}
	if left := unresolvedRoutes(r); len(left) != 6 {
		t.Errorf("the journal holds %d routes, want 6: %+v", len(left), left)
	}

	r.Withdraw("wg")
	l.expectOurs()
	l.expectRoute("8.8.8.8", decision{Dev: "ens18", Via: routerV4})
	l.expectRoute(v4Endpoint, decision{Dev: "ens18", Via: routerV4})
	l.expectProbe("8.8.8.8", "uplink")
	if left := unresolvedRoutes(r); len(left) != 0 {
		t.Errorf("the journal still holds %+v", left)
	}
	if entries := l.dns.Entries("wg"); len(entries) != 0 {
		t.Errorf("resolver entries after Withdraw: %+v", entries)
	}
}

// A split tunnel installs the prefixes it asked for, bound to its device, in
// both families, and never a next hop: the kernel takes on-link routes on a
// point-to-point device, whatever gateway the tunnel announced, even one that
// is not on the device's subnet.
func TestKernelSplitTunnel(t *testing.T) {
	for _, tt := range []struct{ name, v4, v6 string }{
		{"no gateway, as WireGuard", "", ""},
		{"an IPv4 gateway on the subnet of the device, as OpenVPN announces", "10.8.0.1", ""},
		{"gateways of both families", "10.8.0.1", "fd08::1"},
		{"an IPv6 gateway only", "", "fd08::1"},
		{"a gateway that is on no subnet of the device", "172.31.255.1", "fd77::1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			l := newLab(t)
			l.tun("tun0", "10.8.0.6/24", "fd08::6/64")
			r := l.reconciler()
			in := withGateways(up("vpn", 1, "tun0", tunnel.RoleSplit, "10.50.0.0/16", "192.168.77.0/24", "172.20.1.7/32", "2001:db8:77::/48", "fd50::/32"), tt.v4, tt.v6)

			announce(t, r, in)

			l.expectOurs(
				"10.50.0.0/16 dev tun0 metric 5",
				"192.168.77.0/24 dev tun0 metric 5",
				"172.20.1.7/32 dev tun0 metric 5",
				"2001:db8:77::/48 dev tun0 metric 5",
				"fd50::/32 dev tun0 metric 5",
			)
			expectInstalled(t, r, "vpn", "10.50.0.0/16", "192.168.77.0/24", "172.20.1.7/32", "2001:db8:77::/48", "fd50::/32")
			for _, rt := range l.table() {
				if protocol(rt) == linux.RouteProtocol && (rt.Gateway.IsValid() || rt.IfIndex != ifIndexOf(t, "tun0")) {
					t.Errorf("a tunnel route must be on-link and name the device: %+v", rt)
				}
			}
			l.expectRoute("10.50.3.4", decision{Dev: "tun0"})
			l.expectRoute("172.20.1.7", decision{Dev: "tun0"})
			l.expectRoute("2001:db8:77:1::5", decision{Dev: "tun0"})
			l.expectRoute("8.8.8.8", decision{Dev: "ens18", Via: routerV4})
			l.expectProbe("10.50.3.4", "tun0")
			l.expectProbe("8.8.8.8", "uplink")

			r.Withdraw("vpn")
			l.expectOurs()
		})
	}
}

// A prefix that is, or lies inside, the subnet of the uplink would take local
// traffic into the tunnel; it is blocked and nothing is added for it. One that
// only covers the subnet is fine, the connected route is more specific.
func TestKernelPrefixInsideTheConnectedSubnetIsBlocked(t *testing.T) {
	l := newLab(t)
	l.tun("tun0", "10.8.0.6/24")
	r := l.reconciler()

	announce(t, r, up("vpn", 1, "tun0", tunnel.RoleSplit,
		"192.168.50.0/24", "192.168.50.128/25", "192.168.50.2/32", "192.168.0.0/16", "2001:db8:50::/64", "2001:db8:50:0:1::/80", "10.50.0.0/16"))

	l.expectOurs("10.50.0.0/16 dev tun0 metric 5", "192.168.0.0/16 dev tun0 metric 5")
	for dst, detail := range map[string]string{
		"192.168.50.0/24":      "local subnet 192.168.50.0/24",
		"192.168.50.128/25":    "local subnet 192.168.50.0/24",
		"192.168.50.2/32":      "local subnet 192.168.50.0/24",
		"2001:db8:50::/64":     "local subnet 2001:db8:50::/64",
		"2001:db8:50:0:1::/80": "local subnet 2001:db8:50::/64",
	} {
		if rr := reportOf(t, r, dst, "vpn"); rr.State != tunnel.RouteBlocked || rr.Detail != detail {
			t.Errorf("%s: %+v, want blocked: %s", dst, rr, detail)
		}
	}
	l.expectRoute("192.168.50.77", decision{Dev: "ens18"})
	l.expectRoute("192.168.51.77", decision{Dev: "tun0"})
	expectInstalled(t, r, "vpn", "192.168.0.0/16", "10.50.0.0/16")
	if slices.ContainsFunc(l.table(), func(rt osnet.Route) bool {
		return rt.Dst == pfx("192.168.50.0/24") && protocol(rt) == linux.RouteProtocol
	}) {
		t.Error("a blocked prefix is in the table")
	}
}

// Two tunnels that want one prefix: the better one gets it, the other waits and
// is promoted when the first goes. The kernel takes one route per destination and
// metric, so the old route has to go before the new one comes; the kernel never
// has to refuse anything.
func TestKernelTunnelsCompeteForAPrefix(t *testing.T) {
	l := newLab(t)
	l.tun("tun0", "10.1.0.2/24")
	l.tun("tun1", "10.2.0.2/24")
	r := l.reconciler()
	low := up("low", 2, "tun0", tunnel.RoleSplit, "10.0.0.0/8", "172.16.0.0/12", "2001:db8:aa::/48")
	high := up("high", 1, "tun1", tunnel.RoleSplit, "10.0.0.0/8", "2001:db8:aa::/48")

	announce(t, r, low)
	l.expectOurs("10.0.0.0/8 dev tun0 metric 5", "172.16.0.0/12 dev tun0 metric 5", "2001:db8:aa::/48 dev tun0 metric 5")
	announce(t, r, high)

	l.expectOurs("10.0.0.0/8 dev tun1 metric 5", "172.16.0.0/12 dev tun0 metric 5", "2001:db8:aa::/48 dev tun1 metric 5")
	l.expectRoute("10.99.9.9", decision{Dev: "tun1"})
	l.expectRoute("2001:db8:aa::1", decision{Dev: "tun1"})
	for _, dst := range []string{"10.0.0.0/8", "2001:db8:aa::/48"} {
		if rr := reportOf(t, r, dst, "low"); rr.State != tunnel.RouteShadowed || rr.ShadowedBy != "high" {
			t.Errorf("%s of low: %+v", dst, rr)
		}
	}
	expectInstalled(t, r, "high", "10.0.0.0/8", "2001:db8:aa::/48")
	expectInstalled(t, r, "low", "172.16.0.0/12")

	r.Withdraw("high")

	l.expectOurs("10.0.0.0/8 dev tun0 metric 5", "172.16.0.0/12 dev tun0 metric 5", "2001:db8:aa::/48 dev tun0 metric 5")
	l.expectRoute("10.99.9.9", decision{Dev: "tun0"})
	expectInstalled(t, r, "low", "10.0.0.0/8", "172.16.0.0/12", "2001:db8:aa::/48")
	if strings.Contains(l.logs.String(), "reconciliation incomplete") {
		t.Errorf("the kernel refused something:\n%s", l.logs)
	}
	if got := unresolvedRoutes(r); len(got) != 3 {
		t.Errorf("journal: %+v", got)
	}
	r.Withdraw("low")
	l.expectOurs()
}

// Of two full tunnels the better one has the halves of the default route and the
// other stands by. When the first goes, the halves move to the second.
func TestKernelDefaultRouteMovesToTheTunnelOnStandby(t *testing.T) {
	l := newLab(t)
	l.tun("tun0", "10.1.0.2/24")
	l.tun("tun1", "10.2.0.2/24")
	r := l.reconciler()
	best := endpoints(up("best", 1, "tun0", tunnel.RoleFull, "0.0.0.0/0", "::/0"), "203.0.113.10")
	spare := endpoints(up("spare", 2, "tun1", tunnel.RoleFull, "0.0.0.0/0", "::/0"), "203.0.113.20")

	announce(t, r, best)
	announce(t, r, spare)

	l.expectOurs(
		"0.0.0.0/1 dev tun0 metric 5", "128.0.0.0/1 dev tun0 metric 5", "::/1 dev tun0 metric 5", "8000::/1 dev tun0 metric 5",
		"203.0.113.10/32 via 192.168.50.1 dev ens18 metric 1", "203.0.113.20/32 via 192.168.50.1 dev ens18 metric 1",
	)
	if rr := reportOf(t, r, "0.0.0.0/1", "spare"); rr.State != tunnel.RouteShadowed || rr.ShadowedBy != "best" || rr.Detail != "standby" {
		t.Errorf("the standby tunnel: %+v", rr)
	}

	r.Withdraw("best")

	l.expectOurs(
		"0.0.0.0/1 dev tun1 metric 5", "128.0.0.0/1 dev tun1 metric 5", "::/1 dev tun1 metric 5", "8000::/1 dev tun1 metric 5",
		"203.0.113.20/32 via 192.168.50.1 dev ens18 metric 1",
	)
	l.expectRoute("8.8.8.8", decision{Dev: "tun1"})
	expectInstalled(t, r, "spare", "0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1")
	if strings.Contains(l.logs.String(), "reconciliation incomplete") {
		t.Errorf("the kernel refused something:\n%s", l.logs)
	}
}

// foreignAdapter is the device of a VPN that another program runs: a dummy link,
// which the monitor takes for a way out, so that what happens to it is reported.
func (l *lab) foreignAdapter(name string) {
	l.t.Helper()
	runIP(l.t, "link", "add", name, "type", "dummy")
	runIP(l.t, "link", "set", name, "up")
	runIP(l.t, "addr", "add", "10.9.0.2/24", "dev", name)
	runIP(l.t, "-6", "addr", "add", "fd09::2/64", "dev", name, "nodad")
}

// overriddenBy is the detail of a route that the foreign route to dst outranks.
func overriddenBy(dst, gateway string, ifIndex uint32, theirs, ours int) string {
	return fmt.Sprintf("overridden by %s via %s (interface %d): effective metric %d, ours %d", dst, gateway, ifIndex, theirs, ours)
}

// A route of another program to the same prefix with a lower metric carries the
// traffic instead of ours; one with a higher metric does not. Neither is touched,
// the report tells the first case from a route that failed, and the verdict
// follows the other program as it comes and goes.
func TestKernelForeignRoutesOutrankOrLoseToOurs(t *testing.T) {
	l := newLab(t)
	l.tun("tun0", "10.8.0.6/24", "fd08::6/64")
	l.foreignAdapter("gui0")
	gui := ifIndexOf(t, "gui0")
	r := l.reconciler()
	stop := l.run(r)
	defer stop()
	announce(t, r, up("vpn", 1, "tun0", tunnel.RoleSplit, "10.50.0.0/16", "10.60.0.0/16", "10.70.0.0/16", "fd50::/32"))
	ours := []string{"10.50.0.0/16 dev tun0 metric 5", "10.60.0.0/16 dev tun0 metric 5", "10.70.0.0/16 dev tun0 metric 5", "fd50::/32 dev tun0 metric 5"}
	l.expectOurs(ours...)
	l.expectRoute("10.50.1.1", decision{Dev: "tun0"})

	// The other program connects: a lower metric (the metric an openvpn route gets
	// without one is 0), and a higher one.
	runIP(t, "route", "add", "10.50.0.0/16", "via", "10.9.0.1", "dev", "gui0", "proto", "static", "metric", "1")
	runIP(t, "route", "add", "10.60.0.0/16", "via", "10.9.0.1", "dev", "gui0", "proto", "static", "metric", "50")
	runIP(t, "route", "add", "10.70.0.0/16", "via", "10.9.0.1", "dev", "gui0")
	runIP(t, "-6", "route", "add", "fd50::/32", "via", "fd09::1", "dev", "gui0", "metric", "1")

	eventually(t, "the overridden routes to be reported", 3*time.Second, func() bool {
		return reportOf(t, r, "10.50.0.0/16", "vpn").Overridden
	})
	for dst, want := range map[string]string{
		"10.50.0.0/16": overriddenBy("10.50.0.0/16", "10.9.0.1", gui, 1, 5),
		"10.70.0.0/16": overriddenBy("10.70.0.0/16", "10.9.0.1", gui, 0, 5),
		"fd50::/32":    overriddenBy("fd50::/32", "fd09::1", gui, 1, 5),
	} {
		if rr := reportOf(t, r, dst, "vpn"); rr.State != tunnel.RouteFailed || !rr.Overridden || rr.Detail != want {
			t.Errorf("%s: %+v\nwant overridden: %s", dst, rr, want)
		}
	}
	expectInstalled(t, r, "vpn", "10.60.0.0/16")
	// What the kernel does agrees with the verdict, and none of the routes was touched.
	l.expectRoute("10.50.1.1", decision{Dev: "gui0", Via: "10.9.0.1"})
	l.expectRoute("10.60.1.1", decision{Dev: "tun0"})
	l.expectRoute("10.70.1.1", decision{Dev: "gui0", Via: "10.9.0.1"})
	l.expectRoute("fd50::1", decision{Dev: "gui0", Via: "fd09::1"})
	l.expectOurs(ours...)
	if got := strings.Count(l.logs.String(), "used instead of ours"); got != 3 {
		t.Errorf("the log says so %d times, want once per route:\n%s", got, l.logs)
	}

	// It disconnects: ours is in use again.
	runIP(t, "route", "del", "10.50.0.0/16", "dev", "gui0")
	runIP(t, "route", "del", "10.70.0.0/16", "dev", "gui0")
	runIP(t, "-6", "route", "del", "fd50::/32", "dev", "gui0")
	eventually(t, "the routes to be in use again", 3*time.Second, func() bool {
		return !reportOf(t, r, "10.50.0.0/16", "vpn").Overridden && !reportOf(t, r, "10.70.0.0/16", "vpn").Overridden && !reportOf(t, r, "fd50::/32", "vpn").Overridden
	})
	expectInstalled(t, r, "vpn", "10.50.0.0/16", "10.70.0.0/16", "fd50::/32")
	l.expectRoute("10.50.1.1", decision{Dev: "tun0"})
	l.expectRoute("10.70.1.1", decision{Dev: "tun0"})
	if !strings.Contains(l.logs.String(), "our route is in use again") {
		t.Errorf("the log does not say that ours is back:\n%s", l.logs)
	}
	// The foreign route of the higher metric was never ours to delete.
	if _, ok := l.route("10.60.0.0/16", 50); !ok {
		t.Error("the foreign route was deleted")
	}
}

// A route of another program with the destination and metric of ours cannot sit
// beside it: the kernel takes one. Ours is not installed, the report says whose
// the prefix is, and the other program's route is left alone, whether it was
// there first or got in between our look at the table and our write.
func TestKernelForeignRouteWithTheMetricOfOursIsAConflict(t *testing.T) {
	foreignRoute := func(l *lab) {
		runIP(l.t, "route", "add", "10.50.0.0/16", "via", "10.9.0.1", "dev", "gui0", "proto", "static", "metric", "5")
		runIP(l.t, "-6", "route", "add", "fd50::/32", "via", "fd09::1", "dev", "gui0", "proto", "static", "metric", "5")
	}
	check := func(t *testing.T, l *lab, r *Reconciler) {
		t.Helper()
		for dst, via := range map[string]string{"10.50.0.0/16": "10.9.0.1", "fd50::/32": "fd09::1"} {
			if rr := reportOf(t, r, dst, "vpn"); rr.State != tunnel.RouteFailed || rr.Overridden || rr.Detail != "held by another program via "+via {
				t.Errorf("%s: %+v, want a conflict", dst, rr)
			}
		}
		expectInstalled(t, r, "vpn", "10.60.0.0/16")
		l.expectOurs("10.60.0.0/16 dev tun0 metric 5")
		for _, dst := range []string{"10.50.0.0/16", "fd50::/32"} {
			rt, ok := l.route(dst, 5)
			if !ok || rt.Iface != "gui0" || protocol(rt) == linux.RouteProtocol {
				t.Errorf("the route of the other program: %+v (present %v)", rt, ok)
			}
		}
		if ownsRoute(r, "10.50.0.0/16") || ownsRoute(r, "fd50::/32") {
			t.Error("a route that is not ours is owned")
		}
	}
	intent := up("vpn", 1, "tun0", tunnel.RoleSplit, "10.50.0.0/16", "10.60.0.0/16", "fd50::/32")

	t.Run("there first", func(t *testing.T) {
		l := newLab(t)
		l.tun("tun0", "10.8.0.6/24")
		l.foreignAdapter("gui0")
		foreignRoute(l)
		r := l.reconciler()
		announce(t, r, intent)
		check(t, l, r)

		// It goes; the next pass takes the prefix, and Withdraw gives it back.
		runIP(t, "route", "del", "10.50.0.0/16", "dev", "gui0")
		if err := r.Resync(); err != nil {
			t.Fatal(err)
		}
		expectInstalled(t, r, "vpn", "10.50.0.0/16")
		l.expectRoute("10.50.1.1", decision{Dev: "tun0"})
		r.Withdraw("vpn")
		if _, ok := l.route("fd50::/32", 5); !ok {
			t.Error("Withdraw removed the route of the other program")
		}
	})

	t.Run("in between", func(t *testing.T) {
		l := newLab(t)
		l.tun("tun0", "10.8.0.6/24")
		l.foreignAdapter("gui0")
		var once sync.Once
		r := l.reconciler(func(c *Config) {
			c.Routes = racingTable{c.Routes, func(osnet.Route) { once.Do(func() { foreignRoute(l) }) }}
		})
		announce(t, r, intent)
		check(t, l, r)
	})
}

// racingTable is the real routing table with something that gets in just
// before every Add.
type racingTable struct {
	osnet.RouteTable
	before func(osnet.Route)
}

func (t racingTable) Add(rt osnet.Route) error {
	t.before(rt)
	return t.RouteTable.Add(rt)
}

// A foreign VPN on a tunnel device is what the lower metric of a foreign route
// usually is. The monitor reports no event for the interfaces of tunnels, but
// does for a route that another program adds there, so that the verdict follows
// the program as it connects and disconnects.
func TestKernelForeignRouteOverATunnelDeviceIsNoticed(t *testing.T) {
	l := newLab(t)
	l.tun("tun0", "10.8.0.6/24")
	l.tun("tun9", "10.9.0.2/24")
	r := l.reconciler()
	l.run(r)
	announce(t, r, up("vpn", 1, "tun0", tunnel.RoleSplit, "10.50.0.0/16"))
	expectInstalled(t, r, "vpn", "10.50.0.0/16")

	start := time.Now()
	runIP(t, "route", "add", "10.50.0.0/16", "via", "10.9.0.1", "dev", "tun9", "proto", "static", "metric", "1")
	eventually(t, "the foreign VPN to be noticed", 3*time.Second, func() bool { return reportOf(t, r, "10.50.0.0/16", "vpn").Overridden })
	t.Logf("noticed after %v", time.Since(start).Round(time.Millisecond))

	start = time.Now()
	runIP(t, "route", "del", "10.50.0.0/16", "dev", "tun9")
	eventually(t, "the foreign VPN to be gone", 3*time.Second, func() bool { return !reportOf(t, r, "10.50.0.0/16", "vpn").Overridden })
	t.Logf("restored after %v", time.Since(start).Round(time.Millisecond))
	expectInstalled(t, r, "vpn", "10.50.0.0/16")
}

// The engine announces the interface before it exists, or while it is down. The
// routes wait and are added when it comes up, although the monitor reports
// nothing about the interfaces of tunnels.
func TestKernelTunnelThatAppearsAfterItsAnnouncement(t *testing.T) {
	retry := func(c *Config) { c.RetryDelay = 100 * time.Millisecond }
	intent := up("late", 1, "tun3", tunnel.RoleSplit, "10.50.0.0/16", "fd50::/32")

	t.Run("not there yet", func(t *testing.T) {
		l := newLab(t)
		r := l.reconciler(retry)
		l.run(r)
		announce(t, r, intent)
		if rr := reportOf(t, r, "10.50.0.0/16", "late"); rr.State != tunnel.RoutePending || rr.Detail != "interface tun3 not found" {
			t.Fatalf("report: %+v", rr)
		}
		l.expectOurs()

		l.tun("tun3", "10.8.0.6/24", "fd08::6/64")

		eventuallyInstalled(t, r, "late", "10.50.0.0/16", "fd50::/32")
		l.expectOurs("10.50.0.0/16 dev tun3 metric 5", "fd50::/32 dev tun3 metric 5")
	})

	t.Run("there but down", func(t *testing.T) {
		l := newLab(t)
		l.tun("tun3", "10.8.0.6/24", "fd08::6/64")
		runIP(t, "link", "set", "tun3", "down")
		r := l.reconciler(retry)
		l.run(r)
		announce(t, r, intent)
		l.expectOurs()
		if rr := reportOf(t, r, "10.50.0.0/16", "late"); rr.State != tunnel.RoutePending {
			t.Errorf("a route through a device that is down is waiting, not failed: %+v", rr)
		}
		if strings.Contains(l.logs.String(), "reconciliation incomplete") {
			t.Errorf("a device that is down is not a failure of the pass:\n%s", l.logs)
		}

		runIP(t, "link", "set", "tun3", "up")

		eventuallyInstalled(t, r, "late", "10.50.0.0/16", "fd50::/32")
		l.expectOurs("10.50.0.0/16 dev tun3 metric 5", "fd50::/32 dev tun3 metric 5")
	})

	t.Run("gone between the look and the write", func(t *testing.T) {
		l := newLab(t)
		tun := l.tun("tun3", "10.8.0.6/24", "fd08::6/64")
		var once sync.Once
		r := l.reconciler(retry, func(c *Config) {
			c.Routes = racingTable{c.Routes, func(osnet.Route) {
				once.Do(func() {
					tun.destroy()
					eventually(t, "the device to go", 3*time.Second, func() bool { _, err := net.InterfaceByName("tun3"); return err != nil })
				})
			}}
		})
		l.run(r)
		announce(t, r, intent)
		l.expectOurs()
		if rr := reportOf(t, r, "10.50.0.0/16", "late"); rr.State != tunnel.RoutePending {
			t.Errorf("a route through a device that is gone is waiting, not failed: %+v", rr)
		}
		if strings.Contains(l.logs.String(), "reconciliation incomplete") {
			t.Errorf("a device that is gone is not a failure of the pass:\n%s", l.logs)
		}

		l.tun("tun3", "10.8.0.6/24", "fd08::6/64")

		eventuallyInstalled(t, r, "late", "10.50.0.0/16", "fd50::/32")
		l.expectOurs("10.50.0.0/16 dev tun3 metric 5", "fd50::/32 dev tun3 metric 5")
	})
}

// A tunnel interface that is made again with the same name has another index,
// and systemd-resolved keeps its settings by index: the ones of the old
// interface are gone with it. The announcement that follows (the engine's
// state changes) writes them again, and the routes go to the new interface.
func TestKernelTunnelInterfaceMadeAgainWithoutWithdraw(t *testing.T) {
	l := newLab(t)
	old := l.tun("tun0", "10.8.0.6/24")
	r := l.reconciler()
	in := nameserver(endpoints(up("vpn", 1, "tun0", tunnel.RoleFull, "0.0.0.0/0"), v4Endpoint), "10.8.0.1", ".")
	announce(t, r, in)
	oldIndex := old.index
	l.expectOurs("0.0.0.0/1 dev tun0 metric 5", "128.0.0.0/1 dev tun0 metric 5", "203.0.113.10/32 via 192.168.50.1 dev ens18 metric 1")
	applies := func() int {
		n := 0
		for _, op := range l.dns.Ops() {
			if op.Kind == "apply" {
				n++
			}
		}
		return n
	}
	if got := applies(); got != 1 {
		t.Fatalf("resolver writes: %d", got)
	}

	old.destroy()
	eventually(t, "the device to go", 3*time.Second, func() bool { _, err := net.InterfaceByName("tun0"); return err != nil })
	l.expectOurs("203.0.113.10/32 via 192.168.50.1 dev ens18 metric 1")
	fresh := l.tun("tun0", "10.8.0.6/24")
	if fresh.index == oldIndex {
		t.Fatalf("the kernel gave the new device the index %d again", oldIndex)
	}
	announce(t, r, in)

	l.expectOurs("0.0.0.0/1 dev tun0 metric 5", "128.0.0.0/1 dev tun0 metric 5", "203.0.113.10/32 via 192.168.50.1 dev ens18 metric 1")
	for _, rt := range l.table() {
		if protocol(rt) == linux.RouteProtocol && rt.Gateway.IsValid() == false && rt.IfIndex != fresh.index {
			t.Errorf("a route still names the old interface: %+v", rt)
		}
	}
	if got := applies(); got != 2 {
		t.Errorf("the resolver settings of the old interface went with it, they are written again: %d writes, want 2", got)
	}
	expectInstalled(t, r, "vpn", "0.0.0.0/1", "128.0.0.0/1")
}

// A host with IPv6 disabled on the tunnel's device (the sysctl is set for new
// devices by a hardened host, or an engine left it so) takes no IPv6 route
// there. The IPv6 halves are reported failed with the kernel's reason while IPv4
// carries on, nothing is torn down and rebuilt because of it, and they are added
// by the next full pass once IPv6 is enabled.
func TestKernelIPv6DisabledOnTheTunnelDevice(t *testing.T) {
	l := newLab(t)
	l.tun("tun0", "10.8.0.6/24")
	if err := os.WriteFile("/proc/sys/net/ipv6/conf/tun0/disable_ipv6", []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := l.reconciler(func(c *Config) { c.RetryDelay = 50 * time.Millisecond })
	l.run(r)

	announce(t, r, endpoints(up("vpn", 1, "tun0", tunnel.RoleFull, "0.0.0.0/0", "::/0"), v4Endpoint, v6Endpoint))
	// Passes that follow: events of the network, and whatever the Reconciler arms.
	for range 4 {
		runIP(t, "route", "add", "198.51.100.250/32", "dev", uplinkName)
		runIP(t, "route", "del", "198.51.100.250/32", "dev", uplinkName)
		time.Sleep(400 * time.Millisecond)
	}

	l.expectOurs(
		"0.0.0.0/1 dev tun0 metric 5", "128.0.0.0/1 dev tun0 metric 5",
		"203.0.113.10/32 via 192.168.50.1 dev ens18 metric 1", "2001:db8:ee::10/128 via fe80::1 dev ens18 metric 1",
	)
	expectInstalled(t, r, "vpn", "0.0.0.0/1", "128.0.0.0/1", v4Endpoint+"/32")
	for _, dst := range []string{"::/1", "8000::/1"} {
		if rr := reportOf(t, r, dst, "vpn"); rr.State != tunnel.RouteFailed || rr.Overridden || !strings.Contains(rr.Detail, "IPv6 is disabled on nexthop device") {
			t.Errorf("%s: %+v, want failed with the kernel's reason", dst, rr)
		}
	}
	l.expectRoute("8.8.8.8", decision{Dev: "tun0"})
	// The failure is the host's setting, not a pass that does not converge: the
	// IPv4 halves, which carry the traffic, are not removed to try again.
	if n := strings.Count(l.logs.String(), "rebuilding from scratch"); n != 0 {
		t.Errorf("the Reconciler rebuilt everything %d times:\n%s", n, l.logs)
	}
	if n := strings.Count(l.logs.String(), "reconciliation incomplete"); n != 0 {
		t.Errorf("the pass is reported as failed %d times:\n%s", n, l.logs)
	}
	if got := unresolvedRoutes(r); len(got) != 4 {
		t.Errorf("the journal holds %d routes, want 4: %+v", len(got), got)
	}

	if err := os.WriteFile("/proc/sys/net/ipv6/conf/tun0/disable_ipv6", []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.Resync(); err != nil {
		t.Fatal(err)
	}

	l.expectOurs(
		"0.0.0.0/1 dev tun0 metric 5", "128.0.0.0/1 dev tun0 metric 5", "::/1 dev tun0 metric 5", "8000::/1 dev tun0 metric 5",
		"203.0.113.10/32 via 192.168.50.1 dev ens18 metric 1", "2001:db8:ee::10/128 via fe80::1 dev ens18 metric 1",
	)
	expectInstalled(t, r, "vpn", "::/1", "8000::/1")
}

// A foreign route that sends nowhere in particular, as an administrator keeps for
// a range: the report names it, and it is not touched.
func TestKernelForeignBlackholeRoute(t *testing.T) {
	l := newLab(t)
	l.tun("tun0", "10.8.0.6/24")
	r := l.reconciler()
	runIP(t, "route", "add", "blackhole", "10.50.0.0/16", "metric", "5", "proto", "static")
	runIP(t, "route", "add", "unreachable", "10.60.0.0/16", "metric", "1", "proto", "static")

	announce(t, r, up("vpn", 1, "tun0", tunnel.RoleSplit, "10.50.0.0/16", "10.60.0.0/16"))

	if rr := reportOf(t, r, "10.50.0.0/16", "vpn"); rr.State != tunnel.RouteFailed || rr.Overridden || rr.Detail != "held by another program via blackhole" {
		t.Errorf("10.50.0.0/16: %+v", rr)
	}
	if rr := reportOf(t, r, "10.60.0.0/16", "vpn"); rr.State != tunnel.RouteFailed || !rr.Overridden || !strings.HasPrefix(rr.Detail, "overridden by 10.60.0.0/16 via blackhole:") {
		t.Errorf("10.60.0.0/16: %+v", rr)
	}
	l.expectOurs("10.60.0.0/16 dev tun0 metric 5")
	r.Withdraw("vpn")
	for _, c := range []struct {
		dst    string
		metric uint32
	}{{"10.50.0.0/16", 5}, {"10.60.0.0/16", 1}} {
		if rt, ok := l.route(c.dst, c.metric); !ok || !rt.Blackhole {
			t.Errorf("the foreign route to %s: %+v (present %v)", c.dst, rt, ok)
		}
	}
}

// A program may append a route to a destination and metric that has one. The
// kernel keeps the first in use; the table lists both. Ours is the one the
// Reconciler finds again, and Withdraw removes it and not the other.
func TestKernelAppendedRouteOfAnotherProgram(t *testing.T) {
	l := newLab(t)
	l.tun("tun0", "10.8.0.6/24")
	l.foreignAdapter("gui0")
	r := l.reconciler()
	announce(t, r, up("vpn", 1, "tun0", tunnel.RoleSplit, "10.50.0.0/16"))
	runIP(t, "route", "append", "10.50.0.0/16", "via", "10.9.0.1", "dev", "gui0", "proto", "static", "metric", "5")
	if got := len(l.routesTo("10.50.0.0/16")); got != 2 {
		t.Fatalf("setup: %d routes to the prefix after the append", got)
	}

	r.handle(osnet.Change{Reason: osnet.ChangeHeartbeat, At: time.Now()}, false)

	expectInstalled(t, r, "vpn", "10.50.0.0/16")
	l.expectOurs("10.50.0.0/16 dev tun0 metric 5")
	l.expectRoute("10.50.1.1", decision{Dev: "tun0"})
	r.Withdraw("vpn")
	left := l.routesTo("10.50.0.0/16")
	if len(left) != 1 || left[0].Iface != "gui0" || protocol(left[0]) == linux.RouteProtocol {
		t.Errorf("after Withdraw: %+v, want the route of the other program", left)
	}
}

func (l *lab) routesTo(dst string) []osnet.Route {
	l.t.Helper()
	var out []osnet.Route
	for _, rt := range l.table() {
		if rt.Dst == pfx(dst) {
			out = append(out, rt)
		}
	}
	return out
}

// A route of another program that is the route we want, to the last detail, is
// as good as ours, and stays when we go.
func TestKernelIdenticalForeignRouteIsAcceptedAndKept(t *testing.T) {
	intent := endpoints(up("wg", 1, "tun0", tunnel.RoleFull, "0.0.0.0/0"), v4Endpoint)
	check := func(t *testing.T, l *lab, r *Reconciler) {
		t.Helper()
		expectInstalled(t, r, "wg", v4Endpoint+"/32", "0.0.0.0/1")
		rt, ok := l.route("203.0.113.10/32", 1)
		if !ok || protocol(rt) != 4 {
			t.Fatalf("the route: %+v (present %v)", rt, ok)
		}
		if ownsRoute(r, v4Endpoint+"/32") {
			t.Error("a route that is not ours is owned")
		}
		r.Withdraw("wg")
		if _, ok := l.route("203.0.113.10/32", 1); !ok {
			t.Error("Withdraw removed the route of the other program")
		}
		l.expectOurs()
	}
	identical := func(l *lab) {
		runIP(l.t, "route", "add", "203.0.113.10/32", "via", routerV4, "dev", uplinkName, "proto", "static", "metric", "1")
	}

	t.Run("there first", func(t *testing.T) {
		l := newLab(t)
		l.tun("tun0", "10.8.0.6/24")
		identical(l)
		r := l.reconciler()
		announce(t, r, intent)
		check(t, l, r)
	})
	t.Run("in between", func(t *testing.T) {
		l := newLab(t)
		l.tun("tun0", "10.8.0.6/24")
		var once sync.Once
		r := l.reconciler(func(c *Config) {
			c.Routes = racingTable{c.Routes, func(osnet.Route) { once.Do(func() { identical(l) }) }}
		})
		announce(t, r, intent)
		check(t, l, r)
	})
}

// An endpoint whose family has no default route has nowhere to be reached
// through. The route waits, and is added within a moment of the default route
// coming back.
func TestKernelEndpointWaitsForADefaultRoute(t *testing.T) {
	l := newLab(t)
	l.tun("tun0", "10.9.0.2/24", "fd09::2/64")
	runIP(t, "-6", "route", "del", "default")
	r := l.reconciler()
	l.run(r)

	announce(t, r, fullTunnelOfWG())

	if rr := reportOf(t, r, v6Endpoint+"/128", "wg"); rr.State != tunnel.RoutePending || rr.Detail != "no network" {
		t.Errorf("report: %+v", rr)
	}
	expectInstalled(t, r, "wg", v4Endpoint+"/32", "0.0.0.0/1")

	runIP(t, "-6", "route", "add", "default", "via", routerV6, "dev", uplinkName, "proto", "ra", "metric", "100")

	eventuallyInstalled(t, r, "wg", v6Endpoint+"/128")
	l.expectOurs(fullTunnelRoutes...)
	l.expectProbe(v6Endpoint, "uplink")
}

// A tunnel with no IPv6 address still gets the IPv6 halves of a full tunnel, so
// that nothing leaks around it; its device takes them. The traffic has no source
// address there, which is where it stops.
func TestKernelFullTunnelOfADeviceWithoutIPv6Address(t *testing.T) {
	l := newLab(t)
	l.tun("tun0", "10.9.0.2/24")
	r := l.reconciler()

	announce(t, r, endpoints(up("wg", 1, "tun0", tunnel.RoleFull, "0.0.0.0/0", "::/0"), v4Endpoint))

	l.expectOurs(
		"0.0.0.0/1 dev tun0 metric 5", "128.0.0.0/1 dev tun0 metric 5", "::/1 dev tun0 metric 5", "8000::/1 dev tun0 metric 5",
		"203.0.113.10/32 via 192.168.50.1 dev ens18 metric 1",
	)
	expectInstalled(t, r, "wg", "::/1", "8000::/1")
	l.expectRoute("2606:4700::1111", decision{Dev: "tun0"})
	if got := l.probe("2606:4700::1111"); got == "uplink" {
		t.Errorf("IPv6 traffic leaked around the tunnel: %s", got)
	}
}
