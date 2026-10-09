//go:build !windows && !linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenVPNIsUsedAsConfiguredWithoutABuildTimeHash(t *testing.T) {
	source, runDir := installDirs(t, fakeOpenVPN)

	got, err := trustedOpenVPN(source, runDir)
	if err != nil || got != source {
		t.Fatalf("trustedOpenVPN = %q, %v; want the configured path", got, err)
	}
	if _, err := os.Stat(runDir); err == nil {
		t.Error("a development build touched the run directory")
	}
}

// The app bundle keeps openvpn in Contents/Resources/bin, next to
// Contents/MacOS where the daemon is; the flag default and the bundle must
// agree, or the packaged daemon finds no openvpn.
func TestOpenVPNFlagDefaultIsTheBinaryOfTheAppBundle(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(exe), "..", "Resources", "bin", "openvpn")

	cfg, _, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.openvpn != want || defaultOpenVPN() != want {
		t.Fatalf("-openvpn defaults to %q (defaultOpenVPN %q), want %q", cfg.openvpn, defaultOpenVPN(), want)
	}
}
