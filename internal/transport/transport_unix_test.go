//go:build unix

package transport

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// shortSocketPath avoids t.TempDir(), whose path overflows sun_path on macOS.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "t.sock")
}

func TestListenCreatesOwnerOnlySocket(t *testing.T) {
	path := shortSocketPath(t)
	l, err := Listen(path, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v, want a socket with 0600", fi.Mode())
	}
}

// Production passes 0666 so that every local user can connect and the daemon
// authorizes each call itself.
func TestListenHonoursRequestedMode(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o660, 0o666} {
		path := shortSocketPath(t)
		l, err := Listen(path, mode)
		if err != nil {
			t.Fatal(err)
		}
		fi, err := os.Lstat(path)
		l.Close()
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != mode {
			t.Errorf("socket mode = %v, want %v", fi.Mode().Perm(), mode)
		}
	}
}

func TestListenCreatesMissingDirectoryAs0755(t *testing.T) {
	// A restrictive umask must not narrow the directory: other users have to
	// reach the socket inside it.
	old := unix.Umask(0o077)
	defer unix.Umask(old)

	dir := filepath.Join(filepath.Dir(shortSocketPath(t)), "run", "plaitway")
	l, err := Listen(filepath.Join(dir, "d.sock"), 0o666)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("directory mode = %v, want 0755", fi.Mode().Perm())
	}
}

func TestListenLeavesExistingDirectoryAlone(t *testing.T) {
	path := shortSocketPath(t)
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := Listen(path, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("existing directory mode = %v, want it untouched at 0700", fi.Mode().Perm())
	}
}

func TestListenRejectsNonPermissionBits(t *testing.T) {
	if l, err := Listen(shortSocketPath(t), os.ModeSetuid|0o600); err == nil {
		l.Close()
		t.Fatal("mode with setuid accepted")
	}
}

func TestListenReplacesStaleSocket(t *testing.T) {
	path := shortSocketPath(t)
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false) // what a SIGKILLed daemon leaves behind
	stale.Close()

	l, err := Listen(path, 0o600)
	if err != nil {
		t.Fatalf("Listen over a stale socket: %v", err)
	}
	l.Close()
}

func TestListenRefusesLiveSocket(t *testing.T) {
	path := shortSocketPath(t)
	first, err := Listen(path, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	second, err := Listen(path, 0o600)
	if err == nil {
		second.Close()
		t.Fatal("second Listen on a live socket succeeded")
	}
}

func TestListenRefusesNonSocketFile(t *testing.T) {
	path := shortSocketPath(t)
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if l, err := Listen(path, 0o600); err == nil {
		l.Close()
		t.Fatal("Listen replaced a regular file")
	}
	if got, _ := os.ReadFile(path); string(got) != "keep" {
		t.Fatalf("file was modified: %q", got)
	}
}

func TestListenRejectsPathLongerThanSunPath(t *testing.T) {
	path := filepath.Join(os.TempDir(), strings.Repeat("x", 120)+".sock")
	if l, err := Listen(path, 0o600); err == nil {
		l.Close()
		t.Fatal("overlong path accepted")
	}
}
