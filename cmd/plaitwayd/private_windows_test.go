package main

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/fsperm"
	"github.com/KoukeNeko/Plaitway/internal/fsperm/fspermtest"
)

// everyoneSID is the group of all accounts; icacls takes a SID after a "*".
const everyoneSID = "*S-1-1-0"

func mustBePrivate(t *testing.T, path string) {
	t.Helper()
	private, err := fsperm.IsPrivate(path)
	if err != nil {
		t.Fatal(err)
	}
	if !private {
		t.Errorf("%s can be reached by accounts that are not SYSTEM, Administrators or the owner", path)
	}
}

// Under a service nobody sits at the log, so what it holds (endpoints, users)
// must not be readable by the other users of the machine.
func TestLogFileAndItsDirectoryAreCreatedPrivate(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "Logs", "plaitwayd.log")

	if stderr, err := runHelper(t, helperLog, logFile); err != nil {
		t.Fatalf("helper: %v\n%s", err, stderr)
	}

	mustBePrivate(t, filepath.Dir(logFile))
	mustBePrivate(t, logFile)
}

func TestLogFileLeftOpenByAnEarlierRunIsRestricted(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "plaitwayd.log")
	if err := os.WriteFile(logFile, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("icacls", logFile, "/grant", everyoneSID+":R").CombinedOutput(); err != nil {
		t.Fatalf("icacls: %v\n%s", err, out)
	}
	if private, _ := fsperm.IsPrivate(logFile); private {
		t.Fatal("the log was not opened up for the test")
	}

	if stderr, err := runHelper(t, helperLog, logFile); err != nil {
		t.Fatalf("helper: %v\n%s", err, stderr)
	}

	mustBePrivate(t, logFile)
	if logged, _ := os.ReadFile(logFile); !strings.HasPrefix(string(logged), "old\n") {
		t.Errorf("the old lines were lost: %q", logged)
	}
}

// A link in place of the log would make a privileged daemon append to whatever
// the link names, so the daemon stops with the reason instead.
func TestLogOutputRefusesALinkInPlaceOfTheLogFile(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(base, "plaitwayd.log")
	fspermtest.MakeJunction(t, logFile, outside)

	stderr, err := runHelper(t, helperLog, logFile)

	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 {
		t.Fatalf("helper: %v, want it to stop with exit code 2", err)
	}
	if !strings.Contains(stderr, fsperm.ErrReparsePoint.Error()) {
		t.Errorf("stderr does not give the reason: %q", stderr)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("something was written where the link points: %v", entries)
	}
}

func TestEnsureRunDirRefusesAJunction(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "run")
	fspermtest.MakeJunction(t, dir, outside)

	err := ensureRunDir(slog.New(slog.NewTextHandler(io.Discard, nil)), dir)

	if !errors.Is(err, fsperm.ErrReparsePoint) {
		t.Fatalf("ensureRunDir = %v, want an error wrapping ErrReparsePoint", err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("something was written where the junction points: %v", entries)
	}
}

func TestEnsureRunDirCreatesAPrivateDirectoryAndRepairsAnOpenOne(t *testing.T) {
	var logged bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logged, nil))
	dir := filepath.Join(t.TempDir(), "Plaitway", "run")

	if err := ensureRunDir(log, dir); err != nil {
		t.Fatal(err)
	}
	mustBePrivate(t, dir)
	if logged.Len() != 0 {
		t.Fatalf("a new directory was reported: %s", logged.String())
	}

	if out, err := exec.Command("icacls", dir, "/grant", everyoneSID+":(OI)(CI)R").CombinedOutput(); err != nil {
		t.Fatalf("icacls: %v\n%s", err, out)
	}
	if err := ensureRunDir(log, dir); err != nil {
		t.Fatal(err)
	}
	mustBePrivate(t, dir)
	if !strings.Contains(logged.String(), "run directory was accessible to others") {
		t.Fatalf("the repair was not reported: %q", logged.String())
	}
}
