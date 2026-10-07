package macos

import (
	"cmp"
	"math"
	"net/netip"
	"slices"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// buildNetState derives what the Reconciler needs from the interfaces and the
// routing table. Epoch is left to epochTracker.
func buildNetState(ifaces []osnet.Interface, routes []osnet.Route) osnet.NetState {
	return osnet.NetState{
		Interfaces: ifaces,
		DefaultV4:  pickDefault(routes, ifaces, false),
		DefaultV6:  pickDefault(routes, ifaces, true),
		Connected:  connectedSubnets(ifaces),
	}
}

// pickDefault returns the best default route of one family that leaves through
// a physical interface, or nil.
//
// The unscoped default route is the one the system uses. When another VPN took
// it over, the unscoped default leads into that tunnel and the way out
// survives only as a scoped default; those are the fallback, because the
// Reconciler still needs the physical gateway to reach tunnel endpoints. Among
// equals the lowest interface index wins, so the pick does not flap.
func pickDefault(routes []osnet.Route, ifaces []osnet.Interface, v6 bool) *osnet.Nexthop {
	index := make(map[string]int, len(ifaces))
	for _, iface := range ifaces {
		index[iface.Name] = iface.Index
	}
	indexOf := func(name string) int {
		if i, ok := index[name]; ok {
			return i
		}
		return math.MaxInt
	}
	var candidates []osnet.Route
	for _, r := range routes {
		if r.Dst.IsValid() && r.Dst.Bits() == 0 && r.Dst.Addr().Is6() == v6 && !r.Blackhole && isPhysical(r.Iface) {
			candidates = append(candidates, r)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	best := slices.MinFunc(candidates, func(a, b osnet.Route) int {
		return cmp.Or(
			cmp.Compare(boolRank(a.Scoped), boolRank(b.Scoped)),
			cmp.Compare(indexOf(a.Iface), indexOf(b.Iface)),
		)
	})
	return &osnet.Nexthop{Gateway: best.Gateway, Iface: best.Iface}
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

// connectedSubnets lists the on-link subnets of the physical interfaces that
// are up. Link-local subnets are left out: nothing is routed through them.
func connectedSubnets(ifaces []osnet.Interface) []netip.Prefix {
	var subnets []netip.Prefix
	for _, iface := range ifaces {
		if !iface.Up || !isPhysical(iface.Name) {
			continue
		}
		for _, addr := range iface.Addrs {
			if !addr.Addr().IsLinkLocalUnicast() {
				subnets = append(subnets, addr.Masked())
			}
		}
	}
	slices.SortFunc(subnets, comparePrefix)
	return slices.Compact(subnets)
}

// epochTracker numbers the states a monitor has seen. The epoch changes only
// when something the Reconciler reacts to changed: the physical interfaces
// with their addresses, the default routes, and the connected subnets. Tunnel
// interfaces come and go on their own and are left out.
type epochTracker struct {
	last  osnet.NetState
	epoch uint64
}

func (t *epochTracker) stamp(s osnet.NetState) osnet.NetState {
	if t.epoch == 0 || !sameRelevantState(t.last, s) {
		t.epoch++
		t.last = s
	}
	s.Epoch = t.epoch
	return s
}

func sameRelevantState(a, b osnet.NetState) bool {
	return slices.EqualFunc(physicalInterfaces(a), physicalInterfaces(b), sameInterface) &&
		sameNexthop(a.DefaultV4, b.DefaultV4) &&
		sameNexthop(a.DefaultV6, b.DefaultV6) &&
		slices.Equal(a.Connected, b.Connected)
}

func physicalInterfaces(s osnet.NetState) []osnet.Interface {
	return slices.DeleteFunc(slices.Clone(s.Interfaces), func(i osnet.Interface) bool { return !isPhysical(i.Name) })
}

func sameInterface(a, b osnet.Interface) bool {
	return a.Name == b.Name && a.Index == b.Index && a.Up == b.Up && slices.Equal(a.Addrs, b.Addrs)
}

func sameNexthop(a, b *osnet.Nexthop) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
