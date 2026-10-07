// Package fake holds in-memory stand-ins for the osnet adapters, for tests that
// cannot run as root. The route table follows macOS semantics closely enough
// that the bugs the Reconciler exists for (stale gateway routes, EEXIST, no
// metrics) can happen in it.
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
}

func keyOf(r osnet.Route) routeKey {
	k := routeKey{dst: r.Dst.Masked()}
	if r.Scoped {
		k.scope = r.Iface
	}
	return k
}

// RouteTable is an osnet.RouteTable with macOS semantics: a route's key is its
// destination plus scope, adding an existing key fails with ErrExists, deleting
// a missing one with ErrNotFound, and there are no metrics, so the longest
// prefix wins.
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
}

func NewRouteTable() *RouteTable {
	return &RouteTable{
		ifaces: make(map[string]*osnet.Interface),
		routes: make(map[routeKey]osnet.Route),
	}
}

// Dump returns every route, sorted by family, destination and scope.
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
	if err := t.resolveLocked(&r); err != nil {
		return err
	}
	key := keyOf(r)
	if _, ok := t.routes[key]; ok {
		return osnet.ErrExists
	}
	r.Flags = flagsFor(r)
	t.routes[key] = r
	return nil
}

// Delete matches on destination and scope only, not on gateway or interface,
// like RTM_DELETE on macOS.
func (t *RouteTable) Delete(r osnet.Route) (err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var removed osnet.Route
	defer func() { t.recordLocked(Op{Kind: OpDelete, Route: r, Removed: removed, Err: err}) }()
	if err := t.faultLocked(OpDelete, r.Dst); err != nil {
		return err
	}
	key := keyOf(r)
	old, ok := t.routes[key]
	if !ok {
		return osnet.ErrNotFound
	}
	removed = old
	delete(t.routes, key)
	return nil
}

func (t *RouteTable) resolveLocked(r *osnet.Route) error {
	switch {
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
	)
}

func scopeName(r osnet.Route) string {
	if r.Scoped {
		return r.Iface
	}
	return ""
}

// AddInterface creates an interface, or replaces an existing one of the same
// name, together with the connected routes its addresses imply.
func (t *RouteTable) AddInterface(ifc osnet.Interface) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if old, ok := t.ifaces[ifc.Name]; ok {
		t.dropConnectedLocked(old)
	}
	ifc.Addrs = slices.Clone(ifc.Addrs)
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

// Interfaces returns the interfaces sorted by name.
func (t *RouteTable) Interfaces() []osnet.Interface {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]osnet.Interface, 0, len(t.ifaces))
	for _, ifc := range t.ifaces {
		c := *ifc
		c.Addrs = slices.Clone(ifc.Addrs)
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
		r := osnet.Route{Dst: a.Masked(), Iface: ifc.Name}
		r.Flags = flagsFor(r)
		if _, ok := t.routes[keyOf(r)]; !ok {
			t.routes[keyOf(r)] = r
		}
	}
}

func (t *RouteTable) dropConnectedLocked(ifc *osnet.Interface) {
	for _, a := range ifc.Addrs {
		key := routeKey{dst: a.Masked()}
		if r, ok := t.routes[key]; ok && r.Iface == ifc.Name && isConnected(r) {
			delete(t.routes, key)
		}
	}
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
// an existing route with the same key is overwritten. Nothing is recorded.
func (t *RouteTable) Inject(r osnet.Route) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r.Dst = r.Dst.Masked()
	r.Flags = flagsFor(r)
	t.routes[keyOf(r)] = r
}

// Remove deletes the unscoped route to dst behind the Reconciler's back, and
// reports whether there was one. Nothing is recorded.
func (t *RouteTable) Remove(dst netip.Prefix) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := routeKey{dst: dst.Masked()}
	_, ok := t.routes[key]
	delete(t.routes, key)
	return ok
}

// Get returns the unscoped route to dst.
func (t *RouteTable) Get(dst netip.Prefix) (osnet.Route, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := t.routes[routeKey{dst: dst.Masked()}]
	return r, ok
}

// Lookup returns the unscoped route the kernel would pick for addr: the
// longest matching prefix.
func (t *RouteTable) Lookup(addr netip.Addr) (osnet.Route, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var best osnet.Route
	found := false
	for key, r := range t.routes {
		if key.scope != "" || !r.Dst.Contains(addr) {
			continue
		}
		if !found || r.Dst.Bits() > best.Dst.Bits() {
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
