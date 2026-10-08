package ovpn

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// fakeOpenVPNMain plays openvpn for the engine tests (fake_test.go, Unix only).
// It is set there, so the other platforms run no fake.
var fakeOpenVPNMain func() int

func TestMain(m *testing.M) {
	if code, ok := runAsStub(); ok {
		os.Exit(code)
	}
	if fakeOpenVPNMain != nil && os.Getenv("OVPN_FAKE") != "" {
		os.Exit(fakeOpenVPNMain())
	}
	os.Exit(m.Run())
}

// A stub is a copy of the test binary that behaves like an openvpn whose
// "--version" prints a given text. It stands in for the shell scripts the
// probe tests would otherwise need, so that they run wherever Go does. The
// behaviour is kept next to the copy because the probe starts it with the
// daemon's environment and nothing else to carry it.

const stubSuffix = ".stub"

type stubBehavior struct {
	Output  string // printed on stdout
	Exit    int
	Hang    bool   // never finishes, for the probe's timeout
	Counter string // a line is appended here on every run
}

func writeStub(t *testing.T, dir, name string, behavior stubBehavior) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+exeSuffix())
	if err := copyExecutable(self, path); err != nil {
		t.Fatal(err)
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
	fmt.Print(behavior.Output)
	return behavior.Exit, true
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
