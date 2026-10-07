package ovpn

import (
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// dnsOptions are the options of openvpn's "dns" syntax (2.6 and later):
//
//	dns search-domains domain...
//	dns server N address address...
//	dns server N resolve-domains domain...
//	dns server N dnssec|transport|sni value
//
// openvpn reports the old dhcp-option DNS and DOMAIN in the UPDOWN environment
// and nothing at all for these, so the profile's own options are read by
// Parse and the pushed ones from the reply openvpn logs. The zero value is
// empty.
type dnsOptions struct {
	search  []string
	servers map[int]*dnsServer
}

// dnsServer is one "dns server N". unusable marks a server whose settings a
// resolver entry cannot express (a transport other than plain DNS, a port other
// than 53, mandatory DNSSEC): using its address for plain DNS would be wrong,
// so it is skipped as openvpn does for its own DNS helper.
type dnsServer struct {
	addrs    []netip.Addr
	domains  []string // resolve-domains: empty means every name
	unusable bool
}

const (
	dnsPort          = 53
	minDNSServerNum  = -128
	maxDNSServerNum  = 127
	maxDNSServerAddr = 8
	// maxDNSDomains bounds each list of domains: the server that pushes them is
	// not trusted, and they end up in a resolver configuration.
	maxDNSDomains = 128
)

// add applies the arguments of one dns option, the words after "dns". It
// returns why the option has no effect, or only part of it.
func (o *dnsOptions) add(args []string) error {
	if len(args) < 2 {
		return errors.New("needs a type and a value")
	}
	switch args[0] {
	case "search-domains":
		for _, d := range args[1:] {
			domain, ok := cleanDomain(d)
			switch {
			case !ok:
				return fmt.Errorf("%q is not a valid domain", truncate(d, 60))
			case len(o.search) >= maxDNSDomains:
				return fmt.Errorf("more than %d search domains", maxDNSDomains)
			}
			o.search = appendUnique(o.search, domain)
		}
		return nil
	case "server":
		return o.addServerOption(args[1:])
	}
	return fmt.Errorf("unknown type %q", truncate(args[0], 40))
}

// addServerOption applies "N type value...".
func (o *dnsOptions) addServerOption(args []string) error {
	if len(args) < 3 {
		return errors.New("server needs a number, a type and a value")
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n < minDNSServerNum || n > maxDNSServerNum {
		return fmt.Errorf("%q is not a server number", truncate(args[0], 40))
	}
	if o.servers == nil {
		o.servers = map[int]*dnsServer{}
	}
	s := o.servers[n]
	if s == nil {
		s = &dnsServer{}
		o.servers[n] = s
	}

	values := args[2:]
	switch args[1] {
	case "address":
		for _, v := range values {
			addr, port, err := parseDNSServerAddr(v)
			switch {
			case err != nil:
				return fmt.Errorf("server %d: %w", n, err)
			case !usableDNSAddr(addr):
				return fmt.Errorf("server %d: address %s is not used", n, addr)
			case port != 0 && port != dnsPort:
				s.unusable = true
				return fmt.Errorf("server %d: port %d cannot be set in a resolver entry; the server is not used", n, port)
			case len(s.addrs) >= maxDNSServerAddr:
				return fmt.Errorf("server %d: more than %d addresses", n, maxDNSServerAddr)
			}
			s.addrs = appendUnique(s.addrs, addr)
		}
	case "resolve-domains":
		for _, d := range values {
			domain, ok := cleanDomain(d)
			switch {
			case !ok:
				return fmt.Errorf("server %d: %q is not a valid domain", n, truncate(d, 60))
			case len(s.domains) >= maxDNSDomains:
				return fmt.Errorf("server %d: more than %d domains", n, maxDNSDomains)
			}
			s.domains = appendUnique(s.domains, domain)
		}
	case "transport":
		if values[0] != "plain" {
			s.unusable = true
			return fmt.Errorf("server %d: transport %s is not supported; the server is not used", n, truncate(values[0], 20))
		}
	case "dnssec":
		if values[0] == "yes" {
			s.unusable = true
			return fmt.Errorf("server %d: required DNSSEC is not supported; the server is not used", n)
		}
	case "sni":
	default:
		return fmt.Errorf("server %d: unknown type %q", n, truncate(args[1], 40))
	}
	return nil
}

// parseDNSServerAddr reads "address", "address:port" or "[address]:port". port
// is 0 when none is given.
func parseDNSServerAddr(s string) (netip.Addr, uint16, error) {
	if addr, err := netip.ParseAddr(s); err == nil {
		return addr, 0, nil
	}
	ap, err := netip.ParseAddrPort(s)
	if err != nil {
		return netip.Addr{}, 0, fmt.Errorf("%q is not an address", truncate(s, 60))
	}
	return ap.Addr(), ap.Port(), nil
}

// usableDNSAddr: a server that is this host, nothing or a multicast group
// cannot answer for the tunnel.
func usableDNSAddr(a netip.Addr) bool {
	return a.Zone() == "" && !a.IsUnspecified() && !a.IsLoopback() && !a.IsMulticast()
}

// withPulled is o with what the server pushed on top: a pushed server replaces
// the profile's server of the same number, search domains add up.
func (o dnsOptions) withPulled(pulled dnsOptions) dnsOptions {
	out := dnsOptions{
		search:  slices.Clone(o.search),
		servers: maps.Clone(o.servers),
	}
	for _, d := range pulled.search {
		out.search = appendUnique(out.search, d)
	}
	if len(pulled.servers) > 0 && out.servers == nil {
		out.servers = map[int]*dnsServer{}
	}
	maps.Copy(out.servers, pulled.servers)
	return out
}

// usableServers lists the servers that can be configured, in the order of
// their numbers.
func (o dnsOptions) usableServers() []*dnsServer {
	var out []*dnsServer
	for _, n := range slices.Sorted(maps.Keys(o.servers)) {
		if s := o.servers[n]; !s.unusable && len(s.addrs) > 0 {
			out = append(out, s)
		}
	}
	return out
}

// intents turns the options into one DNSIntent per server. A server without
// resolve-domains answers every name: it gets the catch-all in a full tunnel
// and the search domains otherwise, as the dhcp-option DNS servers do.
func (o dnsOptions) intents(full bool) []tunnel.DNSIntent {
	var out []tunnel.DNSIntent
	for _, s := range o.usableServers() {
		domains := s.domains
		switch {
		case len(domains) > 0:
		case full:
			domains = []string{catchAllDomain}
		default:
			domains = o.search
		}
		out = append(out, tunnel.DNSIntent{Servers: slices.Clone(s.addrs), MatchDomains: slices.Clone(domains)})
	}
	return out
}

// pushReplyPrefix starts the line openvpn logs for the server's PUSH_REPLY
// (at verbosity 3 and above; the engine runs it at 4).
const pushReplyPrefix = "PUSH: Received control message: 'PUSH_REPLY,"

// pushReply is a PUSH_REPLY control message.
type pushReply struct {
	options []string
	// more is set when the server continues the reply in another message.
	more bool
}

// parsePushReply reads the log line of a PUSH_REPLY. ok is false for any other
// line.
func parsePushReply(logMsg string) (reply pushReply, ok bool) {
	body, ok := strings.CutPrefix(logMsg, pushReplyPrefix)
	if !ok {
		return pushReply{}, false
	}
	body = strings.TrimSuffix(body, "'")
	for _, opt := range strings.Split(body, ",") {
		if opt == "push-continuation 2" {
			reply.more = true
		}
		reply.options = append(reply.options, opt)
	}
	return reply, true
}
