//go:build unix

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

func stderrIsTerminal() bool {
	_, err := unix.IoctlGetTermios(int(os.Stderr.Fd()), getTermios)
	return err == nil
}

// redirectStderr makes f the process's stderr, so that a Go runtime panic
// lands in the log file too.
func redirectStderr(f *os.File) error {
	return unix.Dup2(int(f.Fd()), int(os.Stderr.Fd()))
}
