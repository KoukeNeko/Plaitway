package linux

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"net/netip"
	"strings"
	"testing"
)

// goldenRequests are requests in the form ip(8) sends them, built apart from
// the code under test (with sequence number 9), so that a mistake in the
// encoder is not repeated in its test.
var goldenRequests = map[string]string{
	"add-v4-gateway": `
		3c00000018000506090000000000000002180000fec700010000000008000100
		c633640008000500cb00710908000400020000000800060005000000
	`,
	"add-v4-dev": `
		3400000018000506090000000000000002180000fec7fd010000000008000100
		c633640008000400040000000800060005000000
	`,
	"add-v6-default-linklocal": `
		400000001800050609000000000000000a000000fec700010000000014000500
		fe80000000000000000000000000000108000400020000000800060005000000
	`,
	"add-v4-blackhole": `
		24000000180005060900000000000000021c0000fec700060000000008000100
		c6336480
	`,
	"delete-v4": `
		3c00000019000500090000000000000002180000fe00ff010000000008000100
		c633640008000500cb00710908000400020000000800060005000000
	`,
	"addr-v4-24": `
		300000001400050609000000000000000218000004000000080002000a080002
		080001000a080002080004000a0800ff
	`,
	"addr-v4-32": `
		280000001400050609000000000000000220000004000000080002000a080002
		080001000a080002
	`,
	"addr-v6-64": `
		400000001400050609000000000000000a400000040000001400020020010db8
		0008000000000000000000021400010020010db8000800000000000000000002
	`,
	"link-up-mtu": `
		2800000010000500090000000000000000000000040000000100000001000000
		080004008c050000
	`,
	"link-up": `
		2000000010000500090000000000000000000000040000000100000001000000
	`,
	"dump-route": `
		1c0000001a0001030900000000000000000000000000000000000000
	`,
	"dump-link": `
		2000000012000103090000000000000000000000000000000000000000000000
	`,
	"dump-addr": `
		180000001600010309000000000000000000000000000000
	`,
}

// fixture decodes one of the captured messages of fixtures_test.go. The
// captures are little-endian, as the kernels they come from.
func fixture(t testing.TB, set map[string]string, name string) []byte {
	t.Helper()
	if binary.NativeEndian.Uint16([]byte{1, 0}) != 1 {
		t.Skip("the fixtures were captured on a little-endian machine")
	}
	text, ok := set[name]
	if !ok {
		t.Fatalf("no fixture %q", name)
	}
	b, err := hex.DecodeString(strings.Join(strings.Fields(text), ""))
	if err != nil {
		t.Fatalf("fixture %q: %v", name, err)
	}
	return b
}

// fixtureMessage parses a fixture that holds exactly one message.
func fixtureMessage(t testing.TB, set map[string]string, name string) rtMessage {
	t.Helper()
	msgs, err := parseMessages(fixture(t, set, name))
	if err != nil || len(msgs) != 1 {
		t.Fatalf("fixture %q parses to %d messages, err %v", name, len(msgs), err)
	}
	return msgs[0]
}

func fixtureRoute(t testing.TB, name string) routeMsg {
	t.Helper()
	m := fixtureMessage(t, routeMessages, name)
	if m.Route == nil {
		t.Fatalf("fixture %q is not a route", name)
	}
	return *m.Route
}

func addr(s string) netip.Addr { return netip.MustParseAddr(s) }

func TestParseRealRouteMessages(t *testing.T) {
	tests := []struct {
		fixture string
		want    routeMsg
	}{
		{"v4-default-dhcp-d0", routeMsg{Family: afInet, Table: rtTableMain, Protocol: 16, Type: rtnUnicast, Gateway: addr("192.0.2.1"), OIf: 2, Priority: 100}},
		{"v4-net24-d0", routeMsg{Family: afInet, DstLen: 24, Table: rtTableMain, Protocol: rtprotKernel, Scope: rtScopeLink, Type: rtnUnicast, Dst: addr("192.0.2.0"), OIf: 2}},
		{"v4-peer-linkdown-tun0", routeMsg{Family: afInet, DstLen: 32, Table: rtTableMain, Protocol: rtprotKernel, Scope: rtScopeLink, Type: rtnUnicast, Flags: 0x10, Dst: addr("10.8.0.1"), OIf: 4}},
		{"v4-gateway-d0", routeMsg{Family: afInet, DstLen: 28, Table: rtTableMain, Protocol: RouteProtocol, Type: rtnUnicast, Dst: addr("198.51.100.64"), Gateway: addr("192.0.2.1"), OIf: 2, Priority: 5}},
		{"v4-blackhole", routeMsg{Family: afInet, DstLen: 28, Table: rtTableMain, Protocol: RouteProtocol, Type: rtnBlackhole, Dst: addr("198.51.100.128")}},
		{"v4-unreachable", routeMsg{Family: afInet, DstLen: 28, Table: rtTableMain, Protocol: 3, Type: rtnUnreachable, Dst: addr("198.51.100.144")}},
		{"v4-table100-gateway", routeMsg{Family: afInet, DstLen: 26, Table: 100, Protocol: 3, Type: rtnUnicast, Dst: addr("203.0.113.64"), Gateway: addr("192.0.2.1"), OIf: 2}},
		{"v4-local", routeMsg{Family: afInet, DstLen: 32, Table: 255, Protocol: rtprotKernel, Scope: 254, Type: 2, Dst: addr("192.0.2.2"), OIf: 2}},
		{"v6-net64-d0", routeMsg{Family: afInet6, DstLen: 64, Table: rtTableMain, Protocol: rtprotKernel, Type: rtnUnicast, Dst: addr("2001:db8:1::"), OIf: 2, Priority: 256}},
		{"v6-gateway-global", routeMsg{Family: afInet6, DstLen: 64, Table: rtTableMain, Protocol: RouteProtocol, Type: rtnUnicast, Dst: addr("2001:db8:9::"), Gateway: addr("2001:db8:1::1"), OIf: 2, Priority: 5}},
		{"v6-blackhole", routeMsg{Family: afInet6, DstLen: 48, Table: rtTableMain, Protocol: RouteProtocol, Type: rtnBlackhole, Dst: addr("2001:db8:ffff::"), OIf: 1, Priority: 1024}},
		{"v6-default-gateway-linklocal", routeMsg{Family: afInet6, Table: rtTableMain, Protocol: RouteProtocol, Type: rtnUnicast, Gateway: addr("fe80::1"), OIf: 2, Priority: 5}},
		{"v6-default-ra", routeMsg{Family: afInet6, Table: rtTableMain, Protocol: rtprotRA, Type: rtnUnicast, Gateway: addr("fe80::1"), OIf: 2, Priority: 1024}},
		{"v6-multicast-local", routeMsg{Family: afInet6, DstLen: 8, Table: 255, Protocol: rtprotKernel, Type: 5, Dst: addr("ff00::"), OIf: 2, Priority: 256}},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			got := fixtureRoute(t, tt.fixture)
			got.NextHops = nil
			if got.Flags&^0x10 != 0 || got.Flags != tt.want.Flags {
				t.Errorf("flags %#x, want %#x", got.Flags, tt.want.Flags)
			}
			if got.Dst != tt.want.Dst || got.Gateway != tt.want.Gateway || got.OIf != tt.want.OIf || got.Priority != tt.want.Priority {
				t.Errorf("got %+v\nwant %+v", got, tt.want)
			}
			if got.Family != tt.want.Family || got.DstLen != tt.want.DstLen || got.Table != tt.want.Table ||
				got.Protocol != tt.want.Protocol || got.Scope != tt.want.Scope || got.Type != tt.want.Type {
				t.Errorf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestParseRealMultipath(t *testing.T) {
	v4 := fixtureRoute(t, "v4-multipath")
	wantV4 := []nextHop{{OIf: 2, Gateway: addr("192.0.2.1")}, {OIf: 2, Gateway: addr("192.0.2.3")}}
	if v4.OIf != 0 || !sameHops(v4.NextHops, wantV4) {
		t.Errorf("IPv4 multipath: oif %d, hops %+v, want %+v", v4.OIf, v4.NextHops, wantV4)
	}
	v6 := fixtureRoute(t, "v6-multipath")
	wantV6 := []nextHop{{OIf: 2, Gateway: addr("fe80::1")}, {OIf: 2, Gateway: addr("fe80::2")}}
	if v6.Priority != 1024 || !sameHops(v6.NextHops, wantV6) {
		t.Errorf("IPv6 multipath: priority %d, hops %+v, want %+v", v6.Priority, v6.NextHops, wantV6)
	}
}

func sameHops(a, b []nextHop) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestParseRealLinkMessages(t *testing.T) {
	tests := []struct {
		fixture string
		want    linkMsg
		flags   uint32 // the flags that matter
	}{
		{"lo", linkMsg{Type: arphrdLoopback, Index: 1, Name: "lo"}, iffUp | iffLoopback | iffLowerUp},
		{"d0-dummy", linkMsg{Type: 1, Index: 2, Name: "d0", Kind: "dummy"}, iffUp | iffLowerUp},
		{"tun0-nocarrier", linkMsg{Type: arphrdNone, Index: 4, Name: "tun0", Kind: "tun"}, iffUp},
		{"wg0", linkMsg{Type: arphrdNone, Index: 6, Name: "wg0", Kind: "wireguard"}, iffUp | iffLowerUp},
		{"br0-bridge", linkMsg{Type: 1, Index: 7, Name: "br0", Kind: "bridge"}, iffUp | iffLowerUp},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			m := fixtureMessage(t, linkMessages, tt.fixture)
			if m.Type != rtmNewLink || m.Link == nil {
				t.Fatalf("not a link message: %+v", m)
			}
			got := *m.Link
			if got.Flags&(iffUp|iffLoopback|iffLowerUp) != tt.flags {
				t.Errorf("flags %#x, want %#x", got.Flags&(iffUp|iffLoopback|iffLowerUp), tt.flags)
			}
			got.Flags = 0
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseRealAddrMessages(t *testing.T) {
	tests := []struct {
		fixture string
		want    addrMsg
	}{
		{"v4-lo", addrMsg{Family: afInet, PrefixLen: 8, Index: 1, Address: addr("127.0.0.1"), Local: addr("127.0.0.1")}},
		{"v4-d0", addrMsg{Family: afInet, PrefixLen: 24, Index: 2, Address: addr("192.0.2.2"), Local: addr("192.0.2.2")}},
		// On a point-to-point link IFA_ADDRESS is the peer.
		{"v4-peer-tun0", addrMsg{Family: afInet, PrefixLen: 32, Index: 4, Address: addr("10.8.0.1"), Local: addr("10.8.0.2")}},
		{"v6-global-d0", addrMsg{Family: afInet6, PrefixLen: 64, Index: 2, Address: addr("2001:db8:1::2")}},
		{"v6-linklocal-d0", addrMsg{Family: afInet6, PrefixLen: 64, Index: 2, Address: addr("fe80::600a:94ff:fe5a:b3c1")}},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			m := fixtureMessage(t, addrMessages, tt.fixture)
			if m.Type != rtmNewAddr || m.Addr == nil || *m.Addr != tt.want {
				t.Errorf("got %+v, want %+v", m.Addr, tt.want)
			}
		})
	}
}

func TestParseRealErrors(t *testing.T) {
	refused := fixtureMessage(t, errorMessages, "add-bad-gateway")
	if refused.Type != nlmsgError || refused.Seq != 9 || refused.Errno != -101 || refused.ExtAck != "Nexthop has invalid gateway" {
		t.Errorf("refused add: %+v", refused)
	}
	absent := fixtureMessage(t, errorMessages, "del-absent")
	if absent.Type != nlmsgError || absent.Errno != -3 || absent.ExtAck != "" {
		t.Errorf("delete of an absent route: %+v", absent)
	}
}

// message builds one netlink message.
func message(typ, flags uint16, seq uint32, payload ...[]byte) []byte {
	b := appendHeader(nil, typ, flags, seq)
	for _, p := range payload {
		b = append(b, p...)
	}
	return finishMessage(b)
}

func le32(v uint32) []byte { return binary.NativeEndian.AppendUint32(nil, v) }

// routePayload is an rtmsg followed by attributes.
func routePayload(family, dstLen, table, protocol, scope, typ uint8, flags uint32, attrs ...[]byte) []byte {
	b := append([]byte{family, dstLen, 0, 0, table, protocol, scope, typ}, le32(flags)...)
	for _, a := range attrs {
		b = append(b, a...)
	}
	return b
}

func attrOf(typ uint16, value []byte) []byte { return appendAttr(nil, typ, value) }

func TestParseMessagesFraming(t *testing.T) {
	route := routePayload(afInet, 24, rtTableMain, 0, 0, rtnUnicast, 0, attrOf(rtaDst, []byte{192, 0, 2, 0}), attrOf(rtaOif, le32(3)))
	link := append([]byte{0, 0, 1, 0}, append(le32(5), append(le32(iffUp|iffLowerUp), le32(0)...)...)...)
	link = append(link, attrOf(iflaIfName, []byte("eth0\x00"))...)
	t.Run("several messages in one datagram", func(t *testing.T) {
		b := append(message(rtmNewRoute, 0, 1, route), message(rtmNewLink, 0, 2, link)...)
		b = append(b, message(nlmsgDone, 2, 3, le32(0))...)
		msgs, err := parseMessages(b)
		if err != nil || len(msgs) != 3 {
			t.Fatalf("got %d messages, err %v", len(msgs), err)
		}
		if msgs[0].Route == nil || msgs[0].Route.OIf != 3 || msgs[1].Link == nil || msgs[1].Link.Name != "eth0" || msgs[2].Type != nlmsgDone {
			t.Errorf("messages %+v", msgs)
		}
	})
	t.Run("a type we do not read is skipped", func(t *testing.T) {
		b := append(message(28 /* RTM_NEWNEIGH */, 0, 1, make([]byte, 12)), message(rtmNewRoute, 0, 2, route)...)
		msgs, err := parseMessages(b)
		if err != nil || len(msgs) != 1 || msgs[0].Seq != 2 {
			t.Errorf("got %+v, err %v", msgs, err)
		}
	})
	t.Run("a done message reports the status of the dump", func(t *testing.T) {
		msgs, err := parseMessages(message(nlmsgDone, 2, 4, le32(uint32(0xffffffea))))
		if err != nil || len(msgs) != 1 || msgs[0].Errno != -22 {
			t.Errorf("got %+v, err %v", msgs, err)
		}
	})
	t.Run("an acknowledgement", func(t *testing.T) {
		request := message(rtmNewRoute, nlmFRequest|nlmFAck, 8, route)
		msgs, err := parseMessages(message(nlmsgError, 0, 8, le32(0), request))
		if err != nil || len(msgs) != 1 || msgs[0].Type != nlmsgError || msgs[0].Errno != 0 || msgs[0].Seq != 8 {
			t.Errorf("got %+v, err %v", msgs, err)
		}
	})
	t.Run("an explanation after a capped request", func(t *testing.T) {
		request := message(rtmNewRoute, nlmFRequest|nlmFAck, 8, route)[:nlmsgHdrLen]
		explanation := attrOf(nlmsgErrAttrMsg, []byte("Nexthop has invalid gateway\x00"))
		msgs, err := parseMessages(message(nlmsgError, nlmFCapped|nlmFAckTLVs, 8, le32(uint32(0xffffff9b)), request, explanation))
		if err != nil || len(msgs) != 1 || msgs[0].Errno != -101 || msgs[0].ExtAck != "Nexthop has invalid gateway" {
			t.Errorf("got %+v, err %v", msgs, err)
		}
	})
	t.Run("an explanation that cannot be read costs the text only", func(t *testing.T) {
		request := message(rtmNewRoute, nlmFRequest|nlmFAck, 8, route)
		msgs, err := parseMessages(message(nlmsgError, nlmFAckTLVs, 8, le32(uint32(0xffffff9b)), request, []byte{9, 9, 9}))
		if err != nil || len(msgs) != 1 || msgs[0].Errno != -101 || msgs[0].ExtAck != "" {
			t.Errorf("got %+v, err %v", msgs, err)
		}
	})
}

func TestParseMessagesRejectsBrokenInput(t *testing.T) {
	route := routePayload(afInet, 24, rtTableMain, 0, 0, rtnUnicast, 0, attrOf(rtaDst, []byte{192, 0, 2, 0}))
	good := message(rtmNewRoute, 0, 1, route)
	withLength := func(b []byte, length uint32) []byte {
		b = bytes.Clone(b)
		binary.NativeEndian.PutUint32(b, length)
		return b
	}
	tests := map[string][]byte{
		"fewer bytes than a header":      good[:10],
		"stray bytes after the message":  append(bytes.Clone(good), 1, 2, 3),
		"length beyond the datagram":     withLength(good, uint32(len(good)+4)),
		"length below the header":        withLength(good, 8),
		"truncated route header":         message(rtmNewRoute, 0, 1, route[:8]),
		"truncated link header":          message(rtmNewLink, 0, 1, make([]byte, 10)),
		"truncated address header":       message(rtmNewAddr, 0, 1, make([]byte, 4)),
		"truncated error":                message(nlmsgError, 0, 1, le32(0)),
		"attribute longer than message":  message(rtmNewRoute, 0, 1, route, []byte{40, 0, 1, 0, 1, 2, 3, 4}),
		"attribute shorter than header":  message(rtmNewRoute, 0, 1, route, []byte{2, 0, 1, 0}),
		"stray bytes after an attribute": message(rtmNewRoute, 0, 1, route, []byte{1, 2}),
		"destination of three bytes":     message(rtmNewRoute, 0, 1, routePayload(afInet, 24, rtTableMain, 0, 0, rtnUnicast, 0, attrOf(rtaDst, []byte{1, 2, 3}))),
		"interface of two bytes":         message(rtmNewRoute, 0, 1, routePayload(afInet, 24, rtTableMain, 0, 0, rtnUnicast, 0, attrOf(rtaOif, []byte{1, 2}))),
		"next hop shorter than header":   message(rtmNewRoute, 0, 1, routePayload(afInet, 24, rtTableMain, 0, 0, rtnUnicast, 0, attrOf(rtaMultipath, []byte{4, 0, 0, 0, 0, 0, 0, 0}))),
		"next hop longer than attribute": message(rtmNewRoute, 0, 1, routePayload(afInet, 24, rtTableMain, 0, 0, rtnUnicast, 0, attrOf(rtaMultipath, []byte{64, 0, 0, 0, 1, 0, 0, 0}))),
		"stray bytes after a next hop":   message(rtmNewRoute, 0, 1, routePayload(afInet, 24, rtTableMain, 0, 0, rtnUnicast, 0, attrOf(rtaMultipath, []byte{8, 0, 0, 0, 1, 0, 0, 0, 1, 2}))),
		"gateway of a next hop is bad":   message(rtmNewRoute, 0, 1, routePayload(afInet, 24, rtTableMain, 0, 0, rtnUnicast, 0, attrOf(rtaMultipath, append([]byte{13, 0, 0, 0, 1, 0, 0, 0, 5, 0, 5, 0, 1}, 0, 0, 0)))),
	}
	for name, b := range tests {
		t.Run(name, func(t *testing.T) {
			if msgs, err := parseMessages(b); err == nil {
				t.Errorf("got %d messages without an error", len(msgs))
			}
		})
	}
}

func TestParseMessagesIgnoresOtherFamilies(t *testing.T) {
	// An MPLS route has a label where a destination would be.
	const afMPLS = 28
	b := message(rtmNewRoute, 0, 1, routePayload(afMPLS, 20, rtTableMain, 0, 0, rtnUnicast, 0, attrOf(rtaDst, []byte{1, 2, 3}), attrOf(rtaOif, []byte{9})))
	msgs, err := parseMessages(b)
	if err != nil || len(msgs) != 1 || msgs[0].Route == nil || msgs[0].Route.Family != afMPLS {
		t.Fatalf("got %+v, err %v", msgs, err)
	}
	if routes := routesFromMessage(*msgs[0].Route, func(uint32) string { return "x" }); len(routes) != 0 {
		t.Errorf("an MPLS route became %+v", routes)
	}
}

func TestForEachAttrClearsNestingFlags(t *testing.T) {
	nested := attrOf(iflaLinkInfo|0x8000, attrOf(iflaInfoKind, []byte("dummy\x00")))
	var types []uint16
	err := forEachAttr(append(nested, attrOf(3|0x4000, []byte{1})...), func(typ uint16, _ []byte) error {
		types = append(types, typ)
		return nil
	})
	if err != nil || len(types) != 2 || types[0] != iflaLinkInfo || types[1] != 3 {
		t.Errorf("types %v, err %v", types, err)
	}
}

func TestMarshalMatchesIPRoute(t *testing.T) {
	const (
		add    = nlmFCreate | nlmFExcl
		delete = 0
	)
	tests := []struct {
		name string
		got  []byte
	}{
		{"add-v4-gateway", routeRequest{Type: rtmNewRoute, Flags: add, Dst: netip.MustParsePrefix("198.51.100.0/24"), Gateway: addr("203.0.113.9"), OIf: 2, Metric: 5, RouteType: rtnUnicast, Protocol: RouteProtocol}.marshal(9)},
		{"add-v4-dev", routeRequest{Type: rtmNewRoute, Flags: add, Dst: netip.MustParsePrefix("198.51.100.0/24"), OIf: 4, Metric: 5, RouteType: rtnUnicast, Protocol: RouteProtocol, Scope: rtScopeLink}.marshal(9)},
		{"add-v6-default-linklocal", routeRequest{Type: rtmNewRoute, Flags: add, Dst: netip.MustParsePrefix("::/0"), Gateway: addr("fe80::1"), OIf: 2, Metric: 5, RouteType: rtnUnicast, Protocol: RouteProtocol}.marshal(9)},
		{"add-v4-blackhole", routeRequest{Type: rtmNewRoute, Flags: add, Dst: netip.MustParsePrefix("198.51.100.128/28"), RouteType: rtnBlackhole, Protocol: RouteProtocol}.marshal(9)},
		{"delete-v4", routeRequest{Type: rtmDelRoute, Flags: delete, Dst: netip.MustParsePrefix("198.51.100.0/24"), Gateway: addr("203.0.113.9"), OIf: 2, Metric: 5, RouteType: rtnUnicast, Scope: rtScopeNowhere}.marshal(9)},
		{"addr-v4-24", newAddrRequest(4, netip.MustParsePrefix("10.8.0.2/24"), 9)},
		{"addr-v4-32", newAddrRequest(4, netip.MustParsePrefix("10.8.0.2/32"), 9)},
		{"addr-v6-64", newAddrRequest(4, netip.MustParsePrefix("2001:db8:8::2/64"), 9)},
		{"link-up-mtu", newLinkUpRequest(4, 1420, 9)},
		{"link-up", newLinkUpRequest(4, 0, 9)},
		{"dump-route", dumpRequest(rtmGetRoute, 9)},
		{"dump-link", dumpRequest(rtmGetLink, 9)},
		{"dump-addr", dumpRequest(rtmGetAddr, 9)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if want := fixture(t, goldenRequests, tt.name); !bytes.Equal(tt.got, want) {
				t.Errorf("got  %x\nwant %x", tt.got, want)
			}
		})
	}
}

// The broadcast address of an IPv4 address is named for every prefix shorter
// than /31, and is the address with all host bits set.
func TestAddrRequestBroadcast(t *testing.T) {
	tests := []struct {
		prefix string
		want   string // "" for none
	}{
		{"10.8.16.5/20", "10.8.31.255"},
		{"192.0.2.77/25", "192.0.2.127"},
		{"172.17.0.1/16", "172.17.255.255"},
		{"10.0.0.1/8", "10.255.255.255"},
		{"203.0.113.9/30", "203.0.113.11"},
		{"203.0.113.9/31", ""},
		{"203.0.113.9/32", ""},
		{"0.0.0.1/0", "255.255.255.255"},
		{"2001:db8::1/64", ""},
	}
	for _, tt := range tests {
		t.Run(tt.prefix, func(t *testing.T) {
			msgs := newAddrRequest(1, netip.MustParsePrefix(tt.prefix), 1)
			var got string
			err := forEachAttr(msgs[nlmsgHdrLen+ifAddrMsgLen:], func(typ uint16, value []byte) error {
				if typ == ifaBroadcast {
					got = netip.AddrFrom4([4]byte(value)).String()
				}
				return nil
			})
			if err != nil || got != tt.want {
				t.Errorf("broadcast %q (err %v), want %q", got, err, tt.want)
			}
		})
	}
}
