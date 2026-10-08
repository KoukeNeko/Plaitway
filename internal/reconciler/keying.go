package reconciler

import (
	"cmp"
	"errors"
	"fmt"
	"net/netip"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// RouteKeying says how the host's routing table tells one route from another,
// and with it everything the Reconciler decides from that: what it owns, what
// conflicts with it, what the journal remembers and what it must name when it
// adds a route. The zero value is the macOS behavior.
type RouteKeying uint8

const (
	// KeyByPrefix is the macOS table: one unscoped route per destination, so a
	// foreign route to the same prefix is a conflict whatever it goes through.
	// The kernel chooses the interface of a gateway route, which is why that
	// interface is not compared, and a route is added without an interface index
	// or a metric.
	KeyByPrefix RouteKeying = iota
	// KeyByPrefixInterfaceNextHop is the Windows table: a route is the
	// destination plus the interface plus the next hop, so several routes can
	// share a prefix and the lowest metric picks the one that is used. Plaitway
	// names the interface index and a metric on every route it adds, and a
	// foreign route to the same prefix through another interface is not a
	// conflict but a neighbour, which may be the one that is used (see
	// markOverridden). The metric is not part of the key; the next hop is, and a
	// tunnel route has the tunnel's own gateway as its next hop when it has one.
	KeyByPrefixInterfaceNextHop
)

// Metrics Plaitway gives the routes it adds on Windows. The system adds the
// metric of the interface to that of the route. The default route is split into
// halves, which win against the physical default route by their prefix length;
// the metric decides only between routes of one prefix, and there a bypass
// route to a tunnel endpoint must win against a tunnel route.
const (
	windowsBypassMetric = 1
	windowsTunnelMetric = 5
)

// Interface names: IFNAMSIZ on macOS, IF_MAX_STRING_SIZE on Windows.
const (
	maxIfaceNameMacOS   = 15
	maxIfaceNameWindows = 256
)

func (k RouteKeying) valid() bool {
	return k == KeyByPrefix || k == KeyByPrefixInterfaceNextHop
}

func (k RouteKeying) byInterface() bool { return k == KeyByPrefixInterfaceNextHop }

func (k RouteKeying) maxIfaceName() int {
	if k.byInterface() {
		return maxIfaceNameWindows
	}
	return maxIfaceNameMacOS
}

// routeKey identifies a route in the table. On macOS only dst is set.
type routeKey struct {
	dst     netip.Prefix
	ifIndex uint32
	gateway netip.Addr
}

// String is the prefix alone where that is the whole key, so that log lines and
// errors read as they always did on macOS.
func (k routeKey) String() string {
	if k.ifIndex == 0 {
		return k.dst.String()
	}
	return fmt.Sprintf("%s if%d via %s", k.dst, k.ifIndex, addrString(k.gateway))
}

func compareKeys(a, b routeKey) int {
	return cmp.Or(comparePrefix(a.dst, b.dst), cmp.Compare(a.ifIndex, b.ifIndex), a.gateway.Compare(b.gateway))
}

// key is the identity of rt in the table.
func (k RouteKeying) key(rt osnet.Route) routeKey {
	key := routeKey{dst: rt.Dst.Masked()}
	if k.byInterface() {
		key.ifIndex, key.gateway = rt.IfIndex, rt.Gateway.WithZone("")
	}
	return key
}

// identity is the text of key for the stale list, where a user names one route:
// the prefix, and on Windows the interface index and next hop after it.
func (k RouteKeying) identity(rt osnet.Route) string {
	if !k.byInterface() {
		return rt.Dst.Masked().String()
	}
	return fmt.Sprintf("%s@%d/%s", rt.Dst.Masked(), rt.IfIndex, addrString(rt.Gateway))
}

// same says whether two routes are the same thing for the Reconciler: the one it
// asked for and the one in the table, or the one it installed and the one there
// now.
//
// On macOS that is what decides where traffic goes. The interface of a gateway
// route is the kernel's choice: it follows the gateway, and when two interfaces
// are on the gateway's network (Wi-Fi and Ethernet to the same router) it need
// not be the one the route was added for, so it is not compared. A route without
// a gateway is bound to its interface, which is compared when both sides name
// one.
//
// On Windows the key is the identity. The metric is not compared: a route with
// the key we want is where we want traffic to go, whatever metric it has, and it
// is the key that tells which route Delete removes.
func (k RouteKeying) same(a, b osnet.Route) bool {
	if a.Blackhole != b.Blackhole {
		return false
	}
	if k.byInterface() {
		return k.key(a) == k.key(b)
	}
	if a.Dst.Masked() != b.Dst.Masked() || a.Gateway.WithZone("") != b.Gateway.WithZone("") {
		return false
	}
	if a.Gateway.IsValid() {
		return true
	}
	return a.Iface == "" || b.Iface == "" || a.Iface == b.Iface
}

// fingerprint identifies a route as installed: where it goes and the kernel's
// flags. A route someone else replaced has a different one. The macOS form is
// what old journals hold and stays as it is.
func (k RouteKeying) fingerprint(rt osnet.Route) string {
	if k.byInterface() {
		return fmt.Sprintf("%s|%d|%d|%#x", addrString(rt.Gateway), rt.IfIndex, rt.Metric, rt.Flags)
	}
	return fmt.Sprintf("%s|%s|%#x", addrString(rt.Gateway), rt.Iface, rt.Flags)
}

// journaled says whether a crash could leave rt behind. On macOS a route bound
// to an interface vanishes with it. On Windows a tunnel adapter that was not
// closed (the process was killed) stays, and its routes with it, so every route
// is journaled.
func (k RouteKeying) journaled(rt osnet.Route) bool {
	return k.byInterface() || rt.Gateway.IsValid()
}

// errOtherTable: a journal record was written for a table keyed another way (a
// macOS record has no interface index), so it does not say which route it was.
var errOtherTable = errors.New("written for another kind of routing table")

// recordKey is the key of the route a journal record stands for. An error means
// the record cannot be matched to a route in this table: it is unreadable, or
// errOtherTable. Such a record is never turned into a delete.
func (k RouteKeying) recordKey(rec record) (routeKey, error) {
	dst, err := netip.ParsePrefix(rec.Key)
	if err != nil {
		return routeKey{}, err
	}
	key := routeKey{dst: dst.Masked()}
	if !k.byInterface() {
		return key, nil
	}
	if rec.IfIndex == 0 {
		return routeKey{}, errOtherTable
	}
	key.ifIndex = rec.IfIndex
	if rec.Gateway != "" {
		if key.gateway, err = netip.ParseAddr(rec.Gateway); err != nil {
			return routeKey{}, fmt.Errorf("unreadable gateway: %w", err)
		}
	}
	return key, nil
}

// recognizes reports whether a route in the table is the one the journal
// recorded. A record that was never confirmed has no fingerprint, only what the
// route was asked to use: on macOS that is compared, on Windows the route was
// found by its whole key already.
func (k RouteKeying) recognizes(rec record, actual osnet.Route) bool {
	if rec.Fingerprint != "" {
		return k.fingerprint(actual) == rec.Fingerprint
	}
	if k.byInterface() {
		return true
	}
	return addrString(actual.Gateway) == rec.Gateway && (rec.Iface == "" || actual.Iface == rec.Iface)
}

// stamp writes into a journal record what makes the route's key.
func (k RouteKeying) stamp(rec *record, rt osnet.Route) {
	rec.Gateway, rec.Iface = addrString(rt.Gateway), rt.Iface
	rec.IfIndex = k.key(rt).ifIndex
}

// tunnelRoute is the route that sends dst into the tunnel in. On Windows it has
// the tunnel's own gateway as its next hop when the tunnel has one that fits dst:
// an Ethernet-like adapter (tap-windows6) answers only for that address, so a
// route bound to the adapter without a next hop would send traffic nowhere.
// macOS routes are bound to the interface and never name a next hop.
func (k RouteKeying) tunnelRoute(dst netip.Prefix, in tunnel.Intent, ns osnet.NetState) osnet.Route {
	rt := osnet.Route{Dst: dst, Iface: in.Iface, Static: true}
	if k.byInterface() {
		rt.IfIndex, rt.Metric = interfaceIndex(ns, in.Iface), windowsTunnelMetric
		rt.Gateway = tunnelGateway(in, dst)
	}
	return rt
}

// tunnelGateway is the next hop for a route to dst out of the gateways a tunnel
// announced: the one of dst's family, or none (the route is on-link) when the
// tunnel announced no usable address of that family.
func tunnelGateway(in tunnel.Intent, dst netip.Prefix) netip.Addr {
	if dst.Addr().Is4() {
		return usableGateway(in.Gateway, true)
	}
	return usableGateway(in.GatewayV6, false)
}

// usableGateway is gateway as a next hop of the IPv4 (v4) or the IPv6 family, or
// the zero Addr when it is not an address that can be one.
func usableGateway(gateway netip.Addr, v4 bool) netip.Addr {
	gateway = gateway.Unmap().WithZone("")
	switch {
	case !gateway.IsValid(), gateway.IsUnspecified(), gateway.IsLoopback(), gateway.IsMulticast():
		return netip.Addr{}
	case gateway.Is4() != v4:
		return netip.Addr{}
	}
	return gateway
}

// needsNextHop says whether a route to dst of this tunnel cannot be delivered
// without a next hop it lacks. A tunnel that names a gateway in either family is
// an Ethernet-like adapter (tap-windows6), which answers only for those
// addresses: a route of the other family bound to the adapter would wait for a
// neighbour that never answers, while it is reported installed. A tunnel with
// none (Wintun, utun) takes on-link routes in both families. The halves of a
// default route are left alone: a full tunnel installs them in both families so
// that nothing leaks around a tunnel that carries one, and a route that leads
// nowhere is what that needs.
func (k RouteKeying) needsNextHop(in tunnel.Intent, dst netip.Prefix) bool {
	return k.byInterface() && !isDefault(dst) && !tunnelGateway(in, dst).IsValid() &&
		(usableGateway(in.Gateway, true).IsValid() || usableGateway(in.GatewayV6, false).IsValid())
}

// noNextHopDetail is why a route to dst is not installed when needsNextHop.
func noNextHopDetail(dst netip.Prefix) string {
	if dst.Addr().Is4() {
		return "no IPv4 gateway in the tunnel"
	}
	return "no IPv6 gateway in the tunnel"
}

// bypassRoute is the host route that keeps an endpoint reachable through the
// physical default route.
func (k RouteKeying) bypassRoute(host netip.Prefix, via osnet.Nexthop, ns osnet.NetState) osnet.Route {
	rt := osnet.Route{Dst: host, Gateway: via.Gateway, Iface: via.Iface, Static: true}
	if k.byInterface() {
		rt.IfIndex, rt.Metric = interfaceIndex(ns, via.Iface), windowsBypassMetric
	}
	return rt
}

// unplaceable says why rt cannot be added yet, or "" when it can. A Windows
// route names its interface by index, and an interface that is not in the
// network state (it was just removed, or not seen yet) has none.
func (k RouteKeying) unplaceable(rt osnet.Route) string {
	if k.byInterface() && rt.IfIndex == 0 {
		return "interface " + rt.Iface + " not found"
	}
	return ""
}

// interfaceIndex is the index of the interface called name, zero when the
// network state does not list it.
func interfaceIndex(ns osnet.NetState, name string) uint32 {
	if ifc, ok := findInterface(ns, name); ok {
		return uint32(ifc.Index)
	}
	return 0
}
