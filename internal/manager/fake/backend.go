// Package fake is an in-memory VPN backend for both profile kinds and a
// Reconciler stand-in. Together they make the daemon a complete stand-in for
// the real one (plaitwayd -fake): the whole API can be exercised without root,
// a VPN server or any change to the host's network.
//
// A profile steers the behaviour of its engine with marker lines, each on a
// line of its own:
//
//	# fake: reject             Parse fails
//	# fake: needs-credentials  the engine asks for a user name and password (it
//	                           does so for a bare "auth-user-pass" too); any
//	                           non-empty pair is accepted, except that the
//	                           password "wrong" is rejected the first time
//	# fake: fail               connecting ends in FAILED
//	# fake: conflict           the profile's routes include one that is
//	                           SHADOWED by another profile and one that is
//	                           BLOCKED by the local network
//
// Without markers a profile connects after ConnectDelay and stays up.
//
// A WireGuard profile gets a public key that is a function of its PrivateKey
// (of the whole text when it has none), so editing the key changes it. The
// option to exclude private IPs is accepted and stored but has no effect on a
// fake tunnel.
package fake

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

const (
	markerReject           = "# fake: reject"
	markerNeedsCredentials = "# fake: needs-credentials"
	markerFail             = "# fake: fail"
	markerConflict         = "# fake: conflict"

	defaultDelay = time.Second
)

type Config struct {
	// ConnectDelay is how long connecting takes; zero means one second.
	ConnectDelay time.Duration
	// StatsInterval is how often the traffic counters move; zero means one second.
	StatsInterval time.Duration
}

func (c Config) withDefaults() Config {
	if c.ConnectDelay == 0 {
		c.ConnectDelay = defaultDelay
	}
	if c.StatsInterval == 0 {
		c.StatsInterval = defaultDelay
	}
	return c
}

// Backends returns a fake backend for OpenVPN and one for WireGuard.
func Backends(cfg Config) []tunnel.Backend {
	f := &factory{cfg: cfg.withDefaults()}
	return []tunnel.Backend{f.backend(tunnel.KindOpenVPN), f.backend(tunnel.KindWireGuard)}
}

type factory struct {
	cfg        Config
	nextNumber atomic.Int32
}

func (f *factory) backend(kind tunnel.Kind) tunnel.Backend {
	return tunnel.Backend{
		Kind:  kind,
		Parse: func(content []byte) (tunnel.Parsed, error) { return parse(kind, content) },
		New: func(spec tunnel.Spec, deps tunnel.Deps) (tunnel.Engine, error) {
			return newEngine(f, kind, spec, deps)
		},
		Probe: func() tunnel.EngineInfo { return tunnel.EngineInfo{Available: true, Version: "fake"} },
	}
}

// Directives that would run programs or change the host; a real backend
// removes them too.
var (
	openVPNScripts = []string{"up", "down", "route-up", "route-pre-down", "script-security", "plugin", "tls-verify", "client-connect", "setenv", "management"}
	wireGuardHooks = []string{"preup", "postup", "predown", "postdown"}
)

func parse(kind tunnel.Kind, content []byte) (tunnel.Parsed, error) {
	text := string(content)
	if strings.TrimSpace(text) == "" {
		return tunnel.Parsed{}, errors.New("profile is empty")
	}
	var (
		out      tunnel.Parsed
		kept     strings.Builder
		remotes  []tunnel.Endpoint
		protocol = "udp"
		section  string
	)
	lineNo := 0
	for line := range strings.Lines(text) {
		lineNo++
		trimmed := strings.TrimSpace(line)
		if trimmed == markerReject {
			return tunnel.Parsed{}, fmt.Errorf("line %d: rejected by %q", lineNo, markerReject)
		}
		if trimmed == "" || trimmed[0] == '#' || trimmed[0] == ';' {
			kept.WriteString(line)
			continue
		}

		if kind == tunnel.KindOpenVPN {
			fields := strings.Fields(trimmed)
			directive := strings.ToLower(fields[0])
			if slices.Contains(openVPNScripts, directive) {
				out.Warnings = append(out.Warnings, tunnel.Warning{Line: lineNo, Directive: directive, Message: "removed, a profile cannot run programs"})
				continue
			}
			switch directive {
			case "remote":
				remotes = append(remotes, remoteEndpoint(fields[1:]))
			case "proto":
				if len(fields) > 1 && strings.HasPrefix(strings.ToLower(fields[1]), "tcp") {
					protocol = "tcp"
				}
			case "route":
				if p, ok := routeDirective(fields[1:]); ok {
					out.Summary.Routes = append(out.Summary.Routes, p)
				}
			case "redirect-gateway":
				out.Summary.RedirectsDefaultRoute = true
			case "auth-user-pass":
				out.Summary.RequiresCredentials = len(fields) == 1
			case "dhcp-option":
				if len(fields) == 3 && strings.EqualFold(fields[1], "DNS") {
					if a, err := netip.ParseAddr(fields[2]); err == nil {
						out.Summary.DNSServers = append(out.Summary.DNSServers, a)
					}
				}
			}
		} else {
			if strings.HasPrefix(trimmed, "[") {
				section = strings.ToLower(trimmed)
			} else if rawKey, value, ok := strings.Cut(trimmed, "="); ok {
				rawKey = strings.TrimSpace(rawKey)
				key := strings.ToLower(rawKey)
				if slices.Contains(wireGuardHooks, key) {
					out.Warnings = append(out.Warnings, tunnel.Warning{Line: lineNo, Directive: rawKey, Message: "removed, a profile cannot run programs"})
					continue
				}
				wireGuardSetting(&out.Summary, section, key, strings.TrimSpace(value))
			}
		}
		kept.WriteString(line)
	}

	for _, r := range remotes {
		if r.Protocol == "" {
			r.Protocol = protocol
		}
		out.Summary.Endpoints = append(out.Summary.Endpoints, r)
	}
	if hasMarker(text, markerNeedsCredentials) {
		out.Summary.RequiresCredentials = true
	}
	if kind == tunnel.KindWireGuard && out.Summary.PublicKey == "" {
		out.Summary.PublicKey = fakePublicKey(kept.String())
	}
	if strings.TrimSpace(kept.String()) == "" {
		return tunnel.Parsed{}, errors.New("nothing is left of the profile after removing its script directives")
	}
	out.Content = []byte(kept.String())
	return out, nil
}

// remoteEndpoint reads "host [port [proto]]".
func remoteEndpoint(args []string) tunnel.Endpoint {
	e := tunnel.Endpoint{Port: 1194}
	if len(args) > 0 {
		e.Host = args[0]
	}
	if len(args) > 1 {
		if port, err := strconv.ParseUint(args[1], 10, 16); err == nil {
			e.Port = uint16(port)
		}
	}
	if len(args) > 2 {
		e.Protocol = "udp"
		if strings.HasPrefix(strings.ToLower(args[2]), "tcp") {
			e.Protocol = "tcp"
		}
	}
	return e
}

// routeDirective reads "network [netmask]".
func routeDirective(args []string) (netip.Prefix, bool) {
	if len(args) == 0 {
		return netip.Prefix{}, false
	}
	network, err := netip.ParseAddr(args[0])
	if err != nil || !network.Is4() {
		return netip.Prefix{}, false
	}
	ones := 32
	if len(args) > 1 {
		mask, err := netip.ParseAddr(args[1])
		if err != nil || !mask.Is4() {
			return netip.Prefix{}, false
		}
		raw := mask.As4()
		ones = bits.OnesCount32(binary.BigEndian.Uint32(raw[:]))
	}
	return netip.PrefixFrom(network, ones).Masked(), true
}

func wireGuardSetting(sum *tunnel.Summary, section, key, value string) {
	switch {
	case section == "[interface]" && key == "privatekey" && value != "":
		sum.PublicKey = fakePublicKey(value)
	case section == "[interface]" && key == "address":
		for _, item := range strings.Split(value, ",") {
			if p, err := netip.ParsePrefix(strings.TrimSpace(item)); err == nil {
				sum.Addresses = append(sum.Addresses, p)
			}
		}
	case section == "[interface]" && key == "dns":
		for _, item := range strings.Split(value, ",") {
			if a, err := netip.ParseAddr(strings.TrimSpace(item)); err == nil {
				sum.DNSServers = append(sum.DNSServers, a)
			}
		}
	case section == "[peer]" && key == "endpoint":
		if host, port, err := net.SplitHostPort(value); err == nil {
			if n, err := strconv.ParseUint(port, 10, 16); err == nil {
				sum.Endpoints = append(sum.Endpoints, tunnel.Endpoint{Host: host, Port: uint16(n)})
			}
		}
	case section == "[peer]" && key == "allowedips":
		for _, item := range strings.Split(value, ",") {
			p, err := netip.ParsePrefix(strings.TrimSpace(item))
			if err != nil {
				continue
			}
			sum.Routes = append(sum.Routes, p)
			if p.Bits() == 0 {
				sum.RedirectsDefaultRoute = true
			}
		}
	}
}

// fakePublicKey stands in for the key a real engine derives from the private
// key: 32 bytes in base64, always the same for the same input.
func fakePublicKey(source string) string {
	sum := sha256.Sum256([]byte(source))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func hasMarker(text, marker string) bool {
	for line := range strings.Lines(text) {
		if strings.TrimSpace(line) == marker {
			return true
		}
	}
	return false
}
