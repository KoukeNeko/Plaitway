package main

import (
	"os"

	"golang.org/x/sys/unix"
)

func isTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), unix.TIOCGETA)
	return err == nil
}

// disableEcho stops f from echoing what is typed and returns what puts the
// terminal back as it was. Input typed ahead is discarded.
func disableEcho(f *os.File) (restore func(), err error) {
	fd := int(f.Fd())
	saved, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return nil, err
	}
	silent := *saved
	silent.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, unix.TIOCSETAF, &silent); err != nil {
		return nil, err
	}
	return func() {
		// Nobody is left to tell when the terminal went away.
		_ = unix.IoctlSetTermios(fd, unix.TIOCSETA, saved)
	}, nil
}
