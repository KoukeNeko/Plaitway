package windows

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// memoryStore is a policyStore in memory that records what was done to it, in
// order.
type memoryStore struct {
	rules map[string]rule
	ops   []string
	// failWrite and failRemove make the operation on the named key fail.
	failWrite  map[string]error
	failRemove map[string]error
	failList   error
}

func newMemoryStore() *memoryStore {
	return &memoryStore{rules: map[string]rule{}, failWrite: map[string]error{}, failRemove: map[string]error{}}
}

func (s *memoryStore) ruleKeys() ([]string, error) {
	if s.failList != nil {
		return nil, s.failList
	}
	keys := make([]string, 0, len(s.rules))
	for key := range s.rules {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys, nil
}

func (s *memoryStore) read(key string) (rule, error) {
	r, ok := s.rules[key]
	if !ok {
		return rule{}, fmt.Errorf("no rule %s", key)
	}
	return r, nil
}

func (s *memoryStore) write(key string, r rule) error {
	s.ops = append(s.ops, "write "+key)
	if err := s.failWrite[key]; err != nil {
		return err
	}
	s.rules[key] = r
	return nil
}

func (s *memoryStore) remove(key string) error {
	s.ops = append(s.ops, "remove "+key)
	if err := s.failRemove[key]; err != nil {
		return err
	}
	delete(s.rules, key)
	return nil
}

func (s *memoryStore) mutations() []string { return s.ops }

// quietSystem is the DNS Client of a PC that has no group policy and takes every
// request at once.
func quietSystem() systemCalls {
	return systemCalls{
		refresh:          func() error { return nil },
		flush:            func() error { return nil },
		groupPolicyRules: func() ([]string, error) { return nil, nil },
	}
}

// syncBuffer is a log destination that the goroutine of a refresh and the test
// can use at the same time.
type syncBuffer struct {
	mu   sync.Mutex
	text strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.String()
}

type dnsFixture struct {
	store     *memoryStore
	dns       *dnsConfigurator
	logs      *syncBuffer
	refreshes int
	flushes   int
	refreshOK error
	// policyRules and policyErr are what the policy branch of the registry holds.
	policyRules []string
	policyErr   error
}

func newDNSFixture() *dnsFixture {
	f := &dnsFixture{store: newMemoryStore(), logs: &syncBuffer{}}
	f.dns = newDNSConfigurator(f.store, slog.New(slog.NewTextHandler(f.logs, nil)), systemCalls{
		refresh:          func() error { f.refreshes++; return f.refreshOK },
		flush:            func() error { f.flushes++; return nil },
		groupPolicyRules: func() ([]string, error) { return f.policyRules, f.policyErr },
	})
	return f
}

func entry(order int, servers []string, domains ...string) osnet.DNSEntry {
	e := osnet.DNSEntry{Order: order, MatchDomains: domains}
	for _, s := range servers {
		e.Servers = append(e.Servers, ip(s))
	}
	return e
}

func TestNormalizeDomain(t *testing.T) {
	tests := []struct {
		in   string
		want string
		bad  bool
	}{
		{"corp.example.com", "corp.example.com", false},
		{"Corp.Example.COM.", "corp.example.com", false},
		{".", ".", false},
		{"under_score.example", "under_score.example", false},
		{"xn--fiqs8s.example", "xn--fiqs8s.example", false},
		{"", "", true},
		{"..", "", true},
		{"a..b", "", true},
		{"-lead.example", "", true},
		{"trail-.example", "", true},
		{"space in.example", "", true},
		{"wild*.example", "", true},
		{"漢字.example", "", true},
		{strings.Repeat("a", 64) + ".example", "", true},
		{strings.Repeat("a.", 130) + "x", "", true},
	}
	for _, tt := range tests {
		got, err := normalizeDomain(tt.in)
		if (err != nil) != tt.bad || (!tt.bad && got != tt.want) {
			t.Errorf("normalizeDomain(%q) = %q, %v; want %q, bad=%v", tt.in, got, err, tt.want, tt.bad)
		}
	}
}

func TestNamespacesRoundTrip(t *testing.T) {
	tests := [][]string{
		{"."},
		{"corp.example.com"},
		{"corp.example.com", "example.org", "a.b.c.d"},
		{".", "corp.example.com"},
	}
	for _, domains := range tests {
		names := namespacesOf(domains)
		if got := domainsOf(names); !slices.Equal(got, domains) {
			t.Errorf("%v -> %v -> %v", domains, names, got)
		}
	}
	// A domain stands for itself and everything below it, as on macOS: the
	// suffix namespace and the exact name.
	if got := namespacesOf([]string{"corp.example.com"}); !slices.Equal(got, []string{".corp.example.com", "corp.example.com"}) {
		t.Errorf("namespaces = %v", got)
	}
	if got := namespacesOf([]string{"."}); !slices.Equal(got, []string{"."}) {
		t.Errorf("catch-all = %v", got)
	}
	// Rules somebody else wrote: an exact name alone is a domain of its own, a
	// suffix alone is its domain, and duplicates collapse.
	if got := domainsOf([]string{"host.example", ".corp.example", ".corp.example"}); !slices.Equal(got, []string{"host.example", "corp.example"}) {
		t.Errorf("foreign names = %v", got)
	}
}

func TestCommentRoundTrip(t *testing.T) {
	owners := []string{
		"profile-1",
		"a very long owner " + strings.Repeat("x", 900),
		"with \"quotes\", back\\slashes and ; semicolons",
		"owner=with order=7 inside",
		"繁體中文的擁有者",
		"new\nline\r\nand\ttab",
		" leading and trailing spaces ",
		"emoji 🛰️",
	}
	for _, order := range []int{0, 5, -3, 1 << 30} {
		for _, owner := range owners {
			gotOwner, gotOrder, ok := parseComment(commentOf(owner, order))
			if !ok || gotOwner != owner || gotOrder != order {
				t.Errorf("owner %q order %d came back as %q, %d, %v", owner, order, gotOwner, gotOrder, ok)
			}
		}
	}
	for _, comment := range []string{"", "Plaitway", "Plaitway order=x owner=y", "somebody else's rule", "Plaitway owner=a order=1"} {
		if _, _, ok := parseComment(comment); ok {
			t.Errorf("%q was taken for ours", comment)
		}
	}
}

func TestRuleKeys(t *testing.T) {
	hash := ownerHash("profile-1")
	if len(hash) != ownerHashLen || strings.Trim(hash, "0123456789abcdef") != "" {
		t.Fatalf("hash %q", hash)
	}
	if hash != ownerHash("profile-1") || hash == ownerHash("profile-2") {
		t.Error("the hash is not stable, or not distinct")
	}
	key := newRuleKey(hash, 3, 12)
	if key.Name != "Plaitway-"+hash+"-3-12" {
		t.Errorf("name = %q", key.Name)
	}
	parsed, ok := parseRuleKey(key.Name)
	if !ok || parsed != key {
		t.Errorf("parsed %+v, %v; want %+v", parsed, ok, key)
	}
	for _, name := range []string{
		"", "Plaitway-", "Plaitway-" + hash, "Plaitway-" + hash + "-1", "Plaitway-" + hash[:8] + "-1-2",
		"Plaitway-" + strings.ToUpper(hash) + "-1-2", "plaitway-" + hash + "-1-2", "Tailscale-{GUID}",
		"{4B1E9A26-6A4B-4F5B-9F6D-0A3C60D3D1AE}", "Plaitway-" + hash + "-1-2-3", "Plaitway-" + hash + "--1-2",
		"Plaitway-" + hash + "-99999999999999999999-2",
	} {
		if _, ok := parseRuleKey(name); ok {
			t.Errorf("%q was taken for one of ours", name)
		}
	}
}

func TestValidateOwner(t *testing.T) {
	for owner, ok := range map[string]bool{
		"profile-1": true, "with spaces and 漢字": true, strings.Repeat("o", maxOwnerLength): true,
		"": false, strings.Repeat("o", maxOwnerLength+1): false, "nul\x00byte": false, "\xff\xfe": false,
	} {
		if err := validateOwner(owner); (err == nil) != ok {
			t.Errorf("validateOwner(%.20q) = %v, want ok=%v", owner, err, ok)
		}
	}
}

func TestPlanRules(t *testing.T) {
	rules, err := planRules("profile-1", []osnet.DNSEntry{
		entry(4, []string{"10.6.0.1", "fd00::1"}, "Corp.Example.com.", "corp.example.com", "example.org"),
		entry(0, []string{"10.6.0.2"}, "."),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []rule{
		{Names: []string{".corp.example.com", "corp.example.com", ".example.org", "example.org"}, Servers: []string{"10.6.0.1", "fd00::1"}, Comment: commentOf("profile-1", 4)},
		{Names: []string{"."}, Servers: []string{"10.6.0.2"}, Comment: commentOf("profile-1", 0)},
	}
	if len(rules) != 2 || !sameRule(rules[0], want[0]) || !slices.Equal(rules[0].Names, want[0].Names) || !sameRule(rules[1], want[1]) {
		t.Fatalf("rules = %+v\nwant   %+v", rules, want)
	}

	for name, entries := range map[string][]osnet.DNSEntry{
		"no servers":         {entry(0, nil, "example.com")},
		"no domains":         {entry(0, []string{"10.6.0.1"})},
		"unspecified server": {entry(0, []string{"0.0.0.0"}, "example.com")},
		"multicast server":   {entry(0, []string{"224.0.0.251"}, "example.com")},
		"server with a zone": {{Servers: []netip.Addr{ip("fe80::1").WithZone("Ethernet")}, MatchDomains: []string{"example.com"}}},
		"invalid domain":     {entry(0, []string{"10.6.0.1"}, "bad domain")},
		"one bad among good": {entry(0, []string{"10.6.0.1"}, "example.com"), entry(0, []string{"10.6.0.1"}, "a..b")},
	} {
		if _, err := planRules("o", entries); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestEntryRoundTrip(t *testing.T) {
	owner := `owner "with" odd; characters`
	in := []osnet.DNSEntry{
		entry(4, []string{"10.6.0.1", "fd00::1"}, "corp.example.com", "example.org"),
		entry(-1, []string{"2001:db8::53"}, "."),
	}
	rules, err := planRules(owner, in)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range rules {
		got, gotOwner, ok := entryOf(r)
		if !ok || gotOwner != owner {
			t.Fatalf("rule %d: owner %q, %v", i, gotOwner, ok)
		}
		if !entriesEqual(got, in[i]) {
			t.Errorf("rule %d came back as %+v, want %+v", i, got, in[i])
		}
	}
	if _, _, ok := entryOf(rule{Names: []string{"."}, Servers: []string{"10.0.0.1"}, Comment: "Tailscale"}); ok {
		t.Error("a foreign rule was read as ours")
	}
	if _, _, ok := entryOf(rule{Names: []string{"."}, Servers: []string{"not an address"}, Comment: commentOf("o", 0)}); ok {
		t.Error("a rule with a bad server was read")
	}
}

func entriesEqual(a, b osnet.DNSEntry) bool {
	return a.Order == b.Order && slices.Equal(a.Servers, b.Servers) && slices.Equal(a.MatchDomains, b.MatchDomains)
}

func TestApplyWritesRulesAndRefreshes(t *testing.T) {
	f := newDNSFixture()
	err := f.dns.Apply("profile-1", []osnet.DNSEntry{
		entry(0, []string{"10.6.0.1"}, "corp.example.com"),
		entry(1, []string{"10.6.0.2"}, "."),
	})
	if err != nil {
		t.Fatal(err)
	}
	hash := ownerHash("profile-1")
	keys, _ := f.dns.Owned()
	wantKeys := []string{"Plaitway-" + hash + "-0-0", "Plaitway-" + hash + "-0-1"}
	if !slices.Equal(keys, wantKeys) {
		t.Fatalf("Owned = %v, want %v", keys, wantKeys)
	}
	if f.refreshes != 1 {
		t.Errorf("the DNS Client was told %d times, want once", f.refreshes)
	}
	if got := f.store.rules[wantKeys[1]]; !slices.Equal(got.Names, []string{"."}) || !slices.Equal(got.Servers, []string{"10.6.0.2"}) {
		t.Errorf("catch-all rule = %+v", got)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	f := newDNSFixture()
	entries := []osnet.DNSEntry{entry(0, []string{"10.6.0.1", "fd00::1"}, "corp.example.com", "example.org")}
	for range 3 {
		if err := f.dns.Apply("profile-1", entries); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.store.mutations()) != 1 || f.refreshes != 1 {
		t.Errorf("repeating the same Apply changed the store again: %v, %d refreshes", f.store.mutations(), f.refreshes)
	}
	// A different order of servers is a different resolver order: a change.
	reordered := []osnet.DNSEntry{entry(0, []string{"fd00::1", "10.6.0.1"}, "corp.example.com", "example.org")}
	if err := f.dns.Apply("profile-1", reordered); err != nil {
		t.Fatal(err)
	}
	if len(f.store.mutations()) == 1 {
		t.Error("another server order was taken for the same rules")
	}
}

// Apply writes the new rules before it removes the old ones, so that a resolver
// finds a complete set at every moment.
func TestApplyAddsBeforeItRemoves(t *testing.T) {
	f := newDNSFixture()
	if err := f.dns.Apply("profile-1", []osnet.DNSEntry{entry(0, []string{"10.6.0.1"}, "corp.example.com"), entry(1, []string{"10.6.0.2"}, "other.example")}); err != nil {
		t.Fatal(err)
	}
	f.store.ops = nil
	if err := f.dns.Apply("profile-1", []osnet.DNSEntry{entry(0, []string{"10.7.0.1"}, "corp.example.com")}); err != nil {
		t.Fatal(err)
	}
	hash := ownerHash("profile-1")
	want := []string{
		"write Plaitway-" + hash + "-1-0",
		"remove Plaitway-" + hash + "-0-0",
		"remove Plaitway-" + hash + "-0-1",
	}
	if !slices.Equal(f.store.ops, want) {
		t.Errorf("operations = %v\nwant         %v", f.store.ops, want)
	}
	keys, _ := f.dns.Owned()
	if !slices.Equal(keys, []string{"Plaitway-" + hash + "-1-0"}) {
		t.Errorf("Owned = %v", keys)
	}
	if f.refreshes != 2 {
		t.Errorf("refreshes = %d, want one per change", f.refreshes)
	}
}

func TestApplyKeepsTheOldRulesWhenTheNewOnesCannotBeWritten(t *testing.T) {
	f := newDNSFixture()
	first := []osnet.DNSEntry{entry(0, []string{"10.6.0.1"}, "corp.example.com")}
	if err := f.dns.Apply("profile-1", first); err != nil {
		t.Fatal(err)
	}
	hash := ownerHash("profile-1")
	boom := errors.New("access denied")
	f.store.failWrite["Plaitway-"+hash+"-1-1"] = boom
	err := f.dns.Apply("profile-1", []osnet.DNSEntry{entry(0, []string{"10.7.0.1"}, "a.example"), entry(1, []string{"10.7.0.2"}, "b.example")})
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v", err)
	}
	keys, _ := f.dns.Owned()
	if !slices.Equal(keys, []string{"Plaitway-" + hash + "-0-0"}) {
		t.Errorf("after the failure Owned = %v: the old rule must stay and the half-written set must go", keys)
	}
	if got := f.store.rules["Plaitway-"+hash+"-0-0"]; !slices.Equal(got.Servers, []string{"10.6.0.1"}) {
		t.Errorf("the old rule changed: %+v", got)
	}
	// And a retry after the cause is gone succeeds.
	delete(f.store.failWrite, "Plaitway-"+hash+"-1-1")
	if err := f.dns.Apply("profile-1", []osnet.DNSEntry{entry(0, []string{"10.7.0.1"}, "a.example")}); err != nil {
		t.Fatal(err)
	}
}

func TestApplyReportsAFailedRemovalOfTheOldRules(t *testing.T) {
	f := newDNSFixture()
	if err := f.dns.Apply("profile-1", []osnet.DNSEntry{entry(0, []string{"10.6.0.1"}, "corp.example.com")}); err != nil {
		t.Fatal(err)
	}
	hash := ownerHash("profile-1")
	boom := errors.New("sharing violation")
	f.store.failRemove["Plaitway-"+hash+"-0-0"] = boom
	next := []osnet.DNSEntry{entry(0, []string{"10.7.0.1"}, "corp.example.com")}
	if err := f.dns.Apply("profile-1", next); !errors.Is(err, boom) {
		t.Fatalf("error = %v", err)
	}
	// Two generations are left. The next Apply sees that and cleans up, even
	// though the new rules are already the wanted ones.
	delete(f.store.failRemove, "Plaitway-"+hash+"-0-0")
	if err := f.dns.Apply("profile-1", next); err != nil {
		t.Fatal(err)
	}
	keys, _ := f.dns.Owned()
	if !slices.Equal(keys, []string{"Plaitway-" + hash + "-2-0"}) {
		t.Errorf("Owned = %v, want one rule of generation 2", keys)
	}
}

func TestApplyWithNoEntriesRemoves(t *testing.T) {
	f := newDNSFixture()
	if err := f.dns.Apply("profile-1", []osnet.DNSEntry{entry(0, []string{"10.6.0.1"}, "corp.example.com")}); err != nil {
		t.Fatal(err)
	}
	if err := f.dns.Apply("profile-1", nil); err != nil {
		t.Fatal(err)
	}
	if keys, _ := f.dns.Owned(); len(keys) != 0 {
		t.Errorf("Owned = %v", keys)
	}
	if err := f.dns.Apply("never-applied", []osnet.DNSEntry{}); err != nil {
		t.Errorf("removing nothing: %v", err)
	}
}

func TestApplyRejectsWhatCannotBeWritten(t *testing.T) {
	f := newDNSFixture()
	if err := f.dns.Apply("", []osnet.DNSEntry{entry(0, []string{"10.6.0.1"}, "a.example")}); err == nil {
		t.Error("an empty owner was accepted")
	}
	if err := f.dns.Apply("o", []osnet.DNSEntry{entry(0, nil, "a.example")}); err == nil {
		t.Error("an entry without servers was accepted")
	}
	if len(f.store.ops) != 0 {
		t.Errorf("a rejected Apply touched the store: %v", f.store.ops)
	}
}

func TestOwnersAreIndependent(t *testing.T) {
	f := newDNSFixture()
	for _, owner := range []string{"a", "b"} {
		if err := f.dns.Apply(owner, []osnet.DNSEntry{entry(0, []string{"10.6.0.1"}, owner+".example")}); err != nil {
			t.Fatal(err)
		}
	}
	// Somebody else's rules are never listed and never removed.
	foreign := rule{Names: []string{".contoso.com"}, Servers: []string{"10.1.1.1"}, Comment: "DirectAccess"}
	f.store.rules["{4B1E9A26-6A4B-4F5B-9F6D-0A3C60D3D1AE}"] = foreign
	f.store.rules["Tailscale-Fallback"] = foreign

	if err := f.dns.Remove("a"); err != nil {
		t.Fatal(err)
	}
	keys, _ := f.dns.Owned()
	if !slices.Equal(keys, []string{"Plaitway-" + ownerHash("b") + "-0-0"}) {
		t.Errorf("Owned = %v", keys)
	}
	if err := f.dns.Apply("b", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.store.rules) != 2 {
		t.Errorf("foreign rules were touched: %v", f.store.rules)
	}
	if err := f.dns.Remove("unknown owner"); err != nil {
		t.Errorf("removing nothing: %v", err)
	}
}

func TestOddOwnersGetSafeKeys(t *testing.T) {
	f := newDNSFixture()
	owners := []string{
		strings.Repeat("very long owner name ", 40),
		`back\slash and "quotes"`,
		"繁體中文",
		"new\nline",
		"Plaitway-0123456789abcdef-0-0", // looks like a key, and is a key: it means its owner
	}
	for _, owner := range owners[:4] {
		if err := f.dns.Apply(owner, []osnet.DNSEntry{entry(0, []string{"10.6.0.1"}, "corp.example.com")}); err != nil {
			t.Fatalf("owner %q: %v", owner, err)
		}
	}
	keys, err := f.dns.Owned()
	if err != nil || len(keys) != 4 {
		t.Fatalf("Owned = %v, %v", keys, err)
	}
	for _, key := range keys {
		if len(key) > 255 || strings.ContainsAny(key, `\/`) {
			t.Errorf("key %q cannot be a registry key name", key)
		}
	}
	for _, owner := range owners[:4] {
		if err := f.dns.Remove(owner); err != nil {
			t.Fatal(err)
		}
	}
	if keys, _ := f.dns.Owned(); len(keys) != 0 {
		t.Errorf("left over: %v", keys)
	}
}

// The process forgets everything; the rules are found again by their marker
// and removed, by key and by owner.
func TestCrashRecovery(t *testing.T) {
	f := newDNSFixture()
	if err := f.dns.Apply("profile-1", []osnet.DNSEntry{entry(0, []string{"10.6.0.1"}, "a.example"), entry(1, []string{"10.6.0.2"}, ".")}); err != nil {
		t.Fatal(err)
	}
	if err := f.dns.Apply("profile-2", []osnet.DNSEntry{entry(0, []string{"10.7.0.1"}, "b.example")}); err != nil {
		t.Fatal(err)
	}

	restarted := newDNSConfigurator(f.store, discardLog(), quietSystem())
	keys, err := restarted.Owned()
	if err != nil || len(keys) != 3 {
		t.Fatalf("Owned after the restart = %v, %v", keys, err)
	}
	for _, key := range keys {
		if err := restarted.Remove(key); err != nil {
			t.Fatalf("Remove(%s): %v", key, err)
		}
	}
	if left, _ := restarted.Owned(); len(left) != 0 || len(f.store.rules) != 0 {
		t.Errorf("left over: %v %v", left, f.store.rules)
	}
}

func TestRemoveByKeyRemovesEveryRuleOfTheOwner(t *testing.T) {
	f := newDNSFixture()
	if err := f.dns.Apply("profile-1", []osnet.DNSEntry{entry(0, []string{"10.6.0.1"}, "a.example"), entry(1, []string{"10.6.0.2"}, "b.example")}); err != nil {
		t.Fatal(err)
	}
	keys, _ := f.dns.Owned()
	if err := f.dns.Remove(keys[1]); err != nil {
		t.Fatal(err)
	}
	if left, _ := f.dns.Owned(); len(left) != 0 {
		t.Errorf("Remove of one key left %v", left)
	}
	// The sweep goes on with the other keys it listed before; they are gone.
	if err := f.dns.Remove(keys[0]); err != nil {
		t.Errorf("removing a key that is gone: %v", err)
	}
}

func TestRemoveReportsFailuresAndStillRefreshes(t *testing.T) {
	f := newDNSFixture()
	if err := f.dns.Apply("profile-1", []osnet.DNSEntry{entry(0, []string{"10.6.0.1"}, "a.example"), entry(1, []string{"10.6.0.2"}, "b.example")}); err != nil {
		t.Fatal(err)
	}
	hash := ownerHash("profile-1")
	boom := errors.New("access denied")
	f.store.failRemove["Plaitway-"+hash+"-0-0"] = boom
	before := f.refreshes
	if err := f.dns.Remove("profile-1"); !errors.Is(err, boom) {
		t.Fatalf("error = %v", err)
	}
	if left, _ := f.dns.Owned(); !slices.Equal(left, []string{"Plaitway-" + hash + "-0-0"}) {
		t.Errorf("the other rule must be removed anyway: %v", left)
	}
	if f.refreshes != before+1 {
		t.Error("the DNS Client was not told about the rules that did go")
	}
}

func TestOwnedReportsStoreErrors(t *testing.T) {
	f := newDNSFixture()
	boom := errors.New("registry unavailable")
	f.store.failList = boom
	if _, err := f.dns.Owned(); !errors.Is(err, boom) {
		t.Errorf("Owned: %v", err)
	}
	if err := f.dns.Apply("o", []osnet.DNSEntry{entry(0, []string{"10.6.0.1"}, "a.example")}); !errors.Is(err, boom) {
		t.Errorf("Apply: %v", err)
	}
	if err := f.dns.Remove("o"); !errors.Is(err, boom) {
		t.Errorf("Remove: %v", err)
	}
}

// A failed refresh is logged and does not fail the Apply: the rules are written,
// and a retry would find nothing to do.
func TestRefreshFailureDoesNotFailApply(t *testing.T) {
	f := newDNSFixture()
	f.refreshOK = errors.New("RefreshPolicyEx: access denied")
	if err := f.dns.Apply("o", []osnet.DNSEntry{entry(0, []string{"10.6.0.1"}, "a.example")}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if f.refreshes != 1 {
		t.Errorf("refreshes = %d", f.refreshes)
	}
	if err := f.dns.Remove("o"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
}

func TestFlush(t *testing.T) {
	f := newDNSFixture()
	if err := f.dns.Flush(); err != nil || f.flushes != 1 {
		t.Errorf("Flush = %v, flushes = %d", err, f.flushes)
	}
	boom := errors.New("DnsFlushResolverCache: access denied")
	f.dns.sys.flush = func() error { return boom }
	if err := f.dns.Flush(); !errors.Is(err, boom) {
		t.Errorf("Flush error = %v", err)
	}
}

// A group policy that delivers name resolution rules makes Windows ignore the
// local table. Apply says so instead of writing rules that do nothing, and
// stays quiet about nothing: the same Apply again is refused too.
func TestApplyRefusesWhileAGroupPolicyDefinesRules(t *testing.T) {
	f := newDNSFixture()
	first := []osnet.DNSEntry{entry(0, []string{"10.6.0.1"}, "corp.example.com")}
	if err := f.dns.Apply("o", first); err != nil {
		t.Fatal(err)
	}
	f.store.ops = nil
	refreshesBefore := f.refreshes

	f.policyRules = []string{"{4B1E9A26-6A4B-4F5B-9F6D-0A3C60D3D1AE}", "DirectAccess-NRPT"}
	for name, entries := range map[string][]osnet.DNSEntry{
		"new rules":      {entry(0, []string{"10.7.0.1"}, "other.example")},
		"the same rules": first,
		"a catch-all":    {entry(0, []string{"10.7.0.1"}, ".")},
	} {
		err := f.dns.Apply("o", entries)
		if !errors.Is(err, ErrGroupPolicyNRPT) {
			t.Errorf("%s: Apply = %v, want ErrGroupPolicyNRPT", name, err)
		}
		if err != nil && !strings.Contains(err.Error(), "rules defined: 2") {
			t.Errorf("%s: %q does not say how many rules the policy holds", name, err)
		}
	}
	if len(f.store.ops) != 0 || f.refreshes != refreshesBefore {
		t.Errorf("a refused Apply touched the machine: %v, %d refreshes", f.store.ops, f.refreshes-refreshesBefore)
	}
	if !strings.Contains(f.logs.String(), "group policy") {
		t.Errorf("the refusal is not logged: %s", f.logs)
	}
}

// Removal is the way out of a PC that got a policy after the rules were
// written, and it must not depend on the policy.
func TestRemoveWorksWhileAGroupPolicyDefinesRules(t *testing.T) {
	f := newDNSFixture()
	if err := f.dns.Apply("o", []osnet.DNSEntry{entry(0, []string{"10.6.0.1"}, ".")}); err != nil {
		t.Fatal(err)
	}
	if err := f.dns.Apply("p", []osnet.DNSEntry{entry(0, []string{"10.6.0.1"}, "a.example")}); err != nil {
		t.Fatal(err)
	}
	f.policyRules = []string{"{4B1E9A26-6A4B-4F5B-9F6D-0A3C60D3D1AE}"}

	if err := f.dns.Remove("o"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := f.dns.Apply("p", nil); err != nil {
		t.Fatalf("Apply with no entries: %v", err)
	}
	if keys, _ := f.dns.Owned(); len(keys) != 0 {
		t.Errorf("left over: %v", keys)
	}
}

func TestApplyGoesOnWhenThePolicyBranchCannotBeRead(t *testing.T) {
	f := newDNSFixture()
	f.policyErr = errors.New("open the policy branch: access denied")
	if err := f.dns.Apply("o", []osnet.DNSEntry{entry(0, []string{"10.6.0.1"}, "a.example")}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if keys, _ := f.dns.Owned(); len(keys) != 1 {
		t.Errorf("Owned = %v, want the rule written", keys)
	}
	if !strings.Contains(f.logs.String(), "could not tell whether a group policy") {
		t.Errorf("the unreadable branch is not logged: %s", f.logs)
	}
}

// eventually polls until condition holds; the goroutine of a refresh finishes
// on its own time.
func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if condition() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// RefreshPolicyEx cannot be interrupted and may take long on a PC in a domain.
// The pass that called it goes on after refreshTimeout, says so, and does not
// start a second request while the first has not come back.
func TestASlowPolicyRefreshDoesNotHoldUpApply(t *testing.T) {
	f := newDNSFixture()
	f.dns.refreshTimeout = 20 * time.Millisecond
	release := make(chan struct{})
	var calls int
	var callsMu sync.Mutex
	f.dns.sys.refresh = func() error {
		callsMu.Lock()
		calls++
		callsMu.Unlock()
		<-release
		return nil
	}
	callCount := func() int {
		callsMu.Lock()
		defer callsMu.Unlock()
		return calls
	}

	applied := make(chan error, 1)
	go func() {
		applied <- f.dns.Apply("o", []osnet.DNSEntry{entry(0, []string{"10.6.0.1"}, "a.example")})
	}()
	select {
	case err := <-applied:
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Apply was held up by a refresh that does not return")
	}
	if !strings.Contains(f.logs.String(), "has not reread its policy yet") {
		t.Errorf("the slow refresh is not logged: %s", f.logs)
	}

	// The next change does not stack a second request on the first.
	if err := f.dns.Apply("o", []osnet.DNSEntry{entry(0, []string{"10.7.0.1"}, "a.example")}); err != nil {
		t.Fatal(err)
	}
	if got := callCount(); got != 1 {
		t.Errorf("refresh was called %d times while the first had not returned, want 1", got)
	}
	if !strings.Contains(f.logs.String(), "not asking again") {
		t.Errorf("the skipped refresh is not logged: %s", f.logs)
	}

	close(release)
	eventually(t, "the late refresh to be logged", func() bool { return strings.Contains(f.logs.String(), "reread its policy, late") })
	eventually(t, "the refresh to be finished", func() bool { return !f.dns.refreshing.Load() })
	if err := f.dns.Apply("o", []osnet.DNSEntry{entry(0, []string{"10.8.0.1"}, "a.example")}); err != nil {
		t.Fatal(err)
	}
	if got := callCount(); got != 2 {
		t.Errorf("refresh was called %d times after the first came back, want 2", got)
	}
}

func TestAPolicyRefreshThatIsQuickIsNotReportedSlow(t *testing.T) {
	f := newDNSFixture()
	if err := f.dns.Apply("o", []osnet.DNSEntry{entry(0, []string{"10.6.0.1"}, "a.example")}); err != nil {
		t.Fatal(err)
	}
	if f.refreshes != 1 {
		t.Fatalf("refreshes = %d", f.refreshes)
	}
	if logs := f.logs.String(); strings.Contains(logs, "level=WARN") {
		t.Errorf("a quick refresh logged a warning: %s", logs)
	}
}

// systemWithoutPolicy counts the flushes of this fixture, like the system of
// the configurator the fixture made, and sees no group policy.
func (f *dnsFixture) systemWithoutPolicy() systemCalls {
	return systemCalls{
		refresh:          func() error { f.refreshes++; return nil },
		flush:            func() error { f.flushes++; return nil },
		groupPolicyRules: func() ([]string, error) { return nil, nil },
	}
}
