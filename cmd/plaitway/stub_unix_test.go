//go:build unix

package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// Unix sockets are files, and so can be left behind, hidden in a directory
// that is missing or closed, or be no socket at all.
func TestDaemonSocketThatIsGoneStaleOrOutOfReach(t *testing.T) {
	t.Parallel()
	dir := shortDir(t)

	missing := filepath.Join(dir, "missing", "d.sock")
	if r := runAt(t, missing, "", nil, "status"); r.code != exitFailure || r.stderr != "plaitway: the daemon is not running: "+missing+" does not exist\n" {
		t.Errorf("no directory: %+v", r)
	}

	// A daemon that was killed leaves its socket file behind.
	stale := filepath.Join(dir, "stale.sock")
	lis, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	lis.(*net.UnixListener).SetUnlinkOnClose(false)
	lis.Close()
	if r := runAt(t, stale, "", nil, "status"); r.code != exitFailure || r.stderr != "plaitway: the daemon is not running: nothing listens on "+stale+"\n" {
		t.Errorf("stale socket: %+v", r)
	}

	if os.Getuid() != 0 {
		locked := filepath.Join(dir, "locked")
		if err := os.Mkdir(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(locked, 0o700)
		socket := filepath.Join(locked, "d.sock")
		if r := runAt(t, socket, "", nil, "status"); r.code != exitFailure || r.stderr != "plaitway: permission denied to open "+socket+"\n" {
			t.Errorf("socket in a closed directory: %+v", r)
		}
	}
}
