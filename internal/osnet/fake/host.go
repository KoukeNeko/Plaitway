package fake

import (
	"net/netip"
	"strings"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// Host is a machine made of the three fakes, wired the way a real one is: the
// NetState the monitor reports is derived from the route table's interfaces
// and default routes. Mutators update the table and the NetState but never
// Emit; the test decides when the Reconciler gets to hear about a change.
type Host struct {
	Routes *RouteTable
	DNS    *DNS
	Net    *NetMonitor
	// kinds is what each interface connects to; an interface that is not in it
	// is osnet.LinkOther.
	kinds map[string]osnet.LinkKind
}

func NewHost() *Host {
	return &Host{Routes: NewRouteTable(), DNS: NewDNS(), Net: NewNetMonitor(), kinds: make(map[string]osnet.LinkKind)}
}

var (
	defaultV4 = netip.MustParsePrefix("0.0.0.0/0")
	defaultV6 = netip.MustParsePrefix("::/0")
)

// AddPhysical brings up a physical interface with an address, and a system
// default route through gateway when it is valid.
func (h *Host) AddPhysical(name string, addr netip.Prefix, gateway netip.Addr) {
	h.Routes.AddInterface(osnet.Interface{Name: name, Up: true, Addrs: []netip.Prefix{addr}})
	h.setDefault(name, gateway)
	h.Sync()
}

// MoveNetwork gives a physical interface a new address and default gateway, as
// DHCP does when the machine joins another network. The system's own default
// route follows; routes added by other programs through the old gateway stay,
// which is how stale routes are born.
func (h *Host) MoveNetwork(name string, addr netip.Prefix, gateway netip.Addr) {
	h.Routes.SetAddrs(name, []netip.Prefix{addr})
	h.setDefault(name, gateway)
	h.Sync()
}

// AddTunnel creates a tunnel interface such as utun10.
func (h *Host) AddTunnel(name string, addrs ...netip.Prefix) {
	h.Routes.AddInterface(osnet.Interface{Name: name, Up: true, Tunnel: true, Addrs: addrs})
	h.Sync()
}

// SetLinkKind says what kind of network the interface connects to, which the
// NetState reports for the default route through it. The kind belongs to the
// name, so it can be set before the interface exists and it outlives
// DestroyInterface.
func (h *Host) SetLinkKind(name string, kind osnet.LinkKind) {
	h.kinds[name] = kind
	h.Sync()
}

// DestroyInterface removes an interface and the routes through it.
func (h *Host) DestroyInterface(name string) {
	h.Routes.DestroyInterface(name)
	h.Sync()
}

// setDefault replaces the system default route of an interface. The route table
// refuses a gateway that is not on the interface's subnet, like the kernel.
func (h *Host) setDefault(name string, gateway netip.Addr) {
	for _, dst := range []netip.Prefix{defaultV4, defaultV6} {
		if r, ok := h.Routes.Get(dst); ok && r.Iface == name {
			h.Routes.Remove(dst)
		}
	}
	if !gateway.IsValid() {
		return
	}
	dst := defaultV4
	if gateway.Is6() {
		dst = defaultV6
	}
	if err := h.Routes.Add(osnet.Route{Dst: dst, Gateway: gateway, Iface: name, Static: true}); err != nil {
		panic("fake host: system default route: " + err.Error())
	}
}

// Sync derives the NetState from the route table and hands it to the monitor.
func (h *Host) Sync() {
	ifaces := h.Routes.Interfaces()
	ns := osnet.NetState{Interfaces: ifaces}
	for _, ifc := range ifaces {
		if ifc.Up && isPhysical(ifc) {
			for _, a := range ifc.Addrs {
				ns.Connected = append(ns.Connected, a.Masked())
			}
		}
	}
	// Dump is sorted, so the first physical default per family is stable.
	if routes, err := h.Routes.Dump(); err == nil {
		for _, r := range routes {
			if r.Scoped || r.Dst.Bits() != 0 || r.Blackhole {
				continue
			}
			ifc, ok := findIface(ifaces, r.Iface)
			if !ok || !ifc.Up || !isPhysical(ifc) {
				continue
			}
			nh := &osnet.Nexthop{Gateway: r.Gateway, Iface: r.Iface, Kind: h.kinds[r.Iface]}
			switch {
			case r.Dst.Addr().Is4() && ns.DefaultV4 == nil:
				ns.DefaultV4 = nh
			case r.Dst.Addr().Is6() && ns.DefaultV6 == nil:
				ns.DefaultV6 = nh
			}
		}
	}
	h.Net.Set(ns)
}

func findIface(ifaces []osnet.Interface, name string) (osnet.Interface, bool) {
	for _, ifc := range ifaces {
		if ifc.Name == name {
			return ifc, true
		}
	}
	return osnet.Interface{}, false
}

// virtualPrefixes are the interfaces that are no way out to the internet, as
// the macOS adapter sees them: AirDrop, low-latency WLAN, loopback, gif, the
// bridges of Internet Sharing and virtual machines, Apple's own links.
var virtualPrefixes = []string{"awdl", "llw", "lo", "gif", "bridge", "anpi", "ap"}

// isPhysical excludes tunnels and the virtual interfaces: neither a default
// route nor a connected subnet of NetState comes from them.
func isPhysical(ifc osnet.Interface) bool {
	if ifc.Tunnel {
		return false
	}
	for _, p := range virtualPrefixes {
		if unit, ok := strings.CutPrefix(ifc.Name, p); ok && strings.Trim(unit, "0123456789") == "" {
			return false
		}
	}
	return true
}
