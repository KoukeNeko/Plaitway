package main

// defaultSocket is where the systemd unit's daemon listens
// (RuntimeDirectory=plaitway), and where the app looks for it.
const defaultSocket = "/run/plaitway/plaitwayd.sock"

// checkSocketPath accepts any path: whatever is not a socket fails when it is
// connected to, with the reason of the OS.
func checkSocketPath(string) error { return nil }

// notRunningHint points at the unit when the production daemon is the one that
// cannot be reached; a daemon at another socket was started by hand.
func notRunningHint(socket string) string {
	if socket != defaultSocket {
		return ""
	}
	return `; run "systemctl status plaitwayd"`
}
