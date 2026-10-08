package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// isTerminal reports whether f is a console. A pipe, a file, NUL and a handle
// that is not valid all fail GetConsoleMode, so they count as no terminal.
func isTerminal(f *os.File) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(f.Fd()), &mode) == nil
}

// disableEcho stops f from echoing what is typed and returns what puts the
// console back as it was. Input typed ahead is discarded.
func disableEcho(f *os.File) (restore func(), err error) {
	handle := windows.Handle(f.Fd())
	var saved uint32
	if err := windows.GetConsoleMode(handle, &saved); err != nil {
		return nil, err
	}
	if err := windows.SetConsoleMode(handle, saved&^windows.ENABLE_ECHO_INPUT); err != nil {
		return nil, err
	}
	restore = func() {
		// Nobody is left to tell when the console went away.
		_ = windows.SetConsoleMode(handle, saved)
	}
	if err := windows.FlushConsoleInputBuffer(handle); err != nil {
		restore()
		return nil, err
	}
	return restore, nil
}

// A console shows color escape sequences as text until it is asked to act on
// them, and this is the only output of the program that has any.
func init() {
	if !isTerminal(os.Stdout) {
		return
	}
	if err := enableEscapeSequences(os.Stdout); err != nil {
		// NO_COLOR is what main reads to leave the sequences out; the name is a
		// constant, so setting it cannot fail.
		_ = os.Setenv("NO_COLOR", "1")
	}
}

func enableEscapeSequences(console *os.File) error {
	handle := windows.Handle(console.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return err
	}
	return windows.SetConsoleMode(handle, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
}
