package main

import (
	"io"
	"os"
	"strconv"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// openPTY opens a pseudo terminal. What the program under test writes to the
// slave side can be read from the master side, and so can the echo of what is
// typed, if the slave side echoes it.
func openPTY(t *testing.T) (master *os.File, slave *os.File, output *syncBuffer) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo terminal: %v", err)
	}
	t.Cleanup(func() { master.Close() })
	fd := int(master.Fd())
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err = os.OpenFile("/dev/pts/"+strconv.Itoa(number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { slave.Close() })
	output = &syncBuffer{}
	go io.Copy(output, master)
	return master, slave, output
}

func echoes(t *testing.T, f *os.File) bool {
	t.Helper()
	termios, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	return termios.Lflag&unix.ECHO != 0
}
