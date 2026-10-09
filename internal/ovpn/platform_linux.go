package ovpn

// maxSocketPath is the longest Unix socket path Linux accepts (sun_path is 108
// bytes including the terminating NUL).
const maxSocketPath = 107

// tapRefusal is why a profile that asks for a tap device cannot connect, and
// the engine does not start it (refusesTap).
//
// The kernel has tap devices, but they are Ethernet links: a route over one
// needs a next hop, whose address the kernel resolves with ARP on the link, and
// only that next hop answers. The Reconciler binds every tunnel route to its
// device without a next hop on Linux (reconciler.KeyLinux; tun and WireGuard
// devices are point-to-point and need none), so a destination behind the
// server is looked for with ARP on the link, where the server does not answer
// for it, and the traffic goes nowhere: the tunnel would look up and carry
// nothing but the server's own tunnel address.
// TestRootLinuxTapRoutesNeedANextHop shows it against a real tap server.
const tapRefusal = "tap devices are not supported on Linux"

const refusesTap = true
