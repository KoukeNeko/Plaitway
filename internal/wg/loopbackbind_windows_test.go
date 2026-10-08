package wg

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"sync"
	"testing"

	"golang.zx2c4.com/wireguard/conn"
)

// The suite runs real wireguard-go devices. Their default sockets listen on
// every address, and Windows Defender Firewall asks the person at the keyboard
// whether to allow each new test binary (a different temporary path every
// build), which in turn leaves an allow rule behind for a file that is deleted
// a minute later. A test never needs more than loopback.
func TestMain(m *testing.M) {
	newBind = newLoopbackBind
	os.Exit(m.Run())
}

// loopbackAddrs are the addresses the test bind listens on. Windows answers on
// all of 127.0.0.0/8; the second address lets a test move a peer to "another
// address of this host" without leaving loopback.
var loopbackAddrs = []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("127.0.0.2")}

// loopbackBind is a wireguard-go Bind that listens on loopbackAddrs only, one
// socket each and all on one port. A reply leaves from the address the request
// arrived on, as it would from a socket listening on every address.
type loopbackBind struct {
	mu      sync.Mutex
	sockets map[netip.Addr]*net.UDPConn
}

func newLoopbackBind() conn.Bind { return &loopbackBind{} }

type loopbackEndpoint struct {
	dst netip.AddrPort
	// local is the address of the socket this endpoint was heard on or is to be
	// sent from; the zero Addr means the first of loopbackAddrs.
	local netip.Addr
}

var _ conn.Endpoint = (*loopbackEndpoint)(nil)

func (e *loopbackEndpoint) ClearSrc()           { e.local = netip.Addr{} }
func (e *loopbackEndpoint) SrcIP() netip.Addr   { return e.local }
func (e *loopbackEndpoint) DstIP() netip.Addr   { return e.dst.Addr() }
func (e *loopbackEndpoint) DstToString() string { return e.dst.String() }
func (e *loopbackEndpoint) SrcToString() string {
	if !e.local.IsValid() {
		return ""
	}
	return e.local.String()
}
func (e *loopbackEndpoint) DstToBytes() []byte {
	encoded, _ := e.dst.MarshalBinary()
	return encoded
}

func (b *loopbackBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sockets != nil {
		return nil, 0, conn.ErrBindAlreadyOpen
	}
	sockets := make(map[netip.Addr]*net.UDPConn, len(loopbackAddrs))
	closeAll := func() {
		for _, socket := range sockets {
			socket.Close()
		}
	}
	for _, addr := range loopbackAddrs {
		socket, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.AddrPortFrom(addr, port)))
		if err != nil {
			closeAll()
			return nil, 0, err
		}
		sockets[addr] = socket
		// With port 0 the first socket picks the port and the others follow it.
		port = uint16(socket.LocalAddr().(*net.UDPAddr).Port)
	}
	b.sockets = sockets
	receive := make([]conn.ReceiveFunc, 0, len(loopbackAddrs))
	for _, addr := range loopbackAddrs {
		receive = append(receive, receiveOn(addr, sockets[addr]))
	}
	return receive, port, nil
}

func receiveOn(local netip.Addr, socket *net.UDPConn) conn.ReceiveFunc {
	return func(packets [][]byte, sizes []int, endpoints []conn.Endpoint) (int, error) {
		size, from, err := socket.ReadFromUDPAddrPort(packets[0])
		if err != nil {
			return 0, err
		}
		sizes[0] = size
		endpoints[0] = &loopbackEndpoint{dst: netip.AddrPortFrom(from.Addr().Unmap(), from.Port()), local: local}
		return 1, nil
	}
}

func (b *loopbackBind) Close() error {
	b.mu.Lock()
	sockets := b.sockets
	b.sockets = nil
	b.mu.Unlock()
	var err error
	for _, socket := range sockets {
		err = errors.Join(err, socket.Close())
	}
	return err
}

func (b *loopbackBind) Send(buffers [][]byte, endpoint conn.Endpoint) error {
	target, ok := endpoint.(*loopbackEndpoint)
	if !ok {
		return conn.ErrWrongEndpointType
	}
	b.mu.Lock()
	socket := b.sockets[b.sourceFor(target)]
	b.mu.Unlock()
	if socket == nil {
		return net.ErrClosed
	}
	for _, buffer := range buffers {
		if _, err := socket.WriteToUDPAddrPort(buffer, target.dst); err != nil {
			return err
		}
	}
	return nil
}

// sourceFor is the local address a packet to the endpoint leaves from: the one
// it was heard on, else the destination's own address when that is one of ours
// (what the system picks for a socket on every address), else the first.
func (b *loopbackBind) sourceFor(target *loopbackEndpoint) netip.Addr {
	if target.local.IsValid() {
		return target.local
	}
	if _, listening := b.sockets[target.dst.Addr()]; listening {
		return target.dst.Addr()
	}
	return loopbackAddrs[0]
}

func (b *loopbackBind) ParseEndpoint(s string) (conn.Endpoint, error) {
	parsed, err := netip.ParseAddrPort(s)
	if err != nil {
		return nil, err
	}
	return &loopbackEndpoint{dst: parsed}, nil
}

func (b *loopbackBind) SetMark(uint32) error { return nil }

func (b *loopbackBind) BatchSize() int { return 1 }
