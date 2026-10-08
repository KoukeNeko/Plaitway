//go:build !windows

package main

// runSubcommand: the daemon of other systems takes flags only, and is
// installed by launchd or the package manager.
func runSubcommand([]string) (exitCode int, handled bool) { return 0, false }
