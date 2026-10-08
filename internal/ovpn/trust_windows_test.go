package ovpn

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/KoukeNeko/Plaitway/internal/authenticode"
	"github.com/KoukeNeko/Plaitway/internal/fsperm"
)

// installedOpenVPN is the official installation of the machine, if any. The
// checks run against the real thing: its directory, its signature, its hash.
func installedOpenVPN(t *testing.T) string {
	t.Helper()
	path := installedBinary()
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no OpenVPN installation at %s", path)
	}
	if err := fsperm.CheckAdminOnlyPath(path); err != nil {
		t.Skipf("the installation is not in a place only administrators write, so it would not be trusted either: %v", err)
	}
	return path
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestVerifyBinaryAcceptsTheInstalledOpenVPN(t *testing.T) {
	path := installedOpenVPN(t)
	if err := VerifyBinary(path, ""); err != nil {
		t.Fatalf("VerifyBinary = %v", err)
	}
	if err := VerifyBinary(path, fileSHA256(t, path)); err != nil {
		t.Errorf("VerifyBinary with its own hash = %v", err)
	}
	if err := VerifyBinary(path, strings.ToUpper(fileSHA256(t, path))); err != nil {
		t.Errorf("VerifyBinary with its own hash in capitals = %v", err)
	}
	if err := VerifyBinary(tapctlPath(path), ""); err != nil {
		t.Errorf("tapctl = %v", err)
	}
}

func TestVerifyBinaryPinsTheHash(t *testing.T) {
	path := installedOpenVPN(t)
	wrong := strings.Repeat("0", 64)
	err := VerifyBinary(path, wrong)
	if err == nil || !strings.Contains(err.Error(), "not the binary this daemon was built for") || !strings.Contains(err.Error(), wrong) {
		t.Errorf("VerifyBinary with another hash = %v", err)
	}
}

// A copy of a perfectly good, signed openvpn is not trusted where a standard
// user can replace it.
func TestVerifyBinaryRefusesAPlaceAUserCanWrite(t *testing.T) {
	path := installedOpenVPN(t)
	copied := filepath.Join(t.TempDir(), "openvpn.exe")
	if err := copyExecutable(path, copied); err != nil {
		t.Fatal(err)
	}
	err := VerifyBinary(copied, "")
	if !errors.Is(err, fsperm.ErrWritableByStandardUsers) && !errors.Is(err, fsperm.ErrUntrustedOwner) {
		t.Fatalf("VerifyBinary(copy in a user's temp directory) = %v, want a refusal for who can write it", err)
	}
	if info := inspectBinary(Config{Binary: copied}, 10*time.Second); info.available || !strings.HasPrefix(info.detail, "openvpn is not trusted: ") {
		t.Errorf("inspectBinary = %+v, want it unavailable with the reason", info)
	}
	if release, err := trustBinary(Config{Binary: copied}); err == nil {
		release()
		t.Error("the start of the process would have gone ahead")
	}
}

// Writable only by administrators is not enough: a file the right accounts own
// but nobody signed must not run either.
func TestVerifyBinaryRefusesAProgramOpenVPNIncDidNotSign(t *testing.T) {
	cmd := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	if err := fsperm.CheckAdminOnlyPath(cmd); err != nil {
		t.Skipf("cmd.exe is not in a place only administrators write: %v", err)
	}
	err := VerifyBinary(cmd, "")
	if err == nil {
		t.Fatal("cmd.exe was trusted as openvpn")
	}
	if errors.Is(err, fsperm.ErrWritableByStandardUsers) {
		t.Fatalf("cmd.exe stopped at the access list, which it passes: %v", err)
	}
	t.Logf("refused: %v", err)
}

func TestVerifySignedByNamesTheSigner(t *testing.T) {
	path := installedOpenVPN(t)
	file, err := openLocked(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	handle := windows.Handle(file.Fd())
	if err := authenticode.VerifySignedBy(handle, path, openvpnSigner); err != nil {
		t.Fatalf("the signer is %q: %v", openvpnSigner, err)
	}
	err = authenticode.VerifySignedBy(handle, path, "Somebody Else Ltd.")
	if err == nil || !strings.Contains(err.Error(), `is signed by "`+openvpnSigner+`", not by "Somebody Else Ltd."`) {
		t.Errorf("VerifySignedBy with another signer = %v", err)
	}
}

func TestVerifyBinaryRefusesWhatIsNotAnAbsolutePathOrNotThere(t *testing.T) {
	tests := map[string]string{
		"":                               "not configured",
		"openvpn.exe":                    "not an absolute path",
		`bin\openvpn.exe`:                "not an absolute path",
		`C:\Windows\Nothing\openvpn.exe`: "openvpn not found at",
	}
	for path, want := range tests {
		if err := VerifyBinary(path, ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("VerifyBinary(%q) = %v, want it to say %q", path, err, want)
		}
	}
	dir := filepath.Join(os.Getenv("SystemRoot"), "System32")
	if err := VerifyBinary(dir, ""); err == nil {
		t.Error("a directory was trusted as openvpn")
	}
}

// The file is held so that nobody can change it between the check and the
// start of the process.
func TestOpenLockedKeepsWritersAndDeletersOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "program.exe")
	if err := os.WriteFile(path, []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := openLocked(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
		t.Error("the file was opened for writing while locked")
	}
	if err := os.Remove(path); err == nil {
		t.Error("the file was deleted while locked")
	}
	if err := os.Rename(path, path+".moved"); err == nil {
		t.Error("the file was renamed while locked")
	}
	if reader, err := os.Open(path); err != nil {
		t.Errorf("reading was refused: %v", err)
	} else {
		reader.Close()
	}
	file.Close()
	if err := os.WriteFile(path, []byte("MZ2"), 0o755); err != nil {
		t.Errorf("the file is still locked after the release: %v", err)
	}
}

func TestInspectBinaryReportsTheInstalledOpenVPN(t *testing.T) {
	path := installedOpenVPN(t)
	info := inspectBinary(Config{Binary: path}, 20*time.Second)
	if !info.available {
		t.Fatalf("inspectBinary = %+v", info)
	}
	if info.driver != "tap-windows6" || info.major < 2 || !info.lzo {
		t.Errorf("inspectBinary = %+v, want openvpn 2.x with LZO on the tap-windows6 driver", info)
	}
	reported := newBackend(Config{Binary: path}).probeInfo()
	if !reported.Available || !strings.HasSuffix(reported.Version, " (tap-windows6)") || reported.Detail != "" {
		t.Errorf("Probe = %+v", reported)
	}
	if hashed := inspectBinary(Config{Binary: path, BinarySHA256: strings.Repeat("0", 64)}, 20*time.Second); hashed.available || !strings.Contains(hashed.detail, "not the binary this daemon was built for") {
		t.Errorf("inspectBinary with the wrong hash = %+v", hashed)
	}
}

func TestCheckAdapterToolingNamesWhatIsMissing(t *testing.T) {
	path := installedOpenVPN(t)
	if err := checkAdapterTooling(path); err != nil {
		t.Skipf("the machine lacks the TAP-Windows6 driver: %v", err)
	}
	// A directory with openvpn.exe and no tapctl.exe: build it from a path that
	// does not exist, which fails the same way the missing file does.
	err := checkAdapterTooling(`C:\Windows\Nothing\openvpn.exe`)
	if err == nil || !strings.Contains(err.Error(), "tapctl is not trusted") {
		t.Errorf("checkAdapterTooling(without tapctl) = %v", err)
	}
}
