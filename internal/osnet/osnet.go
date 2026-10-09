// Package osnet is the boundary between Plaitway and the host's networking: the
// routing table, the network's current shape, and the system resolver. It holds
// types and interfaces only. The Reconciler is the sole caller of RouteTable
// and DNSConfigurator; the OS implementations live in sub-packages and in-memory
// fakes live next to the code that tests against them.
package osnet

import (
	"context"
	"errors"
	"net/netip"
	"time"
)

var (
	// ErrExists: adding a route whose key is already in the table.
	ErrExists = errors.New("route already exists")
	// ErrNotFound: deleting a route that is not in the table.
	ErrNotFound = errors.New("route not found")
	// ErrUnreachable: the gateway is not reachable from any interface right now.
	ErrUnreachable = errors.New("network unreachable")
)

// Route is one entry of the kernel routing table.
type Route struct {
	Dst netip.Prefix
	// Gateway is the next hop; the zero Addr for a route bound to an interface.
	Gateway netip.Addr
	// Iface is the interface name the route leaves through ("" when unknown).
	Iface     string
	Blackhole bool
	// Static is set for routes an administrator or a program added, as opposed
	// to routes the kernel derived from interface addresses (macOS RTF_STATIC;
	// Linux: any protocol but kernel, redirect and router advertisement).
	Static bool
	// Scoped routes belong to one interface (macOS RTF_IFSCOPE) and are only
	// used by sockets bound to it. They never conflict with unscoped routes.
	Scoped bool
	// Flags is the raw kernel flag word, for display and fingerprinting only.
	Flags uint32
	// IfIndex is the index of the interface the route leaves through, zero when
	// unknown. Windows keys a route by destination, interface index and next hop,
	// so there it takes precedence over Iface. Linux names the interface of a
	// route by its index (RTA_OIF) as well, and the Reconciler compares it there,
	// so the adapter sets it on every route it dumps; the kernel never picks
	// another interface than the one named. macOS ignores it and leaves it zero.
	IfIndex uint32
	// Metric is the route metric. Windows adds the metric of the interface to it.
	// On Linux it is the priority of the route and the whole metric: with the
	// destination it is what tells the routes of one prefix apart, adding a
	// second route with both is ErrExists whatever interface and next hop it has,
	// and Delete names it. Zero is a valid metric. macOS has none and ignores it.
	Metric uint32
}

// RouteTable reads and writes the routing table. Implementations write routes
// directly instead of calling /sbin/route, whose exit status is 0 on EEXIST and
// ESRCH. Add returns ErrExists and Delete returns ErrNotFound for those cases.
type RouteTable interface {
	// Dump returns every IPv4 and IPv6 route.
	Dump() ([]Route, error)
	Add(Route) error
	Delete(Route) error
}

// Interface is a network interface as the Reconciler needs to see it.
type Interface struct {
	Name string
	// Index is the system's number of the interface. The Reconciler names the
	// interface of a Windows or Linux route by it, taken from the network state.
	Index int
	Up    bool
	// Tunnel is true for utun, tun, wg, ppp and ipsec interfaces.
	Tunnel bool
	// Addrs are the configured addresses with their prefix lengths.
	Addrs []netip.Prefix
	// Metric is the interface metric that Windows adds to the metric of every
	// route through the interface (0 where unknown or not used: macOS, Linux).
	Metric uint32
}

// LinkKind is what kind of network an interface connects to.
type LinkKind uint8

const (
	LinkOther LinkKind = iota // unknown, a tunnel, a phone tethered over USB or Bluetooth
	LinkWiFi
	LinkEthernet // wired, including USB and Thunderbolt Ethernet adapters
)

// Nexthop is where a default route leads.
type Nexthop struct {
	Gateway netip.Addr
	Iface   string
	// Kind is the kind of network Iface connects to.
	Kind LinkKind
}

// NetState is the physical network at one instant. It is re-read after every
// event; nothing in an event is trusted.
type NetState struct {
	// Epoch increases whenever the relevant part of the state changed.
	Epoch      uint64
	Interfaces []Interface
	// DefaultV4 and DefaultV6 are the best default route that does not leave
	// through a tunnel, awdl, llw, lo or gif interface; nil when there is none.
	DefaultV4, DefaultV6 *Nexthop
	// Connected are the on-link subnets of physical interfaces.
	Connected []netip.Prefix
}

type ChangeReason string

const (
	ChangeRoute     ChangeReason = "route"
	ChangeWake      ChangeReason = "wake"
	ChangeHeartbeat ChangeReason = "heartbeat"
	ChangeManual    ChangeReason = "manual"
)

// Change is a debounced hint that the network may have changed. It carries no
// state: the receiver calls Snapshot.
type Change struct {
	Reason ChangeReason
	At     time.Time
}

// NetMonitor reads the network and reports when it may have changed.
type NetMonitor interface {
	Snapshot() (NetState, error)
	// Events merges route-socket messages, wake-from-sleep detection and a
	// periodic heartbeat into debounced Changes. The channel closes when ctx ends.
	Events(ctx context.Context) <-chan Change
}

// DNSEntry is one resolver the system should consult for some domains.
type DNSEntry struct {
	Servers []netip.Addr
	// MatchDomains are the domains this resolver answers; "." means everything.
	MatchDomains []string
	// Order breaks ties between resolvers for the same domain; lower wins.
	Order int
	// Iface is the tunnel interface the servers belong to. Where the system
	// keeps resolver settings per interface (Linux, systemd-resolved) the entry
	// is written there; macOS names no interface and ignores it.
	Iface string
}

// DNSConfigurator applies resolver configuration. Everything it writes carries
// a marker so Owned can find it again after a crash.
type DNSConfigurator interface {
	// Apply replaces everything previously applied for owner. An empty
	// entries slice is the same as Remove.
	Apply(owner string, entries []DNSEntry) error
	// Remove deletes everything applied for owner; removing nothing is not an
	// error. A key returned by Owned stands for the owner it belongs to.
	Remove(ownerOrKey string) error
	// Owned lists the keys that carry this program's marker, whoever owns them.
	Owned() ([]string, error)
	// Flush drops the resolver caches.
	Flush() error
}
