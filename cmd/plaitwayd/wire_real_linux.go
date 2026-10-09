package main

import (
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/KoukeNeko/Plaitway/internal/fsperm"
	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/osnet/linux"
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

// wireReal builds the real backends, the Reconciler on the Linux adapters
// (netlink and systemd-resolved), and the network monitor the Reconciler and
// the engines share. It is the only place that knows how the real parts fit
// together; the daemon core sees tunnel.Backend, tunnel.Reconciler and
// osnet.NetMonitor only.
//
// reconciler.New replays the journal of a previous run, so a daemon that was
// killed leaves nothing behind once it starts again.
func wireReal(cfg realConfig) ([]tunnel.Backend, tunnel.Reconciler, osnet.NetMonitor, error) {
	// The journal is written to the state directory before the profile store
	// opens it, and it names every route the daemon added. systemd creates a
	// StateDirectory= with mode 0755 unless the unit says otherwise.
	if err := fsperm.MkdirAll(cfg.stateDir, 0o700); err != nil {
		return nil, nil, nil, fmt.Errorf("create the state directory: %w", err)
	}
	if err := fsperm.RestrictAndWarn(cfg.log, "state directory", cfg.stateDir); err != nil {
		return nil, nil, nil, fmt.Errorf("restrict the state directory: %w", err)
	}
	monitor := linux.NewNetMonitor(linux.NetMonitorOptions{Logger: cfg.log})
	dns := linux.NewDNS(linux.DNSOptions{Logger: cfg.log})
	rec, err := reconciler.New(reconciler.Config{
		Routes:      linux.NewRouteTable(),
		DNS:         dns,
		Net:         monitor,
		JournalPath: filepath.Join(cfg.stateDir, "journal"),
		Log:         cfg.log,
		Keying:      reconciler.KeyLinux,
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("start the Reconciler: %w", err)
	}
	// resolvectl may take its time to answer, and the answer changes nothing.
	go warnAboutResolver(cfg.log, resolvConfPath, resolvedDir, func() error {
		return linux.CheckResolved(linux.DNSOptions{Logger: cfg.log})
	})
	backends := []tunnel.Backend{
		ovpn.Backend(ovpn.Config{Binary: cfg.openvpn, RunDir: cfg.runDir, Log: cfg.log}),
		wg.Backend(wg.Config{Log: cfg.log}),
	}
	return backends, rec, monitor, nil
}
