package ovpn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"regexp"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// nullDevice is the profile device that opens nothing; tests use it to run a
// real openvpn without a tunnel interface.
const nullDevice = "null"

// deviceProvider is what an engine does about the tunnel interface that the OS
// makes it do itself. openvpn on macOS opens a utun device of its own; on
// Windows it can only use an adapter that exists, so the engine makes one for
// it and sets its addresses, because openvpn must not (the Reconciler owns
// routes and DNS, and the engine owns nothing else on the adapter).
type deviceProvider interface {
	// options are added to the openvpn command line.
	options() []string
	// acquire makes what openvpn is to use. It may take seconds, and ends with
	// ctx.
	acquire(ctx context.Context) error
	// name is the interface openvpn will use, or "" when openvpn says so itself.
	name() string
	// configure gives the interface its addresses once openvpn reports the tunnel
	// up, and returns when they can be used.
	configure(ctx context.Context, up upInfo) error
	// release removes what acquire made. It is called after openvpn has exited,
	// also when acquire failed halfway.
	release()
}

// openvpnOwnedDevice leaves the interface to openvpn.
type openvpnOwnedDevice struct{}

func (openvpnOwnedDevice) options() []string                       { return nil }
func (openvpnOwnedDevice) acquire(context.Context) error           { return nil }
func (openvpnOwnedDevice) name() string                            { return "" }
func (openvpnOwnedDevice) configure(context.Context, upInfo) error { return nil }
func (openvpnOwnedDevice) release()                                {}

// adapterNameBytes is how many bytes of the owner's hash name an adapter:
// short enough to read in the network settings, long enough that two profiles
// never share a name.
const adapterNameBytes = 4

// ownAdapterPrefix starts the name of every adapter the Windows engine makes.
// The name of a leftover adapter is told by it.
const ownAdapterPrefix = "Plaitway-ovpn-"

// adapterName is the name of the adapter of one profile: the same profile gets
// the same name on every run.
func adapterName(prefix string, owner tunnel.OwnerID) string {
	sum := sha256.Sum256([]byte(owner))
	return prefix + hex.EncodeToString(sum[:adapterNameBytes])
}

// adapterNamePattern matches a name adapterName makes, whatever its prefix.
var adapterNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,31}-[0-9a-f]{8}$`)

// onLinkPrefix is the address of the tunnel with the prefix length the
// interface needs for the peer to be on-link. With topology subnet openvpn
// reports the netmask; with net30 and p2p it reports the peer's address
// instead, and the address then needs the smallest subnet that holds both.
func onLinkPrefix(local netip.Prefix, peer netip.Addr) netip.Prefix {
	if !peer.IsValid() || local.Bits() != local.Addr().BitLen() || peer.BitLen() != local.Addr().BitLen() {
		return local
	}
	for bits := local.Bits(); bits >= 0; bits-- {
		wide := netip.PrefixFrom(local.Addr(), bits)
		if wide.Contains(peer) {
			return wide
		}
	}
	return local
}
