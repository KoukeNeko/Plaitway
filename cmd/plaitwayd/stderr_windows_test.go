package main

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// withStderr makes f the stderr that stderrIsTerminal looks at.
func withStderr(t *testing.T, f *os.File) {
	t.Helper()
	original := os.Stderr
	os.Stderr = f
	t.Cleanup(func() { os.Stderr = original })
}

func TestStderrIsNoTerminalWhenItIsAFileAPipeOrNUL(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "stderr.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	pipeReader, pipeWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pipeReader.Close()
	defer pipeWriter.Close()
	nul, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer nul.Close()

	for name, f := range map[string]*os.File{"file": file, "pipe": pipeWriter, "NUL": nul} {
		t.Run(name, func(t *testing.T) {
			withStderr(t, f)
			if stderrIsTerminal() {
				t.Fatal("stderrIsTerminal() = true")
			}
		})
	}
}

// A service starts with no standard handles; the log file has to take over
// then, not be skipped because the handle cannot be asked.
func TestStderrIsNoTerminalWhenTheHandleIsInvalid(t *testing.T) {
	withStderr(t, os.NewFile(uintptr(windows.InvalidHandle), "invalid"))
	if stderrIsTerminal() {
		t.Fatal("stderrIsTerminal() = true for an invalid handle")
	}
}

func TestStderrIsATerminalWhenItIsTheConsole(t *testing.T) {
	name, err := windows.UTF16PtrFromString("CONOUT$")
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Skipf("this test process has no console to open (%v)", err)
	}
	console := os.NewFile(uintptr(handle), "CONOUT$")
	defer console.Close()

	withStderr(t, console)
	if !stderrIsTerminal() {
		t.Fatal("stderrIsTerminal() = false for the console")
	}
}
