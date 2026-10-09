package linux

import (
	"cmp"
	"net/netip"
	"slices"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// buildNetState derives what the Reconciler needs from the interfaces and the
// routing table. Epoch is left to epochTracker.
func buildNetState(links []link, routes []osnet.Route) osnet.NetState {
	ifaces := make([]osnet.Interface, len(links))
	for i, l := range links {
		ifaces[i] = l.Interface
	}
	return osnet.NetState{
		Interfaces: ifaces,
		DefaultV4:  pickDefault(routes, links, false),
		DefaultV6:  pickDefault(routes, links, true),
		Connected:  connectedSubnets(links),
	}
}

// pickDefault returns the best default route of one family that leaves through
// an uplink that is up, or nil: the one of the lowest metric, and among equals
// the one of the lowest interface index, so the pick does not flap. A default
// route without gateway is on-link, as over a PPP or a WWAN link; the Nexthop
// then has the zero Addr as its gateway.
func pickDefault(routes []osnet.Route, links []link, v6 bool) *osnet.Nexthop {
	byIndex := make(map[uint32]link, len(links))
	for _, l := range links {
		byIndex[uint32(l.Index)] = l
	}
	var best *osnet.Route
	for i := range routes {
		r := &routes[i]
		if !r.Dst.IsValid() || r.Dst.Bits() != 0 || r.Dst.Addr().Is6() != v6 || r.Blackhole {
			continue
		}
		if l, ok := byIndex[r.IfIndex]; !ok || !l.Uplink || !l.Up {
			continue
		}
		if best == nil || compareDefaults(*r, *best) < 0 {
			best = r
		}
	}
	if best == nil {
		return nil
	}
	return &osnet.Nexthop{Gateway: best.Gateway, Iface: byIndex[best.IfIndex].Name}
}

func compareDefaults(a, b osnet.Route) int {
	return cmp.Or(cmp.Compare(a.Metric, b.Metric), cmp.Compare(a.IfIndex, b.IfIndex), a.Gateway.Compare(b.Gateway))
}

// connectedSubnets lists the on-link subnets of the uplinks that are up. This
// includes the subnets of docker0 and of the bridges of virtual machines: they
// are as local as any other subnet. Link-local subnets are left out: nothing is
// routed through them.
func connectedSubnets(links []link) []netip.Prefix {
	var subnets []netip.Prefix
	for _, l := range links {
		if !l.Up || !l.Uplink {
			continue
		}
		for _, addr := range l.Addrs {
			if !addr.Addr().IsLinkLocalUnicast() {
				subnets = append(subnets, addr.Masked())
			}
		}
	}
	slices.SortFunc(subnets, comparePrefix)
	return slices.Compact(subnets)
}

// epochTracker numbers the states a monitor has seen. The epoch changes only
// when something the Reconciler reacts to changed: the uplinks with their
// addresses, the default routes, and the connected subnets. Tunnels come and go
// on their own and are left out.
type epochTracker struct {
	last  relevantState
	epoch uint64
}

// relevantState is the part of a NetState that moves the epoch.
type relevantState struct {
	uplinks              []osnet.Interface
	defaultV4, defaultV6 *osnet.Nexthop
	connected            []netip.Prefix
}

func (t *epochTracker) stamp(links []link, s osnet.NetState) osnet.NetState {
	now := relevantState{defaultV4: s.DefaultV4, defaultV6: s.DefaultV6, connected: s.Connected}
	for _, l := range links {
		if l.Uplink {
			now.uplinks = append(now.uplinks, l.Interface)
		}
	}
	if t.epoch == 0 || !now.equal(t.last) {
		t.epoch++
		t.last = now
	}
	s.Epoch = t.epoch
	return s
}

func (a relevantState) equal(b relevantState) bool {
	return slices.EqualFunc(a.uplinks, b.uplinks, sameInterface) &&
		sameNexthop(a.defaultV4, b.defaultV4) &&
		sameNexthop(a.defaultV6, b.defaultV6) &&
		slices.Equal(a.connected, b.connected)
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
