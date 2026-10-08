//go:build unix

package ovpn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The golden text of the command line on a system where the management
// interface is a Unix socket and openvpn owns its tunnel interface.
func TestBuildArgsGolden(t *testing.T) {
	const config, socket = "/var/run/plaitway/ovpn-0123456789ab/profile.ovpn", "/var/run/plaitway/ovpn-0123456789ab/m.sock"
	tests := []struct {
		golden string
		bin    binaryInfo
	}{
		{"args-2.7.golden", binaryInfo{available: true, major: 2, minor: 7}},
		{"args-2.6.golden", binaryInfo{available: true, major: 2, minor: 6}},
		{"args-2.5.golden", binaryInfo{available: true, major: 2, minor: 5}},
	}
	for _, tt := range tests {
		t.Run(tt.golden, func(t *testing.T) {
			got := strings.Join(buildArgs(tt.bin, config, unixManagementOptions(socket), nil), "\n") + "\n"
			checkGolden(t, tt.golden, []byte(got))
		})
	}
}

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
