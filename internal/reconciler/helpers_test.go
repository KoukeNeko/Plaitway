package reconciler

import (
	"net/netip"
	"slices"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

var t0 = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func ip(s string) netip.Addr { return netip.MustParseAddr(s) }

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }

func pfxs(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = pfx(s)
	}
	return out
}

func ips(ss ...string) []netip.Addr {
	out := make([]netip.Addr, len(ss))
	for i, s := range ss {
		out[i] = ip(s)
	}
	return out
}

// homeNet is a typical physical network: en0 on 192.168.51.0/24 behind a router.
func homeNet() osnet.NetState {
	return osnet.NetState{
		Epoch:      1,
		Interfaces: []osnet.Interface{{Name: "en0", Up: true, Addrs: pfxs("192.168.51.185/24")}},
		DefaultV4:  &osnet.Nexthop{Gateway: ip("192.168.51.1"), Iface: "en0"},
		Connected:  pfxs("192.168.51.0/24"),
	}
}

// up is a tunnel that is up and asks for routes through iface.
func up(owner string, priority int, iface string, role tunnel.Role, routes ...string) tunnel.Intent {
	return tunnel.Intent{
		Owner:    tunnel.OwnerID(owner),
		State:    tunnel.StateUp,
		Iface:    iface,
		Role:     role,
		Priority: priority,
		UpSince:  t0,
		Routes:   pfxs(routes...),
	}
}

func state(in tunnel.Intent, s tunnel.State) tunnel.Intent {
	in.State = s
	return in
}

func endpoints(in tunnel.Intent, eps ...string) tunnel.Intent {
	in.Endpoints = ips(eps...)
	return in
}

func nameserver(in tunnel.Intent, server string, domains ...string) tunnel.Intent {
	in.DNS = append(slices.Clone(in.DNS), tunnel.DNSIntent{Servers: ips(server), MatchDomains: domains})
	return in
}

func upSince(in tunnel.Intent, d time.Duration) tunnel.Intent {
	in.UpSince = t0.Add(d)
	return in
}
