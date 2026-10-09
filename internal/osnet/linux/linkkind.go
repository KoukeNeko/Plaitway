package linux

import (
	"io/fs"
	"strings"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// arphrdEther is the content of /sys/class/net/<if>/type of an Ethernet-like
// device.
const arphrdEther = "1"

// linkKinds tells what kind of network an interface connects to, from sysfs.
// The directory is read for the question each time: it is a few small files of
// memory, and adapters come and go.
type linkKinds struct {
	// sys is /sys/class/net.
	sys fs.FS
}

func newLinkKinds(sys fs.FS) *linkKinds { return &linkKinds{sys: sys} }

// kind is LinkWiFi for a wireless device and LinkEthernet for a physical
// Ethernet-like one. Everything else is LinkOther: tunnels, bridges, veth pairs
// and VLANs have no device behind them, a WWAN modem has one but is no
// Ethernet, and an interface that is gone, or whose name cannot be a sysfs
// path, is nothing.
func (k *linkKinds) kind(name string) osnet.LinkKind {
	if name == "" || strings.Contains(name, "/") {
		return osnet.LinkOther
	}
	switch {
	case k.exists(name + "/wireless"), k.exists(name + "/phy80211"):
		return osnet.LinkWiFi
	case k.hardwareType(name) == arphrdEther && k.exists(name+"/device") && k.deviceType(name) != "wwan":
		return osnet.LinkEthernet
	}
	return osnet.LinkOther
}

func (k *linkKinds) exists(path string) bool {
	_, err := fs.Stat(k.sys, path)
	return err == nil
}

func (k *linkKinds) hardwareType(name string) string {
	b, err := fs.ReadFile(k.sys, name+"/type")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// deviceType is the DEVTYPE line of the uevent file: "wlan", "wwan", "bridge",
// "vlan", and so on for the devices that have one.
func (k *linkKinds) deviceType(name string) string {
	b, err := fs.ReadFile(k.sys, name+"/uevent")
	if err != nil {
		return ""
	}
	for line := range strings.Lines(string(b)) {
		if devtype, ok := strings.CutPrefix(strings.TrimSpace(line), "DEVTYPE="); ok {
			return devtype
		}
	}
	return ""
}
