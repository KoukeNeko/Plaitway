package macos

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

const (
	// Every key this package writes is State:/Network/Service/plaitway-<owner>-<n>/DNS.
	// The prefix is the marker Owned searches for.
	dnsKeyPrefix  = "State:/Network/Service/plaitway-"
	dnsKeySuffix  = "/DNS"
	dnsKeyPattern = dnsKeyPrefix + ".*" + dnsKeySuffix
	// dnsOwnerField holds the owner in the dictionary of every key.
	dnsOwnerField = "PlaitwayOwner"
	// A resolver key with many match domains misbehaves; 50 is NetBird's value.
	maxDomainsPerKey = 50
	// noKeysLine is what scutil's "list" prints when nothing matches.
	noKeysLine = "no keys."
)

var (
	ownerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	labelPattern = regexp.MustCompile(`^[A-Za-z0-9_]([A-Za-z0-9_-]{0,61}[A-Za-z0-9_])?$`)
	dnsKeyParts  = regexp.MustCompile(`^` + regexp.QuoteMeta(dnsKeyPrefix) + `(.+)-([0-9]+)` + regexp.QuoteMeta(dnsKeySuffix) + `$`)
	keyListLine  = regexp.MustCompile(`^subKey \[[0-9]+\] = (.+)$`)
)

// dnsKey is one dynamic store key to write: a resolver for a set of domains.
type dnsKey struct {
	Name    string
	Servers []string
	// Domains are the domains the resolver answers; "" is every domain.
	Domains []string
	Order   int
}

func dnsKeyName(owner string, n int) string {
	return dnsKeyPrefix + owner + "-" + strconv.Itoa(n) + dnsKeySuffix
}

// ownerOfKey returns the owner a key written by this package belongs to.
func ownerOfKey(key string) (owner string, ok bool) {
	m := dnsKeyParts.FindStringSubmatch(key)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// validateOwner rejects owners that could not be part of a key name or would
// need quoting in a scutil script. Scripts are line-oriented commands run as
// root, so everything that goes into one is checked.
func validateOwner(owner string) error {
	if !ownerPattern.MatchString(owner) {
		return fmt.Errorf("invalid DNS owner %q", owner)
	}
	return nil
}

// normalizeDomain returns the match domain scutil wants for d: lower case, no
// trailing dot, and "" for "." which stands for every domain. Domains come from
// profiles and from servers, so anything but letters, digits, hyphens, and
// underscores in dot-separated labels is rejected.
func normalizeDomain(d string) (string, error) {
	if d == "." {
		return "", nil
	}
	name := strings.ToLower(strings.TrimSuffix(d, "."))
	if name == "" || len(name) > 253 {
		return "", fmt.Errorf("invalid DNS domain %q", d)
	}
	for _, label := range strings.Split(name, ".") {
		if !labelPattern.MatchString(label) {
			return "", fmt.Errorf("invalid DNS domain %q", d)
		}
	}
	return name, nil
}

func normalizeServer(a netip.Addr) (string, error) {
	if !osnet.ValidDNSServer(a) {
		return "", fmt.Errorf("invalid DNS server %q", a)
	}
	return a.Unmap().String(), nil
}

// planDNSKeys validates entries and lays them out as keys of at most
// maxDomainsPerKey domains each, numbered from 0.
func planDNSKeys(owner string, entries []osnet.DNSEntry) ([]dnsKey, error) {
	var keys []dnsKey
	for i, entry := range entries {
		if len(entry.Servers) == 0 {
			return nil, fmt.Errorf("DNS entry %d has no servers", i)
		}
		if len(entry.MatchDomains) == 0 {
			return nil, fmt.Errorf("DNS entry %d has no match domains", i)
		}
		var servers, domains []string
		for _, s := range entry.Servers {
			server, err := normalizeServer(s)
			if err != nil {
				return nil, fmt.Errorf("DNS entry %d: %w", i, err)
			}
			servers = append(servers, server)
		}
		for _, d := range entry.MatchDomains {
			domain, err := normalizeDomain(d)
			if err != nil {
				return nil, fmt.Errorf("DNS entry %d: %w", i, err)
			}
			domains = append(domains, domain)
		}
		for chunk := range slices.Chunk(domains, maxDomainsPerKey) {
			keys = append(keys, dnsKey{Name: dnsKeyName(owner, len(keys)), Servers: servers, Domains: chunk, Order: entry.Order})
		}
	}
	return keys, nil
}

// applyScript is the scutil session that writes keys and then removes the keys
// of the same owner that are no longer wanted.
//
// SupplementalMatchDomains makes a resolver for those domains, "" for every
// domain; SupplementalMatchDomainsNoSearch keeps them out of the search list.
func applyScript(owner string, keys []dnsKey, removeKeys []string) string {
	var b strings.Builder
	for _, key := range keys {
		orders := make([]string, len(key.Domains))
		domains := slices.Clone(key.Domains)
		for i, d := range domains {
			orders[i] = strconv.Itoa(key.Order)
			if d == "" {
				domains[i] = `""`
			}
		}
		b.WriteString("d.init\n")
		fmt.Fprintf(&b, "d.add ServerAddresses * %s\n", strings.Join(key.Servers, " "))
		fmt.Fprintf(&b, "d.add SupplementalMatchDomains * %s\n", strings.Join(domains, " "))
		b.WriteString("d.add SupplementalMatchDomainsNoSearch # 1\n")
		fmt.Fprintf(&b, "d.add SupplementalMatchOrders * # %s\n", strings.Join(orders, " "))
		fmt.Fprintf(&b, "d.add %s %s\n", dnsOwnerField, owner)
		fmt.Fprintf(&b, "set %s\n", key.Name)
	}
	b.WriteString(removeScript(removeKeys))
	return b.String()
}

func removeScript(keys []string) string {
	var b strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&b, "remove %s\n", key)
	}
	b.WriteString("quit\n")
	return b.String()
}

// parseKeyList parses the output of scutil's "list" command: one
// "subKey [n] = key" line per key, or "no keys.".
func parseKeyList(out string) ([]string, error) {
	var keys []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == noKeysLine {
			continue
		}
		m := keyListLine.FindStringSubmatch(line)
		if m == nil {
			return nil, errors.New("unexpected scutil list output: " + strconv.Quote(line))
		}
		keys = append(keys, m[1])
	}
	return keys, nil
}
