package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The journal names every route the daemon adds, so the directory it is in is
// private before anything is written to it, also when systemd created it with
// the mode of a StateDirectory= that has no StateDirectoryMode=.
func TestWireRealMakesTheStateDirectoryPrivateBeforeTheJournalIsWritten(t *testing.T) {
	stateDir := filepath.Join(shortDir(t), "state")
	if err := os.Mkdir(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The check of the resolver writes to the log from a goroutine of its own.
	var out syncBuffer

	backends, rec, monitor, err := wireReal(realConfig{log: slog.New(slog.NewTextHandler(&out, nil)), runDir: filepath.Join(filepath.Dir(stateDir), "run"), stateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(backends) != 2 || rec == nil || monitor == nil {
		t.Fatalf("wireReal returned %d backends, %v, %v; want both engines, the Reconciler and the monitor", len(backends), rec, monitor)
	}
	if fi, err := os.Stat(stateDir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("state directory: %v, %v; want mode 0700", fi, err)
	}
	if fi, err := os.Stat(filepath.Join(stateDir, "journal")); err != nil || fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("journal: %v, %v; want it there and private", fi, err)
	}
	if !strings.Contains(out.String(), "state directory was accessible to others") {
		t.Errorf("the log does not say that the directory was restricted:\n%s", out.String())
	}
}

func TestWireRealCreatesAMissingStateDirectoryPrivate(t *testing.T) {
	stateDir := filepath.Join(shortDir(t), "var", "lib", "plaitway")
	if _, _, _, err := wireReal(realConfig{log: discardLog(), runDir: filepath.Join(filepath.Dir(stateDir), "run"), stateDir: stateDir}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(stateDir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("state directory: %v, %v; want mode 0700", fi, err)
	}
}
