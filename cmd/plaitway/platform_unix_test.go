//go:build unix

package main

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// privateEditAccess is what describeAccess says of the file an editor is given
// and of its directory: mode bits.
const privateEditAccess = "-rw-------\ndrwx------\n"

// deviceReference is a profile line's word for a device, and what is said of it.
const (
	deviceReference = "/dev/null"
	deviceRefusal   = "is outside the directory of the profile"
)

// notSocketRefusal is what is said of a path that is a file, not a socket.
// Linux answers connect(2) on a file with ECONNREFUSED, as for a socket that
// nobody listens on; macOS answers with an error of its own.
func notSocketRefusal(path string) string {
	if runtime.GOOS == "darwin" {
		return "cannot connect to " + path + ": "
	}
	return "nothing listens on " + path
}

// newSocketPath is a path for the socket of a daemon, in a directory of its own.
func newSocketPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(shortDir(t), "s.sock")
}

// listenAt listens on the socket at path. Not transport.Listen: it narrows the
// umask of the whole process while it binds, and a directory another parallel
// test creates meanwhile has no permission to be entered.
func listenAt(t *testing.T, path string) net.Listener {
	t.Helper()
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	return listener
}

// relativeSocketArgs are command lines that name the socket by a relative path,
// from the directory the test has moved to.
func relativeSocketArgs(t *testing.T, socket string) [][]string {
	t.Helper()
	t.Chdir(filepath.Dir(socket))
	name := filepath.Base(socket)
	return [][]string{
		{"-socket", name, "list"},
		{"list", "-socket", name},
		{"list", "-socket=" + name},
	}
}

// scriptEnvironment is the environment of a program that a script starts.
func scriptEnvironment() []string {
	return []string{"PATH=" + os.Getenv("PATH")}
}

// prepareToBeInterrupted makes cmd a process that interruptProcess can reach.
func prepareToBeInterrupted(*exec.Cmd) {}

// interruptProcess ends p as Ctrl-C at a terminal does.
func interruptProcess(t *testing.T, p *os.Process) {
	t.Helper()
	if err := p.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
}

// terminateProcess ends p the way launchd ends the daemon.
func terminateProcess(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}

// describeAccess says who can reach a file and its directory: their mode bits,
// one line each.
func describeAccess(file, dir string) (string, error) {
	var described strings.Builder
	for _, path := range []string{file, dir} {
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		described.WriteString(info.Mode().String() + "\n")
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
	"quoted":        {`"/Applications/My Editor/ed" --new-window`, "", []string{"/Applications/My Editor/ed", "--new-window"}, false},
	"escaped":       {`/Applications/My\ Editor/ed -w`, "", []string{"/Applications/My Editor/ed", "-w"}, false},
	"nothing in it": {"# nothing", "", nil, true},
}
