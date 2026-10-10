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
// It is RouteFailed and not a new state, and RouteReport.Overridden tells it from
// a route that could not be installed: the manager warns about every overridden
// route by that flag (tunnelStatusToProto), whatever else of the tunnel is
// installed, while its warning that a tunnel carries nothing counts only the
// routes that are not in the table. Shadowed, the normal state of a tunnel on
// standby, would not warn at all. Failed is also what macOS reports for a prefix
// another program holds ("held by another program"), which is the same situation
// found by another means. Pending would say that the route waits for something,
// and nothing is retried: the verdict is read again whenever the network changes.
const overriddenState = tunnel.RouteFailed

// markOverridden finds the routes of ours that are not the ones in use, because a
// route of another program wins, and reports them as overridden. The route stays
// where it is: lowering its metric below a foreign VPN's would start a fight over
// the host's traffic, and a foreign route is never ours to delete. When the other
// route goes, ours is in use at once, and the next pass says so.
//
// Only a table that gives routes a metric and keeps those of one prefix apart
// can hold such a pair. The verdict is the one Windows and Linux reach:
//   - Among routes to one prefix, the lowest effective metric wins: the metric of
//     the route plus, on Windows, the metric of its interface. A tie is not a
//     loss, because the system breaks it in a way that cannot be read.
//   - A route that leads where ours does is no rival. The kernel's own on-link
//     route of a tunnel's subnet has the metric 0 on Linux and the same interface
//     as a route of ours to that subnet, which a profile asks for whenever it
//     routes the tunnel's own network.
//   - A more specific route wins whatever its metric. That is how every route of
//     another program is meant to work, except for a default route: our two
//     halves are all that stands between the traffic and the physical
//     network, and a list of foreign routes that leaves out only what is not on
//     the internet (the full tunnel of a profile with the private ranges taken
//     out) takes all of it.
//
// An interface metric of zero is one the network state did not give; the detail
// says so when it takes part in a verdict.
//
// The table and the network state are read one after the other, so a route can
// name an interface that the state does not list yet (an adapter that has just
// appeared, with its routes). Such a route has no metric to compare and says
// nothing this pass: it is no rival, and the pass asks to be repeated with a
// fresh look at the network. A route that is still unlisted at the next pass
// belongs to an interface the state does not know, and counts with the interface
// metric unknown.
func (r *Reconciler) markOverridden(table map[routeKey]osnet.Route, out map[netip.Prefix]outcome, res *passResult) {
	if !r.keying.byInterface() {
		return
	}
	foreign, undecided := r.foreignRoutes(table)
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
		} else if slices.ContainsFunc(undecided, func(rival osnet.Route) bool { return competes(ours.Dst.Masked(), rival) }) {
			res.retry = true
		}
	}
	r.logOverrides(verdicts, out)
	r.overridden, res.overridden = verdicts, verdicts
}

// foreignRoutes are the routes in the table that are not ours and can carry
// traffic: a route through an interface that is down does not. A route through an
// interface the network state does not list is undecided the first time it is
// seen (see markOverridden), and a rival after that. It remembers which routes it
// found unlisted for the next pass.
func (r *Reconciler) foreignRoutes(table map[routeKey]osnet.Route) (rivals, undecided []osnet.Route) {
	unlisted := make(map[routeKey]bool)
	for _, key := range sortedKeys(table) {
		rt := table[key]
		if _, ours := r.owned[key]; ours {
			continue
		}
		if rt.IfIndex == 0 && rt.Iface == "" {
			// A blackhole names no interface, so there is none to wait for.
			rivals = append(rivals, rt)
			continue
		}
		ifc, known := routeInterface(r.netState, rt)
		switch {
		case !known:
			unlisted[key] = true
			if !r.unlisted[key] {
				undecided = append(undecided, rt)
				continue
			}
		case !ifc.Up:
			continue
		}
		rivals = append(rivals, rt)
	}
	r.unlisted = unlisted
	return rivals, undecided
}

// competes says whether a foreign route could be the one in use instead of ours
// to dst: one to the same prefix, or, for a half of the default route, one a
// program added inside it.
func competes(dst netip.Prefix, rival osnet.Route) bool {
	return rival.Dst.Masked() == dst || isDefault(dst) && addedInside(dst, rival)
}

// addedInside reports whether a program added rt, and to a prefix inside half.
// The routes the system derives from interface addresses are never a way out.
func addedInside(half netip.Prefix, rt osnet.Route) bool {
	dst := rt.Dst.Masked()
	return rt.Static && dst.Bits() > half.Bits() && half.Contains(dst.Addr())
}

// overrideReason says why a foreign route outranks ours, or "" when none does.
func (r *Reconciler) overrideReason(ours osnet.Route, foreign []osnet.Route) string {
	oursMetric := r.effectiveMetric(ours)
	var winner *osnet.Route
	winnerMetric := oursMetric
	for i, rival := range foreign {
		if rival.Dst.Masked() != ours.Dst.Masked() || leadsSameWay(ours, rival) {
			continue
		}
		if metric := r.effectiveMetric(rival); metric < winnerMetric {
			winner, winnerMetric = &foreign[i], metric
		}
	}
	if winner != nil {
		reason := fmt.Sprintf("overridden by %s: effective metric %d, ours %d", describe(*winner), winnerMetric, oursMetric)
		if r.keying.addsInterfaceMetric() && (!r.interfaceMetricKnown(ours) || !r.interfaceMetricKnown(*winner)) {
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
	if !r.keying.addsInterfaceMetric() {
		return uint64(rt.Metric)
	}
	ifc, _ := routeInterface(r.netState, rt)
	return uint64(rt.Metric) + uint64(ifc.Metric)
}

// leadsSameWay says whether two routes to one prefix send the traffic through
// the same interface and the same next hop, which makes the one no rival of the
// other whatever their metrics.
func leadsSameWay(a, b osnet.Route) bool {
	sameInterface := a.IfIndex == b.IfIndex && (a.IfIndex != 0 || a.Iface != "" && a.Iface == b.Iface)
	return sameInterface && a.Gateway.WithZone("") == b.Gateway.WithZone("")
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
		if !addedInside(half, rt) {
			continue
		}
		if len(pieces) == 0 {
			first = rt
		}
		pieces = append(pieces, rt.Dst.Masked())
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
