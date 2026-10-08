package reconciler

import (
	"fmt"
	"maps"
	"net/netip"
	"slices"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// overriddenState is the state of a route of ours that a route of another
// program outranks: it is in the table, and the traffic goes the other way.
//
// It is RouteFailed and not a new state. The manager turns Pending, Blocked and
// Failed into the warnings that say a tunnel does not carry what it asked for
// (for a bypass route: that the tunnel cannot connect), and keeps quiet about
// Shadowed, which is the normal state of a tunnel on standby; an overridden route
// has to warn. Failed is also what macOS reports for a prefix another program
// holds ("held by another program"), which is the same situation found by another
// means. Pending would say that the route waits for something, and nothing is
// retried: the verdict is read again whenever the network changes.
const overriddenState = tunnel.RouteFailed

// markOverridden finds the routes of ours that are not the ones in use, because a
// route of another program wins, and reports them as overridden. The route stays
// where it is: lowering its metric below a foreign VPN's would start a fight over
// the host's traffic, and a foreign route is never ours to delete. When the other
// route goes, ours is in use at once, and the next pass says so.
//
// Only a table keyed by interface and next hop can hold such a pair. The
// verdict is the one Windows reaches:
//   - Among routes to one prefix, the lowest effective metric wins: the metric of
//     the route plus the metric of its interface. A tie is not a loss, because
//     the system breaks it in a way that cannot be read.
//   - A more specific route wins whatever its metric. That is how every route of
//     another program is meant to work, except for a default route: our two
//     halves are all that stands between the traffic and the physical
//     network, and a list of foreign routes that leaves out only what is not on
//     the internet (the full tunnel of a profile with the private ranges taken
//     out) takes all of it.
//
// An interface metric of zero is one the network state did not give; the detail
// says so when it takes part in a verdict.
func (r *Reconciler) markOverridden(table map[routeKey]osnet.Route, out map[netip.Prefix]outcome) {
	if !r.keying.byInterface() {
		return
	}
	foreign := r.foreignRoutes(table)
	verdicts := make(map[netip.Prefix]string)
	for _, plan := range r.desired.Routes {
		dst := plan.Route.Dst
		if !plan.Install || out[dst] != installed {
			continue
		}
		ours, ok := table[r.keying.key(plan.Route)]
		if !ok {
			continue
		}
		if why := r.overrideReason(ours, foreign); why != "" {
			verdicts[dst] = why
			out[dst] = outcome{overriddenState, why}
		}
	}
	r.logOverrides(verdicts, out)
	r.overridden = verdicts
}

// foreignRoutes are the routes in the table that are not ours and can carry
// traffic: a route through an interface that is down does not.
func (r *Reconciler) foreignRoutes(table map[routeKey]osnet.Route) []osnet.Route {
	var out []osnet.Route
	for _, key := range sortedKeys(table) {
		rt := table[key]
		if _, ours := r.owned[key]; ours {
			continue
		}
		if ifc, known := routeInterface(r.netState, rt); known && !ifc.Up {
			continue
		}
		out = append(out, rt)
	}
	return out
}

// overrideReason says why a foreign route outranks ours, or "" when none does.
func (r *Reconciler) overrideReason(ours osnet.Route, foreign []osnet.Route) string {
	oursMetric := r.effectiveMetric(ours)
	var winner *osnet.Route
	winnerMetric := oursMetric
	for i, rival := range foreign {
		if rival.Dst.Masked() != ours.Dst.Masked() {
			continue
		}
		if metric := r.effectiveMetric(rival); metric < winnerMetric {
			winner, winnerMetric = &foreign[i], metric
		}
	}
	if winner != nil {
		reason := fmt.Sprintf("overridden by %s: effective metric %d, ours %d", describe(*winner), winnerMetric, oursMetric)
		if !r.interfaceMetricKnown(ours) || !r.interfaceMetricKnown(*winner) {
			reason += "; interface metric unknown"
		}
		return reason
	}
	if piece, covered := coveringPiece(ours.Dst.Masked(), foreign); covered {
		return "overridden by more specific routes, such as " + describe(piece)
	}
	return ""
}

// effectiveMetric is what the system compares between routes of one prefix.
func (r *Reconciler) effectiveMetric(rt osnet.Route) uint64 {
	ifc, _ := routeInterface(r.netState, rt)
	return uint64(rt.Metric) + uint64(ifc.Metric)
}

func (r *Reconciler) interfaceMetricKnown(rt osnet.Route) bool {
	ifc, known := routeInterface(r.netState, rt)
	return known && ifc.Metric != 0
}

// routeInterface is the interface a route leaves through: by index where the
// route has one, by name otherwise.
func routeInterface(ns osnet.NetState, rt osnet.Route) (osnet.Interface, bool) {
	if rt.IfIndex == 0 {
		return findInterface(ns, rt.Iface)
	}
	i := slices.IndexFunc(ns.Interfaces, func(ifc osnet.Interface) bool { return uint32(ifc.Index) == rt.IfIndex })
	if i < 0 {
		return osnet.Interface{}, false
	}
	return ns.Interfaces[i], true
}

// coveringPiece reports whether the foreign routes inside the default half cover
// everything on the internet that the half does, the private ranges apart, and
// returns the first of them. Only routes that a program added count: the ones the
// system derives from interface addresses are never a way out.
func coveringPiece(half netip.Prefix, foreign []osnet.Route) (osnet.Route, bool) {
	if !isDefault(half) {
		return osnet.Route{}, false
	}
	universe, ok := internetWithin(half)
	if !ok {
		return osnet.Route{}, false
	}
	var pieces []netip.Prefix
	var first osnet.Route
	for _, rt := range foreign {
		dst := rt.Dst.Masked()
		if !rt.Static || dst.Bits() <= half.Bits() || !half.Contains(dst.Addr()) {
			continue
		}
		if len(pieces) == 0 {
			first = rt
		}
		pieces = append(pieces, dst)
	}
	var notOnTheInternet []netip.Prefix
	if half.Addr().Is4() {
		notOnTheInternet = notOnTheInternetV4
	}
	if !coversAllBut(pieces, universe, notOnTheInternet) {
		return osnet.Route{}, false
	}
	return first, true
}

// internetWithin is the part of the internet (see redirectsAll) that lies in
// half; none when it has no part of it, as the upper half of the IPv6 space.
func internetWithin(half netip.Prefix) ([2]netip.Addr, bool) {
	internet := internetV4
	if half.Addr().Is6() {
		internet = internetV6
	}
	lo, hi := addrBytes(internet[0].As16()), addrBytes(internet[1].As16())
	inHalf := spanOf(half)
	first, last := inHalf.first, inHalf.last
	if first.less(lo) {
		first = lo
	}
	if hi.less(last) {
		last = hi
	}
	if last.less(first) {
		return [2]netip.Addr{}, false
	}
	return [2]netip.Addr{netip.AddrFrom16(first), netip.AddrFrom16(last)}, true
}

// logOverrides logs what changed since the last pass, so that a heartbeat does
// not repeat itself.
func (r *Reconciler) logOverrides(verdicts map[netip.Prefix]string, out map[netip.Prefix]outcome) {
	for _, dst := range slices.SortedFunc(maps.Keys(verdicts), comparePrefix) {
		if r.overridden[dst] != verdicts[dst] {
			r.log.Warn("a route of another program is used instead of ours", "route", dst, "reason", verdicts[dst])
		}
	}
	for _, dst := range slices.SortedFunc(maps.Keys(r.overridden), comparePrefix) {
		if _, still := verdicts[dst]; !still && out[dst] == installed {
			r.log.Info("our route is in use again", "route", dst)
		}
	}
}
