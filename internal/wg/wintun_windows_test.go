package wg

import (
	"bytes"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wintun"
)

const (
	// signedWintunEnv names the signed wintun.dll the tests that need a real
	// one use; without it they look where packaging/windows/fetch-wintun.ps1
	// puts it for a build: bin/wintun/wintun.dll below the repository.
	signedWintunEnv      = "PLAITWAY_WINTUN_DLL"
	fetchedWintunDefault = "../../bin/wintun/wintun.dll"

	// searchOrderEnv, set, makes TestSearchOrderChild do its work; it names the
	// folder that holds a decoy wintun.dll.
	searchOrderEnv = "PLAITWAY_WG_SEARCH_ORDER_DECOY_DIR"

	// decoyStandIn is the system DLL whose copy plays the decoy: loading it is
	// harmless.
	decoyStandIn = "version.dll"
)

// signedWintun returns the path of a real, signed wintun.dll, or skips.
func signedWintun(t *testing.T) string {
	t.Helper()
	path := os.Getenv(signedWintunEnv)
	if path == "" {
		path = fetchedWintunDefault
	}
	path, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != wintunDLLName {
		t.Fatalf("%s=%s: the file must be named %s", signedWintunEnv, path, wintunDLLName)
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no signed %s to test with (%v); run packaging\\windows\\fetch-wintun.ps1 -OutputDirectory bin\\wintun or set %s", wintunDLLName, err, signedWintunEnv)
	}
	return path
}

// useExecutableDir makes dir the folder wintun.dll is looked for in and forgets
// what an earlier test loaded, so that each test checks the folder it names.
func useExecutableDir(t *testing.T, dir string) {
	t.Helper()
	loadedWintun.mu.Lock()
	previous := loadedWintun.module
	loadedWintun.module = 0
	loadedWintun.mu.Unlock()
	original := executableDir
	executableDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() {
		executableDir = original
		loadedWintun.mu.Lock()
		defer loadedWintun.mu.Unlock()
		if loadedWintun.module == 0 {
			loadedWintun.module = previous
		}
	})
}

func writeWintunFile(t *testing.T, content []byte) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, wintunDLLName), content, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func probeIn(t *testing.T, dir string) (available bool, detail string) {
	t.Helper()
	useExecutableDir(t, dir)
	info := Backend(Config{}).Probe()
	return info.Available, info.Detail
}

func wantUnavailable(t *testing.T, dir, wantDetail string) {
	t.Helper()
	available, detail := probeIn(t, dir)
	if available {
		t.Fatalf("Probe is available, want it refused with %q", wantDetail)
	}
	if !strings.Contains(detail, wantDetail) {
		t.Fatalf("Detail = %q, want it to contain %q", detail, wantDetail)
	}
	if !strings.Contains(detail, filepath.Join(dir, wintunDLLName)) {
		t.Errorf("Detail = %q, want it to name the file", detail)
	}
}

func TestProbeSaysWhereTheMissingWintunShouldBe(t *testing.T) {
	wantUnavailable(t, t.TempDir(), "is missing")
}

func TestProbeRefusesAFolderNamedWintun(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, wintunDLLName), 0o700); err != nil {
		t.Fatal(err)
	}
	available, detail := probeIn(t, dir)
	if available || !strings.Contains(detail, filepath.Join(dir, wintunDLLName)) {
		t.Fatalf("Probe = %v, %q; want a refusal that names the folder", available, detail)
	}
}

func TestProbeRefusesAFileThatIsNotSigned(t *testing.T) {
	garbage := make([]byte, 4096)
	if _, err := rand.Read(garbage); err != nil {
		t.Fatal(err)
	}
	wantUnavailable(t, writeWintunFile(t, garbage), "has no Authenticode signature")
}

func TestProbeRefusesAnUnsignedProgram(t *testing.T) {
	// This test binary is a PE file with no signature.
	self, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	wantUnavailable(t, writeWintunFile(t, self), "has no Authenticode signature")
}

// microsoftSignedProgram finds a program that carries its own valid Microsoft
// signature, to stand for a publisher that Windows trusts but that is not the
// one wintun.dll must come from.
func microsoftSignedProgram(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{
		filepath.Join(os.Getenv("ProgramFiles"), "dotnet", "dotnet.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "Windows Defender", "MpCmdRun.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft Visual Studio", "Installer", "vswhere.exe"),
	} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	t.Skip("no program with an embedded Microsoft signature found")
	return ""
}

func TestProbeRefusesAValidSignatureOfAnotherPublisher(t *testing.T) {
	program, err := os.ReadFile(microsoftSignedProgram(t))
	if err != nil {
		t.Fatal(err)
	}
	available, detail := probeIn(t, writeWintunFile(t, program))
	if available {
		t.Fatal("a Microsoft-signed file passed as wintun.dll")
	}
	if !strings.Contains(detail, ` is signed by "`) || !strings.Contains(detail, `not by "WireGuard LLC"`) {
		t.Fatalf("Detail = %q, want the signer named and WireGuard LLC expected", detail)
	}
}

func TestProbeRefusesAChangedDLL(t *testing.T) {
	real, err := os.ReadFile(signedWintun(t))
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Clone(real)
	changed[len(changed)/4]++ // inside the code, far from the signature
	wantUnavailable(t, writeWintunFile(t, changed), "was changed after it was signed")
}

func TestProbeAcceptsTheSignedDLLAndLoadsThatFile(t *testing.T) {
	path := signedWintun(t)
	useExecutableDir(t, filepath.Dir(path))

	info := Backend(Config{}).Probe()
	if !info.Available || info.Detail != "" {
		t.Fatalf("Probe = %+v, want the signed wintun.dll accepted", info)
	}
	if !strings.Contains(info.Version, "wintun 0.14") {
		t.Errorf("Version = %q, want the wintun version in it", info.Version)
	}

	loadedWintun.mu.Lock()
	module := loadedWintun.module
	loadedWintun.mu.Unlock()
	var name [windows.MAX_PATH]uint16
	if _, err := windows.GetModuleFileName(module, &name[0], uint32(len(name))); err != nil {
		t.Fatal(err)
	}
	if got := windows.UTF16ToString(name[:]); !strings.EqualFold(got, path) {
		t.Errorf("the loaded module is %s, want %s", got, path)
	}
	// wireguard-go's module asks for "wintun.dll" by name, in a process whose
	// application directory and system32 have none; it can only succeed by
	// finding the module that was loaded and checked above.
	if version := wintun.Version(); version == "unknown" {
		t.Error("golang.zx2c4.com/wintun does not find the module that was loaded and checked")
	}
}

// TestSearchOrderChild runs only as the child of
// TestWintunIsNotLoadedFromTheWorkingDirectoryOrPath: it needs a process that
// has not loaded wintun.dll.
func TestSearchOrderChild(t *testing.T) {
	decoyDir := os.Getenv(searchOrderEnv)
	if decoyDir == "" {
		t.Skip("runs only as the child of TestWintunIsNotLoadedFromTheWorkingDirectoryOrPath")
	}
	if err := os.Chdir(decoyDir); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", decoyDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// The decoy is in the working directory and first on PATH. The loader of
	// golang.zx2c4.com/wintun searches the application directory and system32
	// only, so it must not find it.
	if version := wintun.Version(); version != "unknown" {
		t.Errorf("wintun.Version() = %q, want \"unknown\": the DLL was found", version)
	}
	name16, err := windows.UTF16PtrFromString(wintunDLLName)
	if err != nil {
		t.Fatal(err)
	}
	var module windows.Handle
	if err := windows.GetModuleHandleEx(windows.GET_MODULE_HANDLE_EX_FLAG_UNCHANGED_REFCOUNT, name16, &module); err == nil {
		t.Errorf("a module named %s is loaded after wintun's loader ran", wintunDLLName)
	}

	// The control: the default search order of LoadLibrary does find the decoy
	// in the working directory, so the check above could have failed.
	control, err := windows.LoadLibraryEx(wintunDLLName, 0, 0)
	if err != nil {
		t.Fatalf("control: the decoy is not found even by the default search order: %v", err)
	}
	windows.FreeLibrary(control)
}

func TestWintunIsNotLoadedFromTheWorkingDirectoryOrPath(t *testing.T) {
	stand := filepath.Join(os.Getenv("SystemRoot"), "System32", decoyStandIn)
	content, err := os.ReadFile(stand)
	if err != nil {
		t.Skipf("no stand-in DLL: %v", err)
	}
	decoyDir := writeWintunFile(t, content)

	cmd := exec.Command(os.Args[0], "-test.run=^TestSearchOrderChild$", "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(), searchOrderEnv+"="+decoyDir)
	out, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(out, []byte("--- PASS: TestSearchOrderChild")) {
		t.Fatalf("child: %v\n%s", err, out)
	}
}
