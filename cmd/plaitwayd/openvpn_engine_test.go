package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/manager/fake"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// fakeOpenVPN is only ever copied and hashed, never run, except by the Unix
// tests that execute the copy.
const fakeOpenVPN = "#!/bin/sh\necho 'OpenVPN 2.7.7 fake'\n"

func sha256Hex(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

// installDirs makes a source binary the user could write to and a run
// directory the way the daemon makes it.
func installDirs(t *testing.T, content string) (source, runDir string) {
	t.Helper()
	dir := t.TempDir()
	source = filepath.Join(dir, "openvpn")
	if err := os.WriteFile(source, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return source, filepath.Join(dir, "run")
}

// The OpenVPN backend of a daemon that cannot trust its binary says why, which
// is what the profile's last_error and DaemonInfo show.
func TestUntrustedOpenVPNMakesTheBackendUnavailableWithTheReason(t *testing.T) {
	backends := openvpnUnavailable(fake.Backends(fake.Config{}), errors.New("openvpn at /x is not the binary this daemon was built for"))

	for _, b := range backends {
		info := b.Probe()
		switch b.Kind {
		case tunnel.KindOpenVPN:
			if info.Available || info.Detail != "openvpn at /x is not the binary this daemon was built for" {
				t.Errorf("OpenVPN: %+v", info)
			}
		default:
			if !info.Available {
				t.Errorf("kind %d was made unavailable: %+v", b.Kind, info)
			}
		}
	}
}
