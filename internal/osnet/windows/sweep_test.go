package windows

import (
	"errors"
	"slices"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// The sweep is for a PC where nothing but the registry remembers what was
// written: it takes the rules of every owner, a catch-all included, and leaves
// the rules of other programs alone.
func TestSweepRemovesEveryRuleThatCarriesTheMarker(t *testing.T) {
	f := newDNSFixture()
	for owner, entries := range map[string][]osnet.DNSEntry{
		"profile-1": {entry(0, []string{"10.6.0.1"}, "a.example"), entry(1, []string{"10.6.0.2"}, ".")},
		"profile-2": {entry(0, []string{"10.7.0.1"}, ".")},
	} {
		if err := f.dns.Apply(owner, entries); err != nil {
			t.Fatal(err)
		}
	}
	foreign := rule{Names: []string{".contoso.com"}, Servers: []string{"10.1.1.1"}, Comment: "DirectAccess"}
	f.store.rules["{4B1E9A26-6A4B-4F5B-9F6D-0A3C60D3D1AE}"] = foreign
	before, _ := f.dns.Owned()

	// A new configurator knows nothing: this is the process that runs after the
	// daemon is gone.
	sweeper := newDNSConfigurator(f.store, discardLog(), f.systemWithoutPolicy())
	result, err := SweepOwnedDNS(sweeper)
	if err != nil {
		t.Fatalf("SweepOwnedDNS: %v", err)
	}
	if !slices.Equal(result.Removed, before) || len(result.Remaining) != 0 {
		t.Errorf("result = %+v, want all of %v removed", result, before)
	}
	if len(f.store.rules) != 1 || f.store.rules["{4B1E9A26-6A4B-4F5B-9F6D-0A3C60D3D1AE}"].Comment != "DirectAccess" {
		t.Errorf("only the foreign rule should be left: %v", f.store.rules)
	}
	if f.flushes != 1 {
		t.Errorf("the resolver cache was flushed %d times, want once", f.flushes)
	}
}

func TestSweepOfAMachineWithoutRulesTouchesNothing(t *testing.T) {
	f := newDNSFixture()
	result, err := SweepOwnedDNS(f.dns)
	if err != nil || len(result.Removed) != 0 || len(result.Remaining) != 0 {
		t.Errorf("SweepOwnedDNS = %+v, %v", result, err)
	}
	if len(f.store.ops) != 0 || f.flushes != 0 || f.refreshes != 0 {
		t.Errorf("a sweep of nothing touched the machine: %v, %d flushes, %d refreshes", f.store.ops, f.flushes, f.refreshes)
	}
}

// A rule that cannot be removed is reported, with what is left, and does not
// stop the sweep from taking the others.
func TestSweepReportsWhatRemains(t *testing.T) {
	f := newDNSFixture()
	for _, owner := range []string{"a", "b"} {
		if err := f.dns.Apply(owner, []osnet.DNSEntry{entry(0, []string{"10.6.0.1"}, ".")}); err != nil {
			t.Fatal(err)
		}
	}
	stuck := "Plaitway-" + ownerHash("a") + "-0-0"
	boom := errors.New("access denied")
	f.store.failRemove[stuck] = boom

	result, err := SweepOwnedDNS(f.dns)
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the failure of the removal", err)
	}
	if !slices.Equal(result.Remaining, []string{stuck}) || !slices.Equal(result.Removed, []string{"Plaitway-" + ownerHash("b") + "-0-0"}) {
		t.Errorf("result = %+v", result)
	}
}

// The group policy that stops Apply must not stop the way out.
func TestSweepWorksWhileAGroupPolicyDefinesRules(t *testing.T) {
	f := newDNSFixture()
	if err := f.dns.Apply("o", []osnet.DNSEntry{entry(0, []string{"10.6.0.1"}, ".")}); err != nil {
		t.Fatal(err)
	}
	f.policyRules = []string{"{4B1E9A26-6A4B-4F5B-9F6D-0A3C60D3D1AE}"}
	result, err := SweepOwnedDNS(f.dns)
	if err != nil || len(result.Removed) != 1 || len(result.Remaining) != 0 {
		t.Errorf("SweepOwnedDNS = %+v, %v", result, err)
	}
}

func TestSweepFailsWhenTheRulesCannotBeListed(t *testing.T) {
	f := newDNSFixture()
	f.store.failList = errors.New("registry unavailable")
	if _, err := SweepOwnedDNS(f.dns); !errors.Is(err, f.store.failList) {
		t.Errorf("error = %v", err)
	}
}

func TestSweepReportsAFailedFlush(t *testing.T) {
	f := newDNSFixture()
	if err := f.dns.Apply("o", []osnet.DNSEntry{entry(0, []string{"10.6.0.1"}, ".")}); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("DnsFlushResolverCache: access denied")
	f.dns.sys.flush = func() error { return boom }
	result, err := SweepOwnedDNS(f.dns)
	if !errors.Is(err, boom) || len(result.Removed) != 1 || len(result.Remaining) != 0 {
		t.Errorf("SweepOwnedDNS = %+v, %v; want the rules gone and the flush reported", result, err)
	}
}
