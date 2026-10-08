package rootguard

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// detach starts the guard without a console and in a process group of its own.
// The console sends Ctrl+C to every process attached to it, which would end the
// guard together with the test whose killing it is there for; a process in a
// new group that has no console receives none of it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
}
