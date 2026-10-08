// Package inet converts between netip.Addr and the SOCKADDR_INET union that
// the IP Helper API puts in every route, address and interface row. The
// conversions exist on Windows only; the package is empty elsewhere.
package inet
