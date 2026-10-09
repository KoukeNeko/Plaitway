// Package linux implements internal/osnet on Linux: the routing table and the
// interface list over a NETLINK_ROUTE socket, and the network monitor. Files
// ending in _linux.go hold the system calls; everything else is pure logic that
// builds and parses bytes or decides things, so it can be tested without
// touching the host.
package linux

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net/netip"
)

// The rtnetlink wire format (linux/netlink.h, linux/rtnetlink.h,
// linux/if_link.h, linux/if_addr.h, linux/if_arp.h, linux/if.h). These are
// kernel ABI values; abi_linux_test.go compares them with golang.org/x/sys/unix.
const (
	nlmsgHdrLen = 16
	nlmsgError  = 2
	nlmsgDone   = 3

	// Flags of a request.
	nlmFRequest = 0x1
	nlmFAck     = 0x4
	nlmFDump    = 0x300 // NLM_F_ROOT | NLM_F_MATCH
	nlmFExcl    = 0x200
	nlmFCreate  = 0x400
	// Flags of a reply.
	nlmFDumpIntr = 0x10  // the table changed while it was being dumped
	nlmFCapped   = 0x100 // an error reply echoes the header of the request only
	nlmFAckTLVs  = 0x200 // an error reply carries attributes (extended ack)

	nlmsgErrAttrMsg = 1 // NLMSGERR_ATTR_MSG: the kernel's explanation of an error

	rtmNewLink  = 16
	rtmDelLink  = 17
	rtmGetLink  = 18
	rtmNewAddr  = 20
	rtmDelAddr  = 21
	rtmGetAddr  = 22
	rtmNewRoute = 24
	rtmDelRoute = 25
	rtmGetRoute = 26

	// Multicast groups of the bitmask form (RTMGRP_*).
	rtmgrpLink      = 0x1
	rtmgrpIPv4Addr  = 0x10
	rtmgrpIPv4Route = 0x40
	rtmgrpIPv6Addr  = 0x100
	rtmgrpIPv6Route = 0x400

	afUnspec = 0
	afInet   = 2
	afInet6  = 10

	nlaTypeMask = 0x3fff // the top two bits of an attribute type are NLA_F_NESTED and NLA_F_NET_BYTEORDER

	rtMsgLen     = 12 // struct rtmsg
	ifInfoMsgLen = 16 // struct ifinfomsg
	ifAddrMsgLen = 8  // struct ifaddrmsg
	rtNextHopLen = 8  // struct rtnexthop

	// Route attributes (RTA_*).
	rtaDst       = 1
	rtaOif       = 4
	rtaGateway   = 5
	rtaPriority  = 6
	rtaMultipath = 9
	rtaTable     = 15
	rtaVia       = 18
	rtaNHID      = 30

	// Route types (RTN_*).
	rtnUnicast     = 1
	rtnBlackhole   = 6
	rtnUnreachable = 7
	rtnProhibit    = 8

	// Route protocols (RTPROT_*). Plaitway's own is RouteProtocol.
	rtprotRedirect = 1
	rtprotKernel   = 2
	rtprotRA       = 9

	// Route scopes (RT_SCOPE_*).
	rtScopeUniverse = 0
	rtScopeLink     = 253
	rtScopeNowhere  = 255

	rtTableMain = 254

	rtmFCloned = 0x200 // a route the kernel cloned from another: a cache entry

	// Link attributes (IFLA_*) and the nested IFLA_INFO_KIND.
	iflaIfName   = 3
	iflaLinkInfo = 18
	iflaInfoKind = 1
	iflaMTU      = 4

	// Interface flags (IFF_*).
	iffUp       = 0x1
	iffLoopback = 0x8
	iffLowerUp  = 0x10000

	// Address attributes (IFA_*).
	ifaAddress   = 1
	ifaLocal     = 2
	ifaBroadcast = 4

	// Hardware types (ARPHRD_*) that mark a link as a tunnel or as loopback.
	arphrdPPP      = 512
	arphrdTunnel   = 768
	arphrdTunnel6  = 769
	arphrdLoopback = 772
	arphrdSIT      = 776
	arphrdIPGRE    = 778
	arphrdNone     = 65534
)

func align4(n int) int { return (n + 3) &^ 3 }

// rtMessage is one rtnetlink message with the attributes we read decoded. The
// field that goes with Type is set; a message of a type we do not read is
// dropped by parseMessages, except the two that frame replies.
type rtMessage struct {
	Type  uint16
	Flags uint16
	Seq   uint32
	// Errno is the status of an NLMSG_ERROR (0 is an acknowledgement) or an
	// NLMSG_DONE, negative for a failure, as the kernel sends it.
	Errno int32
	// ExtAck is the kernel's explanation that comes with an NLMSG_ERROR, or "".
	ExtAck string

	Route *routeMsg
	Link  *linkMsg
	Addr  *addrMsg
}

// routeMsg is an RTM_NEWROUTE or RTM_DELROUTE message.
type routeMsg struct {
	Family   uint8
	DstLen   uint8
	Table    uint32 // rtm_table, or RTA_TABLE when the table does not fit a byte
	Protocol uint8
	Scope    uint8
	Type     uint8
	Flags    uint32
	// Dst is the zero Addr when the message has no RTA_DST, as for a default route.
	Dst      netip.Addr
	Gateway  netip.Addr
	OIf      uint32
	Priority uint32
	// Via: the next hop is given as RTA_VIA, an address of another family.
	Via bool
	// NHID: the route uses a nexthop object. Its next hops are in the message
	// only when the kernel is in nexthop compatibility mode.
	NHID     bool
	NextHops []nextHop
}

// nextHop is one element of RTA_MULTIPATH.
type nextHop struct {
	OIf     uint32
	Gateway netip.Addr
	Via     bool
}

// linkMsg is an RTM_NEWLINK or RTM_DELLINK message.
type linkMsg struct {
	Family uint8
	Type   uint16 // ARPHRD_*
	Index  uint32
	Flags  uint32
	Name   string
	Kind   string // IFLA_INFO_KIND: "dummy", "wireguard", "tun", ...; "" for a plain device
}

// addrMsg is an RTM_NEWADDR or RTM_DELADDR message.
type addrMsg struct {
	Family    uint8
	PrefixLen uint8
	Index     uint32
	Address   netip.Addr
	Local     netip.Addr
}

// parseMessages parses a buffer of netlink messages: a dump reply, or one
// datagram of a multicast group. Messages of types we do not read are skipped,
// because the kernel adds types over time. It never panics: the data comes from
// the kernel but the daemon is root and long-lived, and a parser bug must not
// take it down.
func parseMessages(b []byte) (msgs []rtMessage, err error) {
	defer func() {
		if r := recover(); r != nil {
			msgs, err = nil, fmt.Errorf("parse netlink messages: %v", r)
		}
	}()
	return parseMessagesUnguarded(b)
}

func parseMessagesUnguarded(b []byte) ([]rtMessage, error) {
	var msgs []rtMessage
	for len(b) > 0 {
		if len(b) < nlmsgHdrLen {
			return nil, fmt.Errorf("%d stray bytes after the last netlink message", len(b))
		}
		length := int(binary.NativeEndian.Uint32(b))
		if length < nlmsgHdrLen || length > len(b) {
			return nil, fmt.Errorf("netlink message length %d with %d bytes left", length, len(b))
		}
		payload := b[nlmsgHdrLen:length]
		m := rtMessage{
			Type:  binary.NativeEndian.Uint16(b[4:]),
			Flags: binary.NativeEndian.Uint16(b[6:]),
			Seq:   binary.NativeEndian.Uint32(b[8:]),
		}
		b = b[min(align4(length), len(b)):]

		var err error
		switch m.Type {
		case nlmsgError:
			err = m.decodeError(payload)
		case nlmsgDone:
			// The payload is the status of the dump.
			if len(payload) >= 4 {
				m.Errno = int32(binary.NativeEndian.Uint32(payload))
			}
		case rtmNewRoute, rtmDelRoute:
			m.Route, err = decodeRoute(payload)
		case rtmNewLink, rtmDelLink:
			m.Link, err = decodeLink(payload)
		case rtmNewAddr, rtmDelAddr:
			m.Addr, err = decodeAddr(payload)
		default:
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("netlink message type %d: %w", m.Type, err)
		}
		msgs = append(msgs, m)
	}
	return msgs, nil
}

// forEachAttr calls fn for every attribute in b: a 4-byte header of length and
// type, then the value, padded to 4 bytes. Nesting flags are cleared from the type.
func forEachAttr(b []byte, fn func(typ uint16, value []byte) error) error {
	for len(b) > 0 {
		if len(b) < 4 {
			return fmt.Errorf("%d stray bytes after the last attribute", len(b))
		}
		length := int(binary.NativeEndian.Uint16(b))
		if length < 4 || length > len(b) {
			return fmt.Errorf("attribute length %d with %d bytes left", length, len(b))
		}
		typ := binary.NativeEndian.Uint16(b[2:]) & nlaTypeMask
		if err := fn(typ, b[4:length]); err != nil {
			return err
		}
		b = b[min(align4(length), len(b)):]
	}
	return nil
}

// decodeError reads an NLMSG_ERROR payload: the status, the request it answers
// (in full, unless the kernel capped it to the header), and the attributes of an
// extended acknowledgement.
func (m *rtMessage) decodeError(p []byte) error {
	if len(p) < 4+nlmsgHdrLen {
		return fmt.Errorf("error message of %d bytes", len(p))
	}
	m.Errno = int32(binary.NativeEndian.Uint32(p))
	if m.Flags&nlmFAckTLVs == 0 {
		return nil
	}
	echoed := nlmsgHdrLen
	if m.Flags&nlmFCapped == 0 {
		echoed = align4(int(binary.NativeEndian.Uint32(p[4:])))
	}
	if echoed < nlmsgHdrLen || 4+echoed > len(p) {
		return nil
	}
	// The explanation is a courtesy: an attribute list that cannot be read costs
	// the text, not the status.
	_ = forEachAttr(p[4+echoed:], func(typ uint16, value []byte) error {
		if typ == nlmsgErrAttrMsg {
			m.ExtAck = string(bytes.TrimRight(value, "\x00"))
		}
		return nil
	})
	return nil
}

func decodeRoute(p []byte) (*routeMsg, error) {
	if len(p) < rtMsgLen {
		return nil, fmt.Errorf("route message of %d bytes, header needs %d", len(p), rtMsgLen)
	}
	r := &routeMsg{
		Family:   p[0],
		DstLen:   p[1],
		Table:    uint32(p[4]),
		Protocol: p[5],
		Scope:    p[6],
		Type:     p[7],
		Flags:    binary.NativeEndian.Uint32(p[8:]),
	}
	// The attributes of other families (MPLS labels, multicast routing) mean
	// something else.
	if r.Family != afInet && r.Family != afInet6 {
		return r, nil
	}
	err := forEachAttr(p[rtMsgLen:], func(typ uint16, value []byte) (err error) {
		switch typ {
		case rtaDst:
			r.Dst, err = addrFromBytes(value)
		case rtaGateway:
			r.Gateway, err = addrFromBytes(value)
		case rtaOif:
			r.OIf, err = uint32FromBytes(value)
		case rtaPriority:
			r.Priority, err = uint32FromBytes(value)
		case rtaTable:
			r.Table, err = uint32FromBytes(value)
		case rtaVia:
			r.Via = true
		case rtaNHID:
			r.NHID = true
		case rtaMultipath:
			r.NextHops, err = decodeNextHops(value)
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("route attributes: %w", err)
	}
	return r, nil
}

// decodeNextHops reads RTA_MULTIPATH: struct rtnexthop elements, each followed
// by the attributes of its next hop.
func decodeNextHops(b []byte) ([]nextHop, error) {
	var hops []nextHop
	for len(b) > 0 {
		if len(b) < rtNextHopLen {
			return nil, fmt.Errorf("%d stray bytes after the last next hop", len(b))
		}
		length := int(binary.NativeEndian.Uint16(b))
		if length < rtNextHopLen || length > len(b) {
			return nil, fmt.Errorf("next hop length %d with %d bytes left", length, len(b))
		}
		hop := nextHop{OIf: binary.NativeEndian.Uint32(b[4:])}
		err := forEachAttr(b[rtNextHopLen:length], func(typ uint16, value []byte) (err error) {
			switch typ {
			case rtaGateway:
				hop.Gateway, err = addrFromBytes(value)
			case rtaVia:
				hop.Via = true
			}
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("next hop attributes: %w", err)
		}
		hops = append(hops, hop)
		b = b[min(align4(length), len(b)):]
	}
	return hops, nil
}

func decodeLink(p []byte) (*linkMsg, error) {
	if len(p) < ifInfoMsgLen {
		return nil, fmt.Errorf("link message of %d bytes, header needs %d", len(p), ifInfoMsgLen)
	}
	l := &linkMsg{
		Family: p[0],
		Type:   binary.NativeEndian.Uint16(p[2:]),
		Index:  binary.NativeEndian.Uint32(p[4:]),
		Flags:  binary.NativeEndian.Uint32(p[8:]),
	}
	err := forEachAttr(p[ifInfoMsgLen:], func(typ uint16, value []byte) error {
		switch typ {
		case iflaIfName:
			l.Name = cString(value)
		case iflaLinkInfo:
			return forEachAttr(value, func(typ uint16, value []byte) error {
				if typ == iflaInfoKind {
					l.Kind = cString(value)
				}
				return nil
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("link attributes: %w", err)
	}
	return l, nil
}

func decodeAddr(p []byte) (*addrMsg, error) {
	if len(p) < ifAddrMsgLen {
		return nil, fmt.Errorf("address message of %d bytes, header needs %d", len(p), ifAddrMsgLen)
	}
	a := &addrMsg{Family: p[0], PrefixLen: p[1], Index: binary.NativeEndian.Uint32(p[4:])}
	if a.Family != afInet && a.Family != afInet6 {
		return a, nil
	}
	err := forEachAttr(p[ifAddrMsgLen:], func(typ uint16, value []byte) (err error) {
		switch typ {
		case ifaAddress:
			a.Address, err = addrFromBytes(value)
		case ifaLocal:
			a.Local, err = addrFromBytes(value)
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("address attributes: %w", err)
	}
	return a, nil
}

// addrFromBytes reads an IPv4 or IPv6 address by its length.
func addrFromBytes(b []byte) (netip.Addr, error) {
	addr, ok := netip.AddrFromSlice(b)
	if !ok {
		return netip.Addr{}, fmt.Errorf("address of %d bytes", len(b))
	}
	return addr, nil
}

func uint32FromBytes(b []byte) (uint32, error) {
	if len(b) != 4 {
		return 0, fmt.Errorf("number of %d bytes", len(b))
	}
	return binary.NativeEndian.Uint32(b), nil
}

// cString is the text of a NUL-terminated attribute.
func cString(b []byte) string {
	before, _, _ := bytes.Cut(b, []byte{0})
	return string(before)
}

// Building requests.

// appendHeader starts a message; finishMessage fills in its length.
func appendHeader(b []byte, typ, flags uint16, seq uint32) []byte {
	b = binary.NativeEndian.AppendUint32(b, 0)
	b = binary.NativeEndian.AppendUint16(b, typ)
	b = binary.NativeEndian.AppendUint16(b, flags)
	b = binary.NativeEndian.AppendUint32(b, seq)
	return binary.NativeEndian.AppendUint32(b, 0) // the port: the kernel knows our socket
}

func finishMessage(b []byte) []byte {
	binary.NativeEndian.PutUint32(b, uint32(len(b)))
	return b
}

func appendAttr(b []byte, typ uint16, value []byte) []byte {
	b = binary.NativeEndian.AppendUint16(b, uint16(4+len(value)))
	b = binary.NativeEndian.AppendUint16(b, typ)
	b = append(b, value...)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

func appendAttrUint32(b []byte, typ uint16, v uint32) []byte {
	return appendAttr(b, typ, binary.NativeEndian.AppendUint32(nil, v))
}

func appendAttrAddr(b []byte, typ uint16, addr netip.Addr) []byte {
	return appendAttr(b, typ, addr.AsSlice())
}

// dumpRequest asks for every link, address or route. The header after the
// netlink header is as long as the kernel's own for the type and all zero: with
// the family unspecified the kernel dumps every family.
func dumpRequest(typ uint16, seq uint32) []byte {
	b := appendHeader(nil, typ, nlmFRequest|nlmFDump, seq)
	switch typ {
	case rtmGetLink:
		b = append(b, make([]byte, ifInfoMsgLen)...)
	case rtmGetAddr:
		b = append(b, make([]byte, ifAddrMsgLen)...)
	default:
		b = append(b, make([]byte, rtMsgLen)...)
	}
	return finishMessage(b)
}

// routeRequest is a route add or delete in wire terms.
type routeRequest struct {
	Type    uint16 // rtmNewRoute or rtmDelRoute
	Flags   uint16 // NLM_F_* beyond request and acknowledgement
	Dst     netip.Prefix
	Gateway netip.Addr
	OIf     uint32
	Metric  uint32
	// RouteType, Protocol and Scope are rtm_type, rtm_protocol and rtm_scope.
	// Zero is "any" in a delete.
	RouteType uint8
	Protocol  uint8
	Scope     uint8
}

func (r routeRequest) marshal(seq uint32) []byte {
	b := appendHeader(nil, r.Type, nlmFRequest|nlmFAck|r.Flags, seq)
	family := byte(afInet)
	if r.Dst.Addr().Is6() {
		family = afInet6
	}
	b = append(b, family, byte(r.Dst.Bits()), 0, 0, rtTableMain, r.Protocol, r.Scope, r.RouteType, 0, 0, 0, 0)
	// A default route has no destination attribute, as the kernel's own.
	if r.Dst.Bits() > 0 {
		b = appendAttrAddr(b, rtaDst, r.Dst.Addr())
	}
	if r.Gateway.IsValid() {
		b = appendAttrAddr(b, rtaGateway, r.Gateway)
	}
	if r.OIf != 0 {
		b = appendAttrUint32(b, rtaOif, r.OIf)
	}
	if r.Metric != 0 {
		b = appendAttrUint32(b, rtaPriority, r.Metric)
	}
	return finishMessage(b)
}

// newAddrRequest adds the address in prefix to the link with this index. An IPv4
// address of a prefix shorter than /31 names its broadcast address, as ip(8) does.
func newAddrRequest(index uint32, prefix netip.Prefix, seq uint32) []byte {
	b := appendHeader(nil, rtmNewAddr, nlmFRequest|nlmFAck|nlmFCreate|nlmFExcl, seq)
	addr := prefix.Addr()
	family := byte(afInet6)
	if addr.Is4() {
		family = afInet
	}
	b = append(b, family, byte(prefix.Bits()), 0, rtScopeUniverse)
	b = binary.NativeEndian.AppendUint32(b, index)
	b = appendAttrAddr(b, ifaLocal, addr)
	b = appendAttrAddr(b, ifaAddress, addr)
	if addr.Is4() && prefix.Bits() < 31 {
		broadcast := addr.As4()
		for i := range broadcast {
			if covered := prefix.Bits() - 8*i; covered < 8 {
				broadcast[i] |= byte(0xff >> max(covered, 0))
			}
		}
		b = appendAttrAddr(b, ifaBroadcast, netip.AddrFrom4(broadcast))
	}
	return finishMessage(b)
}

// newLinkUpRequest brings the link with this index up, and sets its MTU unless
// mtu is zero.
func newLinkUpRequest(index uint32, mtu uint32, seq uint32) []byte {
	b := appendHeader(nil, rtmNewLink, nlmFRequest|nlmFAck, seq)
	b = append(b, afUnspec, 0, 0, 0)
	b = binary.NativeEndian.AppendUint32(b, index)
	b = binary.NativeEndian.AppendUint32(b, iffUp) // flags
	b = binary.NativeEndian.AppendUint32(b, iffUp) // change: only this bit
	if mtu != 0 {
		b = appendAttrUint32(b, iflaMTU, mtu)
	}
	return finishMessage(b)
}
