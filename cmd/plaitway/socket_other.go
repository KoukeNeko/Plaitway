//go:build !windows

package main

// defaultSocket is where the LaunchDaemon listens; the -socket argument in its
// launchd plist (packaging/lib.sh) and DaemonLocation.swift say the same.
const defaultSocket = "/var/run/plaitway/plaitwayd.sock"

// checkSocketPath accepts any path: whatever is not a socket fails when it is
// connected to, with the reason of the OS.
func checkSocketPath(string) error { return nil }
