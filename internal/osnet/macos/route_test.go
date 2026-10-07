package macos

import (
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"syscall"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// fixtureNames are the interfaces of the machine the fixtures come from.
var fixtureNames = map[int]string{1: "lo0", 14: "en0", 16: "awdl0", 17: "llw0", 19: "utun0", 29: "bridge100", 50: "utun10"}

func fixtureIfaceName(index int) string { return fixtureNames[index] }

func fixtureIfIndex(name string) (int, error) {
	for index, n := range fixtureNames {
		if n == name {
			return index, nil
		}
	}
	return 0, fmt.Errorf("interface %s: %w", name, os.ErrNotExist)
}

func routeOf(prefix, gateway, iface string, scoped bool) osnet.Route {
	r := osnet.Route{Dst: netip.MustParsePrefix(prefix), Iface: iface, Scoped: scoped}
	if gateway != "" {
		r.Gateway = netip.MustParseAddr(gateway)
	}
	return r
}

func TestRouteFromRealMessages(t *testing.T) {
	tests := []struct {
		fixture string
		want    osnet.Route
	}{
		{"v4-default-en0", osnet.Route{Dst: netip.MustParsePrefix("0.0.0.0/0"), Gateway: netip.MustParseAddr("192.0.2.1"), Iface: "en0", Static: true, Flags: 0x40010803}},
		{"v4-default-scoped-bridge100", osnet.Route{Dst: netip.MustParsePrefix("0.0.0.0/0"), Iface: "bridge100", Static: true, Scoped: true, Flags: 0x41000901}},
		{"v4-default-scoped-utun10", osnet.Route{Dst: netip.MustParsePrefix("0.0.0.0/0"), Iface: "utun10", Static: true, Scoped: true, Flags: 0x41000901}},
		{"v4-net24-utun10", osnet.Route{Dst: netip.MustParsePrefix("203.0.113.0/24"), Iface: "utun10", Static: true, Flags: 0x901}},
		{"v4-host-local-utun10", osnet.Route{Dst: netip.MustParsePrefix("203.0.113.2/32"), Gateway: netip.MustParseAddr("203.0.113.2"), Iface: "utun10", Flags: 0x200005}},
		{"v4-net8-lo0", osnet.Route{Dst: netip.MustParsePrefix("127.0.0.0/8"), Gateway: netip.MustParseAddr("127.0.0.1"), Iface: "lo0", Static: true, Flags: 0x901}},
		{"v4-linklocal-en0", osnet.Route{Dst: netip.MustParsePrefix("169.254.0.0/16"), Iface: "en0", Static: true, Flags: 0x901}},
		{"v4-net24-en0", osnet.Route{Dst: netip.MustParsePrefix("192.0.2.0/24"), Iface: "en0", Static: true, Flags: 0x901}},
		// A /32 with a netmask is a network route, not a host route.
		{"v4-net32-en0", osnet.Route{Dst: netip.MustParsePrefix("192.0.2.1/32"), Iface: "en0", Static: true, Flags: 0x901}},
		{"v4-multicast-en0", osnet.Route{Dst: netip.MustParsePrefix("224.0.0.0/4"), Iface: "en0", Static: true, Flags: 0x800901}},
		{"v4-multicast-scoped-utun10", osnet.Route{Dst: netip.MustParsePrefix("224.0.0.0/4"), Iface: "utun10", Static: true, Scoped: true, Flags: 0x1800901}},
		{"v4-broadcast-scoped-utun10", osnet.Route{Dst: netip.MustParsePrefix("255.255.255.255/32"), Iface: "utun10", Static: true, Scoped: true, Flags: 0x1000901}},
		{"v6-default-scoped-utun0", osnet.Route{Dst: netip.MustParsePrefix("::/0"), Gateway: netip.MustParseAddr("fe80::").WithZone("utun0"), Iface: "utun0", Scoped: true, Flags: 0x41010003}},
		{"v6-host-loopback", osnet.Route{Dst: netip.MustParsePrefix("::1/128"), Gateway: netip.IPv6Loopback(), Iface: "lo0", Flags: 0x200405}},
		{"v6-ula-net64-bridge100", osnet.Route{Dst: netip.MustParsePrefix("fd00:db8::/64"), Iface: "bridge100", Flags: 0x101}},
		{"v6-linklocal-net64-lo0", osnet.Route{Dst: netip.MustParsePrefix("fe80::/64"), Gateway: netip.MustParseAddr("fe80::1").WithZone("lo0"), Iface: "lo0", Scoped: true, Flags: 0x1010001}},
		{"v6-linklocal-net64-en0", osnet.Route{Dst: netip.MustParsePrefix("fe80::/64"), Iface: "en0", Scoped: true, Flags: 0x1000101}},
		{"v6-multicast8-lo0", osnet.Route{Dst: netip.MustParsePrefix("ff00::/8"), Gateway: netip.IPv6Loopback(), Iface: "lo0", Scoped: true, Flags: 0x1800101}},
		{"v6-multicast8-gateway-utun0", osnet.Route{Dst: netip.MustParsePrefix("ff00::/8"), Gateway: netip.MustParseAddr("fe80::1111:2222:3333:4444").WithZone("utun0"), Iface: "utun0", Scoped: true, Flags: 0x1800101}},
		{"v6-multicast32-lo0", osnet.Route{Dst: netip.MustParsePrefix("ff01::/32"), Gateway: netip.IPv6Loopback(), Iface: "lo0", Scoped: true, Flags: 0x1800101}},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			got, ok := routeFromMessage(fixtureMessage(t, ribMessages, tt.fixture), fixtureIfaceName)
			if !ok || got != tt.want {
				t.Errorf("got %+v (ok %v)\nwant %+v", got, ok, tt.want)
			}
		})
	}
}

func TestRouteFromMessageSkipsWhatRouteCannotExpress(t *testing.T) {
	tests := map[string]func(m *rtMessage){
		"no destination": func(m *rtMessage) { m.AddrMask &^= rtaDst },
		"not an IP":      func(m *rtMessage) { m.Addrs[rtaxDst] = appendSockaddrLink(nil, 1) },
		"not a prefix":   func(m *rtMessage) { m.Addrs[rtaxNetmask] = []byte{7, 0xff, 0xff, 0xff, 0xff, 0x01, 0xff} },
		"short sockaddr": func(m *rtMessage) { m.Addrs[rtaxDst] = []byte{16} },
		"empty sockaddr": func(m *rtMessage) { m.Addrs[rtaxDst] = nil },
		"unknown family": func(m *rtMessage) { m.Addrs[rtaxDst] = []byte{16, 99, 0, 0, 1, 2, 3, 4} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			m := fixtureMessage(t, ribMessages, "v4-net24-en0")
			mutate(&m)
			if r, ok := routeFromMessage(m, fixtureIfaceName); ok {
				t.Errorf("got route %+v", r)
			}
		})
	}
}

func TestNewRouteRequest(t *testing.T) {
	const (
		up      = rtfUp | rtfStatic
		gateway = rtfGateway
	)
	tests := []struct {
		name  string
		route osnet.Route
		want  routeRequest
	}{
		{
			name:  "gateway route",
			route: routeOf("198.51.100.0/24", "192.0.2.1", "en0", false),
			want:  routeRequest{Flags: up | gateway, Dst: netip.MustParsePrefix("198.51.100.0/24"), Gateway: netip.MustParseAddr("192.0.2.1"), Netmask: true},
		},
		{
			name:  "host route through a gateway",
			route: routeOf("198.51.100.7/32", "192.0.2.1", "", false),
			want:  routeRequest{Flags: up | gateway | rtfHost, Dst: netip.MustParsePrefix("198.51.100.7/32"), Gateway: netip.MustParseAddr("192.0.2.1")},
		},
		{
			name:  "host bits are cleared",
			route: routeOf("198.51.100.77/24", "192.0.2.1", "", false),
			want:  routeRequest{Flags: up | gateway, Dst: netip.MustParsePrefix("198.51.100.0/24"), Gateway: netip.MustParseAddr("192.0.2.1"), Netmask: true},
		},
		{
			name:  "interface-bound route",
			route: routeOf("203.0.113.0/24", "", "utun10", false),
			want:  routeRequest{Flags: up, Dst: netip.MustParsePrefix("203.0.113.0/24"), LinkIndex: 50, Netmask: true},
		},
		{
			name:  "interface-bound host route",
			route: routeOf("203.0.113.9/32", "", "utun10", false),
			want:  routeRequest{Flags: up | rtfHost, Dst: netip.MustParsePrefix("203.0.113.9/32"), LinkIndex: 50},
		},
		{
			name:  "scoped interface-bound default",
			route: routeOf("0.0.0.0/0", "", "utun10", true),
			want:  routeRequest{Flags: up | rtfIfScope, Index: 50, Dst: netip.MustParsePrefix("0.0.0.0/0"), LinkIndex: 50, Netmask: true},
		},
		{
			name:  "scoped gateway route",
			route: routeOf("198.51.100.0/24", "192.0.2.1", "en0", true),
			want:  routeRequest{Flags: up | gateway | rtfIfScope, Index: 14, Dst: netip.MustParsePrefix("198.51.100.0/24"), Gateway: netip.MustParseAddr("192.0.2.1"), Netmask: true},
		},
		{
			name:  "IPv6 gateway route",
			route: routeOf("2001:db8:1::/48", "2001:db8::1", "en0", false),
			want:  routeRequest{Flags: up | gateway, Dst: netip.MustParsePrefix("2001:db8:1::/48"), Gateway: netip.MustParseAddr("2001:db8::1"), Netmask: true},
		},
		{
			name:  "IPv6 host route",
			route: routeOf("2001:db8:1::5/128", "", "utun10", false),
			want:  routeRequest{Flags: up | rtfHost, Dst: netip.MustParsePrefix("2001:db8:1::5/128"), LinkIndex: 50},
		},
		{
			name:  "link-local gateway takes its zone from the address",
			route: osnet.Route{Dst: netip.MustParsePrefix("2001:db8:1::/48"), Gateway: netip.MustParseAddr("fe80::1").WithZone("en0")},
			want:  routeRequest{Flags: up | gateway, Dst: netip.MustParsePrefix("2001:db8:1::/48"), Gateway: netip.MustParseAddr("fe80::1"), GatewayScope: 14, Netmask: true},
		},
		{
			name:  "link-local gateway takes its zone from the interface",
			route: osnet.Route{Dst: netip.MustParsePrefix("2001:db8:1::/48"), Gateway: netip.MustParseAddr("fe80::1"), Iface: "en0"},
			want:  routeRequest{Flags: up | gateway, Dst: netip.MustParsePrefix("2001:db8:1::/48"), Gateway: netip.MustParseAddr("fe80::1"), GatewayScope: 14, Netmask: true},
		},
		{
			name:  "link-local destination carries the interface",
			route: osnet.Route{Dst: netip.MustParsePrefix("fe80::/64"), Iface: "en0", Scoped: true},
			want:  routeRequest{Flags: up | rtfIfScope, Index: 14, Dst: netip.MustParsePrefix("fe80::/64"), DstScope: 14, LinkIndex: 14, Netmask: true},
		},
		{
			name:  "blackhole",
			route: osnet.Route{Dst: netip.MustParsePrefix("198.51.100.0/24"), Blackhole: true},
			want:  routeRequest{Flags: up | gateway | rtfBlackhole, Dst: netip.MustParsePrefix("198.51.100.0/24"), Gateway: netip.MustParseAddr("127.0.0.1"), Netmask: true},
		},
		{
			name:  "blackhole host route",
			route: osnet.Route{Dst: netip.MustParsePrefix("198.51.100.7/32"), Blackhole: true},
			want:  routeRequest{Flags: up | gateway | rtfBlackhole | rtfHost, Dst: netip.MustParsePrefix("198.51.100.7/32"), Gateway: netip.MustParseAddr("127.0.0.1")},
		},
		{
			name:  "IPv6 blackhole",
			route: osnet.Route{Dst: netip.MustParsePrefix("2001:db8:1::/48"), Blackhole: true},
			want:  routeRequest{Flags: up | gateway | rtfBlackhole, Dst: netip.MustParsePrefix("2001:db8:1::/48"), Gateway: netip.IPv6Loopback(), Netmask: true},
		},
		// A route from Dump keeps its kernel flags; its form must be repeated to
		// delete it. A /32 network route is deleted with a mask, a host route without.
		{
			name:  "kernel network route of one address",
			route: osnet.Route{Dst: netip.MustParsePrefix("192.0.2.1/32"), Iface: "en0", Flags: 0x901},
			want:  routeRequest{Flags: up, Dst: netip.MustParsePrefix("192.0.2.1/32"), LinkIndex: 14, Netmask: true},
		},
		{
			name:  "kernel host route",
			route: osnet.Route{Dst: netip.MustParsePrefix("192.0.2.1/32"), Gateway: netip.MustParseAddr("192.0.2.254"), Flags: 0x40010806},
			want:  routeRequest{Flags: up | gateway | rtfHost, Dst: netip.MustParsePrefix("192.0.2.1/32"), Gateway: netip.MustParseAddr("192.0.2.254")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.want.Type = rtmAdd
			got, err := newRouteRequest(rtmAdd, tt.route, fixtureIfIndex)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestNewRouteRequestRejects(t *testing.T) {
	tests := map[string]osnet.Route{
		"no destination":            {Gateway: netip.MustParseAddr("192.0.2.1")},
		"no gateway and no iface":   routeOf("198.51.100.0/24", "", "", false),
		"scoped without iface":      routeOf("198.51.100.0/24", "192.0.2.1", "", true),
		"unknown interface":         routeOf("198.51.100.0/24", "", "utun99", false),
		"gateway of another family": routeOf("198.51.100.0/24", "2001:db8::1", "", false),
		"link-local without zone":   routeOf("2001:db8:1::/48", "fe80::1", "", false),
	}
	for name, r := range tests {
		t.Run(name, func(t *testing.T) {
			if req, err := newRouteRequest(rtmAdd, r, fixtureIfIndex); err == nil {
				t.Errorf("got %+v", req)
			}
		})
	}
}

// A route read from the kernel, written back and read again is the same route.
func TestRouteSurvivesBeingWrittenAndRead(t *testing.T) {
	for _, name := range []string{
		"v4-default-en0", "v4-default-scoped-utun10", "v4-net24-utun10", "v4-net8-lo0", "v4-net24-en0", "v4-net32-en0",
		"v4-host-local-utun10", "v4-multicast-scoped-utun10", "v4-broadcast-scoped-utun10",
		"v6-default-scoped-utun0", "v6-linklocal-net64-lo0", "v6-multicast8-gateway-utun0", "v6-multicast32-lo0", "v6-host-loopback",
	} {
		t.Run(name, func(t *testing.T) {
			orig, ok := routeFromMessage(fixtureMessage(t, ribMessages, name), fixtureIfaceName)
			if !ok {
				t.Fatal("fixture is not a route")
			}
			req, err := newRouteRequest(rtmAdd, orig, fixtureIfIndex)
			if err != nil {
				t.Fatal(err)
			}
			msgs, err := parseMessages(req.marshal())
			if err != nil || len(msgs) != 1 {
				t.Fatalf("parse: %d messages, %v", len(msgs), err)
			}
			got, ok := routeFromMessage(msgs[0], fixtureIfaceName)
			if !ok {
				t.Fatal("request is not a route")
			}
			// The interface of an unscoped route is not part of the request, and
			// the kernel adds the flags of the entry it creates.
			if !orig.Scoped {
				got.Iface = orig.Iface
			}
			got.Flags, got.Static = orig.Flags, orig.Static
			if got != orig {
				t.Errorf("got  %+v\nwant %+v", got, orig)
			}
		})
	}
}

func TestRouteError(t *testing.T) {
	dst := netip.MustParsePrefix("198.51.100.0/24")
	tests := []struct {
		name string
		err  error
		is   error
	}{
		{"exists", syscall.EEXIST, osnet.ErrExists},
		{"not in table", syscall.ESRCH, osnet.ErrNotFound},
		{"unreachable", syscall.ENETUNREACH, osnet.ErrUnreachable},
		{"wrapped like os.File does", &fs.PathError{Op: "write", Path: "route", Err: syscall.EEXIST}, osnet.ErrExists},
		{"other errors stay what they are", syscall.EPERM, syscall.EPERM},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := routeError("add", dst, tt.err)
			if !errors.Is(err, tt.is) {
				t.Errorf("%v is not %v", err, tt.is)
			}
			for _, other := range []error{osnet.ErrExists, osnet.ErrNotFound, osnet.ErrUnreachable} {
				if other != tt.is && errors.Is(err, other) {
					t.Errorf("%v is also %v", err, other)
				}
			}
		})
	}
}
