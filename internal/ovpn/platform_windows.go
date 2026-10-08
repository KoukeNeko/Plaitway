package ovpn

import (
	"fmt"
	"log/slog"

	"github.com/KoukeNeko/Plaitway/internal/fsperm"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// makeRunDir creates the run directory with an access list of SYSTEM and
// Administrators (and the daemon's own user when it is not elevated).
func makeRunDir(dir string) error { return fsperm.MkdirAll(dir, 0) }

// makePrivateDir creates a directory that only the daemon's accounts can
// enter, and says so only when Windows agrees. What is created in it passes
// the access list on: the profile and the management password are in it.
func makePrivateDir(dir string) error {
	if err := fsperm.MkdirAll(dir, 0); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	private, err := fsperm.IsPrivate(dir)
	if err != nil {
		return fmt.Errorf("check the access list of %s: %w", dir, err)
	}
	if !private {
		return fmt.Errorf("%s is accessible to others", dir)
	}
	return nil
}

// leadingOptions come before the profile on the command line, because the first
// filter that matches a pushed option decides, and a profile must not accept
// what these refuse:
//
//   - block-outside-dns installs Windows Filtering Platform rules that block
//     DNS on every other adapter; the firewall is not openvpn's to change.
//   - ip-win32 picks how openvpn assigns the tunnel address, and a server could
//     push one that has openvpn do it.
//   - dns and dhcp-option are what openvpn applies to the adapter through the
//     OpenVPN Interactive Service (NRPT rules), which only works when it was
//     started by the service; it logs "could not talk to service" otherwise. The
//     Reconciler owns DNS, so openvpn is not to try. The engine does not need the
//     options from openvpn: it reads them from the PUSH_REPLY in the log, which
//     openvpn writes before any filter, and applies the profile's own filters.
func leadingOptions() []string {
	return []string{
		"--pull-filter", "ignore", "block-outside-dns",
		"--pull-filter", "ignore", "ip-win32",
		"--pull-filter", "ignore", "dns ",
		"--pull-filter", "ignore", "dhcp-option",
	}
}

// trailingOptions follow the daemon's own options. openvpn on Windows would
// configure the adapter itself, with netsh or the IP Helper API, or through the
// OpenVPN Interactive Service when it was started by it; neither is wanted:
//
//   - ifconfig-noexec: the engine gives the adapter its addresses (winiface);
//     openvpn only reports them.
//   - ip-win32 manual: no DHCP emulation by the driver and no netsh, whatever a
//     profile says.
//
// Routes are left out by route-noexec and DNS by dns-updown disable, as on
// every system. The service is never involved because the engine does not pass
// --service or --msg-channel. register-dns and block-outside-dns are not
// options a profile can set (see directives.go).
func trailingOptions() []string {
	return []string{"--ifconfig-noexec", "--ip-win32", "manual"}
}

// newMgmtChannel is the management interface of one engine: a password
// protected port on loopback, since openvpn for Windows cannot listen on a Unix
// socket or a named pipe ("MANAGEMENT: this platform does not support unix
// domain sockets"). Only the process that owns the port is trusted with the
// password.
func newMgmtChannel(dir string) (mgmtChannel, error) {
	return newTCPChannel(dir, peerIsProcess), nil
}

// newDeviceProvider gives openvpn an adapter of its own. A profile that opens
// no device (tests) needs none.
func newDeviceProvider(cfg Config, owner tunnel.OwnerID, prof *profile, log *slog.Logger) deviceProvider {
	if prof.device == nullDevice {
		return openvpnOwnedDevice{}
	}
	return newAdapterDevice(adapterDeviceConfig{
		tool:   tapctl{path: tapctlPath(cfg.Binary), log: log},
		prefix: ownAdapterPrefix,
		owner:  owner,
		log:    log,
	})
}

// validTunnelDevice accepts the name of an adapter of the engine's, which has
// hyphens, as well as the names openvpn reports elsewhere.
func validTunnelDevice(name string) bool {
	return validDeviceName(name) || adapterNamePattern.MatchString(name)
}

// readsDHCPOptionsFromLog says whether the dhcp-options a server pushed are
// taken from the log as well as from the environment of the up event: openvpn
// for Windows keeps them in the settings of the adapter, and whether they reach
// the environment too is not something every build does.
const readsDHCPOptionsFromLog = true
