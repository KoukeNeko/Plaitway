package peercred

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// lookup uses SO_PEERCRED, which the kernel fills in at connect(2) time.
func lookup(conn net.Conn) (Info, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return Info{}, fmt.Errorf("not a unix connection: %T", conn)
	}
	rc, err := uc.SyscallConn()
	if err != nil {
		return Info{}, err
	}
	var info Info
	var sockErr error
	if err := rc.Control(func(fd uintptr) {
		u, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err != nil {
			sockErr = fmt.Errorf("SO_PEERCRED: %w", err)
			return
		}
		info = Info{UID: u.Uid, GID: u.Gid, Groups: []uint32{u.Gid}, PID: u.Pid}
	}); err != nil {
		return Info{}, err
	}
	return info, sockErr
}
