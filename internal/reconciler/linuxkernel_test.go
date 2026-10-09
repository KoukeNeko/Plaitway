package reconciler

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/osnet/fake"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// What running the Reconciler against the Linux kernel showed
// (rootintegration_linux_test.go), as tests that run on every machine.

// The monitor reports nothing about the devices of tunnels, which their engines
// make after they announced: the route waits for the interface and a timer
// brings the pass that finds it. The same wait on Windows is ended by the
// event that tells of the adapter (TestVanishedInterfaceIsNotRetried...).
func TestLinuxRouteWaitingForItsInterfaceIsTriedAgain(t *testing.T) {
	e := newEnv(t, linuxCase)

	e.announce(up("late", 1, "tun3", tunnel.RoleSplit, "10.50.0.0/16"))

	if rr := e.routeReport("10.50.0.0/16", "late"); rr.State != tunnel.RoutePending || rr.Detail != "interface tun3 not found" {
		t.Fatalf("report: %+v", rr)
	}
	if !e.r.dirty || e.r.failedPasses != 0 {
		t.Errorf("a route that waits for its interface leaves work for a timer, and is no failure: dirty %v, failed %d", e.r.dirty, e.r.failedPasses)
	}

	e.host.AddTunnel("tun3", pfx("10.8.0.6/24")) // no event for it
	e.r.handle(osnet.Change{Reason: osnet.ChangeHeartbeat, At: t0}, false)

	e.checkIPRoutes("10.50.0.0/16 dev tun3 metric 5")
	e.expectInstalled("10.50.0.0/16", "late")
	e.expectSettled()
}

// The host refuses a route for a reason that is its own setting, as IPv6
// switched off on the tunnel's device (the adapter says errors.ErrUnsupported).
// The route is reported failed with the reason; neither a retry loop nor a
// rebuild of the working IPv4 routes follows, since neither changes the setting.
func TestLinuxRefusalOfTheHostIsNotAFailedPass(t *testing.T) {
	e := newEnv(t, linuxCase)
	e.addTunnel("tun0", "10.8.0.6/24")
	refusal := fmt.Errorf("add route: %w", fmt.Errorf("%w", errors.ErrUnsupported))
	for _, half := range []string{"::/1", "8000::/1"} {
		e.host.Routes.InjectFault(fake.Fault{Op: fake.OpAdd, Dst: pfx(half), Err: refusal, Times: 100})
	}

	e.announce(up("vpn", 1, "tun0", tunnel.RoleFull, "0.0.0.0/0", "::/0"))
	for range 4 {
		e.change(osnet.ChangeHeartbeat)
		e.change(osnet.ChangeRoute)
	}

	e.checkIPRoutes("0.0.0.0/1 dev tun0 metric 5", "128.0.0.0/1 dev tun0 metric 5")
	e.expectInstalled("0.0.0.0/1", "vpn")
	for _, half := range []string{"::/1", "8000::/1"} {
		if rr := e.routeReport(half, "vpn"); rr.State != tunnel.RouteFailed || rr.Overridden || rr.Detail != refusal.Error() {
			t.Errorf("%s: %+v, want failed with the reason of the host", half, rr)
		}
	}
	if e.r.dirty || e.r.failedPasses != 0 || e.r.failStreak != 0 {
		t.Errorf("the host's setting is no failure of the pass: dirty %v, failed %d, streak %d", e.r.dirty, e.r.failedPasses, e.r.failStreak)
	}
	if strings.Contains(e.logs.String(), "rebuilding from scratch") || strings.Contains(e.logs.String(), "reconciliation incomplete") {
		t.Errorf("the Reconciler took it for a pass that does not converge:\n%s", e.logs)
	}
	for _, line := range e.ipOps() {
		if strings.HasPrefix(line, "delete") {
			t.Errorf("a route was removed again: %s", line)
		}
	}
	for _, rec := range e.unresolved() {
		if rec.Key == "::/1" || rec.Key == "8000::/1" {
			t.Errorf("the journal lists a route that was never added: %+v", rec)
		}
	}
}

// systemd-resolved keeps the settings of a link by its index, so an interface
// that is made again under the same name has none of them, and the entries are
// written again. The same announcement for an interface that stayed is not.
func TestLinuxResolverEntriesAreWrittenAgainForAnInterfaceMadeAgain(t *testing.T) {
	e := newEnv(t, linuxCase)
	e.addTunnel("tun0", "10.8.0.6/24")
	in := nameserver(withGateways(up("ovpn", 1, "tun0", tunnel.RoleSplit, "192.168.1.0/24"), "10.8.0.1", ""), "192.168.1.1", "asus.lan")
	e.announce(in)
	applies := count(dnsOps(e), "apply")
	if applies != 1 {
		t.Fatalf("setup: %d writes", applies)
	}
	oldIndex := e.host.ifIndex("tun0")

	e.announce(in)
	e.change(osnet.ChangeHeartbeat)
	if n := count(dnsOps(e), "apply"); n != applies {
		t.Fatalf("the interface stayed, yet the entries were written again (%d, was %d)", n, applies)
	}

	e.host.DestroyInterface("tun0")
	e.host.AddTunnel("tun0", pfx("10.8.0.6/24"))
	if e.host.ifIndex("tun0") == oldIndex {
		t.Fatalf("setup: the new interface has the old index %d", oldIndex)
	}
	e.announce(in)

	if n := count(dnsOps(e), "apply"); n != applies+1 {
		t.Errorf("the entries should be written once more: %d writes, was %d", n, applies)
	}
	if got := e.host.DNS.Entries("ovpn"); len(got) != 1 || got[0].Iface != "tun0" {
		t.Errorf("entries: %+v", got)
	}
	e.checkIPRoutes("192.168.1.0/24 dev tun0 metric 5")
	e.expectInstalled("192.168.1.0/24", "ovpn")

	e.announce(in)
	if n := count(dnsOps(e), "apply"); n != applies+1 {
		t.Errorf("the entries were written again for nothing: %d writes", n)
	}
}

// A route of another program that sends nowhere in particular is named as such
// in the report: it has neither a gateway nor an interface.
func TestLinuxForeignBlackholeIsNamed(t *testing.T) {
	e := newEnv(t, linuxCase)
	e.addTunnel("tun0", "10.8.0.6/24")
	e.host.Inject(osnet.Route{Dst: pfx("10.50.0.0/16"), Blackhole: true, Metric: tunnelMetric, Static: true})
	e.host.Inject(osnet.Route{Dst: pfx("10.60.0.0/16"), Blackhole: true, Metric: 1, Static: true})

	e.announce(up("vpn", 1, "tun0", tunnel.RoleSplit, "10.50.0.0/16", "10.60.0.0/16"))

	e.expectConflict("10.50.0.0/16", "vpn", "held by another program via blackhole")
	if rr := e.routeReport("10.60.0.0/16", "vpn"); rr.State != tunnel.RouteFailed || !rr.Overridden ||
		rr.Detail != "overridden by 10.60.0.0/16 via blackhole: effective metric 1, ours 5" {
		t.Errorf("10.60.0.0/16: %+v", rr)
	}
}
