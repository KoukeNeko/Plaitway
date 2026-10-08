package wg

import (
	"context"
	"fmt"
	"net/netip"
	"slices"

	"golang.zx2c4.com/wireguard/tun"

	"github.com/KoukeNeko/Plaitway/internal/winiface"
)

const (
	// tunnelInterfaceMetric makes the tunnel the preferred interface among
	// routes that tie. It is added to the metric of every route through the
	// interface; the routes themselves are the Reconciler's.
	tunnelInterfaceMetric = 5

	// minIPv6MTU is the smallest MTU IPv6 works with; Windows refuses less.
	minIPv6MTU = 1280
)

// luidReader is implemented by wireguard-go's wintun device.
type luidReader interface {
	LUID() uint64
}

// nativeLUID is the LUID of the adapter behind dev, 0 for a device that is not
// a wintun adapter or is closed.
func nativeLUID(dev tun.Device) uint64 {
	if reader, ok := dev.(luidReader); ok {
		return reader.LUID()
	}
	return 0
}

// windowsInterfaces is the Interfaces of Windows: the IP Helper API through
// winiface. The adapter is found by its LUID, which is not a name another
// adapter can take; by name only when the device did not tell it.
type windowsInterfaces struct {
	luid uint64
}

func (w windowsInterfaces) Configure(ctx context.Context, name string, addrs []netip.Prefix, mtu int) error {
	link, err := w.find(name)
	if err != nil {
		return err
	}
	if err := setMTUs(link, addrs, mtu); err != nil {
		return err
	}
	if err := link.SetAddresses(addrs); err != nil {
		return err
	}
	if err := link.SetInterfaceMetric(tunnelInterfaceMetric); err != nil {
		return err
	}
	return link.WaitUp(ctx)
}

func (w windowsInterfaces) find(name string) (winiface.Link, error) {
	if w.luid != 0 {
		return winiface.FindByLUID(w.luid)
	}
	return winiface.FindByName(name)
}

// setMTUs sets the MTU of each IP family the profile has an address in.
func setMTUs(link winiface.Link, addrs []netip.Prefix, mtu int) error {
	for _, family := range mtuFamilies(addrs, mtu) {
		if err := link.SetMTU(uint32(mtu), family); err != nil {
			return fmt.Errorf("set the MTU: %w", err)
		}
	}
	return nil
}

// mtuFamilies are the families whose MTU is set: those the profile has an
// address in, as the other may be missing from the adapter (disabled in the
// system), which is not a reason to fail. IPv6 does not work below
// minIPv6MTU, so a smaller profile MTU leaves the IPv6 interface alone and an
// IPv6 address of the profile is then refused by the system with its own
// message.
func mtuFamilies(addrs []netip.Prefix, mtu int) []winiface.Family {
	var families []winiface.Family
	if slices.ContainsFunc(addrs, func(p netip.Prefix) bool { return p.Addr().Is4() }) {
		families = append(families, winiface.IPv4)
	}
	if mtu >= minIPv6MTU && slices.ContainsFunc(addrs, func(p netip.Prefix) bool { return p.Addr().Is6() }) {
		families = append(families, winiface.IPv6)
	}
	return families
}
