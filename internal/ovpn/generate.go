package ovpn

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

const configFileName = "profile.ovpn"

// buildArgs is the openvpn command line. The stored profile comes first and
// the daemon's options follow, so that they win wherever the profile sets the
// same option, except where a filter must be first: leadingOptions come before
// the profile. management opens the management interface (see mgmtChannel) and
// device picks the tunnel interface (see deviceProvider); trailingOptions are
// the rest of what the OS needs.
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
//     run, and openvpn may still call its built-in ifconfig or ip.
//   - dns-updown disable: since 2.7 openvpn runs a helper script, whose path
//     is fixed when openvpn is built and which runs even at script-security 1,
//     whenever DNS options are set. The Reconciler owns DNS.
func buildArgs(bin binaryInfo, configPath string, management, device []string) []string {
	args := slices.Concat(leadingOptions(), []string{"--config", configPath}, management, []string{
		"--management-hold",
		"--management-query-passwords",
		"--management-up-down",
		"--route-noexec",
		"--persist-tun",
		"--auth-retry", "interact",
		"--script-security", "1",
		"--verb", "4",
	})
	if !bin.persistKeyIsDeprecated() {
		args = append(args, "--persist-key")
	}
	if bin.hasDNSUpdown() {
		args = append(args, "--dns-updown", "disable")
	}
	if bin.supportsDisableDCO() {
		args = append(args, "--disable-dco")
	}
	return slices.Concat(args, trailingOptions(), device)
}

// workspaceFiles names the engine's private directory and the profile in it.
// The directory name is derived from the owner id with a hash, so that an id
// with path characters or a great length can never reach the file system.
func workspaceFiles(runDir string, owner string) (dir, config string) {
	sum := sha256.Sum256([]byte(owner))
	dir = filepath.Join(runDir, fmt.Sprintf("ovpn-%x", sum[:6]))
	return dir, filepath.Join(dir, configFileName)
}

// workspacePaths is workspaceFiles with the management socket of the Unix
// channel.
func workspacePaths(runDir string, owner string) (dir, config, socket string, err error) {
	dir, config = workspaceFiles(runDir, owner)
	socket, err = unixSocketPath(dir)
	if err != nil {
		return "", "", "", err
	}
	return dir, config, socket, nil
}

// prepareWorkspace creates the engine's private directory and writes the
// profile into it, then whatever the management channel needs there.
func (e *engine) prepareWorkspace() error {
	if err := makeRunDir(e.cfg.RunDir); err != nil {
		return fmt.Errorf("create run directory: %w", err)
	}
	// A crashed predecessor may have left its directory. RemoveAll does not
	// follow a symlink.
	if err := os.RemoveAll(e.dir); err != nil {
		return fmt.Errorf("remove stale %s: %w", e.dir, err)
	}
	if err := makePrivateDir(e.dir); err != nil {
		return err
	}
	if err := e.writeProfile(); err != nil {
		return err
	}
	if err := e.channel.prepare(); err != nil {
		return err
	}
	e.mu.Lock()
	e.secrets = append(e.secrets, e.channel.secrets()...)
	e.mu.Unlock()
	return nil
}

// writeProfile writes the profile; it must not exist yet.
func (e *engine) writeProfile() error {
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
