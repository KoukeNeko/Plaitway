package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/KoukeNeko/Plaitway/internal/fsperm"
	"github.com/KoukeNeko/Plaitway/internal/transport"
)

// privateEditAccess is what describeAccess says of the file an editor is given
// and of its directory: access lists that name nobody but the user and the
// administrators.
const privateEditAccess = "private\nprivate\n"

var pipeCounter atomic.Uint64

// deviceReference is a profile line's word for a device, and what is said of it.
const (
	deviceReference = "NUL"
	// A device name is a file name in every directory.
	deviceRefusal = "is not a regular file"
)

// notSocketRefusal is what is said of a path that is a file, not a pipe.
func notSocketRefusal(path string) string { return path + " is not a named pipe" }

// newSocketPath is a pipe name for a daemon. Pipe names are machine-wide, so
// parallel tests and repeated runs each need one of their own.
func newSocketPath(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf(`\\.\pipe\plaitway-cli-test-%d-%d`, os.Getpid(), pipeCounter.Add(1))
}

// listenAt creates the pipe at path with the access list the daemon gives it.
func listenAt(t *testing.T, path string) net.Listener {
	t.Helper()
	listener, err := transport.Listen(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	return listener
}

// relativeSocketArgs has nothing to give: a pipe is named by its full name.
func relativeSocketArgs(*testing.T, string) [][]string { return nil }

// scriptEnvironment is the environment of a program that a script starts.
// Windows programs find the temporary directory and the system files in it.
func scriptEnvironment() []string {
	environment := make([]string, 0, 4)
	for _, name := range []string{"PATH", "SystemRoot", "TEMP", "TMP"} {
		environment = append(environment, name+"="+os.Getenv(name))
	}
	return environment
}

// prepareToBeInterrupted puts cmd in a process group of its own: Ctrl-Break is
// sent to a group, and must not reach the test.
func prepareToBeInterrupted(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

// interruptProcess ends p as Ctrl-Break at a console does; Go reads it like
// Ctrl-C. The test is skipped when this process has no console to send it from.
func interruptProcess(t *testing.T, p *os.Process) {
	t.Helper()
	err := terminateProcess(p)
	if errors.Is(err, windows.ERROR_INVALID_HANDLE) {
		p.Kill()
		t.Skipf("this test process has no console to send Ctrl-Break from: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// terminateProcess asks p, which prepareToBeInterrupted has made a group, to
// stop. The daemon takes Ctrl-Break as it takes SIGTERM.
func terminateProcess(p *os.Process) error {
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(p.Pid))
}

// describeAccess says who can reach a file and its directory: "private" for an
// access list that names nobody but the user and the administrators, one line
// each.
func describeAccess(file, dir string) (string, error) {
	var described strings.Builder
	for _, path := range []string{file, dir} {
		private, err := fsperm.IsPrivate(path)
		if err != nil {
			return "", err
		}
		word := "open"
		if private {
			word = "private"
		}
		described.WriteString(word + "\n")
	}
	return described.String(), nil
}

// editorCommandCases are the lines of editorCommand's table that depend on how
// the OS writes a command.
var editorCommandCases = map[string]struct {
	visual, editor string
	want           []string
	wantErr        bool
}{
	"quoted":                   {`"C:\Program Files\My Editor\ed.exe" --new-window`, "", []string{`C:\Program Files\My Editor\ed.exe`, "--new-window"}, false},
	"backslashes are the path": {`C:\tools\ed.exe -w`, "", []string{`C:\tools\ed.exe`, "-w"}, false},
	"a hash is not a comment":  {"# nothing", "", []string{"#", "nothing"}, false},
}
