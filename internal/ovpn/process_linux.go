package ovpn

import (
	"os/exec"
	"runtime"
	"syscall"
)

// childEnv is the whole environment of the openvpn child. A build of openvpn
// without its own netlink code runs ip(8), which script-security 1 allows, and
// distributions install it in /sbin, /usr/sbin or /usr/bin; nothing from the
// daemon's environment is needed, and none is passed on.
func childEnv() []string { return []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"} }

// prepareCommand sets the OS specific attributes of the child before it starts.
//
// The child is in a process group of its own, so that a signal for the daemon's
// group (Ctrl-C in a terminal, the hang-up of a closed one) does not reach it:
// openvpn is stopped through its management interface, in order. And it dies
// with the daemon, however the daemon ends: otherwise the child would live on
// with its tunnel interface after a crash, and the next start would find a
// second client of the same profile.
//
// The kernel sends Pdeathsig when the thread that started the child ends, not
// the process, and the Go runtime ends a thread only when the goroutine locked
// to it ends. So the calling goroutine, the engine's, is locked to its thread
// for good: the thread then ends with the goroutine, when the child is gone.
func prepareCommand(cmd *exec.Cmd, _ string) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	runtime.LockOSThread()
}
