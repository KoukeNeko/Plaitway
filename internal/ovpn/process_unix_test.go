//go:build unix

package ovpn

import (
	"errors"
	"syscall"
)

// processExists reports whether pid is still a process. Signal 0 only checks
// that the process can be signalled.
func processExists(pid int) bool {
	return !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}
