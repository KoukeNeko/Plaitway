package inet

import (
	"net/netip"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Addr reads the address in sa. An unset union (family AF_UNSPEC) and any
// family other than IPv4 and IPv6 give the zero Addr. The scope of an IPv6
// address is not part of the result: callers get it from the row's interface.
func Addr(sa *windows.RawSockaddrInet) netip.Addr {
	switch sa.Family {
	case windows.AF_INET:
		return netip.AddrFrom4((*windows.RawSockaddrInet4)(unsafe.Pointer(sa)).Addr)
	case windows.AF_INET6:
		return netip.AddrFrom16((*windows.RawSockaddrInet6)(unsafe.Pointer(sa)).Addr)
	}
	return netip.Addr{}
}

// Set writes addr into sa, replacing whatever it held. The zero Addr clears sa
// to AF_UNSPEC. An IPv4-mapped IPv6 address is written as IPv4, because the
// stack has no such thing as a mapped destination.
func Set(sa *windows.RawSockaddrInet, addr netip.Addr) {
	*sa = windows.RawSockaddrInet{}
	addr = addr.Unmap().WithZone("")
	switch {
	case addr.Is4():
		v4 := (*windows.RawSockaddrInet4)(unsafe.Pointer(sa))
		v4.Family = windows.AF_INET
		v4.Addr = addr.As4()
	case addr.Is6():
		v6 := (*windows.RawSockaddrInet6)(unsafe.Pointer(sa))
		v6.Family = windows.AF_INET6
		v6.Addr = addr.As16()
	}
}
