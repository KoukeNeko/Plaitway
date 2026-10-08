//go:build unix

package ovpn

import (
	"os"
	"path/filepath"
	"testing"
)

// The modes are what keeps other users away from the management socket, which
// openvpn creates world-accessible. Windows has no mode bits to check.
func TestPrepareWorkspaceModes(t *testing.T) {
	runDir := filepath.Join(shortTempDir(t), "run")
	e := newTestEngine(t, runDir, minimalProfile)
	if err := e.prepareWorkspace(); err != nil {
		t.Fatal(err)
	}
	defer e.removeWorkspace()

	for path, want := range map[string]os.FileMode{runDir: 0o700, e.dir: 0o700, e.configPath: 0o600} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode = %v, want %v", path, info.Mode().Perm(), want)
		}
	}
}

func TestPrepareWorkspaceTightensAStaleDirectory(t *testing.T) {
	e := newTestEngine(t, shortTempDir(t), minimalProfile)
	if err := os.MkdirAll(e.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.prepareWorkspace(); err != nil {
		t.Fatal(err)
	}
	defer e.removeWorkspace()

	info, err := os.Stat(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("mode = %v", info.Mode().Perm())
	}
}
