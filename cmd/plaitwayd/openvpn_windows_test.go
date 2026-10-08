package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/sys/windows"
)

const (
	testProgramFiles   = `C:\Program Files`
	testPlaitwayFolder = `C:\Program Files\Plaitway`
	bundledOpenVPNExe  = `C:\Program Files\Plaitway\openvpn\bin\openvpn.exe`
	installedOpenVPN   = `C:\Program Files\OpenVPN\bin\openvpn.exe`
)

func TestOpenVPNPathsFollowTheLayoutOfTheirInstallers(t *testing.T) {
	if got := bundledOpenVPNPath(testPlaitwayFolder); got != bundledOpenVPNExe {
		t.Errorf("bundled copy %q, want %q", got, bundledOpenVPNExe)
	}
	if got := installedOpenVPNPath(testProgramFiles); got != installedOpenVPN {
		t.Errorf("installation %q, want %q", got, installedOpenVPN)
	}
}

func TestChooseOpenVPNTakesTheBundledCopyThenTheInstallation(t *testing.T) {
	tests := []struct {
		name         string
		executable   string
		programFiles string
		existing     []string
		want         string
	}{
		{"both exist: the bundled copy is the one the installer ships", testPlaitwayFolder, testProgramFiles, []string{bundledOpenVPNExe, installedOpenVPN}, bundledOpenVPNExe},
		{"only the installation exists", testPlaitwayFolder, testProgramFiles, []string{installedOpenVPN}, installedOpenVPN},
		{"only the bundled copy exists", testPlaitwayFolder, testProgramFiles, []string{bundledOpenVPNExe}, bundledOpenVPNExe},
		{"neither exists: the reason names the standard place", testPlaitwayFolder, testProgramFiles, nil, installedOpenVPN},
		{"no Program Files: the bundled path is all there is", testPlaitwayFolder, "", nil, bundledOpenVPNExe},
		{"no executable folder: the installation is all there is", "", testProgramFiles, nil, installedOpenVPN},
		{"no folder at all", "", "", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exists := func(path string) bool { return slices.Contains(tt.existing, path) }
			if got := chooseOpenVPN(tt.executable, tt.programFiles, exists); got != tt.want {
				t.Errorf("chooseOpenVPN = %q, want %q", got, tt.want)
			}
		})
	}
}
func TestOpenVPNMayExistIsFalseOnlyForAPathThatIsNotThere(t *testing.T) {
	present := filepath.Join(t.TempDir(), openvpnFileName)
	if err := os.WriteFile(present, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !openvpnMayExist(present) {
		t.Error("a file that is there was taken for missing")
	}
	if openvpnMayExist(filepath.Join(t.TempDir(), "missing", openvpnFileName)) {
		t.Error("a path that is not there was taken for present")
	}
}

// Nothing is bundled next to the test binary, so the default is the standard
// installation below the Program Files folder that the shell names, and that is
// what the -openvpn flag shows. An explicit flag wins.
func TestOpenVPNFlagDefaultIsTheStandardInstallationWhenNothingIsBundled(t *testing.T) {
	programFiles, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, windows.KF_FLAG_DEFAULT)
	if err != nil {
		t.Fatal(err)
	}
	want := installedOpenVPNPath(programFiles)

	cfg, _, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.openvpn != want || defaultOpenVPN() != want {
		t.Fatalf("-openvpn defaults to %q (defaultOpenVPN %q), want %q", cfg.openvpn, defaultOpenVPN(), want)
	}
	if !filepath.IsAbs(cfg.openvpn) {
		t.Errorf("the default %q is not an absolute path, which the engine refuses", cfg.openvpn)
	}

	explicit := `D:\tools\openvpn.exe`
	cfg, _, err = parseFlags([]string{"-openvpn", explicit})
	if err != nil || cfg.openvpn != explicit {
		t.Fatalf("-openvpn %q gave %q, %v", explicit, cfg.openvpn, err)
	}
}

// The engine judges the binary where it is, before every probe and every
// start, so the daemon passes the path on and never copies the file.
func TestTrustedOpenVPNPassesThePathOnAndLeavesTheRunDirectoryAlone(t *testing.T) {
	source, runDir := installDirs(t, fakeOpenVPN)
	defer func(old string) { openvpnSHA256 = old }(openvpnSHA256)
	openvpnSHA256 = sha256Hex(fakeOpenVPN)

	got, err := trustedOpenVPN(source, runDir)
	if err != nil || got != source {
		t.Fatalf("trustedOpenVPN = %q, %v; want the configured path", got, err)
	}
	if _, err := os.Stat(runDir); err == nil {
		t.Error("the run directory was made for a copy that Windows does not need")
	}
}
