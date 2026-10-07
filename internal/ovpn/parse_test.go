package ovpn

import (
	"bytes"
	"errors"
	"flag"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

var update = flag.Bool("update", false, "rewrite golden files")

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the golden file (rerun with -update to accept)\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func mustPrefixes(ss ...string) []netip.Prefix {
	var out []netip.Prefix
	for _, s := range ss {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

// directiveNames lists the directive and block names of stored content, so
// tests can check what survived without matching text.
func directiveNames(t *testing.T, content []byte) []string {
	t.Helper()
	items, err := scan(string(content), 1, false)
	if err != nil {
		t.Fatalf("stored content does not scan: %v", err)
	}
	var names []string
	var walk func([]item)
	walk = func(items []item) {
		for _, it := range items {
			switch it.kind {
			case kindDirective:
				names = append(names, it.name)
			case kindBlock:
				names = append(names, "<"+it.name+">")
			case kindConnection:
				names = append(names, "<connection>")
				walk(it.children)
			}
		}
	}
	walk(items)
	return names
}

func TestParseASUSSample(t *testing.T) {
	parsed, err := Parse(readFixture(t, "asus.ovpn"))
	if err != nil {
		t.Fatal(err)
	}
	wantSummary := tunnel.Summary{
		Endpoints:           []tunnel.Endpoint{{Host: "vpn.example.net", Port: 1194, Protocol: "tcp"}},
		Routes:              mustPrefixes("192.168.1.0/24"),
		RequiresCredentials: true,
	}
	if !reflect.DeepEqual(parsed.Summary, wantSummary) {
		t.Errorf("Summary = %+v\nwant      %+v", parsed.Summary, wantSummary)
	}
	if parsed.Summary.RedirectsDefaultRoute {
		t.Error("the sample ignores redirect-gateway with pull-filter and must not be a full tunnel")
	}
	if parsed.SuggestedName != "ASUS Router" {
		t.Errorf("SuggestedName = %q", parsed.SuggestedName)
	}
	if len(parsed.Warnings) != 0 {
		t.Errorf("the sample must be accepted unchanged, got warnings: %+v", parsed.Warnings)
	}
	checkGolden(t, "asus.golden", parsed.Content)

	// Everything the owner's server needs is kept as the profile says.
	for _, want := range []string{
		"comp-lzo yes\n", "auth SHA1\n", "cipher AES-128-CBC\n", "data-ciphers AES-128-CBC\n",
		"ignore-unknown-option cipher data-ciphers\n", "ignore-unknown-option block-outside-dns\n",
		"pull-filter ignore redirect-gateway\n", "pull-filter ignore dhcp-option\n",
		"route 192.168.1.0 255.255.255.0\n", "auth-user-pass\n", "auth-nocache\n",
	} {
		if !strings.Contains(string(parsed.Content), want) {
			t.Errorf("stored content lost %q", want)
		}
	}
}

func TestParseIsIdempotent(t *testing.T) {
	for _, name := range []string{"asus.ovpn", "windows.ovpn", "merlin.ovpn", "connection-blocks.ovpn"} {
		t.Run(name, func(t *testing.T) {
			first, err := Parse(readFixture(t, name))
			if err != nil {
				t.Fatal(err)
			}
			second, err := Parse(first.Content)
			if err != nil {
				t.Fatalf("stored content no longer parses: %v", err)
			}
			if !bytes.Equal(first.Content, second.Content) {
				t.Errorf("parsing stored content changed it:\n%s\n----\n%s", first.Content, second.Content)
			}
			if len(second.Warnings) != 0 {
				t.Errorf("stored content still has warnings: %+v", second.Warnings)
			}
			if !reflect.DeepEqual(first.Summary, second.Summary) {
				t.Errorf("summary changed on re-parse:\n%+v\n%+v", first.Summary, second.Summary)
			}
		})
	}
}

func TestParseFixtures(t *testing.T) {
	tests := []struct {
		file    string
		summary tunnel.Summary
		name    string
		domains []string
	}{
		{
			file: "windows.ovpn",
			summary: tunnel.Summary{
				Endpoints: []tunnel.Endpoint{{Host: "vpn.example.org", Port: 443, Protocol: "udp"}},
			},
		},
		{
			file: "merlin.ovpn",
			summary: tunnel.Summary{
				Endpoints:             []tunnel.Endpoint{{Host: "203.0.113.7", Port: 1194, Protocol: "udp"}},
				RedirectsDefaultRoute: true,
				RequiresCredentials:   true,
				DNSServers:            []netip.Addr{netip.MustParseAddr("192.168.1.1")},
			},
			name:    "Home Merlin",
			domains: []string{"lan.example"},
		},
		{
			file: "connection-blocks.ovpn",
			summary: tunnel.Summary{
				Endpoints: []tunnel.Endpoint{
					{Host: "primary.example.com", Port: 1194, Protocol: "udp"},
					{Host: "backup.example.com", Port: 443, Protocol: "tcp"},
					{Host: "2001:db8::10", Port: 8443, Protocol: "tcp"},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			p, err := parseProfile(readFixture(t, tt.file))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(p.Summary, tt.summary) {
				t.Errorf("Summary = %+v\nwant      %+v", p.Summary, tt.summary)
			}
			if p.SuggestedName != tt.name {
				t.Errorf("SuggestedName = %q, want %q", p.SuggestedName, tt.name)
			}
			if !reflect.DeepEqual(p.domains, tt.domains) {
				t.Errorf("domains = %v, want %v", p.domains, tt.domains)
			}
			if bytes.Contains(p.Content, []byte("\r")) {
				t.Error("stored content keeps carriage returns")
			}
		})
	}
}

func TestParseCRLFEqualsLF(t *testing.T) {
	lf := "client\ndev tun\nremote h.example 1194\n<ca>\nabc\n</ca>\n"
	crlf := strings.ReplaceAll(lf, "\n", "\r\n")
	a, err := Parse([]byte(lf))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse([]byte(crlf))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Content, b.Content) {
		t.Errorf("CRLF and LF profiles stored differently:\n%q\n%q", a.Content, b.Content)
	}
}

func TestParseRejectsFileReferences(t *testing.T) {
	const base = "client\nremote h.example 1194\n"
	tests := []struct {
		name string
		line string
	}{
		{"ca", "ca /etc/ssl/ca.crt"},
		{"capath", "capath /etc/ssl/certs"},
		{"cert", "cert client.crt"},
		{"key", "key client.key"},
		{"key with traversal", "key ../../../var/root/.ssh/id_rsa"},
		{"key with quoted traversal", `key "../../etc/master.passwd"`},
		{"dh", "dh dh2048.pem"},
		{"pkcs12", "pkcs12 client.p12"},
		{"tls-auth", "tls-auth ta.key 1"},
		{"tls-crypt", "tls-crypt /etc/openvpn/tc.key"},
		{"tls-crypt-v2", "tls-crypt-v2 client-v2.key"},
		{"crl-verify", "crl-verify crl.pem"},
		{"extra-certs", "extra-certs chain.pem"},
		{"secret", "secret static.key"},
		{"askpass", "askpass /etc/openvpn/pass.txt"},
		{"auth-user-pass file", "auth-user-pass /etc/openvpn/creds"},
		{"auth-user-pass relative file", "auth-user-pass ../creds.txt"},
		{"http-proxy-user-pass", "http-proxy-user-pass proxy.txt"},
		{"http-proxy credentials file", "http-proxy proxy.example 8080 /etc/proxy.auth"},
		{"socks-proxy credentials file", "socks-proxy proxy.example 1080 /etc/socks.auth"},
		{"socks-proxy credentials file without port", "socks-proxy proxy.example /etc/socks.auth"},
		{"client-config-dir", "client-config-dir /etc/ccd"},
		{"pkcs11-providers", "pkcs11-providers /usr/lib/evil.dylib"},
		{"double dash form", "--ca /etc/ssl/ca.crt"},
		{"upper case", "CA /etc/ssl/ca.crt"},
		{"dh path is not none", "dh none.pem"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(base + tt.line + "\n"))
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("Parse(%q) error = %v, want *ParseError", tt.line, err)
			}
			if pe.Line != 3 {
				t.Errorf("error names line %d, want 3: %v", pe.Line, err)
			}
		})
	}
}

func TestParseAcceptsInlineAndBareForms(t *testing.T) {
	tests := []struct{ name, line string }{
		{"dh none", "dh none"},
		{"bare askpass", "askpass"},
		{"bare auth-user-pass", "auth-user-pass"},
		{"http-proxy auto", "http-proxy proxy.example 8080 auto"},
		{"http-proxy auto-nct", "http-proxy proxy.example 8080 auto-nct basic"},
		{"http-proxy without credentials", "http-proxy proxy.example 8080"},
		{"socks-proxy with port", "socks-proxy proxy.example 1080"},
		{"inline auth-user-pass", "<auth-user-pass>\nuser\npass\n</auth-user-pass>"},
		{"inline tls-crypt-v2", "<tls-crypt-v2>\nabc\n</tls-crypt-v2>"},
		{"inline pkcs12", "<pkcs12>\nabc\n</pkcs12>"},
		{"inline peer-fingerprint", "<peer-fingerprint>\naa:bb\n</peer-fingerprint>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := Parse([]byte("client\nremote h.example 1194\n" + tt.line + "\n"))
			if err != nil {
				t.Fatal(err)
			}
			if len(parsed.Warnings) != 0 {
				t.Errorf("unexpected warnings: %+v", parsed.Warnings)
			}
		})
	}
}

func TestParseRemovesDangerousDirectives(t *testing.T) {
	const base = "client\nremote h.example 1194\n"
	tests := []struct {
		name   string
		line   string
		want   string // directive reported in the warning
		reason string // why, as the user is told
	}{
		{"up", "up /usr/local/bin/up.sh", "up", reasonProgram},
		{"down", "down /usr/local/bin/down.sh", "down", reasonProgram},
		{"route-up", "route-up /bin/sh", "route-up", reasonProgram},
		{"route-pre-down", "route-pre-down /bin/sh", "route-pre-down", reasonProgram},
		{"ipchange", "ipchange /bin/sh", "ipchange", reasonProgram},
		{"client-connect", "client-connect /bin/sh", "client-connect", reasonProgram},
		{"client-disconnect", "client-disconnect /bin/sh", "client-disconnect", reasonProgram},
		{"learn-address", "learn-address /bin/sh", "learn-address", reasonProgram},
		{"plugin", "plugin /usr/lib/openvpn-plugin-down-root.so", "plugin", reasonCode},
		{"script-security", "script-security 3", "script-security", reasonProgram},
		{"tls-verify", "tls-verify /bin/sh", "tls-verify", reasonProgram},
		{"auth-user-pass-verify", "auth-user-pass-verify /bin/sh via-env", "auth-user-pass-verify", reasonProgram},
		{"setenv", "setenv FOO bar", "setenv", reasonProgram},
		{"setenv-safe", "setenv-safe FOO bar", "setenv-safe", reasonProgram},
		{"management", "management 127.0.0.1 7505", "management", reasonDaemon},
		{"management-hold", "management-hold", "management-hold", reasonDaemon},
		{"management-client-pw-file", "management-client-pw-file /etc/pw", "management-client-pw-file", reasonDaemon},
		{"log", "log /tmp/openvpn.log", "log", reasonFiles},
		{"log-append", "log-append /tmp/openvpn.log", "log-append", reasonFiles},
		{"syslog", "syslog", "syslog", reasonFiles},
		{"cd", "cd /", "cd", reasonFiles},
		{"chroot", "chroot /var/empty", "chroot", reasonFiles},
		{"daemon", "daemon", "daemon", reasonDaemon},
		{"user", "user nobody", "user", reasonDaemon},
		{"group", "group nobody", "group", reasonDaemon},
		{"writepid", "writepid /tmp/pid", "writepid", reasonFiles},
		{"status", "status /tmp/status 10", "status", reasonFiles},
		{"dev-node", "dev-node /dev/tun0", "dev-node", reasonFiles},
		{"config", "config /etc/openvpn/other.conf", "config", reasonFiles},
		{"iproute", "iproute /tmp/iproute.sh", "iproute", reasonProgram},
		{"engine", "engine dynamic", "engine", reasonCode},
		{"providers", "providers legacy default", "providers", reasonCode},
		{"echo", "echo hello", "echo", reasonProgram},
		{"dns-updown", "dns-updown /tmp/x", "dns-updown", reasonProgram},
		{"remap-usr1", "remap-usr1 SIGHUP", "remap-usr1", reasonDaemon},
		{"ifconfig-noexec", "ifconfig-noexec", "ifconfig-noexec", reasonDaemon},
		{"show-ciphers", "show-ciphers", "show-ciphers", reasonDaemon},
		{"pkcs11-id", "pkcs11-id foo", "pkcs11-id", reasonCode},
		{"tmp-dir", "tmp-dir /tmp", "tmp-dir", reasonFiles},
		{"unknown directive", "frobnicate on", "frobnicate", reasonUnknown},
		{"double dash up", "--up /bin/sh", "up", reasonProgram},
		{"upper case up", "UP /bin/sh", "up", reasonProgram},
		{"mixed case script-security", "Script-Security 2", "script-security", reasonProgram},
		{"unknown inline block", "<up>\n/bin/sh\n</up>", "<up>", reasonUnknown},
		{"block-outside-dns", "block-outside-dns", "block-outside-dns", "it is Windows only"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := Parse([]byte(base + tt.line + "\n"))
			if err != nil {
				t.Fatalf("a removable directive must not reject the profile: %v", err)
			}
			if len(parsed.Warnings) != 1 {
				t.Fatalf("warnings = %+v, want exactly one", parsed.Warnings)
			}
			w := parsed.Warnings[0]
			if w.Line != 3 || w.Directive != tt.want || w.Message != "removed: "+tt.reason {
				t.Errorf("warning = %+v, want line 3, directive %q, %q", w, tt.want, "removed: "+tt.reason)
			}
			if got := directiveNames(t, parsed.Content); !reflect.DeepEqual(got, []string{"client", "remote"}) {
				t.Errorf("stored directives = %v, want only client and remote", got)
			}
		})
	}
}

func TestParseNoSmugglingThroughBlocks(t *testing.T) {
	// Each profile hides a dangerous directive in a way that would work if the
	// scanner and openvpn disagreed about where a block ends or what a line is.
	tests := []struct {
		name    string
		profile string
		wantErr bool
	}{
		{
			name:    "directive hidden after an indented closing tag",
			profile: "client\nremote h 1194\n<ca>\nabc\n  </ca>\nup /bin/sh\n",
		},
		{
			name:    "directive after a closing tag that has trailing text",
			profile: "client\nremote h 1194\n<ca>\nabc\n</ca>up /bin/sh\n",
			wantErr: true,
		},
		{
			name:    "up inside a block body stays data",
			profile: "client\nremote h 1194\n<ca>\nup /bin/sh\n</ca>\n",
		},
		{
			name:    "up inside a connection block",
			profile: "client\n<connection>\nremote h 1194\nup /bin/sh\n</connection>\n",
		},
		{
			name:    "closing connection tag inside a nested block",
			profile: "client\n<connection>\nremote h 1194\n<tls-crypt>\nx\n</connection>\nup /bin/sh\n",
			wantErr: true,
		},
		{
			name:    "directive with a comment that hides a second one",
			profile: "client\nremote h 1194 # backup\nup /bin/sh\n",
		},
		{
			name:    "escaped hash at the start of a parameter",
			profile: "client\nremote h 1194 \\#\nup /bin/sh\n",
		},
		{
			name:    "block tag with a trailing comment",
			profile: "client\nremote h 1194\n<ca> # bundle\nabc\n</ca>\nup /bin/sh\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := Parse([]byte(tt.profile))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Parse accepted:\n%s", tt.profile)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range directiveNames(t, parsed.Content) {
				if name == "up" {
					t.Fatalf("stored content still has an up directive:\n%s", parsed.Content)
				}
			}
		})
	}
}

// Whatever the stored content is, OpenVPN must see exactly the directives
// that were validated: no directive outside the allow-list can be written.
func TestStoredContentOnlyHasAllowedNames(t *testing.T) {
	hostile := strings.Join([]string{
		"client", "remote h.example 1194",
		"--UP /bin/sh", "Plugin x", "setenv A b", "  management 1 2",
		"unknown-thing 1", "<ca>", "a", "</ca>", "<weird>", "b", "</weird>",
		"route-up 'x y'", "persist-tun", "\tverb\t3",
	}, "\n") + "\n"
	parsed, err := Parse([]byte(hostile))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range directiveNames(t, parsed.Content) {
		if strings.HasPrefix(name, "<") {
			if !has(allowedBlocks, strings.Trim(name, "<>")) {
				t.Errorf("stored block %s is not allowed", name)
			}
			continue
		}
		if !has(allowedDirectives, name) {
			t.Errorf("stored directive %s is not on the allow-list", name)
		}
	}
}

func TestParseInvalidArguments(t *testing.T) {
	const base = "client\nremote h.example 1194\n"
	tests := []struct{ name, line string }{
		{"remote without host", "remote"},
		{"remote host with a space", `remote "a b" 1194`},
		{"remote host with a shell character", "remote 'a;b' 1194"},
		{"remote with zone", "remote fe80::1%en0 1194"},
		{"remote port zero", "remote h.example 0"},
		{"remote port out of range", "remote h.example 70000"},
		{"remote port name", "remote h.example openvpn"},
		{"remote protocol", "remote h.example 1194 sctp"},
		{"remote too many arguments", "remote h.example 1194 udp extra"},
		{"proto server", "proto tcp-server"},
		{"proto unknown", "proto carrier-pigeon"},
		{"route with a bad netmask", "route 10.0.0.0 255.0.255.0"},
		{"route with an IPv6 network", "route 2001:db8:: 32"},
		{"route-ipv6 with IPv4", "route-ipv6 10.0.0.0/8"},
		{"route-ipv6 without prefix length", "route-ipv6 2001:db8::"},
		{"dhcp-option DNS not an address", "dhcp-option DNS example.com"},
		{"dhcp-option DOMAIN with a slash", "dhcp-option DOMAIN a/b"},
		{"dhcp-option DOMAIN is an address", "dhcp-option DOMAIN 10.0.0.1"},
		{"dev with a path", "dev /dev/tun0"},
		{"dev without a name", "dev"},
		{"pull-filter with a bad action", "pull-filter drop route"},
		{"unclosed quote", `verb "3`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(base + tt.line + "\n"))
			var pe *ParseError
			if !errors.As(err, &pe) || pe.Line != 3 {
				t.Fatalf("Parse(%q) error = %v, want a *ParseError on line 3", tt.line, err)
			}
		})
	}
}

func TestParseRejectsUnusableInput(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		line int
	}{
		{"empty", nil, 0},
		{"comments only", []byte("# nothing here\n"), 0},
		{"no remote", []byte("client\ndev tun\n"), 0},
		{"binary", []byte("\x00\x01\x02\x03\x04"), 1},
		{"binary after text", []byte("client\nremote h 1194\n\xff\xd8\xff\xe0\x00\x10JFIF"), 0},
		{"huge", []byte(strings.Repeat("remote h 1194\n", maxProfileBytes/10)), 0},
		{"huge line", []byte("client\nremote h 1194\n" + strings.Repeat("a", maxDirectiveLineBytes+1) + "\n"), 3},
		{"huge block line", []byte("client\nremote h 1194\n<ca>\n" + strings.Repeat("a", maxBlockLineBytes+1) + "\n</ca>\n"), 4},
		{"lone carriage return", []byte("client\rremote h 1194\n"), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.in)
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("Parse error = %v, want a *ParseError", err)
			}
			if pe.Line != tt.line {
				t.Errorf("error names line %d, want %d: %v", pe.Line, tt.line, err)
			}
		})
	}
}

// openvpn refuses a profile with a line of 255 bytes or more, so what Parse
// writes back must stay below that. The limit is checked on the written form:
// quoting a parameter makes the line longer than the one the user wrote.
func TestParseKeepsLinesWithinWhatOpenVPNReads(t *testing.T) {
	const prefix = "client\nremote h.example 1194\n"
	directive := func(name string, n int) string {
		return name + " " + strings.Repeat("A", n-len(name)-1) + "\n"
	}
	withDirective := func(name string, n int) string { return prefix + directive(name, n) }
	tests := []struct {
		name     string
		in       string
		wantLine int // 0: accepted
	}{
		{"line of 254 bytes", withDirective("verify-x509-name", 254), 0},
		{"line of 255 bytes", withDirective("verify-x509-name", 255), 3},
		{"line of 300 bytes", withDirective("tls-cipher", 300), 3},
		// 253 bytes as written, 255 once the "!" forces quotes around it.
		{"quoting makes the line too long", prefix + "tls-cipher " + strings.Repeat("A!", 121) + "\n", 3},
		// A removed directive is not written back, so its length does not matter.
		{"long unsupported directive", prefix + "frobnicate " + strings.Repeat("A", 300) + "\n", 0},
		{"long directive in a connection block", prefix + "<connection>\n" + directive("tls-cipher", 300) + "</connection>\n", 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := Parse([]byte(tt.in))
			if tt.wantLine == 0 {
				if err != nil {
					t.Fatal(err)
				}
				for _, line := range strings.Split(string(parsed.Content), "\n") {
					if len(line) > maxConfigLineBytes {
						t.Errorf("the written profile has a line of %d bytes: %.40s...", len(line), line)
					}
				}
				return
			}
			var pe *ParseError
			if !errors.As(err, &pe) || pe.Line != tt.wantLine || !strings.Contains(pe.Msg, "254") {
				t.Fatalf("Parse error = %v, want a *ParseError on line %d that names the limit", err, tt.wantLine)
			}
		})
	}
}

// A comment means nothing to openvpn, but it is written back, and a long one
// would stop openvpn from reading the profile.
func TestParseShortensLongComments(t *testing.T) {
	const prefix = "client\nremote h.example 1194\n"
	// "密" is three bytes: the cut must not fall inside it.
	for _, comment := range []string{"# " + strings.Repeat("x", 400), "# " + strings.Repeat("密", 200), "; Name: " + strings.Repeat("x", 300)} {
		parsed, err := Parse([]byte(prefix + comment + "\n<connection>\n" + comment + "\nremote i.example 1194\n</connection>\n"))
		if err != nil {
			t.Fatal(err)
		}
		if !utf8.Valid(parsed.Content) {
			t.Errorf("a comment was cut inside a character: %q", parsed.Content)
		}
		for _, line := range strings.Split(string(parsed.Content), "\n") {
			if len(line) > maxConfigLineBytes {
				t.Errorf("the written profile has a line of %d bytes: %.40s...", len(line), line)
			}
		}
	}
}

// openvpn reads a block in pieces of 254 bytes and ends it at a piece that
// starts with the closing tag. A longer line with the tag inside would end the
// block early for openvpn and not for Parse, and everything after it would be
// read as directives, past the allow-list.
func TestParseRejectsLongBlockLineHidingTheClosingTag(t *testing.T) {
	const prefix = "client\nremote h.example 1194\n"
	for _, tt := range []struct {
		name, in string
		line     int
	}{
		{"tag at the start of the second piece", prefix + "<ca>\n" + strings.Repeat("A", 255) + "</ca>\nplugin /tmp/evil.dylib\n</ca>\n", 4},
		{"tag later in the line", prefix + "<ca>\n" + strings.Repeat("A", 300) + "</ca>\nplugin /tmp/evil.dylib\n</ca>\n", 4},
		{"inside a connection block", prefix + "<connection>\n<ca>\n" + strings.Repeat("A", 255) + "</ca>\nplugin /tmp/evil.dylib\n</ca>\n</connection>\n", 5},
		{"the connection block itself", prefix + "<connection>\n# " + strings.Repeat("A", 260) + "</connection>\nplugin /tmp/evil.dylib\n</connection>\n", 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.in))
			var pe *ParseError
			if !errors.As(err, &pe) || pe.Line != tt.line {
				t.Fatalf("Parse error = %v, want a *ParseError on line %d", err, tt.line)
			}
		})
	}
	// The same text in a short line, or without the tag, is an ordinary body.
	for _, in := range []string{
		prefix + "<ca>\nsee </ca> for the end\n</ca>\n",
		prefix + "<ca>\n" + strings.Repeat("A", 4000) + "\n</ca>\n",
	} {
		if _, err := Parse([]byte(in)); err != nil {
			t.Errorf("Parse(%.60q...) = %v", in, err)
		}
	}
}

func TestParseReadsDNSOptions(t *testing.T) {
	const prefix = "client\nremote h.example 1194\n"
	in := prefix + "dhcp-option DNS 10.9.0.1\ndns server 1 address 10.8.0.1 10.8.0.2\ndns server 1 resolve-domains corp.example\ndns server 2 address 10.8.0.1\ndns search-domains corp.example\n"
	p, err := parseProfile([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if want := addrs("10.9.0.1", "10.8.0.1", "10.8.0.2"); !reflect.DeepEqual(p.Summary.DNSServers, want) {
		t.Errorf("DNSServers = %v, want %v", p.Summary.DNSServers, want)
	}
	if len(p.Warnings) != 0 || string(p.Content) != in {
		t.Errorf("warnings %v, content changed: %q", p.Warnings, p.Content)
	}
	if got := p.dns.intents(false); len(got) != 2 {
		t.Errorf("intents = %+v", got)
	}

	// An option the profile cannot use is reported and kept; its server is
	// not listed.
	p, err = parseProfile([]byte(prefix + "dns server 1 address 10.8.0.1\ndns server 1 transport DoT\ndns server 2 address 10.8.0.3:853\ndns bogus x\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Summary.DNSServers) != 0 {
		t.Errorf("DNSServers = %v, want none", p.Summary.DNSServers)
	}
	if len(p.Warnings) != 3 {
		t.Fatalf("warnings = %+v", p.Warnings)
	}
	for i, want := range []string{"transport DoT", "port 853", "unknown type"} {
		if w := p.Warnings[i]; w.Directive != "dns" || !strings.HasPrefix(w.Message, "not applied: ") || !strings.Contains(w.Message, want) {
			t.Errorf("warning %d = %+v, want one about %q", i, w, want)
		}
	}
	if !strings.Contains(string(p.Content), "dns bogus x") {
		t.Errorf("the dns lines were dropped: %q", p.Content)
	}
}

func TestParseWarnsAboutWhatMakesAProfileUnusable(t *testing.T) {
	const prefix = "client\nremote h.example 1194\n"
	tests := []struct {
		name, line, directive, message string
		kept                           bool
	}{
		{"tap device", "dev tap0", "dev", "no tap device", true},
		{"tap", "dev tap", "dev", "no tap device", true},
		{"one-time code", "static-challenge \"Enter code\" 1", "static-challenge", reasonOTP, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := parseProfile([]byte(prefix + tt.line + "\n"))
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Warnings) != 1 || p.Warnings[0].Directive != tt.directive || !strings.Contains(p.Warnings[0].Message, tt.message) {
				t.Errorf("warnings = %+v, want one for %s about %q", p.Warnings, tt.directive, tt.message)
			}
			if kept := strings.Contains(string(p.Content), tt.line); kept != tt.kept {
				t.Errorf("kept = %v, want %v", kept, tt.kept)
			}
		})
	}
	if p, err := parseProfile([]byte(prefix + "dev tun\n")); err != nil || len(p.Warnings) != 0 {
		t.Errorf("dev tun: %v, warnings %+v", err, p.Warnings)
	}
}

func TestParseHugeBlockWithinLimitIsAccepted(t *testing.T) {
	profile := "client\nremote h.example 1194\n<pkcs12>\n" + strings.Repeat("A", 40<<10) + "\n</pkcs12>\n"
	if _, err := Parse([]byte(profile)); err != nil {
		t.Fatalf("a 40 KiB inline blob is legitimate: %v", err)
	}
}

func TestParseRoutesAndRedirect(t *testing.T) {
	tests := []struct {
		name         string
		profile      string
		routes       []string
		redirect     bool
		warnings     int
		wantWarnPart string
	}{
		{
			name:    "route defaults",
			profile: "route 10.1.0.5\nroute 10.2.0.0 255.255.0.0\nroute 10.3.0.0 255.255.255.0 vpn_gateway 5\nroute-ipv6 fd00::/64\n",
			routes:  []string{"10.1.0.5/32", "10.2.0.0/16", "10.3.0.0/24", "fd00::/64"},
		},
		{
			name:    "duplicates and host bits",
			profile: "route 10.2.0.7 255.255.0.0\nroute 10.2.0.0 255.255.0.0\n",
			routes:  []string{"10.2.0.0/16"},
		},
		{
			name:         "route around the tunnel is not a tunnel route",
			profile:      "route 10.9.0.0 255.255.0.0 net_gateway\nroute 10.8.0.0 255.255.0.0\n",
			routes:       []string{"10.8.0.0/16"},
			warnings:     1,
			wantWarnPart: "goes around the tunnel",
		},
		{
			name:         "route to a host name is left to openvpn",
			profile:      "route intranet.example 255.255.255.255\n",
			warnings:     1,
			wantWarnPart: "not an IP address",
		},
		{
			name:     "redirect-gateway",
			profile:  "redirect-gateway def1\n",
			redirect: true,
		},
		{
			name:     "redirect-gateway ipv6 only",
			profile:  "redirect-gateway !ipv4 ipv6\n",
			redirect: true,
		},
		{
			name:    "redirect-gateway with !ipv4 only redirects nothing",
			profile: "redirect-gateway !ipv4\n",
		},
		{
			name:    "pull-filter ignore takes redirect-gateway back",
			profile: "redirect-gateway def1\npull-filter ignore \"redirect-gateway\"\n",
		},
		{
			name:    "pull-filter reject with a prefix takes it back",
			profile: "redirect-gateway def1\npull-filter reject redirect\n",
		},
		{
			name:     "pull-filter accept does not",
			profile:  "redirect-gateway def1\npull-filter accept redirect-gateway\npull-filter ignore redirect\n",
			redirect: true,
		},
		{
			name:    "redirect-private is not a redirect",
			profile: "redirect-private def1\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := Parse([]byte("client\nremote h.example 1194\n" + tt.profile))
			if err != nil {
				t.Fatal(err)
			}
			if got, want := parsed.Summary.Routes, mustPrefixes(tt.routes...); !reflect.DeepEqual(got, want) && !(len(got) == 0 && len(want) == 0) {
				t.Errorf("Routes = %v, want %v", got, want)
			}
			if parsed.Summary.RedirectsDefaultRoute != tt.redirect {
				t.Errorf("RedirectsDefaultRoute = %v, want %v", parsed.Summary.RedirectsDefaultRoute, tt.redirect)
			}
			if len(parsed.Warnings) != tt.warnings {
				t.Fatalf("warnings = %+v, want %d", parsed.Warnings, tt.warnings)
			}
			if tt.wantWarnPart != "" && !strings.Contains(parsed.Warnings[0].Message, tt.wantWarnPart) {
				t.Errorf("warning %q lacks %q", parsed.Warnings[0].Message, tt.wantWarnPart)
			}
		})
	}
}

func TestParseCredentialsAndAddresses(t *testing.T) {
	tests := []struct {
		name      string
		extra     string
		wantCreds bool
		addrs     []string
	}{
		{"none", "", false, nil},
		{"auth-user-pass asks", "auth-user-pass\n", true, nil},
		{"inline credentials do not ask", "auth-user-pass\n<auth-user-pass>\nu\np\n</auth-user-pass>\n", false, nil},
		{"static ifconfig p2p", "ifconfig 10.8.0.2 10.8.0.1\n", false, []string{"10.8.0.2/32"}},
		{"static ifconfig with netmask", "ifconfig 10.8.0.2 255.255.255.0\n", false, []string{"10.8.0.2/24"}},
		{"static ifconfig-ipv6", "ifconfig-ipv6 fd00::2/64 fd00::1\n", false, []string{"fd00::2/64"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := Parse([]byte("client\nremote h.example 1194\n" + tt.extra))
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Summary.RequiresCredentials != tt.wantCreds {
				t.Errorf("RequiresCredentials = %v", parsed.Summary.RequiresCredentials)
			}
			got := parsed.Summary.Addresses
			if want := mustPrefixes(tt.addrs...); !reflect.DeepEqual(got, want) && !(len(got) == 0 && len(want) == 0) {
				t.Errorf("Addresses = %v, want %v", got, want)
			}
		})
	}
}

func TestParseEndpointDefaults(t *testing.T) {
	tests := []struct {
		name    string
		profile string
		want    []tunnel.Endpoint
	}{
		{
			name:    "udp 1194 by default",
			profile: "remote a.example\n",
			want:    []tunnel.Endpoint{{Host: "a.example", Port: 1194, Protocol: "udp"}},
		},
		{
			name:    "proto and port directives apply to remotes without them, in any order",
			profile: "remote a.example\nremote b.example 8443\nport 4443\nproto tcp-client\n",
			want: []tunnel.Endpoint{
				{Host: "a.example", Port: 4443, Protocol: "tcp"},
				{Host: "b.example", Port: 8443, Protocol: "tcp"},
			},
		},
		{
			name:    "remote overrides the defaults",
			profile: "proto tcp\nremote a.example 53 udp6\n",
			want:    []tunnel.Endpoint{{Host: "a.example", Port: 53, Protocol: "udp"}},
		},
		{
			name:    "connection block inherits then overrides",
			profile: "proto udp\nrport 1195\n<connection>\nremote a.example\n</connection>\n<connection>\nremote b.example\nproto tcp\nrport 443\n</connection>\n",
			want: []tunnel.Endpoint{
				{Host: "a.example", Port: 1195, Protocol: "udp"},
				{Host: "b.example", Port: 443, Protocol: "tcp"},
			},
		},
		{
			name:    "duplicates are listed once",
			profile: "remote a.example 1194\nremote a.example 1194 udp\nremote a.example 1194 tcp\n",
			want: []tunnel.Endpoint{
				{Host: "a.example", Port: 1194, Protocol: "udp"},
				{Host: "a.example", Port: 1194, Protocol: "tcp"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := Parse([]byte(tt.profile))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(parsed.Summary.Endpoints, tt.want) {
				t.Errorf("Endpoints = %+v\nwant        %+v", parsed.Summary.Endpoints, tt.want)
			}
		})
	}
}

func TestParseSuggestedName(t *testing.T) {
	tests := []struct{ comment, want string }{
		{"# Name: Office", "Office"},
		{"# name = Office VPN  ", "Office VPN"},
		{"; profile name: Lab", "Lab"},
		{"# OVPN_ACCESS_SERVER_PROFILE=alice@example.com", "alice@example.com"},
		{"# Generated by a tool", ""},
		{"# hostname: example", ""},
		{"# " + strings.Repeat("n", 100), ""},
		{"# Name: " + strings.Repeat("é", 100), strings.Repeat("é", maxSuggestedName)},
	}
	for _, tt := range tests {
		t.Run(tt.comment, func(t *testing.T) {
			parsed, err := Parse([]byte(tt.comment + "\nclient\nremote h.example 1194\n"))
			if err != nil {
				t.Fatal(err)
			}
			if parsed.SuggestedName != tt.want {
				t.Errorf("SuggestedName = %q, want %q", parsed.SuggestedName, tt.want)
			}
		})
	}
}

func TestParseNeedsLZO(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"comp-lzo yes", true}, {"comp-lzo", true}, {"comp-lzo adaptive", true},
		{"comp-lzo no", false}, {"compress lzo", true}, {"compress lz4-v2", false},
		{"compress", false}, {"", false},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			p, err := parseProfile([]byte("client\nremote h.example 1194\n" + tt.line + "\n"))
			if err != nil {
				t.Fatal(err)
			}
			if p.needsLZO != tt.want {
				t.Errorf("needsLZO = %v", p.needsLZO)
			}
		})
	}
}

func TestParseProxyHostsAreBypassCandidates(t *testing.T) {
	p, err := parseProfile([]byte("client\nremote h.example 1194\nhttp-proxy proxy.example 8080\nsocks-proxy 192.0.2.9 1080\n"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"proxy.example", "192.0.2.9"}; !reflect.DeepEqual(p.proxyHosts, want) {
		t.Errorf("proxyHosts = %v, want %v", p.proxyHosts, want)
	}
	if len(p.Summary.Endpoints) != 1 {
		t.Errorf("a proxy is not a remote: %+v", p.Summary.Endpoints)
	}
}

func TestParseKeepsOwnersCipherSettingsVerbatim(t *testing.T) {
	// The values a profile sets for compression, ciphers and digest are the
	// server's business; Parse must never rewrite them.
	in := "client\nremote h.example 1194\ncomp-lzo yes\ncipher AES-128-CBC\ndata-ciphers AES-128-CBC\nauth SHA1\ntls-cipher DEFAULT:@SECLEVEL=0\n"
	parsed, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if string(parsed.Content) != in {
		t.Errorf("content changed:\n%s\nwant\n%s", parsed.Content, in)
	}
}

func readFixtureForFuzz(name string) ([]byte, error) {
	return os.ReadFile(filepath.Join("testdata", name))
}
