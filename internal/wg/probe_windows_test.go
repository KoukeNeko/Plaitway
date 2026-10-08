package wg

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

// The test binary has no wintun.dll beside it, so the engine is unavailable,
// and the reason is the first thing the operator needs.
func TestProbeWithoutWintunBesideTheExecutable(t *testing.T) {
	useExecutableDir(t, t.TempDir())
	info := Backend(Config{}).Probe()
	if info.Available {
		t.Fatalf("Probe() = %+v, want the engine unavailable without wintun.dll", info)
	}
	if !strings.Contains(info.Detail, wintunDLLName) {
		t.Errorf("Detail = %q, want it to name %s", info.Detail, wintunDLLName)
	}
	build, ok := debug.ReadBuildInfo()
	if !ok {
		t.Skip("no build info in this binary")
	}
	for _, dep := range build.Deps {
		if dep.Path == modulePath {
			if info.Version != dep.Version {
				t.Errorf("Version = %q, want the module version %q while wintun is not loaded", info.Version, dep.Version)
			}
			return
		}
	}
	t.Skip("wireguard-go is not in the build info of this test binary")
}

func TestExecutableDirIsTheFolderOfTheProgram(t *testing.T) {
	dir, err := executableDir()
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Dir(exe); dir != want {
		t.Errorf("executableDir = %q, want %q", dir, want)
	}
}
