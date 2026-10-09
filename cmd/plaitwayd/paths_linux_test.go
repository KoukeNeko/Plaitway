package main

import "testing"

// The systemd unit (StateDirectory, RuntimeDirectory) and the app name these
// paths. Nothing is logged to a file: stderr goes to the journal.
func TestProductionLocationsAreTheSystemdOnes(t *testing.T) {
	got, err := productionLocations()
	if err != nil {
		t.Fatal(err)
	}
	want := locations{stateDir: "/var/lib/plaitway", runDir: "/run/plaitway"}
	if got != want {
		t.Fatalf("production locations %+v, want %+v", got, want)
	}
}
