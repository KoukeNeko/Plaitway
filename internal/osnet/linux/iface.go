package linux

import (
	"cmp"
	"net/netip"
	"slices"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// tunnelKinds are the IFLA_INFO_KIND of the devices that encapsulate traffic.
// "tun" is the kind of tap devices too.
var tunnelKinds = []string{
	"tun", "wireguard", "ipip", "sit", "gre", "gretap", "ip6gre", "ip6gretap", "ip6tnl",
	"vti", "vti6", "geneve", "vxlan",
}

// link is an interface as read from the kernel, with what osnet.Interface does
// not carry.
type link struct {
	osnet.Interface
	// Uplink: the interface can be the way out to the internet. Bridges, veth
	// pairs, VLANs and the like can carry the default route; devices that
	// encapsulate traffic are the Reconciler's own business and loopback goes
	// nowhere. A PPP link or a link without a link layer that no tunnel driver
	// made (a WWAN modem) is Tunnel for the Reconciler, which cannot expect the
	// peer on a subnet of it, but is still where a default route may lead.
	Uplink bool
}

// linkFromMessage classifies the interface a link message describes. Its
// addresses are left empty.
func linkFromMessage(m linkMsg) link {
	loopback := m.Flags&iffLoopback != 0 || m.Type == arphrdLoopback
	encapsulates := slices.Contains(tunnelKinds, m.Kind)
	switch m.Type {
	case arphrdTunnel, arphrdTunnel6, arphrdSIT, arphrdIPGRE:
		encapsulates = true
	}
	return link{
		Interface: osnet.Interface{
			Name:  m.Name,
			Index: int(m.Index),
			// An interface that is administratively up but has no carrier carries nothing.
			Up:     m.Flags&iffUp != 0 && m.Flags&iffLowerUp != 0,
			Tunnel: !loopback && (encapsulates || m.Type == arphrdNone || m.Type == arphrdPPP),
		},
		Uplink: !loopback && !encapsulates,
	}
}

// buildLinks assembles the interfaces from the link and address messages of the
// dumps, in the order of their indexes. An address of an interface that is not
// among the links (it appeared in between) is dropped.
func buildLinks(msgs []rtMessage) []link {
	var out []link
	position := make(map[uint32]int)
	for _, m := range msgs {
		if m.Link != nil {
			position[m.Link.Index] = len(out)
			out = append(out, linkFromMessage(*m.Link))
		}
	}
	for _, m := range msgs {
		if m.Addr == nil {
			continue
		}
		if prefix, ok := m.Addr.prefix(); ok {
			if i, known := position[m.Addr.Index]; known {
				out[i].Addrs = append(out[i].Addrs, prefix)
			}
		}
	}
	for i := range out {
		slices.SortFunc(out[i].Addrs, comparePrefix)
		out[i].Addrs = slices.Compact(out[i].Addrs)
	}
	slices.SortFunc(out, func(a, b link) int { return cmp.Compare(a.Index, b.Index) })
	return out
}

// prefix is the address with its prefix length. The kernel names the address of
// the interface itself in IFA_LOCAL; IFA_ADDRESS is that too, except on a
// point-to-point link, where it is the peer.
func (a addrMsg) prefix() (netip.Prefix, bool) {
	addr := a.Local
	if !addr.IsValid() {
		addr = a.Address
	}
	if !addr.IsValid() || addr.Is4() != (a.Family == afInet) {
		return netip.Prefix{}, false
	}
	prefix := netip.PrefixFrom(addr.Unmap(), int(a.PrefixLen))
	return prefix, prefix.IsValid()
}

func comparePrefix(a, b netip.Prefix) int {
	return cmp.Or(a.Addr().Compare(b.Addr()), cmp.Compare(a.Bits(), b.Bits()))
}

// linkCache resolves interface indexes for the event reader. An interface that
// disappears keeps its entry, so the messages about its last moments can still
// be classified.
type linkCache struct {
	links map[uint32]link
	load  func() ([]link, error)
}

// lookup returns the interface with this index. ok is false when the kernel
// does not know the index (anymore).
func (c *linkCache) lookup(index uint32) (l link, ok bool, err error) {
	if l, ok = c.links[index]; ok {
		return l, true, nil
	}
	loaded, err := c.load()
	if err != nil {
		return link{}, false, err
	}
	if c.links == nil {
		c.links = make(map[uint32]link, len(loaded))
	}
	for _, l := range loaded {
		c.links[uint32(l.Index)] = l
	}
	l, ok = c.links[index]
	return l, ok, nil
}
