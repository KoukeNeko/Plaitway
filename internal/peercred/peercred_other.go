//go:build !darwin && !linux

package peercred

import "net"

// Windows named pipes expose the client through GetNamedPipeClientProcessId and
// ImpersonateNamedPipeClient instead; not implemented yet.
func lookup(net.Conn) (Info, error) { return Info{}, ErrUnsupported }
