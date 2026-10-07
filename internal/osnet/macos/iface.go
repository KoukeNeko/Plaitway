package macos

import (
	"cmp"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

var (
	tunnelPrefixes = []string{"utun", "tun", "wg", "ppp", "ipsec"}
	// virtualPrefixes are interfaces that are neither tunnels nor a way out to
	// the internet: AirDrop, low-latency WLAN, loopback, tunnels of the system
	// itself, bridges of Internet Sharing and VMs, and Apple's own links.
	virtualPrefixes = []string{"awdl", "llw", "lo", "gif", "bridge", "anpi", "ap"}
)

// hasUnitName reports whether name is one of prefixes followed by a unit number.
func hasUnitName(name string, prefixes []string) bool {
	for _, prefix := range prefixes {
		unit, ok := strings.CutPrefix(name, prefix)
		if ok && strings.Trim(unit, "0123456789") == "" {
			return true
		}
	}
	return false
}

func isTunnel(name string) bool { return hasUnitName(name, tunnelPrefixes) }

// isPhysical reports whether name can be the way out to the internet. An empty
// name, an interface the kernel did not name, is not.
func isPhysical(name string) bool {
	return name != "" && !isTunnel(name) && !hasUnitName(name, virtualPrefixes)
}

// listInterfaces reads every interface with its addresses.
func listInterfaces() ([]osnet.Interface, error) {
	nets, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list interfaces: %w", err)
	}
	ifaces := make([]osnet.Interface, 0, len(nets))
	for _, n := range nets {
		addrs, err := n.Addrs()
		if err != nil {
			return nil, fmt.Errorf("addresses of %s: %w", n.Name, err)
		}
		iface := osnet.Interface{
			Name:   n.Name,
			Index:  n.Index,
			Up:     n.Flags&net.FlagUp != 0 && n.Flags&net.FlagRunning != 0,
			Tunnel: isTunnel(n.Name),
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipNet.IP)
			if !ok {
				continue
			}
			ones, _ := ipNet.Mask.Size()
			iface.Addrs = append(iface.Addrs, netip.PrefixFrom(ip.Unmap(), ones))
		}
		slices.SortFunc(iface.Addrs, comparePrefix)
		ifaces = append(ifaces, iface)
	}
	return ifaces, nil
}

func comparePrefix(a, b netip.Prefix) int {
	return cmp.Or(a.Addr().Compare(b.Addr()), cmp.Compare(a.Bits(), b.Bits()))
}

// interfaceNames maps interface index to name.
func interfaceNames() (map[int]string, error) {
	nets, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list interfaces: %w", err)
	}
	names := make(map[int]string, len(nets))
	for _, n := range nets {
		names[n.Index] = n.Name
	}
	return names, nil
}

// nameCache resolves interface indexes for the route socket reader. An
// interface that disappears keeps its name, so the messages about its
// last moments can still be classified.
type nameCache struct {
	names map[int]string
	load  func() (map[int]string, error)
}

// lookup returns the name of the interface with this index. ok is false when
// the kernel does not know the index (anymore).
func (c *nameCache) lookup(index int) (name string, ok bool, err error) {
	if name, ok = c.names[index]; ok {
		return name, true, nil
	}
	loaded, err := c.load()
	if err != nil {
		return "", false, err
	}
	if c.names == nil {
		c.names = make(map[int]string, len(loaded))
	}
	for i, n := range loaded {
		c.names[i] = n
	}
	name, ok = c.names[index]
	return name, ok, nil
}
