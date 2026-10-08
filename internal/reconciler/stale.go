package reconciler

import (
	"cmp"
	"net/netip"
	"slices"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// findStale lists the static host routes through a gateway that no longer match
// the network they were added for:
//   - the interface is gone or down,
//   - the gateway is not on a subnet of the interface any more (not asked of a
//     tunnel interface, see staleReason), or
//   - the route is for a tunnel endpoint and its gateway is not the default
//     route's nexthop, or, where a route is bound to its interface, it leaves
//     through another interface than the default route. Only endpoints are held
//     to this: a host route someone else added for another purpose may well use
//     another router.
//
// owned says whether the journal records Plaitway as the installer; those are
// repaired automatically, the rest are only reported.
func findStale(keying RouteKeying, table map[routeKey]osnet.Route, ns osnet.NetState, endpoints map[netip.Prefix]bool, owned func(osnet.Route) bool) []tunnel.StaleRoute {
	var stale []tunnel.StaleRoute
	for _, rt := range table {
		dst := rt.Dst.Masked()
		if !rt.Static || rt.Scoped || rt.Blackhole || !rt.Gateway.IsValid() || !dst.IsSingleIP() {
			continue
		}
		if reason := staleReason(keying, rt, ns, endpoints[dst]); reason != "" {
			stale = append(stale, tunnel.StaleRoute{Key: keying.identity(rt), Route: rt, Reason: reason, Owned: owned(rt)})
		}
	}
	slices.SortFunc(stale, func(a, b tunnel.StaleRoute) int { return cmp.Compare(a.Key, b.Key) })
	return stale
}

func staleReason(keying RouteKeying, rt osnet.Route, ns osnet.NetState, isEndpoint bool) string {
	if rt.Iface != "" {
		ifc, ok := findInterface(ns, rt.Iface)
		switch {
		case !ok:
			return "interface " + rt.Iface + " is gone"
		case !ifc.Up:
			return "interface " + rt.Iface + " is down"
		// A point-to-point tunnel lists only its own address: the peer that its
		// routes go through (OpenVPN's net30 topology, WireGuard's /32) is on no
		// subnet it shows. The peer does not move when the physical network does,
		// which is what this test is for.
		case !ifc.Tunnel && !onLink(ifc.Addrs, rt.Gateway):
			return "gateway is not on the subnet of " + rt.Iface
		}
	} else if !slices.ContainsFunc(ns.Interfaces, func(ifc osnet.Interface) bool { return ifc.Up && onLink(ifc.Addrs, rt.Gateway) }) {
		return "gateway is not on any connected subnet"
	}
	if isEndpoint {
		nh := ns.DefaultV4
		if rt.Dst.Addr().Is6() {
			nh = ns.DefaultV6
		}
		if nh != nil && nh.Gateway.WithZone("") != rt.Gateway.WithZone("") {
			return "gateway is not the default route's"
		}
		// Two interfaces can be on the router's network, the default route
		// moving from one to the other without the gateway changing.
		if nh != nil && keying.byInterface() && rt.Iface != "" && rt.Iface != nh.Iface {
			return "interface is not the default route's"
		}
	}
	return ""
}

func findInterface(ns osnet.NetState, name string) (osnet.Interface, bool) {
	for _, ifc := range ns.Interfaces {
		if ifc.Name == name {
			return ifc, true
		}
	}
	return osnet.Interface{}, false
}

// onLink reports whether addr is directly reachable through addrs, the
// configured prefixes of one interface. IPv6 link-local addresses always are.
func onLink(addrs []netip.Prefix, addr netip.Addr) bool {
	addr = addr.WithZone("")
	if addr.Is6() && addr.IsLinkLocalUnicast() {
		return true
	}
	return slices.ContainsFunc(addrs, func(p netip.Prefix) bool { return p.Contains(addr) })
}
