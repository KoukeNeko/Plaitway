package linux

import (
	"errors"
	"net/netip"
	"slices"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

func fixtureLink(t testing.TB, name string) link {
	t.Helper()
	m := fixtureMessage(t, linkMessages, name)
	if m.Link == nil {
		t.Fatalf("fixture %q is not a link", name)
	}
	return linkFromMessage(*m.Link)
}

func TestLinkFromRealMessages(t *testing.T) {
	tests := []struct {
		fixture string
		want    link
	}{
		{"lo", link{Interface: osnet.Interface{Name: "lo", Index: 1, Up: true}}},
		{"d0-dummy", link{Interface: osnet.Interface{Name: "d0", Index: 2, Up: true}, Uplink: true}},
		// Administratively up, but nothing holds the tun device open: no carrier.
		{"tun0-nocarrier", link{Interface: osnet.Interface{Name: "tun0", Index: 4, Tunnel: true}}},
		{"wg0", link{Interface: osnet.Interface{Name: "wg0", Index: 6, Up: true, Tunnel: true}}},
		// A bridge can carry the default route.
		{"br0-bridge", link{Interface: osnet.Interface{Name: "br0", Index: 7, Up: true}, Uplink: true}},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			if got := fixtureLink(t, tt.fixture); !sameLink(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func sameLink(a, b link) bool {
	return sameInterface(a.Interface, b.Interface) && a.Tunnel == b.Tunnel && a.Uplink == b.Uplink
}

func TestLinkClassification(t *testing.T) {
	const (
		ether    = 1
		running  = iffUp | iffLowerUp
		nonTun   = false
		isTunnel = true
	)
	tests := []struct {
		name   string
		kind   string
		typ    uint16
		flags  uint32
		tunnel bool
		uplink bool
	}{
		{"Ethernet", "", ether, running, nonTun, true},
		{"veth", "veth", ether, running, nonTun, true},
		{"bridge", "bridge", ether, running, nonTun, true},
		{"bond", "bond", ether, running, nonTun, true},
		{"VLAN", "vlan", ether, running, nonTun, true},
		{"macvlan", "macvlan", ether, running, nonTun, true},
		{"dummy", "dummy", ether, running, nonTun, true},
		{"docker0", "bridge", ether, iffUp, nonTun, true},
		{"loopback", "", arphrdLoopback, running | iffLoopback, nonTun, false},
		{"loopback by flag", "", ether, running | iffLoopback, nonTun, false},

		{"tun", "tun", arphrdNone, running, isTunnel, false},
		{"tap", "tun", ether, running, isTunnel, false},
		{"wireguard", "wireguard", arphrdNone, running, isTunnel, false},
		{"ipip", "ipip", arphrdTunnel, running, isTunnel, false},
		{"sit", "sit", arphrdSIT, running, isTunnel, false},
		{"gre", "gre", arphrdIPGRE, running, isTunnel, false},
		{"gretap", "gretap", ether, running, isTunnel, false},
		{"ip6gre", "ip6gre", 823, running, isTunnel, false},
		{"ip6gretap", "ip6gretap", ether, running, isTunnel, false},
		{"ip6tnl", "ip6tnl", arphrdTunnel6, running, isTunnel, false},
		{"vti", "vti", arphrdIPGRE, running, isTunnel, false},
		{"vti6", "vti6", arphrdTunnel6, running, isTunnel, false},
		{"geneve", "geneve", ether, running, isTunnel, false},
		{"erspan", "erspan", ether, running, isTunnel, false},
		{"ip6erspan", "ip6erspan", ether, running, isTunnel, false},
		{"xfrm", "xfrm", ether, running, isTunnel, false},
		{"bareudp", "bareudp", ether, running, isTunnel, false},
		{"gtp", "gtp", ether, running, isTunnel, false},
		{"ovpn", "ovpn", ether, running, isTunnel, false},
		{"vxlan", "vxlan", ether, running, isTunnel, false},
		{"a tunnel of a kind we do not list, by its hardware type", "", arphrdTunnel, running, isTunnel, false},

		// The Reconciler cannot expect the peer of these on a subnet of theirs, but
		// the default route may well lead through them.
		{"PPP", "", arphrdPPP, running, isTunnel, true},
		{"PPP device", "ppp", arphrdPPP, running, isTunnel, true},
		{"WWAN modem without a link layer", "", arphrdNone, running, isTunnel, true},

		{"administratively down", "", ether, 0, nonTun, true},
		{"no carrier", "", ether, iffUp, nonTun, true},
		{"carrier of an interface that is down", "", ether, iffLowerUp, nonTun, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := linkFromMessage(linkMsg{Name: "x0", Index: 3, Type: tt.typ, Flags: tt.flags, Kind: tt.kind})
			if got.Tunnel != tt.tunnel || got.Uplink != tt.uplink || got.Up != (tt.flags&running == running) {
				t.Errorf("got %+v, want tunnel %v, uplink %v, up %v", got, tt.tunnel, tt.uplink, tt.flags&running == running)
			}
		})
	}
}

func TestBuildLinks(t *testing.T) {
	var msgs []rtMessage
	for _, name := range []string{"v4-lo", "v4-d0", "v4-peer-tun0", "v6-global-d0", "v6-linklocal-d0"} {
		msgs = append(msgs, fixtureMessage(t, addrMessages, name))
	}
	// The dump lists links in the order of their index, but nothing depends on it.
	for _, name := range []string{"br0-bridge", "wg0", "tun0-nocarrier", "d0-dummy", "lo"} {
		msgs = append(msgs, fixtureMessage(t, linkMessages, name))
	}
	// An address of a link the first dump did not see, and one that is no prefix.
	msgs = append(msgs,
		rtMessage{Type: rtmNewAddr, Addr: &addrMsg{Family: afInet, PrefixLen: 24, Index: 99, Local: addr("198.51.100.2")}},
		rtMessage{Type: rtmNewAddr, Addr: &addrMsg{Family: afInet, PrefixLen: 64, Index: 2, Local: addr("198.51.100.3")}},
		rtMessage{Type: rtmNewAddr, Addr: &addrMsg{Family: afInet, PrefixLen: 24, Index: 2}},
	)

	got := buildLinks(msgs)
	wantNames := []string{"lo", "d0", "tun0", "wg0", "br0"}
	var names []string
	for _, l := range got {
		names = append(names, l.Name)
	}
	if !slices.Equal(names, wantNames) {
		t.Fatalf("links %v, want %v", names, wantNames)
	}
	wantAddrs := map[string][]netip.Prefix{
		"lo": {prefix("127.0.0.1/8")},
		// IPv4 before IPv6, as Addr.Compare sorts.
		"d0": {prefix("192.0.2.2/24"), prefix("2001:db8:1::2/64"), prefix("fe80::600a:94ff:fe5a:b3c1/64")},
		// The address of a point-to-point link is the local one, not the peer.
		"tun0": {prefix("10.8.0.2/32")},
	}
	for _, l := range got {
		if !slices.Equal(l.Addrs, wantAddrs[l.Name]) {
			t.Errorf("%s: addresses %v, want %v", l.Name, l.Addrs, wantAddrs[l.Name])
		}
	}
}

func TestAddrPrefix(t *testing.T) {
	tests := []struct {
		name string
		msg  addrMsg
		want string // "" for none
	}{
		{"IPv4", addrMsg{Family: afInet, PrefixLen: 24, Local: addr("192.0.2.2"), Address: addr("192.0.2.2")}, "192.0.2.2/24"},
		{"IPv4 peer", addrMsg{Family: afInet, PrefixLen: 32, Local: addr("10.8.0.2"), Address: addr("10.8.0.1")}, "10.8.0.2/32"},
		{"IPv6 has no local", addrMsg{Family: afInet6, PrefixLen: 64, Address: addr("2001:db8::2")}, "2001:db8::2/64"},
		{"neither", addrMsg{Family: afInet, PrefixLen: 24}, ""},
		{"prefix too long", addrMsg{Family: afInet, PrefixLen: 33, Local: addr("192.0.2.2")}, ""},
		{"IPv6 address in an IPv4 message", addrMsg{Family: afInet, PrefixLen: 24, Local: addr("2001:db8::2")}, ""},
		{"IPv4 address in an IPv6 message", addrMsg{Family: afInet6, PrefixLen: 24, Local: addr("192.0.2.2")}, ""},
		{"IPv4-mapped address is not unmapped to a prefix that does not fit", addrMsg{Family: afInet6, PrefixLen: 120, Address: addr("::ffff:192.0.2.2")}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.msg.prefix()
			if tt.want == "" {
				if ok {
					t.Errorf("got %v", got)
				}
				return
			}
			if !ok || got != prefix(tt.want) {
				t.Errorf("got %v, %v, want %s", got, ok, tt.want)
			}
		})
	}
}

func TestLinkCache(t *testing.T) {
	loads := 0
	cache := linkCache{load: func() ([]link, error) {
		loads++
		return []link{
			{Interface: osnet.Interface{Name: "d0", Index: 2}, Uplink: true},
			{Interface: osnet.Interface{Name: "tun0", Index: 4, Tunnel: true}},
		}, nil
	}}
	for range 3 {
		if l, ok, err := cache.lookup(2); l.Name != "d0" || !l.Uplink || !ok || err != nil {
			t.Fatalf("lookup(2) = %+v, %v, %v", l, ok, err)
		}
	}
	if loads != 1 {
		t.Errorf("loaded %d times for a known index, want 1", loads)
	}
	if l, ok, err := cache.lookup(4); l.Name != "tun0" || !ok || err != nil || loads != 1 {
		t.Errorf("lookup(4) = %+v, %v, %v after %d loads", l, ok, err, loads)
	}
	if _, ok, err := cache.lookup(99); ok || err != nil {
		t.Errorf("lookup(99) = %v, %v", ok, err)
	}
	if loads != 2 {
		t.Errorf("an unknown index was not looked for in the kernel's list")
	}

	failing := linkCache{load: func() ([]link, error) { return nil, errors.New("boom") }}
	if _, ok, err := failing.lookup(1); ok || err == nil {
		t.Errorf("lookup with failing load = %v, %v", ok, err)
	}

	// An interface that went away keeps its entry.
	gone := linkCache{
		links: map[uint32]link{7: {Interface: osnet.Interface{Name: "eth5", Index: 7}, Uplink: true}},
		load:  func() ([]link, error) { return nil, nil },
	}
	if l, ok, _ := gone.lookup(7); l.Name != "eth5" || !ok {
		t.Errorf("lookup of cached interface = %+v, %v", l, ok)
	}
}
