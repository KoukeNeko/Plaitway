package linux

import (
	"cmp"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"syscall"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// RouteProtocol is the rtm_protocol of every route Plaitway adds, so that "ip
// route" shows "proto 199" and a person or a later sweep can tell them from
// routes of other programs. It is in the range the kernel leaves to user space.
const RouteProtocol = 199

// errNoSuchInterface: an interface name that no interface has.
var errNoSuchInterface = errors.New("no such interface")

const (
	opAdd    = "add"
	opDelete = "delete"
)

// listed reports whether the route message is about a route Dump lists: an
// entry of the main table that sends traffic somewhere. Local, broadcast,
// multicast and anycast entries are the kernel's bookkeeping for addresses;
// cache entries are copies of other routes.
func (m routeMsg) listed() bool {
	if m.Table != rtTableMain || m.Flags&rtmFCloned != 0 || (m.Family != afInet && m.Family != afInet6) {
		return false
	}
	switch m.Type {
	case rtnUnicast, rtnBlackhole, rtnUnreachable, rtnProhibit:
		return true
	}
	return false
}

// static says whether a program added the route, as opposed to the kernel
// (from an address, protocol kernel), an ICMP redirect or a router advertisement.
func (m routeMsg) static() bool {
	return m.Protocol != rtprotKernel && m.Protocol != rtprotRedirect && m.Protocol != rtprotRA
}

func blackholeType(t uint8) bool {
	return t == rtnBlackhole || t == rtnUnreachable || t == rtnProhibit
}

// prefix is the destination of the route. The default route has no RTA_DST.
func (m routeMsg) prefix() (netip.Prefix, bool) {
	dst := m.Dst
	if !dst.IsValid() {
		if m.DstLen != 0 {
			return netip.Prefix{}, false
		}
		dst = netip.IPv6Unspecified()
		if m.Family == afInet {
			dst = netip.IPv4Unspecified()
		}
	}
	if dst.Is4() != (m.Family == afInet) {
		return netip.Prefix{}, false
	}
	prefix := netip.PrefixFrom(dst, int(m.DstLen))
	return prefix.Masked(), prefix.IsValid()
}

// routesFromMessage converts a route message from the kernel into osnet.Routes,
// one for every next hop. ifaceName returns "" for an index it does not know,
// 0 included.
//
// Static is set unless the kernel made the route itself: from an address
// (protocol kernel), a redirect or a router advertisement. Flags is the route's
// type, protocol and scope, one byte each from the top (type<<16 | protocol<<8
// | scope); it is for display and fingerprinting only. The result is empty for a
// route Dump does not list and for one osnet.Route cannot express: a next hop of
// another family than the destination (RTA_VIA), or a route that leads nowhere
// in particular, as one on a nexthop object the kernel did not spell out.
func routesFromMessage(m routeMsg, ifaceName func(index uint32) string) []osnet.Route {
	if !m.listed() {
		return nil
	}
	dst, ok := m.prefix()
	if !ok {
		return nil
	}
	hops := m.NextHops
	if len(hops) == 0 {
		hops = []nextHop{{OIf: m.OIf, Gateway: m.Gateway, Via: m.Via}}
	}
	var routes []osnet.Route
	for _, hop := range hops {
		if hop.Via || hop.Gateway.IsValid() && hop.Gateway.Is4() != dst.Addr().Is4() {
			continue
		}
		blackhole := blackholeType(m.Type)
		if !blackhole && hop.OIf == 0 && !hop.Gateway.IsValid() {
			continue
		}
		r := osnet.Route{
			Dst:       dst,
			Gateway:   hop.Gateway,
			Iface:     ifaceName(hop.OIf),
			IfIndex:   hop.OIf,
			Metric:    m.Priority,
			Blackhole: blackhole,
			Static:    m.static(),
			Flags:     uint32(m.Type)<<16 | uint32(m.Protocol)<<8 | uint32(m.Scope),
		}
		if hop.Gateway.Is6() && hop.Gateway.IsLinkLocalUnicast() && r.Iface != "" {
			r.Gateway = hop.Gateway.WithZone(r.Iface)
		}
		routes = append(routes, r)
	}
	return routes
}

// sortRoutes orders routes by family, destination, prefix length, metric and
// interface, so that equal tables dump equal.
func sortRoutes(routes []osnet.Route) {
	slices.SortFunc(routes, func(a, b osnet.Route) int {
		return cmp.Or(
			a.Dst.Addr().Compare(b.Dst.Addr()),
			cmp.Compare(a.Dst.Bits(), b.Dst.Bits()),
			cmp.Compare(a.Metric, b.Metric),
			cmp.Compare(a.IfIndex, b.IfIndex),
			a.Gateway.Compare(b.Gateway),
		)
	})
}

// newRouteRequest builds the request that adds (rtmNewRoute) or deletes
// (rtmDelRoute) r. ifIndex resolves an interface name to its index.
//
// A delete names what makes the route's key: destination, interface, gateway
// and metric. Protocol and scope stay open, so that a route another program
// changed can still be matched by the key it was added with.
func newRouteRequest(typ uint16, r osnet.Route, ifIndex func(name string) (uint32, error)) (routeRequest, error) {
	if !r.Dst.IsValid() {
		return routeRequest{}, errors.New("route has no destination")
	}
	dst := r.Dst.Masked()
	req := routeRequest{Type: typ, Dst: dst, Metric: r.Metric, RouteType: rtnUnicast}
	adding := typ == rtmNewRoute
	if adding {
		req.Flags = nlmFCreate | nlmFExcl
		req.Protocol = RouteProtocol
	} else {
		req.Scope = rtScopeNowhere
	}

	if r.Blackhole {
		// A blackhole route has no next hop. What Dump read from an unreachable
		// or prohibit route is deleted as the type it has.
		req.RouteType = rtnBlackhole
		if t := uint8(r.Flags >> 16); blackholeType(t) && !adding {
			req.RouteType = t
		}
		return req, nil
	}

	if r.Gateway.IsValid() {
		req.Gateway = r.Gateway.WithZone("")
		if req.Gateway.Is4() != dst.Addr().Is4() {
			return routeRequest{}, fmt.Errorf("gateway %s is not in the family of %s", req.Gateway, dst)
		}
	}
	req.OIf = r.IfIndex
	if req.OIf == 0 {
		// The zone of a link-local gateway names its interface.
		if name := cmp.Or(r.Iface, r.Gateway.Zone()); name != "" {
			var err error
			if req.OIf, err = ifIndex(name); err != nil {
				return routeRequest{}, err
			}
		}
	}
	switch {
	case req.Gateway.IsValid():
		if req.OIf == 0 && req.Gateway.IsLinkLocalUnicast() {
			return routeRequest{}, fmt.Errorf("link-local gateway %s needs an interface", req.Gateway)
		}
	case req.OIf == 0:
		return routeRequest{}, errors.New("a route without gateway needs an interface")
	case adding:
		req.Scope = rtScopeLink
	}
	return req, nil
}

// routeError maps the errno of a failed route write to the osnet errors.
func routeError(op string, dst netip.Prefix, err error) error {
	switch {
	case errors.Is(err, syscall.EEXIST):
		err = osnet.ErrExists
	case errors.Is(err, syscall.ESRCH):
		err = osnet.ErrNotFound
	case op == opDelete && (errors.Is(err, syscall.ENODEV) || errors.Is(err, errNoSuchInterface)):
		err = osnet.ErrNotFound
	// A gateway that is on no connected network: IPv4 answers ENETUNREACH, IPv6
	// EHOSTUNREACH.
	case errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EHOSTUNREACH):
		err = osnet.ErrUnreachable
	// An interface that is down, or was removed after the caller looked: it may
	// come back, which is all that ErrUnreachable says. The kernel's own words
	// are kept.
	case op == opAdd && (errors.Is(err, syscall.ENETDOWN) || errors.Is(err, syscall.ENODEV)):
		err = fmt.Errorf("%w: %w", osnet.ErrUnreachable, err)
	// IPv6 switched off on the interface (net.ipv6.conf.<name>.disable_ipv6): the
	// kernel refuses every IPv6 route through it. That is the host's setting, and
	// no retry changes it.
	case op == opAdd && dst.Addr().Is6() && errors.Is(err, syscall.EACCES):
		err = unsupportedError{err}
	}
	return fmt.Errorf("%s route %s: %w", op, dst, err)
}

// unsupportedError is err, which says errors.ErrUnsupported as well: the host
// refuses the operation for a reason that is its configuration, not the state
// of the network.
type unsupportedError struct{ err error }

func (e unsupportedError) Error() string        { return e.err.Error() }
func (e unsupportedError) Unwrap() error        { return e.err }
func (e unsupportedError) Is(target error) bool { return target == errors.ErrUnsupported }
