package ovpn

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeOpenVPNMain plays openvpn for the engine tests (fake_test.go).
var fakeOpenVPNMain func() int

func TestMain(m *testing.M) {
	if code, ok := runAsStub(); ok {
		os.Exit(code)
	}
	code := m.Run()
	removeStubMaster()
	os.Exit(code)
}

// A stub is a copy of the test binary that behaves like a program a test needs:
// an openvpn whose "--version" prints a given text, the fake openvpn of the
// engine tests, the script with which a real openvpn server verifies a
// password. It stands in for the shell scripts the tests would otherwise need,
// so that they run wherever Go does. The behaviour is kept next to the copy
// because the program is started by someone else (the probe, the engine,
// openvpn) with an environment of their choosing and nothing else to carry it.

const stubSuffix = ".stub"

type stubBehavior struct {
	Output  string // printed on stdout
	Exit    int
	Hang    bool   // never finishes, for the probe's timeout
	Counter string // a line is appended here on every run

	// FakeEnv, when set, makes the program the fake openvpn (fake_test.go) with
	// these OVPN_FAKE_* settings.
	FakeEnv map[string]string
	// VerifyPassword, when set, makes the program an auth-user-pass-verify
	// script in "via-file" mode: it exits 0 for the user VerifyUser with this
	// password and 1 for anything else.
	VerifyUser, VerifyPassword string
	// Listen makes the program listen on a loopback port, print "PORT <n>" and
	// keep every connection it gets open, so that a test can ask who owns the
	// other end of a connection.
	Listen bool
	// GuardedChild is the stub to start under guardProcess, as the engine starts
	// openvpn. The program prints "CHILD <pid>" and waits; a test ends the program
	// the hard way and looks whether the child went with it.
	GuardedChild string
}

// stubMaster is the one copy of the test binary the stubs of a test run are
// links to, so that a test does not copy a binary of many megabytes.
var stubMaster struct {
	once sync.Once
	dir  string
	path string
	err  error
}

func stubMasterPath() (string, error) {
	stubMaster.once.Do(func() {
		self, err := os.Executable()
		if err != nil {
			stubMaster.err = err
			return
		}
		if stubMaster.dir, err = os.MkdirTemp("", "ovpn-stub-"); err != nil {
			stubMaster.err = err
			return
		}
		stubMaster.path = filepath.Join(stubMaster.dir, "master"+exeSuffix())
		stubMaster.err = copyExecutable(self, stubMaster.path)
	})
	return stubMaster.path, stubMaster.err
}

func removeStubMaster() {
	if stubMaster.dir != "" {
		os.RemoveAll(stubMaster.dir)
	}
}

func writeStub(t *testing.T, dir, name string, behavior stubBehavior) string {
	t.Helper()
	master, err := stubMasterPath()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+exeSuffix())
	if err := os.Link(master, path); err != nil {
		if err := copyExecutable(master, path); err != nil {
			t.Fatal(err)
		}
	}
	encoded, err := json.Marshal(behavior)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+stubSuffix, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func copyExecutable(from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	return dst.Close()
}

// exeSuffix is what Windows needs at the end of a path to run it.
func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// runAsStub runs the behaviour kept next to this executable. ok is false for
// the test binary itself, which has none.
func runAsStub() (code int, ok bool) {
	self, err := os.Executable()
	if err != nil {
		return 0, false
	}
	encoded, err := os.ReadFile(self + stubSuffix)
	if err != nil {
		return 0, false
	}
	var behavior stubBehavior
	if err := json.Unmarshal(encoded, &behavior); err != nil {
		fmt.Fprintln(os.Stderr, "stub:", err)
		return 2, true
	}
	if behavior.Counter != "" {
		if err := appendLine(behavior.Counter, "run"); err != nil {
			fmt.Fprintln(os.Stderr, "stub:", err)
			return 2, true
		}
	}
	if behavior.Hang {
		time.Sleep(time.Minute)
	}
	if behavior.FakeEnv != nil {
		return runFake(behavior.FakeEnv), true
	}
	if behavior.VerifyPassword != "" {
		return verifyPassword(behavior), true
	}
	if behavior.Listen {
		return listenAndHold(), true
	}
	if behavior.GuardedChild != "" {
		return startGuardedChild(behavior.GuardedChild), true
	}
	fmt.Print(behavior.Output)
	return behavior.Exit, true
}

func runFake(env map[string]string) int {
	for name, value := range env {
		os.Setenv(name, value)
	}
	return fakeOpenVPNMain()
}

// verifyPassword is an auth-user-pass-verify script: openvpn passes the name of
// a file with the user name on its first line and the password on its second.
func verifyPassword(behavior stubBehavior) int {
	if len(os.Args) < 2 {
		return 1
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		return 1
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r", ""), "\n")
	if len(lines) >= 2 && lines[0] == behavior.VerifyUser && lines[1] == behavior.VerifyPassword {
		return 0
	}
	return 1
}

func appendLine(path, line string) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(file, line); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func listenAndHold() int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "stub:", err)
		return 2
	}
	fmt.Printf("PORT %d\n", ln.Addr().(*net.TCPAddr).Port)
	for {
		conn, err := ln.Accept()
		if err != nil {
			return 2
		}
		defer conn.Close()
	}
}

func startGuardedChild(path string) int {
	cmd := exec.Command(path)
	prepareCommand(cmd, filepath.Dir(path))
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "stub:", err)
		return 2
	}
	if _, err := guardProcess(cmd); err != nil {
		fmt.Fprintln(os.Stderr, "stub:", err)
		return 2
	}
	fmt.Printf("CHILD %d\n", cmd.Process.Pid)
	time.Sleep(time.Minute)
	return 0
}
