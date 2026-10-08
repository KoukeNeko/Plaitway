package wg

import (
	"bytes"
	"crypto/ecdh"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

const (
	minMTU = 576
	maxMTU = 65535
)

// hookDirectives are the wg-quick directives that run shell commands or
// rewrite the profile file. The daemon runs as root, so a profile that has one
// is rejected, never cleaned up.
var hookDirectives = map[string]string{
	"preup":      "PreUp",
	"postup":     "PostUp",
	"predown":    "PreDown",
	"postdown":   "PostDown",
	"saveconfig": "SaveConfig",
}

// ParseError is a rejected profile. Its message never contains a value from
// the profile, because values include private keys.
type ParseError struct {
	Line int // 1-based; 0 when the problem is not tied to one line
	Msg  string
}

func (e *ParseError) Error() string {
	if e.Line == 0 {
		return e.Msg
	}
	return fmt.Sprintf("line %d: %s", e.Line, e.Msg)
}

// profile is a validated wg-quick profile.
type profile struct {
	privateKey [32]byte
	addresses  []netip.Prefix
	listenPort uint16
	dnsServers []netip.Addr
	dnsDomains []string
	mtu        int  // 0 selects the default
	noRoutes   bool // Table = off
	fwmark     uint32
	peers      []peerConfig

	// excludePrivate is the user's option, not part of the profile text: the
	// private ranges are left out of every peer's AllowedIPs.
	excludePrivate bool

	// warnings are what the text says one thing in and means another in.
	warnings []tunnel.Warning
}

type peerConfig struct {
	publicKey    [32]byte
	presharedKey [32]byte // all zero when the profile has none
	allowedIPs   []netip.Prefix
	endpoint     *endpointConfig
	keepalive    uint16
}

type endpointConfig struct {
	host string
	addr netip.Addr // valid when host is an IP literal
	port uint16
}

// Parse validates an untrusted wg-quick profile. Anything that could run a
// program, and anything this engine does not understand, rejects the profile.
func Parse(content []byte) (tunnel.Parsed, error) {
	p, err := parseProfile(content)
	if err != nil {
		return tunnel.Parsed{}, err
	}
	summary, err := p.summary()
	if err != nil {
		return tunnel.Parsed{}, err
	}
	return tunnel.Parsed{Summary: summary, Warnings: p.warnings, Content: slices.Clone(content)}, nil
}

func parseProfile(content []byte) (*profile, error) {
	content = bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))
	p := &profile{}
	var (
		section    string // "", "interface" or "peer"
		interfaceN int    // line of [Interface]
		peerLines  []int  // line of each [Peer]
		cur        *peerConfig
	)
	reject := func(line int, format string, args ...any) error {
		return &ParseError{Line: line, Msg: fmt.Sprintf(format, args...)}
	}

	for i, raw := range strings.Split(string(content), "\n") {
		n := i + 1
		line, _, _ := strings.Cut(raw, "#")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "[") {
			name, ok := strings.CutSuffix(line[1:], "]")
			switch {
			case ok && strings.EqualFold(name, "Interface") && interfaceN == 0:
				section, interfaceN = "interface", n
			case ok && strings.EqualFold(name, "Interface"):
				return nil, reject(n, "more than one [Interface] section")
			case ok && strings.EqualFold(name, "Peer"):
				p.peers = append(p.peers, peerConfig{})
				cur = &p.peers[len(p.peers)-1]
				section = "peer"
				peerLines = append(peerLines, n)
			default:
				return nil, reject(n, "unknown section")
			}
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, reject(n, "expected a directive of the form Key = Value")
		}
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
		if hook, banned := hookDirectives[key]; banned {
			return nil, reject(n, "%s is not allowed: profiles cannot run commands", hook)
		}

		var err error
		switch section {
		case "interface":
			err = p.setInterface(key, value)
		case "peer":
			err = cur.set(key, value, func(directive, message string) {
				p.warnings = append(p.warnings, tunnel.Warning{Line: n, Directive: directive, Message: message})
			})
		default:
			err = errors.New("directive outside a section")
		}
		if err != nil {
			return nil, reject(n, "%s", err)
		}
	}

	if interfaceN == 0 {
		return nil, reject(0, "no [Interface] section")
	}
	if p.privateKey == ([32]byte{}) {
		return nil, reject(interfaceN, "[Interface] has no PrivateKey")
	}
	if len(p.peers) == 0 {
		return nil, reject(0, "no [Peer] section")
	}
	for i, peer := range p.peers {
		if peer.publicKey == ([32]byte{}) {
			return nil, reject(peerLines[i], "[Peer] has no PublicKey")
		}
	}
	return p, nil
}

func (p *profile) setInterface(key, value string) error {
	switch key {
	case "privatekey":
		k, ok := parseKey(value)
		if !ok {
			return errors.New("PrivateKey is not a base64-encoded 32-byte key")
		}
		p.privateKey = k
	case "address":
		for _, item := range splitList(value) {
			prefix, ok := parseAddress(item)
			if !ok {
				return errors.New("Address must be an IP address with an optional prefix length")
			}
			p.addresses = append(p.addresses, prefix)
		}
	case "listenport":
		port, err := strconv.ParseUint(value, 10, 16)
		if err != nil {
			return errors.New("ListenPort must be a port number")
		}
		p.listenPort = uint16(port)
	case "dns":
		for _, item := range splitList(value) {
			addr, err := netip.ParseAddr(item)
			domain, isDomain := parseDomain(item)
			switch {
			case err == nil && addr.Zone() == "":
				p.dnsServers = append(p.dnsServers, addr.Unmap())
			case err != nil && isDomain:
				p.dnsDomains = append(p.dnsDomains, domain)
			default:
				return errors.New("DNS must be IP addresses and search domains")
			}
		}
	case "mtu":
		mtu, err := strconv.Atoi(value)
		if err != nil || mtu < minMTU || mtu > maxMTU {
			return fmt.Errorf("MTU must be a number from %d to %d", minMTU, maxMTU)
		}
		p.mtu = mtu
	case "table":
		switch strings.ToLower(value) {
		case "auto":
			p.noRoutes = false
		case "off":
			p.noRoutes = true
		default:
			return errors.New("Table must be auto or off")
		}
	case "fwmark":
		if strings.EqualFold(value, "off") {
			p.fwmark = 0
			break
		}
		mark, err := strconv.ParseUint(value, 0, 32)
		if err != nil {
			return errors.New("FwMark must be a number or off")
		}
		p.fwmark = uint32(mark)
	default:
		return unknownDirective(key)
	}
	return nil
}

// set stores one directive of a [Peer] section; warn is told what it accepted but reads differently
// from how it was written.
func (p *peerConfig) set(key, value string, warn func(directive, message string)) error {
	switch key {
	case "publickey":
		k, ok := parseKey(value)
		if !ok {
			return errors.New("PublicKey is not a base64-encoded 32-byte key")
		}
		p.publicKey = k
	case "presharedkey":
		k, ok := parseKey(value)
		if !ok {
			return errors.New("PresharedKey is not a base64-encoded 32-byte key")
		}
		p.presharedKey = k
	case "allowedips":
		for _, item := range splitList(value) {
			prefix, ok := parseAddress(item)
			if !ok {
				return errors.New("AllowedIPs must be IP addresses with an optional prefix length")
			}
			// A /0 keeps no bit of the address: ASUS routers write 192.168.50.0/0 for the LAN, and
			// it is the whole address space, which turns a tunnel to one network into a full tunnel.
			if prefix.Bits() == 0 && !prefix.Addr().IsUnspecified() {
				warn("AllowedIPs", fmt.Sprintf("%s is read as %s, so all traffic goes through the tunnel; a longer prefix, such as /24, limits it to one network", prefix, prefix.Masked()))
			}
			p.allowedIPs = append(p.allowedIPs, prefix.Masked())
		}
	case "endpoint":
		endpoint, ok := parseEndpoint(value)
		if !ok {
			return errors.New("Endpoint must be host:port or [IPv6]:port")
		}
		p.endpoint = &endpoint
	case "persistentkeepalive":
		if strings.EqualFold(value, "off") {
			p.keepalive = 0
			break
		}
		secs, err := strconv.ParseUint(value, 10, 16)
		if err != nil {
			return errors.New("PersistentKeepalive must be a number of seconds or off")
		}
		p.keepalive = uint16(secs)
	default:
		return unknownDirective(key)
	}
	return nil
}

// unknownDirective names the directive only when it looks like one, so that a
// mangled line holding a secret never reaches an error message.
func unknownDirective(key string) error {
	const maxName = 32
	if len(key) > maxName || strings.Trim(key, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
		return errors.New("unknown directive")
	}
	return fmt.Errorf("unknown directive %q", key)
}

func parseKey(s string) (key [32]byte, ok bool) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(raw) != len(key) {
		return key, false
	}
	return [32]byte(raw), true
}

func splitList(value string) []string {
	var items []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

// parseAddress accepts an address with or without a prefix length; a bare
// address is a host prefix.
func parseAddress(s string) (netip.Prefix, bool) {
	if prefix, err := netip.ParsePrefix(s); err == nil {
		return prefix, true
	}
	addr, err := netip.ParseAddr(s)
	if err != nil || addr.Zone() != "" {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(addr, addr.BitLen()), true
}

func parseEndpoint(s string) (endpointConfig, bool) {
	host, portText, err := net.SplitHostPort(s)
	if err != nil {
		return endpointConfig{}, false
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return endpointConfig{}, false
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return endpointConfig{host: host, addr: addr, port: uint16(port)}, addr.Zone() == ""
	}
	domain, ok := parseDomain(host)
	return endpointConfig{host: domain, port: uint16(port)}, ok
}

// parseDomain accepts ASCII host names. The Reconciler hands search domains to
// the system resolver as root, so nothing else gets through.
func parseDomain(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSuffix(s, "."))
	if s == "" || len(s) > 253 {
		return "", false
	}
	labels := strings.Split(s, ".")
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		if strings.Trim(label, "abcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
			return "", false
		}
	}
	// An all-numeric last label is an IP address with a typo, not a domain.
	if strings.Trim(labels[len(labels)-1], "0123456789") == "" {
		return "", false
	}
	return s, true
}

func (p *profile) summary() (tunnel.Summary, error) {
	publicKey, err := p.publicKey()
	if err != nil {
		return tunnel.Summary{}, err
	}
	s := tunnel.Summary{Addresses: p.addresses, DNSServers: p.dnsServers, PublicKey: publicKey}
	for _, peer := range p.peers {
		if peer.endpoint != nil {
			s.Endpoints = append(s.Endpoints, tunnel.Endpoint{Host: peer.endpoint.host, Port: peer.endpoint.port})
		}
		if !p.noRoutes {
			s.Routes = append(s.Routes, peer.allowedIPs...)
		}
	}
	s.RedirectsDefaultRoute = slices.ContainsFunc(s.Routes, isDefaultRoute)
	return s, nil
}

// publicKey is what `wg pubkey` prints for the profile's PrivateKey.
func (p *profile) publicKey() (string, error) {
	key, err := ecdh.X25519().NewPrivateKey(p.privateKey[:])
	if err != nil {
		return "", fmt.Errorf("derive the public key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}

func isDefaultRoute(p netip.Prefix) bool { return p.Bits() == 0 }

// isDefaultHalf is one of the two halves a default route is often written as:
// 0.0.0.0/1 and 128.0.0.0/1, or ::/1 and 8000::/1.
func isDefaultHalf(p netip.Prefix) bool {
	return p.Bits() == 1 && slices.Contains(defaultHalves, p.Masked())
}

var defaultHalves = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/1"), netip.MustParsePrefix("128.0.0.0/1"),
	netip.MustParsePrefix("::/1"), netip.MustParsePrefix("8000::/1"),
}
