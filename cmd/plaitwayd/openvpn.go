package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// openvpnCopyName is the verified copy of openvpn in the run directory.
const openvpnCopyName = "openvpn"

// trustedOpenVPN returns the path of the openvpn binary the engines may run
// as root. A build that knows the hash of its openvpn (openvpnSHA256) runs a
// verified copy in the run directory; a development build, which has none,
// runs the configured path as it is.
func trustedOpenVPN(source, runDir string) (string, error) {
	if openvpnSHA256 == "" {
		return source, nil
	}
	return installOpenVPN(source, runDir, openvpnSHA256)
}

// installOpenVPN copies the openvpn binary at source into runDir and returns
// the path of the copy.
//
// The app bundle that holds openvpn is writable by whoever installed it, but
// the daemon runs as root. It must not check the file there and run it
// afterwards, because the file can change in between. So the bytes are read
// once, hashed while they are written into a file that only root can change,
// and the file is kept only when the hash is wantSHA256 (hex).
func installOpenVPN(source, runDir, wantSHA256 string) (path string, err error) {
	if source == "" {
		return "", errors.New("openvpn binary is not configured")
	}
	if err := checkRunDir(runDir); err != nil {
		return "", err
	}
	src, err := os.Open(source)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("openvpn not found at %s", source)
	}
	if err != nil {
		return "", fmt.Errorf("open openvpn: %w", err)
	}
	defer src.Close()
	if fi, err := src.Stat(); err != nil || !fi.Mode().IsRegular() {
		return "", fmt.Errorf("openvpn at %s is not a regular file", source)
	}

	tmp, err := os.CreateTemp(runDir, ".openvpn-*")
	if err != nil {
		return "", fmt.Errorf("create the openvpn copy: %w", err)
	}
	defer func() {
		if err != nil {
			tmp.Close()
			if rmErr := os.Remove(tmp.Name()); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
				err = errors.Join(err, rmErr)
			}
		}
	}()
	hash := sha256.New()
	if _, err = io.Copy(io.MultiWriter(tmp, hash), src); err != nil {
		return "", fmt.Errorf("copy openvpn: %w", err)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != strings.ToLower(wantSHA256) {
		return "", fmt.Errorf("openvpn at %s is not the binary this daemon was built for (sha256 %s, expected %s)", source, got, wantSHA256)
	}
	// The copy stays unexecutable until it is known to be the right one.
	if err = tmp.Chmod(0o755); err != nil {
		return "", fmt.Errorf("make the openvpn copy executable: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return "", fmt.Errorf("write the openvpn copy: %w", err)
	}
	path = filepath.Join(runDir, openvpnCopyName)
	if err = os.Rename(tmp.Name(), path); err != nil {
		return "", fmt.Errorf("install the openvpn copy: %w", err)
	}
	return path, nil
}

// checkRunDir creates the run directory when needed and refuses one that
// somebody else could put a different binary into.
func checkRunDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create run directory: %w", err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("run directory: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("run directory %s is not a directory (a symbolic link is not followed)", dir)
	}
	owner, err := fileOwner(dir)
	if err != nil {
		return fmt.Errorf("run directory %s: %w", dir, err)
	}
	if owner != uint32(os.Geteuid()) || fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("run directory %s must belong to the daemon's user and not be writable by others (owner %d, mode %04o)", dir, owner, fi.Mode().Perm())
	}
	return nil
}

// openvpnUnavailable makes the OpenVPN backend report why it cannot be used,
// which the profile's last_error and DaemonInfo show.
func openvpnUnavailable(backends []tunnel.Backend, reason error) []tunnel.Backend {
	for i := range backends {
		if backends[i].Kind == tunnel.KindOpenVPN {
			backends[i].Probe = func() tunnel.EngineInfo { return tunnel.EngineInfo{Detail: reason.Error()} }
		}
	}
	return backends
}
