package wg

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/tuntest"
)

// hexKey is how UAPI writes the key that fixedKey(fill) encodes in base64.
func hexKey(fill byte) string { return strings.Repeat(fmt.Sprintf("%02x", fill), 32) }

func TestUAPIConfig(t *testing.T) {
	tests := []struct {
		name           string
		profile        string
		excludePrivate bool
		endpoints      []netip.AddrPort
		want           string
	}{
		{
			name: "everything",
			profile: fill(`[Interface]
PrivateKey = KEYA
ListenPort = 51820
FwMark = 0x10
[Peer]
PublicKey = KEYB
PresharedKey = KEYC
Endpoint = vpn.example.com:51820
AllowedIPs = 10.0.0.0/8, ::/0
PersistentKeepalive = 25
`),
			endpoints: []netip.AddrPort{netip.MustParseAddrPort("203.0.113.9:51820")},
			want: "private_key=" + hexKey(1) + "\nlisten_port=51820\nfwmark=16\nreplace_peers=true\n" +
				"public_key=" + hexKey(2) + "\npreshared_key=" + hexKey(3) + "\nendpoint=203.0.113.9:51820\n" +
				"persistent_keepalive_interval=25\nreplace_allowed_ips=true\nallowed_ip=10.0.0.0/8\nallowed_ip=::/0\n",
		},
		{
			name: "defaults leave the optional lines out",
			profile: fill(`[Interface]
PrivateKey = KEYA
[Peer]
PublicKey = KEYB
AllowedIPs = 192.168.1.0/24
`),
			endpoints: []netip.AddrPort{{}},
			want: "private_key=" + hexKey(1) + "\nreplace_peers=true\n" +
				"public_key=" + hexKey(2) + "\npersistent_keepalive_interval=0\nreplace_allowed_ips=true\nallowed_ip=192.168.1.0/24\n",
		},
		{
			name: "two peers and an IPv6 endpoint",
			profile: fill(`[Interface]
PrivateKey = KEYA
[Peer]
PublicKey = KEYB
Endpoint = [2001:db8::1]:1000
[Peer]
PublicKey = KEYC
Endpoint = 198.51.100.7:2000
AllowedIPs = 172.16.0.0/12
`),
			endpoints: []netip.AddrPort{netip.MustParseAddrPort("[2001:db8::1]:1000"), netip.MustParseAddrPort("198.51.100.7:2000")},
			want: "private_key=" + hexKey(1) + "\nreplace_peers=true\n" +
				"public_key=" + hexKey(2) + "\nendpoint=[2001:db8::1]:1000\npersistent_keepalive_interval=0\nreplace_allowed_ips=true\n" +
				"public_key=" + hexKey(3) + "\nendpoint=198.51.100.7:2000\npersistent_keepalive_interval=0\nreplace_allowed_ips=true\nallowed_ip=172.16.0.0/12\n",
		},
		{
			name: "excluding private ranges takes them out of every peer's AllowedIPs",
			profile: fill(`[Interface]
PrivateKey = KEYA
[Peer]
PublicKey = KEYB
AllowedIPs = 0.0.0.0/0, ::/0
[Peer]
PublicKey = KEYC
AllowedIPs = 192.168.1.0/24, 203.0.113.0/24
[Peer]
PublicKey = KEYA
AllowedIPs = 10.6.0.0/24
`),
			excludePrivate: true,
			endpoints:      []netip.AddrPort{{}, {}, {}},
			want: "private_key=" + hexKey(1) + "\nreplace_peers=true\n" +
				"public_key=" + hexKey(2) + "\npersistent_keepalive_interval=0\nreplace_allowed_ips=true\n" + allowedIPLines(slices.Concat(publicV4, publicV6)) +
				"public_key=" + hexKey(3) + "\npersistent_keepalive_interval=0\nreplace_allowed_ips=true\nallowed_ip=203.0.113.0/24\n" +
				"public_key=" + hexKey(1) + "\npersistent_keepalive_interval=0\nreplace_allowed_ips=true\n",
		},
		{
			// The device has to accept what the routes send: the tunnel's nameserver is in a private range.
			name: "excluding private ranges keeps a DNS server the peer is allowed",
			profile: fill(`[Interface]
PrivateKey = KEYA
DNS = 10.6.0.1
[Peer]
PublicKey = KEYB
AllowedIPs = 0.0.0.0/0
[Peer]
PublicKey = KEYC
AllowedIPs = 203.0.113.0/24
`),
			excludePrivate: true,
			endpoints:      []netip.AddrPort{{}, {}},
			want: "private_key=" + hexKey(1) + "\nreplace_peers=true\n" +
				"public_key=" + hexKey(2) + "\npersistent_keepalive_interval=0\nreplace_allowed_ips=true\n" + allowedIPLines(mergePrefixes(slices.Concat(publicV4, pfx("10.6.0.1/32")))) +
				"public_key=" + hexKey(3) + "\npersistent_keepalive_interval=0\nreplace_allowed_ips=true\nallowed_ip=203.0.113.0/24\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := parseProfile([]byte(tt.profile))
			if err != nil {
				t.Fatal(err)
			}
			p.excludePrivate = tt.excludePrivate
			if got := p.uapiConfig(tt.endpoints); got != tt.want {
				t.Errorf("uapiConfig:\n got %q\nwant %q", got, tt.want)
			}
		})
	}
}

func allowedIPLines(prefixes []netip.Prefix) string {
	var b strings.Builder
	for _, prefix := range prefixes {
		fmt.Fprintf(&b, "allowed_ip=%s\n", prefix)
	}
	return b.String()
}

func TestEndpointUpdate(t *testing.T) {
	p, err := parseProfile([]byte(fill("[Interface]\nPrivateKey = KEYA\n[Peer]\nPublicKey = KEYB\n[Peer]\nPublicKey = KEYC\n")))
	if err != nil {
		t.Fatal(err)
	}
	a, b, c := netip.MustParseAddrPort("192.0.2.1:1"), netip.MustParseAddrPort("192.0.2.2:1"), netip.MustParseAddrPort("192.0.2.3:1")
	tests := []struct {
		name        string
		old, update []netip.AddrPort
		want        string
	}{
		{"nothing changed", []netip.AddrPort{a, b}, []netip.AddrPort{a, b}, ""},
		{"one peer moved", []netip.AddrPort{a, b}, []netip.AddrPort{a, c}, "public_key=" + hexKey(3) + "\nupdate_only=true\nendpoint=192.0.2.3:1\n"},
		{"both moved", []netip.AddrPort{a, b}, []netip.AddrPort{b, c},
			"public_key=" + hexKey(2) + "\nupdate_only=true\nendpoint=192.0.2.2:1\npublic_key=" + hexKey(3) + "\nupdate_only=true\nendpoint=192.0.2.3:1\n"},
		{"a peer without endpoint stays that way", []netip.AddrPort{a, {}}, []netip.AddrPort{a, {}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.endpointUpdate(tt.old, tt.update); got != tt.want {
				t.Errorf("endpointUpdate = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestUAPIConfigIsAcceptedByWireGuard feeds the generated text to a real
// wireguard-go device and reads the configuration back.
func TestUAPIConfigIsAcceptedByWireGuard(t *testing.T) {
	checkLeaks(t)
	p, err := parseProfile([]byte(fill(`[Interface]
PrivateKey = KEYA
ListenPort = 0
[Peer]
PublicKey = KEYB
PresharedKey = KEYC
AllowedIPs = 10.0.0.0/8, 192.168.1.0/24, ::/0
PersistentKeepalive = 25
`)))
	if err != nil {
		t.Fatal(err)
	}
	silent := &device.Logger{Verbosef: device.DiscardLogf, Errorf: device.DiscardLogf}
	dev := device.NewDevice(tuntest.NewChannelTUN().TUN(), conn.NewDefaultBind(), silent)
	defer dev.Close()
	if err := dev.IpcSet(p.uapiConfig([]netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:51820")})); err != nil {
		t.Fatalf("IpcSet: %v", err)
	}
	dump, err := dev.IpcGet()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"public_key=" + hexKey(2), "preshared_key=" + hexKey(3), "endpoint=127.0.0.1:51820",
		"persistent_keepalive_interval=25", "allowed_ip=10.0.0.0/8", "allowed_ip=192.168.1.0/24", "allowed_ip=::/0",
	} {
		if !strings.Contains(dump, want) {
			t.Errorf("device state lacks %q:\n%s", want, redactKeys(dump))
		}
	}

	// A new endpoint reaches the device through endpointUpdate.
	old := []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:51820")}
	moved := []netip.AddrPort{netip.MustParseAddrPort("127.0.0.2:51821")}
	if err := dev.IpcSet(p.endpointUpdate(old, moved)); err != nil {
		t.Fatalf("IpcSet(endpointUpdate): %v", err)
	}
	dump, _ = dev.IpcGet()
	if !strings.Contains(dump, "endpoint=127.0.0.2:51821") || strings.Contains(dump, "endpoint=127.0.0.1:51820") {
		t.Errorf("endpoint was not replaced:\n%s", redactKeys(dump))
	}
}

func redactKeys(dump string) string {
	var out []string
	for _, line := range strings.Split(dump, "\n") {
		if strings.HasPrefix(line, "private_key=") {
			line = "private_key=<redacted>"
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func TestParseDeviceStats(t *testing.T) {
	tests := []struct {
		name    string
		dump    string
		want    deviceStats
		wantErr bool
	}{
		{"empty", "", deviceStats{}, false},
		{"no handshake yet", "private_key=00\npublic_key=01\nlast_handshake_time_sec=0\nlast_handshake_time_nsec=0\ntx_bytes=148\nrx_bytes=0\n",
			deviceStats{txBytes: 148}, false},
		{"listen port", "private_key=00\nlisten_port=51820\npublic_key=01\n", deviceStats{listenPort: 51820}, false},
		{"listen port that is not a number", "listen_port=abc\n", deviceStats{}, true},
		{"one peer", "public_key=01\nlast_handshake_time_sec=1700000000\nlast_handshake_time_nsec=500\ntx_bytes=10\nrx_bytes=20\nallowed_ip=10.0.0.0/8\n",
			deviceStats{rxBytes: 20, txBytes: 10, lastHandshake: time.Unix(1700000000, 500)}, false},
		{"two peers sum the counters and take the newest handshake",
			"public_key=01\nlast_handshake_time_sec=1700000100\nlast_handshake_time_nsec=0\ntx_bytes=1\nrx_bytes=2\n" +
				"public_key=02\nlast_handshake_time_sec=1700000200\nlast_handshake_time_nsec=7\ntx_bytes=10\nrx_bytes=20\n",
			deviceStats{rxBytes: 22, txBytes: 11, lastHandshake: time.Unix(1700000200, 7)}, false},
		{"a peer that never shook hands does not hide the other",
			"public_key=01\nlast_handshake_time_sec=1700000100\nlast_handshake_time_nsec=0\n" +
				"public_key=02\nlast_handshake_time_sec=0\nlast_handshake_time_nsec=0\n",
			deviceStats{lastHandshake: time.Unix(1700000100, 0)}, false},
		{"counter that is not a number", "tx_bytes=abc\n", deviceStats{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDeviceStats(tt.dump)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if got.listenPort != tt.want.listenPort || got.rxBytes != tt.want.rxBytes || got.txBytes != tt.want.txBytes ||
				!got.lastHandshake.Equal(tt.want.lastHandshake) {
				t.Errorf("stats = %+v, want %+v", got, tt.want)
			}
		})
	}
}
