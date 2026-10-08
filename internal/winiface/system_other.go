//go:build !windows

package winiface

import "net/netip"

// host is the stand-in off Windows: every call reports ErrUnsupported.
var host system = unsupportedSystem{}

type unsupportedSystem struct{}

func (unsupportedSystem) luidFromName(string) (uint64, error)    { return 0, ErrUnsupported }
func (unsupportedSystem) indexFromLUID(uint64) (uint32, error)   { return 0, ErrUnsupported }
func (unsupportedSystem) nameFromLUID(uint64) (string, error)    { return "", ErrUnsupported }
func (unsupportedSystem) addresses(uint64) ([]address, error)    { return nil, ErrUnsupported }
func (unsupportedSystem) addAddress(uint64, netip.Prefix) error  { return ErrUnsupported }
func (unsupportedSystem) deleteAddress(uint64, netip.Addr) error { return ErrUnsupported }
func (unsupportedSystem) addressState(uint64, netip.Addr) (dadState, error) {
	return dadInvalid, ErrUnsupported
}
func (unsupportedSystem) changeIPInterface(uint64, Family, func(*ipInterface)) error {
	return ErrUnsupported
}
func (unsupportedSystem) isUp(uint64) (bool, error) { return false, ErrUnsupported }
