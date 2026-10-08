package main

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// openConsole opens the input or the output of the console of this process.
// A test process without a console (a service, a hidden build agent) skips.
func openConsole(t *testing.T, name string, access uint32) *os.File {
	t.Helper()
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(path, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Skipf("this test process has no console to open (%v)", err)
	}
	console := os.NewFile(uintptr(handle), name)
	t.Cleanup(func() { console.Close() })
	return console
}

func consoleMode(t *testing.T, console *os.File) uint32 {
	t.Helper()
	var mode uint32
	if err := windows.GetConsoleMode(windows.Handle(console.Fd()), &mode); err != nil {
		t.Fatal(err)
	}
	return mode
}

func TestIsTerminalIsTrueForTheConsoleOnly(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "file.txt"))
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
	nul, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer nul.Close()

	for name, f := range map[string]*os.File{
		"file":           file,
		"pipe":           pipeReader,
		"NUL":            nul,
		"invalid handle": os.NewFile(uintptr(windows.InvalidHandle), "invalid"),
	} {
		if isTerminal(f) {
			t.Errorf("isTerminal(%s) = true", name)
		}
	}

	if !isTerminal(openConsole(t, "CONOUT$", windows.GENERIC_READ|windows.GENERIC_WRITE)) {
		t.Error("isTerminal(console output) = false")
	}
	if !isTerminal(openConsole(t, "CONIN$", windows.GENERIC_READ|windows.GENERIC_WRITE)) {
		t.Error("isTerminal(console input) = false")
	}
}

// The password prompt reads with the echo off, and leaves the console as it
// found it, whatever it found.
func TestDisableEchoSwitchesTheEchoOffUntilItIsRestored(t *testing.T) {
	input := openConsole(t, "CONIN$", windows.GENERIC_READ|windows.GENERIC_WRITE)
	saved := consoleMode(t, input)
	t.Cleanup(func() { windows.SetConsoleMode(windows.Handle(input.Fd()), saved) })

	for name, start := range map[string]uint32{
		"echo on":  saved | windows.ENABLE_ECHO_INPUT,
		"echo off": saved &^ windows.ENABLE_ECHO_INPUT,
	} {
		if err := windows.SetConsoleMode(windows.Handle(input.Fd()), start); err != nil {
			t.Fatal(err)
		}
		restore, err := disableEcho(input)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if during := consoleMode(t, input); during != start&^windows.ENABLE_ECHO_INPUT {
			t.Errorf("%s: mode while hidden %#x, want %#x", name, during, start&^windows.ENABLE_ECHO_INPUT)
		}
		restore()
		if after := consoleMode(t, input); after != start {
			t.Errorf("%s: mode after restore %#x, want %#x", name, after, start)
		}
	}
}

func TestDisableEchoRefusesWhatIsNoConsole(t *testing.T) {
	pipeReader, pipeWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pipeReader.Close()
	defer pipeWriter.Close()
	if restore, err := disableEcho(pipeReader); err == nil {
		restore()
		t.Error("disableEcho of a pipe succeeded")
	}
}

func TestEnableEscapeSequencesSwitchesTheConsoleToThem(t *testing.T) {
	output := openConsole(t, "CONOUT$", windows.GENERIC_READ|windows.GENERIC_WRITE)
	saved := consoleMode(t, output)
	t.Cleanup(func() { windows.SetConsoleMode(windows.Handle(output.Fd()), saved) })

	if err := windows.SetConsoleMode(windows.Handle(output.Fd()), saved&^windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING); err != nil {
		t.Fatal(err)
	}
	if err := enableEscapeSequences(output); err != nil {
		t.Fatal(err)
	}
	if mode := consoleMode(t, output); mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING == 0 {
		t.Errorf("mode %#x does not process escape sequences", mode)
	}
}
