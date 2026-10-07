package ovpn

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

const (
	configFileName = "profile.ovpn"
	socketFileName = "m.sock"
	// maxSocketPath is the longest Unix socket path macOS accepts (sun_path is
	// 104 bytes including the terminating NUL).
	maxSocketPath = 103
)

// childEnv is the whole environment of the openvpn child. It runs
// /sbin/ifconfig by absolute path; nothing from the daemon's environment is
// needed, and none is passed on.
func childEnv() []string { return []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"} }

// buildArgs is the openvpn command line. The stored profile comes first and
// the daemon's options follow, so that they win wherever the profile sets the
// same option.
//
//   - management with hold and query-passwords: the engine releases the hold
//     once it is connected and answers credential prompts itself, so no
//     credential is ever written to a file or a command line.
//   - management-up-down reports the pushed routes and DNS options.
//   - route-noexec: the Reconciler owns every route; openvpn only configures
//     the tunnel interface.
//   - persist-tun: after SIGUSR1 (network change) the interface and the routes
//     the Reconciler bound to it stay.
//   - auth-retry interact: a rejected password is asked for again through the
//     management interface instead of ending the process.
//   - script-security 1 is the default, written down: no profile script can
//     run, and openvpn may still call its built-in ifconfig.
//   - dns-updown disable: since 2.7 openvpn runs a helper script, whose path
//     is fixed when openvpn is built and which runs even at script-security 1,
//     whenever DNS options are set. The Reconciler owns DNS.
func buildArgs(bin binaryInfo, configPath, socketPath string) []string {
	args := []string{
		"--config", configPath,
		"--management", socketPath, "unix",
		"--management-hold",
		"--management-query-passwords",
		"--management-up-down",
		"--route-noexec",
		"--persist-tun",
		"--auth-retry", "interact",
		"--script-security", "1",
		"--verb", "4",
	}
	if !bin.persistKeyIsDeprecated() {
		args = append(args, "--persist-key")
	}
	if bin.hasDNSUpdown() {
		args = append(args, "--dns-updown", "disable")
	}
	if bin.supportsDisableDCO() {
		args = append(args, "--disable-dco")
	}
	return args
}

// workspacePaths names the engine's private directory and the files in it. The
// directory name is derived from the owner id with a hash, so that an id with
// path characters or a great length can never reach the file system.
func workspacePaths(runDir string, owner string) (dir, config, socket string, err error) {
	sum := sha256.Sum256([]byte(owner))
	dir = filepath.Join(runDir, fmt.Sprintf("ovpn-%x", sum[:6]))
	config = filepath.Join(dir, configFileName)
	socket = filepath.Join(dir, socketFileName)
	if len(socket) > maxSocketPath {
		return "", "", "", fmt.Errorf("management socket path %q is too long (%d bytes, at most %d)", socket, len(socket), maxSocketPath)
	}
	return dir, config, socket, nil
}

// prepareWorkspace creates the engine's directory with mode 0700 and writes
// the profile into it with mode 0600. openvpn creates its management socket
// with mode 0777, so the directory is what keeps other users away from it.
func (e *engine) prepareWorkspace() error {
	if err := os.MkdirAll(e.cfg.RunDir, 0o700); err != nil {
		return fmt.Errorf("create run directory: %w", err)
	}
	// A crashed predecessor may have left its directory. RemoveAll does not
	// follow a symlink.
	if err := os.RemoveAll(e.dir); err != nil {
		return fmt.Errorf("remove stale %s: %w", e.dir, err)
	}
	if err := os.Mkdir(e.dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", e.dir, err)
	}
	if err := os.Chmod(e.dir, 0o700); err != nil {
		return fmt.Errorf("set mode of %s: %w", e.dir, err)
	}
	f, err := os.OpenFile(e.configPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create profile file: %w", err)
	}
	if _, err := f.Write(e.prof.Content); err != nil {
		f.Close()
		return fmt.Errorf("write profile file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write profile file: %w", err)
	}
	return nil
}
