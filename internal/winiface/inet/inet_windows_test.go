package inet

import (
	"net/netip"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestInetRoundTrip(t *testing.T) {
	for _, text := range []string{"192.0.2.77", "2001:db8::77", "::1", "255.255.255.255"} {
		want := netip.MustParseAddr(text)
		var sa windows.RawSockaddrInet
		Set(&sa, want)
		if got := Addr(&sa); got != want {
			t.Errorf("%s: got %v", text, got)
		}
	}
	var sa windows.RawSockaddrInet
	Set(&sa, netip.MustParseAddr("::ffff:192.0.2.1"))
	if sa.Family != windows.AF_INET || Addr(&sa) != netip.MustParseAddr("192.0.2.1") {
		t.Errorf("mapped address was stored as family %d: %v", sa.Family, Addr(&sa))
	}
	Set(&sa, netip.Addr{})
	if sa.Family != windows.AF_UNSPEC || Addr(&sa).IsValid() {
		t.Errorf("zero Addr left family %d", sa.Family)
	}
	// The address row of an IPv4 address keeps its four bytes where the stack
	// reads them: the union overlays the sockaddr_in layout.
	var row windows.MibUnicastIpAddressRow
	Set((*windows.RawSockaddrInet)(unsafe.Pointer(&row.Address)), netip.MustParseAddr("192.0.2.77"))
	if row.Address.Family != windows.AF_INET || *(*[4]byte)(unsafe.Add(unsafe.Pointer(&row.Address), 4)) != [4]byte{192, 0, 2, 77} {
		t.Errorf("IPv4 layout in an address row: %+v", row.Address)
	}
}
