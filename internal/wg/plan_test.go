package wg

import (
	"net/netip"
	"slices"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

func TestCovers(t *testing.T) {
	tests := []struct {
		name     string
		prefixes []netip.Prefix
		target   netip.Prefix
		want     bool
	}{
		{"nothing", nil, defaultV4, false},
		{"the default route", pfx("0.0.0.0/0"), defaultV4, true},
		{"two halves", pfx("0.0.0.0/1", "128.0.0.0/1"), defaultV4, true},
		{"one half", pfx("0.0.0.0/1"), defaultV4, false},
		{"four quarters", pfx("0.0.0.0/2", "64.0.0.0/2", "128.0.0.0/2", "192.0.0.0/2"), defaultV4, true},
		{"quarters with a hole", pfx("0.0.0.0/2", "64.0.0.0/2", "192.0.0.0/2"), defaultV4, false},
		{"a half and two quarters", pfx("0.0.0.0/1", "128.0.0.0/2", "192.0.0.0/2"), defaultV4, true},
		{"a host short of everything", pfx("0.0.0.0/1", "128.0.0.0/1", "10.0.0.1/32"), pfx("10.0.0.1/32")[0], true},
		{"one address missing", pfx("0.0.0.0/1", "128.0.0.0/2", "192.0.0.0/3", "224.0.0.0/4", "240.0.0.0/5", "248.0.0.0/6", "252.0.0.0/7", "254.0.0.0/8", "255.0.0.0/9"), defaultV4, false},
		{"IPv6 default", pfx("::/0"), defaultV6, true},
		{"IPv6 halves", pfx("::/1", "8000::/1"), defaultV6, true},
		{"IPv6 one half", pfx("8000::/1"), defaultV6, false},
		{"IPv4 does not cover IPv6", pfx("0.0.0.0/0"), defaultV6, false},
		{"IPv6 does not cover IPv4", pfx("::/0"), defaultV4, false},
		{"covered subnet", pfx("10.0.0.0/8"), pfx("10.1.0.0/16")[0], true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := covers(tt.prefixes, tt.target); got != tt.want {
				t.Errorf("covers(%v, %v) = %v, want %v", tt.prefixes, tt.target, got, tt.want)
			}
		})
	}
}

func TestPlan(t *testing.T) {
	const (
		head = "[Interface]\nPrivateKey = KEYA\n"
		peer = "[Peer]\nPublicKey = KEYB\nAllowedIPs = "
	)
	tests := []struct {
		name           string
		profile        string
		mode           tunnel.Mode
		excludePrivate bool
		wantRoutes     []netip.Prefix
		wantRole       tunnel.Role
		wantDNS        []tunnel.DNSIntent
		wantWarnings   []string
	}{
		{
			name:       "auto keeps a full tunnel as written",
			profile:    head + "DNS = 10.6.0.1\n" + peer + "0.0.0.0/0, ::/0\n",
			wantRoutes: pfx("0.0.0.0/0", "::/0"),
			wantRole:   tunnel.RoleFull,
			wantDNS:    []tunnel.DNSIntent{{Servers: addrs("10.6.0.1"), MatchDomains: []string{"."}}},
		},
		{
			name:       "full tunnel DNS also answers the search domains",
			profile:    head + "DNS = 10.6.0.1, corp.example\n" + peer + "0.0.0.0/0\n",
			wantRoutes: pfx("0.0.0.0/0"),
			wantRole:   tunnel.RoleFull,
			wantDNS:    []tunnel.DNSIntent{{Servers: addrs("10.6.0.1"), MatchDomains: []string{"corp.example", "."}}},
		},
		{
			name:       "split tunnel DNS answers only the search domains",
			profile:    head + "DNS = 10.6.0.1, corp.example, lan\n" + peer + "10.6.0.0/24\n",
			wantRoutes: pfx("10.6.0.0/24"),
			wantRole:   tunnel.RoleSplit,
			wantDNS:    []tunnel.DNSIntent{{Servers: addrs("10.6.0.1"), MatchDomains: []string{"corp.example", "lan"}}},
		},
		{
			name:         "split tunnel DNS without search domains has nothing to answer",
			profile:      head + "DNS = 10.6.0.1\n" + peer + "10.6.0.0/24\n",
			wantRoutes:   pfx("10.6.0.0/24"),
			wantRole:     tunnel.RoleSplit,
			wantWarnings: []string{"DNS servers ignored: no search domain and not a full tunnel"},
		},
		{
			name:       "search domains without servers announce no DNS",
			profile:    head + "DNS = corp.example\n" + peer + "10.6.0.0/24\n",
			wantRoutes: pfx("10.6.0.0/24"),
			wantRole:   tunnel.RoleSplit,
		},
		{
			name:       "split mode drops the default routes",
			profile:    head + "DNS = 10.6.0.1, corp.example\n" + peer + "0.0.0.0/0, ::/0, 192.168.1.0/24\n",
			mode:       tunnel.ModeSplit,
			wantRoutes: pfx("192.168.1.0/24"),
			wantRole:   tunnel.RoleSplit,
			wantDNS:    []tunnel.DNSIntent{{Servers: addrs("10.6.0.1"), MatchDomains: []string{"corp.example"}}},
		},
		{
			name:       "split mode on a split profile changes nothing",
			profile:    head + peer + "192.168.1.0/24\n",
			mode:       tunnel.ModeSplit,
			wantRoutes: pfx("192.168.1.0/24"),
			wantRole:   tunnel.RoleSplit,
		},
		{
			name:       "full mode on a full profile changes nothing",
			profile:    head + peer + "0.0.0.0/0, ::/0\n",
			mode:       tunnel.ModeFull,
			wantRoutes: pfx("0.0.0.0/0", "::/0"),
			wantRole:   tunnel.RoleFull,
		},
		{
			name:       "full mode with only an IPv4 default leaves IPv6 alone",
			profile:    head + peer + "0.0.0.0/0\n",
			mode:       tunnel.ModeFull,
			wantRoutes: pfx("0.0.0.0/0"),
			wantRole:   tunnel.RoleFull,
		},
		{
			name:       "full mode turns covering halves into default routes",
			profile:    head + peer + "0.0.0.0/1, 128.0.0.0/1, ::/1, 8000::/1\n",
			mode:       tunnel.ModeFull,
			wantRoutes: pfx("0.0.0.0/0", "::/0"),
			wantRole:   tunnel.RoleFull,
		},
		{
			name:       "full mode redirects only the family AllowedIPs cover",
			profile:    head + peer + "0.0.0.0/1, 128.0.0.0/1, fd00::/8\n",
			mode:       tunnel.ModeFull,
			wantRoutes: pfx("fd00::/8", "0.0.0.0/0"),
			wantRole:   tunnel.RoleFull,
		},
		{
			name:         "full mode cannot redirect what AllowedIPs do not cover",
			profile:      head + peer + "10.0.0.0/8, 192.168.1.0/24\n",
			mode:         tunnel.ModeFull,
			wantRoutes:   pfx("10.0.0.0/8", "192.168.1.0/24"),
			wantRole:     tunnel.RoleSplit,
			wantWarnings: []string{"Full tunnel ignored: AllowedIPs do not cover all addresses"},
		},
		{
			name:         "table off announces no routes",
			profile:      head + "DNS = 10.6.0.1, corp.example\nTable = off\n" + peer + "0.0.0.0/0\n",
			wantRole:     tunnel.RoleSplit,
			wantDNS:      []tunnel.DNSIntent{{Servers: addrs("10.6.0.1"), MatchDomains: []string{"corp.example"}}},
			wantWarnings: nil,
		},
		{
			name:         "table off ignores full mode",
			profile:      head + "Table = off\n" + peer + "0.0.0.0/0\n",
			mode:         tunnel.ModeFull,
			wantRole:     tunnel.RoleSplit,
			wantWarnings: []string{"Full tunnel ignored: Table = off"},
		},
		{
			name:       "peers sharing a prefix announce it once",
			profile:    head + peer + "10.0.0.0/8\n[Peer]\nPublicKey = KEYC\nAllowedIPs = 10.0.0.0/8, 172.16.0.0/12\n",
			wantRoutes: pfx("10.0.0.0/8", "172.16.0.0/12"),
			wantRole:   tunnel.RoleSplit,
		},
		{
			// The tunnel's own nameserver is in a private range, and DNS has to reach it through the tunnel.
			name:           "excluding private ranges turns a default route into everything else, and keeps the DNS server",
			profile:        head + "DNS = 10.6.0.1\n" + peer + "0.0.0.0/0, ::/0\n",
			excludePrivate: true,
			wantRoutes:     mergePrefixes(slices.Concat(publicV4, publicV6, pfx("10.6.0.1/32"))),
			wantRole:       tunnel.RoleFull,
			wantDNS:        []tunnel.DNSIntent{{Servers: addrs("10.6.0.1"), MatchDomains: []string{"."}}},
		},
		{
			name:           "a DNS server that is not private needs nothing kept",
			profile:        head + "DNS = 1.1.1.1\n" + peer + "0.0.0.0/0, ::/0\n",
			excludePrivate: true,
			wantRoutes:     slices.Concat(publicV4, publicV6),
			wantRole:       tunnel.RoleFull,
			wantDNS:        []tunnel.DNSIntent{{Servers: addrs("1.1.1.1"), MatchDomains: []string{"."}}},
		},
		{
			name:           "an IPv6 DNS server in a private range is kept too",
			profile:        head + "DNS = fd00::1\n" + peer + "0.0.0.0/0, ::/0\n",
			excludePrivate: true,
			wantRoutes:     mergePrefixes(slices.Concat(publicV4, publicV6, pfx("fd00::1/128"))),
			wantRole:       tunnel.RoleFull,
			wantDNS:        []tunnel.DNSIntent{{Servers: addrs("fd00::1"), MatchDomains: []string{"."}}},
		},
		{
			// The peer accepts nothing outside AllowedIPs, so a server they leave out is not asked for.
			name:           "a private DNS server that AllowedIPs leave out is not added",
			profile:        head + "DNS = 10.6.0.1, corp.example\n" + peer + "203.0.113.0/24\n",
			excludePrivate: true,
			wantRoutes:     pfx("203.0.113.0/24"),
			wantRole:       tunnel.RoleSplit,
			wantDNS:        []tunnel.DNSIntent{{Servers: addrs("10.6.0.1"), MatchDomains: []string{"corp.example"}}},
		},
		{
			name:           "a private DNS server inside AllowedIPs is the one route left",
			profile:        head + "DNS = 10.6.0.1, corp.example\n" + peer + "10.6.0.0/24\n",
			excludePrivate: true,
			wantRoutes:     pfx("10.6.0.1/32"),
			wantRole:       tunnel.RoleSplit,
			wantDNS:        []tunnel.DNSIntent{{Servers: addrs("10.6.0.1"), MatchDomains: []string{"corp.example"}}},
		},
		{
			name:           "excluding private ranges leaves a split tunnel its public prefixes",
			profile:        head + peer + "10.6.0.0/24, 203.0.113.0/24, 192.168.1.5/32, 2001:db8::/32\n",
			excludePrivate: true,
			wantRoutes:     pfx("203.0.113.0/24", "2001:db8::/32"),
			wantRole:       tunnel.RoleSplit,
		},
		{
			// Split mode drops what the profile calls its default route; the
			// exclusion must not turn that route into prefixes that stay.
			name:           "split mode drops the default route before the private ranges are excluded",
			profile:        head + peer + "0.0.0.0/0, ::/0, 203.0.113.0/24\n",
			mode:           tunnel.ModeSplit,
			excludePrivate: true,
			wantRoutes:     pfx("203.0.113.0/24"),
			wantRole:       tunnel.RoleSplit,
		},
		{
			name:           "split mode with only a default route has nothing to warn about",
			profile:        head + peer + "0.0.0.0/0\n",
			mode:           tunnel.ModeSplit,
			excludePrivate: true,
			wantRole:       tunnel.RoleSplit,
		},
		{
			// A family counts as covered when AllowedIPs covered it before the
			// exclusion: with the private ranges gone nothing covers it any more.
			name:           "full mode redirects a family that AllowedIPs covered before the exclusion",
			profile:        head + peer + "0.0.0.0/1, 128.0.0.0/1, fd00::/8\n",
			mode:           tunnel.ModeFull,
			excludePrivate: true,
			wantRoutes:     publicV4,
			wantRole:       tunnel.RoleFull,
		},
		{
			name:           "full mode with the exclusion cannot redirect what AllowedIPs do not cover",
			profile:        head + peer + "203.0.113.0/24, 192.168.1.0/24\n",
			mode:           tunnel.ModeFull,
			excludePrivate: true,
			wantRoutes:     pfx("203.0.113.0/24"),
			wantRole:       tunnel.RoleSplit,
			wantWarnings:   []string{"Full tunnel ignored: AllowedIPs do not cover all addresses"},
		},
		{
			name:           "private routes only: no route is announced and the user is told",
			profile:        head + peer + "10.6.0.0/24, 192.168.1.5/32, fd00::/8\n",
			excludePrivate: true,
			wantRole:       tunnel.RoleSplit,
			wantWarnings:   []string{"No route left after excluding private ranges"},
		},
		{
			name:           "table off announces no routes and has nothing to warn about",
			profile:        head + "Table = off\n" + peer + "10.6.0.0/24\n",
			excludePrivate: true,
			wantRole:       tunnel.RoleSplit,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := parseProfile([]byte(fill(tt.profile)))
			if err != nil {
				t.Fatal(err)
			}
			p.excludePrivate = tt.excludePrivate
			got := p.plan(tt.mode)
			if !slices.Equal(got.routes, tt.wantRoutes) {
				t.Errorf("routes = %v, want %v", got.routes, tt.wantRoutes)
			}
			if got.role != tt.wantRole {
				t.Errorf("role = %v, want %v", got.role, tt.wantRole)
			}
			if !slices.EqualFunc(got.dns, tt.wantDNS, func(a, b tunnel.DNSIntent) bool {
				return slices.Equal(a.Servers, b.Servers) && slices.Equal(a.MatchDomains, b.MatchDomains)
			}) {
				t.Errorf("dns = %+v, want %+v", got.dns, tt.wantDNS)
			}
			if !slices.Equal(got.warnings, tt.wantWarnings) {
				t.Errorf("warnings = %q, want %q", got.warnings, tt.wantWarnings)
			}
		})
	}
}
