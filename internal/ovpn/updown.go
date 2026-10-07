package ovpn

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// maxPushedRoutes bounds how many route_network_N entries are read from the
// environment; the server is not trusted and this feeds the routing table.
const maxPushedRoutes = 1000

// upInfo is what openvpn reports about its tunnel in the UPDOWN environment.
// Everything in it came from the VPN server and is validated on the way in.
type upInfo struct {
	Iface      string
	Addresses  []netip.Prefix
	Routes     []netip.Prefix
	RedirectV4 bool
	RedirectV6 bool
	DNS        []netip.Addr
	Domains    []string
	TrustedIP  netip.Addr // the server openvpn is connected to
	// PulledDNS holds the "dns" options the server pushed. They are not in the
	// environment: the engine takes them from openvpn's log.
	PulledDNS dnsOptions
}

// parseUpEnv reads the environment of >UPDOWN:UP. Malformed entries are skipped
// and reported in the returned notes; a missing or invalid interface name is
// an error, because the Intent cannot name the tunnel without it.
func parseUpEnv(env map[string]string) (upInfo, []string, error) {
	var (
		info  upInfo
		notes []string
	)
	note := func(format string, args ...any) { notes = append(notes, fmt.Sprintf(format, args...)) }

	info.Iface = env["dev"]
	if !validDeviceName(info.Iface) {
		return upInfo{}, nil, fmt.Errorf("openvpn reported an invalid tunnel device %q", truncate(info.Iface, 40))
	}

	if local, err := netip.ParseAddr(env["ifconfig_local"]); err == nil && local.Is4() {
		ones := 32
		if mask, err := netip.ParseAddr(env["ifconfig_netmask"]); err == nil && mask.Is4() {
			if n, err := maskLength(mask); err == nil {
				ones = n
			}
		}
		info.Addresses = append(info.Addresses, netip.PrefixFrom(local, ones))
	}
	if local, err := netip.ParseAddr(env["ifconfig_ipv6_local"]); err == nil && local.Is6() {
		bitsN, err := strconv.Atoi(env["ifconfig_ipv6_netbits"])
		if err != nil || bitsN < 0 || bitsN > 128 {
			bitsN = 128
		}
		info.Addresses = append(info.Addresses, netip.PrefixFrom(local, bitsN))
	}

	netGateway := env["route_net_gateway"]
	for i := 1; i <= maxPushedRoutes; i++ {
		n := strconv.Itoa(i)
		network, ok := env["route_network_"+n]
		if !ok {
			break
		}
		if netGateway != "" && env["route_gateway_"+n] == netGateway {
			continue // a route around the tunnel, not a tunnel route
		}
		prefix, err := envRoute(network, env["route_netmask_"+n])
		if err != nil {
			note("route %d ignored: %v", i, err)
			continue
		}
		info.Routes = append(info.Routes, prefix)
	}
	for i := 1; i <= maxPushedRoutes; i++ {
		n := strconv.Itoa(i)
		network, ok := env["route_ipv6_network_"+n]
		if !ok {
			break
		}
		prefix, err := netip.ParsePrefix(network)
		if err != nil || !prefix.Addr().Is6() {
			note("IPv6 route %d ignored: %q is not an IPv6 prefix", i, truncate(network, 60))
			continue
		}
		info.Routes = append(info.Routes, prefix.Masked())
	}

	info.RedirectV4 = truthy(env["route_redirect_gateway_ipv4"])
	info.RedirectV6 = truthy(env["route_redirect_gateway_ipv6"])

	for i := 1; ; i++ {
		opt, ok := env["foreign_option_"+strconv.Itoa(i)]
		if !ok {
			break
		}
		fields := strings.Fields(opt)
		if len(fields) != 3 || fields[0] != "dhcp-option" {
			continue
		}
		switch fields[1] {
		case "DNS", "DNS6":
			addr, err := netip.ParseAddr(fields[2])
			if err != nil || addr.Zone() != "" || addr.IsUnspecified() || addr.IsLoopback() || addr.IsMulticast() {
				note("DNS server %q ignored", truncate(fields[2], 60))
				continue
			}
			info.DNS = appendUnique(info.DNS, addr)
		case "DOMAIN", "DOMAIN-SEARCH":
			domain, ok := cleanDomain(fields[2])
			if !ok {
				note("domain %q ignored", truncate(fields[2], 60))
				continue
			}
			info.Domains = appendUnique(info.Domains, domain)
		}
	}

	for _, key := range []string{"trusted_ip", "trusted_ip6"} {
		if addr, err := netip.ParseAddr(env[key]); err == nil && addr.Zone() == "" {
			info.TrustedIP = addr.Unmap()
			break
		}
	}
	return info, notes, nil
}

func envRoute(network, netmask string) (netip.Prefix, error) {
	addr, err := netip.ParseAddr(network)
	if err != nil || !addr.Is4() {
		return netip.Prefix{}, fmt.Errorf("%q is not an IPv4 network", truncate(network, 60))
	}
	ones := 32
	if netmask != "" {
		mask, err := netip.ParseAddr(netmask)
		if err != nil || !mask.Is4() {
			return netip.Prefix{}, fmt.Errorf("%q is not a netmask", truncate(netmask, 60))
		}
		if ones, err = maskLength(mask); err != nil {
			return netip.Prefix{}, err
		}
	}
	return netip.PrefixFrom(addr, ones).Masked(), nil
}

func truthy(s string) bool { return s != "" && s != "0" }

func appendUnique[T comparable](list []T, v T) []T {
	if slices.Contains(list, v) {
		return list
	}
	return append(list, v)
}

// catchAllDomain in MatchDomains means every name.
const catchAllDomain = "."

var (
	defaultRouteV4 = netip.MustParsePrefix("0.0.0.0/0")
	defaultRouteV6 = netip.MustParsePrefix("::/0")
)

// upIntent builds the replace-all statement for a tunnel that is up.
//
// Routes are the profile's own plus the pushed ones. Whether the tunnel is a
// full tunnel is the profile's or server's redirect-gateway unless the user
// chose a mode: ModeFull forces it, ModeSplit forces a split tunnel and removes
// any default route. A full tunnel asks for both address families, so IPv6
// cannot leak around a tunnel that only carries IPv4.
func upIntent(spec tunnel.Spec, prof *profile, up upInfo, endpoints []netip.Addr, upSince time.Time) tunnel.Intent {
	var routes []netip.Prefix
	redirect := up.RedirectV4 || up.RedirectV6 || prof.Summary.RedirectsDefaultRoute
	for _, p := range slices.Concat(prof.Summary.Routes, up.Routes) {
		if p.Bits() == 0 {
			redirect = true
			continue
		}
		routes = appendUnique(routes, p)
	}

	full := redirect
	switch spec.Mode {
	case tunnel.ModeFull:
		full = true
	case tunnel.ModeSplit:
		full = false
	}
	role := tunnel.RoleSplit
	if full {
		role = tunnel.RoleFull
		routes = append(routes, defaultRouteV4, defaultRouteV6)
	}

	intent := tunnel.Intent{
		Owner:     spec.Owner,
		State:     tunnel.StateUp,
		Iface:     up.Iface,
		Role:      role,
		Priority:  spec.Priority,
		UpSince:   upSince,
		Endpoints: endpoints,
		Routes:    routes,
	}
	if up.TrustedIP.IsValid() {
		intent.Endpoints = appendUnique(slices.Clone(endpoints), up.TrustedIP)
	}

	// Like openvpn, which ignores dhcp-option DNS and DOMAIN once there is a
	// dns server, the new options win over the old ones.
	if intent.DNS = prof.dns.withPulled(up.PulledDNS).intents(full); len(intent.DNS) > 0 {
		return intent
	}
	servers := slices.Clone(prof.Summary.DNSServers)
	for _, s := range up.DNS {
		servers = appendUnique(servers, s)
	}
	if len(servers) > 0 {
		domains := slices.Clone(prof.domains)
		for _, d := range up.Domains {
			domains = appendUnique(domains, d)
		}
		if full {
			domains = []string{catchAllDomain}
		}
		intent.DNS = []tunnel.DNSIntent{{Servers: servers, MatchDomains: domains}}
	}
	return intent
}

// connectingIntent keeps the endpoints reachable outside the tunnel while the
// tunnel is not up. It carries no routes.
func connectingIntent(spec tunnel.Spec, endpoints []netip.Addr) tunnel.Intent {
	return tunnel.Intent{
		Owner:     spec.Owner,
		State:     tunnel.StateConnecting,
		Priority:  spec.Priority,
		Endpoints: endpoints,
	}
}
