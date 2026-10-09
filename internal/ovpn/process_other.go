//go:build !windows

package ovpn

import (
	"os/exec"
	"syscall"
)

// processGuard is what the OS keeps to make sure the child does not outlive
// the engine. A Unix child is stopped by its pid, and nothing more is needed;
// on Linux the kernel ends it with the daemon (see prepareCommand).
type processGuard struct{}

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
