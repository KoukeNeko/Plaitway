// Package ovpn is the OpenVPN engine: it validates untrusted .ovpn profiles and
// supervises the official openvpn binary as a child process, controlling it
// through the management interface: a Unix socket where there is one, and on
// Windows, where openvpn cannot listen on one, a password protected port on
// loopback (channel.go).
//
// Division of work with the daemon: openvpn only creates the tunnel interface
// and reports its address. It never touches routes (--route-noexec) or DNS; it
// reports what the profile and the server ask for, and the engine hands that to
// the Reconciler as an Intent. On Windows openvpn cannot even make the interface:
// the engine makes a TAP-Windows6 adapter for it with tapctl, sets its addresses
// through the IP Helper API, and runs openvpn in a job object that ends it with
// the daemon (device_windows.go, process_windows.go). The binary is run only
// after the checks of trust_windows.go.
//
// On Linux openvpn makes its tun device and sets its addresses itself, as on
// macOS, and the kernel ends the child with the daemon (process_linux.go). A
// profile for a tap device is refused, because the Reconciler binds the routes
// of a tunnel to its device without a next hop (platform_linux.go).
//
// Profiles are untrusted input to a root process. Parse is the only way in:
// it rejects profiles that name files, removes directives that run programs
// or reconfigure the daemon, and writes back a canonical profile that holds
// nothing but directives from the allow-list in directives.go. The engine
// parses what it is given again before using it. Credentials never touch a
// file or a command line; they live in memory and go to openvpn through the
// management interface.
package ovpn

import (
	"log/slog"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// Config is the daemon's wiring of the OpenVPN backend.
type Config struct {
	// Binary is the path of the openvpn executable. On Windows it must be an
	// absolute path (see VerifyBinary).
	Binary string
	// RunDir holds the generated configs and management sockets (on Windows the
	// management password). It is created private when missing: mode 0700, or on
	// Windows an access list of SYSTEM and Administrators. Each engine works in its
	// own private subdirectory, because openvpn creates its management socket with
	// mode 0777 and the directory is what keeps other users out.
	RunDir string
	// BinarySHA256 (hex), when not empty, is the hash the binary must have. It is
	// for Windows, where the engine verifies the binary where it is: see
	// VerifyBinary. Elsewhere the daemon decides which binary it trusts before it
	// gives the path (on macOS it checks a copy).
	BinarySHA256 string
	// Log receives the backend's own diagnostics. openvpn's output goes to
	// Deps.Log of each engine instead.
	Log *slog.Logger
}

// Backend returns the OpenVPN implementation of tunnel.Backend.
func Backend(cfg Config) tunnel.Backend {
	return newBackend(cfg).tunnelBackend()
}

func (b *backend) tunnelBackend() tunnel.Backend {
	return tunnel.Backend{
		Kind:  tunnel.KindOpenVPN,
		Parse: Parse,
		New:   b.newEngine,
		Probe: b.probeInfo,
	}
}
