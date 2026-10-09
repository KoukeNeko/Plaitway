//go:build !windows

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// groupOrOtherWrite are the permission bits that let anyone but the owner
// change a file or a directory.
const groupOrOtherWrite fs.FileMode = 0o022

// trustInPlace returns the path of the openvpn binary at source, with its
// symbolic links resolved, when nobody but root can change what would run: the
// file is a regular file that belongs to root and that group and others cannot
// write to, and so is every directory from its own up to top. The first path
// that fails is named in the error.
//
// This is the check for a binary that comes with the distribution, where there
// is no build-time hash to compare it with. The path that was checked is the
// path to run: a link in a directory somebody else can write to could be
// pointed elsewhere between the check and the start.
//
// top is "/" in production. owner returns the uid that owns a path.
func trustInPlace(source, top string, owner func(path string) (uint32, error)) (string, error) {
	if source == "" {
		return "", errors.New("openvpn binary is not configured")
	}
	// A relative path would be checked against the directory the daemon was
	// started in, which is not where anyone looks for openvpn.
	absolute, err := filepath.Abs(source)
	if err != nil {
		return "", fmt.Errorf("resolve openvpn at %s: %w", source, err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("openvpn not found at %s", source)
	}
	if err != nil {
		return "", fmt.Errorf("resolve openvpn at %s: %w", source, err)
	}
	for path := resolved; ; path = filepath.Dir(path) {
		if err := checkRootOwnedPath(path, path == resolved, owner); err != nil {
			return "", fmt.Errorf("openvpn at %s cannot be trusted: %w", source, err)
		}
		if parent := filepath.Dir(path); path == top || parent == path {
			return resolved, nil
		}
	}
}

// checkRootOwnedPath fails for a path that is not owned by root or that group
// or others can write to; the binary itself must also be a regular file.
func checkRootOwnedPath(path string, binary bool, owner func(string) (uint32, error)) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if binary && !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	uid, err := owner(path)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if uid != 0 {
		return fmt.Errorf("%s belongs to uid %d, not to root", path, uid)
	}
	if fi.Mode().Perm()&groupOrOtherWrite != 0 {
		return fmt.Errorf("%s can be written to by its group or by others (mode %04o)", path, fi.Mode().Perm())
	}
	return nil
}
