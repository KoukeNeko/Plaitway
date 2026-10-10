package main

import "os"

// defaultSocket is where the service's daemon listens
// (RuntimeDirectory=plaitway in the unit, /run/plaitway in the OpenRC script),
// and where the app looks for it.
const defaultSocket = "/run/plaitway/plaitwayd.sock"

const (
	// systemdRunDir is there when systemd is the init of the system, and
	// openrcRunDir when OpenRC is.
	systemdRunDir = "/run/systemd/system"
	openrcRunDir  = "/run/openrc"
)

// isDirectory reports whether path is a directory; the tests replace it.
var isDirectory = func(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// checkSocketPath accepts any path: whatever is not a socket fails when it is
// connected to, with the reason of the OS.
func checkSocketPath(string) error { return nil }

// notRunningHint points at the service when the production daemon is the one
// that cannot be reached, with the command of the init that runs it; a daemon at
// another socket was started by hand.
func notRunningHint(socket string) string {
	if socket != defaultSocket {
		return ""
	}
	switch {
	case isDirectory(systemdRunDir):
		return `; run "systemctl status plaitwayd"`
	case isDirectory(openrcRunDir):
		return `; run "rc-service plaitwayd status"`
	}
	return ""
}
