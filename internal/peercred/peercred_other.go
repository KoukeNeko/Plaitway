//go:build !darwin && !linux && !windows

package peercred

import "net"

func lookup(net.Conn) (Info, error) { return Info{}, ErrUnsupported }
