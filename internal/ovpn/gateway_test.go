package ovpn

import (
	"net/netip"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

func TestParseUpEnvGateways(t *testing.T) {
	tests := []struct {
		name          string
		env           string
		wantV4, want6 string
	}{
		{"topology subnet: the pushed route-gateway", "route_vpn_gateway=10.8.0.1\nifconfig_local=10.8.0.2\nifconfig_netmask=255.255.255.0", "10.8.0.1", ""},
		{"net30: the far end of the point-to-point address", "ifconfig_local=10.8.0.6\nifconfig_remote=10.8.0.5", "10.8.0.5", ""},
		{"route_vpn_gateway wins over the far end", "route_vpn_gateway=10.8.0.1\nifconfig_remote=10.8.0.5", "10.8.0.1", ""},
		{"nothing reported", "ifconfig_local=10.8.0.2", "", ""},
		{"IPv6 gateway of the first route", "route_ipv6_gateway_1=fd00:8::1\nifconfig_ipv6_local=fd00:8::2", "", "fd00:8::1"},
		{"IPv6 far end when no route names one", "ifconfig_ipv6_remote=fd00:8::1", "", "fd00:8::1"},
		{"openvpn writes :: where it has none", "route_ipv6_gateway_1=::\nifconfig_ipv6_remote=fd00:8::1", "", "fd00:8::1"},
		{"the unspecified IPv4 address is none", "route_vpn_gateway=0.0.0.0\nifconfig_remote=10.8.0.5", "10.8.0.5", ""},
		{"an address of the wrong family", "route_vpn_gateway=fd00:8::1\nroute_ipv6_gateway_1=10.8.0.1", "", ""},
		{"loopback and multicast are no next hop", "route_vpn_gateway=127.0.0.1\nifconfig_remote=224.0.0.1\nroute_ipv6_gateway_1=::1", "", ""},
		{"text that is no address", "route_vpn_gateway=vpn.example.net", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := envFrom("dev=tun0\n" + tt.env)

			up, _, err := parseUpEnv(env)
			if err != nil {
				t.Fatal(err)
			}

			if got := addrText(up.Gateway); got != tt.wantV4 {
				t.Errorf("Gateway = %q, want %q", got, tt.wantV4)
			}
			if got := addrText(up.GatewayV6); got != tt.want6 {
				t.Errorf("GatewayV6 = %q, want %q", got, tt.want6)
			}
		})
	}
}

func addrText(a netip.Addr) string {
	if !a.IsValid() {
		return ""
	}
	return a.String()
}

func TestParseUpEnvDropsTheZoneOfALinkLocalGateway(t *testing.T) {
	up, _, err := parseUpEnv(envFrom("dev=tun0\nroute_ipv6_gateway_1=fe80::1%tap0"))
	if err != nil {
		t.Fatal(err)
	}
	if got := addrText(up.GatewayV6); got != "fe80::1" {
		t.Errorf("GatewayV6 = %q, want fe80::1", got)
	}
}

// An intent carries the next hop of each family: an Ethernet-like adapter answers
// only for those, so a dual-stack tunnel needs both.
func TestUpIntentCarriesTheTunnelGateways(t *testing.T) {
	v4, v6 := netip.MustParseAddr("10.8.0.1"), netip.MustParseAddr("fd00:8::1")
	tests := []struct {
		name     string
		up       upInfo
		want, v6 netip.Addr
	}{
		{"IPv4 only", upInfo{Iface: "tap0", Gateway: v4}, v4, netip.Addr{}},
		{"both families", upInfo{Iface: "tap0", Gateway: v4, GatewayV6: v6}, v4, v6},
		{"IPv6 only", upInfo{Iface: "tap0", GatewayV6: v6}, netip.Addr{}, v6},
		{"none", upInfo{Iface: "tap0"}, netip.Addr{}, netip.Addr{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent := upIntent(tunnel.Spec{Owner: "o"}, mustProfile(t, ""), tt.up, nil, time.Time{})

			if intent.Gateway != tt.want || intent.GatewayV6 != tt.v6 {
				t.Errorf("Gateway, GatewayV6 = %v, %v; want %v, %v", intent.Gateway, intent.GatewayV6, tt.want, tt.v6)
			}
		})
	}
}
