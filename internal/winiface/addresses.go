package winiface

import (
	"cmp"
	"fmt"
	"net/netip"
	"slices"
)

// addressPlan is the difference between the addresses a link has and the ones
// it should have.
type addressPlan struct {
	remove []netip.Addr
	add    []netip.Prefix
}

// normalizePrefixes validates the wanted addresses and returns them sorted
// without duplicates. An IPv4-mapped IPv6 prefix is taken as IPv4. The
// address keeps its host bits: the prefix says how long the on-link subnet is.
func normalizePrefixes(prefixes []netip.Prefix) ([]netip.Prefix, error) {
	wanted := make([]netip.Prefix, 0, len(prefixes))
	for _, p := range prefixes {
		if !p.IsValid() {
			return nil, fmt.Errorf("invalid prefix %v", p)
		}
		addr := p.Addr().Unmap()
		bits := p.Bits()
		if p.Addr().Is4In6() {
			bits -= 96
		}
		if bits < 0 || addr.Zone() != "" || addr.IsUnspecified() || addr.IsMulticast() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
			return nil, fmt.Errorf("%v is not an address that can be configured", p)
		}
		wanted = append(wanted, netip.PrefixFrom(addr, bits))
	}
	slices.SortFunc(wanted, comparePrefix)
	wanted = slices.Compact(wanted)
	for i := 1; i < len(wanted); i++ {
		if wanted[i].Addr() == wanted[i-1].Addr() {
			return nil, fmt.Errorf("%s is wanted with two prefix lengths: /%d and /%d", wanted[i].Addr(), wanted[i-1].Bits(), wanted[i].Bits())
		}
	}
	return wanted, nil
}

func comparePrefix(a, b netip.Prefix) int {
	return cmp.Or(a.Addr().Compare(b.Addr()), cmp.Compare(a.Bits(), b.Bits()))
}

// plaitwayOwns reports whether SetAddresses may remove the address: only what
// somebody configured, never what the system derived (the IPv6 link-local
// address, autoconfiguration) and never the loopback addresses, which are
// manual too.
func plaitwayOwns(a address) bool {
	addr := a.Prefix.Addr()
	return a.Manual && !addr.IsLoopback() && !addr.IsLinkLocalUnicast()
}

// planAddresses lists what to remove and what to add. An address that is there
// with another prefix length is removed and added again, because the prefix
// length of an existing address cannot be changed.
func planAddresses(current []address, wanted []netip.Prefix) addressPlan {
	var plan addressPlan
	kept := make(map[netip.Prefix]bool, len(current))
	for _, a := range current {
		switch {
		case slices.Contains(wanted, a.Prefix):
			kept[a.Prefix] = true // whoever configured it, it is there
		case plaitwayOwns(a):
			plan.remove = append(plan.remove, a.Prefix.Addr())
		}
	}
	for _, p := range wanted {
		if !kept[p] {
			plan.add = append(plan.add, p)
		}
	}
	slices.SortFunc(plan.remove, netip.Addr.Compare)
	return plan
}
