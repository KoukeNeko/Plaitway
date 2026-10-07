//go:build unix

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/manager/fake"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

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

func directoryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestInstallOpenVPNCopiesTheBinaryThatMatches(t *testing.T) {
	source, runDir := installDirs(t, fakeOpenVPN)

	path, err := installOpenVPN(source, runDir, strings.ToUpper(sha256Hex(fakeOpenVPN)))
	if err != nil {
		t.Fatal(err)
	}

	if path != filepath.Join(runDir, "openvpn") {
		t.Fatalf("path = %s, want the copy in the run directory", path)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode() != 0o755 {
		t.Fatalf("the copy: %v, %v; want mode 0755", fi, err)
	}
	if fi, err := os.Stat(runDir); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("run directory: %v, %v; want mode 0755", fi, err)
	}
	if got := directoryNames(t, runDir); len(got) != 1 {
		t.Fatalf("run directory holds %v, want only the copy", got)
	}

	// What is executed is the copy: changing the source afterwards changes nothing.
	if err := os.WriteFile(source, []byte("#!/bin/sh\necho evil\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(path).Output()
	if err != nil || !strings.HasPrefix(string(out), "OpenVPN 2.7.7") {
		t.Fatalf("running the copy: %q, %v", out, err)
	}
}

func TestInstallOpenVPNRefusesAnotherBinary(t *testing.T) {
	source, runDir := installDirs(t, "#!/bin/sh\necho evil\n")

	_, err := installOpenVPN(source, runDir, sha256Hex(fakeOpenVPN))
	if err == nil || !strings.Contains(err.Error(), "not the binary this daemon was built for") {
		t.Fatalf("err = %v, want it to say that the binary is not the expected one", err)
	}
	if got := directoryNames(t, runDir); len(got) != 0 {
		t.Fatalf("a rejected binary left %v in the run directory", got)
	}
}

func TestInstallOpenVPNReplacesAnEarlierCopy(t *testing.T) {
	source, runDir := installDirs(t, fakeOpenVPN)
	if err := os.Mkdir(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "openvpn"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	path, err := installOpenVPN(source, runDir, sha256Hex(fakeOpenVPN))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != fakeOpenVPN {
		t.Fatalf("the copy holds %q, %v", got, err)
	}
}

func TestInstallOpenVPNReportsWhatIsWrong(t *testing.T) {
	source, runDir := installDirs(t, fakeOpenVPN)
	want := sha256Hex(fakeOpenVPN)

	if _, err := installOpenVPN("", runDir, want); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("no path: %v", err)
	}
	if _, err := installOpenVPN(source+".missing", runDir, want); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing binary: %v", err)
	}
	if _, err := installOpenVPN(filepath.Dir(source), runDir, want); err == nil {
		t.Error("a directory was accepted as the binary")
	}

	// A run directory that others can write to could have the copy replaced
	// after it was verified.
	if err := os.Chmod(runDir, 0o777); err != nil { // made by the calls above
		t.Fatal(err)
	}
	if _, err := installOpenVPN(source, runDir, want); err == nil || !strings.Contains(err.Error(), "run directory") {
		t.Errorf("run directory writable by everyone: %v", err)
	}
}

func TestOpenVPNIsUsedAsConfiguredWithoutABuildTimeHash(t *testing.T) {
	source, runDir := installDirs(t, fakeOpenVPN)

	got, err := trustedOpenVPN(source, runDir)
	if err != nil || got != source {
		t.Fatalf("trustedOpenVPN = %q, %v; want the configured path", got, err)
	}
	if _, err := os.Stat(runDir); err == nil {
		t.Error("a development build touched the run directory")
	}
}

func TestOpenVPNWithABuildTimeHashRunsTheVerifiedCopy(t *testing.T) {
	source, runDir := installDirs(t, fakeOpenVPN)
	defer func(old string) { openvpnSHA256 = old }(openvpnSHA256)

	openvpnSHA256 = sha256Hex(fakeOpenVPN)
	got, err := trustedOpenVPN(source, runDir)
	if err != nil || got != filepath.Join(runDir, "openvpn") {
		t.Fatalf("trustedOpenVPN = %q, %v; want the copy", got, err)
	}

	openvpnSHA256 = sha256Hex("something else")
	if _, err := trustedOpenVPN(source, runDir); err == nil {
		t.Fatal("a binary that does not match the build-time hash was accepted")
	}
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

// The real binary, which is signed, runs from its copy. Skipped when it is not
// built; it is looked up in build/openvpn/bin or in $PLAITWAY_OPENVPN.
func TestRealOpenVPNRunsFromItsCopy(t *testing.T) {
	source := os.Getenv("PLAITWAY_OPENVPN")
	if source == "" {
		source = filepath.Join("..", "..", "build", "openvpn", "bin", "openvpn")
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Skipf("real openvpn binary not found: %v", err)
	}
	sum := sha256.Sum256(data)

	path, err := installOpenVPN(source, filepath.Join(shortDir(t), "run"), hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	// openvpn --version exits with status 1 after printing; the output decides.
	out, _ := exec.Command(path, "--version").CombinedOutput()
	if !strings.HasPrefix(string(out), "OpenVPN ") {
		t.Fatalf("%s --version printed %q", path, out)
	}
}
