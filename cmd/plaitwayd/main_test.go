package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
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

func TestDefaultDirsOfAnUnprivilegedDaemonAreUnderTheTemporaryDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root gets the production directories")
	}
	state, run := defaultDirs()
	if tmp := filepath.Clean(os.TempDir()); filepath.Dir(state) != tmp || filepath.Dir(run) != tmp {
		t.Fatalf("state %s, run %s: want them under %s", state, run, os.TempDir())
	}
}
