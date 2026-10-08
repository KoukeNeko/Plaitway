package reconciler

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// ownedRoute is a route this process installed and will delete.
type ownedRoute struct {
	route osnet.Route // as read back from the table after adding it
	owner tunnel.OwnerID
	kind  tunnel.RouteKind
	// journaled routes are the ones a crash would leave behind, see
	// RouteKeying.journaled.
	journaled bool
}

// outcome is how applying one planned route or DNS entry went.
type outcome struct {
	state  tunnel.RouteState
	detail string
}

var installed = outcome{state: tunnel.RouteInstalled}

// passResult says how a pass went: errs are failures no later pass is
// guaranteed to fix, retry that something is only waiting for the network.
// overridden holds, by destination, the routes of ours that another program's
// route outranks; they are in the table and so are no failure.
type passResult struct {
	errs       []error
	retry      bool
	overridden map[netip.Prefix]string
}

func (p *passResult) fail(err error) { p.errs = append(p.errs, err) }

// maxBackoffShift caps the retry delay at 16 times its base.
const maxBackoffShift = 4

// passLocked brings the host in line with the announced intents. A pass that
// keeps failing, maxPasses times in a row, is answered with a reset.
func (r *Reconciler) passLocked(forceDNS bool) {
	res := r.reconcileLocked(r.intentList(), forceDNS)
	if r.newFailures(res.errs) {
		r.failStreak++
	} else {
		r.failStreak = 0
	}
	if r.failStreak >= r.maxPasses {
		r.log.Warn("reconciliation is not converging, rebuilding from scratch", "passes", r.failStreak)
		r.failStreak = 0
		res = r.resetLocked()
	}
	r.settleLocked(res)
}

// newFailures reports whether errs holds an error that the last reset did not
// leave behind. Errors that went away are forgotten, so that they count as new
// when they come back.
func (r *Reconciler) newFailures(errs []error) bool {
	seen := make(map[string]bool, len(errs))
	fresh := false
	for _, err := range errs {
		seen[err.Error()] = true
		fresh = fresh || !r.futile[err.Error()]
	}
	maps.DeleteFunc(r.futile, func(msg string, _ bool) bool { return !seen[msg] })
	return fresh
}

// settleLocked records whether work is left over and schedules the retry. A
// pass that failed again waits longer than the one before it.
func (r *Reconciler) settleLocked(res passResult) {
	if len(res.errs) > 0 {
		r.failedPasses++
	} else {
		r.failedPasses = 0
	}
	r.dirty = len(res.errs) > 0 || res.retry
	if r.dirty {
		r.armRetry(r.retryAfter())
	}
}

// retryAfter is the delay before the next retry: the base delay, doubled for
// every pass in a row that failed again.
func (r *Reconciler) retryAfter() time.Duration {
	return r.retryDelay << min(max(r.failedPasses-1, 0), maxBackoffShift)
}

// resetLocked removes everything this process owns, last applied first, then
// builds it again. What is still wrong afterwards is not for a reset to fix.
func (r *Reconciler) resetLocked() passResult {
	down := r.reconcileLocked(nil, true)
	if err := r.sweepResolvers(); err != nil {
		down.fail(err)
	}
	up := r.reconcileLocked(r.intentList(), true)
	up.errs = append(down.errs, up.errs...)
	r.futile = make(map[string]bool, len(up.errs))
	for _, err := range up.errs {
		r.futile[err.Error()] = true
	}
	return up
}

// reconcileLocked computes what the intents want and applies the difference.
// Resolver entries that are no longer wanted go first, then routes that are no
// longer wanted (default halves, tunnel routes, bypass routes), then new routes
// in the opposite order (bypass, tunnel, default halves), then new resolver
// entries. A failing subsystem does not stop the others. With forceDNS the
// resolver entries are written again even if nothing changed, because a network
// change or a wake may have disturbed them.
func (r *Reconciler) reconcileLocked(intents []tunnel.Intent, forceDNS bool) passResult {
	var res passResult
	r.desired = computeFor(r.keying, intents, r.netState)
	wantDNS := r.dnsWanted()
	dnsChanged := r.removeDNS(wantDNS, &res)

	var routeOut map[netip.Prefix]outcome
	table, err := r.dumpTable()
	if err != nil {
		res.fail(err)
		routeOut = make(map[netip.Prefix]outcome)
		for _, p := range r.desired.Routes {
			if p.Install {
				routeOut[p.Route.Dst] = outcome{tunnel.RouteFailed, err.Error()}
			}
		}
	} else {
		routeOut, table = r.syncRoutes(table, &res)
	}

	dnsOut := r.applyDNS(wantDNS, forceDNS, dnsChanged, &res)

	r.stale = nil
	if table != nil {
		r.stale = findStale(r.keying, table, r.netState, r.endpointsLocked(), r.isOwned)
	}
	r.buildReports(routeOut, dnsOut, res.overridden)
	for _, err := range res.errs {
		r.log.Warn("reconciliation incomplete", "err", err)
	}
	return res
}

// dumpTable reads the unscoped routes by their key. Scoped routes are a separate
// key space that Plaitway never writes.
//
// Two routes can have one key where a program may append a route to a
// destination and metric that has one (Linux, ip route append; DHCP clients do
// it for their default routes). The table cannot show both, and the one that
// stays must not be a rival of ours: a route we installed is the one we have to
// find again, to see that it is still there and to delete it.
func (r *Reconciler) dumpTable() (map[routeKey]osnet.Route, error) {
	routes, err := r.routes.Dump()
	if err != nil {
		return nil, fmt.Errorf("reading the routing table: %w", err)
	}
	table := make(map[routeKey]osnet.Route, len(routes))
	for _, rt := range routes {
		if rt.Scoped {
			continue
		}
		key := r.keying.key(rt)
		if prev, dup := table[key]; dup && r.isInstalled(prev) {
			continue
		}
		table[key] = rt
	}
	return table, nil
}

// isInstalled says whether rt is a route that we installed and still own.
func (r *Reconciler) isInstalled(rt osnet.Route) bool {
	rec, ok := r.owned[r.keying.key(rt)]
	return ok && r.keying.same(rt, rec.route)
}

// syncRoutes applies the route plans to the table. It returns the outcome per
// planned destination (at most one plan per destination is installed) and the
// table as it is afterwards.
func (r *Reconciler) syncRoutes(table map[routeKey]osnet.Route, res *passResult) (map[netip.Prefix]outcome, map[routeKey]osnet.Route) {
	want := make(map[routeKey]RoutePlan)
	var plans []RoutePlan // in apply order
	for _, p := range r.desired.Routes {
		if p.Install {
			want[r.keying.key(p.Route)] = p
			plans = append(plans, p)
		}
	}
	out := make(map[netip.Prefix]outcome, len(plans))

	// A route stays ours only while the table still shows what we installed.
	// Anything else was replaced or removed by someone, and is not ours to
	// delete. A route that vanished with its interface is the same as a removed
	// one: gone, and nobody's doing.
	for _, key := range sortedKeys(r.owned) {
		actual, ok := table[key]
		switch {
		case !ok:
			r.release(key, "gone from the routing table")
		case !r.keying.same(actual, r.owned[key].route):
			r.log.Warn("a route we installed was changed by another program, leaving it", "route", key, "now", describe(actual))
			r.release(key, "changed by another program")
		}
	}

	// Remove what is no longer wanted, or has to be replaced, last applied first.
	// Where the table can hold the old and the new route side by side, the old one
	// goes after the new one is in: removing it first would let the traffic of a
	// full tunnel out for as long as the add takes.
	doomed := r.doomedRoutes(want)
	replaced, removedNow := r.splitReplaced(doomed, want)
	r.deleteOwned(removedNow, table, res)

	var added []routeKey
	for _, p := range plans {
		key := r.keying.key(p.Route)
		if rec, ok := r.owned[key]; ok {
			if !r.keying.same(rec.route, p.Route) {
				out[p.Route.Dst] = outcome{tunnel.RouteFailed, "the old route could not be removed"}
				continue
			}
			rec.owner, rec.kind = p.Owner, p.Kind
			r.owned[key] = rec
			out[p.Route.Dst] = installed
			continue
		}
		if why := r.keying.unplaceable(p.Route); why != "" {
			// The interface appearing is a change of the network, which brings the
			// next pass, unless the monitor does not report it.
			res.retry = r.keying.retriesUnplaced()
			out[p.Route.Dst] = outcome{tunnel.RoutePending, why}
			continue
		}
		if actual, ok := table[key]; ok {
			// Somebody else's route. Identical is as good as ours, though we
			// leave it alone when we are done; different is a conflict.
			if r.keying.same(actual, p.Route) {
				out[p.Route.Dst] = installed
			} else {
				out[p.Route.Dst] = outcome{tunnel.RouteFailed, "held by another program via " + via(actual)}
			}
			continue
		}
		o, ok := r.addRoute(p, res)
		out[p.Route.Dst] = o
		if ok {
			added = append(added, key)
		}
	}

	if len(added) > 0 {
		table = r.verifyAdded(table, added, out, res)
	}
	r.deleteReplaced(replaced, want, table, out, res)
	r.markOverridden(table, out, res)
	return out, table
}

// deleteReplaced deletes the routes that a route to the same destination through
// another next hop replaces, once that route is in the verified table. A
// replacement that could not be added leaves the old route where it is: it
// still leads into the tunnel, where deleting it would let the traffic of a
// full tunnel out through the physical network while the tunnel reports itself
// up. The pass is retried, and the report of the destination says that the old
// route stays. Only the route of the tunnel that is replaced by its own route is
// kept: a prefix that went to another owner (priority, or the holder of the default
// route changed) is not held for the loser.
func (r *Reconciler) deleteReplaced(replaced []routeKey, want map[routeKey]RoutePlan, table map[routeKey]osnet.Route, out map[netip.Prefix]outcome, res *passResult) {
	replacements := make(map[netip.Prefix]RoutePlan, len(replaced))
	for _, plan := range want {
		replacements[plan.Route.Dst.Masked()] = plan
	}
	var superseded []routeKey
	for _, key := range replaced {
		plan := replacements[key.dst]
		if plan.Owner != r.owned[key].owner || r.replacementIsInTable(plan, key.dst, table, out) {
			superseded = append(superseded, key)
			continue
		}
		out[key.dst] = keptOldRoute(out[key.dst], r.owned[key].route)
		res.retry = true
	}
	r.deleteOwned(superseded, table, res)
}

// replacementIsInTable says whether the route planned for dst was installed this
// pass and is in the table read back after adding.
func (r *Reconciler) replacementIsInTable(plan RoutePlan, dst netip.Prefix, table map[routeKey]osnet.Route, out map[netip.Prefix]outcome) bool {
	if out[dst] != installed {
		return false
	}
	actual, ok := table[r.keying.key(plan.Route)]
	return ok && r.keying.same(actual, plan.Route)
}

// keptOldRoute is the outcome of a destination whose replacement is not in the
// table: the reason it is not, and that the old route stays. A replacement that
// is neither failed nor refused is pending, because nothing says why it is
// missing.
func keptOldRoute(replacement outcome, old osnet.Route) outcome {
	if replacement == installed {
		replacement = outcome{state: tunnel.RoutePending, detail: "the new route is not in the routing table yet"}
	}
	replacement.detail += "; the previous route via " + via(old) + " is kept"
	return replacement
}

// doomedRoutes are the routes we own that are not wanted any more, or not as
// they are, last applied first.
func (r *Reconciler) doomedRoutes(want map[routeKey]RoutePlan) []routeKey {
	var doomed []routeKey
	for key, rec := range r.owned {
		if p, ok := want[key]; !ok || !r.keying.same(p.Route, rec.route) {
			doomed = append(doomed, key)
		}
	}
	slices.SortFunc(doomed, func(a, b routeKey) int {
		return cmp.Or(cmp.Compare(kindRank(r.owned[b].kind), kindRank(r.owned[a].kind)), compareKeys(b, a))
	})
	return doomed
}

// splitReplaced separates the doomed tunnel routes whose destination is wanted
// by a route with another key (the same prefix through another next hop, after
// the tunnel announced another gateway; on Linux the same prefix with another
// metric) from the others. Only a table that can hold both at once can keep the
// old one until the new one is in: on macOS one prefix has one route, and on
// Linux one prefix and metric, so a new route with the key of the old one makes
// the old one go first. A bypass route is not kept: it carries no traffic of the
// user's, and the old one is stale, which is why it is replaced.
func (r *Reconciler) splitReplaced(doomed []routeKey, want map[routeKey]RoutePlan) (replaced, removedNow []routeKey) {
	if !r.keying.byInterface() {
		return nil, doomed
	}
	wanted := make(map[netip.Prefix]bool, len(want))
	for key := range want {
		wanted[key.dst] = true
	}
	for _, key := range doomed {
		// A route that is wanted as it is but is not the same (it needs replacing in
		// place) has to be deleted before it can be added again.
		if _, sameKey := want[key]; wanted[key.dst] && !sameKey && r.owned[key].kind != tunnel.RouteBypass {
			replaced = append(replaced, key)
		} else {
			removedNow = append(removedNow, key)
		}
	}
	return replaced, removedNow
}

// deleteOwned deletes routes we own from the table, in the order given. A route
// that cannot be deleted stays ours, and the pass reports the failure.
func (r *Reconciler) deleteOwned(keys []routeKey, table map[routeKey]osnet.Route, res *passResult) {
	for _, key := range keys {
		rec := r.owned[key]
		if err := r.routes.Delete(rec.route); err != nil && !errors.Is(err, osnet.ErrNotFound) {
			res.fail(fmt.Errorf("deleting route %s: %w", key, err))
			continue
		}
		delete(table, key)
		r.release(key, "")
	}
}

// addRoute journals and adds one route. ok reports that the route is in the
// table now and owned by us; its outcome is final once verifyAdded has read it
// back.
func (r *Reconciler) addRoute(p RoutePlan, res *passResult) (o outcome, ok bool) {
	rt := p.Route
	key := r.keying.key(rt)
	journaled := r.keying.journaled(rt)
	if journaled {
		if err := r.journalRoute(statePending, p.Owner, rt, ""); err != nil {
			res.fail(err)
			return outcome{tunnel.RouteFailed, err.Error()}, false
		}
	}
	abandon := func(note string) {
		if journaled {
			r.journalRouteBestEffort(stateRemoved, p.Owner, rt, note)
		}
	}
	err := r.routes.Add(rt)
	switch {
	case err == nil:
		r.owned[key] = ownedRoute{route: rt, owner: p.Owner, kind: p.Kind, journaled: journaled}
		return installed, true
	case errors.Is(err, osnet.ErrExists):
		// Someone added it between our read and our write. Read it back.
		table, derr := r.dumpTable()
		if derr != nil {
			res.fail(derr)
			abandon("could not read back")
			return outcome{tunnel.RouteFailed, derr.Error()}, false
		}
		cur, found := table[key]
		switch {
		case !found:
			res.retry = true
			abandon("vanished again")
			return outcome{tunnel.RoutePending, "route appeared and vanished"}, false
		case r.keying.same(cur, rt):
			abandon("already present, not ours")
			return installed, false
		default:
			abandon("held by another program")
			return outcome{tunnel.RouteFailed, "held by another program via " + via(cur)}, false
		}
	case errors.Is(err, osnet.ErrUnreachable):
		res.retry = true
		abandon("network unreachable")
		return outcome{tunnel.RoutePending, "network unreachable"}, false
	case errors.Is(err, errors.ErrUnsupported):
		// The host's configuration refuses the route (IPv6 is switched off on the
		// interface). Neither a retry nor a rebuild changes that, so the pass has
		// not failed; the next full pass tries again.
		abandon("not supported here")
		return outcome{tunnel.RouteFailed, err.Error()}, false
	default:
		res.fail(fmt.Errorf("adding route %s: %w", rt.Dst, err))
		abandon("add failed")
		return outcome{tunnel.RouteFailed, err.Error()}, false
	}
}

// verifyAdded reads the table back after adding routes: it records what the
// kernel made of them (the fingerprint of the journal) and catches routes that
// did not stay.
func (r *Reconciler) verifyAdded(table map[routeKey]osnet.Route, added []routeKey, out map[netip.Prefix]outcome, res *passResult) map[routeKey]osnet.Route {
	after, err := r.dumpTable()
	if err != nil {
		res.fail(err)
		return table
	}
	for _, key := range added {
		rec := r.owned[key]
		actual, ok := after[key]
		if !ok || !r.keying.same(actual, rec.route) {
			res.fail(fmt.Errorf("route %s is not in the routing table after adding it", key))
			out[key.dst] = outcome{tunnel.RouteFailed, "not in the routing table after adding"}
			r.release(key, "not in the routing table after adding")
			continue
		}
		rec.route = actual
		r.owned[key] = rec
		if rec.journaled {
			r.journalRouteBestEffort(stateApplied, rec.owner, actual, "")
		}
	}
	return after
}

// release stops owning a route without touching the table, and journals it
// as removed.
func (r *Reconciler) release(key routeKey, note string) {
	rec, ok := r.owned[key]
	if !ok {
		return
	}
	delete(r.owned, key)
	if rec.journaled {
		r.journalRouteBestEffort(stateRemoved, rec.owner, rec.route, note)
	}
}

func (r *Reconciler) journalRoute(state string, owner tunnel.OwnerID, rt osnet.Route, note string) error {
	rec := record{Owner: owner, Kind: kindRoute, Key: rt.Dst.Masked().String(), State: state, Note: note}
	r.keying.stamp(&rec, rt)
	if state == stateApplied {
		rec.Fingerprint = r.keying.fingerprint(rt)
	}
	if err := r.journal.append(rec); err != nil {
		return fmt.Errorf("journaling route %s: %w", rt.Dst, err)
	}
	return nil
}

// journalRouteBestEffort is for the records that follow the change itself: if
// one is lost, the journal is merely older than the host, and replay copes.
func (r *Reconciler) journalRouteBestEffort(state string, owner tunnel.OwnerID, rt osnet.Route, note string) {
	if err := r.journalRoute(state, owner, rt, note); err != nil {
		r.log.Warn("journal write failed", "err", err)
	}
}

// addrString is how the journal writes a gateway. The zone of a link-local
// address is left out: it only repeats the interface.
func addrString(a netip.Addr) string {
	if !a.IsValid() {
		return ""
	}
	return a.WithZone("").String()
}

// via names where a route sends traffic: the gateway, or the interface for a
// route bound to one.
func via(rt osnet.Route) string {
	switch {
	case rt.Gateway.IsValid():
		return rt.Gateway.String()
	case rt.Blackhole:
		return "blackhole"
	}
	return rt.Iface
}

// reportedVia is where a planned route is said to go. A tunnel route is the
// tunnel's, whatever next hop Windows gives it to reach the adapter, so it names
// the interface; a bypass route names the router it goes through.
func reportedVia(p RoutePlan) string {
	if p.Kind != tunnel.RouteBypass {
		return p.Route.Iface
	}
	return via(p.Route)
}

func describe(rt osnet.Route) string {
	text := rt.Dst.String() + " via " + via(rt)
	if rt.IfIndex != 0 {
		text += fmt.Sprintf(" (interface %d)", rt.IfIndex)
	}
	return text
}

func sortedKeys[V any](m map[routeKey]V) []routeKey {
	keys := make([]routeKey, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, compareKeys)
	return keys
}

// appliedDNS is what was written for an owner: the entries and the interfaces
// they were written to, as the system numbered them then.
type appliedDNS struct {
	entries   []osnet.DNSEntry
	ifIndexes []uint32
}

// dnsIfIndexes are the indexes of the interfaces the entries name. Where the
// system keeps resolver settings per interface (Linux, systemd-resolved) they
// belong to an interface by its index: one that was made again under the same
// name has another and none of the settings of the old one, which the entries
// alone do not show.
func (r *Reconciler) dnsIfIndexes(entries []osnet.DNSEntry) []uint32 {
	indexes := make([]uint32, len(entries))
	for i, e := range entries {
		indexes[i] = interfaceIndex(r.netState, e.Iface)
	}
	return indexes
}

func entryEqual(a, b osnet.DNSEntry) bool {
	return a.Order == b.Order && a.Iface == b.Iface && slices.Equal(a.Servers, b.Servers) && slices.Equal(a.MatchDomains, b.MatchDomains)
}

// dnsWanted turns the DNS plans into the entries to apply, by owner.
func (r *Reconciler) dnsWanted() map[tunnel.OwnerID][]osnet.DNSEntry {
	want := make(map[tunnel.OwnerID][]osnet.DNSEntry)
	for _, p := range r.desired.DNS {
		if p.State == tunnel.RoutePending {
			entry := osnet.DNSEntry{
				Servers:      slices.Clone(p.Servers),
				MatchDomains: slices.Clone(p.MatchDomains),
				Order:        p.Order,
			}
			if r.keying.dnsPerInterface() {
				entry.Iface = p.Iface
			}
			want[p.Owner] = append(want[p.Owner], entry)
		}
	}
	return want
}

// removeDNS removes the resolver entries of owners that want none any more. It
// reports whether it removed anything.
func (r *Reconciler) removeDNS(want map[tunnel.OwnerID][]osnet.DNSEntry, res *passResult) (changed bool) {
	for _, owner := range sortedOwners(r.dnsApplied) {
		if _, ok := want[owner]; ok {
			continue
		}
		if err := r.dns.Remove(string(owner)); err != nil {
			res.fail(fmt.Errorf("removing resolver entries of %s: %w", owner, err))
			continue
		}
		delete(r.dnsApplied, owner)
		r.journalResolverBestEffort(stateRemoved, owner, "")
		changed = true
	}
	return changed
}

// applyDNS writes the wanted resolver entries, one Apply per owner, and flushes
// the resolver caches when anything changed. changed says whether removeDNS
// already changed something.
func (r *Reconciler) applyDNS(want map[tunnel.OwnerID][]osnet.DNSEntry, force, changed bool, res *passResult) map[tunnel.OwnerID]outcome {
	out := make(map[tunnel.OwnerID]outcome)
	failed := false
	for _, owner := range sortedOwners(want) {
		entries := want[owner]
		prev, had := r.dnsApplied[owner]
		ifIndexes := r.dnsIfIndexes(entries)
		same := had && slices.EqualFunc(prev.entries, entries, entryEqual) && slices.Equal(prev.ifIndexes, ifIndexes)
		if same && !force {
			out[owner] = installed
			continue
		}
		if !same {
			if err := r.journalResolver(statePending, owner, ""); err != nil {
				res.fail(err)
				out[owner] = outcome{tunnel.RouteFailed, err.Error()}
				continue
			}
		}
		if err := r.dns.Apply(string(owner), entries); err != nil {
			// The entry may or may not have been written. The pending record
			// stays, and the next pass writes it again. Until then the owner is
			// listed with nothing known about its entries, so that removing it
			// removes whatever is there.
			r.dnsApplied[owner] = appliedDNS{}
			failed = true
			res.fail(fmt.Errorf("applying resolver entries of %s: %w", owner, err))
			out[owner] = outcome{tunnel.RouteFailed, err.Error()}
			continue
		}
		if !same {
			r.journalResolverBestEffort(stateApplied, owner, "")
		}
		r.dnsApplied[owner] = appliedDNS{entries, ifIndexes}
		out[owner] = installed
		changed = true
	}
	if changed {
		if err := r.dns.Flush(); err != nil {
			r.log.Warn("flushing the resolver caches failed", "err", err)
		}
	}
	if changed || failed {
		r.refreshResolverEntries()
	}
	return out
}

func sortedOwners[V any](m map[tunnel.OwnerID]V) []tunnel.OwnerID {
	owners := make([]tunnel.OwnerID, 0, len(m))
	for o := range m {
		owners = append(owners, o)
	}
	slices.Sort(owners)
	return owners
}

func (r *Reconciler) journalResolver(state string, owner tunnel.OwnerID, note string) error {
	err := r.journal.append(record{Owner: owner, Kind: kindResolver, Key: string(owner), State: state, Note: note})
	if err != nil {
		return fmt.Errorf("journaling resolver entries of %s: %w", owner, err)
	}
	return nil
}

func (r *Reconciler) journalResolverBestEffort(state string, owner tunnel.OwnerID, note string) {
	if err := r.journalResolver(state, owner, note); err != nil {
		r.log.Warn("journal write failed", "err", err)
	}
}

func (r *Reconciler) refreshResolverEntries() {
	keys, err := r.dns.Owned()
	if err != nil {
		r.log.Warn("listing resolver entries failed", "err", err)
		return
	}
	r.resolverEntries = keys
}

// sweepResolvers removes every resolver entry that carries our marker, whoever
// wrote it. Entries of the current owners are removed before it runs, so what it
// finds is left over.
func (r *Reconciler) sweepResolvers() error {
	keys, err := r.dns.Owned()
	if err != nil {
		return fmt.Errorf("listing resolver entries: %w", err)
	}
	var errs []error
	for _, key := range keys {
		if err := r.dns.Remove(key); err != nil {
			errs = append(errs, fmt.Errorf("removing resolver entry %s: %w", key, err))
		}
	}
	left, err := r.dns.Owned()
	if err != nil {
		return errors.Join(append(errs, fmt.Errorf("listing resolver entries: %w", err))...)
	}
	r.resolverEntries = left
	if len(left) > 0 {
		errs = append(errs, fmt.Errorf("resolver entries remain after removal: %s", strings.Join(left, ", ")))
	}
	clear(r.dnsApplied)
	return errors.Join(errs...)
}

func (r *Reconciler) buildReports(routeOut map[netip.Prefix]outcome, dnsOut map[tunnel.OwnerID]outcome, overridden map[netip.Prefix]string) {
	r.routeReports = r.routeReports[:0:0]
	for _, p := range r.desired.Routes {
		rep := tunnel.RouteReport{
			Prefix: p.Route.Dst, Owner: p.Owner, Kind: p.Kind, Via: reportedVia(p),
			State: p.State, Detail: p.Detail, ShadowedBy: p.ShadowedBy,
		}
		if o, ok := routeOut[p.Route.Dst]; ok && p.Install {
			rep.State, rep.Detail = o.state, o.detail
			rep.Overridden = o.state == overriddenState && overridden[p.Route.Dst] == o.detail
		}
		r.routeReports = append(r.routeReports, rep)
	}
	r.dnsReports = r.dnsReports[:0:0]
	for _, p := range r.desired.DNS {
		rep := tunnel.DNSReport{
			Owner: p.Owner, Servers: p.Servers, MatchDomains: p.MatchDomains,
			State: p.State, Detail: p.Detail,
		}
		if o, ok := dnsOut[p.Owner]; ok && p.State == tunnel.RoutePending {
			rep.State, rep.Detail = o.state, o.detail
		}
		r.dnsReports = append(r.dnsReports, rep)
	}
}

// recover undoes what the previous run left in the journal, then removes every
// resolver entry that carries our marker, journal or not.
func (r *Reconciler) recover() error {
	if left := r.journal.unresolved(); len(left) > 0 {
		table, err := r.dumpTable()
		if err != nil {
			return fmt.Errorf("recovering from the journal: %w", err)
		}
		for _, rec := range left {
			switch rec.Kind {
			case kindRoute:
				r.recoverRoute(rec, table)
			case kindResolver:
				r.recoverResolver(rec)
			}
		}
	}
	if err := r.sweepResolvers(); err != nil {
		r.log.Error("sweeping resolver entries failed", "err", err)
	}
	return nil
}

// recoverRoute deletes a route a previous run journaled, unless somebody else
// changed it since: then it is theirs now.
func (r *Reconciler) recoverRoute(rec record, table map[routeKey]osnet.Route) {
	key, err := r.keying.recordKey(rec)
	if err != nil {
		r.log.Warn("journal names an unreadable route", "key", rec.Key, "err", err)
		note := "unreadable key"
		if errors.Is(err, errOtherTable) {
			note = "written for another kind of routing table, left in place"
		}
		r.journalKeyBestEffort(rec, stateRemoved, note)
		return
	}
	actual, ok := table[key]
	switch {
	case !ok:
		r.journalKeyBestEffort(rec, stateRemoved, "already gone")
	case !r.keying.recognizes(rec, actual):
		r.log.Warn("a route of an earlier run was changed by another program, leaving it", "route", describe(actual))
		r.journalKeyBestEffort(rec, stateRemoved, "changed by another program, left in place")
	default:
		if err := r.routes.Delete(actual); err != nil && !errors.Is(err, osnet.ErrNotFound) {
			// Keep it as ours: the next pass removes it when it is not wanted.
			r.log.Error("removing a route of an earlier run failed", "route", describe(actual), "err", err)
			r.owned[key] = ownedRoute{route: actual, owner: rec.Owner, kind: tunnel.RouteBypass, journaled: true}
			return
		}
		delete(table, key)
		r.log.Info("removed a route of an earlier run", "route", describe(actual))
		r.journalKeyBestEffort(rec, stateRemoved, "removed after restart")
	}
}

func (r *Reconciler) recoverResolver(rec record) {
	if err := r.dns.Remove(rec.Key); err != nil {
		r.log.Error("removing resolver entries of an earlier run failed", "key", rec.Key, "err", err)
		return
	}
	r.journalKeyBestEffort(rec, stateRemoved, "removed after restart")
}

func (r *Reconciler) journalKeyBestEffort(rec record, state, note string) {
	rec.State, rec.Note, rec.Fingerprint = state, note, ""
	if err := r.journal.append(rec); err != nil {
		r.log.Warn("journal write failed", "err", err)
	}
}

// netChanged reports whether anything Compute looks at differs; the epoch is
// ignored.
func netChanged(a, b osnet.NetState) bool {
	return !reflect.DeepEqual(canonical(a), canonical(b))
}

func canonical(ns osnet.NetState) osnet.NetState {
	out := osnet.NetState{DefaultV4: ns.DefaultV4, DefaultV6: ns.DefaultV6, Connected: sortedPrefixes(ns.Connected)}
	for _, ifc := range ns.Interfaces {
		ifc.Addrs = sortedPrefixes(ifc.Addrs)
		out.Interfaces = append(out.Interfaces, ifc)
	}
	slices.SortFunc(out.Interfaces, func(a, b osnet.Interface) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// underlayChanged reports whether the physical network that the tunnels' own
// traffic travels over differs. Every change makes every tunnel reconnect, so
// only what the transport depends on counts: the default routes and the
// interfaces they leave through, with their addresses. Not AirDrop, the bridges
// of virtual machines, tunnels, or a second interface that carries no default
// route, and not the link-local or the privacy address of an interface, which
// come and go on their own.
func underlayChanged(a, b osnet.NetState) bool {
	return !reflect.DeepEqual(underlayOf(a), underlayOf(b))
}

func underlayOf(ns osnet.NetState) osnet.NetState {
	out := osnet.NetState{DefaultV4: ns.DefaultV4, DefaultV6: ns.DefaultV6}
	for _, ifc := range ns.Interfaces {
		if !leavesThrough(ns, ifc.Name) {
			continue
		}
		var addrs []netip.Prefix
		for _, p := range ifc.Addrs {
			switch {
			case p.Addr().IsLinkLocalUnicast():
			case p.Addr().Is6():
				addrs = append(addrs, p.Masked()) // not the address of a /64 that rotates
			default:
				addrs = append(addrs, p)
			}
		}
		ifc.Addrs = slices.Compact(sortedPrefixes(addrs))
		out.Interfaces = append(out.Interfaces, ifc)
	}
	slices.SortFunc(out.Interfaces, func(a, b osnet.Interface) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// leavesThrough reports whether a default route of ns leaves through name.
func leavesThrough(ns osnet.NetState, name string) bool {
	return ns.DefaultV4 != nil && ns.DefaultV4.Iface == name || ns.DefaultV6 != nil && ns.DefaultV6.Iface == name
}

func sortedPrefixes(in []netip.Prefix) []netip.Prefix {
	if len(in) == 0 {
		return nil
	}
	out := slices.Clone(in)
	slices.SortFunc(out, comparePrefix)
	return out
}

func cloneNetState(ns osnet.NetState) osnet.NetState {
	out := ns
	out.Interfaces = slices.Clone(ns.Interfaces)
	for i := range out.Interfaces {
		out.Interfaces[i].Addrs = slices.Clone(out.Interfaces[i].Addrs)
	}
	if ns.DefaultV4 != nil {
		nh := *ns.DefaultV4
		out.DefaultV4 = &nh
	}
	if ns.DefaultV6 != nil {
		nh := *ns.DefaultV6
		out.DefaultV6 = &nh
	}
	out.Connected = slices.Clone(ns.Connected)
	return out
}
