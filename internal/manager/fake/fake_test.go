package fake

import (
	"context"
	"encoding/base64"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// The shape of a router-generated profile: split tunnel, user name and
// password asked at connect, TCP.
const asusLike = `client
dev tun
proto tcp-client
remote vpn.example.com 1194
nobind
auth-user-pass
route 192.168.1.0 255.255.255.0
pull-filter ignore "redirect-gateway"
<ca>
MIIB
</ca>
`

const wireGuardFull = `[Interface]
PrivateKey = abc
Address = 10.6.0.2/32, fd00::2/128
DNS = 10.6.0.1, 1.1.1.1
PostUp = iptables -A FORWARD -j ACCEPT
[Peer]
PublicKey = def
Endpoint = 203.0.113.5:51820
AllowedIPs = 0.0.0.0/0, ::/0
`

func openVPNBackend() tunnel.Backend   { return Backends(Config{})[0] }
func wireGuardBackend() tunnel.Backend { return Backends(Config{})[1] }

func TestParseOpenVPNSummary(t *testing.T) {
	got, err := openVPNBackend().Parse([]byte(asusLike))
	if err != nil {
		t.Fatal(err)
	}
	s := got.Summary
	if len(s.Endpoints) != 1 || s.Endpoints[0] != (tunnel.Endpoint{Host: "vpn.example.com", Port: 1194, Protocol: "tcp"}) {
		t.Errorf("endpoints = %+v", s.Endpoints)
	}
	if !slices.Equal(s.Routes, []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")}) {
		t.Errorf("routes = %v", s.Routes)
	}
	if !s.RequiresCredentials || s.RedirectsDefaultRoute {
		t.Errorf("requires credentials %v, redirects %v; want true, false", s.RequiresCredentials, s.RedirectsDefaultRoute)
	}
	if string(got.Content) != asusLike || len(got.Warnings) != 0 {
		t.Errorf("a clean profile must be stored as it is, with no warnings: %+v", got.Warnings)
	}
}

func TestParseWireGuardSummary(t *testing.T) {
	got, err := wireGuardBackend().Parse([]byte(wireGuardFull))
	if err != nil {
		t.Fatal(err)
	}
	s := got.Summary
	if len(s.Endpoints) != 1 || s.Endpoints[0] != (tunnel.Endpoint{Host: "203.0.113.5", Port: 51820}) {
		t.Errorf("endpoints = %+v", s.Endpoints)
	}
	if len(s.Addresses) != 2 || s.Addresses[0] != netip.MustParsePrefix("10.6.0.2/32") {
		t.Errorf("addresses = %v", s.Addresses)
	}
	if len(s.DNSServers) != 2 || s.DNSServers[0] != netip.MustParseAddr("10.6.0.1") {
		t.Errorf("dns = %v", s.DNSServers)
	}
	if !s.RedirectsDefaultRoute || len(s.Routes) != 2 || s.RequiresCredentials {
		t.Errorf("redirects %v routes %v credentials %v", s.RedirectsDefaultRoute, s.Routes, s.RequiresCredentials)
	}
}

func TestParseWireGuardPublicKey(t *testing.T) {
	parse := func(text string) string {
		t.Helper()
		got, err := wireGuardBackend().Parse([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		return got.Summary.PublicKey
	}

	key := parse(wireGuardFull)
	if raw, err := base64.StdEncoding.DecodeString(key); err != nil || len(key) != 44 || len(raw) != 32 {
		t.Fatalf("public key %q is not 44 characters of base64 for 32 bytes (%v)", key, err)
	}
	if again := parse(wireGuardFull); again != key {
		t.Errorf("the same text gave %q and %q", key, again)
	}
	// It follows the private key, not the rest of the text.
	if got := parse(strings.Replace(wireGuardFull, "203.0.113.5", "203.0.113.6", 1)); got != key {
		t.Errorf("editing the endpoint changed the public key to %q", got)
	}
	if got := parse(strings.Replace(wireGuardFull, "PrivateKey = abc", "PrivateKey = abd", 1)); got == key {
		t.Error("editing the private key left the public key as it was")
	}
	// A text without a private key still gets one, derived from the text.
	bare := parse("[Interface]\nAddress = 10.6.0.2/32\n")
	if len(bare) != 44 || bare == key {
		t.Errorf("public key of a text without a private key = %q", bare)
	}

	// Only WireGuard has one.
	ovpn, err := openVPNBackend().Parse([]byte(asusLike))
	if err != nil || ovpn.Summary.PublicKey != "" {
		t.Errorf("OpenVPN public key = %q, %v", ovpn.Summary.PublicKey, err)
	}
}

func TestParseStripsScriptDirectivesWithWarnings(t *testing.T) {
	ovpn, err := openVPNBackend().Parse([]byte("client\nremote h 1\nscript-security 2\nUP /bin/evil\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ovpn.Warnings) != 2 || ovpn.Warnings[0].Line != 3 || ovpn.Warnings[0].Directive != "script-security" || ovpn.Warnings[1].Directive != "up" {
		t.Errorf("warnings = %+v", ovpn.Warnings)
	}
	if strings.Contains(string(ovpn.Content), "evil") || strings.Contains(string(ovpn.Content), "script-security") {
		t.Errorf("content still has the directives: %q", ovpn.Content)
	}

	wg, err := wireGuardBackend().Parse([]byte(wireGuardFull))
	if err != nil {
		t.Fatal(err)
	}
	if len(wg.Warnings) != 1 || wg.Warnings[0].Line != 5 || wg.Warnings[0].Directive != "PostUp" {
		t.Errorf("warnings = %+v", wg.Warnings)
	}
	if strings.Contains(string(wg.Content), "iptables") {
		t.Errorf("content still has the hook: %q", wg.Content)
	}
}

func TestParseRejects(t *testing.T) {
	for name, content := range map[string]string{
		"empty":  "",
		"blank":  " \n\t\n",
		"marker": "client\n# fake: reject\nremote h 1\n",
	} {
		for _, b := range Backends(Config{}) {
			if _, err := b.Parse([]byte(content)); err == nil {
				t.Errorf("%s: kind %d accepted the profile", name, b.Kind)
			}
		}
	}
	if _, err := openVPNBackend().Parse([]byte("up /bin/evil\n")); err == nil {
		t.Error("a profile with nothing left after removing its scripts was accepted")
	}
	_, err := openVPNBackend().Parse([]byte("client\n# fake: reject\n"))
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("the error must name the line: %v", err)
	}
}

func TestParseAcceptsAnyText(t *testing.T) {
	for _, b := range Backends(Config{}) {
		got, err := b.Parse([]byte("just some words\n"))
		if err != nil || string(got.Content) != "just some words\n" {
			t.Errorf("kind %d: %+v, %v", b.Kind, got, err)
		}
	}
}

func TestNeedsCredentialsMarkerCountsAsRequiringThem(t *testing.T) {
	got, err := openVPNBackend().Parse([]byte("client\n# fake: needs-credentials\n"))
	if err != nil || !got.Summary.RequiresCredentials {
		t.Fatalf("%+v, %v", got.Summary, err)
	}
}

type noopNetwork struct{}

func (noopNetwork) Announce(tunnel.Intent) error { return nil }
func (noopNetwork) Withdraw(tunnel.OwnerID)      {}

func newTestEngine(t *testing.T, content string) tunnel.Engine {
	t.Helper()
	eng, err := openVPNBackend().New(tunnel.Spec{Owner: "p", Content: []byte(content)}, tunnel.Deps{Network: noopNetwork{}, Log: func(tunnel.LogLevel, string) {}})
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

func TestEngineStopIsIdempotentAndClosesStatus(t *testing.T) {
	eng := newTestEngine(t, "client\nremote h 1\n")
	if err := eng.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := eng.Start(context.Background()); err == nil {
		t.Fatal("a second Start succeeded")
	}
	for range 2 {
		if err := eng.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	timeout := time.After(time.Second)
	for {
		select {
		case _, ok := <-eng.Status():
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("the Status channel was not closed by Stop")
		}
	}
}

func TestEngineStoppedBeforeStartNeverRuns(t *testing.T) {
	eng := newTestEngine(t, "client\nremote h 1\n")
	if err := eng.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := eng.Start(context.Background()); err == nil {
		t.Fatal("Start after Stop succeeded")
	}
}

func TestProvideCredentialsOutsideTheRequestIsAnError(t *testing.T) {
	eng := newTestEngine(t, "client\nremote h 1\n")
	if err := eng.ProvideCredentials(tunnel.CredentialUserPassword, "u", "p"); err == nil {
		t.Fatal("credentials accepted by an engine that did not ask")
	}
}
