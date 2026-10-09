package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirstExistingIsTheFirstCandidateThatIsThere(t *testing.T) {
	dir := t.TempDir()
	first, second, third := filepath.Join(dir, "a"), filepath.Join(dir, "b"), filepath.Join(dir, "c")
	candidates := []string{first, second, third}

	if got := firstExisting(candidates); got != first {
		t.Errorf("with none there: %q, want the first candidate %q", got, first)
	}
	for _, path := range []string{third, second} {
		if err := os.WriteFile(path, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got := firstExisting(candidates); got != second {
		t.Errorf("with the second and third there: %q, want %q", got, second)
	}
	if err := os.WriteFile(first, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := firstExisting(candidates); got != first {
		t.Errorf("with all there: %q, want %q", got, first)
	}
}

// The binary comes from the distribution, never from $PATH.
func TestDefaultOpenVPNIsOneOfTheDistributionPaths(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	want := []string{"/usr/sbin/openvpn", "/usr/bin/openvpn", "/usr/local/sbin/openvpn"}
	if strings.Join(openvpnCandidates, " ") != strings.Join(want, " ") {
		t.Fatalf("candidates %v, want %v", openvpnCandidates, want)
	}
	cfg, _, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.openvpn != defaultOpenVPN() {
		t.Fatalf("-openvpn defaults to %q, defaultOpenVPN is %q", cfg.openvpn, defaultOpenVPN())
	}
	found := false
	for _, path := range want {
		found = found || cfg.openvpn == path
	}
	if !found {
		t.Fatalf("-openvpn defaults to %q, want one of %v", cfg.openvpn, want)
	}
}

// Without a build-time hash the binary is run where it is, but only when root
// owns it and everything above it.
func TestOpenVPNWithoutABuildTimeHashIsTrustedInPlaceOnlyWhenRootOwnsIt(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("what root creates is owned by root")
	}
	source, runDir := installDirs(t, fakeOpenVPN)

	got, err := trustedOpenVPN(source, runDir)
	if err == nil || !strings.Contains(err.Error(), source) || !strings.Contains(err.Error(), "not to root") {
		t.Fatalf("trustedOpenVPN = %q, %v; want a refusal that names %s", got, err, source)
	}
	if _, err := os.Stat(runDir); err == nil {
		t.Error("the run directory was touched, but nothing is copied without a hash")
	}
}

// The openvpn of this machine's distribution is the case the check exists for.
func TestTheOpenVPNOfTheDistributionIsTrusted(t *testing.T) {
	source := defaultOpenVPN()
	if _, err := os.Stat(source); err != nil {
		t.Skipf("openvpn is not installed: %v", err)
	}
	got, err := trustedOpenVPN(source, t.TempDir())
	if err != nil {
		t.Fatalf("trustedOpenVPN(%s): %v", source, err)
	}
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil || got != resolved {
		t.Fatalf("trustedOpenVPN = %q, want %q (%v)", got, resolved, err)
	}
}
