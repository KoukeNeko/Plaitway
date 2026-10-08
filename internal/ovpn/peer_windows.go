package ovpn

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// tcpTableOwnerPidAll is TCP_TABLE_OWNER_PID_ALL: every connection and
	// listener, with the process that owns it.
	tcpTableOwnerPidAll = 5

	// The owner of the other end is looked up again for as long as the table
	// does not list the connection yet.
	peerLookupAttempts = 10
	peerLookupInterval = 50 * time.Millisecond
)

var procGetExtendedTCPTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

// tcpRowOwnerPid is MIB_TCPROW_OWNER_PID. The ports are in network byte order
// in the low 16 bits.
type tcpRowOwnerPid struct {
	State      uint32
	LocalAddr  uint32
	LocalPort  uint32
	RemoteAddr uint32
	RemotePort uint32
	OwningPid  uint32
}

var errConnectionNotListed = errors.New("the system does not list the connection")

// peerIsProcess confirms that the process at the other end of a loopback
// connection is pid. The password of the management interface proves nothing
// about who receives it; this does, because the system records which process
// owns every end of a connection, and a process that squatted on the port
// before openvpn did owns the end that answered.
func peerIsProcess(conn net.Conn, pid int) error {
	local, remote, err := loopbackEnds(conn)
	if err != nil {
		return err
	}
	var owner uint32
	for attempt := 0; attempt < peerLookupAttempts; attempt++ {
		owner, err = ownerOfServerEnd(local, remote)
		if !errors.Is(err, errConnectionNotListed) {
			break
		}
		time.Sleep(peerLookupInterval)
	}
	if err != nil {
		return err
	}
	if owner != uint32(pid) {
		return fmt.Errorf("port %d is owned by process %d, not by openvpn (process %d)", remote.Port(), owner, pid)
	}
	return nil
}

func loopbackEnds(conn net.Conn) (local, remote netip.AddrPort, err error) {
	local, localOK := addrPortOf(conn.LocalAddr())
	remote, remoteOK := addrPortOf(conn.RemoteAddr())
	if !localOK || !remoteOK || !local.Addr().Is4() || !remote.Addr().IsLoopback() || !local.Addr().IsLoopback() {
		return netip.AddrPort{}, netip.AddrPort{}, fmt.Errorf("%s to %s is not an IPv4 loopback connection", conn.LocalAddr(), conn.RemoteAddr())
	}
	return local, remote, nil
}

func addrPortOf(addr net.Addr) (netip.AddrPort, bool) {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return netip.AddrPort{}, false
	}
	return tcp.AddrPort(), true
}

// ownerOfServerEnd is the process that owns the end of the connection that the
// client at local reached on remote: the row of the table that has the two
// the other way round.
func ownerOfServerEnd(local, remote netip.AddrPort) (uint32, error) {
	rows, err := tcpTable()
	if err != nil {
		return 0, err
	}
	for _, row := range rows {
		if row.LocalAddr == ipv4Word(remote.Addr()) && portOf(row.LocalPort) == remote.Port() &&
			row.RemoteAddr == ipv4Word(local.Addr()) && portOf(row.RemotePort) == local.Port() {
			return row.OwningPid, nil
		}
	}
	return 0, errConnectionNotListed
}

// ipv4Word is the address as the table holds it: the bytes in memory order.
func ipv4Word(addr netip.Addr) uint32 {
	b := addr.As4()
	return *(*uint32)(unsafe.Pointer(&b[0]))
}

// portOf reads a port from the low 16 bits of a table field, which are in
// network byte order.
func portOf(field uint32) uint16 {
	port := uint16(field)
	return port>>8 | port<<8
}

// tcpTable lists the IPv4 connections of the machine with their owners.
func tcpTable() ([]tcpRowOwnerPid, error) {
	const (
		afInet     = 2
		maxRetries = 5
	)
	var size uint32
	for retry := 0; retry < maxRetries; retry++ {
		buffer := make([]uint32, (size+3)/4+1)
		var first *byte
		if size > 0 {
			first = (*byte)(unsafe.Pointer(&buffer[0]))
		}
		status, _, _ := procGetExtendedTCPTable.Call(uintptr(unsafe.Pointer(first)), uintptr(unsafe.Pointer(&size)), 0, afInet, tcpTableOwnerPidAll, 0)
		switch windows.Errno(status) {
		case windows.ERROR_INSUFFICIENT_BUFFER:
			continue // size now says how much is needed; the table may have grown again
		case 0:
			return rowsOf(buffer), nil
		}
		return nil, fmt.Errorf("GetExtendedTcpTable: %w", windows.Errno(status))
	}
	return nil, errors.New("GetExtendedTcpTable: the table kept growing")
}

// rowsOf reads MIB_TCPTABLE_OWNER_PID: a count, then that many rows.
func rowsOf(buffer []uint32) []tcpRowOwnerPid {
	count := buffer[0]
	rows := unsafe.Slice((*tcpRowOwnerPid)(unsafe.Pointer(&buffer[1])), count)
	return append([]tcpRowOwnerPid(nil), rows...)
}
