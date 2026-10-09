// Package fake holds in-memory stand-ins for the osnet adapters, for tests that
// cannot run as root. The route table follows macOS semantics closely enough
// that the bugs the Reconciler exists for (stale gateway routes, EEXIST, no
// metrics) can happen in it, and can be switched to the semantics of the
// Windows and the Linux routing table (WindowsKeying, LinuxKeying).
package fake

import (
	"cmp"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sync"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// Kernel route flags the fake sets: the subset of macOS's RTF_* values that
// Reconciler code can observe.
const (
	flagUp        = 0x1
	flagGateway   = 0x2
	flagHost      = 0x4
	flagStatic    = 0x800
	flagBlackhole = 0x1000
	flagIfscope   = 0x1000000
)

// OpKind names what a recorded Op did, and which calls a Fault applies to.
type OpKind string

const (
	OpAdd    OpKind = "add"
	OpDelete OpKind = "delete"
	OpMark   OpKind = "mark"
	// OpDump is only used by Fault; Dump calls are not recorded.
	OpDump OpKind = "dump"
)

// Op is one recorded call on the table, in the order the table saw them.
type Op struct {
	Seq   int
	Kind  OpKind
	Route osnet.Route
	// Removed is the route a successful delete took out of the table. macOS
	// deletes by destination alone, so it can differ from Route.
	Removed osnet.Route
	Err     error
	// Label is set for OpMark.
	Label string
}

// Fault makes calls fail with Err instead of running.
type Fault struct {
	Op OpKind
	// Dst limits the fault to one destination; the zero Prefix matches all.
	Dst netip.Prefix
	Err error
	// Times is how many matching calls fail; zero means one.
	Times int
}

type routeKey struct {
	dst netip.Prefix
	// scope is the interface name of a scoped route, empty otherwise.
	scope string
	// iface and gateway complete the key in Windows mode (see WindowsKeying), and
	// in Linux mode together with the metric (see LinuxKeying).
	iface   string
	gateway netip.Addr
	metric  uint32
}

func (t *RouteTable) keyOf(r osnet.Route) routeKey {
	k := routeKey{dst: r.Dst.Masked()}
	switch {
	case t.linux:
		k.iface, k.gateway, k.metric = r.Iface, r.Gateway.WithZone(""), r.Metric
	case t.windows:
		k.iface, k.gateway = r.Iface, r.Gateway.WithZone("")
	case r.Scoped:
		k.scope = r.Iface
	}
	return k
}

// RouteTable is an osnet.RouteTable with macOS semantics: a route's key is its
// destination plus scope, adding an existing key fails with ErrExists, deleting
// a missing one with ErrNotFound, and there are no metrics, so the longest
// prefix wins. WindowsKeying and LinuxKeying switch it to the other two tables.
//
// It also models the interfaces the routes hang off, because that is where
// stale routes come from: routes bound to an interface vanish with it, while a
// gateway route survives its interface changing address.
type RouteTable struct {
	mu     sync.Mutex
	ifaces map[string]*osnet.Interface
	routes map[routeKey]osnet.Route
	ops    []Op
	faults []*Fault
	// pickIface, when set, is the interface gateway routes leave through whenever
	// the gateway is reachable on it (see KernelPicksInterface).
	pickIface string
	// windows selects the Windows key (see WindowsKeying).
	windows bool
	// linux selects the Linux key (see LinuxKeying).
	linux bool
	// lastIndex is the last interface index handed out in Linux mode.
	lastIndex int
	// ifaceMetric is the interface metric Windows adds to a route metric.
	ifaceMetric map[string]uint32
}

func NewRouteTable() *RouteTable {
	return &RouteTable{
		ifaces:      make(map[string]*osnet.Interface),
		routes:      make(map[routeKey]osnet.Route),
		ifaceMetric: make(map[string]uint32),
	}
}

// WindowsLoopback is the interface the table puts blackhole routes on in Windows
// mode: Windows has no blackhole route type, and the adapter uses a route to the
// loopback address on the loopback pseudo-interface instead.
const WindowsLoopback = "Loopback Pseudo-Interface 1"

// windowsLoopbackIndex is the index of WindowsLoopback on every Windows.
const windowsLoopbackIndex = 1

// WindowsKeying switches the table from macOS to Windows semantics, which the
// Reconciler has to handle as well. It must be called on an empty table.
//
//   - A route is keyed by destination, interface and next hop, so several routes
//     can share a prefix when they differ in either; there is no scope.
//   - The metric is an attribute, not part of the key: adding a route whose key
//     exists with another metric is ErrExists.
//   - Delete names the key. Without an interface (name or index) it deletes the
//     route to that destination (and gateway, when given) if exactly one
//     matches, and fails when several do.
//   - Among routes of equal prefix length the lowest effective metric wins: the
//     route metric plus the interface metric (SetInterfaceMetric).
//   - Routes carry IfIndex, taken from the interface; blackholes leave through
//     WindowsLoopback.
func (t *RouteTable) WindowsKeying() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.routes) > 0 || t.linux {
		panic("fake route table: WindowsKeying on a table that has routes or Linux keying")
	}
	t.windows = true
}

// linuxConnectedMetricV6 is the metric of the route the kernel adds for the
// prefix of an IPv6 address. IPv4 ones have the metric 0.
const linuxConnectedMetricV6 = 256

// LinuxKeying switches the table from macOS to Linux semantics, which the
// Reconciler has to handle as well. It must be called on an empty table.
//
//   - The kernel tells routes apart by destination and metric alone: adding a
//     route whose destination and metric exist is ErrExists, whatever interface
//     and next hop it has. Routes with other metrics lie side by side, each with
//     an interface and a next hop of its own. Inject takes the place of the routes
//     with its destination and metric, as ip route replace does; Append puts a
//     second route beside them, as ip route append does.
//   - Delete names the route by destination, metric, next hop and interface (by
//     index, or by name when the index is not known). Without an interface it
//     deletes the route to that destination, metric and next hop if exactly one
//     matches, and fails when several do.
//   - Among routes of equal prefix length the lowest metric wins; there is no
//     interface metric. A metric is taken as it is: the defaults the kernel
//     gives a route added without one (0 for IPv4, 1024 for IPv6) are not
//     modelled.
//   - Routes carry IfIndex. An interface added without an index gets the next
//     free one, and never an index it had before. A route that names an index is
//     added to that interface, whatever Iface says, as with netlink.
//   - The kernel never picks another interface than the one the route names: the
//     next hop must be on a connected subnet of it (any IPv6 link-local address
//     is), and an interface that does not exist or is down is ErrUnreachable.
//     Tunnel interfaces are point-to-point devices and take on-link routes of
//     both address families.
//   - Dump reports a link-local IPv6 next hop with the interface as its zone.
//   - The connected routes of an interface have the metric 0 (IPv4) and 256
//     (IPv6).
func (t *RouteTable) LinuxKeying() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.routes) > 0 || t.windows {
		panic("fake route table: LinuxKeying on a table that has routes or Windows keying")
	}
	t.linux = true
}

// metricAware says whether routes of one prefix are told apart by their metric
// and the lowest wins.
func (t *RouteTable) metricAware() bool { return t.windows || t.linux }

// SetInterfaceMetric sets the interface metric that is added to the metric of
// every route through the interface when routes are compared (Windows mode).
// Interfaces reports it as osnet.Interface.Metric, which is how the Reconciler
// learns it; the Host hands it on with its next Sync.
func (t *RouteTable) SetInterfaceMetric(name string, metric uint32) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ifaceMetric[name] = metric
}

// EffectiveMetric is the metric the table compares: the route metric plus the
// metric of its interface.
func (t *RouteTable) EffectiveMetric(r osnet.Route) uint32 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.effectiveMetricLocked(r)
}

func (t *RouteTable) effectiveMetricLocked(r osnet.Route) uint32 {
	return r.Metric + t.ifaceMetric[r.Iface]
}

// Dump returns every route, sorted by family, destination, scope, interface,
// gateway and metric.
func (t *RouteTable) Dump() ([]osnet.Route, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.faultLocked(OpDump, netip.Prefix{}); err != nil {
		return nil, err
	}
	out := make([]osnet.Route, 0, len(t.routes))
	for _, r := range t.routes {
		out = append(out, r)
	}
	slices.SortFunc(out, compareRoutes)
	return out, nil
}

// Add resolves the interface the way the kernel does: a gateway must lie on a
// connected subnet of an up interface, an interface-bound route needs an up
// interface; otherwise the error is ErrUnreachable.
func (t *RouteTable) Add(r osnet.Route) (err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	asked := r
	defer func() { t.recordLocked(Op{Kind: OpAdd, Route: asked, Err: err}) }()
	if err := t.faultLocked(OpAdd, r.Dst); err != nil {
		return err
	}
	if !r.Dst.IsValid() {
		return fmt.Errorf("fake route table: invalid destination %v", r.Dst)
	}
	r.Dst = r.Dst.Masked()
	if t.linux {
		return t.addLinuxLocked(r)
	}
	if err := t.resolveLocked(&r); err != nil {
		return err
	}
	if ifc, ok := t.ifaces[r.Iface]; ok && t.windows && r.IfIndex == 0 {
		r.IfIndex = uint32(ifc.Index)
	}
	key := t.keyOf(r)
	if _, ok := t.routes[key]; ok {
		return osnet.ErrExists
	}
	r.Flags = flagsFor(r)
	t.routes[key] = r
	return nil
}

// Delete matches on destination and scope only, not on gateway or interface,
// like RTM_DELETE on macOS. In Windows and Linux mode see deleteKeyLocked.
func (t *RouteTable) Delete(r osnet.Route) (err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var removed osnet.Route
	defer func() { t.recordLocked(Op{Kind: OpDelete, Route: r, Removed: removed, Err: err}) }()
	if err := t.faultLocked(OpDelete, r.Dst); err != nil {
		return err
	}
	key, err := t.deleteKeyLocked(r)
	if err != nil {
		return err
	}
	old, ok := t.routes[key]
	if !ok {
		return osnet.ErrNotFound
	}
	removed = old
	delete(t.routes, key)
	return nil
}

// deleteKeyLocked is the key Delete removes: the route's own key on macOS, and
// on Windows and Linux the one route that the interface (name or index),
// destination and gateway of r single out, on Linux also its metric.
func (t *RouteTable) deleteKeyLocked(r osnet.Route) (routeKey, error) {
	if !t.metricAware() {
		return t.keyOf(r), nil
	}
	switch {
	case t.linux && r.IfIndex != 0:
		if name := t.nameOfIndexLocked(r.IfIndex); name != "" {
			r.Iface = name
		}
	case r.Iface == "" && r.IfIndex != 0:
		r.Iface = t.nameOfIndexLocked(r.IfIndex)
	}
	if r.Iface != "" {
		return t.keyOf(r), nil
	}
	var matches []routeKey
	for key := range t.routes {
		if key.dst == r.Dst.Masked() && (!r.Gateway.IsValid() || key.gateway == r.Gateway.WithZone("")) && (!t.linux || key.metric == r.Metric) {
			matches = append(matches, key)
		}
	}
	switch len(matches) {
	case 0:
		return t.keyOf(r), nil // not in the table: Delete reports ErrNotFound
	case 1:
		return matches[0], nil
	}
	return routeKey{}, fmt.Errorf("fake route table: %d routes to %v match, name the interface", len(matches), r.Dst)
}

func (t *RouteTable) nameOfIndexLocked(index uint32) string {
	if t.windows && index == windowsLoopbackIndex {
		return WindowsLoopback
	}
	for name, ifc := range t.ifaces {
		if uint32(ifc.Index) == index {
			return name
		}
	}
	return ""
}

func (t *RouteTable) resolveLocked(r *osnet.Route) error {
	switch {
	case r.Blackhole && t.windows:
		r.Iface, r.IfIndex = WindowsLoopback, windowsLoopbackIndex
		return nil
	case r.Blackhole:
		r.Iface = "lo0"
	case r.Gateway.IsValid():
		if ifc, ok := t.ifaces[t.pickIface]; ok && ifc.Up && OnLink(ifc.Addrs, r.Gateway) {
			r.Iface = ifc.Name
			return nil
		}
		ifc := t.ifaceForGatewayLocked(r.Gateway, r.Iface)
		if ifc == nil {
			return osnet.ErrUnreachable
		}
		r.Iface = ifc.Name
	default:
		ifc, ok := t.ifaces[r.Iface]
		if !ok || !ifc.Up {
			return osnet.ErrUnreachable
		}
	}
	return nil
}

// addLinuxLocked adds a route to a Linux table. Destination and metric are what
// the kernel refuses a second time.
func (t *RouteTable) addLinuxLocked(r osnet.Route) error {
	if err := t.resolveLinuxLocked(&r); err != nil {
		return err
	}
	for key := range t.routes {
		if key.dst == r.Dst && key.metric == r.Metric {
			return osnet.ErrExists
		}
	}
	r.Flags = flagsFor(r)
	t.routes[t.keyOf(r)] = r
	return nil
}

// resolveLinuxLocked finds the interface of r the way the kernel does: the one
// the index names, or else the name, never another; the next hop has to be on
// a subnet of it. Without an interface, the next hop's own subnet decides.
func (t *RouteTable) resolveLinuxLocked(r *osnet.Route) error {
	if r.Blackhole {
		return nil
	}
	name := r.Iface
	if r.IfIndex != 0 {
		if name = t.nameOfIndexLocked(r.IfIndex); name == "" {
			return osnet.ErrUnreachable
		}
	}
	var ifc *osnet.Interface
	if r.Gateway.IsValid() {
		ifc = t.ifaceForGatewayLocked(r.Gateway, name)
	} else if i, ok := t.ifaces[name]; ok && i.Up {
		ifc = i
	}
	if ifc == nil {
		return osnet.ErrUnreachable
	}
	r.Iface, r.IfIndex = ifc.Name, uint32(ifc.Index)
	t.reportLinuxLocked(r)
	return nil
}

// reportLinuxLocked completes a route the way Dump reports it on Linux: the
// index of the interface that has the name or the name of the index, and the
// interface as the zone of a link-local IPv6 next hop.
func (t *RouteTable) reportLinuxLocked(r *osnet.Route) {
	if r.Iface == "" && r.IfIndex != 0 {
		r.Iface = t.nameOfIndexLocked(r.IfIndex)
	}
	if ifc, ok := t.ifaces[r.Iface]; ok && r.IfIndex == 0 {
		r.IfIndex = uint32(ifc.Index)
	}
	r.Gateway = r.Gateway.WithZone("")
	if r.Gateway.Is6() && r.Gateway.IsLinkLocalUnicast() && r.Iface != "" {
		r.Gateway = r.Gateway.WithZone(r.Iface)
	}
}

// ifaceForGatewayLocked finds the up interface a gateway is directly reachable
// on; name restricts the search to one interface when it is not empty.
func (t *RouteTable) ifaceForGatewayLocked(gw netip.Addr, name string) *osnet.Interface {
	names := make([]string, 0, len(t.ifaces))
	for n := range t.ifaces {
		if name == "" || n == name {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	for _, n := range names {
		ifc := t.ifaces[n]
		if ifc.Up && OnLink(ifc.Addrs, gw) {
			return ifc
		}
	}
	return nil
}

// KernelPicksInterface makes every gateway route leave through name when the
// gateway is reachable on it, whatever interface the route was added for. The
// macOS kernel does this when two interfaces (Wi-Fi and Ethernet) are on the
// gateway's network: it follows the gateway, not the interface that was asked.
func (t *RouteTable) KernelPicksInterface(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pickIface = name
}

// OnLink reports whether addr is directly reachable through addrs, the
// configured prefixes of one interface. IPv6 link-local addresses always are.
func OnLink(addrs []netip.Prefix, addr netip.Addr) bool {
	addr = addr.WithZone("")
	if addr.IsLinkLocalUnicast() && addr.Is6() {
		return true
	}
	for _, p := range addrs {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func flagsFor(r osnet.Route) uint32 {
	if r.Flags != 0 {
		return r.Flags
	}
	flags := uint32(flagUp)
	if r.Gateway.IsValid() {
		flags |= flagGateway
	}
	if r.Dst.IsSingleIP() {
		flags |= flagHost
	}
	if r.Static {
		flags |= flagStatic
	}
	if r.Blackhole {
		flags |= flagBlackhole
	}
	if r.Scoped {
		flags |= flagIfscope
	}
	return flags
}

func compareRoutes(a, b osnet.Route) int {
	return cmp.Or(
		cmp.Compare(a.Dst.Addr().BitLen(), b.Dst.Addr().BitLen()),
		a.Dst.Addr().Compare(b.Dst.Addr()),
		cmp.Compare(a.Dst.Bits(), b.Dst.Bits()),
		cmp.Compare(scopeName(a), scopeName(b)),
		cmp.Compare(a.Iface, b.Iface),
		a.Gateway.Compare(b.Gateway),
		cmp.Compare(a.Metric, b.Metric),
	)
}

func scopeName(r osnet.Route) string {
	if r.Scoped {
		return r.Iface
	}
	return ""
}

// AddInterface creates an interface, or replaces an existing one of the same
// name, together with the connected routes its addresses imply. In Linux mode
// an Index of zero is replaced by a new one.
func (t *RouteTable) AddInterface(ifc osnet.Interface) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if old, ok := t.ifaces[ifc.Name]; ok {
		t.dropConnectedLocked(old)
	}
	ifc.Addrs = slices.Clone(ifc.Addrs)
	if ifc.Metric != 0 {
		t.ifaceMetric[ifc.Name] = ifc.Metric
	}
	if t.linux {
		if ifc.Index == 0 {
			ifc.Index = t.lastIndex + 1
		}
		t.lastIndex = max(t.lastIndex, ifc.Index)
	}
	t.ifaces[ifc.Name] = &ifc
	t.addConnectedLocked(&ifc)
}

// DestroyInterface removes an interface and every route through it, as the
// kernel does when a utun is closed.
func (t *RouteTable) DestroyInterface(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.ifaces, name)
	t.dropRoutesViaLocked(name)
}

// SetUp changes the link state. Going down deletes every route through the
// interface, static ones included (in_ifadown on macOS).
func (t *RouteTable) SetUp(name string, up bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	ifc, ok := t.ifaces[name]
	if !ok || ifc.Up == up {
		return
	}
	ifc.Up = up
	if up {
		t.addConnectedLocked(ifc)
	} else {
		t.dropRoutesViaLocked(name)
	}
}

// SetAddrs replaces an interface's addresses. Only the connected routes
// change: a gateway route through the interface survives (SIOCDIFADDR on
// macOS) and keeps pointing at the old network. That is how stale routes are
// born.
func (t *RouteTable) SetAddrs(name string, addrs []netip.Prefix) {
	t.mu.Lock()
	defer t.mu.Unlock()
	ifc, ok := t.ifaces[name]
	if !ok {
		return
	}
	t.dropConnectedLocked(ifc)
	ifc.Addrs = slices.Clone(addrs)
	t.addConnectedLocked(ifc)
}

// Interfaces returns the interfaces sorted by name, each with the metric the
// table adds to the routes through it.
func (t *RouteTable) Interfaces() []osnet.Interface {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]osnet.Interface, 0, len(t.ifaces))
	for _, ifc := range t.ifaces {
		c := *ifc
		c.Addrs = slices.Clone(ifc.Addrs)
		c.Metric = t.ifaceMetric[ifc.Name]
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b osnet.Interface) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

func isConnected(r osnet.Route) bool { return !r.Static && !r.Gateway.IsValid() && !r.Scoped }

func (t *RouteTable) addConnectedLocked(ifc *osnet.Interface) {
	if !ifc.Up {
		return
	}
	for _, a := range ifc.Addrs {
		r := t.connectedRoute(ifc, a)
		if _, ok := t.routes[t.keyOf(r)]; !ok {
			t.routes[t.keyOf(r)] = r
		}
	}
}

func (t *RouteTable) dropConnectedLocked(ifc *osnet.Interface) {
	for _, a := range ifc.Addrs {
		key := t.keyOf(t.connectedRoute(ifc, a))
		if r, ok := t.routes[key]; ok && r.Iface == ifc.Name && isConnected(r) {
			delete(t.routes, key)
		}
	}
}

// connectedRoute is the route the kernel derives from the address a of ifc.
func (t *RouteTable) connectedRoute(ifc *osnet.Interface, a netip.Prefix) osnet.Route {
	r := osnet.Route{Dst: a.Masked(), Iface: ifc.Name}
	if t.metricAware() {
		r.IfIndex = uint32(ifc.Index)
	}
	if t.linux && a.Addr().Is6() {
		r.Metric = linuxConnectedMetricV6
	}
	r.Flags = flagsFor(r)
	return r
}

func (t *RouteTable) dropRoutesViaLocked(name string) {
	for key, r := range t.routes {
		if r.Iface == name {
			delete(t.routes, key)
		}
	}
}

// Inject puts a route into the table unconditionally, the way another program
// or an earlier run would have left it: no interface or gateway validation, and
// an existing route with the same key is overwritten. Nothing is recorded. In
// Linux mode the route takes the place of the routes with its destination and
// metric, whatever interface and next hop they have.
func (t *RouteTable) Inject(r osnet.Route) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r.Dst = r.Dst.Masked()
	if t.linux {
		t.reportLinuxLocked(&r)
		for key := range t.routes {
			if key.dst == r.Dst && key.metric == r.Metric {
				delete(t.routes, key)
			}
		}
	}
	r.Flags = flagsFor(r)
	t.routes[t.keyOf(r)] = r
}

// Append puts a route beside the ones that have its destination and metric
// (Linux mode), as ip route append does. The table then holds two routes that
// Delete tells apart by interface and next hop. Nothing is recorded.
func (t *RouteTable) Append(r osnet.Route) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.linux {
		panic("fake route table: Append needs Linux keying")
	}
	r.Dst = r.Dst.Masked()
	t.reportLinuxLocked(&r)
	r.Flags = flagsFor(r)
	t.routes[t.keyOf(r)] = r
}

// Remove deletes the unscoped route to dst behind the Reconciler's back, and
// reports whether there was one. Nothing is recorded. With Windows or Linux
// keying it is the route Get returns.
func (t *RouteTable) Remove(dst netip.Prefix) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	key, ok := t.bestKeyLocked(dst)
	delete(t.routes, key)
	return ok
}

// removeDefaultsVia removes every default route through the interface, behind
// the Reconciler's back and unrecorded, as Remove does. A Linux table can hold
// several default routes to one prefix, of which Remove takes only the best.
func (t *RouteTable) removeDefaultsVia(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for key, r := range t.routes {
		if r.Dst.Bits() == 0 && r.Iface == name && !r.Blackhole {
			delete(t.routes, key)
		}
	}
}

// Get returns the unscoped route to dst; with Windows or Linux keying the one
// with the lowest effective metric when there are several.
func (t *RouteTable) Get(dst netip.Prefix) (osnet.Route, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	key, ok := t.bestKeyLocked(dst)
	return t.routes[key], ok
}

// bestKeyLocked is the key of the route to dst that the kernel would use.
func (t *RouteTable) bestKeyLocked(dst netip.Prefix) (routeKey, bool) {
	dst = dst.Masked()
	if !t.metricAware() {
		key := routeKey{dst: dst}
		_, ok := t.routes[key]
		return key, ok
	}
	var best routeKey
	found := false
	for key, r := range t.routes {
		if key.dst != dst {
			continue
		}
		if !found || t.beats(r, t.routes[best]) {
			best, found = key, true
		}
	}
	return best, found
}

// beats says whether route a is preferred over b of the same prefix: the lower
// effective metric, then the interface name and the gateway, so that the pick
// does not depend on map order.
func (t *RouteTable) beats(a, b osnet.Route) bool {
	return cmp.Or(
		cmp.Compare(t.effectiveMetricLocked(a), t.effectiveMetricLocked(b)),
		cmp.Compare(a.Iface, b.Iface),
		a.Gateway.Compare(b.Gateway),
	) < 0
}

// Lookup returns the unscoped route the kernel would pick for addr: the
// longest matching prefix, and with Windows or Linux keying the lowest
// effective metric among those.
func (t *RouteTable) Lookup(addr netip.Addr) (osnet.Route, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var best osnet.Route
	found := false
	for key, r := range t.routes {
		if key.scope != "" || !r.Dst.Contains(addr) {
			continue
		}
		if !found || r.Dst.Bits() > best.Dst.Bits() || (t.metricAware() && r.Dst.Bits() == best.Dst.Bits() && t.beats(r, best)) {
			best, found = r, true
		}
	}
	return best, found
}

// InjectFault arms a Fault.
func (t *RouteTable) InjectFault(f Fault) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if f.Times <= 0 {
		f.Times = 1
	}
	t.faults = append(t.faults, &f)
}

func (t *RouteTable) faultLocked(op OpKind, dst netip.Prefix) error {
	for i, f := range t.faults {
		if f.Op != op || (f.Dst.IsValid() && f.Dst != dst.Masked()) {
			continue
		}
		f.Times--
		err := f.Err
		if f.Times == 0 {
			t.faults = slices.Delete(t.faults, i, i+1)
		}
		if err == nil {
			err = errors.New("fake route table: injected fault")
		}
		return err
	}
	return nil
}

// Ops returns the recorded calls and marks, oldest first.
func (t *RouteTable) Ops() []Op {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.ops)
}

// Mark puts a label into the operation log, so a test can assert what happened
// before and after some other event.
func (t *RouteTable) Mark(label string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.recordLocked(Op{Kind: OpMark, Label: label})
}

func (t *RouteTable) recordLocked(op Op) {
	op.Seq = len(t.ops)
	t.ops = append(t.ops, op)
}
