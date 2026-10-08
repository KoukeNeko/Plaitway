package windows

import (
	"cmp"
	"math"
	"net/netip"
	"slices"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// interfacesOf converts the adapters to what the Reconciler sees.
func interfacesOf(adapters []adapter) []osnet.Interface {
	ifaces := make([]osnet.Interface, 0, len(adapters))
	for _, a := range adapters {
		addrs := slices.Clone(a.Addrs)
		slices.SortFunc(addrs, comparePrefix)
		ifaces = append(ifaces, osnet.Interface{
			Name:   a.Name,
			Index:  int(a.Index),
			Up:     a.Up,
			Tunnel: isTunnel(a),
			Addrs:  addrs,
			Metric: primaryInterfaceMetric(a),
		})
	}
	slices.SortFunc(ifaces, func(a, b osnet.Interface) int { return cmp.Compare(a.Index, b.Index) })
	return ifaces
}

// buildNetState derives what the Reconciler needs from the adapters and the
// routing table. Epoch is left to epochTracker and the kind of the defaults to
// the monitor.
func buildNetState(adapters []adapter, routes []osnet.Route) osnet.NetState {
	return osnet.NetState{
		Interfaces: interfacesOf(adapters),
		DefaultV4:  pickDefault(routes, adapters, false),
		DefaultV6:  pickDefault(routes, adapters, true),
		Connected:  connectedSubnets(adapters),
	}
}

// pickDefault returns the best default route of one family that leaves through
// an adapter that is up and physical, or nil. Windows keeps every default route
// in the table, VPNs' included; the one it uses is the one of the lowest
// effective metric, which is the route metric plus the interface metric. Among
// equals the lowest interface index wins, so the pick does not flap.
func pickDefault(routes []osnet.Route, adapters []adapter, v6 bool) *osnet.Nexthop {
	byIndex := make(map[uint32]adapter, len(adapters))
	for _, a := range adapters {
		byIndex[a.Index] = a
	}
	type candidate struct {
		route  osnet.Route
		metric uint64
		index  uint32
	}
	var candidates []candidate
	for _, r := range routes {
		if !r.Dst.IsValid() || r.Dst.Bits() != 0 || r.Dst.Addr().Is6() != v6 || r.Blackhole {
			continue
		}
		a, ok := byIndex[r.IfIndex]
		if !ok || !a.Up || !isPhysical(a) {
			continue
		}
		candidates = append(candidates, candidate{r, effectiveMetric(r, a, v6), a.Index})
	}
	if len(candidates) == 0 {
		return nil
	}
	best := slices.MinFunc(candidates, func(a, b candidate) int {
		return cmp.Or(cmp.Compare(a.metric, b.metric), cmp.Compare(a.index, b.index))
	})
	chosen := byIndex[best.index]
	return &osnet.Nexthop{Gateway: best.route.Gateway, Iface: chosen.Name, Kind: linkKind(chosen)}
}

// primaryInterfaceMetric is the metric that Windows adds to the metric of every
// route through the adapter, which the Reconciler adds to the route metric when
// it ranks routes. It is the IPv4 one, as an adapter that is in use has an IPv4
// stack; an adapter that has only IPv6 reports that one. The metric is the
// effective one: the system fills the field of an adapter with an automatic
// metric with the value it computed from the link speed. Zero means that the
// adapter is down (not read) or has no IP stack.
func primaryInterfaceMetric(a adapter) uint32 {
	if metric := interfaceMetric(a, false); metric != 0 {
		return metric
	}
	return interfaceMetric(a, true)
}

// effectiveMetric is what Windows compares between routes of equal prefix
// length. The sum does not wrap: a route metric can be near the top of uint32.
func effectiveMetric(r osnet.Route, a adapter, v6 bool) uint64 {
	return min(uint64(r.Metric)+uint64(interfaceMetric(a, v6)), math.MaxUint32)
}

// connectedSubnets lists the on-link subnets of the local networks (see
// isLocalNetwork) that are up, virtual switches included. Link-local subnets
// are left out: nothing is routed through them. So are
// IPv6 host prefixes: Windows lists its temporary (privacy) addresses with a
// prefix length of 128, and they come and go, but they open no subnet.
func connectedSubnets(adapters []adapter) []netip.Prefix {
	var subnets []netip.Prefix
	for _, a := range adapters {
		if !a.Up || !isLocalNetwork(a) {
			continue
		}
		for _, addr := range a.Addrs {
			if !addr.Addr().IsLinkLocalUnicast() && !(addr.Addr().Is6() && addr.IsSingleIP()) {
				subnets = append(subnets, addr.Masked())
			}
		}
	}
	slices.SortFunc(subnets, comparePrefix)
	return slices.Compact(subnets)
}

func comparePrefix(a, b netip.Prefix) int {
	return cmp.Or(a.Addr().Compare(b.Addr()), cmp.Compare(a.Bits(), b.Bits()))
}

// epochTracker numbers the states a monitor has seen. The epoch changes only
// when something the Reconciler reacts to changed: the interfaces that are not
// tunnels, with their addresses, the default routes, and the connected subnets.
// Tunnel interfaces come and go on their own and are left out. So is the
// interface metric: Windows moves an automatic metric with the speed of the
// link, which would start a pass for nothing, and what a metric changes in
// the choice of the default shows in the default.
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
	return slices.EqualFunc(nonTunnelInterfaces(a), nonTunnelInterfaces(b), sameInterface) &&
		sameNexthop(a.DefaultV4, b.DefaultV4) &&
		sameNexthop(a.DefaultV6, b.DefaultV6) &&
		slices.Equal(a.Connected, b.Connected)
}

func nonTunnelInterfaces(s osnet.NetState) []osnet.Interface {
	return slices.DeleteFunc(slices.Clone(s.Interfaces), func(i osnet.Interface) bool { return i.Tunnel })
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
