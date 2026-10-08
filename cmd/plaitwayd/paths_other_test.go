//go:build !windows

package main

import "testing"

// The LaunchDaemon plist and the app (DaemonLocation.swift) name these paths.
func TestProductionLocationsAreTheLaunchDaemonOnes(t *testing.T) {
	got, err := productionLocations()
	if err != nil {
		t.Fatal(err)
	}
	want := locations{
		stateDir: "/Library/Application Support/Plaitway",
		runDir:   "/var/run/plaitway",
		logFile:  "/Library/Logs/Plaitway/plaitwayd.log",
	}
	if got != want {
		t.Fatalf("production locations %+v, want %+v", got, want)
	}
}
