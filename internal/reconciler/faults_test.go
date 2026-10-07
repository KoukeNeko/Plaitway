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

// wgBypass is the bypass route of wgIntent's endpoint through the home router.
var wgBypass = osnet.Route{Dst: pfx("203.0.113.10/32"), Gateway: ip("192.168.51.1"), Iface: "en0", Static: true}

func splitIntent() tunnel.Intent {
	return up("asus", 2, "utun11", tunnel.RoleSplit, "192.168.1.0/24")
}

// ErrUnreachable is not a failure: the route waits, and any later event tries
// again, even one that changed nothing.
func TestUnreachableWaitsForTheNextEvent(t *testing.T) {
	e := newEnv(t)
	e.addTunnel("utun11", "10.8.0.6/24")
	e.host.Routes.InjectFault(fake.Fault{Op: fake.OpAdd, Dst: pfx("192.168.1.0/24"), Err: osnet.ErrUnreachable})

	e.announce(splitIntent())

	e.checkTable()
	if rr := e.routeReport("192.168.1.0/24", "asus"); rr.State != tunnel.RoutePending || rr.Detail != "network unreachable" {
		t.Errorf("report: %+v", rr)
	}
	e.change(osnet.ChangeRoute)
	e.checkTable("192.168.1.0/24 dev utun11")
	if rr := e.routeReport("192.168.1.0/24", "asus"); rr.State != tunnel.RouteInstalled {
		t.Errorf("report: %+v", rr)
	}
}

// Nobody has to send an event for work that is only waiting.
func TestPendingWorkIsRetriedByTimer(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.RetryDelay = 20 * time.Millisecond })
	e.addTunnel("utun11", "10.8.0.6/24")
	e.run()
	e.host.Routes.InjectFault(fake.Fault{Op: fake.OpAdd, Dst: pfx("192.168.1.0/24"), Err: osnet.ErrUnreachable, Times: 2})

	e.announce(splitIntent())

	e.eventually("the retry to install the route", func() bool {
		return slices.Equal(e.table(), []string{"192.168.1.0/24 dev utun11"})
	})
}

// ESRCH is success: the route is gone, which is what was wanted.
func TestDeletingAMissingRouteIsSuccess(t *testing.T) {
	e := newEnv(t)
	e.addTunnel("utun11", "10.8.0.6/24")
	e.announce(splitIntent())
	// The route disappears between our read of the table and our delete.
	e.host.Routes.InjectFault(fake.Fault{Op: fake.OpDelete, Err: osnet.ErrNotFound})

	e.r.Withdraw("asus")

	if e.r.dirty || e.r.failStreak != 0 {
		t.Errorf("ESRCH counted as a failure: dirty %v, streak %d", e.r.dirty, e.r.failStreak)
	}
	if e.r.isOwned(pfx("192.168.1.0/24")) {
		t.Error("the route is still considered ours")
	}
}

// A route someone else removed is not deleted again, and ours comes back.
func TestRouteRemovedByAnotherProgramIsNotAnError(t *testing.T) {
	e := newEnv(t)
	e.addTunnel("utun11", "10.8.0.6/24")
	e.announce(splitIntent())
	e.host.Routes.Remove(pfx("192.168.1.0/24"))
	before := count(e.opLines(), "delete 192.168.1.0/24")

	e.r.Withdraw("asus")

	if n := count(e.opLines(), "delete 192.168.1.0/24"); n != before {
		t.Errorf("deleted a route that was not there")
	}
	if e.r.dirty {
		t.Error("counted as a failure")
	}
}

// The route table already holds the very route we want: that is as good as
// installing it, but it is not ours to remove.
func TestIdenticalForeignRouteIsAcceptedAndKept(t *testing.T) {
	e := newEnv(t)
	e.addTunnel("utun10", "10.6.0.2/24")
	e.host.Routes.Inject(wgBypass)

	e.announce(wgIntent())

	if rr := e.routeReport("203.0.113.10/32", "wg"); rr.State != tunnel.RouteInstalled {
		t.Errorf("report: %+v", rr)
	}
	if n := count(e.opLines(), "add 203.0.113.10/32 via 192.168.51.1"); n != 0 {
		t.Errorf("the route was added although it existed")
	}
	e.r.Withdraw("wg")
	if _, ok := e.host.Routes.Get(wgBypass.Dst); !ok {
		t.Error("a route we did not install was deleted")
	}
	if left := e.unresolved(); len(left) != 0 {
		t.Errorf("unresolved: %+v", left)
	}
}

// A different route on the same key is a conflict that is recorded and left
// alone; when it goes away ours follows.
func TestDifferentForeignRouteIsAConflict(t *testing.T) {
	e := newEnv(t)
	e.addTunnel("utun10", "10.6.0.2/24")
	foreign := osnet.Route{Dst: wgBypass.Dst, Gateway: ip("192.168.51.254"), Iface: "en0", Static: true}
	e.host.Routes.Inject(foreign)

	e.announce(wgIntent())

	if rr := e.routeReport("203.0.113.10/32", "wg"); rr.State != tunnel.RouteFailed || rr.Detail != "held by another program via 192.168.51.254" {
		t.Errorf("report: %+v", rr)
	}
	if got, _ := e.host.Routes.Get(wgBypass.Dst); got.Gateway != foreign.Gateway {
		t.Errorf("the foreign route was changed: %+v", got)
	}
	if ops := e.opLines(); count(ops, "delete 203.0.113.10/32") != 0 {
		t.Errorf("deleted the foreign route: %v", ops)
	}
	e.r.Withdraw("wg")
	if _, ok := e.host.Routes.Get(wgBypass.Dst); !ok {
		t.Error("withdrawing removed a route that is not ours")
	}

	e.announce(wgIntent())
	e.host.Routes.Remove(wgBypass.Dst)
	e.change(osnet.ChangeHeartbeat)
	if got, _ := e.host.Routes.Get(wgBypass.Dst); got.Gateway != wgBypass.Gateway {
		t.Errorf("ours should be installed once the conflict is gone: %+v", got)
	}
}

// racyTable makes a route appear just before our Add: someone else was
// quicker than our read of the table.
type racyTable struct {
	*fake.RouteTable
	rival osnet.Route
}

func (r racyTable) Add(rt osnet.Route) error {
	if rt.Dst == r.rival.Dst.Masked() {
		r.RouteTable.Inject(r.rival)
		return osnet.ErrExists
	}
	return r.RouteTable.Add(rt)
}

func TestExistsAfterTheReadIsReadBack(t *testing.T) {
	tests := []struct {
		name      string
		rival     osnet.Route
		wantState tunnel.RouteState
		wantDet   string
		wantNote  string
	}{
		{"identical", wgBypass, tunnel.RouteInstalled, "", "already present, not ours"},
		{"different", osnet.Route{Dst: wgBypass.Dst, Gateway: ip("192.168.51.254"), Iface: "en0", Static: true},
			tunnel.RouteFailed, "held by another program via 192.168.51.254", "held by another program"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.addTunnel("utun10", "10.6.0.2/24")
			e.r.routes = racyTable{e.host.Routes, tt.rival}

			e.announce(wgIntent())

			if rr := e.routeReport("203.0.113.10/32", "wg"); rr.State != tt.wantState || rr.Detail != tt.wantDet {
				t.Errorf("report: %+v", rr)
			}
			if e.r.isOwned(wgBypass.Dst) {
				t.Error("a route we did not add is considered ours")
			}
			var notes []string
			for _, rec := range e.journalFile() {
				if rec.Key == "203.0.113.10/32" && rec.State == stateRemoved {
					notes = append(notes, rec.Note)
				}
			}
			if !slices.Equal(notes, []string{tt.wantNote}) {
				t.Errorf("the pending record must be closed: %v", notes)
			}
			if left := e.unresolved(); len(left) != 1 || left[0].Kind != kindResolver {
				t.Errorf("only the resolver entry may be unresolved: %+v", left)
			}
		})
	}
}

// EEXIST on a route that is gone again by the time we read: try again later.
func TestExistsThatVanishedIsRetried(t *testing.T) {
	e := newEnv(t)
	e.addTunnel("utun10", "10.6.0.2/24")
	e.host.Routes.InjectFault(fake.Fault{Op: fake.OpAdd, Dst: wgBypass.Dst, Err: osnet.ErrExists})

	e.announce(wgIntent())

	if rr := e.routeReport("203.0.113.10/32", "wg"); rr.State != tunnel.RoutePending {
		t.Errorf("report: %+v", rr)
	}
	if !e.r.dirty {
		t.Error("nothing is scheduled to try again")
	}
	e.change(osnet.ChangeRoute)
	if got, _ := e.host.Routes.Get(wgBypass.Dst); got.Gateway != wgBypass.Gateway {
		t.Errorf("not installed on the retry: %+v", got)
	}
}

func TestOtherAddErrorsAreFailures(t *testing.T) {
	e := newEnv(t)
	e.addTunnel("utun10", "10.6.0.2/24")
	e.host.Routes.InjectFault(fake.Fault{Op: fake.OpAdd, Dst: wgBypass.Dst, Err: errBoom})

	e.announce(wgIntent())

	if rr := e.routeReport("203.0.113.10/32", "wg"); rr.State != tunnel.RouteFailed || rr.Detail != "boom" {
		t.Errorf("report: %+v", rr)
	}
	if got := e.journalFor(kindRoute, "203.0.113.10/32"); !slices.Equal(got, []string{"pending 192.168.51.1", "removed 192.168.51.1"}) {
		t.Errorf("a failed add must not stay pending: %v", got)
	}
	// One subsystem failing does not stop the others.
	e.checkTable("0.0.0.0/1 dev utun10", "128.0.0.0/1 dev utun10", "::/1 dev utun10", "8000::/1 dev utun10")
	if len(e.host.DNS.All()) != 1 {
		t.Error("DNS should have been applied")
	}
	if e.r.failStreak != 1 {
		t.Errorf("failStreak %d", e.r.failStreak)
	}
}

func TestUnreadableRoutingTableDoesNotStopDNS(t *testing.T) {
	e := newEnv(t)
	e.bothTunnels()
	e.host.Routes.InjectFault(fake.Fault{Op: fake.OpDump, Err: errBoom})

	e.announce(wgIntent())

	e.checkTable()
	if rr := e.routeReport("0.0.0.0/1", "wg"); rr.State != tunnel.RouteFailed {
		t.Errorf("report: %+v", rr)
	}
	if len(e.host.DNS.All()) != 1 {
		t.Error("DNS should have been applied")
	}
	e.change(osnet.ChangeHeartbeat)
	e.checkTable("0.0.0.0/1 dev utun10", "128.0.0.0/1 dev utun10", "203.0.113.10/32 via 192.168.51.1 dev en0", "::/1 dev utun10", "8000::/1 dev utun10")
}

func TestDNSFailureDoesNotStopRoutes(t *testing.T) {
	e := newEnv(t)
	e.bothTunnels()
	e.host.DNS.Fail("apply", errBoom, 1)

	e.announce(asusIntent())

	e.checkTable("192.168.1.0/24 dev utun11")
	if dr := e.dnsReport("asus"); dr.State != tunnel.RouteFailed {
		t.Errorf("report: %+v", dr)
	}
	if e.r.dirty != true {
		t.Error("the DNS entry must be tried again")
	}
	e.change(osnet.ChangeRoute)
	if dr := e.dnsReport("asus"); dr.State != tunnel.RouteInstalled {
		t.Errorf("report after the retry: %+v", dr)
	}
	if owned, _ := e.host.DNS.Owned(); !slices.Equal(owned, []string{"asus"}) {
		t.Errorf("owned: %v", owned)
	}
}

func TestDNSRemovalFailureIsRetried(t *testing.T) {
	e := newEnv(t)
	e.bothTunnels()
	e.announce(asusIntent())
	e.host.DNS.Fail("remove", errBoom, 1)

	e.r.Withdraw("asus")
	e.checkTable()
	if owned, _ := e.host.DNS.Owned(); len(owned) != 1 {
		t.Fatalf("the entry should still be there: %v", owned)
	}
	e.change(osnet.ChangeHeartbeat)
	if owned, _ := e.host.DNS.Owned(); len(owned) != 0 {
		t.Errorf("the retry should remove it: %v", owned)
	}
}

// Write-ahead: what the journal cannot record is not applied, because a crash
// would leave it behind for good.
func TestNothingIsAppliedThatCannotBeJournaled(t *testing.T) {
	e := newEnv(t)
	e.bothTunnels()
	e.r.journal.file.Close() // the disk went away

	e.announce(wgIntent())

	// Interface-bound routes vanish with the interface and need no journal.
	e.checkTable("0.0.0.0/1 dev utun10", "128.0.0.0/1 dev utun10", "::/1 dev utun10", "8000::/1 dev utun10")
	if rr := e.routeReport("203.0.113.10/32", "wg"); rr.State != tunnel.RouteFailed {
		t.Errorf("the bypass must not be added without a journal record: %+v", rr)
	}
	if dr := e.dnsReport("wg"); dr.State != tunnel.RouteFailed {
		t.Errorf("neither may the resolver entry: %+v", dr)
	}
	if owned, _ := e.host.DNS.Owned(); len(owned) != 0 {
		t.Errorf("resolver entries: %v", owned)
	}
}

// A route of ours that another program replaced is theirs: never deleted, never
// altered, not even by a withdraw.
func TestReplacedRouteIsNeverTouched(t *testing.T) {
	e := newEnv(t)
	e.addTunnel("utun11", "10.8.0.6/24")
	e.announce(splitIntent())
	replacement := osnet.Route{Dst: pfx("192.168.1.0/24"), Iface: "en0", Static: true}
	e.host.Routes.Inject(replacement)

	e.change(osnet.ChangeHeartbeat)

	if rr := e.routeReport("192.168.1.0/24", "asus"); rr.State != tunnel.RouteFailed || rr.Detail != "held by another program via en0" {
		t.Errorf("report: %+v", rr)
	}
	e.r.Withdraw("asus")
	if got, ok := e.host.Routes.Get(replacement.Dst); !ok || got.Iface != "en0" {
		t.Errorf("the replacement was touched: %+v %v", got, ok)
	}
}

func TestSnapshotFailureIsSurvived(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.RetryDelay = 20 * time.Millisecond })
	e.host.AddTunnel("utun11", pfx("10.8.0.6/24"))
	e.run()
	e.announce(endpoints(up("ovpn", 1, "utun11", tunnel.RoleFull, "0.0.0.0/0"), "198.51.100.88"))

	e.host.Net.FailSnapshot(errBoom)
	e.host.MoveNetwork("en0", pfx("10.20.30.7/24"), ip("10.20.30.1"))
	e.host.Net.Emit(osnet.Change{Reason: osnet.ChangeRoute})
	time.Sleep(60 * time.Millisecond) // several retries fail
	if !slices.Contains(e.table(), "198.51.100.88/32 via 192.168.51.1 dev en0") {
		t.Error("nothing should change while the network cannot be read")
	}

	e.host.Net.FailSnapshot(nil)
	e.eventually("the retry to repair the route", func() bool {
		return slices.Contains(e.table(), "198.51.100.88/32 via 10.20.30.1 dev en0")
	})
}

// refusingDNS is the fake DNS with the refusals of a real adapter: reject
// returns the error of an Apply it will not carry out, and every attempt is
// counted.
type refusingDNS struct {
	osnet.DNSConfigurator
	reject   func(owner string, entries []osnet.DNSEntry) error
	attempts atomic.Int32
}

func (d *refusingDNS) Apply(owner string, entries []osnet.DNSEntry) error {
	d.attempts.Add(1)
	if err := d.reject(owner, entries); err != nil {
		return err
	}
	return d.DNSConfigurator.Apply(owner, entries)
}

// refusesUnusableServers is what the macOS adapter does with a nameserver it
// cannot write into a resolver entry.
func refusesUnusableServers(_ string, entries []osnet.DNSEntry) error {
	for _, entry := range entries {
		for _, s := range entry.Servers {
			if s.Zone() != "" || s.IsUnspecified() || s.IsMulticast() {
				return fmt.Errorf("invalid DNS server %q", s)
			}
		}
	}
	return nil
}

// An Apply that failed may have written some of the entries. The owner stays
// known, so that withdrawing it removes them: a catch-all resolver that points
// at a dead tunnel breaks every lookup on the machine.
func TestFailedDNSApplyIsStillRemovedOnWithdraw(t *testing.T) {
	e := newEnv(t)
	e.bothTunnels()
	e.announce(asusIntent())
	if owned, _ := e.host.DNS.Owned(); !slices.Equal(owned, []string{"asus"}) {
		t.Fatalf("setup: %v", owned)
	}

	// Another nameserver for the same domain; the write fails, the old entry stays.
	e.host.DNS.Fail("apply", errBoom, 1)
	e.announce(nameserver(endpoints(up("asus", 2, "utun11", tunnel.RoleSplit, "192.168.1.0/24"), "198.51.100.7"), "192.168.1.2", "asus.lan"))
	if dr := e.dnsReport("asus"); dr.State != tunnel.RouteFailed {
		t.Fatalf("report: %+v", dr)
	}
	e.r.Withdraw("asus")

	if owned, _ := e.host.DNS.Owned(); len(owned) != 0 {
		t.Errorf("the entry of a withdrawn tunnel is left behind: %v", owned)
	}
	if left := e.unresolved(); len(left) != 0 {
		t.Errorf("the journal still lists %+v", left)
	}
}

// The adapter refuses a nameserver that cannot be written into a resolver
// entry, however often it is asked. A full tunnel "reaches" 0.0.0.0 through its
// 0/1, so Compute has to leave such an address out; if it passes it on, every
// pass fails and the whole machine is rebuilt every few passes.
func TestNameserverTheAdapterRefusesDoesNotRebuildEverything(t *testing.T) {
	e := newEnv(t)
	e.bothTunnels()
	dns := &refusingDNS{DNSConfigurator: e.host.DNS, reject: refusesUnusableServers}
	e.r.dns = dns

	e.announce(nameserver(endpoints(up("wg", 1, "utun10", tunnel.RoleFull, "0.0.0.0/0", "::/0"), "203.0.113.10"), "0.0.0.0", "."))
	for range 10 {
		e.change(osnet.ChangeHeartbeat)
	}

	if n := count(e.opLines(), "delete 0.0.0.0/1"); n != 0 {
		t.Errorf("the default routes were taken away %d times: %v", n, e.opLines())
	}
	if n := dns.attempts.Load(); n != 0 {
		t.Errorf("the adapter was asked %d times to write a nameserver it cannot", n)
	}
	if dr := e.dnsReport("wg"); dr.State != tunnel.RouteBlocked || dr.Detail != "0.0.0.0 is not a usable nameserver" {
		t.Errorf("report: %+v", dr)
	}
	e.checkTable("0.0.0.0/1 dev utun10", "128.0.0.0/1 dev utun10", "203.0.113.10/32 via 192.168.51.1 dev en0", "::/1 dev utun10", "8000::/1 dev utun10")
}

// A rebuild cannot cure what it has just failed to cure. The failure stays in
// the report and is tried again, but the healthy tunnels are not taken apart
// every few passes because of it; something new going wrong still is answered
// with a rebuild.
func TestResetIsNotRepeatedForAFailureItDidNotCure(t *testing.T) {
	e := newEnv(t)
	e.bothTunnels()
	e.r.dns = &refusingDNS{DNSConfigurator: e.host.DNS, reject: func(owner string, _ []osnet.DNSEntry) error {
		if owner == "asus" {
			return errBoom
		}
		return nil
	}}
	e.announce(wgIntent())
	e.announce(asusIntent())
	for range 30 {
		e.change(osnet.ChangeHeartbeat)
	}

	if n := count(e.opLines(), "delete 0.0.0.0/1"); n != 1 {
		t.Errorf("the rebuild should be tried once, not %d times", n)
	}
	if dr := e.dnsReport("asus"); dr.State != tunnel.RouteFailed {
		t.Errorf("the failure must stay reported: %+v", dr)
	}
	if !e.r.dirty {
		t.Error("the failed entry must still be tried again")
	}
	e.checkTable("0.0.0.0/1 dev utun10", "128.0.0.0/1 dev utun10", "192.168.1.0/24 dev utun11", "198.51.100.7/32 via 192.168.51.1 dev en0", "203.0.113.10/32 via 192.168.51.1 dev en0", "::/1 dev utun10", "8000::/1 dev utun10")

	e.addTunnel("utun12", "10.9.0.2/24")
	e.host.Routes.InjectFault(fake.Fault{Op: fake.OpAdd, Dst: pfx("10.9.0.0/16"), Err: errBoom, Times: 3})
	e.announce(up("c", 3, "utun12", tunnel.RoleSplit, "10.9.0.0/16"))
	e.change(osnet.ChangeHeartbeat)
	e.change(osnet.ChangeHeartbeat)
	if n := count(e.opLines(), "delete 0.0.0.0/1"); n != 2 {
		t.Errorf("a new failure that keeps failing should still be answered with a rebuild, got %d", n)
	}
	if rr := e.routeReport("10.9.0.0/16", "c"); rr.State != tunnel.RouteInstalled {
		t.Errorf("the rebuild should have fixed it: %+v", rr)
	}
}

// Failed work is tried again with growing pauses, not every RetryDelay for ever.
func TestRetryBacksOffWhileSomethingKeepsFailing(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.RetryDelay = 20 * time.Millisecond })
	e.bothTunnels()
	dns := &refusingDNS{DNSConfigurator: e.host.DNS, reject: func(string, []osnet.DNSEntry) error { return errBoom }}
	e.r.dns = dns
	e.run()

	e.announce(asusIntent())
	time.Sleep(700 * time.Millisecond)

	// About 20, 40, 80, 160 and 320 ms apart: six attempts and a rebuild. Without
	// a backoff there would be more than thirty.
	n := dns.attempts.Load()
	if n < 2 || n > 15 {
		t.Errorf("%d attempts in 700 ms", n)
	}
}
