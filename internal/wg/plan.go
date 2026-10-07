package wg

import (
	"net/netip"
	"slices"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

var (
	defaultV4 = netip.MustParsePrefix("0.0.0.0/0")
	defaultV6 = netip.MustParsePrefix("::/0")
)

// plan is what a tunnel asks the Reconciler for once it is up.
type plan struct {
	routes   []netip.Prefix
	role     tunnel.Role
	dns      []tunnel.DNSIntent
	warnings []string // for the user, about what the plan leaves out
}

// plan combines the profile with the user's mode. The default route is passed
// on as 0.0.0.0/0 and ::/0; the Reconciler splits it.
func (p *profile) plan(mode tunnel.Mode) plan {
	var pl plan
	if p.noRoutes {
		if mode == tunnel.ModeFull {
			pl.warnings = append(pl.warnings, "Full tunnel ignored: Table = off")
		}
	} else {
		pl.routes = p.allowedIPs()
	}

	switch mode {
	case tunnel.ModeSplit:
		pl.routes = slices.DeleteFunc(pl.routes, isDefaultRoute)
	case tunnel.ModeFull:
		// Everything sent into the tunnel has to be something the peers accept,
		// so a family is only redirected when AllowedIPs already cover all of it.
		coversV4, coversV6 := covers(pl.routes, defaultV4), covers(pl.routes, defaultV6)
		if coversV4 {
			pl.routes = withDefault(pl.routes, defaultV4)
		}
		if coversV6 {
			pl.routes = withDefault(pl.routes, defaultV6)
		}
		if !p.noRoutes && !coversV4 && !coversV6 {
			pl.warnings = append(pl.warnings, "Full tunnel ignored: AllowedIPs do not cover all addresses")
		}
	}

	if slices.ContainsFunc(pl.routes, isDefaultRoute) {
		pl.role = tunnel.RoleFull
	}
	// The exclusion comes last, on purpose. A family counts as covered (Full
	// mode above) when AllowedIPs covered all of it before the exclusion: with
	// the private ranges gone nothing covers a whole family any more. A full
	// tunnel keeps its role, and with it DNS for everything, although its
	// default route is now a list of prefixes. And Split mode has already
	// dropped the default routes, so they do not come back as prefixes.
	if p.excludePrivate && len(pl.routes) > 0 {
		pl.routes = p.withoutPrivate(pl.routes)
		if len(pl.routes) == 0 {
			pl.warnings = append(pl.warnings, "No route left after excluding private ranges")
		}
	}
	pl.dns, pl.warnings = p.dnsPlan(pl.role == tunnel.RoleFull, pl.warnings)
	return pl
}

// allowedIPs is every peer's AllowedIPs, without duplicates.
func (p *profile) allowedIPs() []netip.Prefix {
	var all []netip.Prefix
	for _, peer := range p.peers {
		for _, prefix := range peer.allowedIPs {
			if !slices.Contains(all, prefix) {
				all = append(all, prefix)
			}
		}
	}
	return all
}

// dnsPlan answers the profile's DNS servers for its search domains, and for
// everything ("." ) when the tunnel is a full tunnel. A split tunnel without
// search domains has nothing the servers could be asked for.
func (p *profile) dnsPlan(full bool, warnings []string) ([]tunnel.DNSIntent, []string) {
	if len(p.dnsServers) == 0 {
		return nil, warnings
	}
	domains := slices.Clone(p.dnsDomains)
	if full {
		domains = append(domains, ".")
	}
	if len(domains) == 0 {
		return nil, append(warnings, "DNS servers ignored: no search domain and not a full tunnel")
	}
	return []tunnel.DNSIntent{{Servers: slices.Clone(p.dnsServers), MatchDomains: domains}}, warnings
}

// withDefault replaces the routes of def's address family by def.
func withDefault(routes []netip.Prefix, def netip.Prefix) []netip.Prefix {
	if slices.Contains(routes, def) {
		return routes
	}
	routes = slices.DeleteFunc(slices.Clone(routes), func(p netip.Prefix) bool {
		return p.Addr().Is4() == def.Addr().Is4()
	})
	return append(routes, def)
}

// covers reports whether the prefixes together contain every address of p.
func covers(prefixes []netip.Prefix, p netip.Prefix) bool {
	var inside []netip.Prefix
	for _, c := range prefixes {
		if c.Addr().BitLen() != p.Addr().BitLen() {
			continue
		}
		if c.Bits() <= p.Bits() && c.Contains(p.Addr()) {
			return true
		}
		if p.Bits() < c.Bits() && p.Contains(c.Addr()) {
			inside = append(inside, c)
		}
	}
	if len(inside) == 0 || p.Bits() == p.Addr().BitLen() {
		return false
	}
	low, high := halves(p)
	return covers(inside, low) && covers(inside, high)
}

// halves splits a prefix that is not a host prefix into its two halves.
func halves(p netip.Prefix) (low, high netip.Prefix) {
	bits := p.Bits() + 1
	addr := p.Masked().Addr().AsSlice()
	low = netip.PrefixFrom(p.Masked().Addr(), bits)
	addr[p.Bits()/8] |= 0x80 >> (p.Bits() % 8)
	highAddr, _ := netip.AddrFromSlice(addr)
	return low, netip.PrefixFrom(highAddr, bits)
}
