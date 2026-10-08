//go:build !windows

package ovpn

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// makeRunDir creates the run directory. It is mode 0700 because the engines'
// directories are in it.
func makeRunDir(dir string) error { return os.MkdirAll(dir, 0o700) }

// makePrivateDir creates a directory only the owner can enter. openvpn creates
// its management socket with mode 0777, so the directory is what keeps other
// users away from it.
func makePrivateDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("set mode of %s: %w", dir, err)
	}
	return nil
}

// leadingOptions come before the profile on the command line.
func leadingOptions() []string { return nil }

// trailingOptions follow the daemon's own options.
func trailingOptions() []string { return nil }

// newMgmtChannel is the management interface of one engine: a Unix socket in
// its directory.
func newMgmtChannel(dir string) (mgmtChannel, error) { return newUnixChannel(dir) }

// newDeviceProvider leaves the tunnel interface to openvpn.
func newDeviceProvider(Config, tunnel.OwnerID, *profile, *slog.Logger) deviceProvider {
	return openvpnOwnedDevice{}
}

// validTunnelDevice accepts the name openvpn reports for its interface.
func validTunnelDevice(name string) bool { return validDeviceName(name) }

// readsDHCPOptionsFromLog says whether the dhcp-options a server pushed are
// taken from the log as well as from the environment of the up event. openvpn
// puts them in the environment itself on Unix.
const readsDHCPOptionsFromLog = false

// inspectBinary asks the binary what it is. The daemon decides which binary it
// trusts before it gives the path to the engine.
func inspectBinary(cfg Config, timeout time.Duration) binaryInfo {
	return probeBinary(cfg.Binary, timeout)
}

// trustBinary has nothing to check before a start: see inspectBinary.
func trustBinary(Config) (release func(), err error) { return func() {}, nil }
