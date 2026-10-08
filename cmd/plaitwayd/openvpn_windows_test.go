package main

import (
	"strings"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/manager/fake"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// The verified copy of openvpn is a Unix mechanism (owner and mode of the run
// directory); the Windows equivalent is a later task. Until then a build that
// pins the hash must say that it cannot trust any openvpn, not crash or run the
// unverified binary.
func TestOpenVPNWithABuildTimeHashIsUnavailableWithAReasonOnWindows(t *testing.T) {
	source, runDir := installDirs(t, fakeOpenVPN)
	defer func(old string) { openvpnSHA256 = old }(openvpnSHA256)
	openvpnSHA256 = sha256Hex(fakeOpenVPN)

	path, err := trustedOpenVPN(source, runDir)
	if err == nil || path != "" {
		t.Fatalf("trustedOpenVPN = %q, %v; want no path and a reason", path, err)
	}
	if !strings.Contains(err.Error(), "run directory") {
		t.Errorf("reason %q does not name what could not be verified", err)
	}

	for _, b := range openvpnUnavailable(fake.Backends(fake.Config{}), err) {
		if b.Kind != tunnel.KindOpenVPN {
			continue
		}
		if info := b.Probe(); info.Available || info.Detail != err.Error() {
			t.Errorf("OpenVPN backend: %+v, want it unavailable because of %q", info, err)
		}
	}
}
