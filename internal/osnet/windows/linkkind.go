package windows

import (
	"net/netip"
	"slices"
	"strings"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// Interface types (IANAifType) that the classification looks at.
const (
	ifTypeEthernet    = 6
	ifTypePPP         = 23
	ifTypeLoopback    = 24
	ifTypePropVirtual = 53
	ifTypeIEEE80211   = 71
	ifTypeTunnel      = 131
)

// adapterNamePrefix starts the name of every adapter the engines of Plaitway
// create, so that they count as tunnels whatever driver sits below them.
const adapterNamePrefix = "plaitway"

// adapter is a network interface as the classification and the NetState see
// it. The Windows files fill it from MIB_IF_ROW2, the address table and the IP
// interface table; the logic on top of it does not touch the API.
type adapter struct {
	LUID  uint64
	Index uint32
	// Name is the interface alias, which is also what net.Interface calls it.
	Name        string
	Description string
	Type        uint32
	// Hardware is true for a physical adapter, false for every virtual one
	// (Hyper-V, TAP, Wintun, Bluetooth PAN).
	Hardware bool
	Up       bool
	// Metric4 and Metric6 are the interface metrics, which Windows adds to the
	// metric of every route through the adapter.
	Metric4, Metric6 uint32
	// Addrs are the unicast addresses, each with the length of its on-link prefix.
	Addrs []netip.Prefix
}

// tunnelDescriptions are the parts of a driver description that mark a VPN or
// tunnel adapter of an interface type that does not say so by itself. Wintun,
// the WireGuard NT driver, the OpenVPN TAP and DCO drivers (type 53 on the
// machine this was written on) and the common commercial VPN clients.
var tunnelDescriptions = []string{
	"wintun", "wireguard", "tap-windows", "tap-win32", "openvpn", "ovpn-dco",
	"tailscale", "zerotier", "nordlynx", "anyconnect", "globalprotect", "pangp",
	"fortinet", "vpn", "tunnel", adapterNamePrefix,
}

// notAWayOut are the parts of a description of an adapter that carries
// Ethernet or Wi-Fi frames without being a network of its own: virtual
// switches, virtual machine adapters, the Bluetooth personal area network,
// network bridges, and phones that share their connection over USB. Apple
// names the phones, Windows the RNDIS protocol they use.
var notAWayOut = []string{
	"virtual", "hyper-v", "vmware", "virtualbox", "bluetooth", "bridge",
	"wi-fi direct", "rndis", "remote ndis", "usbncm", "apple mobile device", "iphone", "ipad",
	"miniport", "loopback",
}

func isLoopback(a adapter) bool { return a.Type == ifTypeLoopback }

// isTunnel reports whether the adapter is a VPN or other tunnel. The default
// route of a tunnel is never the physical way out. PPP is a tunnel here as it
// is for the macOS adapter, which also means that a PPPoE broadband connection
// is not found as the way out.
func isTunnel(a adapter) bool {
	switch a.Type {
	case ifTypeTunnel, ifTypePPP, ifTypePropVirtual:
		return true
	}
	return containsAny(strings.ToLower(a.Description), tunnelDescriptions) ||
		strings.HasPrefix(strings.ToLower(a.Name), adapterNamePrefix)
}

// hyperVSwitchDescription is the driver of the adapter that Hyper-V makes in
// the host for a virtual switch ("vEthernet (<switch>)"). It is a software
// adapter, so the hardware flag is false, and it is the way out when the
// switch is an external one: the physical adapter behind it gives up its
// addresses and the switch's adapter holds them and the default route. An
// internal or NAT switch (the Default Switch, WSL) has no default route.
const hyperVSwitchDescription = "hyper-v virtual ethernet adapter"

// isLocalNetwork reports whether the adapter connects the PC to a network of
// its own, a virtual switch's included: not the loopback and not a tunnel. The
// subnets of such adapters are on-link, whatever they lead to, and a VPN route
// into one of them would cut the PC off from it.
func isLocalNetwork(a adapter) bool { return !isLoopback(a) && !isTunnel(a) }

// isPhysical reports whether the adapter can be the way out to the internet:
// the default route of the PC, and the gateway that routes to a VPN's server
// keep going through. It must be a network adapter of the PC (a hardware
// interface), or the adapter of a Hyper-V external switch.
//
// The hardware flag is required, and not just the absence of a tunnel's marks,
// because the marks are a list of what is known: a VPN driver that reports
// Ethernet, has no telling name and sits on a virtual device would otherwise
// count as the way out, and the routes to a second VPN's server would go
// through the first one's gateway. The error runs the safe way: a virtual
// adapter that really is the only link (a PC whose connection comes from the
// Bluetooth personal area network, say) has no default, which shows as "no
// network" in the routes that need one, in the on-demand rules and in the
// diagnostics, until a physical link comes up.
//
// The names of known tunnels stay a second guard: some VPN drivers set the
// hardware flag, and the Cisco AnyConnect adapter is one.
func isPhysical(a adapter) bool {
	return isLocalNetwork(a) && (a.Hardware || isHyperVSwitch(a))
}

func isHyperVSwitch(a adapter) bool {
	return a.Type == ifTypeEthernet && strings.Contains(strings.ToLower(a.Description), hyperVSwitchDescription)
}

// linkKind tells what kind of network the adapter connects to. Only a hardware
// adapter that is a Wi-Fi or Ethernet one by type is, and not when its driver
// says that it is virtual, a Bluetooth link or a tethered phone: the rules that
// follow the kind of network are about the networks a person plugs in or joins.
func linkKind(a adapter) osnet.LinkKind {
	if !a.Hardware || !isPhysical(a) || containsAny(strings.ToLower(a.Description), notAWayOut) {
		return osnet.LinkOther
	}
	switch a.Type {
	case ifTypeIEEE80211:
		return osnet.LinkWiFi
	case ifTypeEthernet:
		return osnet.LinkEthernet
	}
	return osnet.LinkOther
}

func containsAny(s string, substrings []string) bool {
	return slices.ContainsFunc(substrings, func(sub string) bool { return strings.Contains(s, sub) })
}
