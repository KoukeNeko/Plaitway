//go:build !windows

package main

import (
	"log/slog"

	"github.com/KoukeNeko/Plaitway/internal/fsperm"
)

const (
	// Where the LaunchDaemon keeps its state, sockets and logs.
	productionStateDir = "/Library/Application Support/Plaitway"
	productionRunDir   = "/var/run/plaitway"
	productionLogFile  = "/Library/Logs/Plaitway/plaitwayd.log"
)

func productionLocations() (locations, error) {
	return locations{stateDir: productionStateDir, runDir: productionRunDir, logFile: productionLogFile}, nil
}

// ensureRunDir creates the run directory. It stays open to every user: the
// control socket is created in it, and the clients have to reach that.
func ensureRunDir(_ *slog.Logger, dir string) error {
	return fsperm.MkdirAll(dir, runDirMode)
}
