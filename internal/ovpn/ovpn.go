// Package ovpn is the OpenVPN engine: it validates untrusted .ovpn profiles and
// supervises the official openvpn binary as a child process, controlling it
// through the management interface on a Unix socket.
//
// Division of work with the daemon: openvpn only creates the tunnel interface
// and its address. It never touches routes (--route-noexec) or DNS; it reports
// what the profile and the server ask for, and the engine hands that to the
// Reconciler as an Intent.
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
	"io"
	"log/slog"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// Config is the daemon's wiring of the OpenVPN backend.
type Config struct {
	// Binary is the path of the openvpn executable.
	Binary string
	// RunDir holds the generated configs and management sockets. It is created
	// with mode 0700 when missing. Each engine works in its own 0700
	// subdirectory, because openvpn creates its management socket with mode
	// 0777 and the directory is what keeps other users out.
	RunDir string
	// Log receives the backend's own diagnostics. openvpn's output goes to
	// Deps.Log of each engine instead.
	Log *slog.Logger
}

// Backend returns the OpenVPN implementation of tunnel.Backend.
func Backend(cfg Config) tunnel.Backend {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	b := newBackend(cfg)
	return tunnel.Backend{
		Kind:  tunnel.KindOpenVPN,
		Parse: Parse,
		New:   b.newEngine,
		Probe: b.probeInfo,
	}
}
