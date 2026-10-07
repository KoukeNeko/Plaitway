package main

import (
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/osnet/macos"
	"github.com/KoukeNeko/Plaitway/internal/ovpn"
	"github.com/KoukeNeko/Plaitway/internal/reconciler"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
	"github.com/KoukeNeko/Plaitway/internal/wg"
)

// realConfig is what the real engines need from the command line.
type realConfig struct {
	log *slog.Logger
	// openvpn is the path of the openvpn binary.
	openvpn string
	// runDir holds management sockets and generated configs.
	runDir string
	// stateDir holds the Reconciler's journal next to the stored profiles.
	stateDir string
}

// wireReal builds the real backends, the Reconciler on the macOS adapters, and
// the network monitor the Reconciler and the engines share. It is the only
// place that knows how the real parts fit together; the daemon core sees
// tunnel.Backend, tunnel.Reconciler and osnet.NetMonitor only.
//
// reconciler.New replays the journal of a previous run, so a daemon that was
// killed leaves nothing behind once it starts again.
func wireReal(cfg realConfig) ([]tunnel.Backend, tunnel.Reconciler, osnet.NetMonitor, error) {
	monitor := macos.NewNetMonitor(macos.NetMonitorOptions{Logger: cfg.log})
	rec, err := reconciler.New(reconciler.Config{
		Routes:      macos.NewRouteTable(),
		DNS:         macos.NewDNS(macos.DNSOptions{}),
		Net:         monitor,
		JournalPath: filepath.Join(cfg.stateDir, "journal"),
		Log:         cfg.log,
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("start the Reconciler: %w", err)
	}
	backends := []tunnel.Backend{
		ovpn.Backend(ovpn.Config{Binary: cfg.openvpn, RunDir: cfg.runDir, Log: cfg.log}),
		wg.Backend(wg.Config{Log: cfg.log}),
	}
	return backends, rec, monitor, nil
}
