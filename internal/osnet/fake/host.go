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
	// linux is set when the route table has Linux semantics (see NewLinuxHost).
	linux bool
	// defaultMetrics are the metrics of the default routes of an interface in
	// Linux mode (see SetDefaultMetric).
	defaultMetrics map[string]uint32
}

func NewHost() *Host {
	return &Host{Routes: NewRouteTable(), DNS: NewDNS(), Net: NewNetMonitor(), kinds: make(map[string]osnet.LinkKind)}
}

// NewWindowsHost is a Host whose route table has Windows semantics (see
// RouteTable.WindowsKeying).
func NewWindowsHost() *Host {
	h := NewHost()
	h.Routes.WindowsKeying()
	return h
}

// NewLinuxHost is a Host whose route table has Linux semantics (see
// RouteTable.LinuxKeying). Its interfaces get indexes, and the default route of
// a physical interface has the metric 100 unless SetDefaultMetric says
// otherwise: the kernel does not take two default routes with one metric.
func NewLinuxHost() *Host {
	h := NewHost()
	h.Routes.LinuxKeying()
	h.linux = true
	h.defaultMetrics = make(map[string]uint32)
	return h
}

// defaultMetricDHCP is the metric DHCP clients and NetworkManager give the
// default route of a wired interface.
const defaultMetricDHCP = 100

// SetDefaultMetric sets the metric of the default routes that AddPhysical and
// MoveNetwork give the interface name, in Linux mode. Call it before the
// interface gets its default route; two physical interfaces need different
// metrics.
func (h *Host) SetDefaultMetric(name string, metric uint32) {
	h.defaultMetrics[name] = metric
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
	if h.linux {
		h.Routes.removeDefaultsVia(name)
	} else {
		for _, dst := range []netip.Prefix{defaultV4, defaultV6} {
			if r, ok := h.Routes.Get(dst); ok && r.Iface == name {
				h.Routes.Remove(dst)
			}
		}
	}
	if !gateway.IsValid() {
		return
	}
	dst := defaultV4
	if gateway.Is6() {
		dst = defaultV6
	}
	rt := osnet.Route{Dst: dst, Gateway: gateway, Iface: name, Static: true}
	if h.linux {
		rt.Metric = defaultMetricDHCP
		if metric, ok := h.defaultMetrics[name]; ok {
			rt.Metric = metric
		}
	}
	if err := h.Routes.Add(rt); err != nil {
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
	// Dump is sorted, so the pick among equals is stable. A route replaces the
	// pick of its family only when its effective metric is lower, which matters
	// when the table has Windows semantics and keeps several defaults.
	if routes, err := h.Routes.Dump(); err == nil {
		var bestV4, bestV6 uint32
		for _, r := range routes {
			if r.Scoped || r.Dst.Bits() != 0 || r.Blackhole {
				continue
			}
			ifc, ok := findIface(ifaces, r.Iface)
			if !ok || !ifc.Up || !isPhysical(ifc) {
				continue
			}
			nh := &osnet.Nexthop{Gateway: r.Gateway, Iface: r.Iface, Kind: h.kinds[r.Iface]}
			metric := h.Routes.EffectiveMetric(r)
			switch {
			case r.Dst.Addr().Is4() && (ns.DefaultV4 == nil || metric < bestV4):
				ns.DefaultV4, bestV4 = nh, metric
			case r.Dst.Addr().Is6() && (ns.DefaultV6 == nil || metric < bestV6):
				ns.DefaultV6, bestV6 = nh, metric
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
