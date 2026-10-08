package manager

import (
	"cmp"
	"fmt"
	"net/netip"
	"slices"

	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/profile"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

func kindToProto(k tunnel.Kind) pb.ProfileKind {
	switch k {
	case tunnel.KindOpenVPN:
		return pb.ProfileKind_PROFILE_KIND_OPENVPN
	case tunnel.KindWireGuard:
		return pb.ProfileKind_PROFILE_KIND_WIREGUARD
	}
	return pb.ProfileKind_PROFILE_KIND_UNSPECIFIED
}

// kindFromProto maps UNSPECIFIED to 0, which asks for detection.
func kindFromProto(k pb.ProfileKind) (tunnel.Kind, error) {
	switch k {
	case pb.ProfileKind_PROFILE_KIND_UNSPECIFIED:
		return 0, nil
	case pb.ProfileKind_PROFILE_KIND_OPENVPN:
		return tunnel.KindOpenVPN, nil
	case pb.ProfileKind_PROFILE_KIND_WIREGUARD:
		return tunnel.KindWireGuard, nil
	}
	return 0, &profile.InvalidError{Err: fmt.Errorf("unknown profile kind %d", k)}
}

func modeToProto(m tunnel.Mode) pb.TunnelMode {
	switch m {
	case tunnel.ModeFull:
		return pb.TunnelMode_TUNNEL_MODE_FULL
	case tunnel.ModeSplit:
		return pb.TunnelMode_TUNNEL_MODE_SPLIT
	}
	return pb.TunnelMode_TUNNEL_MODE_AUTO
}

// modeFromProto treats UNSPECIFIED as AUTO, the default.
func modeFromProto(m pb.TunnelMode) (tunnel.Mode, error) {
	switch m {
	case pb.TunnelMode_TUNNEL_MODE_UNSPECIFIED, pb.TunnelMode_TUNNEL_MODE_AUTO:
		return tunnel.ModeAuto, nil
	case pb.TunnelMode_TUNNEL_MODE_FULL:
		return tunnel.ModeFull, nil
	case pb.TunnelMode_TUNNEL_MODE_SPLIT:
		return tunnel.ModeSplit, nil
	}
	return 0, &profile.InvalidError{Err: fmt.Errorf("unknown tunnel mode %d", m)}
}

func settingsToProto(s profile.Settings) *pb.ProfileSettings {
	return &pb.ProfileSettings{
		AutoConnect:       s.AutoConnect,
		TunnelMode:        modeToProto(s.TunnelMode),
		Priority:          int32(s.Priority),
		ExcludePrivateIps: s.ExcludePrivateIPs,
		OnDemand:          &pb.OnDemandRules{Ethernet: s.OnDemand.Ethernet, Wifi: s.OnDemand.WiFi},
	}
}

func settingsFromProto(s *pb.ProfileSettings) (profile.Settings, error) {
	mode, err := modeFromProto(s.GetTunnelMode())
	if err != nil {
		return profile.Settings{}, err
	}
	return profile.Settings{
		AutoConnect:       s.GetAutoConnect(),
		TunnelMode:        mode,
		Priority:          int(s.GetPriority()),
		ExcludePrivateIPs: s.GetExcludePrivateIps(),
		OnDemand:          profile.OnDemand{Ethernet: s.GetOnDemand().GetEthernet(), WiFi: s.GetOnDemand().GetWifi()},
	}, nil
}

func stateToProto(s tunnel.State) pb.ProfileState {
	switch s {
	case tunnel.StateDisconnected:
		return pb.ProfileState_PROFILE_STATE_DISCONNECTED
	case tunnel.StateConnecting:
		return pb.ProfileState_PROFILE_STATE_CONNECTING
	case tunnel.StateAwaitingCredentials:
		return pb.ProfileState_PROFILE_STATE_AWAITING_CREDENTIALS
	case tunnel.StateUp:
		return pb.ProfileState_PROFILE_STATE_CONNECTED
	case tunnel.StateReconnecting:
		return pb.ProfileState_PROFILE_STATE_RECONNECTING
	case tunnel.StateDisconnecting:
		return pb.ProfileState_PROFILE_STATE_DISCONNECTING
	case tunnel.StateFailed:
		return pb.ProfileState_PROFILE_STATE_FAILED
	}
	return pb.ProfileState_PROFILE_STATE_UNSPECIFIED
}

func credentialKindToProto(k tunnel.CredentialKind) pb.CredentialKind {
	switch k {
	case tunnel.CredentialUserPassword:
		return pb.CredentialKind_CREDENTIAL_KIND_USER_PASSWORD
	case tunnel.CredentialKeyPassphrase:
		return pb.CredentialKind_CREDENTIAL_KIND_KEY_PASSPHRASE
	}
	return pb.CredentialKind_CREDENTIAL_KIND_UNSPECIFIED
}

func credentialKindFromProto(k pb.CredentialKind) tunnel.CredentialKind {
	switch k {
	case pb.CredentialKind_CREDENTIAL_KIND_USER_PASSWORD:
		return tunnel.CredentialUserPassword
	case pb.CredentialKind_CREDENTIAL_KIND_KEY_PASSPHRASE:
		return tunnel.CredentialKeyPassphrase
	}
	return tunnel.CredentialNone
}

func routeStateToProto(s tunnel.RouteState) pb.RouteState {
	switch s {
	case tunnel.RoutePending:
		return pb.RouteState_ROUTE_STATE_PENDING
	case tunnel.RouteInstalled:
		return pb.RouteState_ROUTE_STATE_INSTALLED
	case tunnel.RouteShadowed:
		return pb.RouteState_ROUTE_STATE_SHADOWED
	case tunnel.RouteBlocked:
		return pb.RouteState_ROUTE_STATE_BLOCKED
	case tunnel.RouteFailed:
		return pb.RouteState_ROUTE_STATE_FAILED
	}
	return pb.RouteState_ROUTE_STATE_UNSPECIFIED
}

func routeKindToProto(k tunnel.RouteKind) pb.RouteKind {
	switch k {
	case tunnel.RouteTunnel:
		return pb.RouteKind_ROUTE_KIND_TUNNEL
	case tunnel.RouteBypass:
		return pb.RouteKind_ROUTE_KIND_BYPASS
	case tunnel.RouteDefaultHalf:
		return pb.RouteKind_ROUTE_KIND_DEFAULT
	}
	return pb.RouteKind_ROUTE_KIND_UNSPECIFIED
}

func summaryToProto(s tunnel.Summary) *pb.ProfileSummary {
	out := &pb.ProfileSummary{
		RedirectsDefaultRoute: s.RedirectsDefaultRoute,
		RequiresCredentials:   s.RequiresCredentials,
		Routes:                prefixStrings(s.Routes),
		DnsServers:            addrStrings(s.DNSServers),
		Addresses:             prefixStrings(s.Addresses),
		PublicKey:             s.PublicKey,
	}
	// Hosts come from the profile text, which need not be valid UTF-8.
	for _, e := range s.Endpoints {
		out.Endpoints = append(out.Endpoints, &pb.Endpoint{Host: sanitizeText(e.Host, maxHostText), Port: uint32(e.Port), Protocol: e.Protocol})
	}
	return out
}

func prefixStrings(prefixes []netip.Prefix) []string {
	var out []string
	for _, p := range prefixes {
		out = append(out, p.String())
	}
	return out
}

func addrStrings(addrs []netip.Addr) []string {
	var out []string
	for _, a := range addrs {
		out = append(out, a.String())
	}
	return out
}

// tunnelStatusToProto adds what the Reconciler reports for the profile to what
// its engine reports about itself. Routes that only keep an endpoint reachable
// are not the profile's routes and stay out; Diagnostics lists them. A tunnel
// that is up but has none of its routes and DNS installed also says so in its
// warnings, because nothing else on the profile shows that it carries nothing.
// A route that another program's route outranks (RouteReport.Overridden) warns
// on its own, whatever else of the profile is installed: the traffic it was for
// goes through the other program.
func tunnelStatusToProto(st tunnel.Status, id string, report *tunnel.Report) *pb.TunnelStatus {
	out := &pb.TunnelStatus{
		InterfaceName: st.Iface,
		Addresses:     prefixStrings(st.Addresses),
		Remote:        st.Remote,
		RxBytes:       st.Stats.RxBytes,
		TxBytes:       st.Stats.TxBytes,
		Warnings:      slices.Clone(st.Warnings),
	}
	if !st.Since.IsZero() {
		out.ConnectedSince = timestamppb.New(st.Since)
	}
	owner := tunnel.OwnerID(id)
	var inTable []*pb.RouteStatus // the routes that count for notInstalledWarnings
	var overridden []tunnel.RouteReport
	for _, r := range report.Routes {
		if r.Owner != owner || r.Kind == tunnel.RouteBypass {
			continue
		}
		route := &pb.RouteStatus{
			Prefix:     r.Prefix.String(),
			State:      routeStateToProto(r.State),
			Detail:     r.Detail,
			ShadowedBy: string(r.ShadowedBy),
		}
		out.Routes = append(out.Routes, route)
		if r.Overridden {
			overridden = append(overridden, r)
		} else {
			inTable = append(inTable, route)
		}
	}
	for _, d := range report.DNS {
		if d.Owner == owner {
			out.Dns = append(out.Dns, &pb.DnsStatus{
				Servers:      addrStrings(d.Servers),
				MatchDomains: slices.Clone(d.MatchDomains),
				State:        routeStateToProto(d.State),
				Detail:       d.Detail,
			})
		}
	}
	// The route that keeps the server reachable is not the profile's route, but
	// when it cannot be installed the tunnel cannot connect (a stale route of
	// another program holding the server's address is the usual cause), and
	// this is the only place the profile says so.
	for _, r := range report.Routes {
		if r.Owner == owner && r.Kind == tunnel.RouteBypass {
			out.Warnings = append(out.Warnings, bypassWarnings(r)...)
		}
	}
	if st.State == tunnel.StateUp {
		out.Warnings = append(out.Warnings, notInstalledWarnings(inTable, out.Dns)...)
	}
	out.Warnings = append(out.Warnings, overriddenWarnings(overridden)...)
	return out
}

// bypassWarnings says what is wrong with the route that keeps the server
// reachable, or nothing when it is in use.
func bypassWarnings(r tunnel.RouteReport) []string {
	target := "Route to " + r.Prefix.Addr().String()
	switch {
	case r.Overridden:
		return []string{withReason(target+" not in use", r.Detail)}
	case isNotInstalled(routeStateToProto(r.State)):
		return []string{withReason(target+" not installed", r.Detail)}
	}
	return nil
}

// overriddenWarnings says that routes of the profile are in the table but not in
// use, with the reason of the first. The reason names the route of the other
// program.
func overriddenWarnings(overridden []tunnel.RouteReport) []string {
	if len(overridden) == 0 {
		return nil
	}
	return []string{withReason(routeCount(len(overridden), "not in use"), overridden[0].Detail)}
}

// routeCount is "1 route not installed" or "2 routes not installed".
func routeCount(n int, what string) string {
	if n == 1 {
		return "1 route " + what
	}
	return fmt.Sprintf("%d routes %s", n, what)
}

// notInstalledWarnings explains why nothing of what a tunnel asked for is in
// place: every route and DNS entry is blocked, failed or pending. It says
// nothing when something is installed, and nothing when a route is shadowed,
// which is the normal state of a tunnel that stands by for one of higher
// priority. The routes it is given are the ones that are not overridden: those
// are in place and have their own warning.
func notInstalledWarnings(routes []*pb.RouteStatus, dns []*pb.DnsStatus) []string {
	var routeReason, dnsReason string
	for _, r := range routes {
		if !isNotInstalled(r.State) {
			return nil
		}
		routeReason = cmp.Or(routeReason, r.Detail)
	}
	for _, d := range dns {
		if !isNotInstalled(d.State) {
			return nil
		}
		dnsReason = cmp.Or(dnsReason, d.Detail)
	}
	var out []string
	if n := len(routes); n > 0 {
		out = append(out, withReason(routeCount(n, "not installed"), routeReason))
	}
	if len(dns) > 0 {
		out = append(out, withReason("DNS not installed", dnsReason))
	}
	return out
}

func isNotInstalled(s pb.RouteState) bool {
	return s == pb.RouteState_ROUTE_STATE_PENDING || s == pb.RouteState_ROUTE_STATE_BLOCKED || s == pb.RouteState_ROUTE_STATE_FAILED
}

func withReason(text, reason string) string {
	if reason == "" {
		return text
	}
	return text + ": " + reason
}
