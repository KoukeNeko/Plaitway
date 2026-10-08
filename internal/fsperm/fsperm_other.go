//go:build !windows

package fsperm

import (
	"log/slog"
	"os"
)

const (
	privateDirMode  os.FileMode = 0o700
	privateFileMode os.FileMode = 0o600
	// groupAndOtherBits are the permission bits that let anyone but the owner in.
	groupAndOtherBits os.FileMode = 0o077

	restrictedMessageSuffix = " was accessible to others, restricted to 0700"
)

// MkdirAll creates dir and its missing parents with mode perm. An existing
// directory is left as it is.
func MkdirAll(dir string, perm os.FileMode) error {
	return os.MkdirAll(dir, perm)
}

// OpenAppend opens path for appending and creates it with mode 0600 when it is
// missing. An existing file keeps its mode.
func OpenAppend(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, privateFileMode)
}

// IsPrivate reports whether nobody but the owner can reach path (a symbolic
// link is followed).
func IsPrivate(path string) (bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return fi.Mode().Perm()&groupAndOtherBits == 0, nil
}

// Restrict makes path private when it is not and reports whether it had to.
func Restrict(path string) (changed bool, err error) {
	_, changed, err = tighten(path)
	return changed, err
}

// RestrictAndWarn is Restrict for a directory of the daemon, and logs a warning
// that names label and the mode it found when it had to change something.
func RestrictAndWarn(log *slog.Logger, label, dir string) error {
	was, changed, err := tighten(dir)
	if err != nil {
		return err
	}
	if changed {
		log.Warn(label+restrictedMessageSuffix, "dir", dir, "was", was)
	}
	return nil
}

// tighten is Restrict that also returns the permission bits it found.
func tighten(path string) (was os.FileMode, changed bool, err error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, false, err
	}
	was = fi.Mode().Perm()
	if was&groupAndOtherBits == 0 {
		return was, false, nil
	}
	mode := privateFileMode
	if fi.IsDir() {
		mode = privateDirMode
	}
	if err := os.Chmod(path, mode); err != nil {
		return was, false, err
	}
	return was, true, nil
}
