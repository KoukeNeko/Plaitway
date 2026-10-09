//go:build rootintegration && linux

package reconciler

// What happens to the routes when the machine, the daemon or another program
// does something to them: the daemon dies, the router changes, someone flushes
// the table. See rootlab_linux_test.go for how to run these.

import (
	"fmt"
	"net"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet/linux"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// fullTunnelOfWG is a WireGuard-like full tunnel with a resolver, announced as
// up, with one endpoint of each family.
func fullTunnelOfWG() tunnel.Intent {
	return nameserver(endpoints(up("wg", 1, "tun0", tunnel.RoleFull, "0.0.0.0/0", "::/0"), v4Endpoint, v6Endpoint), "10.9.0.1", ".")
}

// fullTunnelRoutes is what fullTunnelOfWG leaves in the table of lab.
var fullTunnelRoutes = []string{
	"0.0.0.0/1 dev tun0 metric 5",
	"128.0.0.0/1 dev tun0 metric 5",
	"203.0.113.10/32 via 192.168.50.1 dev ens18 metric 1",
	"::/1 dev tun0 metric 5",
	"8000::/1 dev tun0 metric 5",
	"2001:db8:ee::10/128 via fe80::1 dev ens18 metric 1",
}

func sortedCopy(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}

func deviceGone(name string) func() bool {
	return func() bool { _, err := net.InterfaceByName(name); return err != nil }
}

// The daemon dies: nothing runs its cleanup. The tunnel device goes with the
// process and the routes bound to it with the device; the routes through the
// router stay, and the next start removes them by what the journal says, and
// nothing else.
func TestKernelCrashRecovery(t *testing.T) {
	// crash returns the machine as the dead daemon left it, with the journal records
	// of its two bypass routes.
	crash := func(t *testing.T) (l *lab, tun *labTun, v4, v6 record) {
		l = newLab(t)
		tun = l.tun("tun0", "10.9.0.2/24", "fd09::2/64")
		r1 := l.reconciler()
		announce(t, r1, fullTunnelOfWG())
		l.expectOurs(fullTunnelRoutes...)
		for _, rec := range unresolvedRoutes(r1) {
			switch rec.Key {
			case v4Endpoint + "/32":
				v4 = rec
			case v6Endpoint + "/128":
				v6 = rec
			}
		}
		if len(unresolvedRoutes(r1)) != 6 || v4.Metric != bypassMetric || v6.Gateway != "fe80::1" || v6.IfIndex != ifIndexOf(t, uplinkName) {
			t.Fatalf("journal: %+v", unresolvedRoutes(r1))
		}
		// r1 is abandoned: the process is gone.
		return l, tun, v4, v6
	}

	t.Run("the device and its routes are gone, the bypass routes are removed", func(t *testing.T) {
		l, tun, _, _ := crash(t)
		tun.destroy()
		eventually(t, "the device to go", 3*time.Second, deviceGone("tun0"))
		l.expectOurs("203.0.113.10/32 via 192.168.50.1 dev ens18 metric 1", "2001:db8:ee::10/128 via fe80::1 dev ens18 metric 1")

		r2 := l.reconciler()

		l.expectOurs()
		if left := unresolvedRoutes(r2); len(left) != 0 {
			t.Errorf("the journal still lists %+v", left)
		}
		if stale := r2.Report().Stale; len(stale) != 0 {
			t.Errorf("stale routes after recovery: %+v", stale)
		}
		if owned, _ := l.dns.Owned(); len(owned) != 0 {
			t.Errorf("resolver entries survived the restart: %v", owned)
		}
		if n := strings.Count(l.logs.String(), "removed a route of an earlier run"); n != 2 {
			t.Errorf("removed %d routes of an earlier run, want the 2 that stayed:\n%s", n, l.logs)
		}
		l.expectRoute("8.8.8.8", decision{Dev: "ens18", Via: routerV4})
	})

	t.Run("the device is still there, and so are its routes", func(t *testing.T) {
		// An OpenVPN child can outlive its daemon, and with it the device.
		l, _, _, _ := crash(t)
		l.reconciler()
		l.expectOurs()
		l.expectRoute("8.8.8.8", decision{Dev: "ens18", Via: routerV4})
	})

	t.Run("an interface index that no longer exists", func(t *testing.T) {
		l, tun, v4, _ := crash(t)
		tun.destroy()
		// The uplink is plugged out and in: the interface has another index, and the
		// routes through it went with the old one. A route another program made
		// meanwhile has our destination and metric, and the new interface.
		old := ifIndexOf(t, uplinkName)
		runIP(t, "link", "del", uplinkName)
		l.addUplink(uplinkName, "far0", "192.168.50", "2001:db8:50::")
		runIP(t, "route", "add", "default", "via", routerV4, "dev", uplinkName, "proto", "dhcp", "metric", "100")
		runIP(t, "-6", "route", "add", "default", "via", routerV6, "dev", uplinkName, "proto", "ra", "metric", "100")
		if ifIndexOf(t, uplinkName) == old || v4.IfIndex != old {
			t.Fatalf("setup: the uplink has the index %d, the journal says %d", ifIndexOf(t, uplinkName), v4.IfIndex)
		}
		runIP(t, "route", "add", "203.0.113.10/32", "via", routerV4, "dev", uplinkName, "proto", "static", "metric", "1")

		r2 := l.reconciler()

		if rt, ok := l.route("203.0.113.10/32", 1); !ok || protocol(rt) != 4 {
			t.Errorf("the route of the other program: %+v (present %v)", rt, ok)
		}
		if left := unresolvedRoutes(r2); len(left) != 0 {
			t.Errorf("the journal still lists %+v", left)
		}
		if !strings.Contains(l.logs.String(), "route of an earlier run was changed by another program") {
			t.Errorf("the log does not say that the route was left alone:\n%s", l.logs)
		}
		l.expectOurs()
	})

	t.Run("an interface index that another interface has now", func(t *testing.T) {
		l, tun, v4, _ := crash(t)
		tun.destroy()
		old := ifIndexOf(t, uplinkName)
		runIP(t, "link", "del", uplinkName)
		// The kernel may give the index of a removed interface to a new one; here it
		// is asked to. The new interface has a route that is the one of the journal in
		// every respect but the name of the interface.
		runIP(t, "link", "add", "ens19", "index", fmt.Sprint(old), "type", "dummy")
		runIP(t, "link", "set", "ens19", "up")
		runIP(t, "addr", "add", "192.168.50.2/24", "dev", "ens19")
		runIP(t, "route", "add", "203.0.113.10/32", "via", routerV4, "dev", "ens19", "proto", "199", "metric", "1")
		if ifIndexOf(t, "ens19") != old || v4.IfIndex != old {
			t.Fatalf("setup: the new interface has the index %d, the journal says %d", ifIndexOf(t, "ens19"), v4.IfIndex)
		}

		l.reconciler()

		if rt, ok := l.route("203.0.113.10/32", 1); !ok || rt.Iface != "ens19" {
			t.Errorf("the route of the other interface was removed: %+v (present %v)", rt, ok)
		}
	})

	t.Run("a route another program replaced", func(t *testing.T) {
		for _, tt := range []struct {
			name, gateway string
			protocol      uint32
		}{
			{"with its protocol", routerV4, 4},
			{"with its gateway", "192.168.50.9", linux.RouteProtocol},
		} {
			t.Run(tt.name, func(t *testing.T) {
				l, tun, _, _ := crash(t)
				tun.destroy()
				runIP(t, "route", "replace", "203.0.113.10/32", "via", tt.gateway, "dev", uplinkName, "proto", fmt.Sprint(tt.protocol), "metric", "1")

				r2 := l.reconciler()

				rt, ok := l.route("203.0.113.10/32", 1)
				if !ok {
					t.Fatal("the replaced route was removed")
				}
				if rt.Gateway.String() != tt.gateway || protocol(rt) != tt.protocol {
					t.Errorf("the replaced route was changed: %+v", rt)
				}
				if _, ok := l.route("2001:db8:ee::10/128", 1); ok {
					t.Error("the other bypass route, which nobody changed, was left")
				}
				if left := unresolvedRoutes(r2); len(left) != 0 {
					t.Errorf("the journal still lists %+v", left)
				}
			})
		}
	})
}

// The machine joins another network behind the same interface: the default
// route leads to another router, the old address goes. The bypass routes of the
// endpoints follow within a second of the last change, which the real monitor
// reports, and the engines are asked to rebind.
func TestKernelDefaultGatewayChangeRepointsBypassRoutes(t *testing.T) {
	setup := func(t *testing.T) (*lab, *Reconciler, *atomic.Int32) {
		l := newLab(t)
		l.tun("tun0", "10.9.0.2/24", "fd09::2/64")
		r := l.reconciler()
		rebinds := new(atomic.Int32)
		r.SetRebind(func() { rebinds.Add(1) })
		l.run(r)
		announce(t, r, fullTunnelOfWG())
		l.expectOurs(fullTunnelRoutes...)
		rebinds.Store(0)
		return l, r, rebinds
	}

	t.Run("another router on the same interface", func(t *testing.T) {
		l, r, rebinds := setup(t)
		l.far.ip("addr", "add", "10.20.30.1/24", "dev", "far0")
		l.far.ip("-6", "addr", "add", "fe80::2/64", "dev", "far0", "nodad")
		l.far.ip("-6", "addr", "add", "2001:db8:60::1/64", "dev", "far0", "nodad")

		runIP(t, "addr", "add", "10.20.30.2/24", "dev", uplinkName)
		runIP(t, "route", "replace", "default", "via", "10.20.30.1", "dev", uplinkName, "proto", "dhcp", "metric", "100")
		runIP(t, "addr", "del", "192.168.50.2/24", "dev", uplinkName)
		runIP(t, "-6", "addr", "add", "2001:db8:60::2/64", "dev", uplinkName, "nodad")
		runIP(t, "-6", "route", "replace", "default", "via", "fe80::2", "dev", uplinkName, "proto", "ra", "metric", "100")
		runIP(t, "-6", "addr", "del", "2001:db8:50::2/64", "dev", uplinkName)
		changed := time.Now()

		want := sortedCopy([]string{
			"0.0.0.0/1 dev tun0 metric 5", "128.0.0.0/1 dev tun0 metric 5", "::/1 dev tun0 metric 5", "8000::/1 dev tun0 metric 5",
			"203.0.113.10/32 via 10.20.30.1 dev ens18 metric 1", "2001:db8:ee::10/128 via fe80::2 dev ens18 metric 1",
		})
		eventually(t, "the bypass routes to follow", 5*time.Second, func() bool { return slices.Equal(l.ours(), want) })
		took := time.Since(changed)
		t.Logf("the bypass routes followed after %v", took.Round(time.Millisecond))
		if took > time.Second {
			t.Errorf("the bypass routes followed after %v, want less than a second", took)
		}
		eventually(t, "the engines to be asked to rebind", time.Second, func() bool { return rebinds.Load() >= 1 })
		l.expectRoute(v4Endpoint, decision{Dev: "ens18", Via: "10.20.30.1"})
		l.expectRoute(v6Endpoint, decision{Dev: "ens18", Via: "fe80::2"})
		l.expectProbe(v4Endpoint, "uplink")
		l.expectProbe(v6Endpoint, "uplink")
		if ns := r.Report().Net; ns.DefaultV4 == nil || ns.DefaultV4.Gateway != ip("10.20.30.1") {
			t.Errorf("the network state: %+v", ns.DefaultV4)
		}
		if stale := r.Report().Stale; len(stale) != 0 {
			t.Errorf("stale routes: %+v", stale)
		}
		if got := unresolvedRoutes(r); len(got) != 6 {
			t.Errorf("journal: %+v", got)
		}
	})

	t.Run("a cable is plugged in and its interface has the better default route", func(t *testing.T) {
		l, r, _ := setup(t)
		l.addUplink("ens19", "far1", "192.168.51", "2001:db8:51::")

		runIP(t, "route", "add", "default", "via", "192.168.51.1", "dev", "ens19", "proto", "dhcp", "metric", "50")
		// Every router announces itself as fe80::1.
		runIP(t, "-6", "route", "add", "default", "via", "fe80::1", "dev", "ens19", "proto", "ra", "metric", "50")
		changed := time.Now()

		want := sortedCopy([]string{
			"0.0.0.0/1 dev tun0 metric 5", "128.0.0.0/1 dev tun0 metric 5", "::/1 dev tun0 metric 5", "8000::/1 dev tun0 metric 5",
			"203.0.113.10/32 via 192.168.51.1 dev ens19 metric 1", "2001:db8:ee::10/128 via fe80::1 dev ens19 metric 1",
		})
		eventually(t, "the bypass routes to follow", 5*time.Second, func() bool { return slices.Equal(l.ours(), want) })
		if took := time.Since(changed); took > time.Second {
			t.Errorf("the bypass routes followed after %v, want less than a second", took)
		}
		l.expectRoute(v6Endpoint, decision{Dev: "ens19", Via: "fe80::1"})
		if stale := r.Report().Stale; len(stale) != 0 {
			t.Errorf("stale routes: %+v", stale)
		}

		// And back: the cable is pulled.
		runIP(t, "link", "del", "ens19")
		eventually(t, "the bypass routes to come back", 5*time.Second, func() bool { return slices.Equal(l.ours(), sortedCopy(fullTunnelRoutes)) })
	})
}

// Someone removes our routes. A route of the uplink going is a change that the
// monitor reports, and the pass puts everything back; the routes of the tunnel
// device are not reported, and are put back by the next change that is, or by
// Resync.
func TestKernelFlushOfOurRoutes(t *testing.T) {
	setup := func(t *testing.T) (*lab, *Reconciler) {
		l := newLab(t)
		l.tun("tun0", "10.9.0.2/24", "fd09::2/64")
		r := l.reconciler()
		l.run(r)
		announce(t, r, fullTunnelOfWG())
		l.expectOurs(fullTunnelRoutes...)
		return l, r
	}
	flush := func(t *testing.T, args ...string) {
		t.Helper()
		runIP(t, append([]string{"-4", "route", "flush"}, args...)...)
		runIP(t, append([]string{"-6", "route", "flush"}, args...)...)
	}

	t.Run("all of them", func(t *testing.T) {
		l, r := setup(t)
		flush(t, "proto", "199")
		l.expectOurs()
		eventually(t, "the routes to be back", 3*time.Second, func() bool { return slices.Equal(l.ours(), sortedCopy(fullTunnelRoutes)) })
		eventuallyInstalled(t, r, "wg", "0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1", v4Endpoint+"/32", v6Endpoint+"/128")
	})

	t.Run("the routes of the tunnel device", func(t *testing.T) {
		l, r := setup(t)
		flush(t, "dev", "tun0", "proto", "199")
		l.expectOurs("203.0.113.10/32 via 192.168.50.1 dev ens18 metric 1", "2001:db8:ee::10/128 via fe80::1 dev ens18 metric 1")
		l.expectProbe("8.8.8.8", "uplink") // traffic leaks until the Reconciler looks

		// The next change the monitor reports.
		runIP(t, "route", "add", "198.51.100.250/32", "dev", uplinkName)
		eventually(t, "the routes to be back", 3*time.Second, func() bool { return slices.Equal(l.ours(), sortedCopy(fullTunnelRoutes)) })
		l.expectProbe("8.8.8.8", "tun0")

		// Or Resync, which does not wait for anything.
		flush(t, "dev", "tun0", "proto", "199")
		if err := r.Resync(); err != nil {
			t.Fatal(err)
		}
		l.expectOurs(fullTunnelRoutes...)
		expectInstalled(t, r, "wg", "0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1")
	})
}

// A host route to an endpoint through a router that is not the default route's
// any more is stale. The Reconciler lists it, and RemoveStale removes it after
// looking again; a key that names no stale route is refused.
func TestKernelStaleHostRoute(t *testing.T) {
	l := newLab(t)
	l.tun("tun0", "10.9.0.2/24")
	r := l.reconciler()
	announce(t, r, endpoints(up("vpn", 1, "tun0", tunnel.RoleSplit, "10.50.0.0/16"), v4Endpoint))
	// What an earlier run left, its journal lost: a route through the router the
	// machine had before it joined another network. The old address stays, so the
	// router is still on a subnet of the interface.
	l.far.ip("addr", "add", "10.20.30.1/24", "dev", "far0")
	runIP(t, "addr", "add", "10.20.30.2/24", "dev", uplinkName)
	runIP(t, "route", "replace", "default", "via", "10.20.30.1", "dev", uplinkName, "proto", "dhcp", "metric", "100")
	runIP(t, "route", "add", "203.0.113.10/32", "via", routerV4, "dev", uplinkName, "proto", "static", "metric", "1")
	// A host route for another purpose through another router is nobody's business.
	runIP(t, "route", "add", "198.51.100.77/32", "via", routerV4, "dev", uplinkName, "proto", "static", "metric", "1")

	if err := r.Resync(); err != nil {
		t.Fatal(err)
	}

	stale := r.Report().Stale
	if len(stale) != 1 {
		t.Fatalf("stale routes: %+v", stale)
	}
	want := fmt.Sprintf("203.0.113.10/32@%d/192.168.50.1/1", ifIndexOf(t, uplinkName))
	if stale[0].Key != want || stale[0].Owned || stale[0].Reason != "gateway is not the default route's" {
		t.Errorf("stale route: %+v, want key %s", stale[0], want)
	}
	if err := r.RemoveStale(fmt.Sprintf("198.51.100.77/32@%d/192.168.50.1/1", ifIndexOf(t, uplinkName))); err == nil {
		t.Error("a key that names no stale route was accepted")
	}
	if _, ok := l.route("198.51.100.77/32", 1); !ok {
		t.Fatal("a route that is not stale was removed")
	}

	if err := r.RemoveStale(stale[0].Key); err != nil {
		t.Fatal(err)
	}

	if _, ok := l.route("203.0.113.10/32", 1); ok {
		t.Error("the stale route is still in the table")
	}
	if _, ok := l.route("198.51.100.77/32", 1); !ok {
		t.Error("the other host route was removed")
	}
	if left := r.Report().Stale; len(left) != 0 {
		t.Errorf("stale routes after the removal: %+v", left)
	}
	if err := r.RemoveStale(stale[0].Key); err == nil {
		t.Error("removing the same route twice was accepted")
	}
}

// The router of an interface is a link-local address, which only means
// something together with the interface. It goes through the kernel and the
// journal with its interface, and the router of the same name on another
// interface is not taken for it.
func TestKernelLinkLocalGatewayWithItsZone(t *testing.T) {
	l := newLab(t)
	l.tun("tun0", "10.9.0.2/24", "fd09::2/64")
	l.addUplink("ens19", "far1", "192.168.51", "2001:db8:51::")
	runIP(t, "-6", "route", "add", "default", "via", "fe80::1", "dev", "ens19", "proto", "ra", "metric", "200")
	intent := endpoints(up("wg", 1, "tun0", tunnel.RoleFull, "::/0"), v6Endpoint)
	r1 := l.reconciler()

	announce(t, r1, intent)

	rt, ok := l.route(v6Endpoint+"/128", 1)
	if !ok || rt.Gateway.String() != "fe80::1%ens18" || rt.Iface != "ens18" || rt.IfIndex != ifIndexOf(t, "ens18") || protocol(rt) != linux.RouteProtocol {
		t.Fatalf("the route as the table reports it: %+v (present %v)", rt, ok)
	}
	var rec record
	for _, got := range unresolvedRoutes(r1) {
		if got.Key == v6Endpoint+"/128" {
			rec = got
		}
	}
	wantFingerprint := fmt.Sprintf("fe80::1|%d|1|%#x", rt.IfIndex, 1<<16|linux.RouteProtocol<<8)
	if rec.Gateway != "fe80::1" || rec.IfIndex != rt.IfIndex || rec.Iface != "ens18" || rec.Metric != 1 || rec.Fingerprint != wantFingerprint {
		t.Errorf("journal record: %+v\nwant fingerprint %s", rec, wantFingerprint)
	}

	// The default route moves to ens19, which has a router of the same name.
	runIP(t, "-6", "route", "replace", "default", "via", "fe80::1", "dev", "ens19", "proto", "ra", "metric", "50")
	if err := r1.Resync(); err != nil {
		t.Fatal(err)
	}
	rt, ok = l.route(v6Endpoint+"/128", 1)
	if !ok || rt.Gateway.String() != "fe80::1%ens19" || rt.IfIndex != ifIndexOf(t, "ens19") {
		t.Errorf("the bypass route did not move to the other interface: %+v (present %v)", rt, ok)
	}
	if got := l.ours(); len(got) != 3 {
		t.Errorf("routes: %v", got)
	}

	// The daemon dies, and another program replaced the route by one of the same
	// destination and metric through the router of the interface that the journal
	// does not name.
	runIP(t, "-6", "route", "del", v6Endpoint+"/128", "dev", "ens19")
	runIP(t, "-6", "route", "add", v6Endpoint+"/128", "via", "fe80::1", "dev", "ens18", "proto", "static", "metric", "1")
	r2 := l.reconciler()
	rt, ok = l.route(v6Endpoint+"/128", 1)
	if !ok || rt.IfIndex != ifIndexOf(t, "ens18") {
		t.Errorf("the route through the router of another interface was removed or changed: %+v (present %v)", rt, ok)
	}
	if left := unresolvedRoutes(r2); len(left) != 0 {
		t.Errorf("the journal still lists %+v", left)
	}

	// A route of ours, through the router the journal names, goes.
	runIP(t, "-6", "route", "del", v6Endpoint+"/128", "dev", "ens18")
	announce(t, r2, intent)
	if rt, ok := l.route(v6Endpoint+"/128", 1); !ok || rt.IfIndex != ifIndexOf(t, "ens19") {
		t.Fatalf("setup: the bypass route is %+v (present %v)", rt, ok)
	}
	l.reconciler()
	if got := l.ours(); len(got) != 0 {
		t.Errorf("the next start left %v", got)
	}
}

// The network goes away and comes back, as when Wi-Fi is switched off and on:
// the kernel takes the routes through the interface with it, the tunnel's own
// stay, and the bypass routes are added again when a default route is.
func TestKernelNetworkGoesAndComesBack(t *testing.T) {
	l := newLab(t)
	l.tun("tun0", "10.9.0.2/24", "fd09::2/64")
	r := l.reconciler()
	rebinds := new(atomic.Int32)
	r.SetRebind(func() { rebinds.Add(1) })
	l.run(r)
	announce(t, r, fullTunnelOfWG())
	l.expectOurs(fullTunnelRoutes...)

	runIP(t, "link", "set", uplinkName, "down")

	eventually(t, "the bypass routes to be reported as waiting", 3*time.Second, func() bool {
		return reportOf(t, r, v4Endpoint+"/32", "wg").State == tunnel.RoutePending && reportOf(t, r, v6Endpoint+"/128", "wg").State == tunnel.RoutePending
	})
	if rr := reportOf(t, r, v4Endpoint+"/32", "wg"); rr.Detail != "no network" {
		t.Errorf("report: %+v", rr)
	}
	l.expectOurs("0.0.0.0/1 dev tun0 metric 5", "128.0.0.0/1 dev tun0 metric 5", "::/1 dev tun0 metric 5", "8000::/1 dev tun0 metric 5")
	if ns := r.Report().Net; ns.DefaultV4 != nil || ns.DefaultV6 != nil {
		t.Errorf("the network state still has default routes: %+v %+v", ns.DefaultV4, ns.DefaultV6)
	}
	if stale := r.Report().Stale; len(stale) != 0 {
		t.Errorf("stale routes: %+v", stale)
	}

	runIP(t, "link", "set", uplinkName, "up")
	runIP(t, "route", "add", "default", "via", routerV4, "dev", uplinkName, "proto", "dhcp", "metric", "100")
	eventually(t, "the link-local address of the interface", 5*time.Second, func() bool {
		return strings.Contains(runIP(t, "-6", "addr", "show", "dev", uplinkName, "scope", "link"), "fe80::")
	})
	runIP(t, "-6", "route", "add", "default", "via", routerV6, "dev", uplinkName, "proto", "ra", "metric", "100")
	back := time.Now()

	eventually(t, "the bypass routes to come back", 5*time.Second, func() bool { return slices.Equal(l.ours(), sortedCopy(fullTunnelRoutes)) })
	t.Logf("the bypass routes were back after %v", time.Since(back).Round(time.Millisecond))
	eventuallyInstalled(t, r, "wg", v4Endpoint+"/32", v6Endpoint+"/128")
	l.expectProbe(v4Endpoint, "uplink")
	l.expectProbe(v6Endpoint, "uplink")
	if rebinds.Load() == 0 {
		t.Error("the engines were not asked to rebind")
	}
}

// The daemon is told to stop: Run takes everything it put in the host out again,
// and the journal has nothing left for the next start.
func TestKernelRunRemovesEverythingWhenItStops(t *testing.T) {
	l := newLab(t)
	l.tun("tun0", "10.9.0.2/24", "fd09::2/64")
	r := l.reconciler()
	stop := l.run(r)
	announce(t, r, fullTunnelOfWG())
	l.expectOurs(fullTunnelRoutes...)
	if entries := l.dns.Entries("wg"); len(entries) != 1 {
		t.Fatalf("resolver entries: %+v", entries)
	}

	if err := stop(); err != nil {
		t.Errorf("Run: %v", err)
	}

	l.expectOurs()
	l.expectRoute("8.8.8.8", decision{Dev: "ens18", Via: routerV4})
	if owned, _ := l.dns.Owned(); len(owned) != 0 {
		t.Errorf("resolver entries left: %v", owned)
	}
	if err := r.Announce(fullTunnelOfWG()); err != ErrStopped {
		t.Errorf("Announce after Run ended: %v", err)
	}
	// What the journal holds is read again by the next start.
	r2 := l.reconciler()
	if left := unresolvedRoutes(r2); len(left) != 0 {
		t.Errorf("the next start finds %+v", left)
	}
}

// A profile that routes the network of its own tunnel: the kernel has a route
// for the subnet of the device already, with a metric of zero and the same
// interface, and the route of ours is no worse for it, and not overridden.
func TestKernelRouteToTheSubnetOfTheTunnelItself(t *testing.T) {
	l := newLab(t)
	l.tun("tun0", "10.8.0.6/24", "fd08::6/64")
	r := l.reconciler()

	announce(t, r, up("vpn", 1, "tun0", tunnel.RoleSplit, "10.8.0.0/24", "fd08::/64", "10.50.0.0/16"))

	expectInstalled(t, r, "vpn", "10.8.0.0/24", "fd08::/64", "10.50.0.0/16")
	l.expectOurs("10.8.0.0/24 dev tun0 metric 5", "fd08::/64 dev tun0 metric 5", "10.50.0.0/16 dev tun0 metric 5")
	l.expectRoute("10.8.0.99", decision{Dev: "tun0"})
	r.Withdraw("vpn")
	if _, ok := l.route("10.8.0.0/24", 0); !ok {
		t.Error("the route the kernel made for the address was removed")
	}
}
