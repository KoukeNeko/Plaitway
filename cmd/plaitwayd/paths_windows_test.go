package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// %ProgramData% is where the known-folder API puts it unless the machine was
// set up differently, which the environment says too.
func TestProductionLocationsAreBelowProgramData(t *testing.T) {
	programData := os.Getenv("ProgramData")
	if programData == "" {
		t.Skip("the environment does not name ProgramData to compare with")
	}
	got, err := productionLocations()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(programData, "Plaitway")
	want := locations{
		stateDir: root,
		runDir:   filepath.Join(root, "run"),
		logFile:  filepath.Join(root, "Logs", "plaitwayd.log"),
	}
	if !strings.EqualFold(got.stateDir, want.stateDir) || !strings.EqualFold(got.runDir, want.runDir) || !strings.EqualFold(got.logFile, want.logFile) {
		t.Fatalf("production locations %+v, want %+v", got, want)
	}
}

// The production location is chosen by the privilege of the process, so a
// development daemon never reaches for ProgramData.
func TestDefaultLocationsFollowThePrivilegeOfTheProcess(t *testing.T) {
	got, err := defaultLocations()
	if err != nil {
		t.Fatal(err)
	}
	want := developmentLocations()
	if isPrivileged() {
		if want, err = productionLocations(); err != nil {
			t.Fatal(err)
		}
	}
	if got != want {
		t.Fatalf("default locations %+v, want %+v", got, want)
	}
}

// The default socket of the daemon is the pipe, whatever the OS default for
// files is.
func TestDefaultSocketIsTheNamedPipe(t *testing.T) {
	cfg, _, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.socket != `\\.\pipe\plaitway` {
		t.Fatalf("socket %q, want the pipe \\\\.\\pipe\\plaitway", cfg.socket)
	}
}
