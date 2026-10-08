//go:build !windows

package ovpn

import (
	"os/exec"
	"syscall"
)

// childEnv is the whole environment of the openvpn child. It runs
// /sbin/ifconfig by absolute path; nothing from the daemon's environment is
// needed, and none is passed on.
func childEnv() []string { return []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"} }

// processGuard is what the OS keeps to make sure the child does not outlive
// the engine. A Unix child is stopped by its pid, and nothing more is needed.
type processGuard struct{}

// prepareCommand sets the OS specific attributes of the child before it starts.
func prepareCommand(*exec.Cmd, string) {}

// guardProcess takes the child that has just started under the guard.
func guardProcess(*exec.Cmd) (processGuard, error) { return processGuard{}, nil }

// kill ends the child and everything it started, at once.
func (processGuard) kill(cmd *exec.Cmd) error { return cmd.Process.Kill() }

// close releases the guard.
func (processGuard) close() {}

// signalExit asks the child to exit without the management interface.
func signalExit(cmd *exec.Cmd) error { return cmd.Process.Signal(syscall.SIGTERM) }

// prepareProbe sets the OS specific attributes of the "--version" run.
func prepareProbe(*exec.Cmd) {}
