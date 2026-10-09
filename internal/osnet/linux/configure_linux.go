package linux

import (
	"errors"
	"fmt"
	"math"
	"net/netip"
	"syscall"
)

// ipv6MinMTU is the smallest MTU an interface with IPv6 addresses can have: the
// kernel removes them when the MTU goes below it.
const ipv6MinMTU = 1280

// ConfigureLink gives a link that was just created its addresses and MTU, and
// brings it up. It stands in for ifconfig on macOS. An address the link has
// already is left as it is; mtu 0 leaves the MTU alone.
func ConfigureLink(name string, addrs []netip.Prefix, mtu int) error {
	if mtu < 0 || mtu > math.MaxInt32 {
		return fmt.Errorf("configure link %s: MTU %d out of range", name, mtu)
	}
	for _, prefix := range addrs {
		if !prefix.IsValid() {
			return fmt.Errorf("configure link %s: invalid address", name)
		}
		if mtu != 0 && mtu < ipv6MinMTU && prefix.Addr().Is6() {
			return fmt.Errorf("configure link %s: MTU %d would remove the IPv6 address %s, the kernel needs at least %d", name, mtu, prefix, ipv6MinMTU)
		}
	}
	conn, err := openNetlink(0)
	if err != nil {
		return fmt.Errorf("configure link %s: %w", name, err)
	}
	defer conn.close()
	index, err := conn.interfaceIndex(name)
	if err != nil {
		return fmt.Errorf("configure link %s: %w", name, err)
	}
	for _, prefix := range addrs {
		err := conn.request(func(seq uint32) []byte { return newAddrRequest(index, prefix, seq) })
		if err != nil && !errors.Is(err, syscall.EEXIST) {
			return fmt.Errorf("configure link %s: add address %s: %w", name, prefix, err)
		}
	}
	if err := conn.request(func(seq uint32) []byte { return newLinkUpRequest(index, uint32(mtu), seq) }); err != nil {
		return fmt.Errorf("configure link %s: set MTU and bring up: %w", name, err)
	}
	return nil
}
