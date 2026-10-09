package linux

import (
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// A request is answered at once; a dump of a big table takes longer.
	requestTimeout = 10 * time.Second
	// recvBufferSize holds any datagram the kernel sends for these requests.
	recvBufferSize = 1 << 16
)

// netlinkConn is a NETLINK_ROUTE socket. As a *os.File it is polled by the
// runtime, so closing it ends a blocked read.
type netlinkConn struct {
	f   *os.File
	raw syscall.RawConn
	seq atomic.Uint32
	buf []byte
}

// openNetlink opens a NETLINK_ROUTE socket in the namespace of this process,
// subscribed to the multicast groups (RTMGRP_* bits).
func openNetlink(groups uint32) (*netlinkConn, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, unix.NETLINK_ROUTE)
	if err != nil {
		return nil, fmt.Errorf("open netlink socket: %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: groups}); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("bind netlink socket: %w", err)
	}
	// Errors come with the kernel's explanation of them. Kernels before 4.12
	// do not know the option and give the errno alone.
	if err := unix.SetsockoptInt(fd, unix.SOL_NETLINK, unix.NETLINK_EXT_ACK, 1); err != nil && !errors.Is(err, unix.ENOPROTOOPT) {
		unix.Close(fd)
		return nil, fmt.Errorf("configure netlink socket: %w", err)
	}
	f := os.NewFile(uintptr(fd), "netlink")
	raw, err := f.SyscallConn()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("open netlink socket: %w", err)
	}
	return &netlinkConn{f: f, raw: raw, buf: make([]byte, recvBufferSize)}, nil
}

func (c *netlinkConn) close() error { return c.f.Close() }

// recv reads one datagram from the kernel. The slice it returns is valid until
// the next call.
func (c *netlinkConn) recv() ([]byte, error) {
	for {
		var (
			n, flags int
			from     unix.Sockaddr
			recvErr  error
		)
		err := c.raw.Read(func(fd uintptr) bool {
			for {
				n, _, flags, from, recvErr = unix.Recvmsg(int(fd), c.buf, nil, 0)
				if recvErr != unix.EINTR {
					break
				}
			}
			return recvErr != unix.EAGAIN
		})
		if err != nil {
			return nil, err
		}
		if recvErr != nil {
			return nil, fmt.Errorf("read netlink socket: %w", recvErr)
		}
		if flags&unix.MSG_TRUNC != 0 {
			return nil, fmt.Errorf("read netlink socket: message of %d bytes does not fit the buffer", n)
		}
		// Only the kernel (port 0) tells us about the network.
		if nl, ok := from.(*unix.SockaddrNetlink); ok && nl.Pid != 0 {
			continue
		}
		return c.buf[:n], nil
	}
}

// request sends one message built for the next sequence number and waits for
// the kernel's answer: nil for an acknowledgement, a *netlinkError for a refusal.
func (c *netlinkConn) request(build func(seq uint32) []byte) error {
	if err := c.f.SetDeadline(time.Now().Add(requestTimeout)); err != nil {
		return err
	}
	seq := c.seq.Add(1)
	if _, err := c.f.Write(build(seq)); err != nil {
		return fmt.Errorf("write netlink socket: %w", err)
	}
	return readAck(seq, c.recv)
}

// dump asks for every link, address or route and returns the messages that
// describe them.
func (c *netlinkConn) dump(typ uint16) ([]rtMessage, error) {
	return dumpConsistently(typ, func() ([]rtMessage, bool, error) {
		if err := c.f.SetDeadline(time.Now().Add(requestTimeout)); err != nil {
			return nil, false, err
		}
		seq := c.seq.Add(1)
		if _, err := c.f.Write(dumpRequest(typ, seq)); err != nil {
			return nil, false, fmt.Errorf("write netlink socket: %w", err)
		}
		return readDump(seq, typ, c.recv)
	})
}

// links reads the interfaces, with their addresses when withAddrs is set.
func (c *netlinkConn) links(withAddrs bool) ([]link, error) {
	msgs, err := c.dump(rtmGetLink)
	if err != nil {
		return nil, fmt.Errorf("read interfaces: %w", err)
	}
	if withAddrs {
		addrs, err := c.dump(rtmGetAddr)
		if err != nil {
			return nil, fmt.Errorf("read addresses: %w", err)
		}
		msgs = append(msgs, addrs...)
	}
	return buildLinks(msgs), nil
}

// interfaceIndex resolves an interface name.
func (c *netlinkConn) interfaceIndex(name string) (uint32, error) {
	links, err := c.links(false)
	if err != nil {
		return 0, err
	}
	for _, l := range links {
		if l.Name == name {
			return uint32(l.Index), nil
		}
	}
	return 0, fmt.Errorf("interface %s: %w", name, errNoSuchInterface)
}
