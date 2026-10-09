package reconciler

import (
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/osnet/fake"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

var (
	// Few prefixes, so that tunnels collide: overlaps, endpoints inside routes,
	// a route that is an endpoint, the networks the machine moves between.
	routePool    = []string{"10.0.0.0/8", "10.1.0.0/16", "10.2.0.0/16", "172.16.0.0/12", "192.168.1.0/24", "192.168.50.0/24", "203.0.113.0/24", "203.0.113.10/32", "fd00::/64", "0.0.0.0/0", "::/0"}
	endpointPool = []string{"203.0.113.10", "198.51.100.7", "192.168.51.1", "10.20.30.1", "2001:db8::10"}
	serverPool   = []string{"192.168.51.1", "192.168.1.1", "10.1.0.53", "192.168.50.1", "10.101.1.1"}
	domainPool   = []string{"corp.lan", "other.lan", "."}
	gatewayPool  = []string{"192.168.51.1", "192.168.51.254", "10.20.30.1", "10.99.0.1", "192.168.1.1"}
	networks     = []struct{ addr, gateway string }{
		{"192.168.51.185/24", "192.168.51.1"},
		{"10.20.30.7/24", "10.20.30.1"},
		{"192.168.1.50/24", "192.168.1.1"}, // the remote site's subnet: conflicts
	}
	ownerNames = []tunnel.OwnerID{"a", "b", "c", "d"}
)

// world drives an env with random events and checks the invariants after each.
type world struct {
	t       *testing.T
	e       *env
	rng     *rand.Rand
	intents map[tunnel.OwnerID]tunnel.Intent
	// foreign are the routes other programs put in the table, as they were
	// right after the injection: they must come out the way they went in.
	foreign map[routeKey]osnet.Route
	// added are the routes the Reconciler added, from the operation log; it may
	// delete only those.
	added   map[routeKey]osnet.Route
	allowed map[routeKey]bool // foreign routes the user asked to remove
	opsSeen int
	rebinds atomic.Int32
}

func newWorld(t *testing.T, k keyingCase, seed uint64) *world {
	e := newEnv(t, k)
	e.r.journal.noSync = true
	w := &world{
		t: t, e: e, rng: rand.New(rand.NewPCG(seed, seed*7919+1)),
		intents: make(map[tunnel.OwnerID]tunnel.Intent),
		foreign: make(map[routeKey]osnet.Route),
		added:   make(map[routeKey]osnet.Route),
		allowed: make(map[routeKey]bool),
	}
	for i := range ownerNames {
		e.host.AddTunnel(w.iface(i), pfx(fmt.Sprintf("10.101.%d.2/24", i)))
	}
	e.change(osnet.ChangeRoute)
	e.r.SetRebind(func() {
		w.rebinds.Add(1)
		w.checkNoStaleBypass()
	})
	return w
}

func (w *world) iface(i int) string { return fmt.Sprintf("utun%d", i+1) }

func (w *world) key(rt osnet.Route) routeKey { return w.e.k.keying.key(rt) }

func (w *world) same(a, b osnet.Route) bool { return w.e.k.keying.same(a, b) }

// table is the unscoped routes in the table by their key.
func (w *world) table() map[routeKey]osnet.Route {
	routes, err := w.e.host.Routes.Dump()
	if err != nil {
		w.t.Fatal(err)
	}
	table := make(map[routeKey]osnet.Route)
	for _, rt := range routes {
		if !rt.Scoped {
			table[w.key(rt)] = rt
		}
	}
	return table
}

// removeBehindBack deletes a route as another program would: the Reconciler is
// not told, and the deletion is not one of its operations.
func (w *world) removeBehindBack(rt osnet.Route) {
	w.checkOps()
	if err := w.e.host.Routes.Delete(rt); err != nil && !errors.Is(err, osnet.ErrNotFound) {
		w.t.Fatalf("removing %s: %v", routeLine(rt), err)
	}
	w.opsSeen = len(w.e.host.Routes.Ops())
}

func (w *world) pick(pool []string) string { return pool[w.rng.IntN(len(pool))] }

func (w *world) pickN(pool []string, max int) []string {
	var out []string
	for range w.rng.IntN(max + 1) {
		out = append(out, w.pick(pool))
	}
	return out
}

func (w *world) randomIntent(i int) tunnel.Intent {
	states := []tunnel.State{
		tunnel.StateUp, tunnel.StateUp, tunnel.StateUp, tunnel.StateUp, tunnel.StateUp,
		tunnel.StateConnecting, tunnel.StateConnecting, tunnel.StateReconnecting,
		tunnel.StateAwaitingCredentials, tunnel.StateFailed, tunnel.StateDisconnecting,
	}
	role := tunnel.RoleSplit
	if w.rng.IntN(5) < 2 {
		role = tunnel.RoleFull
	}
	in := up(string(ownerNames[i]), 1+w.rng.IntN(3), w.iface(i), role, w.pickN(routePool, 4)...)
	in.State = states[w.rng.IntN(len(states))]
	in.UpSince = t0.Add(time.Duration(w.rng.IntN(3)) * time.Second)
	in.Endpoints = ips(w.pickN(endpointPool, 2)...)
	for range w.rng.IntN(3) {
		in.DNS = append(in.DNS, tunnel.DNSIntent{Servers: ips(w.pick(serverPool)), MatchDomains: w.pickN(domainPool, 2)})
	}
	return in
}

func (w *world) step() string {
	e := w.e
	switch n := w.rng.IntN(100); {
	case n < 33:
		i := w.rng.IntN(len(ownerNames))
		in := w.randomIntent(i)
		w.intents[in.Owner] = in
		e.announce(in)
		return fmt.Sprintf("announce %s %v %v", in.Owner, in.State, in.Routes)
	case n < 42:
		o := ownerNames[w.rng.IntN(len(ownerNames))]
		delete(w.intents, o)
		e.r.Withdraw(o)
		return "withdraw " + string(o)
	case n < 56:
		return w.changeNetwork()
	case n < 68:
		return w.injectForeign()
	case n < 73:
		if keys := sortedKeys(w.foreign); len(keys) > 0 {
			key := keys[w.rng.IntN(len(keys))]
			w.removeBehindBack(w.foreign[key])
			delete(w.foreign, key)
			e.change(osnet.ChangeRoute)
			return "remove foreign " + key.String()
		}
	case n < 79:
		i := w.rng.IntN(len(ownerNames))
		if slices.ContainsFunc(e.host.Routes.Interfaces(), func(ifc osnet.Interface) bool { return ifc.Name == w.iface(i) }) {
			e.host.DestroyInterface(w.iface(i))
			w.pruneForeign()
			e.change(osnet.ChangeRoute)
			return "destroy " + w.iface(i)
		}
		e.host.AddTunnel(w.iface(i), pfx(fmt.Sprintf("10.101.%d.2/24", i)))
		e.change(osnet.ChangeRoute)
		return "create " + w.iface(i)
	case n < 85:
		return w.removeStale()
	case n < 90:
		return w.dropOneOfOurs()
	case n < 92:
		if err := e.r.Resync(); err != nil {
			w.t.Fatalf("Resync: %v", err)
		}
		return "resync"
	}
	reason := []osnet.ChangeReason{osnet.ChangeHeartbeat, osnet.ChangeWake, osnet.ChangeManual, osnet.ChangeRoute}[w.rng.IntN(4)]
	e.change(reason)
	return "event " + string(reason)
}

func (w *world) changeNetwork() string {
	e := w.e
	n := w.rng.IntN(len(networks) + 1)
	if n == len(networks) {
		e.host.Routes.SetUp("en0", false)
		e.host.Sync()
		w.pruneForeign()
		e.change(osnet.ChangeRoute)
		return "network down"
	}
	e.host.Routes.SetUp("en0", true)
	e.host.MoveNetwork("en0", pfx(networks[n].addr), ip(networks[n].gateway))
	e.change(osnet.ChangeRoute)
	return "network " + networks[n].addr
}

// pruneForeign forgets the foreign routes the kernel removed with an interface.
func (w *world) pruneForeign() {
	table := w.table()
	for key := range w.foreign {
		if _, ok := table[key]; !ok {
			delete(w.foreign, key)
		}
	}
}

func (w *world) injectForeign() string {
	e := w.e
	dstText := w.pick(append(slices.Clone(routePool[:8]), "8.8.8.8/32", "203.0.113.10/32", "198.51.100.7/32", "10.20.30.1/32"))
	dst := pfx(dstText)
	if dst.Bits() == 0 {
		return "skip"
	}
	rt := osnet.Route{Dst: dst, Gateway: ip(w.pick(gatewayPool)), Iface: "en0", Static: true}
	if w.rng.IntN(3) == 0 {
		rt = osnet.Route{Dst: dst, Iface: "en0", Static: true}
	}
	if e.k.linux() {
		// Metrics that beat ours, tie with ours (a conflict) and lose to ours.
		rt.Metric = []uint32{0, 0, bypassMetric, tunnelMetric, 100}[w.rng.IntN(5)]
	}
	rt = e.host.index(rt)
	key := w.key(rt)
	if cur, ok := w.table()[key]; ok && (!cur.Static || w.same(cur, rt)) {
		return "skip" // the kernel's own route, or what is already there
	}
	e.host.Inject(rt)
	w.foreign[key] = w.table()[key]
	e.change(osnet.ChangeRoute)
	return "foreign " + routeLine(rt)
}

func (w *world) removeStale() string {
	stale := w.e.r.Report().Stale
	if len(stale) == 0 {
		return "no stale"
	}
	s := stale[w.rng.IntN(len(stale))]
	key := w.key(s.Route)
	if _, ok := w.foreign[key]; ok {
		w.allowed[key] = true
	}
	if err := w.e.r.RemoveStale(s.Key); err != nil {
		w.t.Fatalf("RemoveStale(%s): %v", s.Key, err)
	}
	delete(w.foreign, key)
	return "remove stale " + s.Key
}

// dropOneOfOurs is another program deleting one of our routes.
func (w *world) dropOneOfOurs() string {
	table := w.table()
	var candidates []routeKey
	for _, key := range sortedKeys(w.added) {
		if cur, ok := table[key]; ok && w.same(cur, w.added[key]) {
			if _, held := w.foreign[key]; !held {
				candidates = append(candidates, key)
			}
		}
	}
	if len(candidates) == 0 {
		return "nothing to drop"
	}
	key := candidates[w.rng.IntN(len(candidates))]
	w.removeBehindBack(table[key])
	delete(w.added, key)
	w.e.change(osnet.ChangeHeartbeat)
	return "someone deletes " + key.String()
}

// settle runs passes until they change nothing.
func (w *world) settle() {
	w.t.Helper()
	for range 6 {
		before := w.changes()
		w.e.change(osnet.ChangeHeartbeat)
		w.checkOps()
		if w.changes() == before {
			return
		}
	}
	w.t.Fatalf("the Reconciler does not settle")
}

// changes counts what succeeded in the route and DNS fakes.
func (w *world) changes() int {
	n := 0
	for _, op := range w.e.host.Routes.Ops() {
		if op.Err == nil && op.Kind != fake.OpMark {
			n++
		}
	}
	for _, op := range w.e.host.DNS.Ops() {
		if op.Kind != "flush" {
			n++
		}
	}
	return n
}

// checkOps enforces that Plaitway only ever deletes routes it added.
func (w *world) checkOps() {
	w.t.Helper()
	ops := w.e.host.Routes.Ops()
	for _, op := range ops[w.opsSeen:] {
		if op.Err != nil {
			continue
		}
		switch op.Kind {
		case fake.OpAdd:
			w.added[w.key(op.Route)] = op.Route
		case fake.OpDelete:
			key := w.key(op.Removed)
			if w.allowed[key] {
				delete(w.allowed, key)
				continue
			}
			ours, ok := w.added[key]
			if !ok || !w.same(op.Removed, ours) {
				w.t.Fatalf("deleted %s, which it did not add (it added %+v)", describe(op.Removed), ours)
			}
			delete(w.added, key)
		}
	}
	w.opsSeen = len(ops)
}

func (w *world) intentList() []tunnel.Intent {
	var out []tunnel.Intent
	for _, in := range w.intents {
		out = append(out, in)
	}
	return out
}

// installable says whether the kernel would accept the route right now.
func installable(host *fake.Host, rt osnet.Route) bool {
	for _, ifc := range host.Routes.Interfaces() {
		if !ifc.Up || (rt.Iface != "" && rt.Iface != ifc.Name) {
			continue
		}
		if !rt.Gateway.IsValid() || fake.OnLink(ifc.Addrs, rt.Gateway) {
			return true
		}
	}
	return false
}

// checkNoStaleBypass is what the rebind callback asserts: when engines are
// told to rebind, no route of ours may still point at a router that is gone.
func (w *world) checkNoStaleBypass() {
	ns, _ := w.e.host.Net.Snapshot()
	for key, rec := range w.e.r.owned {
		if rec.route.Gateway.IsValid() && ns.DefaultV4 != nil && key.dst.Addr().Is4() && rec.route.Gateway != ns.DefaultV4.Gateway {
			w.t.Errorf("rebind while %s still goes through %s, the default is %s", key.dst, rec.route.Gateway, ns.DefaultV4.Gateway)
		}
	}
}

func lines(m map[routeKey]osnet.Route) []string {
	var out []string
	for _, rt := range m {
		out = append(out, routeLine(rt))
	}
	slices.Sort(out)
	return out
}

// checkInvariants is called when the Reconciler has settled.
func (w *world) checkInvariants(after string) {
	t := w.t
	t.Helper()
	fail := func(format string, args ...any) {
		t.Fatalf("after %q: "+format, append([]any{after}, args...)...)
	}
	e := w.e
	ns, _ := e.host.Net.Snapshot()
	routes, _ := e.host.Routes.Dump()

	seen := make(map[string]bool)
	table := make(map[routeKey]osnet.Route)
	for _, rt := range routes {
		id := fmt.Sprint(rt.Dst, rt.Scoped, rt.Iface)
		if !rt.Scoped {
			id = e.k.keying.identity(rt)
			table[w.key(rt)] = rt
		}
		if seen[id] {
			fail("duplicate route %s", id)
		}
		seen[id] = true
	}

	// Routes of other programs are exactly as they were put there.
	for key, f := range w.foreign {
		if got, ok := table[key]; !ok || got != f {
			fail("foreign route %+v is now %+v (present %v)", f, got, ok)
		}
	}

	// What is left in the table is what Compute says, minus what others hold.
	desired := computeFor(e.k.keying, w.intentList(), ns)
	expected := make(map[routeKey]osnet.Route)
	for _, p := range desired.Routes {
		if _, held := w.foreign[w.key(p.Route)]; p.Install && !held && installable(e.host.Host, p.Route) {
			expected[w.key(p.Route)] = p.Route
		}
	}
	actual := make(map[routeKey]osnet.Route)
	for key, rt := range table {
		if _, ok := w.foreign[key]; rt.Static && key.dst.Bits() != 0 && !ok {
			actual[key] = rt
		}
	}
	if len(expected) != len(actual) {
		fail("table:\n got %v\nwant %v", lines(actual), lines(expected))
	}
	for key, want := range expected {
		if got, ok := actual[key]; !ok || !w.same(got, want) {
			fail("route %s: got %+v, want %+v\ntable:\n got %v\nwant %v", key, got, want, lines(actual), lines(expected))
		}
	}

	// The same for DNS.
	wantDNS := make(map[string][]osnet.DNSEntry)
	for _, p := range desired.DNS {
		if p.State == tunnel.RoutePending {
			entry := osnet.DNSEntry{Servers: p.Servers, MatchDomains: p.MatchDomains, Order: p.Order}
			if e.k.keying.dnsPerInterface() {
				entry.Iface = p.Iface
			}
			wantDNS[string(p.Owner)] = append(wantDNS[string(p.Owner)], entry)
		}
	}
	gotDNS := e.host.DNS.All()
	if len(gotDNS) != len(wantDNS) {
		fail("DNS owners: got %v, want %v", gotDNS, wantDNS)
	}
	for owner, want := range wantDNS {
		if !slices.EqualFunc(gotDNS[owner], want, entryEqual) {
			fail("DNS of %s: got %+v, want %+v", owner, gotDNS[owner], want)
		}
	}

	// The journal lists exactly what a crash would leave behind.
	wantLeft := make(map[string]string)
	for key, rt := range actual {
		if e.k.keying.journaled(rt) {
			rec := record{Kind: kindRoute, Key: key.dst.String()}
			e.k.keying.stamp(&rec, rt)
			wantLeft[rec.id()] = rec.Gateway
		}
	}
	for owner := range wantDNS {
		wantLeft[kindResolver+" "+owner] = ""
	}
	left := e.unresolved()
	if len(left) != len(wantLeft) {
		fail("journal lists %+v, want %v", left, wantLeft)
	}
	for _, rec := range left {
		gw, ok := wantLeft[rec.id()]
		if !ok || rec.Gateway != gw || rec.State != stateApplied {
			fail("journal record %+v, want gateway %q applied (%v)", rec, gw, ok)
		}
	}

	rep := e.r.Report()
	for _, rr := range rep.Routes {
		if rr.State == tunnel.RouteInstalled && !goesVia(table, rr.Prefix, rr.Via) {
			fail("report says %+v is installed, the table has %v", rr, lines(table))
		}
	}
	for _, s := range rep.Stale {
		if s.Owned {
			fail("an owned stale route is left: %+v", s)
		}
	}
}

// goesVia says whether some route to dst in the table sends traffic where a
// report says: through the gateway, or, when via names an interface, bound to
// it. Where a prefix can have several routes, any of them will do.
func goesVia(table map[routeKey]osnet.Route, dst netip.Prefix, via string) bool {
	gateway, err := netip.ParseAddr(via)
	for _, rt := range table {
		switch {
		case rt.Dst != dst:
		case err == nil && rt.Gateway == gateway:
			return true
		case err != nil && !rt.Gateway.IsValid() && rt.Iface == via:
			return true
		}
	}
	return false
}

// A random mix of everything that happens to the Reconciler: intents coming and
// going, the network changing, other programs adding and removing routes,
// interfaces vanishing. After every step it settles and the invariants hold.
func TestRandomSequences(t *testing.T) { eachKeying(t, testRandomSequences) }

func testRandomSequences(t *testing.T, k keyingCase) {
	seeds, steps := 30, 100
	if testing.Short() {
		seeds, steps = 3, 40
	}
	for seed := uint64(1); seed <= uint64(seeds); seed++ {
		t.Run(fmt.Sprint("seed", seed), func(t *testing.T) {
			w := newWorld(t, k, seed)
			var history []string
			defer func() {
				if t.Failed() {
					t.Logf("steps so far: %v", history)
				}
			}()
			for range steps {
				what := w.step()
				history = append(history, what)
				w.checkOps()
				w.settle()
				w.checkInvariants(what)
			}

			// Whatever happened, a clean exit leaves only what others put there.
			if err := w.e.r.stop(nil); err != nil {
				t.Fatalf("stop: %v", err)
			}
			w.checkOps()
			table := w.table()
			for key, rt := range w.foreign {
				if got, ok := table[key]; !ok || got != rt {
					t.Errorf("foreign route %+v after exit: %+v %v", rt, got, ok)
				}
			}
			foreign := slices.Collect(maps.Values(w.foreign))
			for _, line := range w.e.table() {
				if !slices.ContainsFunc(foreign, func(f osnet.Route) bool { return routeLine(f) == line }) {
					t.Errorf("route %q left after exit", line)
				}
			}
			if owned, _ := w.e.host.DNS.Owned(); len(owned) != 0 {
				t.Errorf("resolver entries left: %v", owned)
			}
			if left := w.e.unresolved(); len(left) != 0 {
				t.Errorf("journal still lists %+v", left)
			}
		})
	}
}

// Everything at once, from several goroutines, with the event loop and its
// timers running. This is for the race detector (run it with -count=20); at the
// end the machine must settle on exactly what the final intents say.
func TestConcurrentUse(t *testing.T) { eachKeying(t, testConcurrentUse) }

func testConcurrentUse(t *testing.T, k keyingCase) {
	e := newEnv(t, k, func(c *Config) {
		c.WakeDelay = 2 * time.Millisecond
		c.RetryDelay = 2 * time.Millisecond
	})
	e.r.journal.noSync = true
	w := &world{
		t: t, e: e, intents: make(map[tunnel.OwnerID]tunnel.Intent),
		foreign: make(map[routeKey]osnet.Route),
		added:   make(map[routeKey]osnet.Route), allowed: make(map[routeKey]bool),
	}
	for i := range ownerNames {
		e.host.AddTunnel(w.iface(i), pfx(fmt.Sprintf("10.101.%d.2/24", i)))
	}
	var rebinds atomic.Int32
	e.r.SetRebind(func() { rebinds.Add(1) })
	stop := e.run()

	var wg sync.WaitGroup
	// Each owner has one goroutine, which alone writes its slot: the last
	// intent it announced, or nil after a withdraw.
	finals := make([]*tunnel.Intent, len(ownerNames))
	for i, owner := range ownerNames {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(i)+1, 99))
			ww := &world{rng: rng}
			for range 60 {
				if rng.IntN(4) == 0 {
					finals[i] = nil
					e.r.Withdraw(owner)
					continue
				}
				in := ww.randomIntent(i)
				finals[i] = &in
				if err := e.r.Announce(in); err != nil {
					t.Errorf("Announce: %v", err)
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() { // the network moves
		defer wg.Done()
		for i := range 40 {
			n := networks[i%len(networks)]
			e.host.MoveNetwork("en0", pfx(n.addr), ip(n.gateway))
			e.host.Net.Emit(osnet.Change{Reason: osnet.ChangeRoute})
		}
	}()
	wg.Add(1)
	go func() { // wakes and heartbeats
		defer wg.Done()
		for i := range 40 {
			reason := osnet.ChangeHeartbeat
			if i%5 == 0 {
				reason = osnet.ChangeWake
			}
			e.host.Net.Emit(osnet.Change{Reason: reason})
			time.Sleep(time.Millisecond)
		}
	}()
	wg.Add(1)
	go func() { // readers
		defer wg.Done()
		for range 100 {
			rep := e.r.Report()
			_ = rep.Routes
			select {
			case <-e.r.Changed():
			default:
			}
			time.Sleep(200 * time.Microsecond)
		}
	}()
	wg.Add(1)
	go func() { // Resync, rarely
		defer wg.Done()
		for range 3 {
			if err := e.r.Resync(); err != nil {
				t.Errorf("Resync: %v", err)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	wg.Wait()

	// Quiet again: the last heartbeat is handled after everything else.
	for _, in := range finals {
		if in != nil {
			w.intents[in.Owner] = *in
		}
	}
	marker := time.Now()
	e.host.Net.Emit(osnet.Change{Reason: osnet.ChangeHeartbeat, At: marker})
	e.eventually("the final heartbeat", func() bool { return e.r.Report().LastChange.At.Equal(marker) })
	w.checkInvariants("the concurrent run")

	if err := stop(); err != nil {
		t.Errorf("Run: %v", err)
	}
	if got := e.table(); len(got) != 0 {
		t.Errorf("routes left after Run: %v", got)
	}
	if owned, _ := e.host.DNS.Owned(); len(owned) != 0 {
		t.Errorf("resolver entries left: %v", owned)
	}
}
