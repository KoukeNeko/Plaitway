package main

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/transport"
)

func TestParseFlagsDefaults(t *testing.T) {
	cfg, level, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.socketMode != 0o600 || cfg.fake != nil || level != slog.LevelInfo {
		t.Fatalf("defaults: mode %v, fake %v, level %v; want 0600, real engines, info", cfg.socketMode, cfg.fake, level)
	}
	if cfg.socket == "" || cfg.stateDir == "" || cfg.runDir == "" {
		t.Fatalf("a default is empty: %+v", cfg)
	}
}

func TestParseFlagsValues(t *testing.T) {
	cfg, level, err := parseFlags([]string{
		"-socket", "/var/run/plaitway/plaitwayd.sock", "-socket-mode", "0666", "-state-dir", "/s", "-run-dir", "/r",
		"-openvpn", "/o/openvpn", "-log-level", "debug", "-fake",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.socket != "/var/run/plaitway/plaitwayd.sock" || cfg.socketMode != 0o666 || cfg.stateDir != "/s" || cfg.runDir != "/r" ||
		cfg.openvpn != "/o/openvpn" || level != slog.LevelDebug || cfg.fake == nil {
		t.Fatalf("cfg = %+v, level %v", cfg, level)
	}
}

func TestParseFlagsRejectsBadInput(t *testing.T) {
	for name, args := range map[string][]string{
		"mode not octal":    {"-socket-mode", "rw"},
		"mode with 8":       {"-socket-mode", "0668"},
		"mode too large":    {"-socket-mode", "01777"},
		"unknown log level": {"-log-level", "chatty"},
		"unknown flag":      {"-nope"},
		"stray argument":    {"extra"},
	} {
		if _, _, err := parseFlags(args); err == nil {
			t.Errorf("%s: accepted %v", name, args)
		}
	}
}

func TestDefaultLocationsOfAnUnprivilegedDaemonAreUnderTheTemporaryDirectory(t *testing.T) {
	if isPrivileged() {
		t.Skip("a privileged daemon gets the production locations")
	}
	got, err := defaultLocations()
	if err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Clean(os.TempDir())
	if filepath.Dir(got.stateDir) != tmp || filepath.Dir(got.runDir) != tmp {
		t.Fatalf("state %s, run %s: want them under %s", got.stateDir, got.runDir, os.TempDir())
	}
	if got.logFile != "" {
		t.Fatalf("log file %s: a development daemon logs to stderr only", got.logFile)
	}
	if got != developmentLocations() {
		t.Fatalf("locations %+v, want %+v", got, developmentLocations())
	}
}

// The flags show the defaults of the OS the daemon runs on, and the default
// socket is the one the command line client also dials.
func TestParseFlagsDefaultToTheLocationsAndTheSocketOfThisOS(t *testing.T) {
	cfg, _, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	want, err := defaultLocations()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.stateDir != want.stateDir || cfg.runDir != want.runDir || cfg.logFile != want.logFile {
		t.Fatalf("flag defaults %q %q %q, want %+v", cfg.stateDir, cfg.runDir, cfg.logFile, want)
	}
	if cfg.socket != transport.DefaultPath() {
		t.Fatalf("socket default %q, want %q", cfg.socket, transport.DefaultPath())
	}
}

// A launch argument that was valid before stays valid on every OS, even where
// it has no effect.
func TestParseFlagsAcceptsTheSocketModeOnEveryOS(t *testing.T) {
	cfg, _, err := parseFlags([]string{"-socket-mode", "0666"})
	if err != nil || cfg.socketMode != 0o666 {
		t.Fatalf("socket mode %v, %v; want 0666", cfg.socketMode, err)
	}
}

// The helper process of the log tests: the test binary runs itself so that
// what happens to the stderr of a whole process can be seen from outside.
const (
	helperModeEnv = "PLAITWAYD_TEST_HELPER"
	helperLogEnv  = "PLAITWAYD_TEST_LOG_FILE"
	helperCrash   = "crash"
	helperLog     = "log"
	crashMessage  = "boom from the helper process"
	loggedLine    = "line from the helper process"
)

func TestHelperProcess(t *testing.T) {
	mode := os.Getenv(helperModeEnv)
	if mode == "" {
		t.Skip("only runs as the helper process of another test")
	}
	out, closeLog, err := logOutput(os.Getenv(helperLogEnv))
	if err != nil {
		fmt.Fprintln(os.Stderr, "logOutput:", err)
		os.Exit(2)
	}
	defer closeLog()
	log, _ := newLogger(out, slog.LevelInfo)
	log.Info(loggedLine)
	if mode == helperCrash {
		panic(crashMessage)
	}
}

// runHelper runs the helper process with stderr on a pipe, as a service has it.
func runHelper(t *testing.T, mode, logFile string) (stderr string, err error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), helperModeEnv+"="+mode, helperLogEnv+"="+logFile)
	var captured bytes.Buffer
	cmd.Stderr = &captured
	err = cmd.Run()
	return captured.String(), err
}

// A daemon with no terminal must not lose the trace of a panic: it lands in the
// log file, not in a stderr that goes nowhere.
func TestLogOutputKeepsThePanicTraceOfAProcessWithoutATerminal(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "Logs", "plaitwayd.log")

	stderr, err := runHelper(t, helperCrash, logFile)
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("helper: %v, want it to crash", err)
	}

	logged, readErr := os.ReadFile(logFile)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, want := range []string{loggedLine, "panic: " + crashMessage, "goroutine "} {
		if !strings.Contains(string(logged), want) {
			t.Errorf("log file lacks %q:\n%s", want, logged)
		}
	}
	if strings.Contains(stderr, crashMessage) {
		t.Errorf("the panic went to the original stderr as well:\n%s", stderr)
	}
}

func TestLogOutputMovesAsideALogAboveTheLimit(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "plaitwayd.log")
	if err := os.WriteFile(logFile, bytes.Repeat([]byte("x"), maxLogFileSize+1), 0o600); err != nil {
		t.Fatal(err)
	}

	if stderr, err := runHelper(t, helperLog, logFile); err != nil {
		t.Fatalf("helper: %v\n%s", err, stderr)
	}

	if fi, err := os.Stat(logFile + ".1"); err != nil || fi.Size() != maxLogFileSize+1 {
		t.Errorf("rotated log: %v, %v; want the old %d bytes", fi, err, maxLogFileSize+1)
	}
	logged, err := os.ReadFile(logFile)
	if err != nil || !strings.Contains(string(logged), loggedLine) || len(logged) > 1<<10 {
		t.Errorf("new log: %d bytes, %v; want only the new line", len(logged), err)
	}
}
