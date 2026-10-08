package windows

import (
	"cmp"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"syscall"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// Win32 error codes (and the Winsock ones the stack may return as well) that
// the route functions answer with. They are numbers here so that the mapping is
// the same on every OS and can be tested everywhere.
const (
	errorFileNotFound        syscall.Errno = 2
	errorBadNetPath          syscall.Errno = 53
	errorNotFound            syscall.Errno = 1168
	errorNetworkUnreachable  syscall.Errno = 1231
	errorHostUnreachable     syscall.Errno = 1232
	errorObjectAlreadyExists syscall.Errno = 5010
	wsaeNetUnreachable       syscall.Errno = 10051
	wsaeHostUnreachable      syscall.Errno = 10065
)

// Route protocols (MIB_IPPROTO_*) and origins (NL_ROUTE_ORIGIN) that matter.
const (
	protocolNetMgmt        = 3
	protocolNTAutoStatic   = 10002
	protocolNTStatic       = 10006
	protocolNTStaticNonDOD = 10007
	originManual           = 0
)

// The Flags word of a route: the origin above the protocol.
const (
	flagsOriginShift  = 16
	flagsProtocolMask = 0xFFFF
)

type routeOp string

const (
	opAdd    routeOp = "add"
	opDelete routeOp = "delete"
)

// routeError maps the error of a failed route write to the osnet errors.
//
// A key that exists is ERROR_OBJECT_ALREADY_EXISTS, one that does not is
// ERROR_NOT_FOUND. When adding, the same code, or ERROR_FILE_NOT_FOUND, means
// that the interface the route names is gone, and the others mean that the next
// hop is not reachable from it: in both cases the route cannot be placed.
func routeError(op routeOp, dst netip.Prefix, err error) error {
	if err == nil {
		return nil // the call worked: wrapping nothing would make a success an error
	}
	switch {
	case errors.Is(err, errorObjectAlreadyExists) && op == opAdd:
		err = osnet.ErrExists
	case (errors.Is(err, errorNotFound) || errors.Is(err, errorFileNotFound)) && op == opDelete:
		err = osnet.ErrNotFound
	case op == opAdd && (errors.Is(err, errorNotFound) || errors.Is(err, errorFileNotFound) ||
		errors.Is(err, errorNetworkUnreachable) || errors.Is(err, errorHostUnreachable) ||
		errors.Is(err, errorBadNetPath) || errors.Is(err, wsaeNetUnreachable) || errors.Is(err, wsaeHostUnreachable)):
		err = osnet.ErrUnreachable
	}
	return fmt.Errorf("%s route %s: %w", op, dst, err)
}

// isStaticProtocol reports whether the protocol field says that an
// administrator or a program put the route there (MIB_IPPROTO_NETMGMT, and the
// legacy static ones), as opposed to the stack deriving it from an address.
// DHCP routes are NETMGMT too, like the default route macOS marks static.
func isStaticProtocol(protocol uint32) bool {
	switch protocol {
	case protocolNetMgmt, protocolNTAutoStatic, protocolNTStatic, protocolNTStaticNonDOD:
		return true
	}
	return false
}

// routeFlags packs what Windows knows about a route's origin into the opaque
// Flags word: the origin in the upper half, the protocol in the lower.
func routeFlags(protocol, origin uint32) uint32 {
	return origin<<flagsOriginShift | protocol&flagsProtocolMask
}

// interfaceForGateway finds the adapter a gateway is directly reachable on:
// one that is up and has an address in the gateway's subnet. A link-local IPv6
// gateway can be on any link, so the zone must name it. Among several adapters
// the lowest interface metric wins, then the lowest index.
func interfaceForGateway(adapters []adapter, gateway netip.Addr) (adapter, error) {
	if zone := gateway.Zone(); zone != "" {
		i := slices.IndexFunc(adapters, func(a adapter) bool { return a.Name == zone && a.Up })
		if i < 0 {
			return adapter{}, fmt.Errorf("gateway %s: interface %q: %w", gateway, zone, osnet.ErrUnreachable)
		}
		return adapters[i], nil
	}
	var onLink []adapter
	for _, a := range adapters {
		if a.Up && !isLoopback(a) && slices.ContainsFunc(a.Addrs, func(p netip.Prefix) bool { return p.Masked().Contains(gateway) }) {
			onLink = append(onLink, a)
		}
	}
	if len(onLink) == 0 {
		return adapter{}, fmt.Errorf("gateway %s is on no connected subnet: %w", gateway, osnet.ErrUnreachable)
	}
	family6 := gateway.Is6()
	return slices.MinFunc(onLink, func(a, b adapter) int {
		return cmp.Or(cmp.Compare(interfaceMetric(a, family6), interfaceMetric(b, family6)), cmp.Compare(a.Index, b.Index))
	}), nil
}

func interfaceMetric(a adapter, v6 bool) uint32 {
	if v6 {
		return a.Metric6
	}
	return a.Metric4
}

// sameRouteKey reports whether two routes have the key Windows keys a route
// by: destination, interface and next hop. The metric is not part of it.
func sameRouteKey(a, b osnet.Route) bool {
	return a.Dst.Masked() == b.Dst.Masked() && a.IfIndex == b.IfIndex && a.Gateway.WithZone("") == b.Gateway.WithZone("")
}

// matchDelete picks the route in table that a Delete without an interface
// means: the one to the same destination and, when given, through the same
// gateway. ErrNotFound when there is none, an error when several are left, so
// that a route of another adapter is never deleted by guess.
func matchDelete(table []osnet.Route, want osnet.Route) (osnet.Route, error) {
	var matches []osnet.Route
	for _, r := range table {
		if r.Dst.Masked() == want.Dst.Masked() && (!want.Gateway.IsValid() || r.Gateway == want.Gateway.WithZone("")) {
			matches = append(matches, r)
		}
	}
	switch len(matches) {
	case 0:
		return osnet.Route{}, osnet.ErrNotFound
	case 1:
		return matches[0], nil
	}
	return osnet.Route{}, fmt.Errorf("%d routes to %s match; name the interface", len(matches), want.Dst)
}
