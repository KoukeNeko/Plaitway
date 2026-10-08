package wg

import (
	"fmt"
	"strings"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

func TestAdapterNameIsStablePerProfile(t *testing.T) {
	const owner = tunnel.OwnerID("7d1f3a52-9c0e-4f61-8a5d-0b2c4e6f8a10")
	first := adapterName(defaultAdapterPrefix, owner)
	if again := adapterName(defaultAdapterPrefix, owner); again != first {
		t.Errorf("name changed between calls: %q, then %q", first, again)
	}
	if !strings.HasPrefix(first, defaultAdapterPrefix) || len(first) != len(defaultAdapterPrefix)+2*adapterIDBytes {
		t.Errorf("name = %q, want the prefix and %d hex digits", first, 2*adapterIDBytes)
	}
}

func TestAdapterNamesDifferBetweenProfilesAndPrefixes(t *testing.T) {
	seen := map[string]tunnel.OwnerID{}
	for i := range 200 {
		owner := tunnel.OwnerID(fmt.Sprintf("profile-%d", i))
		name := adapterName(defaultAdapterPrefix, owner)
		if other, clash := seen[name]; clash {
			t.Fatalf("profiles %q and %q share the adapter name %q", other, owner, name)
		}
		seen[name] = owner
	}
	if adapterName("Plaitway-test-", "a") == adapterName(defaultAdapterPrefix, "a") {
		t.Error("a scratch prefix gives the production name")
	}
}

func TestAdapterGUIDIsStablePerProfileAndNamed(t *testing.T) {
	const owner = tunnel.OwnerID("profile-1")
	first := adapterGUID(owner)
	if again := adapterGUID(owner); again != first {
		t.Errorf("GUID changed between calls: %v, then %v", first, again)
	}
	if other := adapterGUID("profile-2"); other == first {
		t.Errorf("two profiles share the GUID %v", first)
	}
	if got := first.Data3 >> 12; got != 5 {
		t.Errorf("version nibble = %d, want 5 (name-based)", got)
	}
	if got := first.Data4[0] >> 6; got != 0b10 {
		t.Errorf("variant bits = %02b, want 10 (RFC 4122)", got)
	}
}

func TestAdapterGUIDIsNotTheNameHash(t *testing.T) {
	// The name is public in the network settings; the GUID must not be
	// recoverable from it.
	const owner = tunnel.OwnerID("profile-1")
	name := adapterName("", owner)
	if guid := fmt.Sprintf("%08x", adapterGUID(owner).Data1); guid == name {
		t.Errorf("GUID starts like the name hash: %s", guid)
	}
}
