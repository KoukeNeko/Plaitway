package windows

import (
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

func kindName(k osnet.LinkKind) string {
	switch k {
	case osnet.LinkWiFi:
		return "LinkWiFi"
	case osnet.LinkEthernet:
		return "LinkEthernet"
	}
	return "LinkOther"
}

// Adapters the classification has to get right beyond the fixtures of the PC
// this was written on.
var (
	// corporateVPN is the case the hardware flag exists for: a VPN client with
	// a driver that reports Ethernet, is not hardware, and has nothing in its
	// name or description that the list of known tunnels knows.
	corporateVPN = adapter{LUID: 0x3001, Index: 70, Name: "Ethernet 8", Description: "Contoso Secure Link Adapter", Type: ifTypeEthernet, Up: true, Metric4: 5, Addrs: pfx("10.99.0.2/24")}
	// hyperVExternal is the switch adapter of an external Hyper-V switch: the
	// physical NIC behind it holds no address, the switch's adapter holds them
	// and the default route.
	hyperVExternal = adapter{LUID: 0x3002, Index: 71, Name: "vEthernet (External)", Description: "Hyper-V Virtual Ethernet Adapter #2", Type: ifTypeEthernet, Up: true, Metric4: 15, Addrs: pfx("192.168.1.101/24")}
	// dockEthernet is the wired port of a docking station: a USB device, and
	// hardware like any other.
	dockEthernet = adapter{LUID: 0x3003, Index: 72, Name: "Ethernet 12", Description: "Realtek USB FE Family Controller", Type: ifTypeEthernet, Hardware: true, Up: true, Metric4: 25, Addrs: pfx("192.168.1.150/24")}
)

func TestClassifyAdapters(t *testing.T) {
	tests := []struct {
		name         string
		a            adapter
		wantTunnel   bool
		wantLoopback bool
		wantWayOut   bool
		wantKind     osnet.LinkKind
	}{
		{"wired Ethernet", intelEthernet, false, false, true, osnet.LinkEthernet},
		{"USB gigabit adapter", usbGigabit, false, false, true, osnet.LinkEthernet},
		{"docking station Ethernet", dockEthernet, false, false, true, osnet.LinkEthernet},
		{"Wi-Fi", realtekWiFi, false, false, true, osnet.LinkWiFi},
		{"cellular modem", wwan, false, false, true, osnet.LinkOther},
		{"phone over USB, NCM (USB tethering)", usbNCM, false, false, true, osnet.LinkOther},
		{"iPhone over USB", appleUSB, false, false, true, osnet.LinkOther},
		{"Android over USB, RNDIS", rndisPhone, false, false, true, osnet.LinkOther},
		{"Wi-Fi Direct virtual adapter", wifiDirect, false, false, true, osnet.LinkOther},
		{"OpenVPN TAP, interface type 53", openVPNTap, true, false, false, osnet.LinkOther},
		{"OpenVPN data channel offload, type 53", openVPNDCO, true, false, false, osnet.LinkOther},
		{"Wintun by description", wintun, true, false, false, osnet.LinkOther},
		{"WireGuard by type and name", wireGuard, true, false, false, osnet.LinkOther},
		{"Tailscale by description", tailscale, true, false, false, osnet.LinkOther},
		{"Cisco AnyConnect claims to be hardware", anyConnect, true, false, false, osnet.LinkOther},
		{"Teredo, IF_TYPE_TUNNEL", teredo, true, false, false, osnet.LinkOther},
		{"WAN Miniport IKEv2, IF_TYPE_TUNNEL", wanMiniportIKE, true, false, false, osnet.LinkOther},
		{"PPPoE, IF_TYPE_PPP", wanPPPoE, true, false, false, osnet.LinkOther},
		{"loopback", loopback, false, true, false, osnet.LinkOther},

		// A virtual adapter that is not a known tunnel is no way out: only a
		// hardware adapter is, or the adapter of a Hyper-V external switch.
		{"corporate VPN: virtual, Ethernet type, no telling name", corporateVPN, false, false, false, osnet.LinkOther},
		{"Hyper-V external switch in front of a physical NIC", hyperVExternal, false, false, true, osnet.LinkOther},
		{"Hyper-V Default Switch (NAT, never has a default route)", hyperVSwitch, false, false, true, osnet.LinkOther},
		{"Bluetooth personal area network", bluetoothPAN, false, false, false, osnet.LinkOther},
		{"unknown VPN driver of type 53", adapter{Name: "Ethernet 8", Description: "Contoso Secure Link Adapter", Type: ifTypePropVirtual, Up: true}, true, false, false, osnet.LinkOther},
		{"an adapter of ours, named so", adapter{Name: "Plaitway-wg0", Description: "Acme Network Adapter", Type: ifTypeEthernet, Hardware: true, Up: true}, true, false, false, osnet.LinkOther},
		{"unknown software adapter without hardware", adapter{Name: "Ethernet 9", Description: "Acme Network Adapter", Type: ifTypeEthernet, Up: true}, false, false, false, osnet.LinkOther},
		{"unknown Wi-Fi without hardware", adapter{Name: "Wi-Fi 2", Description: "Acme Wireless Adapter", Type: ifTypeIEEE80211, Up: true}, false, false, false, osnet.LinkOther},
		{"hardware flag with a VPN in the description: the keywords still guard", adapter{Name: "Ethernet 13", Description: "Acme VPN Virtual Miniport", Type: ifTypeEthernet, Hardware: true, Up: true}, true, false, false, osnet.LinkOther},
		// The exception is the Hyper-V driver and nothing like it.
		{"a switch adapter of another hypervisor is not the exception", adapter{Name: "VMware Network Adapter VMnet8", Description: "VMware Virtual Ethernet Adapter for VMnet8", Type: ifTypeEthernet, Up: true}, false, false, false, osnet.LinkOther},
		{"the Hyper-V driver on a tunnel type is a tunnel", adapter{Name: "vEthernet (x)", Description: "Hyper-V Virtual Ethernet Adapter", Type: ifTypeTunnel, Up: true}, true, false, false, osnet.LinkOther},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTunnel(tt.a); got != tt.wantTunnel {
				t.Errorf("isTunnel = %v, want %v", got, tt.wantTunnel)
			}
			if got := isLoopback(tt.a); got != tt.wantLoopback {
				t.Errorf("isLoopback = %v, want %v", got, tt.wantLoopback)
			}
			if got := isPhysical(tt.a); got != tt.wantWayOut {
				t.Errorf("isPhysical = %v, want %v", got, tt.wantWayOut)
			}
			if got := linkKind(tt.a); got != tt.wantKind {
				t.Errorf("linkKind = %s, want %s", kindName(got), kindName(tt.wantKind))
			}
			// Whatever is not a tunnel or the loopback is a network of the PC's
			// own, its subnet on-link, even when it is no way out.
			if got, want := isLocalNetwork(tt.a), !tt.wantTunnel && !tt.wantLoopback; got != want {
				t.Errorf("isLocalNetwork = %v, want %v", got, want)
			}
		})
	}
}

// A hardware adapter of a type that is neither Wi-Fi nor Ethernet (token ring,
// FireWire) is not a network the on-demand rules know.
func TestLinkKindOfUnknownHardwareType(t *testing.T) {
	a := adapter{Name: "Ethernet 9", Description: "Some NIC", Type: 144, Hardware: true, Up: true}
	if got := linkKind(a); got != osnet.LinkOther {
		t.Errorf("linkKind = %s, want LinkOther", kindName(got))
	}
}
