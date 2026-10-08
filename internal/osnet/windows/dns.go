package windows

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

const (
	// Every rule key this package writes is Plaitway-<owner hash>-<generation>-<n>.
	// The prefix is the marker Owned searches for; the hash stands for the owner,
	// whatever characters its name has and however long it is.
	ruleKeyPrefix = "Plaitway-"
	ownerHashLen  = 16 // hex digits of the SHA-256 of the owner: 64 bits
	// maxOwnerLength bounds what goes into the Comment value of a rule.
	maxOwnerLength = 1024

	// catchAll is the namespace of the rule that answers everything.
	catchAll        = "."
	serverSeparator = ";"
	// ruleComment prefixes the Comment value; the owner and the order follow.
	// It is what a person sees in Get-DnsClientNrptRule.
	ruleComment = "Plaitway"

	// defaultRefreshPolicyAfterChange: see refreshPolicyAfterChange.
	defaultRefreshPolicyAfterChange = false
	// defaultRefreshTimeout is how long a change waits for that request. The
	// request is a machine-wide group policy refresh, which on a PC in a domain
	// can take long, and the Reconciler holds its lock while it waits.
	defaultRefreshTimeout = 5 * time.Second
)

// refreshPolicyAfterChange makes every change of the rules end with a request to
// the DNS Client to reread its policy (RefreshPolicyEx), which is a machine-wide
// group policy refresh.
//
// Off: TestRootDNSRegistryWriteWithoutPolicyRefresh, run elevated on Windows 11
// build 26300, showed that the DNS Client picks a rule up from the registry by
// itself, after 88 ms, and TestRootDNSRuleResolvesThroughTheSystemResolver after
// 23 ms. Only that build has been measured. If an older Windows or a Server
// edition does not pick rules up, set the default to true; the refresh code and
// systemCalls.refresh stay for that reason. It is a variable only so that the
// unit tests, which exercise the refresh code, can turn it on.
var refreshPolicyAfterChange = defaultRefreshPolicyAfterChange

// ErrGroupPolicyNRPT is returned by Apply when a group policy (or DirectAccess)
// delivers a name resolution policy to this PC. Windows then ignores the rules
// of the local table, so a rule that was written would be dead letters while
// the Reconciler reported DNS installed. Remove still works: the rules written
// before the policy arrived are taken out.
var ErrGroupPolicyNRPT = errors.New("DNS is not in effect: a group policy defines name resolution (NRPT) rules, and Windows applies those instead of the rules of local programs")

var (
	ruleKeyPattern = regexp.MustCompile(`^` + ruleKeyPrefix + `([0-9a-f]{` + strconv.Itoa(ownerHashLen) + `})-([0-9]+)-([0-9]+)$`)
	// commentPattern reads the Comment value back. The owner comes last because
	// it can contain anything.
	commentPattern = regexp.MustCompile(`(?s)^` + ruleComment + ` order=(-?[0-9]+) owner=(.*)$`)
	labelPattern   = regexp.MustCompile(`^[A-Za-z0-9_]([A-Za-z0-9_-]{0,61}[A-Za-z0-9_])?$`)
)

// rule is one NRPT rule as the registry holds it, without the values that are
// the same for every rule (see registryStore).
type rule struct {
	// Names are the namespaces: ".example.com" for a name and everything under
	// it, "example.com" for the name alone, "." for every name.
	Names   []string
	Servers []string
	Comment string
}

// ruleKey is a parsed rule key name.
type ruleKey struct {
	Name  string
	Owner string // the owner hash
	// Generation orders the rules an owner has had. A new set is written under
	// a new generation before the old one is removed, so that a resolver always
	// finds one complete set.
	Generation int
	Index      int
}

func newRuleKey(ownerHash string, generation, index int) ruleKey {
	return ruleKey{
		Name:       fmt.Sprintf("%s%s-%d-%d", ruleKeyPrefix, ownerHash, generation, index),
		Owner:      ownerHash,
		Generation: generation,
		Index:      index,
	}
}

// parseRuleKey reads a key name written by this package.
func parseRuleKey(name string) (ruleKey, bool) {
	m := ruleKeyPattern.FindStringSubmatch(name)
	if m == nil {
		return ruleKey{}, false
	}
	generation, err1 := strconv.Atoi(m[2])
	index, err2 := strconv.Atoi(m[3])
	if err1 != nil || err2 != nil {
		return ruleKey{}, false
	}
	return ruleKey{Name: name, Owner: m[1], Generation: generation, Index: index}, true
}

func ownerHash(owner string) string {
	sum := sha256.Sum256([]byte(owner))
	return hex.EncodeToString(sum[:])[:ownerHashLen]
}

// validateOwner rejects owners that cannot be stored, not owners with odd
// characters: those are hashed into the key name.
func validateOwner(owner string) error {
	if owner == "" || len(owner) > maxOwnerLength || !utf8.ValidString(owner) || strings.ContainsRune(owner, 0) {
		return fmt.Errorf("invalid DNS owner %q", owner)
	}
	return nil
}

// normalizeDomain returns the match domain for d: lower case, no trailing dot,
// "." for every domain. Domains come from profiles and from servers, so
// anything but letters, digits, hyphens and underscores in dot-separated labels
// is rejected.
func normalizeDomain(d string) (string, error) {
	if d == catchAll {
		return catchAll, nil
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

// namespacesOf writes the domains as NRPT namespaces. A domain means itself and
// everything below it, as on macOS, and the NRPT has a namespace for each half:
// the suffix ".example.com" and the exact name "example.com". "." is a
// namespace of its own.
func namespacesOf(domains []string) []string {
	var names []string
	for _, d := range domains {
		if d == catchAll {
			names = append(names, catchAll)
		} else {
			names = append(names, "."+d, d)
		}
	}
	return names
}

// domainsOf is the inverse of namespacesOf. An exact name that has its suffix
// beside it was written as the other half of that domain and adds nothing; one
// without is a domain of its own.
func domainsOf(names []string) []string {
	var domains []string
	for _, name := range names {
		domain := name
		switch {
		case name == catchAll:
		case strings.HasPrefix(name, "."):
			domain = name[1:]
		case slices.Contains(names, "."+name):
			continue
		}
		if !slices.Contains(domains, domain) {
			domains = append(domains, domain)
		}
	}
	return domains
}

func commentOf(owner string, order int) string {
	return fmt.Sprintf("%s order=%d owner=%s", ruleComment, order, owner)
}

// parseComment reads what commentOf wrote.
func parseComment(comment string) (owner string, order int, ok bool) {
	m := commentPattern.FindStringSubmatch(comment)
	if m == nil {
		return "", 0, false
	}
	order, err := strconv.Atoi(m[1])
	if err != nil {
		return "", 0, false
	}
	return m[2], order, true
}

// planRules validates the entries and turns each into one rule.
func planRules(owner string, entries []osnet.DNSEntry) ([]rule, error) {
	rules := make([]rule, 0, len(entries))
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
			if !slices.Contains(domains, domain) {
				domains = append(domains, domain)
			}
		}
		rules = append(rules, rule{Names: namespacesOf(domains), Servers: servers, Comment: commentOf(owner, entry.Order)})
	}
	return rules, nil
}

// entryOf reads a rule back as the entry that wrote it. ok is false for a rule
// that does not carry this package's comment or holds a server that is no
// address.
func entryOf(r rule) (entry osnet.DNSEntry, owner string, ok bool) {
	owner, order, ok := parseComment(r.Comment)
	if !ok {
		return osnet.DNSEntry{}, "", false
	}
	entry = osnet.DNSEntry{MatchDomains: domainsOf(r.Names), Order: order}
	for _, s := range r.Servers {
		addr, err := netip.ParseAddr(s)
		if err != nil {
			return osnet.DNSEntry{}, "", false
		}
		entry.Servers = append(entry.Servers, addr)
	}
	return entry, owner, true
}

// policyStore is where the NRPT rules live. The registry is the real one.
type policyStore interface {
	// ruleKeys lists the names of all subkeys, whoever wrote them.
	ruleKeys() ([]string, error)
	read(key string) (rule, error)
	// write creates the key or replaces it. The rule must become visible to the
	// DNS Client only when it is complete.
	write(key string, r rule) error
	// remove deletes the key; a key that is not there is not an error.
	remove(key string) error
}

// systemCalls are the calls of the DNS Client and of the registry's policy
// branch that the configurator makes beside the rules themselves.
type systemCalls struct {
	// refresh makes the DNS Client reread its policy. A failure is logged, not
	// returned: the rules are written, and a retry would find nothing to change.
	refresh func() error
	flush   func() error
	// groupPolicyRules lists the rules that a group policy delivers.
	groupPolicyRules func() ([]string, error)
}

// dnsConfigurator implements osnet.DNSConfigurator with NRPT rules.
type dnsConfigurator struct {
	store policyStore
	log   *slog.Logger
	sys   systemCalls
	// refreshTimeout bounds the wait for sys.refresh.
	refreshTimeout time.Duration
	// refreshing is set while a refresh is under way, so that a refresh that
	// hangs is not followed by a second one that hangs too.
	refreshing atomic.Bool
}

func newDNSConfigurator(store policyStore, log *slog.Logger, sys systemCalls) *dnsConfigurator {
	return &dnsConfigurator{store: store, log: log, sys: sys, refreshTimeout: defaultRefreshTimeout}
}

// Apply replaces everything applied for owner. The new rules are written under
// a new generation, then the old ones are removed, so that a resolver finds the
// old set or the new one and never none. It does nothing when the owner's rules
// are already exactly the wanted ones. It fails with ErrGroupPolicyNRPT when a
// group policy makes Windows ignore local rules.
func (d *dnsConfigurator) Apply(owner string, entries []osnet.DNSEntry) error {
	if err := validateOwner(owner); err != nil {
		return err
	}
	if len(entries) == 0 {
		return d.Remove(owner)
	}
	rules, err := planRules(owner, entries)
	if err != nil {
		return fmt.Errorf("apply DNS for %s: %w", owner, err)
	}
	if err := d.checkNoGroupPolicy(); err != nil {
		return fmt.Errorf("apply DNS for %s: %w", owner, err)
	}
	hash := ownerHash(owner)
	existing, err := d.keysOf(hash)
	if err != nil {
		return fmt.Errorf("apply DNS for %s: %w", owner, err)
	}
	if d.alreadyApplied(existing, rules) {
		return nil
	}
	written, err := d.writeRules(hash, nextGeneration(existing), rules)
	if err != nil {
		return fmt.Errorf("apply DNS for %s: %w", owner, errors.Join(err, d.removeKeys(written)))
	}
	if err := d.removeKeys(existing); err != nil {
		return fmt.Errorf("apply DNS for %s: remove the previous rules: %w", owner, err)
	}
	d.refreshPolicy()
	return nil
}

// Remove deletes everything applied for owner. It also accepts a key returned
// by Owned, which stands for the owner that key belongs to: the Reconciler's
// sweep of leftovers passes the keys it found.
func (d *dnsConfigurator) Remove(ownerOrKey string) error {
	hash, err := ownerHashOf(ownerOrKey)
	if err != nil {
		return err
	}
	existing, err := d.keysOf(hash)
	if err != nil {
		return fmt.Errorf("remove DNS of %s: %w", ownerOrKey, err)
	}
	if len(existing) == 0 {
		return nil
	}
	err = d.removeKeys(existing)
	d.refreshPolicy()
	if err != nil {
		return fmt.Errorf("remove DNS of %s: %w", ownerOrKey, err)
	}
	return nil
}

// ownerHashOf is the owner hash that a name stands for: a key written by this
// package stands for the owner it belongs to, anything else is an owner.
func ownerHashOf(ownerOrKey string) (string, error) {
	if key, ok := parseRuleKey(ownerOrKey); ok {
		return key.Owner, nil
	}
	if err := validateOwner(ownerOrKey); err != nil {
		return "", err
	}
	return ownerHash(ownerOrKey), nil
}

func (d *dnsConfigurator) Owned() ([]string, error) {
	keys, err := d.parsedKeys()
	if err != nil {
		return nil, err
	}
	names := make([]string, len(keys))
	for i, key := range keys {
		names[i] = key.Name
	}
	return names, nil
}

func (d *dnsConfigurator) Flush() error { return d.sys.flush() }

// checkNoGroupPolicy returns ErrGroupPolicyNRPT when the policy branch of the
// registry holds a rule. If the branch cannot be read the rules are written
// anyway: a PC that cannot show its policy is more likely to be one without,
// and a refusal would take DNS away from every PC with a locked-down registry.
func (d *dnsConfigurator) checkNoGroupPolicy() error {
	rules, err := d.sys.groupPolicyRules()
	if err != nil {
		d.log.Warn("could not tell whether a group policy defines name resolution rules; writing the DNS rules anyway", "err", err)
		return nil
	}
	if len(rules) > 0 {
		d.log.Warn("a group policy defines name resolution rules, which Windows applies instead of the local ones", "rules", len(rules))
		return fmt.Errorf("%w (rules defined: %d)", ErrGroupPolicyNRPT, len(rules))
	}
	return nil
}

// parsedKeys lists the keys written by this package, sorted by owner, generation
// and index.
func (d *dnsConfigurator) parsedKeys() ([]ruleKey, error) {
	names, err := d.store.ruleKeys()
	if err != nil {
		return nil, fmt.Errorf("list DNS rules: %w", err)
	}
	var keys []ruleKey
	for _, name := range names {
		if key, ok := parseRuleKey(name); ok {
			keys = append(keys, key)
		}
	}
	slices.SortFunc(keys, compareRuleKeys)
	return keys, nil
}

func compareRuleKeys(a, b ruleKey) int {
	return cmp.Or(
		strings.Compare(a.Owner, b.Owner),
		cmp.Compare(a.Generation, b.Generation),
		cmp.Compare(a.Index, b.Index),
		strings.Compare(a.Name, b.Name), // numbers written with leading zeros
	)
}

func (d *dnsConfigurator) keysOf(hash string) ([]ruleKey, error) {
	keys, err := d.parsedKeys()
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(keys, func(k ruleKey) bool { return k.Owner != hash }), nil
}

func nextGeneration(existing []ruleKey) int {
	next := 0
	for _, key := range existing {
		next = max(next, key.Generation+1)
	}
	return next
}

// alreadyApplied reports whether the owner has exactly one generation of rules
// and they are the wanted ones.
func (d *dnsConfigurator) alreadyApplied(existing []ruleKey, wanted []rule) bool {
	if len(existing) != len(wanted) {
		return false
	}
	for i, key := range existing {
		if key.Generation != existing[0].Generation || key.Index != i {
			return false
		}
		have, err := d.store.read(key.Name)
		if err != nil || !sameRule(have, wanted[i]) {
			return false
		}
	}
	return true
}

func sameRule(a, b rule) bool {
	return a.Comment == b.Comment && slices.Equal(a.Servers, b.Servers) &&
		slices.Equal(slices.Sorted(slices.Values(a.Names)), slices.Sorted(slices.Values(b.Names)))
}

// writeRules writes the rules under the generation. It returns the keys it
// wrote, also when it fails half way, so that the caller can take them out.
func (d *dnsConfigurator) writeRules(hash string, generation int, rules []rule) ([]ruleKey, error) {
	var written []ruleKey
	for i, r := range rules {
		key := newRuleKey(hash, generation, i)
		// The key is listed before the write, as a write that fails may have
		// created it.
		written = append(written, key)
		if err := d.store.write(key.Name, r); err != nil {
			return written, fmt.Errorf("write DNS rule %s: %w", key.Name, err)
		}
	}
	return written, nil
}

func (d *dnsConfigurator) removeKeys(keys []ruleKey) error {
	var errs []error
	for _, key := range keys {
		if err := d.store.remove(key.Name); err != nil {
			errs = append(errs, fmt.Errorf("remove DNS rule %s: %w", key.Name, err))
		}
	}
	return errors.Join(errs...)
}

// refreshPolicy asks the DNS Client to reread its policy and waits for the
// answer for refreshTimeout at most. The call runs in a goroutine of its own:
// it cannot be interrupted, and the caller must not be held up by it. The
// rules are written already, so a request that is slow or fails costs only the
// promptness of their pickup.
func (d *dnsConfigurator) refreshPolicy() {
	if !refreshPolicyAfterChange {
		return
	}
	if !d.refreshing.CompareAndSwap(false, true) {
		d.log.Warn("the previous request to the DNS Client to reread its policy has not returned; not asking again")
		return
	}
	done := make(chan struct{})
	go func() {
		started := time.Now()
		err := d.sys.refresh()
		d.refreshing.Store(false)
		close(done)
		if err != nil {
			d.log.Warn("the DNS Client was not told to reread its policy; it may take a while to notice the rules", "err", err)
		} else if elapsed := time.Since(started); elapsed > d.refreshTimeout {
			d.log.Warn("the DNS Client has reread its policy, late", "after", elapsed)
		}
	}()
	timer := time.NewTimer(d.refreshTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		d.log.Warn("the DNS Client has not reread its policy yet; going on, the rules are written", "waited", d.refreshTimeout)
	}
}
