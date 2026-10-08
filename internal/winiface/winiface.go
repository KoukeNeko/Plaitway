// Package winiface finds a Windows network interface and configures what an
// engine needs on the adapter it created: addresses, MTU, interface metric,
// and the wait until the link is up. It is the only code besides the osnet
// adapters that talks to the IP Helper API, so that the engines never
// duplicate it. It deliberately knows nothing about routes and DNS: those have
// one owner, the Reconciler.
//
// Everything compiles on every OS so that platform-neutral code can import it;
// off Windows each call returns ErrUnsupported.
package winiface

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"
)

var (
	// ErrNotFound: no interface has this name, or LUID.
	ErrNotFound = errors.New("network interface not found")
	// ErrUnsupported is returned on every OS but Windows.
	ErrUnsupported = errors.New("winiface: network interfaces are configured through the IP Helper API, which exists on Windows only")
)

// Family is an address family, by the value of AF_INET and AF_INET6, so that
// the constants of golang.org/x/sys/windows can be passed as well.
type Family uint16

const (
	IPv4 Family = 2
	IPv6 Family = 23
)

func (f Family) String() string {
	switch f {
	case IPv4:
		return "IPv4"
	case IPv6:
		return "IPv6"
	}
	return fmt.Sprintf("address family %d", uint16(f))
}

// Link is one network interface.
type Link struct {
	// LUID is the locally unique identifier of the interface: it stays the same
	// for the life of the adapter, where Index may change with a driver update.
	LUID  uint64
	Index uint32
	// Name is the interface alias, the name shown in the network settings.
	Name string
}

// Windows runs duplicate address detection for a new address; for IPv4 it
// lasts about three seconds on a link that is not a loopback. The wait is
// bounded because a link that never answers must not hang the engine. They are
// variables so that tests can shorten them.
var (
	dadTimeout      = 15 * time.Second
	dadPollInterval = 50 * time.Millisecond
	upPollInterval  = 100 * time.Millisecond
)

// dadState is the state of an address in duplicate address detection.
type dadState uint8

const (
	dadInvalid dadState = iota
	dadTentative
	dadDuplicate
	dadDeprecated
	dadPreferred
)

// address is a unicast address of a link, as the system lists it.
type address struct {
	Prefix netip.Prefix // the address with the length of its on-link prefix
	// Manual is true when both origins say that somebody configured it by hand
	// or through the API, as opposed to DHCP, router advertisements, a random
	// or well-known suffix.
	Manual bool
	DAD    dadState
}

// ipInterface is the part of an IP interface that Plaitway changes.
type ipInterface struct {
	MTU        uint32
	Metric     uint32
	AutoMetric bool
}

// system is the IP Helper API as far as this package uses it. The Windows
// implementation calls the real functions; tests supply a fake.
type system interface {
	luidFromName(name string) (uint64, error)
	indexFromLUID(luid uint64) (uint32, error)
	nameFromLUID(luid uint64) (string, error)
	addresses(luid uint64) ([]address, error)
	addAddress(luid uint64, p netip.Prefix) error
	deleteAddress(luid uint64, a netip.Addr) error
	addressState(luid uint64, a netip.Addr) (dadState, error)
	// changeIPInterface reads the IP interface of one family, lets change edit
	// it and writes it back. It returns errFamilyNotBound when the link has no
	// such IP stack.
	changeIPInterface(luid uint64, family Family, change func(*ipInterface)) error
	isUp(luid uint64) (bool, error)
}

var errFamilyNotBound = errors.New("the interface has no IP stack of this family")

// FindByName returns the interface whose alias is name.
func FindByName(name string) (Link, error) {
	if name == "" {
		return Link{}, fmt.Errorf("find interface: %w: empty name", ErrNotFound)
	}
	luid, err := host.luidFromName(name)
	if err != nil {
		return Link{}, fmt.Errorf("find interface %q: %w", name, err)
	}
	return linkOf(luid)
}

// FindByLUID returns the interface with this LUID.
func FindByLUID(luid uint64) (Link, error) {
	link, err := linkOf(luid)
	if err != nil {
		return Link{}, fmt.Errorf("find interface with LUID %#x: %w", luid, err)
	}
	return link, nil
}

func linkOf(luid uint64) (Link, error) {
	index, err := host.indexFromLUID(luid)
	if err != nil {
		return Link{}, err
	}
	name, err := host.nameFromLUID(luid)
	if err != nil {
		return Link{}, err
	}
	return Link{LUID: luid, Index: index, Name: name}, nil
}

// SetAddresses makes prefixes the manually configured unicast addresses of the
// link, IPv4 and IPv6, and returns once the new ones are usable (duplicate
// address detection has ended). Each prefix is an address with the length of its
// on-link subnet, so 10.7.0.2/24 is the address 10.7.0.2 and the connected
// route 10.7.0.0/24.
//
// Addresses that the system configured (DHCP, autoconfiguration, the IPv6
// link-local address) and loopback addresses are left alone; every other
// manual address of the link that is not in prefixes is removed. Calling it
// again with the same prefixes changes nothing.
func (l Link) SetAddresses(prefixes []netip.Prefix) error {
	wanted, err := normalizePrefixes(prefixes)
	if err != nil {
		return fmt.Errorf("set addresses of %s: %w", l.Name, err)
	}
	current, err := host.addresses(l.LUID)
	if err != nil {
		return fmt.Errorf("list addresses of %s: %w", l.Name, err)
	}
	plan := planAddresses(current, wanted)
	for _, addr := range plan.remove {
		if err := host.deleteAddress(l.LUID, addr); err != nil {
			return fmt.Errorf("remove address %s from %s: %w", addr, l.Name, err)
		}
	}
	for _, prefix := range plan.add {
		if err := host.addAddress(l.LUID, prefix); err != nil {
			return fmt.Errorf("add address %s to %s: %w", prefix, l.Name, err)
		}
	}
	for _, prefix := range plan.add {
		if err := waitForDAD(l.LUID, prefix.Addr()); err != nil {
			return fmt.Errorf("address %s on %s: %w", prefix.Addr(), l.Name, err)
		}
	}
	return nil
}

// SetMTU sets the MTU of the link's IP interface of one family.
func (l Link) SetMTU(mtu uint32, family Family) error {
	if family != IPv4 && family != IPv6 {
		return fmt.Errorf("set MTU of %s: unsupported %s", l.Name, family)
	}
	if mtu == 0 {
		return fmt.Errorf("set MTU of %s: MTU is zero", l.Name)
	}
	err := host.changeIPInterface(l.LUID, family, func(row *ipInterface) { row.MTU = mtu })
	if err != nil {
		return fmt.Errorf("set %s MTU of %s to %d: %w", family, l.Name, mtu, err)
	}
	return nil
}

// SetInterfaceMetric sets the interface metric of both IP families, which is
// added to the metric of every route through the link. Zero gives the decision
// back to the system (automatic metric). A family the link has no IP stack for
// is skipped; a link with neither is an error.
func (l Link) SetInterfaceMetric(metric uint32) error {
	change := func(row *ipInterface) { row.Metric, row.AutoMetric = metric, metric == 0 }
	var set int
	for _, family := range []Family{IPv4, IPv6} {
		err := host.changeIPInterface(l.LUID, family, change)
		switch {
		case errors.Is(err, errFamilyNotBound):
		case err != nil:
			return fmt.Errorf("set %s interface metric of %s to %d: %w", family, l.Name, metric, err)
		default:
			set++
		}
	}
	if set == 0 {
		return fmt.Errorf("set interface metric of %s: %w", l.Name, errFamilyNotBound)
	}
	return nil
}

// WaitUp returns when the link's operational status is up, or when ctx ends.
func (l Link) WaitUp(ctx context.Context) error {
	ticker := time.NewTicker(upPollInterval)
	defer ticker.Stop()
	for {
		up, err := host.isUp(l.LUID)
		if err != nil {
			return fmt.Errorf("read status of %s: %w", l.Name, err)
		}
		if up {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for %s to come up: %w", l.Name, context.Cause(ctx))
		case <-ticker.C:
		}
	}
}

// waitForDAD polls until the address has left the tentative state.
func waitForDAD(luid uint64, addr netip.Addr) error {
	deadline := time.Now().Add(dadTimeout)
	for {
		state, err := host.addressState(luid, addr)
		if err != nil {
			return err
		}
		switch state {
		case dadPreferred, dadDeprecated:
			return nil
		case dadDuplicate:
			return errors.New("duplicate address detected: another host on the link uses it")
		case dadInvalid:
			return errors.New("the system does not list the address after adding it")
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("still tentative after %s", dadTimeout)
		}
		time.Sleep(dadPollInterval)
	}
}
