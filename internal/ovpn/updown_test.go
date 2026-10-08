package ovpn

import (
	"fmt"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

func envFrom(dump string) map[string]string {
	env := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(dump), "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
		env[k] = v
	}
	return env
}

func addrs(ss ...string) []netip.Addr {
	var out []netip.Addr
	for _, s := range ss {
		out = append(out, netip.MustParseAddr(s))
	}
	return out
}

// asusUpEnv is the environment openvpn reports for the owner's ASUS profile:
// net30 topology, one route from the profile, nothing pushed (the profile
// filters redirect-gateway and dhcp-option).
const asusUpEnv = `
daemon=0
daemon_log_redirect=0
dev=utun11
dev_type=tun
ifconfig_local=10.8.0.6
ifconfig_remote=10.8.0.5
link_mtu=1559
proto_1=tcp-client
remote_1=vpn.example.net
remote_port_1=1194
route_gateway_1=10.8.0.5
route_net_gateway=192.168.51.1
route_netmask_1=255.255.255.0
route_network_1=192.168.1.0
route_vpn_gateway=10.8.0.5
script_context=init
script_type=route-up
tun_mtu=1500
trusted_ip=203.0.113.88
trusted_port=1194
untrusted_ip=203.0.113.88
untrusted_port=1194
verb=4
`

func TestParseUpEnvASUS(t *testing.T) {
	up, notes, err := parseUpEnv(envFrom(asusUpEnv))
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Errorf("notes = %v", notes)
	}
	want := upInfo{
		Iface:     "utun11",
		Addresses: []netip.Prefix{netip.MustParsePrefix("10.8.0.6/32")},
		Routes:    []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")},
		TrustedIP: netip.MustParseAddr("203.0.113.88"),
		Peer:      netip.MustParseAddr("10.8.0.5"),
		Gateway:   netip.MustParseAddr("10.8.0.5"),
		MTU:       1500,
	}
	if !reflect.DeepEqual(up, want) {
		t.Errorf("upInfo = %+v\nwant     %+v", up, want)
	}
}

func TestParseUpEnvPushedConfiguration(t *testing.T) {
	env := envFrom(`
dev=utun5
ifconfig_local=10.8.0.2
ifconfig_netmask=255.255.255.0
ifconfig_ipv6_local=fd00:8::2
ifconfig_ipv6_netbits=64
route_net_gateway=192.168.51.1
route_vpn_gateway=10.8.0.1
route_network_1=10.20.0.0
route_netmask_1=255.255.0.0
route_gateway_1=10.8.0.1
route_network_2=172.16.5.7
route_gateway_2=10.8.0.1
route_network_3=10.99.0.0
route_netmask_3=255.255.0.0
route_gateway_3=192.168.51.1
route_ipv6_network_1=2001:db8:5::/48
route_ipv6_gateway_1=fd00:8::1
route_redirect_gateway_ipv4=1
route_redirect_gateway_ipv6=1
foreign_option_1=dhcp-option DNS 10.20.0.1
foreign_option_2=dhcp-option DNS 10.20.0.2
foreign_option_3=dhcp-option DNS6 2001:db8:5::53
foreign_option_4=dhcp-option DOMAIN corp.example
foreign_option_5=dhcp-option DOMAIN-SEARCH Eng.Corp.Example.
foreign_option_6=dhcp-option NTP 10.20.0.9
foreign_option_7=dhcp-option DNS 10.20.0.1
trusted_ip6=2001:db8::10
`)
	up, notes, err := parseUpEnv(env)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Errorf("notes = %v", notes)
	}
	want := upInfo{
		Iface: "utun5",
		Addresses: []netip.Prefix{
			netip.MustParsePrefix("10.8.0.2/24"), netip.MustParsePrefix("fd00:8::2/64"),
		},
		Routes: []netip.Prefix{
			netip.MustParsePrefix("10.20.0.0/16"), netip.MustParsePrefix("172.16.5.7/32"),
			netip.MustParsePrefix("2001:db8:5::/48"),
		},
		RedirectV4: true,
		RedirectV6: true,
		DNS:        addrs("10.20.0.1", "10.20.0.2", "2001:db8:5::53"),
		Domains:    []string{"corp.example", "eng.corp.example"},
		TrustedIP:  netip.MustParseAddr("2001:db8::10"),
		Gateway:    netip.MustParseAddr("10.8.0.1"),
		GatewayV6:  netip.MustParseAddr("fd00:8::1"),
	}
	if !reflect.DeepEqual(up, want) {
		t.Errorf("upInfo = %+v\nwant     %+v", up, want)
	}
}

// The environment comes from the VPN server. Whatever is wrong with an entry,
// it is skipped and never reaches the routing table or the resolver.
func TestParseUpEnvDistrustsTheServer(t *testing.T) {
	env := envFrom(`
dev=utun5
route_network_1=not-an-ip
route_network_2=10.1.0.0
route_netmask_2=255.0.255.0
route_network_3=2001:db8::
route_network_4=10.4.0.0
route_netmask_4=255.255.0.0
route_ipv6_network_1=10.0.0.0/8
route_ipv6_network_2=garbage
foreign_option_1=dhcp-option DNS 127.0.0.1
foreign_option_2=dhcp-option DNS 0.0.0.0
foreign_option_3=dhcp-option DNS 224.0.0.1
foreign_option_4=dhcp-option DNS fe80::1%en0
foreign_option_5=dhcp-option DNS not-an-address
foreign_option_6=dhcp-option DOMAIN a/b
foreign_option_7=dhcp-option DOMAIN .
foreign_option_8=dhcp-option DOMAIN 10.0.0.1
foreign_option_9=dhcp-option DNS 10.0.0.53
foreign_option_10=route 10.0.0.0 255.0.0.0
trusted_ip=999.1.1.1
`)
	up, notes, err := parseUpEnv(env)
	if err != nil {
		t.Fatal(err)
	}
	if want := []netip.Prefix{netip.MustParsePrefix("10.4.0.0/16")}; !reflect.DeepEqual(up.Routes, want) {
		// Route 1 is not an address, so the walk skips it; entries after a bad
		// one are still read.
		t.Errorf("Routes = %v, want %v", up.Routes, want)
	}
	if want := addrs("10.0.0.53"); !reflect.DeepEqual(up.DNS, want) {
		t.Errorf("DNS = %v, want %v", up.DNS, want)
	}
	if len(up.Domains) != 0 {
		t.Errorf("Domains = %v, want none", up.Domains)
	}
	if up.TrustedIP.IsValid() {
		t.Errorf("TrustedIP = %v for an invalid address", up.TrustedIP)
	}
	if len(notes) < 8 {
		t.Errorf("notes = %v; every skipped entry should be reported", notes)
	}
}

func TestParseUpEnvRejectsBadDevice(t *testing.T) {
	for _, dev := range []string{"", "../etc", "utun5; rm -rf", "a b", "0tun", strings.Repeat("a", 40)} {
		if _, _, err := parseUpEnv(map[string]string{"dev": dev}); err == nil {
			t.Errorf("dev %q accepted", dev)
		}
	}
}

func TestParseUpEnvBoundsRoutes(t *testing.T) {
	env := map[string]string{"dev": "utun5"}
	for i := 1; i <= maxPushedRoutes+500; i++ {
		env["route_network_"+strconv.Itoa(i)] = fmt.Sprintf("10.%d.%d.0", i/256, i%256)
		env["route_netmask_"+strconv.Itoa(i)] = "255.255.255.0"
	}
	up, _, err := parseUpEnv(env)
	if err != nil {
		t.Fatal(err)
	}
	if len(up.Routes) != maxPushedRoutes {
		t.Errorf("read %d routes, want the bound %d", len(up.Routes), maxPushedRoutes)
	}
}

func mustProfile(t *testing.T, text string) *profile {
	t.Helper()
	p, err := parseProfile([]byte("client\nremote vpn.example.net 1194\n" + text))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUpIntent(t *testing.T) {
	since := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	endpoints := addrs("203.0.113.5")
	spec := func(mode tunnel.Mode) tunnel.Spec {
		return tunnel.Spec{Owner: "office", Mode: mode, Priority: 3}
	}
	split := upInfo{Iface: "utun11", Routes: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")}}
	redirect := upInfo{Iface: "utun5", RedirectV4: true}

	tests := []struct {
		name      string
		spec      tunnel.Spec
		profile   string
		up        upInfo
		role      tunnel.Role
		routes    []string
		dns       []tunnel.DNSIntent
		endpoints []string
	}{
		{
			name:      "split tunnel keeps pushed and profile routes",
			spec:      spec(tunnel.ModeAuto),
			profile:   "route 192.168.1.0 255.255.255.0\n",
			up:        split,
			role:      tunnel.RoleSplit,
			routes:    []string{"192.168.1.0/24", "10.20.0.0/16"},
			endpoints: []string{"203.0.113.5"},
		},
		{
			name:    "a route in both the profile and the environment is listed once",
			spec:    spec(tunnel.ModeAuto),
			profile: "route 10.20.0.0 255.255.0.0\n",
			up:      split,
			role:    tunnel.RoleSplit,
			routes:  []string{"10.20.0.0/16"},
		},
		{
			name:   "redirect-gateway becomes both default routes",
			spec:   spec(tunnel.ModeAuto),
			up:     redirect,
			role:   tunnel.RoleFull,
			routes: []string{"0.0.0.0/0", "::/0"},
		},
		{
			name:    "a redirect written in the profile is a full tunnel even without the 2.7 variable",
			spec:    spec(tunnel.ModeAuto),
			profile: "redirect-gateway def1\n",
			up:      upInfo{Iface: "utun5"},
			role:    tunnel.RoleFull,
			routes:  []string{"0.0.0.0/0", "::/0"},
		},
		{
			name:   "an explicit default route is a full tunnel",
			spec:   spec(tunnel.ModeAuto),
			up:     upInfo{Iface: "utun5", Routes: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}},
			role:   tunnel.RoleFull,
			routes: []string{"0.0.0.0/0", "::/0"},
		},
		{
			name:   "ModeFull forces a full tunnel on a split profile",
			spec:   spec(tunnel.ModeFull),
			up:     split,
			role:   tunnel.RoleFull,
			routes: []string{"10.20.0.0/16", "0.0.0.0/0", "::/0"},
		},
		{
			name:   "ModeSplit removes the default route of a redirecting profile",
			spec:   spec(tunnel.ModeSplit),
			up:     upInfo{Iface: "utun5", RedirectV4: true, RedirectV6: true, Routes: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16"), netip.MustParsePrefix("0.0.0.0/0")}},
			role:   tunnel.RoleSplit,
			routes: []string{"10.20.0.0/16"},
		},
		{
			name:    "ModeSplit also overrides a redirect in the profile",
			spec:    spec(tunnel.ModeSplit),
			profile: "redirect-gateway def1\nroute 10.1.0.0 255.255.0.0\n",
			up:      upInfo{Iface: "utun5"},
			role:    tunnel.RoleSplit,
			routes:  []string{"10.1.0.0/16"},
		},
		{
			name:    "split DNS matches the pushed and profile domains",
			spec:    spec(tunnel.ModeAuto),
			profile: "dhcp-option DNS 192.168.1.1\ndhcp-option DOMAIN lan.example\n",
			up:      upInfo{Iface: "utun11", DNS: addrs("10.20.0.1", "192.168.1.1"), Domains: []string{"corp.example", "lan.example"}},
			role:    tunnel.RoleSplit,
			dns: []tunnel.DNSIntent{{
				Servers:      addrs("192.168.1.1", "10.20.0.1"),
				MatchDomains: []string{"lan.example", "corp.example"},
			}},
		},
		{
			name:   "a full tunnel's DNS answers everything",
			spec:   spec(tunnel.ModeAuto),
			up:     upInfo{Iface: "utun5", RedirectV4: true, DNS: addrs("10.8.0.1"), Domains: []string{"corp.example"}},
			role:   tunnel.RoleFull,
			dns:    []tunnel.DNSIntent{{Servers: addrs("10.8.0.1"), MatchDomains: []string{"."}}},
			routes: []string{"0.0.0.0/0", "::/0"},
		},
		{
			name: "forced split drops the catch-all that the server meant for a full tunnel",
			spec: spec(tunnel.ModeSplit),
			up:   upInfo{Iface: "utun5", RedirectV4: true, DNS: addrs("10.8.0.1")},
			role: tunnel.RoleSplit,
			dns:  []tunnel.DNSIntent{{Servers: addrs("10.8.0.1")}},
		},
		{
			name:   "pushed dns options answer every name in a full tunnel",
			spec:   spec(tunnel.ModeAuto),
			up:     upInfo{Iface: "utun5", RedirectV4: true, PulledDNS: dnsFrom(t, "dns server 1 address 10.8.0.1\ndns search-domains corp.example")},
			role:   tunnel.RoleFull,
			dns:    []tunnel.DNSIntent{{Servers: addrs("10.8.0.1"), MatchDomains: []string{"."}}},
			routes: []string{"0.0.0.0/0", "::/0"},
		},
		{
			name: "pushed dns options answer their own domains in a split tunnel",
			spec: spec(tunnel.ModeAuto),
			up:   upInfo{Iface: "utun5", PulledDNS: dnsFrom(t, "dns server 1 address 10.8.0.1\ndns server 1 resolve-domains corp.example\ndns server 2 address 10.8.0.2\ndns search-domains lan.example")},
			role: tunnel.RoleSplit,
			dns: []tunnel.DNSIntent{
				{Servers: addrs("10.8.0.1"), MatchDomains: []string{"corp.example"}},
				{Servers: addrs("10.8.0.2"), MatchDomains: []string{"lan.example"}},
			},
		},
		{
			name:    "dns options in the profile",
			spec:    spec(tunnel.ModeAuto),
			profile: "dns server 1 address 192.168.1.1\ndns search-domains lan.example\n",
			up:      upInfo{Iface: "utun5"},
			role:    tunnel.RoleSplit,
			dns:     []tunnel.DNSIntent{{Servers: addrs("192.168.1.1"), MatchDomains: []string{"lan.example"}}},
		},
		{
			name:    "a pushed server replaces the profile's server of the same number",
			spec:    spec(tunnel.ModeAuto),
			profile: "dns server 1 address 192.168.1.1\ndns server 1 resolve-domains lan.example\n",
			up:      upInfo{Iface: "utun5", PulledDNS: dnsFrom(t, "dns server 1 address 10.8.0.1\ndns server 1 resolve-domains corp.example")},
			role:    tunnel.RoleSplit,
			dns:     []tunnel.DNSIntent{{Servers: addrs("10.8.0.1"), MatchDomains: []string{"corp.example"}}},
		},
		{
			name:    "the new dns options win over dhcp-option, as in openvpn",
			spec:    spec(tunnel.ModeAuto),
			profile: "dhcp-option DNS 192.168.1.1\n",
			up:      upInfo{Iface: "utun5", DNS: addrs("10.20.0.1"), PulledDNS: dnsFrom(t, "dns server 1 address 10.8.0.1")},
			role:    tunnel.RoleSplit,
			dns:     []tunnel.DNSIntent{{Servers: addrs("10.8.0.1")}},
		},
		{
			name: "dhcp-option stays in force when no dns server can be used",
			spec: spec(tunnel.ModeAuto),
			up: upInfo{Iface: "utun5", DNS: addrs("10.20.0.1"), Domains: []string{"corp.example"},
				PulledDNS: dnsOptions{servers: map[int]*dnsServer{1: {addrs: addrs("10.8.0.1"), unusable: true}}}},
			role: tunnel.RoleSplit,
			dns:  []tunnel.DNSIntent{{Servers: addrs("10.20.0.1"), MatchDomains: []string{"corp.example"}}},
		},
		{
			name:      "the server openvpn connected to is an endpoint too",
			spec:      spec(tunnel.ModeAuto),
			up:        upInfo{Iface: "utun11", TrustedIP: netip.MustParseAddr("198.51.100.9")},
			role:      tunnel.RoleSplit,
			endpoints: []string{"203.0.113.5", "198.51.100.9"},
		},
		{
			name:      "an endpoint that is already known is not repeated",
			spec:      spec(tunnel.ModeAuto),
			up:        upInfo{Iface: "utun11", TrustedIP: netip.MustParseAddr("203.0.113.5")},
			role:      tunnel.RoleSplit,
			endpoints: []string{"203.0.113.5"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := upIntent(tt.spec, mustProfile(t, tt.profile), tt.up, endpoints, since)
			wantEndpoints := tt.endpoints
			if wantEndpoints == nil {
				wantEndpoints = []string{"203.0.113.5"}
			}
			want := tunnel.Intent{
				Owner:     "office",
				State:     tunnel.StateUp,
				Iface:     tt.up.Iface,
				Role:      tt.role,
				Priority:  3,
				UpSince:   since,
				Endpoints: addrs(wantEndpoints...),
				Routes:    mustPrefixes(tt.routes...),
				DNS:       tt.dns,
			}
			if len(want.Routes) == 0 {
				want.Routes = nil
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("intent =\n%+v\nwant\n%+v", got, want)
			}
		})
	}
}

func TestUpIntentDoesNotShareSlicesWithItsInputs(t *testing.T) {
	endpoints := append(make([]netip.Addr, 0, 4), netip.MustParseAddr("203.0.113.5"))
	up := upInfo{Iface: "utun11", TrustedIP: netip.MustParseAddr("198.51.100.9")}
	intent := upIntent(tunnel.Spec{Owner: "o"}, mustProfile(t, ""), up, endpoints, time.Time{})
	if len(intent.Endpoints) != 2 {
		t.Fatalf("intent endpoints = %v", intent.Endpoints)
	}
	if spare := endpoints[:cap(endpoints)][1]; spare.IsValid() {
		t.Errorf("the intent wrote %v into the caller's backing array", spare)
	}
}

func TestConnectingIntentCarriesOnlyEndpoints(t *testing.T) {
	got := connectingIntent(tunnel.Spec{Owner: "o", Priority: 2, Mode: tunnel.ModeFull}, addrs("192.0.2.1"))
	want := tunnel.Intent{Owner: "o", State: tunnel.StateConnecting, Priority: 2, Endpoints: addrs("192.0.2.1")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("intent = %+v, want %+v", got, want)
	}
}

// real-transcript-pushed.txt was captured from openvpn 2.7.7 talking to a real
// server that pushed a route, an IPv6 route, redirect-gateway and DNS options
// (see TestRealBinaryPushedRedirectAndDNS). Replaying it keeps the parser
// honest about what openvpn really sends, in the order it sends it.
func TestRealTranscriptPushedConfiguration(t *testing.T) {
	transcript := strings.ReplaceAll(string(readFixture(t, "real-transcript-pushed.txt")), "\n", "\r\n")
	events, _ := collectEvents(t, transcript)

	var states []string
	var dump *upDownEvent
	var connectedAt int
	for i, ev := range events {
		switch ev := ev.(type) {
		case stateEvent:
			states = append(states, ev.Name)
			if ev.Name == "CONNECTED" {
				connectedAt = i
			}
		case upDownEvent:
			d := ev
			dump = &d
			if connectedAt != 0 {
				t.Error("the UPDOWN dump came after CONNECTED")
			}
		}
	}
	if want := []string{"TCP_CONNECT", "WAIT", "AUTH", "CONNECTED"}; !reflect.DeepEqual(states, want) {
		t.Errorf("states = %v, want %v", states, want)
	}
	if dump == nil || dump.Kind != "UP" {
		t.Fatalf("no UPDOWN:UP event in %+v", events)
	}
	if connected, ok := events[connectedAt].(stateEvent); !ok || connected.RemoteIP != "127.0.0.1" || connected.RemotePort != "1194" || connected.LocalIP != "10.8.0.2" {
		t.Errorf("CONNECTED = %+v", events[connectedAt])
	}

	up, notes, err := parseUpEnv(dump.Env)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Errorf("notes = %v", notes)
	}
	want := upInfo{
		Iface:      "null",
		Addresses:  mustPrefixes("10.8.0.2/24"),
		Routes:     mustPrefixes("192.168.1.0/24", "10.20.0.0/16", "2001:db8:5::/48"),
		RedirectV4: true,
		DNS:        addrs("10.8.0.1"),
		Domains:    []string{"corp.example"},
		TrustedIP:  netip.MustParseAddr("127.0.0.1"),
		Gateway:    netip.MustParseAddr("10.8.0.1"),
		MTU:        1500,
	}
	if !reflect.DeepEqual(up, want) {
		t.Errorf("upInfo = %+v\nwant     %+v", up, want)
	}

	intent := upIntent(tunnel.Spec{Owner: "o"}, mustProfile(t, "route 192.168.1.0 255.255.255.0\n"), up, addrs("127.0.0.1"), time.Time{})
	if intent.Role != tunnel.RoleFull || !reflect.DeepEqual(intent.Routes, mustPrefixes("192.168.1.0/24", "10.20.0.0/16", "2001:db8:5::/48", "0.0.0.0/0", "::/0")) {
		t.Errorf("intent = %+v", intent)
	}
	if want := []tunnel.DNSIntent{{Servers: addrs("10.8.0.1"), MatchDomains: []string{"."}}}; !reflect.DeepEqual(intent.DNS, want) {
		t.Errorf("DNS = %+v, want %+v", intent.DNS, want)
	}
}
