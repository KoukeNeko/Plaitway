package reconciler

import (
	"fmt"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// renderRoutes writes each route plan as one line, in plan order:
// "<kind> <prefix> <owner> <fate>[ via X][ by Owner][ (detail)]".
func renderRoutes(d Desired) []string {
	var out []string
	for _, p := range d.Routes {
		kind := map[tunnel.RouteKind]string{
			tunnel.RouteTunnel: "tunnel", tunnel.RouteBypass: "bypass", tunnel.RouteDefaultHalf: "default",
		}[p.Kind]
		fate := map[tunnel.RouteState]string{
			tunnel.RoutePending: "waiting", tunnel.RouteShadowed: "shadowed", tunnel.RouteBlocked: "blocked",
		}[p.State]
		if p.Install {
			fate = "install"
		}
		line := fmt.Sprintf("%s %s %s %s", kind, p.Route.Dst, p.Owner, fate)
		if p.Install {
			line += " via " + via(p.Route)
		}
		if p.ShadowedBy != "" {
			line += " by " + string(p.ShadowedBy)
		}
		if p.Detail != "" {
			line += " (" + p.Detail + ")"
		}
		out = append(out, line)
	}
	return out
}

// renderDNS writes each DNS plan as "<owner> <servers> [<domains>] <fate>...".
func renderDNS(d Desired) []string {
	var out []string
	for _, p := range d.DNS {
		var servers []string
		for _, s := range p.Servers {
			servers = append(servers, s.String())
		}
		fate := map[tunnel.RouteState]string{
			tunnel.RoutePending: "install", tunnel.RouteShadowed: "shadowed", tunnel.RouteBlocked: "blocked",
		}[p.State]
		line := fmt.Sprintf("%s %s [%s] %s", p.Owner, strings.Join(servers, ","), strings.Join(p.MatchDomains, ","), fate)
		if p.ShadowedBy != "" {
			line += " by " + string(p.ShadowedBy)
		}
		if p.Detail != "" {
			line += " (" + p.Detail + ")"
		}
		out = append(out, line)
	}
	return out
}

func checkLines(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s\n got:\n  %s\nwant:\n  %s", what, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestComputeRoutes(t *testing.T) {
	const wgEndpoint = "203.0.113.10"
	wgFull := func() tunnel.Intent { return up("wg", 1, "utun10", tunnel.RoleFull, "0.0.0.0/0") }
	asus := func() tunnel.Intent { return up("asus", 2, "utun11", tunnel.RoleSplit, "192.168.1.0/24") }

	tests := []struct {
		name    string
		net     func(*osnet.NetState)
		intents []tunnel.Intent
		want    []string
	}{
		{
			name: "nothing announced",
		},
		{
			name:    "a default route is split into halves and never installed whole",
			intents: []tunnel.Intent{wgFull()},
			want: []string{
				"default 0.0.0.0/1 wg install via utun10",
				"default 128.0.0.0/1 wg install via utun10",
			},
		},
		{
			name:    "the IPv6 default route is split too",
			intents: []tunnel.Intent{up("wg", 1, "utun10", tunnel.RoleFull, "::/0")},
			want: []string{
				"default ::/1 wg install via utun10",
				"default 8000::/1 wg install via utun10",
			},
		},
		{
			name:    "both families, tunnel routes before default halves",
			intents: []tunnel.Intent{up("wg", 1, "utun10", tunnel.RoleFull, "::/0", "0.0.0.0/0", "10.6.0.0/24")},
			want: []string{
				"tunnel 10.6.0.0/24 wg install via utun10",
				"default 0.0.0.0/1 wg install via utun10",
				"default 128.0.0.0/1 wg install via utun10",
				"default ::/1 wg install via utun10",
				"default 8000::/1 wg install via utun10",
			},
		},
		{
			name:    "prefixes are masked and duplicates dropped",
			intents: []tunnel.Intent{up("a", 1, "utun1", tunnel.RoleSplit, "10.1.2.3/8", "10.0.0.0/8")},
			want:    []string{"tunnel 10.0.0.0/8 a install via utun1"},
		},
		{
			name:    "explicit halves are a default route: refused for a split tunnel",
			intents: []tunnel.Intent{up("a", 1, "utun1", tunnel.RoleSplit, "0.0.0.0/1", "128.0.0.0/1", "10.0.0.0/8")},
			want:    []string{"tunnel 10.0.0.0/8 a install via utun1"},
		},
		{
			name:    "a split tunnel that names 0.0.0.0/0 only gets its other routes",
			intents: []tunnel.Intent{up("o", 1, "utun2", tunnel.RoleSplit, "0.0.0.0/0", "192.168.1.0/24")},
			want:    []string{"tunnel 192.168.1.0/24 o install via utun2"},
		},
		{
			name: "lower priority number wins an overlapping prefix",
			intents: []tunnel.Intent{
				up("a", 2, "utun1", tunnel.RoleSplit, "10.0.0.0/8"),
				up("b", 1, "utun2", tunnel.RoleSplit, "10.0.0.0/8"),
			},
			want: []string{
				"tunnel 10.0.0.0/8 b install via utun2",
				"tunnel 10.0.0.0/8 a shadowed by b",
			},
		},
		{
			name: "earlier UpSince breaks a priority tie",
			intents: []tunnel.Intent{
				upSince(up("a", 1, "utun1", tunnel.RoleSplit, "10.0.0.0/8"), 5*time.Second),
				upSince(up("b", 1, "utun2", tunnel.RoleSplit, "10.0.0.0/8"), 0),
			},
			want: []string{
				"tunnel 10.0.0.0/8 b install via utun2",
				"tunnel 10.0.0.0/8 a shadowed by b",
			},
		},
		{
			name: "owner breaks the last tie",
			intents: []tunnel.Intent{
				up("b", 1, "utun2", tunnel.RoleSplit, "10.0.0.0/8"),
				up("a", 1, "utun1", tunnel.RoleSplit, "10.0.0.0/8"),
			},
			want: []string{
				"tunnel 10.0.0.0/8 a install via utun1",
				"tunnel 10.0.0.0/8 b shadowed by a",
			},
		},
		{
			name: "different prefixes do not compete, the longest wins in the kernel",
			intents: []tunnel.Intent{
				up("a", 1, "utun1", tunnel.RoleSplit, "10.0.0.0/8"),
				up("b", 2, "utun2", tunnel.RoleSplit, "10.1.0.0/16"),
			},
			want: []string{
				"tunnel 10.0.0.0/8 a install via utun1",
				"tunnel 10.1.0.0/16 b install via utun2",
			},
		},
		{
			name: "only the best full tunnel gets the default routes, the other is on standby",
			intents: []tunnel.Intent{
				up("ovpn", 2, "utun11", tunnel.RoleFull, "0.0.0.0/0", "172.16.0.0/12"),
				wgFull(),
			},
			want: []string{
				"tunnel 172.16.0.0/12 ovpn install via utun11",
				"default 0.0.0.0/1 wg install via utun10",
				"default 0.0.0.0/1 ovpn shadowed by wg (standby)",
				"default 128.0.0.0/1 wg install via utun10",
				"default 128.0.0.0/1 ovpn shadowed by wg (standby)",
			},
		},
		{
			name: "the standby takes over when the holder is gone",
			intents: []tunnel.Intent{
				up("ovpn", 2, "utun11", tunnel.RoleFull, "0.0.0.0/0", "172.16.0.0/12"),
				state(wgFull(), tunnel.StateFailed),
			},
			want: []string{
				"tunnel 172.16.0.0/12 ovpn install via utun11",
				"default 0.0.0.0/1 ovpn install via utun11",
				"default 128.0.0.0/1 ovpn install via utun11",
			},
		},
		{
			name: "a split tunnel never holds the default route, whatever its priority",
			intents: []tunnel.Intent{
				up("split", 1, "utun1", tunnel.RoleSplit, "0.0.0.0/0", "10.0.0.0/8"),
				up("full", 9, "utun2", tunnel.RoleFull, "0.0.0.0/0"),
			},
			want: []string{
				"tunnel 10.0.0.0/8 split install via utun1",
				"default 0.0.0.0/1 full install via utun2",
				"default 128.0.0.0/1 full install via utun2",
			},
		},
		{
			name: "a full tunnel without a default route is not the holder",
			intents: []tunnel.Intent{
				up("a", 1, "utun1", tunnel.RoleFull, "10.0.0.0/8"),
				up("b", 2, "utun2", tunnel.RoleFull, "0.0.0.0/0"),
			},
			want: []string{
				"tunnel 10.0.0.0/8 a install via utun1",
				"default 0.0.0.0/1 b install via utun2",
				"default 128.0.0.0/1 b install via utun2",
			},
		},
		{
			name: "a prefix equal to or inside the local subnet is blocked, a bigger one is not",
			net: func(ns *osnet.NetState) {
				ns.Interfaces[0].Addrs = pfxs("192.168.1.50/24")
				ns.Connected = pfxs("192.168.1.0/24")
				ns.DefaultV4.Gateway = ip("192.168.1.1")
			},
			intents: []tunnel.Intent{up("asus", 2, "utun11", tunnel.RoleSplit,
				"192.168.1.0/24", "192.168.1.128/25", "192.168.0.0/16", "10.9.0.0/16")},
			want: []string{
				"tunnel 10.9.0.0/16 asus install via utun11",
				"tunnel 192.168.0.0/16 asus install via utun11",
				"tunnel 192.168.1.0/24 asus blocked (local subnet 192.168.1.0/24)",
				"tunnel 192.168.1.128/25 asus blocked (local subnet 192.168.1.0/24)",
			},
		},
		{
			name: "a blocked prefix does not shadow anybody",
			net:  func(ns *osnet.NetState) { ns.Connected = pfxs("192.168.1.0/24") },
			intents: []tunnel.Intent{
				up("a", 1, "utun1", tunnel.RoleSplit, "192.168.1.0/24"),
				up("b", 2, "utun2", tunnel.RoleSplit, "192.168.1.0/24"),
			},
			want: []string{
				"tunnel 192.168.1.0/24 a blocked (local subnet 192.168.1.0/24)",
				"tunnel 192.168.1.0/24 b blocked (local subnet 192.168.1.0/24)",
			},
		},
		{
			name: "IPv6 local subnets block too",
			net:  func(ns *osnet.NetState) { ns.Connected = pfxs("fd00::/64") },
			intents: []tunnel.Intent{up("a", 1, "utun1", tunnel.RoleSplit,
				"fd00::/64", "fd00::1:0/112", "fd00::/48")},
			want: []string{
				"tunnel fd00::/48 a install via utun1",
				"tunnel fd00::/64 a blocked (local subnet fd00::/64)",
				"tunnel fd00::1:0/112 a blocked (local subnet fd00::/64)",
			},
		},
		{
			name: "the owner's real case: WireGuard full tunnel and the ASUS split tunnel together",
			intents: []tunnel.Intent{
				endpoints(wgFull(), wgEndpoint),
				endpoints(asus(), "198.51.100.7"),
			},
			want: []string{
				"bypass 198.51.100.7/32 asus install via 192.168.51.1",
				"bypass 203.0.113.10/32 wg install via 192.168.51.1",
				"tunnel 192.168.1.0/24 asus install via utun11",
				"default 0.0.0.0/1 wg install via utun10",
				"default 128.0.0.0/1 wg install via utun10",
			},
		},

		{
			name: "special ranges are blocked, whatever the tunnel asks",
			intents: []tunnel.Intent{up("a", 1, "utun1", tunnel.RoleSplit,
				"127.0.0.1/32", "224.0.0.0/4", "169.254.0.0/16", "0.0.0.0/32", "fe80::/10", "10.0.0.0/8",
				"0.0.0.0/8", "127.0.0.0/8", "239.1.2.0/24", "::/128", "::1/128", "ff02::/16")},
			want: []string{
				"tunnel 0.0.0.0/8 a blocked (special address range)",
				"tunnel 0.0.0.0/32 a blocked (special address range)",
				"tunnel 10.0.0.0/8 a install via utun1",
				"tunnel 127.0.0.0/8 a blocked (special address range)",
				"tunnel 127.0.0.1/32 a blocked (special address range)",
				"tunnel 169.254.0.0/16 a blocked (special address range)",
				"tunnel 224.0.0.0/4 a blocked (special address range)",
				"tunnel 239.1.2.0/24 a blocked (special address range)",
				"tunnel ::/128 a blocked (special address range)",
				"tunnel ::1/128 a blocked (special address range)",
				"tunnel fe80::/10 a blocked (special address range)",
				"tunnel ff02::/16 a blocked (special address range)",
			},
		},
		{
			// 0.0.0.0/5 starts at the unspecified address and 224.0.0.0/3 at the
			// multicast range, but they cover ordinary addresses too: they are what
			// "0.0.0.0/0 minus the private ranges" is made of. The kernel's own
			// routes for loopback, link-local and multicast are more specific.
			name: "a prefix that only starts or ends in a special range is not special",
			intents: []tunnel.Intent{up("a", 1, "utun1", tunnel.RoleSplit,
				"0.0.0.0/5", "8.0.0.0/7", "224.0.0.0/3", "169.254.0.0/15", "::/3", "fe80::/9")},
			want: []string{
				"tunnel 0.0.0.0/5 a install via utun1",
				"tunnel 8.0.0.0/7 a install via utun1",
				"tunnel 169.254.0.0/15 a install via utun1",
				"tunnel 224.0.0.0/3 a install via utun1",
				"tunnel ::/3 a install via utun1",
				"tunnel fe80::/9 a install via utun1",
			},
		},

		// Bypass routes.
		{
			name:    "a split tunnel that captures nothing needs no bypass",
			intents: []tunnel.Intent{endpoints(asus(), "198.51.100.7")},
			want:    []string{"tunnel 192.168.1.0/24 asus install via utun11"},
		},
		{
			name:    "an endpoint on the local subnet needs no bypass",
			intents: []tunnel.Intent{endpoints(wgFull(), "192.168.51.1")},
			want: []string{
				"default 0.0.0.0/1 wg install via utun10",
				"default 128.0.0.0/1 wg install via utun10",
			},
		},
		{
			name:    "no network: the bypass waits, there is nothing to blackhole to",
			net:     func(ns *osnet.NetState) { ns.DefaultV4 = nil },
			intents: []tunnel.Intent{endpoints(wgFull(), wgEndpoint)},
			want: []string{
				"bypass 203.0.113.10/32 wg waiting (no network)",
				"default 0.0.0.0/1 wg install via utun10",
				"default 128.0.0.0/1 wg install via utun10",
			},
		},
		{
			name: "the bypass goes through the current default nexthop",
			net: func(ns *osnet.NetState) {
				ns.DefaultV4 = &osnet.Nexthop{Gateway: ip("10.20.30.1"), Iface: "en0"}
				ns.Connected = pfxs("10.20.30.0/24")
			},
			intents: []tunnel.Intent{endpoints(wgFull(), wgEndpoint)},
			want: []string{
				"bypass 203.0.113.10/32 wg install via 10.20.30.1",
				"default 0.0.0.0/1 wg install via utun10",
				"default 128.0.0.0/1 wg install via utun10",
			},
		},
		{
			name:    "an interface-bound default nexthop gives an interface-bound bypass",
			net:     func(ns *osnet.NetState) { ns.DefaultV4 = &osnet.Nexthop{Iface: "ppp0"} },
			intents: []tunnel.Intent{endpoints(wgFull(), wgEndpoint)},
			want: []string{
				"bypass 203.0.113.10/32 wg install via ppp0",
				"default 0.0.0.0/1 wg install via utun10",
				"default 128.0.0.0/1 wg install via utun10",
			},
		},
		{
			name: "an IPv6 endpoint uses the IPv6 default nexthop",
			net:  func(ns *osnet.NetState) { ns.DefaultV6 = &osnet.Nexthop{Gateway: ip("fe80::1"), Iface: "en0"} },
			intents: []tunnel.Intent{
				endpoints(up("wg", 1, "utun10", tunnel.RoleFull, "::/0"), "2001:db8::10"),
			},
			want: []string{
				"bypass 2001:db8::10/128 wg install via fe80::1",
				"default ::/1 wg install via utun10",
				"default 8000::/1 wg install via utun10",
			},
		},
		{
			name: "an IPv6 endpoint without an IPv6 default waits",
			intents: []tunnel.Intent{
				endpoints(up("wg", 1, "utun10", tunnel.RoleFull, "::/0"), "2001:db8::10"),
			},
			want: []string{
				"bypass 2001:db8::10/128 wg waiting (no network)",
				"default ::/1 wg install via utun10",
				"default 8000::/1 wg install via utun10",
			},
		},
		{
			name:    "an IPv4-mapped endpoint is an IPv4 endpoint",
			intents: []tunnel.Intent{endpoints(wgFull(), "::ffff:203.0.113.10")},
			want: []string{
				"bypass 203.0.113.10/32 wg install via 192.168.51.1",
				"default 0.0.0.0/1 wg install via utun10",
				"default 128.0.0.0/1 wg install via utun10",
			},
		},
		{
			name: "endpoints that cannot leave through the network get no bypass: a local proxy, link-local, unspecified",
			intents: []tunnel.Intent{
				endpoints(wgFull(), "127.0.0.1", "169.254.1.1", "0.0.0.0", "224.0.0.1", "::1", "10.5.5.5"),
			},
			want: []string{
				"bypass 10.5.5.5/32 wg install via 192.168.51.1",
				"default 0.0.0.0/1 wg install via utun10",
				"default 128.0.0.0/1 wg install via utun10",
			},
		},
		{
			name: "a connecting tunnel's endpoint is kept out of another tunnel's routes",
			intents: []tunnel.Intent{
				wgFull(),
				state(endpoints(asus(), "198.51.100.7"), tunnel.StateConnecting),
			},
			want: []string{
				"bypass 198.51.100.7/32 asus install via 192.168.51.1",
				"default 0.0.0.0/1 wg install via utun10",
				"default 128.0.0.0/1 wg install via utun10",
			},
		},
		{
			name:    "a connecting tunnel alone captures nothing and contributes only endpoints",
			intents: []tunnel.Intent{state(endpoints(wgFull(), wgEndpoint), tunnel.StateConnecting)},
		},
		{
			name: "a disconnecting tunnel's endpoint is not kept",
			intents: []tunnel.Intent{
				wgFull(),
				state(endpoints(asus(), "198.51.100.7"), tunnel.StateDisconnecting),
			},
			want: []string{
				"default 0.0.0.0/1 wg install via utun10",
				"default 128.0.0.0/1 wg install via utun10",
			},
		},
		{
			name: "an endpoint shared by two tunnels gets one bypass, owned by the better one",
			intents: []tunnel.Intent{
				endpoints(up("b", 2, "utun2", tunnel.RoleFull, "0.0.0.0/0"), wgEndpoint),
				endpoints(up("a", 1, "utun1", tunnel.RoleSplit, "10.0.0.0/8"), wgEndpoint),
			},
			want: []string{
				"bypass 203.0.113.10/32 a install via 192.168.51.1",
				"tunnel 10.0.0.0/8 a install via utun1",
				"default 0.0.0.0/1 b install via utun2",
				"default 128.0.0.0/1 b install via utun2",
			},
		},
		{
			name: "the bypass wins over a tunnel route naming the endpoint itself",
			intents: []tunnel.Intent{
				endpoints(wgFull(), wgEndpoint),
				up("b", 2, "utun2", tunnel.RoleSplit, wgEndpoint+"/32"),
			},
			want: []string{
				"bypass 203.0.113.10/32 wg install via 192.168.51.1",
				"tunnel 203.0.113.10/32 b shadowed by wg (tunnel endpoint)",
				"default 0.0.0.0/1 wg install via utun10",
				"default 128.0.0.0/1 wg install via utun10",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ns := homeNet()
			if tt.net != nil {
				tt.net(&ns)
			}
			checkLines(t, "routes", renderRoutes(Compute(tt.intents, ns)), tt.want)
		})
	}
}

// Only a tunnel that carries routes contributes them: up, and reconnecting
// because it keeps its interface and its routes while it reconnects.
func TestComputeStates(t *testing.T) {
	states := map[tunnel.State]bool{
		tunnel.StateDisconnected:        false,
		tunnel.StateConnecting:          false,
		tunnel.StateAwaitingCredentials: false,
		tunnel.StateUp:                  true,
		tunnel.StateReconnecting:        true,
		tunnel.StateDisconnecting:       false,
		tunnel.StateFailed:              false,
	}
	for s, carries := range states {
		t.Run(fmt.Sprint(s), func(t *testing.T) {
			in := nameserver(up("a", 1, "utun1", tunnel.RoleFull, "0.0.0.0/0", "10.0.0.0/8"), "10.0.0.53", ".")
			d := Compute([]tunnel.Intent{state(in, s)}, homeNet())
			if got := len(d.Routes) > 0; got != carries {
				t.Errorf("routes in state %d: got %v, want %v: %v", s, renderRoutes(d), carries, d.Routes)
			}
			if got := len(d.DNS) > 0; got != carries {
				t.Errorf("DNS in state %d: got %v, want %v", s, got, carries)
			}
		})
	}
}

func TestComputeDNS(t *testing.T) {
	wgFull := func() tunnel.Intent {
		return nameserver(up("wg", 1, "utun10", tunnel.RoleFull, "0.0.0.0/0"), "192.168.50.1", ".")
	}
	asus := func(domains ...string) tunnel.Intent {
		return nameserver(up("asus", 2, "utun11", tunnel.RoleSplit, "192.168.1.0/24"), "192.168.1.1", domains...)
	}
	lanOnEn0 := func(ns *osnet.NetState) {
		ns.Interfaces[0].Addrs = pfxs("192.168.1.50/24")
		ns.Connected = pfxs("192.168.1.0/24")
		ns.DefaultV4.Gateway = ip("192.168.1.1")
	}

	tests := []struct {
		name    string
		net     func(*osnet.NetState)
		intents []tunnel.Intent
		want    []string
	}{
		{
			name:    "the owner of the default routes gets the catch-all",
			intents: []tunnel.Intent{wgFull()},
			want:    []string{"wg 192.168.50.1 [.] install"},
		},
		{
			name:    "no match domains means the catch-all",
			intents: []tunnel.Intent{nameserver(up("wg", 1, "utun10", tunnel.RoleFull, "0.0.0.0/0"), "192.168.50.1")},
			want:    []string{"wg 192.168.50.1 [.] install"},
		},
		{
			name:    "a split tunnel does not get the catch-all",
			intents: []tunnel.Intent{asus(".")},
			want:    []string{"asus 192.168.1.1 [.] shadowed (catch-all is for the default route owner)"},
		},
		{
			name:    "split domains and the catch-all live side by side",
			intents: []tunnel.Intent{wgFull(), asus("asus.lan")},
			want: []string{
				"wg 192.168.50.1 [.] install",
				"asus 192.168.1.1 [asus.lan] install",
			},
		},
		{
			name:    "the nameserver address equals the local router's: unreachable, not installed",
			net:     lanOnEn0,
			intents: []tunnel.Intent{asus("asus.lan")},
			want:    []string{"asus 192.168.1.1 [asus.lan] blocked (192.168.1.1 is reached through en0)"},
		},
		{
			name: "the nameserver prefix is held by another tunnel: unreachable through this one",
			intents: []tunnel.Intent{
				up("wg", 1, "utun10", tunnel.RoleSplit, "192.168.1.0/24"),
				asus("asus.lan"),
			},
			want: []string{"asus 192.168.1.1 [asus.lan] blocked (192.168.1.1 is reached through utun10)"},
		},
		{
			name: "a nameserver inside the tunnel's own subnet is reachable",
			net: func(ns *osnet.NetState) {
				ns.Interfaces = append(ns.Interfaces, osnet.Interface{Name: "utun11", Up: true, Tunnel: true, Addrs: pfxs("10.8.0.6/24")})
			},
			intents: []tunnel.Intent{nameserver(up("asus", 2, "utun11", tunnel.RoleSplit, "192.168.1.0/24"), "10.8.0.1", "asus.lan")},
			want:    []string{"asus 10.8.0.1 [asus.lan] install"},
		},
		{
			name: "one winner per domain: the lower priority number",
			intents: []tunnel.Intent{
				nameserver(up("a", 2, "utun1", tunnel.RoleSplit, "10.0.0.0/8"), "10.0.0.53", "corp.lan"),
				nameserver(up("b", 1, "utun2", tunnel.RoleSplit, "10.1.0.0/16"), "10.1.0.53", "corp.lan"),
			},
			want: []string{
				"b 10.1.0.53 [corp.lan] install",
				"a 10.0.0.53 [corp.lan] shadowed by b",
			},
		},
		{
			name: "a loser keeps the domains it won",
			intents: []tunnel.Intent{
				nameserver(up("a", 2, "utun1", tunnel.RoleSplit, "10.0.0.0/8"), "10.0.0.53", "corp.lan", "other.lan"),
				nameserver(up("b", 1, "utun2", tunnel.RoleSplit, "10.1.0.0/16"), "10.1.0.53", "corp.lan"),
			},
			want: []string{
				"b 10.1.0.53 [corp.lan] install",
				"a 10.0.0.53 [other.lan] install (shadowed: corp.lan)",
			},
		},
		{
			name: "an unreachable nameserver does not take the domain from a working one",
			intents: []tunnel.Intent{
				nameserver(up("a", 2, "utun1", tunnel.RoleSplit, "10.0.0.0/8"), "10.0.0.53", "corp.lan"),
				nameserver(up("b", 1, "utun2", tunnel.RoleSplit, "10.1.0.0/16"), "10.9.9.9", "corp.lan"),
			},
			want: []string{
				"b 10.9.9.9 [corp.lan] blocked (10.9.9.9 is reached through utun1)",
				"a 10.0.0.53 [corp.lan] install",
			},
		},
		{
			name: "what is not a domain name is left out",
			intents: []tunnel.Intent{
				nameserver(up("a", 1, "utun1", tunnel.RoleSplit, "10.0.0.0/8"), "10.0.0.53", "good.lan", "bad domain", "x..y", "-no.lan", "x.lan\nsecond command"),
			},
			want: []string{"a 10.0.0.53 [good.lan] install"},
		},
		{
			name: "domains that are all unusable are not the catch-all",
			intents: []tunnel.Intent{
				nameserver(up("a", 1, "utun1", tunnel.RoleFull, "0.0.0.0/0"), "10.0.0.53", "bad domain"),
			},
			want: []string{"a 10.0.0.53 [] blocked (no valid domain)"},
		},
		{
			name: "domains are lowercased and lose the trailing dot",
			intents: []tunnel.Intent{
				nameserver(up("a", 1, "utun1", tunnel.RoleSplit, "10.0.0.0/8"), "10.0.0.53", "Corp.LAN.", "corp.lan"),
			},
			want: []string{"a 10.0.0.53 [corp.lan] install"},
		},
		{
			name: "a connecting tunnel has no DNS yet",
			intents: []tunnel.Intent{
				state(nameserver(up("a", 1, "utun1", tunnel.RoleSplit, "10.0.0.0/8"), "10.0.0.53", "corp.lan"), tunnel.StateConnecting),
			},
		},
		{
			// The resolver configuration refuses these; a full tunnel "reaches"
			// 0.0.0.0 and 224.0.0.1 through its 0/1 and 128/1, so only the
			// address itself can say no.
			name: "a nameserver that cannot be a resolver is not installed",
			intents: []tunnel.Intent{
				nameserver(up("a", 1, "utun1", tunnel.RoleFull, "0.0.0.0/0"), "0.0.0.0", "."),
				nameserver(up("b", 2, "utun2", tunnel.RoleSplit, "10.0.0.0/8"), "224.0.0.1", "b.lan"),
				nameserver(up("c", 3, "utun3", tunnel.RoleSplit, "10.1.0.0/16"), "10.1.0.53", "c.lan"),
			},
			want: []string{
				"a 0.0.0.0 [.] blocked (0.0.0.0 is not a usable nameserver)",
				"b 224.0.0.1 [b.lan] blocked (224.0.0.1 is not a usable nameserver)",
				"c 10.1.0.53 [c.lan] install",
			},
		},
		{
			name: "an unusable nameserver does not take a usable one down with it",
			intents: []tunnel.Intent{func() tunnel.Intent {
				in := up("a", 1, "utun1", tunnel.RoleSplit, "10.0.0.0/8")
				in.DNS = []tunnel.DNSIntent{{Servers: ips("0.0.0.0", "10.0.0.53"), MatchDomains: []string{"corp.lan"}}}
				return in
			}()},
			want: []string{"a 10.0.0.53 [corp.lan] install (0.0.0.0 is not a usable nameserver)"},
		},
		{
			name: "an entry without nameservers cannot be installed",
			intents: []tunnel.Intent{func() tunnel.Intent {
				in := up("a", 1, "utun1", tunnel.RoleSplit, "10.0.0.0/8")
				in.DNS = []tunnel.DNSIntent{{MatchDomains: []string{"corp.lan"}}}
				return in
			}()},
			want: []string{"a  [corp.lan] blocked (no nameserver)"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ns := homeNet()
			if tt.net != nil {
				tt.net(&ns)
			}
			checkLines(t, "dns", renderDNS(Compute(tt.intents, ns)), tt.want)
		})
	}
}

// The no-route case needs its own check: the default route is what makes most
// addresses reachable, and without any the lookup finds nothing.
func TestComputeDNSNoRoute(t *testing.T) {
	ns := homeNet()
	ns.DefaultV4 = nil
	in := nameserver(up("a", 1, "utun1", tunnel.RoleSplit, "10.0.0.0/8"), "172.16.0.53", "a.lan")
	checkLines(t, "dns", renderDNS(Compute([]tunnel.Intent{in}, ns)),
		[]string{"a 172.16.0.53 [a.lan] blocked (no route to 172.16.0.53)"})
}

func TestComputeIsDeterministic(t *testing.T) {
	intents := []tunnel.Intent{
		endpoints(nameserver(up("wg", 1, "utun10", tunnel.RoleFull, "0.0.0.0/0", "10.6.0.0/24"), "10.6.0.1", "."), "203.0.113.10"),
		endpoints(nameserver(up("asus", 2, "utun11", tunnel.RoleSplit, "192.168.1.0/24", "10.6.0.0/24"), "192.168.1.1", "asus.lan"), "198.51.100.7"),
		upSince(up("third", 2, "utun12", tunnel.RoleFull, "0.0.0.0/0", "10.6.0.0/24"), -time.Hour),
		up("fourth", 2, "utun13", tunnel.RoleSplit, "10.6.0.0/24", "172.16.0.0/12"),
	}
	want := Compute(intents, homeNet())
	permute(intents, func(p []tunnel.Intent) {
		if got := Compute(p, homeNet()); !reflect.DeepEqual(got, want) {
			t.Fatalf("order %v changes the result:\n got: %v\nwant: %v", owners(p), renderRoutes(got), renderRoutes(want))
		}
	})
}

func owners(intents []tunnel.Intent) []tunnel.OwnerID {
	var out []tunnel.OwnerID
	for _, in := range intents {
		out = append(out, in.Owner)
	}
	return out
}

func permute(in []tunnel.Intent, visit func([]tunnel.Intent)) {
	var rec func(k int)
	work := slices.Clone(in)
	rec = func(k int) {
		if k == len(work) {
			visit(slices.Clone(work))
			return
		}
		for i := k; i < len(work); i++ {
			work[k], work[i] = work[i], work[k]
			rec(k + 1)
			work[k], work[i] = work[i], work[k]
		}
	}
	rec(0)
}

func TestComputeDoesNotChangeItsInput(t *testing.T) {
	intents := []tunnel.Intent{
		endpoints(nameserver(up("wg", 1, "utun10", tunnel.RoleFull, "0.0.0.0/0", "10.6.0.1/8"), "10.6.0.1", "Corp."), "203.0.113.10"),
		up("asus", 2, "utun11", tunnel.RoleSplit, "192.168.1.0/24"),
	}
	ns := homeNet()
	wantIntents := cloneAll(intents)
	wantNet := cloneNetState(ns)
	Compute(intents, ns)
	if !reflect.DeepEqual(intents, wantIntents) || !reflect.DeepEqual(ns, wantNet) {
		t.Error("Compute modified its arguments")
	}
}

func cloneAll(in []tunnel.Intent) []tunnel.Intent {
	out := make([]tunnel.Intent, len(in))
	for i, x := range in {
		out[i] = cloneIntent(x)
	}
	return out
}

// allExcept lists the prefixes that cover p except for what the excluded ones cover, the way
// a full tunnel with the private ranges taken out announces it.
func allExcept(p netip.Prefix, excluded []netip.Prefix) []netip.Prefix {
	overlapping := false
	for _, e := range excluded {
		if e.Bits() <= p.Bits() && e.Contains(p.Addr()) {
			return nil
		}
		overlapping = overlapping || p.Overlaps(e)
	}
	if !overlapping {
		return []netip.Prefix{p}
	}
	low := netip.PrefixFrom(p.Addr(), p.Bits()+1)
	var highBytes [4]byte
	if p.Addr().Is4() {
		highBytes = p.Addr().As4()
		highBytes[p.Bits()/8] |= 0x80 >> (p.Bits() % 8)
	}
	high := netip.PrefixFrom(netip.AddrFrom4(highBytes), p.Bits()+1)
	return append(allExcept(low, excluded), allExcept(high, excluded)...)
}

var privateV4 = pfxs("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16")

func TestRedirectsAll(t *testing.T) {
	everythingButPrivate := allExcept(netip.MustParsePrefix("0.0.0.0/0"), privateV4)
	if len(everythingButPrivate) < 20 {
		t.Fatalf("the helper made %d prefixes: %v", len(everythingButPrivate), everythingButPrivate)
	}
	tests := []struct {
		name   string
		routes []netip.Prefix
		want   bool
	}{
		{"a default route", pfxs("0.0.0.0/0"), true},
		{"the halves of one", pfxs("0.0.0.0/1", "128.0.0.0/1"), true},
		{"an IPv6 default route", pfxs("::/0"), true},
		{"everything but the private ranges", everythingButPrivate, true},
		{"everything but the private ranges, with other routes", append(pfxs("10.8.0.0/24"), everythingButPrivate...), true},
		{"everything but the private ranges and a /8", allExcept(netip.MustParsePrefix("0.0.0.0/0"), append(pfxs("44.0.0.0/8"), privateV4...)), false},
		{"everything but the private ranges and a /24", allExcept(netip.MustParsePrefix("0.0.0.0/0"), append(pfxs("44.1.2.0/24"), privateV4...)), false},
		{"exactly the unicast space, in pieces", pfxs("1.0.0.0/8", "2.0.0.0/7", "4.0.0.0/6", "8.0.0.0/5", "16.0.0.0/4", "32.0.0.0/3", "64.0.0.0/2", "128.0.0.0/2", "192.0.0.0/3"), true},
		{"the unicast space without its first /8", pfxs("2.0.0.0/7", "4.0.0.0/6", "8.0.0.0/5", "16.0.0.0/4", "32.0.0.0/3", "64.0.0.0/2", "128.0.0.0/2", "192.0.0.0/3"), false},
		{"the unicast space without its last /8", pfxs("1.0.0.0/8", "2.0.0.0/7", "4.0.0.0/6", "8.0.0.0/5", "16.0.0.0/4", "32.0.0.0/3", "64.0.0.0/2", "128.0.0.0/2", "192.0.0.0/4", "208.0.0.0/5", "216.0.0.0/6", "220.0.0.0/7"), false},
		{"the IPv6 global unicast range", pfxs("2000::/3"), true},
		{"the IPv6 global unicast range, in pieces", pfxs("3000::/5", "2000::/4", "3800::/5"), true},
		{"half of the IPv6 global unicast range", pfxs("2000::/4"), false},
		{"a private network only", pfxs("10.0.0.0/8"), false},
		{"a public network only", pfxs("203.0.113.0/24"), false},
		{"no routes", nil, false},
		{"an invalid prefix", []netip.Prefix{{}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redirectsAll(tt.routes); got != tt.want {
				t.Errorf("redirectsAll = %v, want %v", got, tt.want)
			}
		})
	}
}

// A full tunnel that leaves the private ranges out has no default route, and is still the
// tunnel everything goes through: it gets the catch-all DNS, and a lower priority full tunnel
// stands by.
func TestAFullTunnelWithoutThePrivateRangesHoldsTheDefault(t *testing.T) {
	routes := allExcept(netip.MustParsePrefix("0.0.0.0/0"), privateV4)
	names := make([]string, len(routes))
	for i, p := range routes {
		names[i] = p.String()
	}
	wg := nameserver(up("wg", 1, "utun10", tunnel.RoleFull, names...), "1.1.1.1", ".")
	ovpn := nameserver(up("ovpn", 2, "utun11", tunnel.RoleFull, "0.0.0.0/0"), "9.9.9.9", ".")

	d := Compute([]tunnel.Intent{wg, ovpn}, homeNet())

	states := make(map[tunnel.OwnerID]tunnel.RouteState)
	for _, p := range d.DNS {
		states[p.Owner] = p.State
	}
	// Its nameserver is reached through wg, which holds those prefixes, so the lower tunnel's
	// catch-all is not installed either way.
	if states["wg"] != tunnel.RoutePending || states["ovpn"] == tunnel.RoutePending || len(states) != 2 {
		t.Errorf("DNS states = %v, want the catch-all for wg to install and none for ovpn", states)
	}
	for _, r := range d.Routes {
		if r.Owner == "ovpn" && r.Kind == tunnel.RouteDefaultHalf && r.State != tunnel.RouteShadowed {
			t.Errorf("a default half of the lower priority tunnel is %v, want standby: %v", r.State, r)
		}
	}
	installed := 0
	for _, r := range d.Routes {
		if r.Owner == "wg" && r.Install {
			installed++
		}
	}
	if installed < 20 {
		t.Errorf("only %d of the routes of the tunnel are installed", installed)
	}
}

// The pieces of a full tunnel that leaves the private ranges out are longer than the halves of
// the default route, so a tunnel that stands by must not install them: they would take the
// internet away from the holder.
func TestAFullTunnelWithoutThePrivateRangesStandsByWhenItIsNotTheHolder(t *testing.T) {
	routes := allExcept(netip.MustParsePrefix("0.0.0.0/0"), privateV4)
	names := make([]string, len(routes))
	for i, p := range routes {
		names[i] = p.String()
	}
	wg := func(priority int) tunnel.Intent {
		return up("wg", priority, "utun10", tunnel.RoleFull, append(slices.Clone(names), "10.6.0.1/32")...)
	}
	ovpn := func(priority int) tunnel.Intent {
		return up("ovpn", priority, "utun11", tunnel.RoleFull, "0.0.0.0/0", "172.16.0.0/12")
	}
	split := up("split", 3, "utun12", tunnel.RoleSplit, "192.168.9.0/24")

	installed := func(d Desired) map[tunnel.OwnerID]int {
		count := make(map[tunnel.OwnerID]int)
		for _, r := range d.Routes {
			if r.Install {
				count[r.Owner]++
			}
		}
		return count
	}

	t.Run("a higher priority full tunnel holds the default", func(t *testing.T) {
		got := installed(Compute([]tunnel.Intent{ovpn(1), wg(2), split}, homeNet()))
		if got["wg"] != 0 {
			t.Errorf("the standby tunnel installed %d routes", got["wg"])
		}
		// Its two halves and its own 172.16.0.0/12; the split tunnel keeps its prefix.
		if got["ovpn"] != 3 || got["split"] != 1 {
			t.Errorf("installed = %v, want ovpn 3 and split 1", got)
		}
	})
	t.Run("and the other way round", func(t *testing.T) {
		d := Compute([]tunnel.Intent{wg(1), ovpn(2), split}, homeNet())
		got := installed(d)
		if got["wg"] < 20 || got["split"] != 1 {
			t.Errorf("installed = %v, want the wg pieces and the split prefix", got)
		}
		// Only its own 172.16.0.0/12 is left of the lower tunnel, unless wg holds that too.
		for _, r := range d.Routes {
			if r.Owner == "ovpn" && r.Kind == tunnel.RouteDefaultHalf && r.Install {
				t.Errorf("a default half of the lower priority tunnel is installed: %v", r)
			}
		}
	})
	t.Run("without another tunnel it is the holder", func(t *testing.T) {
		if got := installed(Compute([]tunnel.Intent{wg(1)}, homeNet()))["wg"]; got < 20 {
			t.Errorf("the only full tunnel installed %d routes", got)
		}
	})
}
