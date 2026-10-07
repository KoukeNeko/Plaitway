package macos

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"math/rand"
	"net/netip"
	"slices"
	"strings"
	"testing"
)

// fixture decodes one of the captured messages of fixtures_test.go.
func fixture(t testing.TB, set map[string]string, name string) []byte {
	t.Helper()
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

// padded returns a sockaddr of a parsed message with the padding that follows
// it in the message. Slices of a parsed message keep the capacity of the buffer.
func padded(sa []byte) []byte { return sa[:sockaddrSpace(len(sa))] }

func TestParseMessagesSplitsABuffer(t *testing.T) {
	names := []string{"v4-default-en0", "v6-default-scoped-utun0", "v4-net24-en0"}
	var buf []byte
	for _, n := range names {
		buf = append(buf, fixture(t, ribMessages, n)...)
	}
	msgs, err := parseMessages(buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != len(names) {
		t.Fatalf("got %d messages, want %d", len(msgs), len(names))
	}
	for i, m := range msgs {
		if m.Type != rtmGet {
			t.Errorf("message %d: type %d, want RTM_GET", i, m.Type)
		}
	}
	if msgs[1].Index != 19 || msgs[1].Flags != 0x41010003 {
		t.Errorf("message 1: index %d flags %#x, want 19 and 0x41010003", msgs[1].Index, msgs[1].Flags)
	}
}

func TestParseMessagesSkipsWhatItDoesNotKnow(t *testing.T) {
	other := fixture(t, ribMessages, "v4-net24-en0")
	known := slices.Clone(other)
	other[2] = rtmVersion + 1 // another protocol version
	unknownType := slices.Clone(known)
	unknownType[3] = 0x42
	msgs, err := parseMessages(slices.Concat(other, unknownType, known))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want only the known one", len(msgs))
	}
}

func TestParseMessagesRejectsBrokenLengths(t *testing.T) {
	good := fixture(t, ribMessages, "v4-net24-en0")
	tests := map[string][]byte{
		"truncated":     good[:len(good)-1],
		"stray bytes":   slices.Concat(good, []byte{1, 2, 3}),
		"zero length":   slices.Concat(good, make([]byte, 8)),
		"short header":  {byte(rtMsghdrLen - 1), 0, rtmVersion, rtmGet, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		"missing addrs": withAddrMask(good, rtaDst|rtaGateway|rtaNetmask|rtaIFA|0x10|0x40|0x80),
	}
	for name, b := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseMessages(b); err == nil {
				t.Fatal("no error")
			}
		})
	}
}

func withAddrMask(msg []byte, mask uint32) []byte {
	b := slices.Clone(msg)
	binary.NativeEndian.PutUint32(b[12:], mask)
	return b
}

// The parser must survive any bytes: it is fed by the kernel, but runs as root.
func TestParseMessagesNeverPanics(t *testing.T) {
	var seeds [][]byte
	for _, set := range []map[string]string{ribMessages, ifaceMessages, missMessage} {
		for name := range set {
			seeds = append(seeds, fixture(t, set, name))
		}
	}
	try := func(b []byte) {
		t.Helper()
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic %v on %x", r, b)
			}
		}()
		_, _ = parseMessagesUnguarded(b)
	}
	rnd := rand.New(rand.NewSource(1))
	for _, seed := range seeds {
		for n := range len(seed) {
			try(seed[:n])
		}
		for range 300 {
			b := slices.Clone(seed)
			for range 1 + rnd.Intn(4) {
				b[rnd.Intn(len(b))] = byte(rnd.Intn(256))
			}
			try(b)
		}
	}
}

func TestPrefixLenFromNetmaskOfKernelMasks(t *testing.T) {
	tests := []struct {
		mask   string
		family byte
		want   int
		ok     bool
	}{
		{"05ffffffff", afInet, 8, true},
		{"06ffffffffff", afInet, 16, true},
		{"07ffffffffffff", afInet, 24, true},
		{"07fffffffffffe", afInet, 23, true},
		{"05fffffff0", afInet, 4, true},
		{"08ffffffffffffff", afInet, 32, true},
		{"", afInet, 0, true},
		{"09ffffffffffffffff", afInet6, 8, true},
		{"0cffffffffffffffffffffff", afInet6, 32, true},
		{"10ffffffffffffffffffffffffffffff", afInet6, 64, true},
		{"18ffffffffffffffffffffffffffffffffffffffffffffff", afInet6, 128, true},
		{"07ffffffffff01", afInet, 0, false}, // not a prefix
		{"06ffffff00ff", afInet, 0, false},
	}
	for _, tt := range tests {
		sa, _ := hex.DecodeString(tt.mask)
		got, ok := prefixLenFromNetmask(tt.family, sa)
		if got != tt.want || ok != tt.ok {
			t.Errorf("mask %s: got %d, %v; want %d, %v", tt.mask, got, ok, tt.want, tt.ok)
		}
	}
}

// What the kernel reports for the same routes is what we send.
func TestAppendNetmaskMatchesKernelForm(t *testing.T) {
	tests := []struct {
		fixture string
		v6      bool
		bits    int
	}{
		{"v4-default-en0", false, 0},
		{"v4-net8-lo0", false, 8},
		{"v4-linklocal-en0", false, 16},
		{"v4-net24-en0", false, 24},
		{"v4-net32-en0", false, 32},
		{"v4-multicast-en0", false, 4},
		{"v6-ula-net64-bridge100", true, 64},
		{"v6-multicast8-lo0", true, 8},
		{"v6-multicast32-lo0", true, 32},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			m := fixtureMessage(t, ribMessages, tt.fixture)
			want := padded(m.Addrs[rtaxNetmask])
			if got := appendNetmask(nil, tt.v6, tt.bits); !bytes.Equal(got, want) {
				t.Errorf("/%d mask is %x, the kernel's is %x", tt.bits, got, want)
			}
		})
	}
}

func TestAppendNetmaskOfOddLengths(t *testing.T) {
	tests := []struct {
		v6   bool
		bits int
		want string
	}{
		{false, 23, "07fffffffffffe00"},
		{false, 1, "05ffffff80000000"},
		{true, 127, "18" + strings.Repeat("ff", 22) + "fe"},
		{true, 128, "18" + strings.Repeat("ff", 23)},
		{true, 0, "00000000"},
	}
	for _, tt := range tests {
		got := hex.EncodeToString(appendNetmask(nil, tt.v6, tt.bits))
		if got != tt.want {
			t.Errorf("v6=%v /%d: got %s, want %s", tt.v6, tt.bits, got, tt.want)
		}
	}
}

func TestAppendSockaddrsMatchKernelForm(t *testing.T) {
	tests := []struct {
		fixture string
		slot    int
		build   func() []byte
	}{
		{"v4-net24-en0", rtaxDst, func() []byte { return appendSockaddrIP(nil, netip.MustParseAddr("192.0.2.0"), 0) }},
		{"v4-default-en0", rtaxGateway, func() []byte { return appendSockaddrIP(nil, netip.MustParseAddr("192.0.2.1"), 0) }},
		{"v4-net24-utun10", rtaxGateway, func() []byte { return appendSockaddrLink(nil, 50) }},
		{"v4-default-scoped-utun10", rtaxGateway, func() []byte { return appendSockaddrLink(nil, 50) }},
		{"v6-linklocal-net64-en0", rtaxDst, func() []byte { return appendSockaddrIP(nil, netip.MustParseAddr("fe80::"), 14) }},
		{"v6-linklocal-net64-lo0", rtaxGateway, func() []byte { return appendSockaddrIP(nil, netip.MustParseAddr("fe80::1"), 1) }},
		{"v6-multicast8-gateway-utun0", rtaxGateway, func() []byte { return appendSockaddrIP(nil, netip.MustParseAddr("fe80::1111:2222:3333:4444"), 19) }},
		{"v6-multicast32-lo0", rtaxDst, func() []byte { return appendSockaddrIP(nil, netip.MustParseAddr("ff01::"), 1) }},
		{"v6-ula-net64-bridge100", rtaxDst, func() []byte { return appendSockaddrIP(nil, netip.MustParseAddr("fd00:db8::"), 0) }},
		{"v6-host-loopback", rtaxDst, func() []byte { return appendSockaddrIP(nil, netip.IPv6Loopback(), 0) }},
	}
	for _, tt := range tests {
		t.Run(tt.fixture+"/"+string(rune('0'+tt.slot)), func(t *testing.T) {
			m := fixtureMessage(t, ribMessages, tt.fixture)
			got, want := tt.build(), padded(m.Addrs[tt.slot])
			if want[1] == afInet && tt.slot == rtaxGateway {
				// The kernel keeps the interface of an IPv4 gateway in sin_zero.
				want = append(slices.Clone(want[:8]), make([]byte, len(want)-8)...)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("got %x, kernel has %x", got, want)
			}
		})
	}
}

// The type of a link-level address is the type of the interface (6 for
// Ethernet); only the interface index matters when adding a route.
func TestAppendSockaddrLinkDiffersFromKernelOnlyInType(t *testing.T) {
	m := fixtureMessage(t, ribMessages, "v4-net24-en0")
	want := slices.Clone(padded(m.Addrs[rtaxGateway]))
	want[4] = 0
	if got := appendSockaddrLink(nil, 14); !bytes.Equal(got, want) {
		t.Errorf("got %x, want %x", got, want)
	}
}

func TestDecodeScopedIPv6(t *testing.T) {
	tests := []struct {
		fixture   string
		slot      int
		want      string
		wantScope int
	}{
		{"v6-linklocal-net64-en0", rtaxDst, "fe80::", 14},
		{"v6-linklocal-net64-lo0", rtaxGateway, "fe80::1", 1},
		{"v6-default-scoped-utun0", rtaxGateway, "fe80::", 19}, // the kernel also sets sin6_scope_id here
		{"v6-multicast8-gateway-utun0", rtaxGateway, "fe80::1111:2222:3333:4444", 19},
		{"v6-multicast32-lo0", rtaxDst, "ff01::", 1},
		{"v6-ula-net64-bridge100", rtaxDst, "fd00:db8::", 0},
	}
	for _, tt := range tests {
		m := fixtureMessage(t, ribMessages, tt.fixture)
		got, scope, ok := ipFromSockaddr(m.Addrs[tt.slot])
		if !ok || got != netip.MustParseAddr(tt.want) || scope != tt.wantScope {
			t.Errorf("%s slot %d: got %v scope %d ok %v, want %s scope %d", tt.fixture, tt.slot, got, scope, ok, tt.want, tt.wantScope)
		}
	}
}

// The three socket addresses of a real route message are what newRouteRequest
// sends for the same route, byte for byte; only the header differs.
func TestMarshalKeepsTheKernelsAddresses(t *testing.T) {
	ifIndex := func(name string) (int, error) { return map[string]int{"utun10": 50}[name], nil }
	cases := []struct {
		fixture string
		prefix  string
		scoped  bool
	}{
		{"v4-net24-utun10", "203.0.113.0/24", false},
		{"v4-default-scoped-utun10", "0.0.0.0/0", true},
		{"v4-multicast-scoped-utun10", "224.0.0.0/4", true},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			m := fixtureMessage(t, ribMessages, c.fixture)
			var want []byte
			for _, slot := range []int{rtaxDst, rtaxGateway, rtaxNetmask} {
				want = append(want, padded(m.Addrs[slot])...)
			}
			req, err := newRouteRequest(rtmAdd, routeOf(c.prefix, "", "utun10", c.scoped), ifIndex)
			if err != nil {
				t.Fatal(err)
			}
			if got := req.marshal()[rtMsghdrLen:]; !bytes.Equal(got, want) {
				t.Errorf("addresses are %x, the kernel's are %x", got, want)
			}
		})
	}
}

// A whole message, field by field: add 198.51.100.0/24 via 192.0.2.1.
func TestMarshalWholeMessage(t *testing.T) {
	req := routeRequest{
		Type: rtmAdd, Flags: rtfUp | rtfGateway | rtfStatic, Seq: 7, PID: 4242,
		Dst:     netip.MustParsePrefix("198.51.100.0/24"),
		Gateway: netip.MustParseAddr("192.0.2.1"), Netmask: true,
	}
	want := "" +
		"8400" + "05" + "01" + "0000" + "0000" + // length 132, version 5, RTM_ADD, no interface
		"03080000" + "07000000" + // UP|GATEWAY|STATIC; DST|GATEWAY|NETMASK
		"92100000" + "07000000" + // pid 4242, seq 7
		strings.Repeat("00", rtMsghdrLen-24) + // errno, use, metrics: zero
		"10020000c633640000000000" + "00000000" + // 198.51.100.0
		"10020000c000020100000000" + "00000000" + // 192.0.2.1
		"07ffffffffffff00" // /24 in the kernel's form
	if got := hex.EncodeToString(req.marshal()); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// Captured from the kernel of macOS 27 while a utun interface lost and gained
// its IPv6 addresses: the mask announces the broadcast slot (RTAX_BRD, bit 7)
// and the message ends after the interface address without it.
var ipv6AddressMessages = map[string]string{
	"deladdr-utun9-link-local": "6000050db40000000001000029000000000000001c1e000000000000ffffffffffffffff00000000000000000000000014122900010500007574756e39000000000000001c1e000000000000fe80002900000000d874aed01f380d9100000000",
	"newaddr-utun9-ula":        "6000050cb4000000000100002e000000000000001c1e000000000000ffffffffffffffff00000000000000000000000014122e00010500007574756e39000000000000001c1e000000000000fdfaf0b6f0af0000000000000000000200000000",
}

func TestParseIPv6AddressMessagesWithoutTheBroadcastSlot(t *testing.T) {
	const rtaxBRD = 7
	tests := []struct {
		name  string
		typ   int
		index int
	}{
		{"deladdr-utun9-link-local", rtmDelAddr, 41},
		{"newaddr-utun9-ula", rtmNewAddr, 46},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := fixture(t, ipv6AddressMessages, tt.name)
			msgs, err := parseMessages(raw)
			if err != nil {
				t.Fatalf("parseMessages: %v", err)
			}
			if len(msgs) != 1 {
				t.Fatalf("got %d messages, want 1", len(msgs))
			}
			m := msgs[0]
			if m.Type != tt.typ || m.Index != tt.index || m.AddrMask != 0xb4 {
				t.Errorf("type %d index %d mask %#x, want type %d index %d mask 0xb4", m.Type, m.Index, m.AddrMask, tt.typ, tt.index)
			}
			if addr, _, ok := ipFromSockaddr(m.Addrs[rtaxIFA]); !ok || !addr.Is6() {
				t.Errorf("interface address = %v (decoded %v), want an IPv6 address", m.Addrs[rtaxIFA], ok)
			}
			if m.Addrs[rtaxBRD] != nil {
				t.Errorf("the broadcast slot holds %x, want nothing", m.Addrs[rtaxBRD])
			}
		})
	}
}

// Only the address messages tolerate a missing trailing address: a route
// message that ends early is still broken.
func TestParseMessagesStillRejectsARouteMessageThatEndsEarly(t *testing.T) {
	raw := fixture(t, ipv6AddressMessages, "newaddr-utun9-ula")
	raw[3] = rtmAdd // the same bytes, read as a route message, are too short for their header
	if _, err := parseMessages(raw); err == nil {
		t.Fatal("a truncated route message parsed")
	}
}
