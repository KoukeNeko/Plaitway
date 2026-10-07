package macos

import (
	"errors"
	"fmt"
	"net/netip"
	"syscall"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// routeFromMessage converts a route message from the kernel into an
// osnet.Route. ifaceName returns "" for an index it does not know, 0 included.
// ok is false for a route osnet.Route cannot express: another address family,
// or a netmask that is not a prefix.
func routeFromMessage(m rtMessage, ifaceName func(index int) string) (r osnet.Route, ok bool) {
	if !m.has(rtaDst) {
		return osnet.Route{}, false
	}
	dst, _, isIP := ipFromSockaddr(m.Addrs[rtaxDst])
	if !isIP {
		return osnet.Route{}, false
	}
	// Without a netmask the route is a host route.
	prefixLen := dst.BitLen()
	if m.has(rtaNetmask) {
		if prefixLen, ok = prefixLenFromNetmask(m.Addrs[rtaxDst][1], m.Addrs[rtaxNetmask]); !ok {
			return osnet.Route{}, false
		}
	}
	r = osnet.Route{
		Dst:       netip.PrefixFrom(dst, prefixLen).Masked(),
		Iface:     ifaceName(m.Index),
		Blackhole: m.Flags&rtfBlackhole != 0,
		Static:    m.Flags&rtfStatic != 0,
		Scoped:    m.Flags&rtfIfScope != 0,
		Flags:     m.Flags,
	}
	// A gateway that is not an IP address is a link-level address: the route is
	// bound to the interface, or is a neighbor entry.
	if m.has(rtaGateway) {
		if gateway, scope, isIP := ipFromSockaddr(m.Addrs[rtaxGateway]); isIP {
			if zone := ifaceName(scope); zone != "" {
				gateway = gateway.WithZone(zone)
			}
			r.Gateway = gateway
		}
	}
	return r, true
}

// newRouteRequest builds the request that adds (rtmAdd) or deletes (rtmDelete)
// r. ifIndex resolves an interface name to its index.
//
// Host routes (a prefix of one address) are sent as RTF_HOST without a netmask,
// as route(8) does. A route that came from Dump has its own flags, and a /32
// there may be a network route with a full mask; the kernel only deletes it
// when the request has the same form, so the flags win over the prefix length.
func newRouteRequest(typ int, r osnet.Route, ifIndex func(name string) (int, error)) (routeRequest, error) {
	if !r.Dst.IsValid() {
		return routeRequest{}, errors.New("route has no destination")
	}
	dst := r.Dst.Masked()
	req := routeRequest{Type: typ, Flags: rtfUp | rtfStatic, Dst: dst, Netmask: true}
	host := dst.Bits() == dst.Addr().BitLen()
	if r.Flags != 0 {
		host = r.Flags&rtfHost != 0
	}
	if host {
		req.Flags |= rtfHost
		req.Netmask = false
	}

	var linkIndex int
	if r.Iface != "" {
		var err error
		if linkIndex, err = ifIndex(r.Iface); err != nil {
			return routeRequest{}, err
		}
	}
	if dst.Addr().Is6() && hasEmbeddedScope(dst.Addr().As16()) {
		req.DstScope = linkIndex
	}
	if r.Scoped {
		if linkIndex == 0 {
			return routeRequest{}, errors.New("a scoped route needs an interface")
		}
		req.Flags |= rtfIfScope
		req.Index = linkIndex
	}

	switch {
	case r.Blackhole:
		// route(8) takes the loopback address as the gateway of a blackhole.
		req.Flags |= rtfBlackhole | rtfGateway
		req.Gateway = netip.AddrFrom4([4]byte{127, 0, 0, 1})
		if dst.Addr().Is6() {
			req.Gateway = netip.IPv6Loopback()
		}
	case r.Gateway.IsValid():
		gateway := r.Gateway.WithZone("")
		if gateway.Is4() != dst.Addr().Is4() {
			return routeRequest{}, fmt.Errorf("gateway %s is not in the family of %s", gateway, dst)
		}
		req.Flags |= rtfGateway
		req.Gateway = gateway
		if gateway.Is6() && hasEmbeddedScope(gateway.As16()) {
			zone := r.Gateway.Zone()
			if zone == "" {
				zone = r.Iface
			}
			if zone == "" {
				return routeRequest{}, fmt.Errorf("link-local gateway %s needs an interface", gateway)
			}
			var err error
			if req.GatewayScope, err = ifIndex(zone); err != nil {
				return routeRequest{}, err
			}
		}
	default:
		if linkIndex == 0 {
			return routeRequest{}, errors.New("a route without gateway needs an interface")
		}
		req.LinkIndex = linkIndex
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
	case errors.Is(err, syscall.ENETUNREACH):
		err = osnet.ErrUnreachable
	}
	return fmt.Errorf("%s route %s: %w", op, dst, err)
}
