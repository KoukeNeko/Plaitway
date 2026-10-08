package windows

import (
	"cmp"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"syscall"
	"unsafe"

	win "golang.org/x/sys/windows"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/winiface"
	"github.com/KoukeNeko/Plaitway/internal/winiface/inet"
)

const (
	// A route lives as long as the stack does: infinite lifetimes, in the active
	// store only, so that nothing survives a reboot.
	infiniteLifetime = 0xFFFFFFFF
)

var (
	iphlpapi = win.NewLazySystemDLL("iphlpapi.dll")

	procInitializeIpForwardEntry = iphlpapi.NewProc("InitializeIpForwardEntry")
	procCreateIpForwardEntry2    = iphlpapi.NewProc("CreateIpForwardEntry2")
	procDeleteIpForwardEntry2    = iphlpapi.NewProc("DeleteIpForwardEntry2")
)

// routeTable reads the routing table with GetIpForwardTable2 and changes it
// with CreateIpForwardEntry2 and DeleteIpForwardEntry2.
type routeTable struct {
	// readAdapters lists the interfaces with their type; listAdapters adds
	// addresses and metrics, which only finding a gateway's interface needs.
	readAdapters func() ([]adapter, error)
	listAdapters func() ([]adapter, error)
}

// NewRouteTable returns the routing table of this PC. Writing it needs an
// elevated process or the LocalSystem account.
func NewRouteTable() osnet.RouteTable {
	return &routeTable{readAdapters: readAdapters, listAdapters: listAdapters}
}

func (t *routeTable) Dump() ([]osnet.Route, error) {
	var table *win.MibIpForwardTable2
	if err := win.GetIpForwardTable2(win.AF_UNSPEC, &table); err != nil {
		return nil, fmt.Errorf("read routing table: %w", err)
	}
	defer win.FreeMibTable(unsafe.Pointer(table))
	// The adapters are read after the routes: an interface that appeared in
	// between is named, one that vanished takes its routes with it.
	adapters, err := t.readAdapters()
	if err != nil {
		return nil, err
	}
	byLUID := make(map[uint64]adapter, len(adapters))
	for _, a := range adapters {
		byLUID[a.LUID] = a
	}
	rows := unsafe.Slice(&table.Table[0], table.NumEntries)
	routes := make([]osnet.Route, 0, len(rows))
	for i := range rows {
		if r, ok := routeFromRow(&rows[i], byLUID); ok {
			routes = append(routes, r)
		}
	}
	slices.SortFunc(routes, compareRoutes)
	return routes, nil
}

// compareRoutes orders the dump by family, destination and interface, so that
// two reads of an unchanged table agree.
func compareRoutes(a, b osnet.Route) int {
	return cmp.Or(
		cmp.Compare(a.Dst.Addr().BitLen(), b.Dst.Addr().BitLen()),
		a.Dst.Addr().Compare(b.Dst.Addr()),
		cmp.Compare(a.Dst.Bits(), b.Dst.Bits()),
		cmp.Compare(a.IfIndex, b.IfIndex),
		a.Gateway.Compare(b.Gateway),
	)
}

// routeFromRow converts a row of the forwarding table. ok is false for a row
// osnet.Route cannot express: another address family, or a prefix length that
// does not fit the family.
func routeFromRow(row *win.MibIpForwardRow2, byLUID map[uint64]adapter) (r osnet.Route, ok bool) {
	dst := inet.Addr(&row.DestinationPrefix.Prefix)
	prefix := netip.PrefixFrom(dst, int(row.DestinationPrefix.PrefixLength))
	if !dst.IsValid() || !prefix.IsValid() {
		return osnet.Route{}, false
	}
	iface := byLUID[row.InterfaceLuid]
	r = osnet.Route{
		Dst:     prefix.Masked(),
		Iface:   iface.Name,
		IfIndex: row.InterfaceIndex,
		Metric:  row.Metric,
		Static:  isStaticProtocol(row.Protocol),
		Flags:   routeFlags(row.Protocol, row.Origin),
	}
	// The next hop of a route that is bound to its interface is the unspecified
	// address. A loopback address as next hop on the loopback pseudo-interface is
	// how a route is made to discard its traffic: Windows has no blackhole type.
	nextHop := inet.Addr(&row.NextHop)
	switch {
	case isLoopback(iface) && nextHop.IsLoopback():
		r.Blackhole = true
	case nextHop.IsValid() && !nextHop.IsUnspecified():
		r.Gateway = nextHop
	}
	return r, true
}

func (t *routeTable) Add(r osnet.Route) error {
	if !r.Dst.IsValid() {
		return fmt.Errorf("add route: route has no destination")
	}
	target, err := t.resolveTarget(r, true)
	if err != nil {
		return fmt.Errorf("add route %s: %w", r.Dst, err)
	}
	row := forwardRow(r, target)
	return routeError(opAdd, r.Dst, callRoute(procCreateIpForwardEntry2, &row))
}

func (t *routeTable) Delete(r osnet.Route) error {
	if !r.Dst.IsValid() {
		return fmt.Errorf("delete route: route has no destination")
	}
	if r.IfIndex == 0 && r.Iface == "" && !r.Blackhole {
		// Windows needs the interface to name a route. Without one, the table
		// tells which route is meant.
		table, err := t.Dump()
		if err != nil {
			return fmt.Errorf("delete route %s: %w", r.Dst, err)
		}
		match, err := matchDelete(table, r)
		if err != nil {
			return routeError(opDelete, r.Dst, err)
		}
		r = match
	}
	target, err := t.resolveTarget(r, false)
	if errors.Is(err, osnet.ErrUnreachable) {
		return routeError(opDelete, r.Dst, osnet.ErrNotFound) // its interface is gone, and the route with it
	}
	if err != nil {
		return fmt.Errorf("delete route %s: %w", r.Dst, err)
	}
	row := forwardRow(r, target)
	return routeError(opDelete, r.Dst, callRoute(procDeleteIpForwardEntry2, &row))
}

// target is where a route leaves: an interface and the next hop on it.
type target struct {
	LUID    uint64
	Index   uint32
	Gateway netip.Addr // the zero Addr for a route bound to its interface
}

// resolveTarget finds the interface of r and its next hop. The interface
// comes from, in this order: the blackhole's loopback pseudo-interface, the
// index, the name (or the zone of a link-local gateway), and, when the route is
// to be added and has a gateway, the connected subnet the gateway is in.
func (t *routeTable) resolveTarget(r osnet.Route, mayFindByGateway bool) (target, error) {
	gateway := r.Gateway.WithZone("")
	if gateway.IsValid() && gateway.Is4() != r.Dst.Addr().Is4() {
		return target{}, fmt.Errorf("gateway %s is not in the family of %s", gateway, r.Dst)
	}
	switch {
	case r.Blackhole:
		return t.loopbackTarget(r.Dst.Addr())
	case r.IfIndex != 0:
		return target{Index: r.IfIndex, Gateway: gateway}, nil
	case r.Iface != "" || r.Gateway.Zone() != "":
		name := cmp.Or(r.Iface, r.Gateway.Zone())
		link, err := winiface.FindByName(name)
		if errors.Is(err, winiface.ErrNotFound) {
			return target{}, fmt.Errorf("interface %q: %w", name, osnet.ErrUnreachable)
		}
		if err != nil {
			return target{}, err
		}
		return target{LUID: link.LUID, Index: link.Index, Gateway: gateway}, nil
	case gateway.IsValid() && mayFindByGateway:
		adapters, err := t.listAdapters()
		if err != nil {
			return target{}, err
		}
		a, err := interfaceForGateway(adapters, gateway)
		if err != nil {
			return target{}, err
		}
		return target{LUID: a.LUID, Index: a.Index, Gateway: gateway}, nil
	}
	return target{}, errors.New("a route needs an interface or a gateway")
}

// loopbackTarget is the loopback pseudo-interface with the loopback address of
// dst's family as next hop.
func (t *routeTable) loopbackTarget(dst netip.Addr) (target, error) {
	adapters, err := t.readAdapters()
	if err != nil {
		return target{}, err
	}
	i := slices.IndexFunc(adapters, isLoopback)
	if i < 0 {
		return target{}, errors.New("this PC has no loopback pseudo-interface")
	}
	nextHop := netip.AddrFrom4([4]byte{127, 0, 0, 1})
	if dst.Is6() {
		nextHop = netip.IPv6Loopback()
	}
	return target{LUID: adapters[i].LUID, Index: adapters[i].Index, Gateway: nextHop}, nil
}

// forwardRow builds the row that names (and for an addition describes) a route.
// The row starts from the system's defaults for a new route: protocol
// NETMGMT, which is what makes a route Static, manual origin, infinite
// lifetimes.
func forwardRow(r osnet.Route, at target) win.MibIpForwardRow2 {
	var row win.MibIpForwardRow2
	procInitializeIpForwardEntry.Call(uintptr(unsafe.Pointer(&row)))
	dst := r.Dst.Masked()
	row.InterfaceLuid = at.LUID
	row.InterfaceIndex = at.Index
	inet.Set(&row.DestinationPrefix.Prefix, dst.Addr())
	row.DestinationPrefix.PrefixLength = uint8(dst.Bits())
	nextHop := at.Gateway
	if !nextHop.IsValid() {
		nextHop = unspecifiedOf(dst.Addr())
	}
	inet.Set(&row.NextHop, nextHop)
	row.Metric = r.Metric
	row.Protocol = protocolNetMgmt
	row.Origin = originManual
	row.ValidLifetime = infiniteLifetime
	row.PreferredLifetime = infiniteLifetime
	return row
}

func unspecifiedOf(like netip.Addr) netip.Addr {
	if like.Is6() {
		return netip.IPv6Unspecified()
	}
	return netip.IPv4Unspecified()
}

// callRoute calls CreateIpForwardEntry2 or DeleteIpForwardEntry2, which return
// a Win32 error code.
func callRoute(proc *win.LazyProc, row *win.MibIpForwardRow2) error {
	status, _, _ := proc.Call(uintptr(unsafe.Pointer(row)))
	if status != 0 {
		return syscall.Errno(status)
	}
	return nil
}
