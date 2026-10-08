//go:build !windows

package main

import "testing"

// The daemon of Unix systems takes flags only: a word like install is still the
// stray argument that parseFlags refuses, and nothing is a service.
func TestSubcommandsAndServiceModeDoNotExistOutsideWindows(t *testing.T) {
	for _, args := range [][]string{{"install"}, {"install", "-start"}, {"uninstall"}, {"start"}, {"stop"}, {"status"}} {
		if code, handled := runSubcommand(args); handled {
			t.Errorf("runSubcommand(%q) handled it with code %d", args, code)
		}
		if _, _, err := parseFlags(args); err == nil {
			t.Errorf("parseFlags(%q) accepted a subcommand word", args)
		}
	}
	if enterServiceMode() != nil {
		t.Error("enterServiceMode reports a service host on a system that has none")
	}
}
