//go:build unix

// Package transport hides the per-OS local IPC mechanism (Unix domain socket
// here, named pipe on Windows) behind four functions.
package transport

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
)

// DefaultPath is where an unprivileged development daemon listens. macOS limits
// sun_path to 104 bytes, so it lives in the short per-user $TMPDIR rather than
// under the project. The Swift client derives the same path from $TMPDIR
// (DaemonLocation.swift); its integration test starts the daemon without
// -socket to keep the two in agreement.
func DefaultPath() string { return filepath.Join(os.TempDir(), "plaitway.sock") }

// Listen binds the socket so that it has exactly mode from the moment the inode
// exists. bind(2) creates the inode with 0777 &^ umask, so the umask is
// narrowed around net.Listen instead of calling chmod afterwards (a chmod
// would leave a window in which the socket is more open than intended).
// umask is process-wide: call Listen during start-up, before other goroutines
// create files. A missing parent directory is created with mode 0755.
func Listen(path string, mode os.FileMode) (net.Listener, error) {
	if mode&^os.ModePerm != 0 {
		return nil, fmt.Errorf("socket mode %v has bits other than permissions", mode)
	}
	if max := len(unix.RawSockaddrUnix{}.Path); len(path) >= max {
		return nil, fmt.Errorf("socket path is %d bytes, sun_path allows at most %d: %s", len(path), max-1, path)
	}
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := removeStale(path); err != nil {
		return nil, err
	}

	old := unix.Umask(int(os.ModePerm &^ mode))
	l, err := net.Listen("unix", path)
	unix.Umask(old)
	if err != nil {
		return nil, err
	}

	// Fail closed if the filesystem did not honour the mode.
	fi, err := os.Lstat(path)
	if err == nil && fi.Mode().Perm() != mode {
		err = fmt.Errorf("socket %s has mode %v, want %v", path, fi.Mode().Perm(), mode)
	}
	if err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

// ensureDir creates dir as 0755 when it is missing, whatever the umask. An
// existing directory is left alone.
func ensureDir(dir string) error {
	if _, err := os.Stat(dir); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.Chmod(dir, 0o755)
}

// removeStale deletes a socket file left behind by a daemon that was killed
// without cleaning up, but refuses to touch a socket that still answers.
func removeStale(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s exists and is not a socket", path)
	}
	c, err := net.DialTimeout("unix", path, time.Second)
	if err == nil {
		c.Close()
		return fmt.Errorf("another daemon is already listening on %s", path)
	}
	return os.Remove(path)
}

// Target is the grpc-go target string for path.
func Target(path string) string { return "unix://" + path }

// DialOptions are extra grpc.DialOptions this OS needs (none: grpc-go has a
// built-in "unix" resolver and dialer).
func DialOptions() []grpc.DialOption { return nil }
