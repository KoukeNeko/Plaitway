//go:build !windows && !linux

package main

const (
	// Where the LaunchDaemon keeps its state, sockets and logs.
	productionStateDir = "/Library/Application Support/Plaitway"
	productionRunDir   = "/var/run/plaitway"
	productionLogFile  = "/Library/Logs/Plaitway/plaitwayd.log"
)

func productionLocations() (locations, error) {
	return locations{stateDir: productionStateDir, runDir: productionRunDir, logFile: productionLogFile}, nil
}
