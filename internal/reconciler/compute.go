package reconciler

import (
	"cmp"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// The halves a default route is split into, so that the system's own default
// route is never replaced: a /1 is more specific than the /0 it covers.
var (
	v4Halves = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/1"), netip.MustParsePrefix("128.0.0.0/1")}
	v6Halves = []netip.Prefix{netip.MustParsePrefix("::/1"), netip.MustParsePrefix("8000::/1")}
)

// Desired is what the host's routes and DNS should look like for a set of
// intents, including what was asked for and refused.
type Desired struct {
	Routes []RoutePlan
	DNS    []DNSPlan
}

// RoutePlan is the fate of one route a tunnel asked for, or that its endpoint
// needs.
type RoutePlan struct {
	// Route is what to install when Install is set. Otherwise only Dst, Iface
	// and Gateway are meaningful, for display.
	Route  osnet.Route
	Owner  tunnel.OwnerID
	Kind   tunnel.RouteKind
	State  tunnel.RouteState // RoutePending when it is to be installed
	Detail string            // why it is not installed
	// ShadowedBy is the owner that holds the prefix, or the default route when
	// the owner is on standby.
	ShadowedBy tunnel.OwnerID
	// Install is false for routes that lost, are blocked or have to wait.
	Install bool
}

// DNSPlan is the fate of one DNSIntent.
type DNSPlan struct {
	Owner tunnel.OwnerID
	// Servers are the reachable ones when the plan is installed, all of them
	// otherwise.
	Servers []netip.Addr
	// MatchDomains are the domains this plan installs, or asked for when it
	// does not install anything.
	MatchDomains []string
	Order        int
	State        tunnel.RouteState // RoutePending when it is to be installed
	Detail       string
	ShadowedBy   tunnel.OwnerID
}

// carriesRoutes says whether a tunnel in this state holds its routes and DNS.
// A reconnecting tunnel keeps its interface and its routes (see
// tunnel.StateReconnecting), so removing them would leak traffic until it is
// back.
func carriesRoutes(s tunnel.State) bool {
	return s == tunnel.StateUp || s == tunnel.StateReconnecting
}

// carriesEndpoints says whether the tunnel's own traffic may be flowing and so
// needs its endpoints to stay reachable.
func carriesEndpoints(s tunnel.State) bool {
	switch s {
	case tunnel.StateConnecting, tunnel.StateAwaitingCredentials, tunnel.StateUp, tunnel.StateReconnecting:
		return true
	}
	return false
}

// Compute decides what the host should look like for the given intents and
// physical network. It is a pure function: the same input always gives the same
// Desired, whatever the order of intents.
//
// Rules:
//   - Only tunnels that carry routes (up or reconnecting) contribute routes and
//     DNS; a connecting tunnel contributes only its endpoints.
//   - A default route is split into halves. Only the best RoleFull tunnel gets
//     them, the others are on standby and take over when it goes. A RoleSplit
//     tunnel never gets a default route.
//   - For every other prefix the lowest Priority wins, then the earliest
//     UpSince, then the Owner. Losers are shadowed and keep their intent, so
//     they are promoted in the next round.
//   - A prefix equal to or inside a directly attached subnet is blocked.
//   - An endpoint that an installed route would capture gets a host route
//     through the current default nexthop of the physical network.
//   - DNS: one winner per domain; the catch-all only for the owner of the
//     default routes; servers must be reached through the owner's interface.
func Compute(intents []tunnel.Intent, ns osnet.NetState) Desired {
	live := slices.Clone(intents)
	slices.SortStableFunc(live, compareIntents)
	holder := defaultHolder(live)

	routes := routePlans(live, holder, ns)
	bypass := bypassPlans(live, routes, ns)
	routes = append(routes, bypass...)
	demoteEndpointRoutes(routes)
	slices.SortStableFunc(routes, comparePlans)

	return Desired{Routes: routes, DNS: dnsPlans(live, holder, routes, ns)}
}

func compareIntents(a, b tunnel.Intent) int {
	return cmp.Or(
		cmp.Compare(a.Priority, b.Priority),
		a.UpSince.Compare(b.UpSince),
		cmp.Compare(a.Owner, b.Owner),
	)
}

// isDefault reports whether p redirects everything: a default route or one of
// its halves.
func isDefault(p netip.Prefix) bool {
	return p.Bits() == 0 || slices.Contains(v4Halves, p) || slices.Contains(v6Halves, p)
}

// redirectsAll reports whether routes send the whole internet through the
// tunnel: a default route or its halves, or a list that leaves out only what is
// not on the internet. That is the list of a full tunnel with the private
// ranges taken out ("0.0.0.0/5, 8.0.0.0/7, 11.0.0.0/8, ..."), which has no
// default route although it is the tunnel everything goes through. A list with
// any other hole does not count: the traffic in the hole would then have no
// tunnel at all, where a lower priority tunnel's default route would have
// caught it.
func redirectsAll(routes []netip.Prefix) bool {
	var v4, v6 []netip.Prefix
	for _, p := range routes {
		if !p.IsValid() {
			continue
		}
		p = p.Masked()
		if isDefault(p) {
			return true
		}
		if p.Addr().Is4() {
			v4 = append(v4, p)
		} else {
			v6 = append(v6, p)
		}
	}
	return coversAllBut(v4, internetV4, notOnTheInternetV4) || coversAllBut(v6, internetV6, nil)
}

var (
	// internetV4 is the unicast space, and internetV6 the global unicast range.
	internetV4 = [2]netip.Addr{netip.MustParseAddr("1.0.0.0"), netip.MustParseAddr("223.255.255.255")}
	internetV6 = [2]netip.Addr{netip.MustParseAddr("2000::"), netip.MustParseAddr("3fff:ffff:ffff:ffff:ffff:ffff:ffff:ffff")}
	// notOnTheInternetV4 is what a list may leave out of internetV4: the private ranges and
	// the ones the kernel keeps for itself.
	notOnTheInternetV4 = pfxs4("10.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16")
)

func pfxs4(prefixes ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(prefixes))
	for i, p := range prefixes {
		out[i] = netip.MustParsePrefix(p)
	}
	return out
}

// addrBytes is an address as 16 bytes, which orders both families and can be stepped.
type addrBytes [16]byte

func (a addrBytes) next() addrBytes {
	for i := len(a) - 1; i >= 0; i-- {
		a[i]++
		if a[i] != 0 {
			break
		}
	}
	return a
}

func (a addrBytes) prev() addrBytes {
	for i := len(a) - 1; i >= 0; i-- {
		a[i]--
		if a[i] != 0xff {
			break
		}
	}
	return a
}

func (a addrBytes) less(b addrBytes) bool { return slices.Compare(a[:], b[:]) < 0 }

// span is the addresses of a prefix, first to last.
type span struct{ first, last addrBytes }

func spanOf(p netip.Prefix) span {
	first := addrBytes(p.Addr().As16())
	last := first
	bits := p.Bits()
	if p.Addr().Is4() {
		bits += 96
	}
	for i := bits; i < 128; i++ {
		last[i/8] |= 0x80 >> (i % 8)
	}
	return span{first, last}
}

// merged returns the spans of the prefixes in order, with overlapping and adjacent ones joined.
func merged(prefixes []netip.Prefix) []span {
	spans := make([]span, len(prefixes))
	for i, p := range prefixes {
		spans[i] = spanOf(p)
	}
	slices.SortFunc(spans, func(a, b span) int { return slices.Compare(a.first[:], b.first[:]) })
	var out []span
	for _, s := range spans {
		if n := len(out); n > 0 && !out[n-1].last.next().less(s.first) {
			if out[n-1].last.less(s.last) {
				out[n-1].last = s.last
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

// coversAllBut reports whether prefixes cover every address from universe[0] to
// universe[1] except those inside the allowed prefixes.
func coversAllBut(prefixes []netip.Prefix, universe [2]netip.Addr, allowed []netip.Prefix) bool {
	cover, allowedSpans := merged(prefixes), merged(allowed)
	within := func(gap span) bool {
		return slices.ContainsFunc(allowedSpans, func(a span) bool { return !gap.first.less(a.first) && !a.last.less(gap.last) })
	}
	cursor, end := addrBytes(universe[0].As16()), addrBytes(universe[1].As16())
	for _, s := range cover {
		if s.last.less(cursor) {
			continue
		}
		if cursor.less(s.first) {
			gap := span{cursor, s.first.prev()}
			if end.less(gap.last) {
				gap.last = end
			}
			if !within(gap) {
				return false
			}
		}
		if end.less(s.last) {
			return true
		}
		cursor = s.last.next()
	}
	return end.less(cursor) || within(span{cursor, end})
}

// hasDefault reports whether routes include a default route or one of its halves.
func hasDefault(routes []netip.Prefix) bool {
	return slices.ContainsFunc(routes, func(p netip.Prefix) bool { return p.IsValid() && isDefault(p.Masked()) })
}

// defaultHolder is the best RoleFull tunnel that sends the internet through
// itself; live is already in precedence order.
func defaultHolder(live []tunnel.Intent) tunnel.OwnerID {
	for _, in := range live {
		if in.Role == tunnel.RoleFull && carriesRoutes(in.State) && redirectsAll(in.Routes) {
			return in.Owner
		}
	}
	return ""
}

// expandRoutes masks the prefixes, splits default routes into halves and drops
// duplicates.
func expandRoutes(routes []netip.Prefix) []netip.Prefix {
	var out []netip.Prefix
	add := func(p netip.Prefix) {
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	for _, r := range routes {
		if !r.IsValid() {
			continue
		}
		r = r.Masked()
		switch {
		case r.Bits() != 0:
			add(r)
		case r.Addr().Is4():
			for _, h := range v4Halves {
				add(h)
			}
		default:
			for _, h := range v6Halves {
				add(h)
			}
		}
	}
	return out
}

func routePlans(live []tunnel.Intent, holder tunnel.OwnerID, ns osnet.NetState) []RoutePlan {
	winners := make(map[netip.Prefix]tunnel.OwnerID)
	var plans []RoutePlan
	for _, in := range live {
		if !carriesRoutes(in.State) {
			continue
		}
		// A full tunnel that is not the holder stands by as a whole. With the private
		// ranges left out it has no default route to stand by with: its prefixes are the
		// default, in pieces, and are longer than the holder's halves, so installed they
		// would take everything over.
		standby := in.Role == tunnel.RoleFull && in.Owner != holder && redirectsAll(in.Routes) && !hasDefault(in.Routes)
		for _, p := range expandRoutes(in.Routes) {
			plan := RoutePlan{
				Route: osnet.Route{Dst: p, Iface: in.Iface, Static: true},
				Owner: in.Owner,
				Kind:  tunnel.RouteTunnel,
			}
			if isDefault(p) || standby {
				plan.Kind = tunnel.RouteDefaultHalf
				if in.Role != tunnel.RoleFull {
					continue
				}
			}
			switch sub, blocked := attachedSubnet(ns.Connected, p); {
			case plan.Kind == tunnel.RouteDefaultHalf && in.Owner != holder:
				plan.State, plan.Detail, plan.ShadowedBy = tunnel.RouteShadowed, "standby", holder
			case special(p):
				plan.State, plan.Detail = tunnel.RouteBlocked, "special address range"
			case blocked:
				plan.State, plan.Detail = tunnel.RouteBlocked, "local subnet "+sub.String()
			case winners[p] != "":
				plan.State, plan.ShadowedBy = tunnel.RouteShadowed, winners[p]
			default:
				winners[p] = in.Owner
				plan.State, plan.Install = tunnel.RoutePending, true
			}
			plans = append(plans, plan)
		}
	}
	return plans
}

// attachedSubnet returns the directly attached subnet that equals or contains
// p.
func attachedSubnet(connected []netip.Prefix, p netip.Prefix) (netip.Prefix, bool) {
	for _, c := range connected {
		c = c.Masked()
		if c.Addr().BitLen() == p.Addr().BitLen() && c.Bits() <= p.Bits() && c.Contains(p.Addr()) {
			return c, true
		}
	}
	return netip.Prefix{}, false
}

// specialRanges are the ranges that must not be routed through a tunnel: this
// network and the unspecified address, loopback, link-local and multicast.
var specialRanges = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

// special reports whether p lies inside one of the specialRanges, or is an
// address form routes cannot express. The kernel's own routes for most of them
// would win anyway; a longer prefix inside one would not. A prefix that only
// starts in such a range or covers it is not special: 0.0.0.0/5 and 224.0.0.0/3
// are part of every "all addresses except the private ranges" list, and the
// kernel's routes for the special ranges inside them are more specific.
func special(p netip.Prefix) bool {
	a := p.Addr()
	if isDefault(p) {
		return false
	}
	if a.Is4In6() || a.Zone() != "" {
		return true
	}
	return slices.ContainsFunc(specialRanges, func(r netip.Prefix) bool {
		return r.Bits() <= p.Bits() && r.Contains(a)
	})
}

// routable reports whether traffic to a can leave through the physical network
// at all: not loopback (an OpenVPN profile may well point at a local proxy),
// not unspecified, multicast or link-local.
func routable(a netip.Addr) bool {
	return a.IsValid() && a.IsGlobalUnicast()
}

func bypassPlans(live []tunnel.Intent, routes []RoutePlan, ns osnet.NetState) []RoutePlan {
	var captors []netip.Prefix
	for _, r := range routes {
		if r.Install {
			captors = append(captors, r.Route.Dst)
		}
	}
	captured := func(a netip.Addr) bool {
		return slices.ContainsFunc(captors, func(p netip.Prefix) bool { return p.Contains(a) })
	}
	onLink := func(a netip.Addr) bool {
		return slices.ContainsFunc(ns.Connected, func(p netip.Prefix) bool { return p.Contains(a) })
	}

	seen := make(map[netip.Prefix]bool)
	var plans []RoutePlan
	for _, in := range live {
		if !carriesEndpoints(in.State) {
			continue
		}
		for _, ep := range in.Endpoints {
			if !ep.IsValid() {
				continue
			}
			ep = ep.Unmap().WithZone("")
			host := netip.PrefixFrom(ep, ep.BitLen())
			if !routable(ep) || seen[host] || onLink(ep) || !captured(ep) {
				continue
			}
			seen[host] = true
			plan := RoutePlan{
				Route: osnet.Route{Dst: host},
				Owner: in.Owner,
				Kind:  tunnel.RouteBypass,
				State: tunnel.RoutePending,
			}
			nh := ns.DefaultV4
			if ep.Is6() {
				nh = ns.DefaultV6
			}
			if nh == nil {
				plan.Detail = "no network"
			} else {
				plan.Route.Gateway, plan.Route.Iface, plan.Route.Static = nh.Gateway, nh.Iface, true
				plan.Install = true
			}
			plans = append(plans, plan)
		}
	}
	return plans
}

// demoteEndpointRoutes shadows a tunnel route that names an endpoint's host
// route itself: one prefix can only have one route, and the endpoint must stay
// reachable outside the tunnels.
func demoteEndpointRoutes(plans []RoutePlan) {
	bypass := make(map[netip.Prefix]tunnel.OwnerID)
	for _, p := range plans {
		if p.Kind == tunnel.RouteBypass && p.Install {
			bypass[p.Route.Dst] = p.Owner
		}
	}
	for i, p := range plans {
		if owner, ok := bypass[p.Route.Dst]; ok && p.Install && p.Kind != tunnel.RouteBypass {
			plans[i].State, plans[i].Install = tunnel.RouteShadowed, false
			plans[i].Detail, plans[i].ShadowedBy = "tunnel endpoint", owner
		}
	}
}

// kindRank is the apply order: bypass routes first, then tunnel routes, then
// the default halves. Teardown goes the other way.
func kindRank(k tunnel.RouteKind) int {
	switch k {
	case tunnel.RouteBypass:
		return 0
	case tunnel.RouteTunnel:
		return 1
	default:
		return 2
	}
}

func comparePlans(a, b RoutePlan) int {
	return cmp.Or(
		cmp.Compare(kindRank(a.Kind), kindRank(b.Kind)),
		comparePrefix(a.Route.Dst, b.Route.Dst),
		compareBool(b.Install, a.Install), // the installed one first
		cmp.Compare(a.Owner, b.Owner),
	)
}

// comparePrefix orders prefixes by family, address and length.
func comparePrefix(a, b netip.Prefix) int {
	return cmp.Or(
		cmp.Compare(a.Addr().BitLen(), b.Addr().BitLen()),
		a.Addr().Compare(b.Addr()),
		cmp.Compare(a.Bits(), b.Bits()),
	)
}

func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case !a:
		return -1
	}
	return 1
}

// routeLookup answers which interface the kernel would send an address through
// once the installed routes are in place: the longest matching prefix among the
// interface subnets, the planned routes and the physical default routes. At
// equal length the earlier of those wins, which is the order the kernel
// acquired them in.
func routeLookup(routes []RoutePlan, ns osnet.NetState) func(netip.Addr) (string, bool) {
	type entry struct {
		prefix netip.Prefix
		iface  string
	}
	var table []entry
	for _, ifc := range ns.Interfaces {
		if !ifc.Up {
			continue
		}
		for _, a := range ifc.Addrs {
			table = append(table, entry{a.Masked(), ifc.Name})
		}
	}
	for _, r := range routes {
		if r.Install {
			table = append(table, entry{r.Route.Dst, r.Route.Iface})
		}
	}
	if ns.DefaultV4 != nil {
		table = append(table, entry{netip.PrefixFrom(netip.IPv4Unspecified(), 0), ns.DefaultV4.Iface})
	}
	if ns.DefaultV6 != nil {
		table = append(table, entry{netip.PrefixFrom(netip.IPv6Unspecified(), 0), ns.DefaultV6.Iface})
	}
	return func(addr netip.Addr) (string, bool) {
		best, iface := -1, ""
		for _, e := range table {
			if e.prefix.Contains(addr) && e.prefix.Bits() > best {
				best, iface = e.prefix.Bits(), e.iface
			}
		}
		return iface, best >= 0
	}
}

// normalizeDomains lowercases, drops the trailing dot and duplicates, and
// leaves out what is not a domain name: a pushed domain ends up in a resolver
// entry. No domain at all means everything; domains that are all unusable mean
// nothing, not everything.
func normalizeDomains(domains []string) []string {
	if len(domains) == 0 {
		return []string{"."}
	}
	var out []string
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "." {
			d = strings.TrimSuffix(d, ".")
		}
		if validDomain(d) && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	return out
}

// validDomain accepts "." and dot-separated labels of letters, digits, hyphens
// and underscores.
func validDomain(d string) bool {
	if d == "." {
		return true
	}
	if d == "" || len(d) > 253 {
		return false
	}
	for _, label := range strings.Split(d, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			switch {
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
			default:
				return false
			}
		}
	}
	return true
}

func dnsPlans(live []tunnel.Intent, holder tunnel.OwnerID, routes []RoutePlan, ns osnet.NetState) []DNSPlan {
	lookup := routeLookup(routes, ns)
	claimed := make(map[string]tunnel.OwnerID)
	var plans []DNSPlan
	for _, in := range live {
		if !carriesRoutes(in.State) {
			continue
		}
		for _, d := range in.DNS {
			plans = append(plans, dnsPlan(in, d, holder, lookup, claimed))
		}
	}
	return plans
}

// dnsPlan decides one DNSIntent. claimed holds the domains that better
// tunnels have taken; the winners of this one are added to it. A nameserver that
// is not reached through the owner's interface does not compete for domains.
func dnsPlan(in tunnel.Intent, d tunnel.DNSIntent, holder tunnel.OwnerID, lookup func(netip.Addr) (string, bool), claimed map[string]tunnel.OwnerID) DNSPlan {
	plan := DNSPlan{
		Owner:        in.Owner,
		Servers:      slices.Clone(d.Servers),
		MatchDomains: normalizeDomains(d.MatchDomains),
		Order:        in.Priority,
		State:        tunnel.RouteBlocked,
	}
	var reached []netip.Addr
	var notes []string
	for _, s := range d.Servers {
		switch iface, ok := lookup(s.WithZone("")); {
		case !osnet.ValidDNSServer(s):
			notes = append(notes, s.String()+" is not a usable nameserver")
		case ok && iface == in.Iface:
			reached = append(reached, s)
		case ok:
			notes = append(notes, fmt.Sprintf("%s is reached through %s", s, iface))
		default:
			notes = append(notes, "no route to "+s.String())
		}
	}
	switch {
	case len(d.Servers) == 0:
		plan.Detail = "no nameserver"
		return plan
	case len(reached) == 0:
		plan.Detail = strings.Join(notes, "; ")
		return plan
	case len(plan.MatchDomains) == 0:
		plan.Detail = "no valid domain"
		return plan
	}
	plan.Servers = reached

	var won, lost []string
	for _, dom := range plan.MatchDomains {
		switch {
		case dom == "." && in.Owner != holder:
			notes = append([]string{"catch-all is for the default route owner"}, notes...)
		case claimed[dom] != "":
			lost = append(lost, dom)
			plan.ShadowedBy = claimed[dom]
		default:
			claimed[dom] = in.Owner
			won = append(won, dom)
		}
	}
	plan.State = tunnel.RouteShadowed
	if len(won) > 0 {
		plan.State, plan.MatchDomains, plan.ShadowedBy = tunnel.RoutePending, won, ""
		if len(lost) > 0 {
			notes = append([]string{"shadowed: " + strings.Join(lost, ", ")}, notes...)
		}
	}
	plan.Detail = strings.Join(notes, "; ")
	return plan
}
