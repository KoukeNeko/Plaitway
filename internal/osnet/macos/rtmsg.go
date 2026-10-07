// Package macos implements internal/osnet on macOS: the routing table over a
// PF_ROUTE socket, the network monitor, and DNS through the scutil dynamic
// store. Files ending in _darwin.go hold the system calls; everything else is
// pure logic that builds and parses bytes or decides things, so it can be
// tested without touching the host.
package macos

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"net/netip"
)

// The routing socket wire format of Darwin (net/route.h, net/if.h,
// net/if_dl.h). These are ABI values; route_darwin_test.go compares them with
// golang.org/x/sys/unix.
const (
	rtmVersion = 5

	rtmAdd     = 0x1
	rtmDelete  = 0x2
	rtmChange  = 0x3
	rtmGet     = 0x4
	rtmMiss    = 0x7
	rtmNewAddr = 0xc
	rtmDelAddr = 0xd
	rtmIfInfo  = 0xe

	rtfUp        = 0x1
	rtfGateway   = 0x2
	rtfHost      = 0x4
	rtfLLInfo    = 0x400
	rtfStatic    = 0x800
	rtfBlackhole = 0x1000
	rtfWasCloned = 0x20000
	rtfBroadcast = 0x400000
	rtfMulticast = 0x800000
	rtfIfScope   = 0x1000000

	rtaDst     = 0x1
	rtaGateway = 0x2
	rtaNetmask = 0x4
	rtaIFA     = 0x20

	rtaxDst     = 0
	rtaxGateway = 1
	rtaxNetmask = 2
	rtaxIFA     = 5
	rtaxMax     = 8

	afInet  = 2
	afLink  = 18
	afInet6 = 30

	sockaddrInLen   = 16
	sockaddrIn6Len  = 28
	sockaddrDLLen   = 20
	rtMsghdrLen     = 92
	ifaMsghdrLen    = 20
	ifMsghdrLen     = 112
	sockaddrInAddr  = 4 // offset of sin_addr in sockaddr_in
	sockaddrIn6Addr = 8 // offset of sin6_addr in sockaddr_in6
)

// messageLayout says where a message type keeps the header fields we read.
type messageLayout struct {
	headerLen, addrsOff, flagsOff, indexOff int
	// partialAddrs: the message may end before every announced address. macOS
	// announces the broadcast slot in the address messages of IPv6 addresses
	// and sends nothing for it.
	partialAddrs bool
}

var (
	rtLayout  = messageLayout{headerLen: rtMsghdrLen, addrsOff: 12, flagsOff: 8, indexOff: 4}
	ifaLayout = messageLayout{headerLen: ifaMsghdrLen, addrsOff: 4, flagsOff: 8, indexOff: 12, partialAddrs: true}
	ifmLayout = messageLayout{headerLen: ifMsghdrLen, addrsOff: 4, flagsOff: 8, indexOff: 12}

	layouts = map[int]messageLayout{
		rtmAdd: rtLayout, rtmDelete: rtLayout, rtmChange: rtLayout, rtmGet: rtLayout, rtmMiss: rtLayout,
		rtmNewAddr: ifaLayout, rtmDelAddr: ifaLayout,
		rtmIfInfo: ifmLayout,
	}
)

// rtMessage is one routing message as the kernel sends it, with the socket
// addresses cut out but not decoded.
type rtMessage struct {
	Type int
	// Flags are the RTF_* flags of a route message and the IFF_* flags of an
	// interface message.
	Flags uint32
	// Index is the interface the message is about.
	Index    int
	AddrMask uint32
	// Addrs[i] is the raw sockaddr in slot RTAX_*, valid when AddrMask has the bit.
	Addrs [rtaxMax][]byte
}

func (m rtMessage) has(rta uint32) bool { return m.AddrMask&rta != 0 }

// parseMessages parses a buffer of routing messages: a sysctl dump, or one
// read from a routing socket. Messages of other versions and types are
// skipped, because the kernel adds types over time. It never panics: the data
// comes from the kernel but the daemon is root and long-lived, and a parser
// bug must not take it down.
func parseMessages(b []byte) (msgs []rtMessage, err error) {
	defer func() {
		if r := recover(); r != nil {
			msgs, err = nil, fmt.Errorf("parse routing messages: %v", r)
		}
	}()
	return parseMessagesUnguarded(b)
}

func parseMessagesUnguarded(b []byte) ([]rtMessage, error) {
	var msgs []rtMessage
	for len(b) > 0 {
		if len(b) < 4 {
			return nil, fmt.Errorf("%d stray bytes after the last routing message", len(b))
		}
		length := int(binary.NativeEndian.Uint16(b))
		if length < 4 || length > len(b) {
			return nil, fmt.Errorf("routing message length %d with %d bytes left", length, len(b))
		}
		raw := b[:length]
		b = b[length:]
		if raw[2] != rtmVersion {
			continue
		}
		layout, known := layouts[int(raw[3])]
		if !known {
			continue
		}
		if length < layout.headerLen {
			return nil, fmt.Errorf("routing message type %d is %d bytes, header needs %d", raw[3], length, layout.headerLen)
		}
		m := rtMessage{
			Type:     int(raw[3]),
			Flags:    binary.NativeEndian.Uint32(raw[layout.flagsOff:]),
			Index:    int(binary.NativeEndian.Uint16(raw[layout.indexOff:])),
			AddrMask: binary.NativeEndian.Uint32(raw[layout.addrsOff:]),
		}
		if err := m.splitAddrs(raw[layout.headerLen:], layout.partialAddrs); err != nil {
			return nil, fmt.Errorf("routing message type %d: %w", m.Type, err)
		}
		msgs = append(msgs, m)
	}
	return msgs, nil
}

// splitAddrs cuts the socket addresses named by AddrMask out of b. Each is
// padded to a multiple of 4 bytes, and a zero-length one still takes 4. With
// partial, addresses missing at the end are left empty instead of being an error.
func (m *rtMessage) splitAddrs(b []byte, partial bool) error {
	for i := 0; i < rtaxMax; i++ {
		if m.AddrMask&(1<<i) == 0 {
			continue
		}
		if len(b) == 0 {
			if partial {
				return nil
			}
			return errors.New("fewer socket addresses than the address mask announces")
		}
		length := int(b[0])
		if length > len(b) {
			return fmt.Errorf("socket address of %d bytes with %d left", length, len(b))
		}
		m.Addrs[i] = b[:length]
		b = b[min(sockaddrSpace(length), len(b)):]
	}
	return nil
}

func sockaddrSpace(length int) int {
	if length == 0 {
		return 4
	}
	return (length + 3) &^ 3
}

// span returns sa[from:to] cut to what the sockaddr has. Kernel netmasks are
// shortened to their last non-zero byte.
func span(sa []byte, from, to int) []byte {
	from = min(from, len(sa))
	to = min(to, len(sa))
	return sa[from:to]
}

// ipFromSockaddr decodes an AF_INET or AF_INET6 socket address. For IPv6
// link-local addresses the kernel embeds the interface index in bytes 2 and 3
// (KAME form); it is returned as scope and cleared from the address.
func ipFromSockaddr(sa []byte) (addr netip.Addr, scope int, ok bool) {
	if len(sa) < 2 {
		return netip.Addr{}, 0, false
	}
	switch sa[1] {
	case afInet:
		var ip [4]byte
		copy(ip[:], span(sa, sockaddrInAddr, sockaddrInAddr+4))
		return netip.AddrFrom4(ip), 0, true
	case afInet6:
		var ip [16]byte
		copy(ip[:], span(sa, sockaddrIn6Addr, sockaddrIn6Addr+16))
		if hasEmbeddedScope(ip) {
			scope = int(binary.BigEndian.Uint16(ip[2:4]))
			ip[2], ip[3] = 0, 0
			if scope == 0 && len(sa) >= sockaddrIn6Len {
				scope = int(binary.NativeEndian.Uint32(sa[24:28]))
			}
		}
		return netip.AddrFrom16(ip), scope, true
	}
	return netip.Addr{}, 0, false
}

// hasEmbeddedScope reports whether the kernel keeps a zone in ip: link-local
// unicast, and interface-local and link-local multicast.
func hasEmbeddedScope(ip [16]byte) bool {
	if ip[0] == 0xfe && ip[1]&0xc0 == 0x80 {
		return true
	}
	return ip[0] == 0xff && (ip[1]&0x0f == 1 || ip[1]&0x0f == 2)
}

// linkIndexFromSockaddr decodes the interface index of an AF_LINK address.
func linkIndexFromSockaddr(sa []byte) (int, bool) {
	if len(sa) < 4 || sa[1] != afLink {
		return 0, false
	}
	return int(binary.NativeEndian.Uint16(sa[2:4])), true
}

// prefixLenFromNetmask decodes a kernel netmask for the given address family
// (afInet or afInet6). The kernel stores the mask as a sockaddr cut after its
// last non-zero byte and fills the family and port bytes with 0xff, so only
// the address bytes count. ok is false for a mask that is not a prefix.
func prefixLenFromNetmask(family byte, sa []byte) (n int, ok bool) {
	off, size := sockaddrInAddr, 4
	if family == afInet6 {
		off, size = sockaddrIn6Addr, 16
	}
	var mask [16]byte
	copy(mask[:size], span(sa, off, off+size))
	i := 0
	for i < size && mask[i] == 0xff {
		n += 8
		i++
	}
	if i < size && mask[i] != 0 {
		ones := bits.OnesCount8(mask[i])
		if mask[i] != byte(0xff<<(8-ones)) {
			return 0, false
		}
		n += ones
		i++
	}
	for ; i < size; i++ {
		if mask[i] != 0 {
			return 0, false
		}
	}
	return n, true
}

// appendSockaddrIP appends a sockaddr_in or sockaddr_in6. Link-local IPv6
// addresses carry scope (an interface index) in the KAME embedded form, as the
// kernel itself reports them and as route(8) writes them.
func appendSockaddrIP(b []byte, addr netip.Addr, scope int) []byte {
	if addr.Is4() {
		sa := make([]byte, sockaddrInLen)
		sa[0], sa[1] = sockaddrInLen, afInet
		ip := addr.As4()
		copy(sa[sockaddrInAddr:], ip[:])
		return append(b, sa...)
	}
	sa := make([]byte, sockaddrIn6Len)
	sa[0], sa[1] = sockaddrIn6Len, afInet6
	ip := addr.As16()
	if scope != 0 && hasEmbeddedScope(ip) {
		binary.BigEndian.PutUint16(ip[2:4], uint16(scope))
	}
	copy(sa[sockaddrIn6Addr:], ip[:])
	return append(b, sa...)
}

// appendSockaddrLink appends a sockaddr_dl that names an interface by index
// only, the form of "route add -interface" and of the kernel's "link#N".
func appendSockaddrLink(b []byte, index int) []byte {
	sa := make([]byte, sockaddrDLLen)
	sa[0], sa[1] = sockaddrDLLen, afLink
	binary.NativeEndian.PutUint16(sa[2:4], uint16(index))
	return append(b, sa...)
}

// appendNetmask appends the mask of a prefix of n bits in the kernel's own
// form: cut after the last non-zero byte, family and port bytes 0xff, padded
// to 4 bytes. A zero-length mask (the default route) is 4 zero bytes.
func appendNetmask(b []byte, v6 bool, n int) []byte {
	off, size := sockaddrInAddr, 4
	if v6 {
		off, size = sockaddrIn6Addr, 16
	}
	var mask [16]byte
	for i := range size {
		switch left := n - 8*i; {
		case left >= 8:
			mask[i] = 0xff
		case left > 0:
			mask[i] = byte(0xff << (8 - left))
		}
	}
	used := size
	for used > 0 && mask[used-1] == 0 {
		used--
	}
	if used == 0 {
		return append(b, 0, 0, 0, 0)
	}
	sa := make([]byte, sockaddrSpace(off+used))
	sa[0] = byte(off + used)
	for i := 1; i < off; i++ {
		sa[i] = 0xff
	}
	copy(sa[off:], mask[:used])
	return append(b, sa...)
}

// routeRequest is a route add or delete in wire terms.
type routeRequest struct {
	Type  int
	Flags uint32
	// Index is rtm_index: the interface a scoped route belongs to.
	Index int
	Seq   int32
	PID   int32
	Dst   netip.Prefix
	// DstScope is the interface a link-local or interface-local destination
	// belongs to.
	DstScope int
	// Gateway is the next hop when valid; with GatewayScope for link-local
	// IPv6. Otherwise LinkIndex names the interface the route is bound to.
	Gateway      netip.Addr
	GatewayScope int
	LinkIndex    int
	// Netmask is false for host routes, which carry no mask.
	Netmask bool
}

func (r routeRequest) marshal() []byte {
	b := make([]byte, rtMsghdrLen)
	b[2], b[3] = rtmVersion, byte(r.Type)
	binary.NativeEndian.PutUint16(b[4:], uint16(r.Index))
	binary.NativeEndian.PutUint32(b[8:], r.Flags)
	addrs := uint32(rtaDst)
	binary.NativeEndian.PutUint32(b[16:], uint32(r.PID))
	binary.NativeEndian.PutUint32(b[20:], uint32(r.Seq))

	b = appendSockaddrIP(b, r.Dst.Addr(), r.DstScope)
	if r.Gateway.IsValid() {
		addrs |= rtaGateway
		b = appendSockaddrIP(b, r.Gateway, r.GatewayScope)
	} else if r.LinkIndex != 0 {
		addrs |= rtaGateway
		b = appendSockaddrLink(b, r.LinkIndex)
	}
	if r.Netmask {
		addrs |= rtaNetmask
		b = appendNetmask(b, r.Dst.Addr().Is6(), r.Dst.Bits())
	}
	binary.NativeEndian.PutUint32(b[12:], addrs)
	binary.NativeEndian.PutUint16(b[0:], uint16(len(b)))
	return b
}
