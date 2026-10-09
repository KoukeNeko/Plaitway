//go:build rootintegration && linux

package reconciler

// A long route list, and many things at once. See rootlab_linux_test.go for how
// to run these.

import (
	"fmt"
	"math/rand/v2"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/osnet/linux"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// pushedRoutes is the route list a large VPN pushes: v4 networks of a /24 and v6
// networks of a /48, none of them in the ranges of the lab.
func pushedRoutes(v4, v6 int) []string {
	var out []string
	for i := range v4 {
		out = append(out, fmt.Sprintf("10.%d.%d.0/24", 20+i/250, i%250))
	}
	for i := range v6 {
		out = append(out, fmt.Sprintf("2001:db8:%x::/48", 0x1000+i))
	}
	return out
}

func countProto199(t *testing.T) int {
	t.Helper()
	n := 0
	for _, family := range []string{"-4", "-6"} {
		out := strings.TrimSpace(runIP(t, family, "route", "show", "proto", "199"))
		if out != "" {
			n += strings.Count(out, "\n") + 1
		}
	}
	return n
}

// A few thousand prefixes are added and removed in a time a person does not
// notice, each journaled before and after it is added, and a pass that has
// nothing to do costs little.
func TestKernelManyRoutes(t *testing.T) {
	const v4, v6 = 4000, 1000
	l := newLab(t)
	// The journal is synced to disk twice for every route; the temporary directory
	// of the tests is often memory.
	if dir, err := os.MkdirTemp("/var/tmp", "plaitway-journal-"); err == nil {
		t.Cleanup(func() { os.RemoveAll(dir) })
		l.journal = filepath.Join(dir, "journal")
	} else {
		t.Logf("the journal is in the temporary directory, which may not sync: %v", err)
	}
	l.tun("tun0", "172.30.0.2/24", "fd30::2/64")
	r := l.reconciler()
	routes := pushedRoutes(v4, v6)
	in := endpoints(up("big", 1, "tun0", tunnel.RoleSplit, routes...), v4Endpoint, v6Endpoint)
	in.Routes = append(in.Routes, pfx("10.200.0.0/16"))

	timed := func(what string, f func()) time.Duration {
		start := time.Now()
		f()
		took := time.Since(start)
		t.Logf("%-34s %v", what, took.Round(time.Millisecond))
		return took
	}

	if took := timed(fmt.Sprintf("announce %d routes", len(in.Routes)), func() { announce(t, r, in) }); took > 60*time.Second {
		t.Errorf("announcing took %v", took)
	}
	if got := countProto199(t); got != v4+v6+1 {
		t.Fatalf("the kernel has %d routes with protocol %d, want %d", got, linux.RouteProtocol, v4+v6+1)
	}
	if got := len(unresolvedRoutes(r)); got != v4+v6+1 {
		t.Errorf("the journal holds %d routes", got)
	}
	for _, rr := range r.Report().Routes {
		if rr.State != tunnel.RouteInstalled {
			t.Fatalf("not installed: %+v", rr)
		}
	}
	l.expectRoute("10.20.5.77", decision{Dev: "tun0"})
	l.expectRoute("10.35.249.1", decision{Dev: "tun0"})
	l.expectRoute("2001:db8:13e7::1", decision{Dev: "tun0"})

	if took := timed("a pass with nothing to do", func() { r.handle(osnet.Change{Reason: osnet.ChangeHeartbeat, At: time.Now()}, false) }); took > 5*time.Second {
		t.Errorf("a pass that changes nothing took %v", took)
	}
	if got := countProto199(t); got != v4+v6+1 {
		t.Errorf("the idle pass changed the table: %d routes", got)
	}

	// A hundred of them change.
	changed := in
	changed.Routes = slices.Concat(in.Routes[100:], pfxs("10.150.0.0/24", "10.150.1.0/24", "10.150.2.0/24"))
	timed("announce, 100 gone and 3 new", func() { announce(t, r, changed) })
	if got := countProto199(t); got != v4+v6+1-100+3 {
		t.Errorf("the kernel has %d routes, want %d", got, v4+v6+1-100+3)
	}

	timed("Resync", func() {
		if err := r.Resync(); err != nil {
			t.Fatal(err)
		}
	})
	if got := countProto199(t); got != v4+v6+1-100+3 {
		t.Errorf("after Resync: %d routes", got)
	}

	timed("Withdraw", func() { r.Withdraw("big") })
	l.expectOurs()
	if got := len(unresolvedRoutes(r)); got != 0 {
		t.Errorf("the journal still holds %d routes", got)
	}
}

// Several engines announce, change their minds and withdraw at once while the
// network under them changes and the table is read. Run with -race. At the end
// what is in the kernel is what the Reconciler computed for the last
// announcements, and nothing else.
func TestKernelConcurrentAnnouncesWhileTheNetworkChanges(t *testing.T) {
	const owners, rounds = 6, 60
	l := newLab(t)
	for i := range owners {
		l.tun(fmt.Sprintf("tun%d", i), fmt.Sprintf("10.%d.0.2/24", 100+i), fmt.Sprintf("fd%02x::2/64", 100+i))
	}
	r := l.reconciler(func(c *Config) { c.RetryDelay = 20 * time.Millisecond })
	var rebinds int
	var rebindMu sync.Mutex
	r.SetRebind(func() { rebindMu.Lock(); rebinds++; rebindMu.Unlock() })
	l.run(r)

	// The prefixes of the owners overlap, so that they compete, and the
	// endpoints are captured by the halves of whoever has them.
	pool := []string{"10.60.0.0/16", "10.61.0.0/16", "10.62.0.0/16", "172.20.0.0/14", "192.168.200.0/24", "2001:db8:aa::/48", "2001:db8:bb::/48"}
	intentOf := func(i, round int, rnd *rand.Rand) tunnel.Intent {
		var routes []netip.Prefix
		for _, p := range pool {
			if rnd.IntN(2) == 0 {
				routes = append(routes, pfx(p))
			}
		}
		in := tunnel.Intent{
			Owner: tunnel.OwnerID(fmt.Sprintf("owner%d", i)), State: tunnel.StateUp, Iface: fmt.Sprintf("tun%d", i),
			Role: tunnel.RoleSplit, Priority: i % 3, UpSince: t0.Add(time.Duration(i) * time.Second), Routes: routes,
			Endpoints: []netip.Addr{ip(fmt.Sprintf("203.0.113.%d", 10+i)), ip(fmt.Sprintf("2001:db8:ee::%d", 10+i))},
			DNS:       []tunnel.DNSIntent{{Servers: ips(fmt.Sprintf("10.%d.0.1", 100+i)), MatchDomains: []string{fmt.Sprintf("owner%d.lan", i)}}},
		}
		if i == 0 && round%2 == 0 {
			in.Role, in.Routes = tunnel.RoleFull, append(in.Routes, pfx("0.0.0.0/0"), pfx("::/0"))
		}
		if round%7 == 3 {
			in.State = tunnel.StateConnecting
		}
		return in
	}

	stop := make(chan struct{})
	var churn, readers, announcers sync.WaitGroup
	errs := make(chan error, 256)
	fail := func(err error) {
		select {
		case errs <- err:
		default:
		}
	}

	// The network changes: routes come and go on the uplink, an address is added and
	// removed, the default route is replaced by one with another metric, a foreign
	// route sits on a prefix of the pool.
	churn.Add(1)
	go func() {
		defer churn.Done()
		for n := 0; ; n++ {
			select {
			case <-stop:
				return
			default:
			}
			steps := [][]string{
				{"route", "add", "198.51.100.250/32", "dev", uplinkName},
				{"route", "del", "198.51.100.250/32", "dev", uplinkName},
				{"addr", "add", "192.168.60.2/24", "dev", uplinkName},
				{"route", "replace", "default", "via", routerV4, "dev", uplinkName, "proto", "dhcp", "metric", "101"},
				{"route", "add", "10.61.0.0/16", "dev", uplinkName, "proto", "static", "metric", "1"},
				{"addr", "del", "192.168.60.2/24", "dev", uplinkName},
				{"route", "replace", "default", "via", routerV4, "dev", uplinkName, "proto", "dhcp", "metric", "100"},
				{"route", "del", "10.61.0.0/16", "dev", uplinkName, "metric", "1"},
			}
			if err := tryIP(steps[n%len(steps)]...); err != nil {
				fail(err)
			}
			time.Sleep(time.Duration(2+n%5) * time.Millisecond)
		}
	}()

	for range 2 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				rep := r.Report()
				_ = len(rep.Routes) + len(rep.DNS) + len(rep.Stale) + len(rep.Journal)
				for _, rr := range rep.Routes {
					_ = rr.Detail
				}
				time.Sleep(time.Millisecond)
			}
		}()
	}

	// Resync and RemoveStale are called by the daemon's management side at any
	// time.
	readers.Add(1)
	go func() {
		defer readers.Done()
		for n := 0; ; n++ {
			select {
			case <-stop:
				return
			case <-r.Changed():
			case <-time.After(15 * time.Millisecond):
			}
			if n%4 == 0 {
				if err := r.Resync(); err != nil && !strings.Contains(err.Error(), "held by another program") {
					t.Logf("Resync: %v", err)
				}
			}
			_ = r.RemoveStale("203.0.113.10/32@1/192.168.50.1/1") // names nothing stale
		}
	}()

	for i := range owners {
		announcers.Add(1)
		go func() {
			defer announcers.Done()
			rnd := rand.New(rand.NewPCG(uint64(i), 7))
			for round := range rounds {
				if err := r.Announce(intentOf(i, round, rnd)); err != nil {
					fail(err)
				}
				if round%9 == 8 {
					r.Withdraw(tunnel.OwnerID(fmt.Sprintf("owner%d", i)))
				}
				time.Sleep(time.Duration(rnd.IntN(8)) * time.Millisecond)
			}
		}()
	}
	announcers.Wait()
	close(stop)
	churn.Wait()
	readers.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent call failed: %v", err)
	}

	// The network is as it was. Everybody announces the last time, and the
	// Reconciler settles.
	_ = tryIP("addr", "del", "192.168.60.2/24", "dev", uplinkName)
	_ = tryIP("route", "del", "10.61.0.0/16", "dev", uplinkName, "metric", "1")
	runIP(t, "route", "replace", "default", "via", routerV4, "dev", uplinkName, "proto", "dhcp", "metric", "100")
	rnd := rand.New(rand.NewPCG(99, 7))
	for i := range owners {
		announce(t, r, intentOf(i, 2, rnd))
	}
	if err := r.Resync(); err != nil {
		t.Fatal(err)
	}

	var want []string
	r.applyMu.Lock() // Run may still be looking at the last changes of the network
	for _, p := range r.desired.Routes {
		if p.Install {
			want = append(want, ipLine(p.Route))
		}
	}
	owned := len(r.owned)
	r.applyMu.Unlock()
	if len(want) == 0 {
		t.Fatal("setup: nothing is wanted")
	}
	l.expectOurs(want...)
	if owned != len(want) {
		t.Errorf("the Reconciler owns %d routes, the plan has %d", owned, len(want))
	}
	if got := unresolvedRoutes(r); len(got) != len(want) {
		t.Errorf("the journal holds %d routes, want %d", len(got), len(want))
	}
	rebindMu.Lock()
	t.Logf("%d routes wanted, %d rebinds", len(want), rebinds)
	rebindMu.Unlock()

	for i := range owners {
		r.Withdraw(tunnel.OwnerID(fmt.Sprintf("owner%d", i)))
	}
	l.expectOurs()
	if got := unresolvedRoutes(r); len(got) != 0 {
		t.Errorf("the journal still holds %+v", got)
	}
	if owned, _ := l.dns.Owned(); len(owned) != 0 {
		t.Errorf("resolver entries left: %v", owned)
	}
}
