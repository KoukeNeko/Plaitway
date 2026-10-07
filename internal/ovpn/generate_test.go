package ovpn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

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
			got := strings.Join(buildArgs(tt.bin, config, socket), "\n") + "\n"
			checkGolden(t, tt.golden, []byte(got))
		})
	}
}

// The requirements of the command line, independent of the golden text.
func TestBuildArgsContract(t *testing.T) {
	args := buildArgs(binaryInfo{available: true, major: 2, minor: 7}, "/c/profile.ovpn", "/c/m.sock")
	has := func(flag string) bool {
		for _, a := range args {
			if a == flag {
				return true
			}
		}
		return false
	}
	for _, flag := range []string{
		"--management-hold", "--management-query-passwords", "--management-up-down",
		"--route-noexec", "--persist-tun", "--disable-dco", "--dns-updown",
	} {
		if !has(flag) {
			t.Errorf("command line lacks %s: %v", flag, args)
		}
	}
	if has("--persist-key") {
		t.Error("2.7 ignores --persist-key with a deprecation notice; it must not be passed")
	}
	if args[0] != "--config" || args[1] != "/c/profile.ovpn" {
		t.Errorf("the profile must come first so the daemon's options override it: %v", args)
	}
	for i, a := range args {
		if a == "--management" && (args[i+1] != "/c/m.sock" || args[i+2] != "unix") {
			t.Errorf("--management arguments = %v", args[i:i+3])
		}
		if a == "--script-security" && args[i+1] != "1" {
			t.Errorf("script-security = %s, want 1", args[i+1])
		}
		for _, secret := range []string{"user", "pass", "auth-user-pass"} {
			if strings.Contains(a, secret) && a != "--management-query-passwords" {
				t.Errorf("argument %q looks like it carries a credential", a)
			}
		}
	}
	older := buildArgs(binaryInfo{available: true, major: 2, minor: 5}, "/c/p", "/c/m")
	var persistKey, disableDCO bool
	for _, a := range older {
		persistKey = persistKey || a == "--persist-key"
		disableDCO = disableDCO || a == "--disable-dco"
	}
	if !persistKey || disableDCO {
		t.Errorf("2.5 needs --persist-key and does not know --disable-dco: %v", older)
	}
	for _, a := range older {
		if a == "--dns-updown" {
			t.Errorf("--dns-updown is unknown before 2.7: %v", older)
		}
	}
}

func TestWorkspacePaths(t *testing.T) {
	t.Run("hostile owner ids stay inside the run directory", func(t *testing.T) {
		for _, owner := range []string{"../../etc", "a/b", "/abs", "..", "x\x00y", strings.Repeat("a", 500), "名稱"} {
			dir, config, socket, err := workspacePaths("/var/run/plaitway", owner)
			if err != nil {
				t.Fatalf("owner %q: %v", owner, err)
			}
			for _, p := range []string{dir, config, socket} {
				if rel, err := filepath.Rel("/var/run/plaitway", p); err != nil || strings.HasPrefix(rel, "..") {
					t.Errorf("owner %q escaped: %s", owner, p)
				}
			}
			if filepath.Dir(dir) != "/var/run/plaitway" || filepath.Dir(config) != dir || filepath.Dir(socket) != dir {
				t.Errorf("owner %q: unexpected layout %s %s %s", owner, dir, config, socket)
			}
		}
	})
	t.Run("different owners get different directories", func(t *testing.T) {
		a, _, _, _ := workspacePaths("/r", "alpha")
		b, _, _, _ := workspacePaths("/r", "beta")
		a2, _, _, _ := workspacePaths("/r", "alpha")
		if a == b || a != a2 {
			t.Errorf("directories: %s %s %s", a, b, a2)
		}
	})
	t.Run("the socket path must fit sun_path", func(t *testing.T) {
		if _, _, _, err := workspacePaths("/"+strings.Repeat("d", 100), "x"); err == nil {
			t.Error("a 100 byte run directory was accepted")
		}
		if _, _, _, err := workspacePaths("/var/run/plaitway", "x"); err != nil {
			t.Errorf("the production layout was refused: %v", err)
		}
	})
}

func newTestEngine(t *testing.T, runDir string, content string) *engine {
	t.Helper()
	b := Backend(Config{Binary: "/nonexistent/openvpn", RunDir: runDir})
	eng, err := b.New(tunnel.Spec{Owner: "office", Content: []byte(content)}, tunnel.Deps{Network: &fakeNetwork{}})
	if err != nil {
		t.Fatal(err)
	}
	return eng.(*engine)
}

const minimalProfile = "client\nremote 192.0.2.1 1194\n<ca>\nPEM\n</ca>\n"

func TestPrepareWorkspace(t *testing.T) {
	runDir := filepath.Join(shortTempDir(t), "run")
	e := newTestEngine(t, runDir, minimalProfile)
	if err := e.prepareWorkspace(); err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]os.FileMode{runDir: 0o700, e.dir: 0o700, e.configPath: 0o600} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode = %v, want %v", path, info.Mode().Perm(), want)
		}
	}
	got, err := os.ReadFile(e.configPath)
	if err != nil || string(got) != minimalProfile {
		t.Errorf("config = %q, %v", got, err)
	}

	e.removeWorkspace()
	if _, err := os.Stat(e.dir); !os.IsNotExist(err) {
		t.Errorf("workspace still there: %v", err)
	}
}

func TestPrepareWorkspaceReplacesLeftovers(t *testing.T) {
	runDir := shortTempDir(t)
	e := newTestEngine(t, runDir, minimalProfile)

	t.Run("a stale directory with a socket", func(t *testing.T) {
		if err := os.MkdirAll(e.dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(e.socketPath, []byte("stale"), 0o666); err != nil {
			t.Fatal(err)
		}
		if err := e.prepareWorkspace(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(e.socketPath); !os.IsNotExist(err) {
			t.Error("the stale socket survived")
		}
		info, _ := os.Stat(e.dir)
		if info.Mode().Perm() != 0o700 {
			t.Errorf("mode = %v", info.Mode().Perm())
		}
		e.removeWorkspace()
	})

	t.Run("a symlink in the way is removed, not followed", func(t *testing.T) {
		target := filepath.Join(shortTempDir(t), "victim")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		keep := filepath.Join(target, "keep")
		if err := os.WriteFile(keep, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, e.dir); err != nil {
			t.Fatal(err)
		}
		if err := e.prepareWorkspace(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("the symlink target was touched: %v", err)
		}
		if _, err := os.Stat(filepath.Join(target, configFileName)); !os.IsNotExist(err) {
			t.Error("the profile was written through the symlink")
		}
		info, err := os.Lstat(e.dir)
		if err != nil || !info.IsDir() {
			t.Errorf("workspace is not a real directory: %v %v", info, err)
		}
		e.removeWorkspace()
	})
}

func TestNewEngineRejectsBadInput(t *testing.T) {
	b := Backend(Config{Binary: "/x", RunDir: shortTempDir(t)})
	net := &fakeNetwork{}
	tests := []struct {
		name string
		spec tunnel.Spec
		deps tunnel.Deps
	}{
		{"no owner", tunnel.Spec{Content: []byte(minimalProfile)}, tunnel.Deps{Network: net}},
		{"no network", tunnel.Spec{Owner: "o", Content: []byte(minimalProfile)}, tunnel.Deps{}},
		{"profile with a key file", tunnel.Spec{Owner: "o", Content: []byte("client\nremote h 1194\nkey /etc/k\n")}, tunnel.Deps{Network: net}},
		{"empty profile", tunnel.Spec{Owner: "o"}, tunnel.Deps{Network: net}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if eng, err := b.New(tt.spec, tt.deps); err == nil {
				t.Fatalf("New succeeded: %v", eng)
			}
		})
	}
	if _, err := Backend(Config{Binary: "/x"}).New(tunnel.Spec{Owner: "o", Content: []byte(minimalProfile)}, tunnel.Deps{Network: net}); err == nil {
		t.Error("New without a RunDir succeeded")
	}
}
