package macos

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// realList is what scutil printed for "list State:/Network/Service/.*/DNS" on
// the machine the other fixtures come from; the service ids are replaced.
const realList = `  subKey [0] = State:/Network/Service/00000000-0000-4000-8000-000000000001/DNS
  subKey [1] = State:/Network/Service/00000000-0000-4000-8000-000000000002/DNS
`

// realNoKeys is what scutil printed for a pattern that matched nothing.
const realNoKeys = "  no keys.\n"

// realDNS is what "scutil --dns" printed on the machine the other fixtures come
// from, with the address of the tunnel's resolver replaced. The first resolver
// is a supplemental one without a domain, the system's own default has order
// 200000, and the .local resolvers answer through mDNS.
const realDNS = `DNS configuration

resolver #1
  nameserver[0] : 203.0.113.1
  if_index : 50 (utun10)
  flags    : Supplemental, Request A records
  reach    : 0x00000003 (Reachable,Transient Connection)
  order    : 103200

resolver #2
  nameserver[0] : 1.1.1.1
  flags    : Request A records
  reach    : 0x00000002 (Reachable)
  order    : 200000

resolver #3
  domain   : local
  options  : mdns
  timeout  : 5
  flags    : Request A records
  reach    : 0x00000000 (Not Reachable)
  order    : 300000

resolver #4
  domain   : 254.169.in-addr.arpa
  options  : mdns
  timeout  : 5
  flags    : Request A records
  reach    : 0x00000000 (Not Reachable)
  order    : 300200

resolver #5
  domain   : 8.e.f.ip6.arpa
  options  : mdns
  timeout  : 5
  flags    : Request A records
  reach    : 0x00000000 (Not Reachable)
  order    : 300400

resolver #6
  domain   : 9.e.f.ip6.arpa
  options  : mdns
  timeout  : 5
  flags    : Request A records
  reach    : 0x00000000 (Not Reachable)
  order    : 300600

resolver #7
  domain   : a.e.f.ip6.arpa
  options  : mdns
  timeout  : 5
  flags    : Request A records
  reach    : 0x00000000 (Not Reachable)
  order    : 300800

resolver #8
  domain   : b.e.f.ip6.arpa
  options  : mdns
  timeout  : 5
  flags    : Request A records
  reach    : 0x00000000 (Not Reachable)
  order    : 301000

DNS configuration (for scoped queries)

resolver #1
  nameserver[0] : 1.1.1.1
  if_index : 14 (en0)
  flags    : Scoped, Request A records
  reach    : 0x00000002 (Reachable)

resolver #2
  nameserver[0] : 203.0.113.1
  if_index : 50 (utun10)
  flags    : Scoped, Request A records
  reach    : 0x00000003 (Reachable,Transient Connection)
`

func key(owner string, n int) string { return dnsKeyName(owner, n) }

// fakeSystem plays scutil and the other tools the DNS configurator runs. Its
// store holds, per key, the "d.add" lines that were set.
type fakeSystem struct {
	mu    sync.Mutex
	store map[string][]string
	runs  []string // "program: first line of stdin"
	// dropSets makes "set" do nothing, silently: a write that failed in a way
	// the output does not tell.
	dropSets bool
	// printed is what a scutil session that writes prints.
	printed string
	// dropRemoves is dropSets for "remove".
	dropRemoves bool
	fail        map[string]error
}

func newFakeSystem() *fakeSystem {
	return &fakeSystem{
		store: map[string][]string{
			"State:/Network/Service/00000000-0000-4000-8000-000000000001/DNS": {"d.add ServerAddresses * 192.0.2.53"},
		},
		fail: map[string]error{},
	}
}

func (f *fakeSystem) run(_ context.Context, name string, args []string, stdin string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, name+" "+strings.Join(args, " ")+"|"+strings.SplitN(stdin, "\n", 2)[0])
	if err := f.fail[name]; err != nil {
		return []byte("it printed this"), err
	}
	if name != scutilPath {
		return nil, nil
	}
	var out strings.Builder
	var dict []string
	wrote := false
	for _, line := range strings.Split(stdin, "\n") {
		cmd, arg, _ := strings.Cut(line, " ")
		switch cmd {
		case "":
		case "d.init":
			dict = nil
		case "d.add":
			dict = append(dict, line)
		case "set":
			wrote = true
			if !f.dropSets {
				f.store[arg] = slices.Clone(dict)
			}
		case "remove":
			wrote = true
			if !f.dropRemoves {
				delete(f.store, arg)
			}
		case "list":
			pattern := regexp.MustCompile(arg)
			var keys []string
			for k := range f.store {
				if pattern.MatchString(k) {
					keys = append(keys, k)
				}
			}
			slices.Sort(keys)
			for i, k := range keys {
				fmt.Fprintf(&out, "  subKey [%d] = %s\n", i, k)
			}
			if len(keys) == 0 {
				out.WriteString("  no keys.\n")
			}
		case "quit":
		default:
			fmt.Fprintf(&out, "%s: unknown, type \"help\" for command info\n", cmd)
		}
	}
	if wrote {
		out.WriteString(f.printed)
	}
	return []byte(out.String()), nil
}

func (f *fakeSystem) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []string
	for k := range f.store {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func newTestDNS(f *fakeSystem) *dnsConfigurator {
	return newDNSConfigurator(DNSOptions{Run: f.run})
}

func addrs(s ...string) []netip.Addr {
	out := make([]netip.Addr, len(s))
	for i, a := range s {
		out[i] = netip.MustParseAddr(a)
	}
	return out
}

func TestApplyScript(t *testing.T) {
	keys, err := planDNSKeys("office", []osnet.DNSEntry{
		{Servers: addrs("10.6.0.1", "fd00::1"), MatchDomains: []string{".", "corp.example.com"}, Order: 5000},
		{Servers: addrs("192.0.2.53"), MatchDomains: []string{"lan"}, Order: 7},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := applyScript("office", keys, []string{key("office", 2)})
	want := `d.init
d.add ServerAddresses * 10.6.0.1 fd00::1
d.add SupplementalMatchDomains * "" corp.example.com
d.add SupplementalMatchDomainsNoSearch # 1
d.add SupplementalMatchOrders * # 5000 5000
d.add PlaitwayOwner office
set State:/Network/Service/plaitway-office-0/DNS
d.init
d.add ServerAddresses * 192.0.2.53
d.add SupplementalMatchDomains * lan
d.add SupplementalMatchDomainsNoSearch # 1
d.add SupplementalMatchOrders * # 7
d.add PlaitwayOwner office
set State:/Network/Service/plaitway-office-1/DNS
remove State:/Network/Service/plaitway-office-2/DNS
quit
`
	if got != want {
		t.Errorf("script:\n%s\nwant:\n%s", got, want)
	}
}

func TestApplyWritesTheKeys(t *testing.T) {
	f := newFakeSystem()
	d := newTestDNS(f)
	err := d.Apply("office", []osnet.DNSEntry{
		{Servers: addrs("10.6.0.1"), MatchDomains: []string{"."}, Order: 100},
		{Servers: addrs("10.6.0.53"), MatchDomains: []string{"Corp.Example.COM.", "lan"}, Order: 200},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := f.store[key("office", 0)], []string{
		"d.add ServerAddresses * 10.6.0.1",
		`d.add SupplementalMatchDomains * ""`,
		"d.add SupplementalMatchDomainsNoSearch # 1",
		"d.add SupplementalMatchOrders * # 100",
		"d.add PlaitwayOwner office",
	}; !slices.Equal(got, want) {
		t.Errorf("key 0 = %q, want %q", got, want)
	}
	if got, want := f.store[key("office", 1)], []string{
		"d.add ServerAddresses * 10.6.0.53",
		"d.add SupplementalMatchDomains * corp.example.com lan",
		"d.add SupplementalMatchDomainsNoSearch # 1",
		"d.add SupplementalMatchOrders * # 200 200",
		"d.add PlaitwayOwner office",
	}; !slices.Equal(got, want) {
		t.Errorf("key 1 = %q, want %q", got, want)
	}
	if len(f.store) != 3 {
		t.Errorf("store has %v: the system's own key must stay", f.keys())
	}
}

// The Reconciler's sweep of leftovers hands Remove the keys that Owned found.
func TestRemoveAcceptsAKeyFromOwned(t *testing.T) {
	f := newFakeSystem()
	d := newTestDNS(f)
	entry := []osnet.DNSEntry{{Servers: addrs("10.6.0.1"), MatchDomains: []string{"a.example"}}}
	for _, owner := range []string{"office", "lab"} {
		if err := d.Apply(owner, entry); err != nil {
			t.Fatal(err)
		}
	}
	owned, err := d.Owned()
	if err != nil || len(owned) != 2 {
		t.Fatalf("Owned = %v, %v", owned, err)
	}
	if err := d.Remove(key("office", 0)); err != nil {
		t.Fatalf("Remove(key): %v", err)
	}
	// The system's own key stays; so does the other owner's.
	owned, err = d.Owned()
	if err != nil || !slices.Equal(owned, []string{key("lab", 0)}) {
		t.Errorf("Owned after removing office's key = %v, %v, want only lab's", owned, err)
	}
	if len(f.keys()) != 2 {
		t.Errorf("store has %v, want the system key and lab's", f.keys())
	}
}

func TestApplySplitsLongDomainLists(t *testing.T) {
	var domains []string
	for i := range 120 {
		domains = append(domains, fmt.Sprintf("d%d.example.com", i))
	}
	f := newFakeSystem()
	if err := newTestDNS(f).Apply("office", []osnet.DNSEntry{{Servers: addrs("10.6.0.1"), MatchDomains: domains, Order: 9}}); err != nil {
		t.Fatal(err)
	}
	var counts []int
	for n := range 3 {
		line := f.store[key("office", n)][1] // SupplementalMatchDomains
		counts = append(counts, len(strings.Fields(line))-3)
	}
	if !slices.Equal(counts, []int{50, 50, 20}) {
		t.Errorf("domains per key: %v, want 50, 50, 20", counts)
	}
	if _, extra := f.store[key("office", 3)]; extra {
		t.Error("a fourth key")
	}
}

func TestApplyReplacesWhatIsThere(t *testing.T) {
	f := newFakeSystem()
	d := newTestDNS(f)
	three := []osnet.DNSEntry{
		{Servers: addrs("10.6.0.1"), MatchDomains: []string{"a.example"}},
		{Servers: addrs("10.6.0.1"), MatchDomains: []string{"b.example"}},
		{Servers: addrs("10.6.0.1"), MatchDomains: []string{"c.example"}},
	}
	if err := d.Apply("a", three); err != nil {
		t.Fatal(err)
	}
	// An owner whose name continues the first one's must not be touched.
	if err := d.Apply("a-1", three[:1]); err != nil {
		t.Fatal(err)
	}
	before := len(f.keys())

	if err := d.Apply("a", three[2:]); err != nil {
		t.Fatal(err)
	}
	if got := f.store[key("a", 0)][1]; got != "d.add SupplementalMatchDomains * c.example" {
		t.Errorf("key 0 was not replaced: %q", got)
	}
	for _, stale := range []string{key("a", 1), key("a", 2)} {
		if _, ok := f.store[stale]; ok {
			t.Errorf("%s survived", stale)
		}
	}
	if _, ok := f.store[key("a-1", 0)]; !ok {
		t.Error("the keys of owner a-1 were removed when a was applied")
	}
	if got := len(f.keys()); got != before-2 {
		t.Errorf("%d keys, want %d", got, before-2)
	}
}

func TestApplyOfNothingRemoves(t *testing.T) {
	f := newFakeSystem()
	d := newTestDNS(f)
	if err := d.Apply("office", []osnet.DNSEntry{{Servers: addrs("10.6.0.1"), MatchDomains: []string{"."}}}); err != nil {
		t.Fatal(err)
	}
	if err := d.Apply("office", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.store[key("office", 0)]; ok {
		t.Error("key is still there")
	}
	if len(f.store) != 1 {
		t.Errorf("store has %v: the system's own key must stay", f.keys())
	}
}

func TestRemove(t *testing.T) {
	f := newFakeSystem()
	d := newTestDNS(f)
	one := []osnet.DNSEntry{{Servers: addrs("10.6.0.1"), MatchDomains: []string{"."}}}
	for _, owner := range []string{"office", "home"} {
		if err := d.Apply(owner, one); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Remove("office"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.store[key("office", 0)]; ok {
		t.Error("office is still there")
	}
	if _, ok := f.store[key("home", 0)]; !ok {
		t.Error("home was removed too")
	}

	// Removing nothing is not an error and writes nothing.
	f.runs = nil
	if err := d.Remove("office"); err != nil {
		t.Fatal(err)
	}
	for _, run := range f.runs {
		if strings.Contains(run, "remove") || strings.Contains(run, "d.init") {
			t.Errorf("a write session for nothing: %q", run)
		}
	}
}

func TestOwnedListsOnlyOurKeys(t *testing.T) {
	f := newFakeSystem()
	for _, k := range []string{
		key("office", 0), key("office", 1), key("home", 0),
		"State:/Network/Service/plaitway-/DNS",                 // not ours: no owner
		"State:/Network/Service/plaitway-noindex/DNS",          // not ours: no number
		"State:/Network/Service/other-plaitway-office-0/DNS",   // not ours: other prefix
		"State:/Network/Service/plaitway-office-0/IPv4",        // not ours: other dictionary
		"Setup:/Network/Service/plaitway-office-0/DNS",         // not ours: not State:
		"State:/Network/Service/plaitway-office-0/DNS/extra/x", // not ours: longer
	} {
		f.store[k] = nil
	}
	got, err := newTestDNS(f).Owned()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{key("home", 0), key("office", 0), key("office", 1)}; !slices.Equal(got, want) {
		t.Errorf("owned = %v, want %v", got, want)
	}
}

func TestOwnedOnAnEmptyStore(t *testing.T) {
	f := newFakeSystem()
	clear(f.store)
	got, err := newTestDNS(f).Owned()
	if err != nil || len(got) != 0 {
		t.Errorf("owned = %v, %v", got, err)
	}
}

func TestApplyRejectsWhatCouldInjectCommands(t *testing.T) {
	servers := addrs("10.6.0.1")
	long := strings.Repeat("a", 64)
	tests := []struct {
		name    string
		owner   string
		entries []osnet.DNSEntry
	}{
		{"empty owner", "", []osnet.DNSEntry{{Servers: servers, MatchDomains: []string{"."}}}},
		{"owner with a space", "a b", []osnet.DNSEntry{{Servers: servers, MatchDomains: []string{"."}}}},
		{"owner with a command", "a\nremove State:/Network/Global/IPv4", []osnet.DNSEntry{{Servers: servers, MatchDomains: []string{"."}}}},
		{"owner with a slash", "a/b", []osnet.DNSEntry{{Servers: servers, MatchDomains: []string{"."}}}},
		{"owner with a regexp", "a.*", []osnet.DNSEntry{{Servers: servers, MatchDomains: []string{"."}}}},
		{"owner that is too long", strings.Repeat("a", 65), []osnet.DNSEntry{{Servers: servers, MatchDomains: []string{"."}}}},
		{"domain with a newline", "o", []osnet.DNSEntry{{Servers: servers, MatchDomains: []string{"x.com\nset State:/Network/Global/DNS"}}}},
		{"domain with a space", "o", []osnet.DNSEntry{{Servers: servers, MatchDomains: []string{"a b.com"}}}},
		{"domain with a quote", "o", []osnet.DNSEntry{{Servers: servers, MatchDomains: []string{`a".com`}}}},
		{"wildcard domain", "o", []osnet.DNSEntry{{Servers: servers, MatchDomains: []string{"*.example.com"}}}},
		{"domain with an empty label", "o", []osnet.DNSEntry{{Servers: servers, MatchDomains: []string{"a..com"}}}},
		{"domain starting with a dot", "o", []osnet.DNSEntry{{Servers: servers, MatchDomains: []string{".example.com"}}}},
		{"label starting with a hyphen", "o", []osnet.DNSEntry{{Servers: servers, MatchDomains: []string{"-a.com"}}}},
		{"label ending with a hyphen", "o", []osnet.DNSEntry{{Servers: servers, MatchDomains: []string{"a-.com"}}}},
		{"label that is too long", "o", []osnet.DNSEntry{{Servers: servers, MatchDomains: []string{long + ".com"}}}},
		{"domain that is too long", "o", []osnet.DNSEntry{{Servers: servers, MatchDomains: []string{strings.Repeat("abcdefgh.", 31) + "com"}}}},
		{"empty domain", "o", []osnet.DNSEntry{{Servers: servers, MatchDomains: []string{""}}}},
		{"non-ASCII domain", "o", []osnet.DNSEntry{{Servers: servers, MatchDomains: []string{"例え.jp"}}}},
		{"no domains", "o", []osnet.DNSEntry{{Servers: servers}}},
		{"no servers", "o", []osnet.DNSEntry{{MatchDomains: []string{"."}}}},
		{"invalid server", "o", []osnet.DNSEntry{{Servers: []netip.Addr{{}}, MatchDomains: []string{"."}}}},
		{"unspecified server", "o", []osnet.DNSEntry{{Servers: addrs("0.0.0.0"), MatchDomains: []string{"."}}}},
		{"multicast server", "o", []osnet.DNSEntry{{Servers: addrs("224.0.0.251"), MatchDomains: []string{"."}}}},
		{"server with a zone", "o", []osnet.DNSEntry{{Servers: []netip.Addr{netip.MustParseAddr("fe80::1%en0")}, MatchDomains: []string{"."}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeSystem()
			if err := newTestDNS(f).Apply(tt.owner, tt.entries); err == nil {
				t.Error("no error")
			}
			if len(f.runs) != 0 {
				t.Errorf("scutil ran: %q", f.runs)
			}
		})
	}
	f := newFakeSystem()
	d := newTestDNS(f)
	if err := d.Remove("a b"); err == nil {
		t.Error("Remove accepted an invalid owner")
	}
	if len(f.runs) != 0 {
		t.Errorf("scutil ran: %q", f.runs)
	}
}

func TestApplyAcceptsWhatProfilesContain(t *testing.T) {
	f := newFakeSystem()
	err := newTestDNS(f).Apply("a1b2c3d4-e5f6", []osnet.DNSEntry{{
		Servers:      addrs("10.6.0.1", "::ffff:10.6.0.2", "2001:db8::53"),
		MatchDomains: []string{"corp.example.com", "_sip._tcp.example.com", "xn--r8jz45g.jp", "168.192.in-addr.arpa", "a"},
		Order:        5,
	}})
	if err != nil {
		t.Fatal(err)
	}
	lines := f.store[key("a1b2c3d4-e5f6", 0)]
	if lines[0] != "d.add ServerAddresses * 10.6.0.1 10.6.0.2 2001:db8::53" {
		t.Errorf("servers %q", lines[0])
	}
	if lines[3] != "d.add SupplementalMatchOrders * # 5 5 5 5 5" {
		t.Errorf("orders %q", lines[3])
	}
}

// scutil exits 0 whatever happens, so a write that silently did not happen is
// found by reading the keys back.
func TestApplyNoticesAWriteThatDidNotHappen(t *testing.T) {
	f := newFakeSystem()
	f.dropSets = true
	f.printed = "  Permission denied\n"
	err := newTestDNS(f).Apply("office", []osnet.DNSEntry{{Servers: addrs("10.6.0.1"), MatchDomains: []string{"."}}})
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{key("office", 0), "Permission denied"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestRemoveNoticesAKeyThatStays(t *testing.T) {
	f := newFakeSystem()
	d := newTestDNS(f)
	if err := d.Apply("office", []osnet.DNSEntry{{Servers: addrs("10.6.0.1"), MatchDomains: []string{"."}}}); err != nil {
		t.Fatal(err)
	}
	f.dropRemoves = true
	if err := d.Remove("office"); err == nil {
		t.Error("Remove did not notice that the key is still there")
	}
}

func TestScutilErrorsKeepTheOutput(t *testing.T) {
	f := newFakeSystem()
	f.fail[scutilPath] = errors.New("exit status 1")
	d := newTestDNS(f)
	for name, call := range map[string]func() error{
		"Apply": func() error {
			return d.Apply("office", []osnet.DNSEntry{{Servers: addrs("10.6.0.1"), MatchDomains: []string{"."}}})
		},
		"Remove": func() error { return d.Remove("office") },
		"Owned":  func() error { _, err := d.Owned(); return err },
	} {
		err := call()
		if err == nil || !strings.Contains(err.Error(), "exit status 1") || !strings.Contains(err.Error(), "it printed this") {
			t.Errorf("%s: error %v", name, err)
		}
	}
}

func TestFlush(t *testing.T) {
	f := newFakeSystem()
	if err := newTestDNS(f).Flush(); err != nil {
		t.Fatal(err)
	}
	want := []string{dscacheutilPath + " -flushcache|", killallPath + " -HUP mDNSResponder|"}
	if !slices.Equal(f.runs, want) {
		t.Errorf("ran %q, want %q", f.runs, want)
	}

	f = newFakeSystem()
	f.fail[dscacheutilPath] = errors.New("exit status 1")
	err := newTestDNS(f).Flush()
	if err == nil || !strings.Contains(err.Error(), "dscacheutil") {
		t.Errorf("error %v does not mention dscacheutil", err)
	}
	if len(f.runs) != 2 {
		t.Errorf("ran %q: mDNSResponder must be signalled even when dscacheutil fails", f.runs)
	}
}

func TestParseKeyList(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want []string
		bad  bool
	}{
		{"real output", realList, []string{
			"State:/Network/Service/00000000-0000-4000-8000-000000000001/DNS",
			"State:/Network/Service/00000000-0000-4000-8000-000000000002/DNS",
		}, false},
		{"real output without keys", realNoKeys, nil, false},
		{"empty output", "", nil, false},
		{"something else", "  Permission denied\n", nil, true},
		{"a good line and a bad one", realList + "SCDynamicStoreCopyKeyList() failed\n", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseKeyList(tt.out)
			if (err != nil) != tt.bad || !slices.Equal(got, tt.want) {
				t.Errorf("got %v, %v; want %v, bad=%v", got, err, tt.want, tt.bad)
			}
		})
	}
}

func TestOwnerOfKey(t *testing.T) {
	tests := []struct {
		key   string
		owner string
		ok    bool
	}{
		{key("office", 0), "office", true},
		{key("office", 12), "office", true},
		{key("a-1", 0), "a-1", true},
		{key("a", 1), "a", true},
		{key("550e8400-e29b-41d4-a716-446655440000", 3), "550e8400-e29b-41d4-a716-446655440000", true},
		{"State:/Network/Service/00000000-0000-4000-8000-000000000001/DNS", "", false},
		{"State:/Network/Service/plaitway-office/DNS", "", false},
		{"State:/Network/Service/plaitway--0/DNS", "", false},
	}
	for _, tt := range tests {
		owner, ok := ownerOfKey(tt.key)
		if owner != tt.owner || ok != tt.ok {
			t.Errorf("ownerOfKey(%q) = %q, %v; want %q, %v", tt.key, owner, ok, tt.owner, tt.ok)
		}
	}
}

// resolver is one resolver of "scutil --dns".
type resolver struct {
	Domain       string
	Nameservers  []string
	Order        int
	Supplemental bool
	Scoped       bool
}

// parseResolvers parses the output of "scutil --dns", for the root-gated test
// to check what configd made of the keys.
func parseResolvers(t testing.TB, out string) []resolver {
	t.Helper()
	var resolvers []resolver
	scoped := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "DNS configuration (for scoped queries)"):
			scoped = true
		case strings.HasPrefix(line, "resolver #"):
			resolvers = append(resolvers, resolver{Scoped: scoped})
		}
		key, value, ok := strings.Cut(line, " : ")
		if !ok || len(resolvers) == 0 {
			continue
		}
		r := &resolvers[len(resolvers)-1]
		switch key = strings.TrimSpace(key); {
		case strings.HasPrefix(key, "nameserver["):
			r.Nameservers = append(r.Nameservers, value)
		case key == "domain":
			r.Domain = value
		case key == "order":
			order, err := strconv.Atoi(value)
			if err != nil {
				t.Fatalf("order %q: %v", value, err)
			}
			r.Order = order
		case key == "flags":
			r.Supplemental = strings.Contains(value, "Supplemental")
		}
	}
	return resolvers
}

func TestParseResolvers(t *testing.T) {
	got := parseResolvers(t, realDNS)
	want := []resolver{
		{Nameservers: []string{"203.0.113.1"}, Order: 103200, Supplemental: true},
		{Nameservers: []string{"1.1.1.1"}, Order: 200000},
		{Domain: "local", Order: 300000},
		{Domain: "254.169.in-addr.arpa", Order: 300200},
		{Domain: "8.e.f.ip6.arpa", Order: 300400},
		{Domain: "9.e.f.ip6.arpa", Order: 300600},
		{Domain: "a.e.f.ip6.arpa", Order: 300800},
		{Domain: "b.e.f.ip6.arpa", Order: 301000},
		{Nameservers: []string{"1.1.1.1"}, Scoped: true},
		{Nameservers: []string{"203.0.113.1"}, Scoped: true},
	}
	if len(got) != len(want) {
		t.Fatalf("%d resolvers, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if !slices.Equal(got[i].Nameservers, want[i].Nameservers) || got[i].Domain != want[i].Domain || got[i].Order != want[i].Order ||
			got[i].Supplemental != want[i].Supplemental || got[i].Scoped != want[i].Scoped {
			t.Errorf("resolver %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
