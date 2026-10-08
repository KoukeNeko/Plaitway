package winiface

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// loopbackAlias is the name of the loopback pseudo-interface on every Windows.
const loopbackAlias = "Loopback Pseudo-Interface 1"

// These tests only read: they look up the loopback pseudo-interface and list
// its addresses.

func TestRealLoopbackLink(t *testing.T) {
	link, err := FindByName(loopbackAlias)
	if err != nil {
		t.Fatal(err)
	}
	if link.LUID == 0 || link.Index == 0 || link.Name != loopbackAlias {
		t.Fatalf("link = %+v", link)
	}
	again, err := FindByLUID(link.LUID)
	if err != nil || again != link {
		t.Fatalf("FindByLUID = %+v, %v; want %+v", again, err, link)
	}
	if _, err := FindByName("Plaitway-test-no-such-adapter"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown name: %v, want ErrNotFound", err)
	}
	if _, err := FindByLUID(0xDEADBEEF00); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown LUID: %v, want ErrNotFound", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := link.WaitUp(ctx); err != nil {
		t.Errorf("WaitUp on the loopback: %v", err)
	}
}

// The loopback addresses are manual in the system's eyes, which is exactly why
// SetAddresses must not treat them as its own.
func TestRealLoopbackAddressesAreNeverOwned(t *testing.T) {
	link, err := FindByName(loopbackAlias)
	if err != nil {
		t.Fatal(err)
	}
	addrs, err := host.addresses(link.LUID)
	if err != nil {
		t.Fatal(err)
	}
	var sawIPv4Loopback bool
	for _, a := range addrs {
		if a.Prefix.Addr() == netip.MustParseAddr("127.0.0.1") {
			sawIPv4Loopback = true
			if a.DAD != dadPreferred {
				t.Errorf("127.0.0.1 is in DAD state %d", a.DAD)
			}
		}
		if plaitwayOwns(a) {
			t.Errorf("%v would be treated as Plaitway's", a.Prefix)
		}
	}
	if !sawIPv4Loopback {
		t.Errorf("127.0.0.1 is not among the addresses of the loopback: %+v", addrs)
	}
	state, err := host.addressState(link.LUID, netip.MustParseAddr("192.0.2.123"))
	if err != nil || state != dadInvalid {
		t.Errorf("state of an address that is not there: %v, %v", state, err)
	}
}

func TestRealIPInterfaceReadsBothFamilies(t *testing.T) {
	link, err := FindByName(loopbackAlias)
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range []Family{IPv4, IPv6} {
		row := windows.MibIpInterfaceRow{Family: uint16(family), InterfaceLuid: link.LUID}
		if err := windows.GetIpInterfaceEntry(&row); err != nil {
			t.Errorf("%s: %v", family, err)
		}
	}
}

// Writing needs an elevated process. Run without one, the calls reach the
// system, are refused with ERROR_ACCESS_DENIED, and change nothing. They are
// skipped when elevated, so that they never write for real.
func TestRealWritesAreRefusedWithoutElevation(t *testing.T) {
	if windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("elevated: the rootintegration tests cover writing")
	}
	link, err := FindByName(loopbackAlias)
	if err != nil {
		t.Fatal(err)
	}
	scratch := netip.MustParsePrefix("192.0.2.77/32")
	if err := link.SetAddresses([]netip.Prefix{scratch}); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Errorf("SetAddresses = %v, want ERROR_ACCESS_DENIED", err)
	}
	if err := link.SetInterfaceMetric(76); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Errorf("SetInterfaceMetric = %v, want ERROR_ACCESS_DENIED", err)
	}
	if err := link.SetMTU(1400, IPv4); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Errorf("SetMTU = %v, want ERROR_ACCESS_DENIED", err)
	}
	addrs, err := host.addresses(link.LUID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		if a.Prefix.Addr() == scratch.Addr() {
			t.Errorf("%v is on the loopback", a.Prefix)
		}
	}
}
