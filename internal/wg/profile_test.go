package wg

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

var (
	keyA = fixedKey(1)
	keyB = fixedKey(2)
	keyC = fixedKey(3)
)

func pfx(s ...string) []netip.Prefix {
	var out []netip.Prefix
	for _, p := range s {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}

func addrs(s ...string) []netip.Addr {
	var out []netip.Addr
	for _, a := range s {
		out = append(out, netip.MustParseAddr(a))
	}
	return out
}

const fullProfileTemplate = `# a wg-quick profile
[Interface]
PrivateKey = KEYA
Address = 10.6.0.2/32, fd00::2/128
DNS = 10.6.0.1, 1.1.1.1, corp.Example.com, lan
MTU = 1380
ListenPort = 51820
FwMark = 0xca6c

[Peer]
PublicKey = KEYB
PresharedKey = KEYC
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = vpn.example.com:51820
PersistentKeepalive = 25
`

func fill(s string) string {
	return strings.NewReplacer("KEYA", keyA, "KEYB", keyB, "KEYC", keyC).Replace(s)
}

func TestParseSummary(t *testing.T) {
	parsed, err := Parse([]byte(fill(fullProfileTemplate)))
	if err != nil {
		t.Fatal(err)
	}
	s := parsed.Summary
	if want := []tunnel.Endpoint{{Host: "vpn.example.com", Port: 51820}}; !slices.Equal(s.Endpoints, want) {
		t.Errorf("Endpoints = %v, want %v", s.Endpoints, want)
	}
	if want := pfx("0.0.0.0/0", "::/0"); !slices.Equal(s.Routes, want) {
		t.Errorf("Routes = %v, want %v", s.Routes, want)
	}
	if !s.RedirectsDefaultRoute {
		t.Error("RedirectsDefaultRoute = false, want true")
	}
	if want := addrs("10.6.0.1", "1.1.1.1"); !slices.Equal(s.DNSServers, want) {
		t.Errorf("DNSServers = %v, want %v", s.DNSServers, want)
	}
	if want := pfx("10.6.0.2/32", "fd00::2/128"); !slices.Equal(s.Addresses, want) {
		t.Errorf("Addresses = %v, want %v", s.Addresses, want)
	}
	if s.RequiresCredentials {
		t.Error("RequiresCredentials = true, want false")
	}
	if string(parsed.Content) != fill(fullProfileTemplate) {
		t.Error("Content differs from the input")
	}
}

// TestParseDerivesThePublicKey uses the key pairs of RFC 7748, section 6.1:
// wg prints the public key as the standard base64 of the X25519 base point
// multiplied by the private key.
func TestParseDerivesThePublicKey(t *testing.T) {
	base64OfHex := func(s string) string {
		raw, err := hex.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return base64.StdEncoding.EncodeToString(raw)
	}
	tests := []struct{ name, private, public string }{
		{"Alice", "77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a", "8520f0098930a754748b7ddcb43ef75a0dbf3a0d26381af4eba4a98eaa9b4e6a"},
		{"Bob", "5dab087e624a8a4b79e17f8b83800ee66f3bb1292618b6fd1c2f8b27ff88e0eb", "de9edb7d7b7dc1b4d35b61c2ece435373f8343c85b78674dadfc7e146f882b4f"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := Parse([]byte("[Interface]\nPrivateKey = " + base64OfHex(tt.private) + "\n[Peer]\nPublicKey = " + keyB + "\n"))
			if err != nil {
				t.Fatal(err)
			}
			if got, want := parsed.Summary.PublicKey, base64OfHex(tt.public); got != want {
				t.Errorf("PublicKey = %q, want %q", got, want)
			}
		})
	}
}

func TestParseValues(t *testing.T) {
	p, err := parseProfile([]byte(fill(fullProfileTemplate)))
	if err != nil {
		t.Fatal(err)
	}
	if p.mtu != 1380 || p.listenPort != 51820 || p.fwmark != 0xca6c || p.noRoutes {
		t.Errorf("interface = mtu %d port %d fwmark %#x noRoutes %v", p.mtu, p.listenPort, p.fwmark, p.noRoutes)
	}
	if want := []string{"corp.example.com", "lan"}; !slices.Equal(p.dnsDomains, want) {
		t.Errorf("dnsDomains = %v, want %v", p.dnsDomains, want)
	}
	peer := p.peers[0]
	if peer.keepalive != 25 || peer.endpoint == nil || peer.endpoint.host != "vpn.example.com" || peer.endpoint.port != 51820 {
		t.Errorf("peer = %+v", peer)
	}
	if peer.presharedKey == ([32]byte{}) {
		t.Error("PresharedKey was dropped")
	}
}

func TestParseAccepts(t *testing.T) {
	iface := "[Interface]\nPrivateKey = " + keyA + "\nAddress = 10.0.0.2/32\n"
	peer := "[Peer]\nPublicKey = " + keyB + "\nAllowedIPs = 10.0.0.0/8\n"
	tests := []struct {
		name    string
		content string
		check   func(t *testing.T, p *profile)
	}{
		{"minimal", iface + peer, nil},
		{"CRLF line endings", strings.ReplaceAll(iface+peer, "\n", "\r\n"), nil},
		{"byte order mark", "\xef\xbb\xbf" + iface + peer, nil},
		{"comments and blank lines", "\n# top\n" + iface + "   # indented comment\n\n" + peer + "AllowedIPs = 10.9.0.0/16 # trailing\n", func(t *testing.T, p *profile) {
			if want := pfx("10.0.0.0/8", "10.9.0.0/16"); !slices.Equal(p.allowedIPs(), want) {
				t.Errorf("allowedIPs = %v, want %v", p.allowedIPs(), want)
			}
		}},
		{"odd whitespace", "  [Interface]  \n\tPrivateKey\t=\t" + keyA + "  \nAddress=10.0.0.2/32\n" + "[Peer]\nPublicKey=" + keyB + "\n  AllowedIPs   =   10.0.0.0/8  ,  ::/0  \n", func(t *testing.T, p *profile) {
			if want := pfx("10.0.0.0/8", "::/0"); !slices.Equal(p.peers[0].allowedIPs, want) {
				t.Errorf("AllowedIPs = %v, want %v", p.peers[0].allowedIPs, want)
			}
		}},
		{"names and keys in any case", "[INTERFACE]\nprivatekey = " + keyA + "\nADDRESS = 10.0.0.2/32\n[peer]\nPUBLICKEY = " + keyB + "\n", nil},
		{"address without prefix length", "[Interface]\nPrivateKey = " + keyA + "\nAddress = 10.0.0.2, fd00::2\n" + peer, func(t *testing.T, p *profile) {
			if want := pfx("10.0.0.2/32", "fd00::2/128"); !slices.Equal(p.addresses, want) {
				t.Errorf("addresses = %v, want %v", p.addresses, want)
			}
		}},
		{"repeated list directives add up", "[Interface]\nPrivateKey = " + keyA + "\nAddress = 10.0.0.2/32\nAddress = fd00::2/128\nDNS = 1.1.1.1\nDNS = 9.9.9.9\n" + peer, func(t *testing.T, p *profile) {
			if len(p.addresses) != 2 || len(p.dnsServers) != 2 {
				t.Errorf("addresses %v, dns %v", p.addresses, p.dnsServers)
			}
		}},
		{"AllowedIPs host bits are cleared", iface + "[Peer]\nPublicKey = " + keyB + "\nAllowedIPs = 10.1.2.3/8\n", func(t *testing.T, p *profile) {
			if want := pfx("10.0.0.0/8"); !slices.Equal(p.peers[0].allowedIPs, want) {
				t.Errorf("AllowedIPs = %v, want %v", p.peers[0].allowedIPs, want)
			}
		}},
		{"Table auto and off", iface + "Table = Auto\nTable = off\n" + peer, func(t *testing.T, p *profile) {
			if !p.noRoutes {
				t.Error("Table = off was not applied")
			}
		}},
		{"FwMark off and KeepAlive off", "[Interface]\nPrivateKey = " + keyA + "\nFwMark = off\n" + peer + "PersistentKeepalive = off\n", func(t *testing.T, p *profile) {
			if p.fwmark != 0 || p.peers[0].keepalive != 0 {
				t.Error("off was not zero")
			}
		}},
		{"IPv6 endpoint", iface + "[Peer]\nPublicKey = " + keyB + "\nEndpoint = [2001:db8::1]:51820\n", func(t *testing.T, p *profile) {
			ep := p.peers[0].endpoint
			if ep.addr != netip.MustParseAddr("2001:db8::1") || ep.port != 51820 {
				t.Errorf("endpoint = %+v", ep)
			}
		}},
		{"IPv4 endpoint", iface + "[Peer]\nPublicKey = " + keyB + "\nEndpoint = 203.0.113.9:1194\n", func(t *testing.T, p *profile) {
			if ep := p.peers[0].endpoint; ep.addr != netip.MustParseAddr("203.0.113.9") || ep.port != 1194 {
				t.Errorf("endpoint = %+v", ep)
			}
		}},
		{"peer without endpoint", iface + peer, func(t *testing.T, p *profile) {
			if p.peers[0].endpoint != nil {
				t.Error("endpoint should be nil")
			}
		}},
		{"IPv6 DNS server", "[Interface]\nPrivateKey = " + keyA + "\nDNS = fd00::1, 2606:4700:4700::1111\n" + peer, func(t *testing.T, p *profile) {
			if want := addrs("fd00::1", "2606:4700:4700::1111"); !slices.Equal(p.dnsServers, want) {
				t.Errorf("dnsServers = %v, want %v", p.dnsServers, want)
			}
		}},
		{"several peers", iface + peer + "[Peer]\nPublicKey = " + keyC + "\nEndpoint = b.example.com:2000\nAllowedIPs = 192.168.5.0/24\n", func(t *testing.T, p *profile) {
			if len(p.peers) != 2 || p.peers[1].endpoint.host != "b.example.com" {
				t.Errorf("peers = %+v", p.peers)
			}
			parsed, err := Parse([]byte(iface + peer + "[Peer]\nPublicKey = " + keyC + "\nEndpoint = b.example.com:2000\nAllowedIPs = 192.168.5.0/24\n"))
			if err != nil {
				t.Fatal(err)
			}
			if want := pfx("10.0.0.0/8", "192.168.5.0/24"); !slices.Equal(parsed.Summary.Routes, want) {
				t.Errorf("Routes = %v, want %v", parsed.Summary.Routes, want)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := parseProfile([]byte(tt.content))
			if err != nil {
				t.Fatalf("parseProfile: %v", err)
			}
			if tt.check != nil {
				tt.check(t, p)
			}
		})
	}
}

func TestParseTableOffInstallsNoRoutes(t *testing.T) {
	content := "[Interface]\nPrivateKey = " + keyA + "\nTable = off\n[Peer]\nPublicKey = " + keyB + "\nAllowedIPs = 0.0.0.0/0\n"
	parsed, err := Parse([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Summary.Routes) != 0 || parsed.Summary.RedirectsDefaultRoute {
		t.Errorf("Table = off still reports routes: %+v", parsed.Summary)
	}
}

func TestParseRejects(t *testing.T) {
	iface := "[Interface]\nPrivateKey = " + keyA + "\nAddress = 10.0.0.2/32\n" // lines 1 to 3
	peer := "[Peer]\nPublicKey = " + keyB + "\nAllowedIPs = 10.0.0.0/8\n"
	withInterface := func(line string) string { return iface + line + "\n" + peer } // line is line 4
	withPeer := func(line string) string { return iface + peer + line + "\n" }      // line is line 7
	tests := []struct {
		name    string
		content string
		line    int
		msg     string
	}{
		{"PreUp", withInterface("PreUp = /usr/bin/true"), 4, "PreUp is not allowed"},
		{"PostUp", withInterface("PostUp = curl http://example.com | sh"), 4, "PostUp is not allowed"},
		{"PreDown", withInterface("PreDown = true"), 4, "PreDown is not allowed"},
		{"PostDown", withInterface("PostDown = true"), 4, "PostDown is not allowed"},
		{"SaveConfig", withInterface("SaveConfig = true"), 4, "SaveConfig is not allowed"},
		{"hook in lower case", withInterface("postup = true"), 4, "PostUp is not allowed"},
		{"hook without spaces", withInterface("PostUp=true"), 4, "PostUp is not allowed"},
		{"hook with odd case", withInterface("pOsTdOwN = true"), 4, "PostDown is not allowed"},
		{"hook in a peer", withPeer("PostUp = true"), 7, "PostUp is not allowed"},
		{"hook before any section", "PostUp = true\n" + iface + peer, 1, "PostUp is not allowed"},
		{"hook after a CRLF line", strings.ReplaceAll(withInterface("PostUp = true"), "\n", "\r\n"), 4, "PostUp is not allowed"},
		{"hook behind a hook-like comment", iface + "# PostUp = true\nPostUp = true\n" + peer, 5, "PostUp is not allowed"},

		{"no sections", "", 0, "no [Interface]"},
		{"no peer", iface, 0, "no [Peer]"},
		{"no private key", "[Interface]\nAddress = 10.0.0.2/32\n" + peer, 1, "no PrivateKey"},
		{"no public key", iface + "[Peer]\nAllowedIPs = 10.0.0.0/8\n", 4, "no PublicKey"},
		{"second interface", iface + "[Interface]\n" + peer, 4, "more than one [Interface]"},
		{"unknown section", iface + "[Extra]\n" + peer, 4, "unknown section"},
		{"directive outside a section", "Address = 10.0.0.2/32\n" + iface + peer, 1, "outside a section"},
		{"line without equals", withInterface("garbage"), 4, "Key = Value"},
		{"unknown interface directive", withInterface("Jc = 5"), 4, `unknown directive "jc"`},
		{"peer directive in the interface", withInterface("PublicKey = " + keyB), 4, "unknown directive"},
		{"interface directive in a peer", withPeer("Address = 10.0.0.9/32"), 7, "unknown directive"},

		{"private key not base64", withInterface("PrivateKey = not a key!!"), 4, "PrivateKey is not"},
		{"private key too short", withInterface("PrivateKey = AAAA"), 4, "PrivateKey is not"},
		{"private key 33 bytes", withInterface("PrivateKey = " + keyOfLen(33)), 4, "PrivateKey is not"},
		{"private key empty", withInterface("PrivateKey ="), 4, "PrivateKey is not"},
		{"public key not base64", iface + "[Peer]\nPublicKey = ????\n", 5, "PublicKey is not"},
		{"public key 31 bytes", iface + "[Peer]\nPublicKey = " + keyOfLen(31) + "\n", 5, "PublicKey is not"},
		{"preshared key bad", withPeer("PresharedKey = AAAA"), 7, "PresharedKey is not"},
		{"address bad", withInterface("Address = 10.0.0.2/33"), 4, "Address must be"},
		{"address a name", withInterface("Address = router.example.com"), 4, "Address must be"},
		{"address with zone", withInterface("Address = fe80::1%en0"), 4, "Address must be"},
		{"allowed ips bad", withPeer("AllowedIPs = 10.0.0.0/8, nope"), 7, "AllowedIPs must be"},
		{"listen port too big", withInterface("ListenPort = 70000"), 4, "ListenPort"},
		{"listen port text", withInterface("ListenPort = abc"), 4, "ListenPort"},
		{"MTU too small", withInterface("MTU = 100"), 4, "MTU must be"},
		{"MTU too big", withInterface("MTU = 100000"), 4, "MTU must be"},
		{"MTU text", withInterface("MTU = big"), 4, "MTU must be"},
		{"Table number", withInterface("Table = 51820"), 4, "Table must be auto or off"},
		{"Table main", withInterface("Table = main"), 4, "Table must be auto or off"},
		{"Table empty", withInterface("Table ="), 4, "Table must be auto or off"},
		{"FwMark text", withInterface("FwMark = xyz"), 4, "FwMark"},
		{"keepalive negative", withPeer("PersistentKeepalive = -1"), 7, "PersistentKeepalive"},
		{"keepalive too big", withPeer("PersistentKeepalive = 70000"), 7, "PersistentKeepalive"},
		{"DNS with spaces", withInterface("DNS = bad domain!"), 4, "DNS must be"},
		{"DNS root", withInterface("DNS = 1.1.1.1, ."), 4, "DNS must be"},
		{"DNS leading hyphen", withInterface("DNS = -x.com"), 4, "DNS must be"},
		{"DNS with zone", withInterface("DNS = fe80::1%en0"), 4, "DNS must be"},
		{"DNS broken address", withInterface("DNS = 1.2.3"), 4, "DNS must be"},
		{"DNS with shell characters", withInterface("DNS = $(id).example.com"), 4, "DNS must be"},
		{"DNS tilde", withInterface("DNS = ~corp.example.com"), 4, "DNS must be"},

		{"endpoint without port", withPeer("Endpoint = vpn.example.com"), 7, "Endpoint must be"},
		{"endpoint port zero", withPeer("Endpoint = vpn.example.com:0"), 7, "Endpoint must be"},
		{"endpoint port too big", withPeer("Endpoint = vpn.example.com:99999"), 7, "Endpoint must be"},
		{"endpoint host with space", withPeer("Endpoint = bad host:51820"), 7, "Endpoint must be"},
		{"endpoint host with slash", withPeer("Endpoint = a/b.example.com:51820"), 7, "Endpoint must be"},
		{"endpoint empty host", withPeer("Endpoint = :51820"), 7, "Endpoint must be"},
		{"endpoint IPv6 without brackets", withPeer("Endpoint = 2001:db8::1:51820"), 7, "Endpoint must be"},
		{"endpoint IPv6 zone", withPeer("Endpoint = [fe80::1%en0]:51820"), 7, "Endpoint must be"},
		{"endpoint broken IPv4", withPeer("Endpoint = 1.2.3:51820"), 7, "Endpoint must be"},
		{"endpoint hyphen label", withPeer("Endpoint = -vpn.example.com:51820"), 7, "Endpoint must be"},
		{"endpoint non-ASCII", withPeer("Endpoint = vpn.exämple.com:51820"), 7, "Endpoint must be"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.content))
			var perr *ParseError
			if !errors.As(err, &perr) {
				t.Fatalf("Parse error = %v, want a *ParseError", err)
			}
			if perr.Line != tt.line {
				t.Errorf("error line = %d, want %d (%v)", perr.Line, tt.line, err)
			}
			if !strings.Contains(err.Error(), tt.msg) {
				t.Errorf("error = %q, want it to contain %q", err, tt.msg)
			}
			if tt.line > 0 && !strings.HasPrefix(err.Error(), "line ") {
				t.Errorf("error = %q does not name the line", err)
			}
		})
	}
}

func keyOfLen(n int) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, n)) }

func TestParseErrorsNeverHoldProfileValues(t *testing.T) {
	secret := fixedKey(9)
	tests := map[string]string{
		"bad private key":    "[Interface]\nPrivateKey = " + secret + "AA\n",
		"private key typo":   "[Interface]\nPrivateKey = " + secret[:40] + "\n",
		"key as directive":   "[Interface]\n" + secret + " = 1\n",
		"key with colon":     "[Interface]\nPrivateKey: " + secret + "\n",
		"key in address":     "[Interface]\nPrivateKey = " + keyA + "\nAddress = " + secret + "\n",
		"key in AllowedIPs":  "[Interface]\nPrivateKey = " + keyA + "\n[Peer]\nPublicKey = " + keyB + "\nAllowedIPs = " + secret + "\n",
		"key in endpoint":    "[Interface]\nPrivateKey = " + keyA + "\n[Peer]\nPublicKey = " + keyB + "\nEndpoint = " + secret + "\n",
		"key in DNS":         "[Interface]\nPrivateKey = " + keyA + "\nDNS = " + secret + "\n",
		"key as table":       "[Interface]\nPrivateKey = " + keyA + "\nTable = " + secret + "\n",
		"key in hook":        "[Interface]\nPrivateKey = " + keyA + "\nPostUp = echo " + secret + "\n",
		"short secret value": "[Interface]\nPrivateKey = " + keyA + "\nMTU = " + secret + "\n",
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(content))
			if err == nil {
				t.Fatal("profile was accepted")
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), secret[:20]) {
				t.Errorf("error contains profile text: %q", err)
			}
		})
	}
}

// ASUS routers write "192.168.50.0/0" where the LAN is meant. WireGuard masks that to 0.0.0.0/0,
// which turns a tunnel to one network into a tunnel for everything.
func TestParseWarnsOfAnAddressWithAZeroPrefixLength(t *testing.T) {
	const head = "[Interface]\nPrivateKey = KEYA\nAddress = 10.6.0.2/32\n\n[Peer]\nPublicKey = KEYB\n"
	tests := []struct {
		name     string
		allowed  string
		wantLine int // 0: no warning
		wantText string
	}{
		{"an IPv4 address with /0", "192.168.50.0/0", 7, "192.168.50.0/0 is read as 0.0.0.0/0"},
		{"one of several", "10.6.0.0/24, 192.168.50.0/0", 7, "192.168.50.0/0 is read as 0.0.0.0/0"},
		{"an IPv6 address with /0", "fd00::1/0", 7, "fd00::1/0 is read as ::/0"},
		{"the IPv4 default", "0.0.0.0/0", 0, ""},
		{"the IPv6 default", "::/0", 0, ""},
		{"a network", "192.168.50.0/24", 0, ""},
		{"a host address with a prefix length, which WireGuard masks as wg-quick does", "10.0.0.5/24", 0, ""},
		{"an address without a length", "192.168.50.7", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := Parse([]byte(fill(head + "AllowedIPs = " + tt.allowed + "\n")))
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantLine == 0 {
				if len(parsed.Warnings) != 0 {
					t.Fatalf("warnings = %+v, want none", parsed.Warnings)
				}
				return
			}
			if len(parsed.Warnings) != 1 {
				t.Fatalf("warnings = %+v, want one", parsed.Warnings)
			}
			w := parsed.Warnings[0]
			if w.Line != tt.wantLine || w.Directive != "AllowedIPs" || !strings.Contains(w.Message, tt.wantText) {
				t.Errorf("warning = %+v, want line %d, AllowedIPs, a message with %q", w, tt.wantLine, tt.wantText)
			}
		})
	}
}

func TestPlanRepeatsTheWarningsOfParse(t *testing.T) {
	p, err := parseProfile([]byte(fill("[Interface]\nPrivateKey = KEYA\n[Peer]\nPublicKey = KEYB\nAllowedIPs = 192.168.50.0/0\n")))
	if err != nil {
		t.Fatal(err)
	}
	got := p.plan(tunnel.ModeAuto).warnings
	if len(got) != 1 || !strings.HasPrefix(got[0], "AllowedIPs: 192.168.50.0/0 is read as 0.0.0.0/0") {
		t.Errorf("warnings = %q, want the warning of Parse, led by its directive", got)
	}
}
