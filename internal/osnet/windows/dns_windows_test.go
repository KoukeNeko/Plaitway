package windows

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// scratchRoot is the key under HKEY_CURRENT_USER that stands in for the DNS
// Client's DnsPolicyConfig key in these tests. Nothing under HKEY_LOCAL_MACHINE
// is touched; the scratch key is deleted when the test ends.
func scratchStore(t *testing.T) *registryStore {
	t.Helper()
	base := fmt.Sprintf(`Software\Plaitway-test-%d-%s`, os.Getpid(), strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()))
	t.Cleanup(func() {
		deleteTree(t, registry.CURRENT_USER, base)
	})
	return &registryStore{root: registry.CURRENT_USER, path: base + `\DnsPolicyConfig`}
}

// deleteTree deletes a key with all subkeys below it.
func deleteTree(t *testing.T, root registry.Key, path string) {
	t.Helper()
	k, err := registry.OpenKey(root, path, registry.ENUMERATE_SUB_KEYS|registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return
	}
	if err != nil {
		t.Errorf("clean up %s: %v", path, err)
		return
	}
	names, err := k.ReadSubKeyNames(-1)
	k.Close()
	if err != nil {
		t.Errorf("clean up %s: %v", path, err)
		return
	}
	for _, name := range names {
		deleteTree(t, root, path+`\`+name)
	}
	if err := registry.DeleteKey(root, path); err != nil && !errors.Is(err, registry.ErrNotExist) {
		t.Errorf("clean up %s: %v", path, err)
	}
}

func realDNS(t *testing.T) (*dnsConfigurator, *registryStore, *int) {
	t.Helper()
	store := scratchStore(t)
	refreshes := new(int)
	sys := quietSystem()
	sys.refresh = func() error { *refreshes++; return nil }
	return newDNSConfigurator(store, discardLog(), sys), store, refreshes
}

// The values must have the types the DNS Client reads: Name is a multi-string,
// ConfigOptions and Version are DWORDs, the servers a plain string.
func TestRegistryRuleLayout(t *testing.T) {
	dns, store, _ := realDNS(t)
	err := dns.Apply("profile-1", []osnet.DNSEntry{entry(3, []string{"10.6.0.1", "fd00::1"}, "corp.example.com", "example.org")})
	if err != nil {
		t.Fatal(err)
	}
	keys, err := dns.Owned()
	if err != nil || len(keys) != 1 {
		t.Fatalf("Owned = %v, %v", keys, err)
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, store.path+`\`+keys[0], registry.QUERY_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()

	names, valtype, err := k.GetStringsValue("Name")
	if err != nil || valtype != registry.MULTI_SZ || !slices.Equal(names, []string{".corp.example.com", "corp.example.com", ".example.org", "example.org"}) {
		t.Errorf("Name = %v, type %d, %v", names, valtype, err)
	}
	servers, valtype, err := k.GetStringValue("GenericDNSServers")
	if err != nil || valtype != registry.SZ || servers != "10.6.0.1;fd00::1" {
		t.Errorf("GenericDNSServers = %q, type %d, %v", servers, valtype, err)
	}
	for name, want := range map[string]uint64{"ConfigOptions": 8, "Version": 2} {
		got, valtype, err := k.GetIntegerValue(name)
		if err != nil || valtype != registry.DWORD || got != want {
			t.Errorf("%s = %d, type %d, %v; want %d", name, got, valtype, err, want)
		}
	}
	comment, _, err := k.GetStringValue("Comment")
	if err != nil {
		t.Fatal(err)
	}
	if owner, order, ok := parseComment(comment); !ok || owner != "profile-1" || order != 3 {
		t.Errorf("Comment = %q", comment)
	}
	if display, _, _ := k.GetStringValue("DisplayName"); display != "Plaitway" {
		t.Errorf("DisplayName = %q", display)
	}
}

func TestRegistryDNSLifecycle(t *testing.T) {
	dns, store, refreshes := realDNS(t)
	if keys, err := dns.Owned(); err != nil || len(keys) != 0 {
		t.Fatalf("before anything is written: %v, %v", keys, err)
	}
	if err := dns.Remove("nobody"); err != nil {
		t.Fatalf("removing from a key that does not exist: %v", err)
	}

	first := []osnet.DNSEntry{entry(0, []string{"10.6.0.1"}, "corp.example.com"), entry(1, []string{"10.6.0.2", "2001:db8::53"}, ".")}
	if err := dns.Apply("profile-1", first); err != nil {
		t.Fatal(err)
	}
	if err := dns.Apply("profile-1", first); err != nil {
		t.Fatal(err)
	}
	if *refreshes != 1 {
		t.Errorf("refreshes = %d: the second, identical Apply wrote again", *refreshes)
	}

	// Read the rules back through the store: what was written is what was meant.
	keys, _ := dns.Owned()
	for i, key := range keys {
		r, err := store.read(key)
		if err != nil {
			t.Fatal(err)
		}
		got, owner, ok := entryOf(r)
		if !ok || owner != "profile-1" || !entriesEqual(got, first[i]) {
			t.Errorf("rule %s read back as %+v (%q, %v), want %+v", key, got, owner, ok, first[i])
		}
	}

	// A foreign rule next to ours is never listed and never removed.
	foreign := `{4B1E9A26-6A4B-4F5B-9F6D-0A3C60D3D1AE}`
	if err := store.write(foreign, rule{Names: []string{".contoso.com"}, Servers: []string{"10.1.1.1"}, Comment: "DirectAccess"}); err != nil {
		t.Fatal(err)
	}

	second := []osnet.DNSEntry{entry(0, []string{"10.7.0.1"}, "corp.example.com")}
	if err := dns.Apply("profile-1", second); err != nil {
		t.Fatal(err)
	}
	keys, _ = dns.Owned()
	if want := "Plaitway-" + ownerHash("profile-1") + "-1-0"; !slices.Equal(keys, []string{want}) {
		t.Errorf("after the replacement Owned = %v, want [%s]", keys, want)
	}
	all, err := store.ruleKeys()
	if err != nil || !slices.Contains(all, foreign) || len(all) != 2 {
		t.Errorf("keys = %v, %v", all, err)
	}

	// Crash recovery: a new configurator knows nothing and finds the rule.
	restarted := newDNSConfigurator(store, discardLog(), quietSystem())
	found, err := restarted.Owned()
	if err != nil || !slices.Equal(found, keys) {
		t.Fatalf("after the restart Owned = %v, %v", found, err)
	}
	if err := restarted.Remove(found[0]); err != nil {
		t.Fatal(err)
	}
	all, _ = store.ruleKeys()
	if !slices.Equal(all, []string{foreign}) {
		t.Errorf("only the foreign rule should be left: %v", all)
	}
}

// A registry key name takes any character but a backslash, up to 255; the
// owner names that Plaitway uses may be longer than that and have odd
// characters, which is why they are hashed.
func TestRegistryDNSWithOddOwners(t *testing.T) {
	dns, _, _ := realDNS(t)
	owners := []string{
		`back\slash and "quotes"`,
		"繁體中文 🛰️",
		"a/b*c?d",
		"line separator" + string(rune(0x2028)) + "inside",
	}
	long := ""
	for len(long) < 900 {
		long += "owner-name-"
	}
	owners = append(owners, long)
	for _, owner := range owners {
		if err := dns.Apply(owner, []osnet.DNSEntry{entry(0, []string{"10.6.0.1"}, "corp.example.com")}); err != nil {
			t.Fatalf("owner %.40q: %v", owner, err)
		}
	}
	keys, err := dns.Owned()
	if err != nil || len(keys) != len(owners) {
		t.Fatalf("Owned = %v, %v", keys, err)
	}
	for _, owner := range owners {
		if err := dns.Remove(owner); err != nil {
			t.Fatalf("remove %.40q: %v", owner, err)
		}
	}
	if keys, _ := dns.Owned(); len(keys) != 0 {
		t.Errorf("left over: %v", keys)
	}
}

// A rule that is written again over a key that already has one starts by
// retiring its namespace, so that it is never half old and half new.
func TestRegistryWriteReplacesAnExistingKey(t *testing.T) {
	_, store, _ := realDNS(t)
	key := "Plaitway-" + ownerHash("o") + "-0-0"
	if err := store.write(key, rule{Names: []string{"."}, Servers: []string{"10.0.0.1"}, Comment: commentOf("o", 0)}); err != nil {
		t.Fatal(err)
	}
	next := rule{Names: []string{".a.example", "a.example"}, Servers: []string{"10.0.0.2", "10.0.0.3"}, Comment: commentOf("o", 1)}
	if err := store.write(key, next); err != nil {
		t.Fatal(err)
	}
	got, err := store.read(key)
	if err != nil || !slices.Equal(got.Names, next.Names) || !slices.Equal(got.Servers, next.Servers) || got.Comment != next.Comment {
		t.Errorf("read back %+v, %v; want %+v", got, err, next)
	}
	if err := store.remove(key); err != nil {
		t.Fatal(err)
	}
	if err := store.remove(key); err != nil {
		t.Errorf("removing a key twice: %v", err)
	}
	if _, err := store.read(key); err == nil {
		t.Error("a removed rule can still be read")
	}
}

// The real configurator reads the DNS Client's key. This machine may have no
// rules at all (the key may not exist), but listing must not fail.
func TestRealDNSOwnedReadsTheMachineKey(t *testing.T) {
	dns := NewDNS(DNSOptions{Logger: discardLog()})
	if _, err := dns.Owned(); err != nil {
		t.Fatalf("Owned on the real key: %v", err)
	}
}

func TestSystemEntryPointsExist(t *testing.T) {
	for name, proc := range map[string]interface{ Find() error }{
		"RefreshPolicyEx":                          procRefreshPolicyEx,
		"DnsFlushResolverCache":                    procDnsFlushResolverCache,
		"InitializeIpForwardEntry":                 procInitializeIpForwardEntry,
		"CreateIpForwardEntry2":                    procCreateIpForwardEntry2,
		"DeleteIpForwardEntry2":                    procDeleteIpForwardEntry2,
		"PowerRegisterSuspendResumeNotification":   procPowerRegisterSuspendResumeNotification,
		"PowerUnregisterSuspendResumeNotification": procPowerUnregisterSuspendResumeNotification,
	} {
		if err := proc.Find(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The policy branch is read as the rule store is: one subkey per rule. A
// scratch branch stands in for the real one, which a test must not write.
func TestApplyReadsTheGroupPolicyBranchOfTheRegistry(t *testing.T) {
	rules, store, _ := realDNS(t)
	branch := &registryStore{root: store.root, path: strings.TrimSuffix(store.path, "DnsPolicyConfig") + "GroupPolicy"}
	rules.sys.groupPolicyRules = branch.ruleKeys
	entries := []osnet.DNSEntry{entry(0, []string{"10.6.0.1"}, "corp.example.com")}

	if err := rules.Apply("o", entries); err != nil {
		t.Fatalf("Apply with no policy branch: %v", err)
	}
	if err := branch.write("{4B1E9A26-6A4B-4F5B-9F6D-0A3C60D3D1AE}", rule{Names: []string{".contoso.com"}, Servers: []string{"10.1.1.1"}}); err != nil {
		t.Fatal(err)
	}
	if err := rules.Apply("o", []osnet.DNSEntry{entry(0, []string{"10.7.0.1"}, "other.example")}); !errors.Is(err, ErrGroupPolicyNRPT) {
		t.Errorf("Apply with a policy rule = %v, want ErrGroupPolicyNRPT", err)
	}
	if err := branch.remove("{4B1E9A26-6A4B-4F5B-9F6D-0A3C60D3D1AE}"); err != nil {
		t.Fatal(err)
	}
	if err := rules.Apply("o", []osnet.DNSEntry{entry(0, []string{"10.7.0.1"}, "other.example")}); err != nil {
		t.Errorf("Apply after the policy rule is gone: %v", err)
	}
}

// The real branch, read only. This machine's answer is logged: a PC in a
// domain may well have rules, and this test must not fail on it.
func TestTheRealGroupPolicyBranchIsReadable(t *testing.T) {
	rules, err := groupPolicyRules()
	if err != nil {
		t.Fatalf("groupPolicyRules: %v", err)
	}
	t.Logf("rules delivered by group policy on this PC: %d", len(rules))
}

// The sweep against a real registry key: the rules of two owners go, the rule
// of another program stays.
func TestSweepOnTheRegistry(t *testing.T) {
	dns, store, _ := realDNS(t)
	for _, owner := range []string{"profile-1", "profile-2"} {
		if err := dns.Apply(owner, []osnet.DNSEntry{entry(0, []string{"10.6.0.1"}, ".")}); err != nil {
			t.Fatal(err)
		}
	}
	foreign := `{4B1E9A26-6A4B-4F5B-9F6D-0A3C60D3D1AE}`
	if err := store.write(foreign, rule{Names: []string{".contoso.com"}, Servers: []string{"10.1.1.1"}, Comment: "DirectAccess"}); err != nil {
		t.Fatal(err)
	}
	result, err := SweepOwnedDNS(dns)
	if err != nil || len(result.Removed) != 2 || len(result.Remaining) != 0 {
		t.Fatalf("SweepOwnedDNS = %+v, %v", result, err)
	}
	if keys, err := store.ruleKeys(); err != nil || !slices.Equal(keys, []string{foreign}) {
		t.Errorf("keys = %v, %v; want only the foreign rule", keys, err)
	}
}

// The command in the messages must name the key and the marker the rules
// really have.
func TestManualSweepCommandNamesTheRealKeyAndMarker(t *testing.T) {
	command := ManualSweepCommand()
	for _, want := range []string{`HKLM:\` + policyConfigPath, "'" + ruleKeyPrefix + "*'", "Remove-Item", "Clear-DnsClientCache"} {
		if !strings.Contains(command, want) {
			t.Errorf("%q lacks %q", command, want)
		}
	}
}
