package main

import (
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/KoukeNeko/Plaitway/internal/fsperm"
	"github.com/KoukeNeko/Plaitway/internal/osnet"
	winnet "github.com/KoukeNeko/Plaitway/internal/osnet/windows"
	"github.com/KoukeNeko/Plaitway/internal/ovpn"
	"github.com/KoukeNeko/Plaitway/internal/reconciler"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
	"github.com/KoukeNeko/Plaitway/internal/wg"
)

const (
	// journalFileName is the Reconciler's write-ahead journal in the state
	// directory, next to the stored profiles.
	journalFileName = "journal"
	// stateDirMode has no meaning on Windows, where the directory gets a
	// protected access list; it is what the profile store passes as well.
	stateDirMode = 0o700
)

// engineNames are the engines as the log names them.
var engineNames = map[tunnel.Kind]string{
	tunnel.KindOpenVPN:   "openvpn",
	tunnel.KindWireGuard: "wireguard",
}

// realConfig is what the real engines need from the command line.
type realConfig struct {
	log *slog.Logger
	// openvpn is the path of openvpn.exe.
	openvpn string
	// runDir holds the engines' private folders: generated configs and the
	// management password.
	runDir string
	// stateDir holds the Reconciler's journal next to the stored profiles.
	stateDir string
}

// wireReal builds the real backends, the Reconciler on the Windows adapters, and
// the network monitor the Reconciler and the engines share. It is the only
// place that knows how the real parts fit together; the daemon core sees
// tunnel.Backend, tunnel.Reconciler and osnet.NetMonitor only.
//
// reconciler.New replays the journal of a previous run, so a daemon that was
// killed leaves nothing behind once it starts again.
//
// An engine that cannot run here (no trusted wintun.dll, an openvpn that fails
// the checks of internal/ovpn) does not stop the daemon: its Probe says why,
// and the profiles show the reason. Nor does an unelevated daemon fail to
// start: it reads the network and writes it only when a tunnel announces
// routes.
func wireReal(cfg realConfig) ([]tunnel.Backend, tunnel.Reconciler, osnet.NetMonitor, error) {
	if err := prepareStateDir(cfg.log, cfg.stateDir); err != nil {
		return nil, nil, nil, err
	}
	monitor := winnet.NewNetMonitor(winnet.NetMonitorOptions{Logger: cfg.log})
	journalPath := filepath.Join(cfg.stateDir, journalFileName)
	rec, err := reconciler.New(reconciler.Config{
		Routes:      winnet.NewRouteTable(),
		DNS:         winnet.NewDNS(winnet.DNSOptions{Logger: cfg.log}),
		Net:         monitor,
		JournalPath: journalPath,
		Log:         cfg.log,
		// The zero value is the macOS table: one route per prefix.
		Keying: reconciler.KeyByPrefixInterfaceNextHop,
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("start the Reconciler (journal %s): %w", journalPath, err)
	}
	backends := []tunnel.Backend{
		ovpn.Backend(ovpn.Config{Binary: cfg.openvpn, RunDir: cfg.runDir, BinarySHA256: openvpnSHA256, Log: cfg.log}),
		wg.Backend(wg.Config{Log: cfg.log}),
	}
	logUnavailableEngines(cfg.log, backends)
	return backends, rec, monitor, nil
}

// prepareStateDir makes the state directory private before the Reconciler puts
// its journal in it. reconciler.New creates a missing directory with the access
// list it inherits, and the journal names every route the daemon installs. The
// profile store restricts the directory too, but only after the journal exists.
func prepareStateDir(log *slog.Logger, dir string) error {
	if err := fsperm.MkdirAll(dir, stateDirMode); err != nil {
		return fmt.Errorf("create state directory %s: %w", dir, err)
	}
	if err := fsperm.RestrictAndWarn(log, "state directory", dir); err != nil {
		return fmt.Errorf("restrict state directory %s: %w", dir, err)
	}
	return nil
}

// logUnavailableEngines puts into the log why an engine cannot run, so that the
// reason is there for whoever reads the log of a service that nobody watches.
// The probes are cached by the engines: this costs the first look that the first
// profile would have made.
func logUnavailableEngines(log *slog.Logger, backends []tunnel.Backend) {
	for _, backend := range backends {
		if info := backend.Probe(); !info.Available {
			log.Warn("engine unavailable", "engine", engineNames[backend.Kind], "reason", info.Detail)
		}
	}
}
