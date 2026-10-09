//go:build unix

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// nobody is a group id that no test process belongs to.
const nobody = 4_000_000_000

// everyone is the policy under which the test process may do everything: its
// own primary group counts as the administrator group.
func everyone() caller {
	return caller{pol: &policy{administrator: inGroup(uint32(os.Getgid())), consoleUIDs: func() ([]uint32, error) { return []uint32{uint32(os.Getuid())}, nil }}}
}

// consoleUserOnly lets the test process read and connect but not modify.
func consoleUserOnly() caller {
	return caller{pol: &policy{administrator: inGroup(nobody), consoleUIDs: func() ([]uint32, error) { return []uint32{uint32(os.Getuid())}, nil }}}
}

// stranger lets the test process do nothing.
func stranger() caller {
	return caller{pol: &policy{administrator: inGroup(nobody), consoleUIDs: func() ([]uint32, error) { return []uint32{uint32(os.Getuid()) + 1}, nil }}}
}

func skipIfEveryoneIsAuthorized(t *testing.T) {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("root is always authorized, there is nobody to deny")
	}
}

// newSocketPath is a socket in a directory of its own.
func newSocketPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(shortDir(t), "d.sock")
}

// nestedSocketPath is a socket whose directory does not exist yet: run has to
// create it.
func nestedSocketPath(dir string) string {
	return filepath.Join(dir, "run", "plaitway", "plaitwayd.sock")
}

// assertRunFootprint checks the modes of what a running daemon created.
func assertRunFootprint(t *testing.T, cfg config) {
	t.Helper()
	if fi, err := os.Stat(filepath.Dir(cfg.socket)); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("socket directory: %v, %v; want mode 0755", fi, err)
	}
	if fi, err := os.Lstat(cfg.socket); err != nil || fi.Mode().Perm() != cfg.socketMode {
		t.Errorf("socket: %v, %v; want mode %04o", fi, err, cfg.socketMode)
	}
	if fi, err := os.Stat(cfg.stateDir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("state directory: %v, %v; want mode 0700", fi, err)
	}
}

// socketGone checks that nothing is left at the address of a daemon that stopped.
func socketGone(t *testing.T, socket string) {
	t.Helper()
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatalf("the socket was left behind: %v", err)
	}
}
