package manager

import (
	"net/netip"
	"slices"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

func TestTunnelThatIsUpButHasNothingInstalledWarns(t *testing.T) {
	route := func(prefix string, state tunnel.RouteState, detail string) tunnel.RouteReport {
		return tunnel.RouteReport{Prefix: netip.MustParsePrefix(prefix), Owner: "p", State: state, Detail: detail}
	}
	bypass := tunnel.RouteReport{Prefix: netip.MustParsePrefix("203.0.113.5/32"), Owner: "p", Kind: tunnel.RouteBypass, State: tunnel.RouteInstalled}
	dns := func(state tunnel.RouteState, detail string) tunnel.DNSReport {
		return tunnel.DNSReport{Owner: "p", Servers: []netip.Addr{netip.MustParseAddr("192.168.1.1")}, State: state, Detail: detail}
	}
	otherOwner := route("10.0.0.0/8", tunnel.RouteInstalled, "")
	otherOwner.Owner = "other"

	tests := []struct {
		name   string
		state  tunnel.State
		routes []tunnel.RouteReport
		dns    []tunnel.DNSReport
		want   []string
	}{
		{
			name:   "a route and DNS that are both blocked",
			state:  tunnel.StateUp,
			routes: []tunnel.RouteReport{route("192.168.1.0/24", tunnel.RouteBlocked, "local subnet 192.168.1.0/24")},
			dns:    []tunnel.DNSReport{dns(tunnel.RouteBlocked, "192.168.1.1 is reached through en0")},
			want:   []string{"1 route not installed: local subnet 192.168.1.0/24", "DNS not installed: 192.168.1.1 is reached through en0"},
		},
		{
			name:   "several routes, the reason of the first",
			state:  tunnel.StateUp,
			routes: []tunnel.RouteReport{route("10.1.0.0/16", tunnel.RouteFailed, "held by another program via en0"), route("10.2.0.0/16", tunnel.RoutePending, "network unreachable")},
			want:   []string{"2 routes not installed: held by another program via en0"},
		},
		{
			name:   "pending routes without a reason",
			state:  tunnel.StateUp,
			routes: []tunnel.RouteReport{route("10.1.0.0/16", tunnel.RoutePending, "")},
			want:   []string{"1 route not installed"},
		},
		{
			name:   "the endpoint's own route does not count as installed",
			state:  tunnel.StateUp,
			routes: []tunnel.RouteReport{bypass, route("192.168.1.0/24", tunnel.RouteBlocked, "local subnet 192.168.1.0/24")},
			want:   []string{"1 route not installed: local subnet 192.168.1.0/24"},
		},
		{
			name:   "another profile's routes do not count",
			state:  tunnel.StateUp,
			routes: []tunnel.RouteReport{otherOwner, route("192.168.1.0/24", tunnel.RouteBlocked, "local subnet 192.168.1.0/24")},
			want:   []string{"1 route not installed: local subnet 192.168.1.0/24"},
		},
		{
			name:   "one route installed",
			state:  tunnel.StateUp,
			routes: []tunnel.RouteReport{route("10.1.0.0/16", tunnel.RouteInstalled, ""), route("192.168.1.0/24", tunnel.RouteBlocked, "local subnet 192.168.1.0/24")},
		},
		{
			name:   "DNS installed while the routes are blocked",
			state:  tunnel.StateUp,
			routes: []tunnel.RouteReport{route("192.168.1.0/24", tunnel.RouteBlocked, "local subnet 192.168.1.0/24")},
			dns:    []tunnel.DNSReport{dns(tunnel.RouteInstalled, "")},
		},
		{
			// A standby tunnel is shadowed by design, not broken.
			name:   "routes held by a tunnel of higher priority",
			state:  tunnel.StateUp,
			routes: []tunnel.RouteReport{route("0.0.0.0/1", tunnel.RouteShadowed, "standby"), route("128.0.0.0/1", tunnel.RouteShadowed, "standby")},
		},
		{
			name:   "shadowed and blocked",
			state:  tunnel.StateUp,
			routes: []tunnel.RouteReport{route("10.1.0.0/16", tunnel.RouteShadowed, ""), route("192.168.1.0/24", tunnel.RouteBlocked, "local subnet 192.168.1.0/24")},
		},
		{
			name:  "nothing is wanted",
			state: tunnel.StateUp,
		},
		{
			name:   "not up yet",
			state:  tunnel.StateConnecting,
			routes: []tunnel.RouteReport{route("192.168.1.0/24", tunnel.RoutePending, "")},
		},
		{
			name:   "reconnecting keeps its routes",
			state:  tunnel.StateReconnecting,
			routes: []tunnel.RouteReport{route("192.168.1.0/24", tunnel.RouteBlocked, "local subnet 192.168.1.0/24")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := &tunnel.Report{Routes: tt.routes, DNS: tt.dns}
			got := tunnelStatusToProto(tunnel.Status{State: tt.state, Warnings: []string{"from the engine"}}, "p", report).Warnings
			want := slices.Concat([]string{"from the engine"}, tt.want)
			if !slices.Equal(got, want) {
				t.Fatalf("warnings = %q, want %q", got, want)
			}
		})
	}
}

// The owner's original failure: a stale route of another program holds the
// server's address, so the route that keeps the server reachable cannot be
// installed and the tunnel never connects. The profile has to say so.
func TestProfileSaysWhenTheRouteToItsServerCannotBeInstalled(t *testing.T) {
	bypass := func(state tunnel.RouteState, detail string) tunnel.RouteReport {
		return tunnel.RouteReport{Prefix: netip.MustParsePrefix("203.0.113.5/32"), Owner: "p", Kind: tunnel.RouteBypass, State: state, Detail: detail}
	}
	other := bypass(tunnel.RouteFailed, "held by another program")
	other.Owner = "other"
	tests := []struct {
		name   string
		state  tunnel.State
		routes []tunnel.RouteReport
		want   []string
	}{
		{"failed while connecting", tunnel.StateConnecting, []tunnel.RouteReport{bypass(tunnel.RouteFailed, "203.0.113.5/32 is held by a route through 10.1.1.1")}, []string{"Route to 203.0.113.5 not installed: 203.0.113.5/32 is held by a route through 10.1.1.1"}},
		{"pending", tunnel.StateConnecting, []tunnel.RouteReport{bypass(tunnel.RoutePending, "")}, []string{"Route to 203.0.113.5 not installed"}},
		{"installed", tunnel.StateConnecting, []tunnel.RouteReport{bypass(tunnel.RouteInstalled, "")}, nil},
		{"another profile's", tunnel.StateConnecting, []tunnel.RouteReport{other}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tunnelStatusToProto(tunnel.Status{State: tt.state}, "p", &tunnel.Report{Routes: tt.routes}).Warnings
			if !slices.Equal(got, tt.want) {
				t.Fatalf("warnings = %q, want %q", got, tt.want)
			}
		})
	}
}
