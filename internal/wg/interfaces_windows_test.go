package wg

import (
	"net/netip"
	"slices"
	"testing"

	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/tun/tuntest"

	"github.com/KoukeNeko/Plaitway/internal/winiface"
)

func TestMTUFamilies(t *testing.T) {
	v4 := netip.MustParsePrefix("10.6.0.2/24")
	v6 := netip.MustParsePrefix("fd00::2/64")
	tests := []struct {
		name  string
		addrs []netip.Prefix
		mtu   int
		want  []winiface.Family
	}{
		{"IPv4 only", []netip.Prefix{v4}, 1420, []winiface.Family{winiface.IPv4}},
		{"IPv6 only", []netip.Prefix{v6}, 1420, []winiface.Family{winiface.IPv6}},
		{"both", []netip.Prefix{v4, v6}, 1420, []winiface.Family{winiface.IPv4, winiface.IPv6}},
		{"IPv6 at its minimum", []netip.Prefix{v6}, 1280, []winiface.Family{winiface.IPv6}},
		{"IPv6 below its minimum", []netip.Prefix{v4, v6}, 1279, []winiface.Family{winiface.IPv4}},
		{"no addresses", nil, 1420, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mtuFamilies(tt.addrs, tt.mtu); !slices.Equal(got, tt.want) {
				t.Errorf("mtuFamilies = %v, want %v", got, tt.want)
			}
		})
	}
}

// luidDevice is a tunnel device that, like wintun's, can say the LUID of its
// adapter.
type luidDevice struct {
	tun.Device
	luid uint64
}

func (d luidDevice) LUID() uint64 { return d.luid }

func TestNativeLUIDReadsTheDeviceOrIsZero(t *testing.T) {
	device := tuntest.NewChannelTUN().TUN()
	if got := nativeLUID(device); got != 0 {
		t.Errorf("a device without a LUID gave %#x, want 0", got)
	}
	const luid uint64 = 0x0006_0000_0123_4567
	if got := nativeLUID(luidDevice{device, luid}); got != luid {
		t.Errorf("nativeLUID = %#x, want %#x", got, luid)
	}
}
