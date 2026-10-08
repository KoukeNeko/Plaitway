package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// stderrIsTerminal reports whether stderr is a console. A service or a hidden
// process has no console and often no valid handle at all, which also fails
// GetConsoleMode, so it counts as no terminal.
func stderrIsTerminal() bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(os.Stderr.Fd()), &mode) == nil
}

// redirectStderr makes the file behind f the process's stderr. The Go runtime
// writes a panic trace to whatever GetStdHandle returns at that moment, so the
// standard handle is replaced as well as os.Stderr. They get a handle of their
// own, like dup2 gives on Unix: a panic runs the deferred closing of the log
// before the trace is printed, and that must not take stderr with it.
func redirectStderr(f *os.File) error {
	process := windows.CurrentProcess()
	var stderr windows.Handle
	if err := windows.DuplicateHandle(process, windows.Handle(f.Fd()), process, &stderr, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return err
	}
	if err := windows.SetStdHandle(windows.STD_ERROR_HANDLE, stderr); err != nil {
		windows.CloseHandle(stderr)
		return err
	}
	os.Stderr = os.NewFile(uintptr(stderr), "stderr")
	return nil
}
