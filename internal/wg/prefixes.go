package wg

import (
	"cmp"
	"net/netip"
	"slices"
)

// privateRanges are what the "exclude private IPs" option keeps out of
// AllowedIPs: the local network stays outside the tunnel.
var privateRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
}

// subtractPrefixes returns the addresses of from that are not in excluded as
// the fewest prefixes: sorted by address with IPv4 first, none overlapping and
// no two that are the two halves of a larger one. Neither argument is changed.
func subtractPrefixes(from, excluded []netip.Prefix) []netip.Prefix {
	excluded = mergePrefixes(excluded)
	var out []netip.Prefix
	for _, p := range mergePrefixes(from) {
		out = append(out, subtractFrom(p, excluded)...)
	}
	return out
}

// subtractFrom is p less the prefixes of excluded, which are disjoint. Each
// half of p is handled on its own, so what is left comes out sorted, and a
// half that holds none of excluded stays whole: the pieces are as large as
// they can be, and no two of them are siblings.
func subtractFrom(p netip.Prefix, excluded []netip.Prefix) []netip.Prefix {
	var inside []netip.Prefix
	for _, e := range excluded {
		switch {
		case prefixContains(e, p):
			return nil
		case prefixContains(p, e):
			inside = append(inside, e)
		}
	}
	if len(inside) == 0 {
		return []netip.Prefix{p}
	}
	// What lies inside p is longer than p, so p is not a host prefix and has halves.
	low, high := halves(p)
	return append(subtractFrom(low, inside), subtractFrom(high, inside)...)
}

// mergePrefixes returns the fewest prefixes that hold the same addresses as
// prefixes, sorted by address with IPv4 first.
func mergePrefixes(prefixes []netip.Prefix) []netip.Prefix {
	sorted := make([]netip.Prefix, len(prefixes))
	for i, p := range prefixes {
		sorted[i] = p.Masked()
	}
	// A prefix sorts before everything inside it.
	slices.SortFunc(sorted, func(a, b netip.Prefix) int {
		return cmp.Or(a.Addr().Compare(b.Addr()), cmp.Compare(a.Bits(), b.Bits()))
	})

	var out []netip.Prefix
	for _, p := range sorted {
		if n := len(out); n > 0 && prefixContains(out[n-1], p) {
			continue
		}
		out = append(out, p)
		// Two siblings are their parent; the parent may now have a sibling too.
		for len(out) >= 2 && areSiblings(out[len(out)-2], out[len(out)-1]) {
			parent := parentOf(out[len(out)-1])
			out = append(out[:len(out)-2], parent)
		}
	}
	return out
}

// prefixContains reports whether every address of p is in outer; both are masked.
func prefixContains(outer, p netip.Prefix) bool {
	return outer.Bits() <= p.Bits() && outer.Contains(p.Addr())
}

func areSiblings(a, b netip.Prefix) bool {
	return a.Bits() == b.Bits() && a.Bits() > 0 && a != b && parentOf(a) == parentOf(b)
}

func parentOf(p netip.Prefix) netip.Prefix {
	return netip.PrefixFrom(p.Addr(), p.Bits()-1).Masked()
}
