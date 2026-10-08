package authenticode

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// The fixtures are programs of this machine that carry an embedded signature of
// Microsoft. Most of Windows itself is signed through a catalog and has none
// (see TestACatalogSignedFileHasNoEmbeddedSignature), so the list is of tools
// that Visual Studio and the .NET SDK install. Every test that needs one skips
// without it.
func embeddedSignedPrograms() []string {
	return []string{
		filepath.Join(os.Getenv("ProgramFiles"), "dotnet", "dotnet.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft Visual Studio", "Installer", "vswhere.exe"),
	}
}

func signedProgram(t *testing.T) string {
	t.Helper()
	for _, candidate := range embeddedSignedPrograms() {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	t.Skip("no program with an embedded Microsoft signature found")
	return ""
}

// openLikeTheEngines opens the file the way the callers do: shared for reading
// only, so that it cannot change while it is checked.
func openLikeTheEngines(t *testing.T, path string) windows.Handle {
	t.Helper()
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(path16, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { windows.CloseHandle(handle) })
	return handle
}

func verify(t *testing.T, path, signer string) error {
	t.Helper()
	return VerifySignedBy(openLikeTheEngines(t, path), path, signer)
}

func writeFile(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "program.exe")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func signerOf(t *testing.T, path string) string {
	t.Helper()
	name, err := signerName(path)
	if err != nil {
		t.Fatalf("signerName(%s): %v", path, err)
	}
	return name
}

func TestAnEmbeddedSignatureOfTheNamedSignerIsAccepted(t *testing.T) {
	path := signedProgram(t)
	name := signerOf(t, path)
	if !strings.Contains(name, "Microsoft") && name != ".NET" {
		t.Fatalf("the signer of %s reads %q, not a Microsoft name", path, name)
	}
	if err := verify(t, path, name); err != nil {
		t.Fatalf("%s signed by %q: %v", path, name, err)
	}
}

func TestAnotherSignerNameIsRefused(t *testing.T) {
	path := signedProgram(t)
	name := signerOf(t, path)
	err := verify(t, path, "Somebody Else Ltd.")
	want := `is signed by "` + name + `", not by "Somebody Else Ltd."`
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
	// The name is compared exactly, not as a part of the real one.
	if err := verify(t, path, name[:len(name)-1]); err == nil {
		t.Errorf("a prefix of the signer's name was accepted")
	}
}

func TestAChangedFileIsRefused(t *testing.T) {
	content, err := os.ReadFile(signedProgram(t))
	if err != nil {
		t.Fatal(err)
	}
	// A byte inside the code, far from the headers and the signature at the
	// end: the signature stays, the digest it covers does not.
	content[len(content)/4]++
	path := writeFile(t, content)
	err = verify(t, path, "anybody")
	if err == nil || err.Error() != "was changed after it was signed" {
		t.Errorf("error = %v, want the file called changed", err)
	}
}

func TestAFileWithoutASignatureIsRefused(t *testing.T) {
	garbage := make([]byte, 4096)
	if _, err := rand.Read(garbage); err != nil {
		t.Fatal(err)
	}
	self, err := os.ReadFile(os.Args[0]) // a PE file that nobody signed
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{"random bytes": garbage, "an unsigned program": self} {
		err := verify(t, writeFile(t, content), "anybody")
		if err == nil || err.Error() != "has no Authenticode signature" {
			t.Errorf("%s: error = %v, want the file called unsigned", name, err)
		}
	}
}

// Windows signs its own files through catalogs, which the check does not
// follow: a perfectly valid Microsoft file is refused, and that is wanted for
// the publishers that are checked (they sign their files).
func TestACatalogSignedFileHasNoEmbeddedSignature(t *testing.T) {
	path := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	if _, err := os.Stat(path); err != nil {
		t.Skip(err)
	}
	err := verify(t, path, "Microsoft Windows")
	if err == nil || err.Error() != "has no Authenticode signature" {
		t.Errorf("error = %v, want the catalog-signed file called unsigned", err)
	}
}

// vswhere.exe is signed with a certificate that has expired since; its
// signature carries a timestamp from when the certificate was valid, which is
// how wintun.dll is signed too. The engines need it accepted.
func TestAnExpiredCertificateWithATrustedTimestampIsAccepted(t *testing.T) {
	path := filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft Visual Studio", "Installer", "vswhere.exe")
	if _, err := os.Stat(path); err != nil {
		t.Skip("vswhere.exe is not installed")
	}
	var expiry time.Time
	err := withSignerCertificate(path, func(cert *windows.CertContext) error {
		expiry = time.Unix(0, cert.CertInfo.NotAfter.Nanoseconds())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if expiry.After(time.Now()) {
		t.Skipf("the certificate of %s has not expired (it is valid until %s), so it shows nothing", path, expiry)
	}
	if err := verify(t, path, signerOf(t, path)); err != nil {
		t.Errorf("the certificate expired on %s, the timestamp is trusted: %v", expiry.Format(time.DateOnly), err)
	}
}

// Whether the check asks the network is not something a test can watch from
// outside; this holds the request to what the engines depend on: nothing is
// looked up, not for the file and not for the chain.
func TestTheCheckAsksForNoRevocationLookup(t *testing.T) {
	data := trustDataFor(&windows.WinTrustFileInfo{})
	if data.RevocationChecks != windows.WTD_REVOKE_NONE {
		t.Errorf("RevocationChecks = %#x, want WTD_REVOKE_NONE", data.RevocationChecks)
	}
	if data.ProvFlags&windows.WTD_REVOCATION_CHECK_NONE == 0 {
		t.Errorf("ProvFlags = %#x, want WTD_REVOCATION_CHECK_NONE", data.ProvFlags)
	}
	if data.ProvFlags&windows.WTD_CACHE_ONLY_URL_RETRIEVAL == 0 {
		t.Errorf("ProvFlags = %#x, want WTD_CACHE_ONLY_URL_RETRIEVAL: no download of a chain", data.ProvFlags)
	}
	if data.UIChoice != windows.WTD_UI_NONE {
		t.Errorf("UIChoice = %#x, want no user interface: the daemon has no desktop", data.UIChoice)
	}
}

func TestTheAnswersOfTheSystemAreDescribed(t *testing.T) {
	tests := []struct {
		err  windows.Errno
		want string
	}{
		{windows.Errno(windows.TRUST_E_NOSIGNATURE), "has no Authenticode signature"},
		{windows.Errno(windows.TRUST_E_SUBJECT_FORM_UNKNOWN), "has no Authenticode signature"},
		{windows.Errno(windows.TRUST_E_BAD_DIGEST), "was changed after it was signed"},
		{windows.Errno(windows.CERT_E_UNTRUSTEDROOT), "is signed by a publisher this machine does not trust"},
		{windows.Errno(windows.CERT_E_CHAINING), "is signed by a publisher this machine does not trust"},
		{windows.Errno(windows.TRUST_E_EXPLICIT_DISTRUST), "is signed by a publisher this machine does not trust"},
		{windows.Errno(windows.CERT_E_REVOKED), "is signed with a revoked certificate"},
		{windows.ERROR_ACCESS_DENIED, "has no valid Authenticode signature"},
	}
	for _, tt := range tests {
		if got := describeTrustError(tt.err); !strings.HasPrefix(got.Error(), tt.want) {
			t.Errorf("describeTrustError(%v) = %q, want it to start with %q", tt.err, got, tt.want)
		}
	}
}
