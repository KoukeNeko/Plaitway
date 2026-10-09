package linux

import (
	"fmt"
	"testing"
	"testing/fstest"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// kindName makes failures readable: osnet.LinkKind has no String method.
func kindName(k osnet.LinkKind) string {
	switch k {
	case osnet.LinkOther:
		return "LinkOther"
	case osnet.LinkWiFi:
		return "LinkWiFi"
	case osnet.LinkEthernet:
		return "LinkEthernet"
	}
	return fmt.Sprintf("LinkKind(%d)", k)
}

// sysfs is the part of /sys/class/net that a few devices of a laptop with a
// dock have, as the kernel lays it out: a physical device has a "device" link, a
// wireless one a "wireless" directory (wireless extensions) or a "phy80211"
// link (cfg80211), and all have their hardware type in "type".
func sysfs() fstest.MapFS {
	dir := &fstest.MapFile{Mode: 0o755 | 1<<31}
	return fstest.MapFS{
		"enp0s31f6/type":   {Data: []byte("1\n")},
		"enp0s31f6/device": dir,
		"enp0s31f6/uevent": {Data: []byte("INTERFACE=enp0s31f6\nIFINDEX=2\n")},

		"wlp0s20f3/type":     {Data: []byte("1\n")},
		"wlp0s20f3/device":   dir,
		"wlp0s20f3/phy80211": dir,
		"wlp0s20f3/uevent":   {Data: []byte("DEVTYPE=wlan\nINTERFACE=wlp0s20f3\n")},

		"wlan1/type":     {Data: []byte("1\n")},
		"wlan1/device":   dir,
		"wlan1/wireless": dir, // a driver with wireless extensions only

		"enx001122334455/type":   {Data: []byte("1\n")}, // a USB Ethernet adapter
		"enx001122334455/device": dir,

		"wwan0/type":   {Data: []byte("1\n")}, // a modem in Ethernet mode
		"wwan0/device": dir,
		"wwan0/uevent": {Data: []byte("DEVTYPE=wwan\nINTERFACE=wwan0\n")},

		"wwan1/type":   {Data: []byte("519\n")}, // a modem in raw-IP mode
		"wwan1/device": dir,

		"docker0/type":   {Data: []byte("1\n")},
		"docker0/uevent": {Data: []byte("DEVTYPE=bridge\nINTERFACE=docker0\n")},
		"veth1a2b/type":  {Data: []byte("1\n")},
		"wg0/type":       {Data: []byte("65534\n")},
		"tun0/type":      {Data: []byte("65534\n")},
		"lo/type":        {Data: []byte("772\n")},
		"eth9/device":    dir, // no type file: not an Ethernet device we can vouch for
	}
}

func TestLinkKinds(t *testing.T) {
	kinds := newLinkKinds(sysfs())
	tests := []struct {
		name string
		want osnet.LinkKind
	}{
		{"enp0s31f6", osnet.LinkEthernet},
		{"enx001122334455", osnet.LinkEthernet},
		{"wlp0s20f3", osnet.LinkWiFi},
		{"wlan1", osnet.LinkWiFi},
		{"wwan0", osnet.LinkOther},
		{"wwan1", osnet.LinkOther},
		{"docker0", osnet.LinkOther},
		{"veth1a2b", osnet.LinkOther},
		{"wg0", osnet.LinkOther},
		{"tun0", osnet.LinkOther},
		{"lo", osnet.LinkOther},
		{"eth9", osnet.LinkOther},
		{"gone0", osnet.LinkOther},
		{"", osnet.LinkOther},
		{"../etc", osnet.LinkOther},
		{"enp0s31f6/device", osnet.LinkOther},
		{".", osnet.LinkOther},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := kinds.kind(tt.name); got != tt.want {
				t.Errorf("kind(%q) = %s, want %s", tt.name, kindName(got), kindName(tt.want))
			}
		})
	}
}

// Adapters come and go, so the question is answered from the directory as it is.
func TestLinkKindsFollowTheDirectory(t *testing.T) {
	sys := sysfs()
	kinds := newLinkKinds(sys)
	if got := kinds.kind("usb0"); got != osnet.LinkOther {
		t.Fatalf("kind of an adapter that is not there = %s", kindName(got))
	}
	sys["usb0/type"] = &fstest.MapFile{Data: []byte("1\n")}
	sys["usb0/device"] = &fstest.MapFile{Mode: 0o755 | 1<<31}
	if got := kinds.kind("usb0"); got != osnet.LinkEthernet {
		t.Errorf("kind of the adapter that was plugged in = %s", kindName(got))
	}
}
