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

// defaultHolder is the best RoleFull tunnel that asks for a default route; live
// is already in precedence order.
func defaultHolder(live []tunnel.Intent) tunnel.OwnerID {
	for _, in := range live {
		if in.Role == tunnel.RoleFull && carriesRoutes(in.State) && slices.ContainsFunc(in.Routes, func(p netip.Prefix) bool {
			return p.IsValid() && isDefault(p.Masked())
		}) {
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
		for _, p := range expandRoutes(in.Routes) {
			plan := RoutePlan{
				Route: osnet.Route{Dst: p, Iface: in.Iface, Static: true},
				Owner: in.Owner,
				Kind:  tunnel.RouteTunnel,
			}
			if isDefault(p) {
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
