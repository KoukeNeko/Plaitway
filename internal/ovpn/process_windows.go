package ovpn

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobExitCode is the exit status of a child that the engine ended by force.
const jobExitCode = 1

var procNtResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

// childEnv is the whole environment of the openvpn child. Windows needs the
// system directory to find its libraries and to run anything at all, and a PATH
// of the system directories only keeps a directory a standard user can write to
// from ever being searched. The daemon's own environment is not passed on.
func childEnv() []string {
	system, err := windows.GetSystemWindowsDirectory()
	if err != nil {
		system = `C:\Windows`
	}
	return []string{
		"SystemRoot=" + system,
		"windir=" + system,
		"PATH=" + filepath.Join(system, "System32") + ";" + system,
	}
}

// processGuard is the job object the child runs in. It ends the child when the
// last handle to it is closed, which is also when the daemon dies, so a crashed
// daemon leaves no openvpn behind.
type processGuard struct{ job *jobHandle }

// jobHandle serializes the use of the handle with its closing: a handle number
// is reused by the next one opened, and a kill that raced with the close would
// then end something else.
type jobHandle struct {
	mu     sync.Mutex
	handle windows.Handle
}

// prepareCommand makes the child start without a console window, in the
// engine's private directory, and suspended: it must be in the job before it
// runs a single instruction.
func prepareCommand(cmd *exec.Cmd, dir string) {
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_SUSPENDED,
	}
}

// guardProcess puts the suspended child in a new job object that kills it on
// close, and lets it run. The child is not left running on failure.
func guardProcess(cmd *exec.Cmd) (processGuard, error) {
	handle, err := newKillOnCloseJob()
	if err != nil {
		return processGuard{}, err
	}
	guard := processGuard{job: &jobHandle{handle: handle}}
	if err := assignAndResume(cmd, handle); err != nil {
		guard.close()
		return processGuard{}, err
	}
	return guard, nil
}

func newKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("create the job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	if err != nil {
		windows.CloseHandle(job)
		return 0, fmt.Errorf("make the job object end its processes on close: %w", err)
	}
	return job, nil
}

func assignAndResume(cmd *exec.Cmd, job windows.Handle) error {
	var assignErr error
	withErr := cmd.Process.WithHandle(func(handle uintptr) {
		if assignErr = windows.AssignProcessToJobObject(job, windows.Handle(handle)); assignErr != nil {
			return
		}
		if status, _, _ := procNtResumeProcess.Call(handle); status != 0 {
			assignErr = fmt.Errorf("NtResumeProcess: status %#x", status)
		}
	})
	switch {
	case withErr != nil:
		return fmt.Errorf("reach the process: %w", withErr)
	case assignErr != nil:
		return fmt.Errorf("supervise the process: %w", assignErr)
	}
	return nil
}

// kill ends the child and everything it started. The job is gone once the
// child has exited and been waited for; there is then nothing left to end.
func (g processGuard) kill(cmd *exec.Cmd) error {
	if g.job == nil {
		return cmd.Process.Kill()
	}
	g.job.mu.Lock()
	defer g.job.mu.Unlock()
	if g.job.handle == 0 {
		return nil
	}
	return windows.TerminateJobObject(g.job.handle, jobExitCode)
}

// close closes the job, which ends what is still in it.
func (g processGuard) close() {
	if g.job == nil {
		return
	}
	g.job.mu.Lock()
	defer g.job.mu.Unlock()
	if g.job.handle != 0 {
		windows.CloseHandle(g.job.handle)
		g.job.handle = 0
	}
}

// signalExit has no meaning on Windows: a console process can be sent a break,
// but openvpn has no console here, and the management interface is the way to
// ask it to exit.
func signalExit(*exec.Cmd) error { return errNoExitSignal }

// prepareProbe makes the "--version" run without a console window and with the
// same environment as the tunnel itself.
func prepareProbe(cmd *exec.Cmd) {
	cmd.Env = childEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}
