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
	// KeyLinux is the Linux table. The kernel tells the routes of one prefix
	// apart by their metric alone: a second route with the same destination and
	// metric is refused whatever interface and next hop it has, and routes with
	// other metrics are neighbours that each have an interface and a next hop of
	// their own. So the key is the destination and the metric, and the interface
	// index and next hop are attributes of the route, compared like the gateway
	// on macOS. Plaitway names the interface index and a metric on every route it
	// adds. The metric of a route is the whole story: Linux has no interface
	// metric. Tunnel devices (tun, WireGuard) are point-to-point layer 3 devices
	// that take on-link routes in both address families, so a tunnel route never
	// names a next hop, whatever gateway the tunnel announced.
	KeyLinux
)

// Metrics Plaitway gives the routes it adds where routes carry one. The default
// route is split into halves, which win against the physical default route by
// their prefix length; the metric decides only between routes of one prefix, and
// there a bypass route to a tunnel endpoint must win against a tunnel route: a
// tunnel route that is still in the table for the endpoint's own address (one of
// ours that is being replaced by the bypass route, or another program's) would
// otherwise send the tunnel's own packets into the tunnel.
const (
	bypassMetric = 1
	tunnelMetric = 5
)

// Longest interface names: IFNAMSIZ less the terminator on macOS and Linux,
// IF_MAX_STRING_SIZE on Windows.
const (
	maxIfaceNameMacOS   = 15
	maxIfaceNameLinux   = 15
	maxIfaceNameWindows = 256
)

func (k RouteKeying) valid() bool {
	return k == KeyByPrefix || k == KeyByPrefixInterfaceNextHop || k == KeyLinux
}

// byInterface says whether a route names its interface by index and carries a
// metric, so that a route of another program to the same prefix through another
// interface is a neighbour that may be the one in use, not a conflict.
func (k RouteKeying) byInterface() bool { return k == KeyByPrefixInterfaceNextHop || k == KeyLinux }

// keyedByMetric says whether the metric is part of a route's key, and its
// interface and next hop are not (Linux). Elsewhere the metric is an attribute.
func (k RouteKeying) keyedByMetric() bool { return k == KeyLinux }

// namesTunnelGateway says whether a tunnel route has the tunnel's own gateway as
// its next hop when the tunnel announced one (Windows). Where tunnel devices are
// point-to-point (macOS, Linux) the route is bound to the interface.
func (k RouteKeying) namesTunnelGateway() bool { return k == KeyByPrefixInterfaceNextHop }

// addsInterfaceMetric says whether the system adds the metric of an interface to
// the metric of every route through it (Windows).
func (k RouteKeying) addsInterfaceMetric() bool { return k == KeyByPrefixInterfaceNextHop }

// dnsPerInterface says whether the system keeps resolver settings per interface
// (Linux, systemd-resolved), so that a resolver entry has to name the tunnel it
// belongs to.
func (k RouteKeying) dnsPerInterface() bool { return k == KeyLinux }

func (k RouteKeying) maxIfaceName() int {
	switch k {
	case KeyByPrefixInterfaceNextHop:
		return maxIfaceNameWindows
	case KeyLinux:
		return maxIfaceNameLinux
	}
	return maxIfaceNameMacOS
}

// routeKey identifies a route in the table. On macOS only dst is set; with
// KeyLinux dst and metric are.
type routeKey struct {
	dst     netip.Prefix
	ifIndex uint32
	gateway netip.Addr
	metric  uint32
}

// String is the prefix alone where that is the whole key, so that log lines and
// errors read as they always did on macOS.
func (k routeKey) String() string {
	switch {
	case k.metric != 0:
		return fmt.Sprintf("%s metric %d", k.dst, k.metric)
	case k.ifIndex == 0:
		return k.dst.String()
	}
	return fmt.Sprintf("%s if%d via %s", k.dst, k.ifIndex, addrString(k.gateway))
}

func compareKeys(a, b routeKey) int {
	return cmp.Or(comparePrefix(a.dst, b.dst), cmp.Compare(a.ifIndex, b.ifIndex), a.gateway.Compare(b.gateway), cmp.Compare(a.metric, b.metric))
}

// key is the identity of rt in the table.
func (k RouteKeying) key(rt osnet.Route) routeKey {
	key := routeKey{dst: rt.Dst.Masked()}
	switch k {
	case KeyByPrefixInterfaceNextHop:
		key.ifIndex, key.gateway = rt.IfIndex, rt.Gateway.WithZone("")
	case KeyLinux:
		key.metric = rt.Metric
	}
	return key
}

// identity is the text of key for the stale list, where a user names one route:
// the prefix, and where a table tells routes apart by more than that the
// interface index and next hop after it, and on Linux the metric.
func (k RouteKeying) identity(rt osnet.Route) string {
	if !k.byInterface() {
		return rt.Dst.Masked().String()
	}
	id := fmt.Sprintf("%s@%d/%s", rt.Dst.Masked(), rt.IfIndex, addrString(rt.Gateway))
	if k.keyedByMetric() {
		id += fmt.Sprintf("/%d", rt.Metric)
	}
	return id
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
//
// On Linux the key is destination and metric, and the route goes where its
// interface and next hop say: the kernel takes them literally, so both are
// compared as well.
func (k RouteKeying) same(a, b osnet.Route) bool {
	if a.Blackhole != b.Blackhole {
		return false
	}
	if k.keyedByMetric() {
		return k.key(a) == k.key(b) && a.IfIndex == b.IfIndex && a.Gateway.WithZone("") == b.Gateway.WithZone("")
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
// is journaled. So it is on Linux, where OpenVPN is a child process, and a daemon
// that is killed does not necessarily take it and its tun device along.
func (k RouteKeying) journaled(rt osnet.Route) bool {
	return k.byInterface() || rt.Gateway.IsValid()
}

// errOtherTable: a journal record was written for a table keyed another way (a
// macOS record has no interface index, a Windows one no metric), so it does not
// say which route it was.
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
	if rec.IfIndex == 0 || k.keyedByMetric() && rec.Metric == 0 {
		return routeKey{}, errOtherTable
	}
	if k.keyedByMetric() {
		key.metric = rec.Metric
		return key, nil
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
//
// On Linux the key is only destination and metric, so the route must also leave
// through the interface and next hop that were recorded. An interface index
// that the kernel handed to another interface since is told by the name.
func (k RouteKeying) recognizes(rec record, actual osnet.Route) bool {
	if k.keyedByMetric() && !(actual.IfIndex == rec.IfIndex && addrString(actual.Gateway) == rec.Gateway &&
		(rec.Iface == "" || actual.Iface == "" || actual.Iface == rec.Iface)) {
		return false
	}
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
	if k.byInterface() {
		rec.IfIndex = rt.IfIndex
	}
	if k.keyedByMetric() {
		rec.Metric = rt.Metric
	}
}

// tunnelRoute is the route that sends dst into the tunnel in. On Windows it has
// the tunnel's own gateway as its next hop when the tunnel has one that fits dst:
// an Ethernet-like adapter (tap-windows6) answers only for that address, so a
// route bound to the adapter without a next hop would send traffic nowhere.
// macOS and Linux routes are bound to the interface and never name a next hop.
// On Linux that is also what works whatever the tunnel announced: a route via a
// gateway needs it to be on the tunnel's subnet and in dst's family, an on-link
// route on a point-to-point device needs nothing.
func (k RouteKeying) tunnelRoute(dst netip.Prefix, in tunnel.Intent, ns osnet.NetState) osnet.Route {
	rt := osnet.Route{Dst: dst, Iface: in.Iface, Static: true}
	if k.byInterface() {
		rt.IfIndex, rt.Metric = interfaceIndex(ns, in.Iface), tunnelMetric
	}
	if k.namesTunnelGateway() {
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
// none (Wintun, utun) takes on-link routes in both families, and so does every
// tunnel on Linux, whatever it announced (see tunnelRoute). The halves of a
// default route are left alone: a full tunnel installs them in both families so
// that nothing leaks around a tunnel that carries one, and a route that leads
// nowhere is what that needs.
func (k RouteKeying) needsNextHop(in tunnel.Intent, dst netip.Prefix) bool {
	return k.namesTunnelGateway() && !isDefault(dst) && !tunnelGateway(in, dst).IsValid() &&
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
		rt.IfIndex, rt.Metric = interfaceIndex(ns, via.Iface), bypassMetric
	}
	return rt
}

// unplaceable says why rt cannot be added yet, or "" when it can. A Windows or
// Linux route names its interface by index, and an interface that is not in the
// network state (it was just removed, or not seen yet) has none.
func (k RouteKeying) unplaceable(rt osnet.Route) string {
	if k.byInterface() && rt.IfIndex == 0 {
		return "interface " + rt.Iface + " not found"
	}
	return ""
}

// retriesUnplaced says whether a route that waits for its interface is tried
// again on a timer. Where the monitor reports an interface appearing (Windows),
// the event brings the next pass. The Linux monitor reports nothing about the
// devices of tunnels, which their engines make after the Reconciler was told.
func (k RouteKeying) retriesUnplaced() bool { return k == KeyLinux }

// syncsPendingOnly says whether the journal syncs only the records written
// before a change. Routes of the Linux table do not outlive a reboot, so a lost
// record of what followed a change costs nothing, and five thousand routes would
// otherwise cost five thousand syncs.
func (k RouteKeying) syncsPendingOnly() bool { return k == KeyLinux }

// interfaceIndex is the index of the interface called name, zero when the
// network state does not list it.
func interfaceIndex(ns osnet.NetState, name string) uint32 {
	if ifc, ok := findInterface(ns, name); ok {
		return uint32(ifc.Index)
	}
	return 0
}
