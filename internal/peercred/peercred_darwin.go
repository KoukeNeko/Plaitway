package peercred

import (
	"fmt"
	"net"
	"slices"

	"golang.org/x/sys/unix"
)

// lookup uses LOCAL_PEERCRED (uid, groups) and LOCAL_PEERPID on the accepted
// socket. These describe the process that called connect(2).
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
		x, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err != nil {
			sockErr = fmt.Errorf("LOCAL_PEERCRED: %w", err)
			return
		}
		info.UID = x.Uid
		// cr_groups[0] is the effective gid.
		info.Groups = slices.Clone(x.Groups[:min(int(x.Ngroups), len(x.Groups))])
		if len(info.Groups) > 0 {
			info.GID = info.Groups[0]
		}
		pid, err := unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
		if err != nil {
			sockErr = fmt.Errorf("LOCAL_PEERPID: %w", err)
			return
		}
		info.PID = int32(pid)
	}); err != nil {
		return Info{}, err
	}
	return info, sockErr
}
