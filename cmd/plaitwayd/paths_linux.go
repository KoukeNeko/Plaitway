package main

const (
	// Where the systemd unit keeps the state of the daemon and its run files:
	// StateDirectory=plaitway (mode 0700) and RuntimeDirectory=plaitway.
	productionStateDir = "/var/lib/plaitway"
	productionRunDir   = "/run/plaitway"
)

// productionLocations has no log file: the daemon logs to stderr, which systemd
// sends to the journal.
func productionLocations() (locations, error) {
	return locations{stateDir: productionStateDir, runDir: productionRunDir}, nil
}
