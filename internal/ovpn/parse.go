package ovpn

import (
	"fmt"
	"math/bits"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

const (
	defaultRemotePort  = 1194
	maxSuggestedName   = 64
	protocolUDP        = "udp"
	protocolTCP        = "tcp"
	redirectGatewayDir = "redirect-gateway"
)

// profile is a parsed profile: what tunnel.Parsed carries, plus what only this
// engine needs.
type profile struct {
	tunnel.Parsed
	// domains are the dhcp-option DOMAIN and DOMAIN-SEARCH names the profile
	// itself lists, lower-case.
	domains []string
	// proxyHosts are the http-proxy and socks-proxy servers. The tunnel's own
	// traffic goes to them, so they need the same bypass as an endpoint.
	proxyHosts []string
	// needsLZO is set when the profile asks for LZO compression, which the
	// openvpn binary must have been built with.
	needsLZO bool
	// dns holds the profile's own "dns" options.
	dns dnsOptions
	// filters are the profile's pull-filter lines: the options the server
	// pushes are subject to them.
	filters []pullFilter
}

// Parse validates an untrusted profile. It rejects the whole profile, with an
// error naming the line (*ParseError), when it refers to files the daemon
// would read; it removes directives that run programs or change how the daemon
// runs openvpn, and reports each removal as a warning. Content in the result is
// the profile written back in a canonical form that contains only what was
// validated, so storing and running it never depends on how this parser and
// openvpn's differ on odd input.
func Parse(content []byte) (tunnel.Parsed, error) {
	p, err := parseProfile(content)
	if err != nil {
		return tunnel.Parsed{}, err
	}
	return p.Parsed, nil
}

func parseProfile(content []byte) (*profile, error) {
	text, err := normalize(content)
	if err != nil {
		return nil, err
	}
	items, err := scan(text, 1, false)
	if err != nil {
		return nil, err
	}
	pr := &parser{
		seenEndpoints: map[tunnel.Endpoint]struct{}{},
		seenRoutes:    map[netip.Prefix]struct{}{},
		seenDNS:       map[netip.Addr]struct{}{},
		seenDomains:   map[string]struct{}{},
		seenProxies:   map[string]struct{}{},
	}
	pr.filters = pullFilters(items)
	kept, err := pr.process(items, scope{proto: protocolUDP, port: defaultRemotePort}, true)
	if err != nil {
		return nil, err
	}
	if len(pr.Summary.Endpoints) == 0 {
		return nil, errAt(0, "profile has no remote server")
	}
	for _, s := range pr.dns.usableServers() {
		for _, addr := range s.addrs {
			pr.addDNSServer(addr)
		}
	}
	pr.Summary.RedirectsDefaultRoute = pr.redirects()
	pr.Summary.RequiresCredentials = pr.authBare && !pr.authInline
	pr.Content = []byte(serialize(kept))
	return &pr.profile, nil
}

// scope carries the defaults that apply to remote lines: the proto and port
// directives of the profile or of the <connection> block they are in.
type scope struct {
	proto string
	port  uint16
}

type pullFilter struct{ action, text string }

type parser struct {
	profile
	redirectGW    [][]string
	authBare      bool
	authInline    bool
	seenEndpoints map[tunnel.Endpoint]struct{}
	seenRoutes    map[netip.Prefix]struct{}
	seenDNS       map[netip.Addr]struct{}
	seenDomains   map[string]struct{}
	seenProxies   map[string]struct{}
}

func (pr *parser) warn(line int, directive, message string) {
	pr.Warnings = append(pr.Warnings, tunnel.Warning{Line: line, Directive: directive, Message: message})
}

func (pr *parser) process(items []item, base scope, top bool) ([]item, error) {
	sc := scopeDefaults(items, base)
	var out []item
	for _, it := range items {
		switch it.kind {
		case kindComment:
			if top && pr.SuggestedName == "" {
				pr.SuggestedName = nameFromComment(it.text)
			}
			out = append(out, it)
		case kindBlock:
			if !has(allowedBlocks, it.name) {
				pr.warn(it.line, "<"+it.name+">", "removed: "+reasonUnknown)
				continue
			}
			if it.name == "auth-user-pass" {
				pr.authInline = true
			}
			out = append(out, it)
		case kindConnection:
			children, err := pr.process(it.children, sc, false)
			if err != nil {
				return nil, err
			}
			it.children = children
			out = append(out, it)
		case kindDirective:
			keep, err := pr.directive(it, sc)
			if err != nil {
				return nil, err
			}
			if keep {
				it.name = directiveName(it.name)
				out = append(out, it)
			}
		}
	}
	return out, nil
}

// scopeDefaults applies the proto and port directives found in items on top of
// base. Their arguments are validated later, when the directives are handled.
func scopeDefaults(items []item, base scope) scope {
	sc := base
	for _, it := range items {
		if it.kind != kindDirective || len(it.args) == 0 {
			continue
		}
		switch directiveName(it.name) {
		case "proto":
			if p, err := parseProtocol(it.args[0]); err == nil {
				sc.proto = p
			}
		case "port", "rport":
			if p, err := parsePort(it.args[0], false); err == nil {
				sc.port = p
			}
		}
	}
	return sc
}

func pullFilters(items []item) []pullFilter {
	var filters []pullFilter
	for _, it := range items {
		if it.kind == kindDirective && directiveName(it.name) == "pull-filter" && len(it.args) == 2 {
			filters = append(filters, pullFilter{action: it.args[0], text: it.args[1]})
		}
	}
	return filters
}

// directive decides one directive: kept (true), removed (false, with a
// warning) or the whole profile rejected (error).
func (pr *parser) directive(it item, sc scope) (bool, error) {
	name := directiveName(it.name)
	switch {
	case name == "":
		pr.warn(it.line, it.name, "removed: "+reasonUnknown)
		return false, nil
	case has(rejectedFileDirectives, name):
		if name == "dh" && len(it.args) == 1 && it.args[0] == "none" {
			return true, nil
		}
		return false, errAt(it.line, "%s refers to a file; put the content inline in <%s> ... </%s>", name, name, name)
	case has(rejectedWithArgument, name) && len(it.args) > 0:
		return false, errAt(it.line, "%s refers to a file; use it without an argument", name)
	}
	if reason := removalReason(name); reason != "" {
		pr.warn(it.line, name, "removed: "+reason)
		return false, nil
	}
	if !has(allowedDirectives, name) {
		pr.warn(it.line, name, "removed: "+reasonUnknown)
		return false, nil
	}
	if err := pr.check(it, name, sc); err != nil {
		return false, errAt(it.line, "%s: %v", name, err)
	}
	if n := len(directiveLine(item{name: name, args: it.args})); n > maxConfigLineBytes {
		return false, errAt(it.line, "%s: the line is %d bytes once written back; openvpn cannot read a line longer than %d bytes", name, n, maxConfigLineBytes)
	}
	return true, nil
}

// check validates the arguments of the directives the engine reads facts from
// or that could name a file, and records those facts.
func (pr *parser) check(it item, name string, sc scope) error {
	args := it.args
	switch name {
	case "remote":
		return pr.remote(args, sc)
	case "proto":
		if len(args) != 1 {
			return fmt.Errorf("needs one argument")
		}
		_, err := parseProtocol(args[0])
		return err
	case "port", "rport":
		if len(args) != 1 {
			return fmt.Errorf("needs one argument")
		}
		_, err := parsePort(args[0], false)
		return err
	case "lport":
		if len(args) != 1 {
			return fmt.Errorf("needs one argument")
		}
		_, err := parsePort(args[0], true)
		return err
	case "dev":
		if len(args) != 1 || !validDeviceName(args[0]) {
			return fmt.Errorf("needs a device name such as tun")
		}
		if strings.HasPrefix(args[0], "tap") {
			pr.warn(it.line, "dev", "kept: macOS has no tap device, so the profile cannot connect")
		}
	case "route":
		return pr.route(it)
	case "route-ipv6":
		return pr.routeIPv6(it)
	case redirectGatewayDir:
		pr.redirectGW = append(pr.redirectGW, args)
	case "auth-user-pass":
		pr.authBare = true
	case "dhcp-option":
		return pr.dhcpOption(args)
	case "dns":
		// openvpn itself refuses a malformed dns option; one it merely cannot
		// act on here is reported and the profile is kept.
		if err := pr.dns.add(args); err != nil {
			pr.warn(it.line, "dns", "not applied: "+err.Error())
		}
	case "ifconfig":
		return pr.ifconfig(args)
	case "ifconfig-ipv6":
		return pr.ifconfigIPv6(args)
	case "comp-lzo":
		pr.needsLZO = pr.needsLZO || len(args) == 0 || args[0] != "no"
	case "compress":
		pr.needsLZO = pr.needsLZO || (len(args) > 0 && args[0] == "lzo")
	case "pull-filter":
		if len(args) != 2 || (args[0] != "accept" && args[0] != "ignore" && args[0] != "reject") {
			return fmt.Errorf("needs accept, ignore or reject and the text to match")
		}
	case "http-proxy":
		return pr.httpProxy(args)
	case "socks-proxy":
		return pr.socksProxy(args)
	}
	return nil
}

func (pr *parser) remote(args []string, sc scope) error {
	if len(args) < 1 || len(args) > 3 {
		return fmt.Errorf("needs a host, optionally a port and a protocol")
	}
	if !validHost(args[0]) {
		return fmt.Errorf("%q is not a valid host name or address", args[0])
	}
	ep := tunnel.Endpoint{Host: args[0], Port: sc.port, Protocol: sc.proto}
	if len(args) > 1 {
		port, err := parsePort(args[1], false)
		if err != nil {
			return err
		}
		ep.Port = port
	}
	if len(args) > 2 {
		proto, err := parseProtocol(args[2])
		if err != nil {
			return err
		}
		ep.Protocol = proto
	}
	if _, dup := pr.seenEndpoints[ep]; !dup {
		pr.seenEndpoints[ep] = struct{}{}
		pr.Summary.Endpoints = append(pr.Summary.Endpoints, ep)
	}
	return nil
}

func (pr *parser) addRoute(p netip.Prefix) {
	p = p.Masked()
	if _, dup := pr.seenRoutes[p]; !dup {
		pr.seenRoutes[p] = struct{}{}
		pr.Summary.Routes = append(pr.Summary.Routes, p)
	}
}

// outsideTunnel reports whether a route's gateway keyword sends the route
// around the tunnel.
func outsideTunnel(gateway string) bool {
	return gateway == "net_gateway" || gateway == "remote_host"
}

func (pr *parser) route(it item) error {
	args := it.args
	if len(args) < 1 || len(args) > 4 {
		return fmt.Errorf("needs a network, optionally a netmask, a gateway and a metric")
	}
	network, err := netip.ParseAddr(args[0])
	if err != nil {
		pr.warn(it.line, "route", "not listed: "+args[0]+" is not an IP address")
		return nil
	}
	if !network.Is4() {
		return fmt.Errorf("%s is not an IPv4 address; use route-ipv6", args[0])
	}
	ones := 32
	if len(args) > 1 && args[1] != "default" {
		mask, err := netip.ParseAddr(args[1])
		if err != nil || !mask.Is4() {
			return fmt.Errorf("%q is not a netmask", args[1])
		}
		if ones, err = maskLength(mask); err != nil {
			return err
		}
	}
	if len(args) > 2 && outsideTunnel(args[2]) {
		pr.warn(it.line, "route", "not applied: it goes around the tunnel")
		return nil
	}
	pr.addRoute(netip.PrefixFrom(network, ones))
	return nil
}

func (pr *parser) routeIPv6(it item) error {
	args := it.args
	if len(args) < 1 || len(args) > 3 {
		return fmt.Errorf("needs a prefix, optionally a gateway and a metric")
	}
	prefix, err := netip.ParsePrefix(args[0])
	if err != nil || !prefix.Addr().Is6() {
		return fmt.Errorf("%q is not an IPv6 prefix", args[0])
	}
	if len(args) > 1 && outsideTunnel(args[1]) {
		pr.warn(it.line, "route-ipv6", "not applied: it goes around the tunnel")
		return nil
	}
	pr.addRoute(prefix)
	return nil
}

func (pr *parser) addDNSServer(addr netip.Addr) {
	if _, dup := pr.seenDNS[addr]; !dup {
		pr.seenDNS[addr] = struct{}{}
		pr.Summary.DNSServers = append(pr.Summary.DNSServers, addr)
	}
}

func (pr *parser) dhcpOption(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("needs an option type")
	}
	switch args[0] {
	case "DNS", "DNS6":
		if len(args) != 2 {
			return fmt.Errorf("%s needs an address", args[0])
		}
		addr, err := netip.ParseAddr(args[1])
		if err != nil || addr.Zone() != "" {
			return fmt.Errorf("%q is not an IP address", args[1])
		}
		pr.addDNSServer(addr)
	case "DOMAIN", "DOMAIN-SEARCH":
		if len(args) != 2 {
			return fmt.Errorf("%s needs a domain", args[0])
		}
		domain, ok := cleanDomain(args[1])
		if !ok {
			return fmt.Errorf("%q is not a valid domain", args[1])
		}
		if _, dup := pr.seenDomains[domain]; !dup {
			pr.seenDomains[domain] = struct{}{}
			pr.profile.domains = append(pr.profile.domains, domain)
		}
	}
	return nil
}

func (pr *parser) ifconfig(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("needs a local address and a remote address or netmask")
	}
	local, err := netip.ParseAddr(args[0])
	if err != nil || !local.Is4() {
		return fmt.Errorf("%q is not an IPv4 address", args[0])
	}
	ones := 32
	if mask, err := netip.ParseAddr(args[1]); err == nil && mask.Is4() {
		if n, err := maskLength(mask); err == nil {
			ones = n
		}
	}
	pr.Summary.Addresses = append(pr.Summary.Addresses, netip.PrefixFrom(local, ones))
	return nil
}

func (pr *parser) ifconfigIPv6(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("needs an address with its prefix length and a remote address")
	}
	prefix, err := netip.ParsePrefix(args[0])
	if err != nil || !prefix.Addr().Is6() {
		return fmt.Errorf("%q is not an IPv6 address with a prefix length", args[0])
	}
	pr.Summary.Addresses = append(pr.Summary.Addresses, prefix)
	return nil
}

// httpProxy: http-proxy server [port [authfile|auto|auto-nct [method]]]. Only
// "auto" and "auto-nct" avoid reading a credentials file.
func (pr *parser) httpProxy(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("needs a server")
	}
	if len(args) >= 3 && args[2] != "auto" && args[2] != "auto-nct" {
		return fmt.Errorf("refers to a credentials file; use it without one")
	}
	return pr.addProxy(args[0])
}

// socksProxy: socks-proxy server [port] [authfile].
func (pr *parser) socksProxy(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("needs a server")
	}
	if len(args) >= 3 {
		return fmt.Errorf("refers to a credentials file; use it without one")
	}
	if len(args) == 2 {
		if _, err := parsePort(args[1], false); err != nil {
			return fmt.Errorf("refers to a credentials file; use it without one")
		}
	}
	return pr.addProxy(args[0])
}

func (pr *parser) addProxy(host string) error {
	if !validHost(host) {
		return fmt.Errorf("%q is not a valid host name or address", host)
	}
	if _, dup := pr.seenProxies[host]; !dup {
		pr.seenProxies[host] = struct{}{}
		pr.proxyHosts = append(pr.proxyHosts, host)
	}
	return nil
}

// redirects reports whether the profile itself sends the default route
// through the tunnel. A pull-filter that ignores the directive's text takes
// it back, as the owner's own profiles use it.
func (pr *parser) redirects() bool {
	for _, args := range pr.redirectGW {
		line := strings.Join(append([]string{redirectGatewayDir}, args...), " ")
		if pr.filtered(line) {
			continue
		}
		ipv4, ipv6 := true, false
		for _, flag := range args {
			switch flag {
			case "!ipv4":
				ipv4 = false
			case "ipv6":
				ipv6 = true
			}
		}
		if ipv4 || ipv6 {
			return true
		}
	}
	return false
}

// filtered reports whether the first pull-filter that matches option drops it.
func (p *profile) filtered(option string) bool {
	for _, f := range p.filters {
		if strings.HasPrefix(option, f.text) {
			return f.action != "accept"
		}
	}
	return false
}

func parseProtocol(s string) (string, error) {
	switch s {
	case "udp", "udp4", "udp6":
		return protocolUDP, nil
	case "tcp", "tcp4", "tcp6", "tcp-client", "tcp4-client", "tcp6-client":
		return protocolTCP, nil
	}
	return "", fmt.Errorf("%q is not a client protocol (udp or tcp-client)", s)
}

func parsePort(s string, allowZero bool) (uint16, error) {
	n, err := strconv.ParseUint(s, 10, 16)
	if err != nil || (n == 0 && !allowZero) {
		return 0, fmt.Errorf("%q is not a port number", s)
	}
	return uint16(n), nil
}

// maskLength returns the prefix length of a contiguous IPv4 netmask.
func maskLength(mask netip.Addr) (int, error) {
	b := mask.As4()
	m := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	ones := bits.OnesCount32(m)
	if ones > 0 && m != ^uint32(0)<<(32-ones) {
		return 0, fmt.Errorf("%s is not a netmask", mask)
	}
	return ones, nil
}

var deviceName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,14}$`)

// validDeviceName accepts tun, tun0, utun5 and null; a path is refused,
// because it would open a device node the profile picked.
func validDeviceName(s string) bool { return deviceName.MatchString(s) }

// validHost accepts an IP address or a DNS name. The value is used for a DNS
// lookup and in a bypass route, so anything else is refused.
func validHost(h string) bool {
	if addr, err := netip.ParseAddr(h); err == nil {
		return addr.Zone() == ""
	}
	return validDNSName(h)
}

func validDNSName(h string) bool {
	h = strings.TrimSuffix(h, ".")
	if h == "" || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return false
			}
		}
	}
	return true
}

// cleanDomain validates and lower-cases a DNS search or match domain. An IP
// address or the root "." is not a domain.
func cleanDomain(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSuffix(s, "."))
	if _, err := netip.ParseAddr(s); err == nil || !validDNSName(s) {
		return "", false
	}
	return s, true
}

var (
	nameLine         = regexp.MustCompile(`(?i)^[#;]\s*(?:profile\s+)?name\s*[:=]\s*(.+?)\s*$`)
	accessServerLine = regexp.MustCompile(`^#\s*OVPN_ACCESS_SERVER_PROFILE=(.+?)\s*$`)
)

// nameFromComment reads a profile name from "# Name: x", "# profile name = x"
// or the "# OVPN_ACCESS_SERVER_PROFILE=x" line OpenVPN Access Server writes.
func nameFromComment(comment string) string {
	m := nameLine.FindStringSubmatch(comment)
	if m == nil {
		m = accessServerLine.FindStringSubmatch(comment)
	}
	if m == nil {
		return ""
	}
	name := strings.TrimSpace(m[1])
	if utf8.RuneCountInString(name) > maxSuggestedName {
		name = string([]rune(name)[:maxSuggestedName])
	}
	return name
}

// directiveLine is a directive as serialize writes it.
func directiveLine(it item) string {
	var b strings.Builder
	b.WriteString(it.name)
	for _, a := range it.args {
		b.WriteByte(' ')
		b.WriteString(quoteField(a))
	}
	return b.String()
}

// shortenComment cuts a comment to what openvpn can read as one line. A
// comment says nothing to openvpn, so nothing is lost.
func shortenComment(text string) string {
	n := maxConfigLineBytes
	if len(text) <= n {
		return text
	}
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n]
}

// serialize writes items back as profile text.
func serialize(items []item) string {
	var b strings.Builder
	for _, it := range items {
		switch it.kind {
		case kindComment:
			b.WriteString(shortenComment(it.text))
			b.WriteByte('\n')
		case kindDirective:
			b.WriteString(directiveLine(it))
			b.WriteByte('\n')
		case kindBlock:
			b.WriteString("<" + it.name + ">\n" + it.body + "</" + it.name + ">\n")
		case kindConnection:
			b.WriteString("<connection>\n" + serialize(it.children) + "</connection>\n")
		}
	}
	return b.String()
}
