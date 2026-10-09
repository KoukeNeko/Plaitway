package wg

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// uapiConfig renders the whole device configuration in wireguard-go's UAPI
// text form. endpoints holds the resolved endpoint of each peer, the zero
// AddrPort for a peer without one: wireguard-go takes addresses only.
func (p *profile) uapiConfig(endpoints []netip.AddrPort) string {
	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%x\n", p.privateKey)
	if p.listenPort != 0 {
		fmt.Fprintf(&b, "listen_port=%d\n", p.listenPort)
	}
	if p.fwmark != 0 {
		// On Linux the mark goes on the UDP sockets, for the policy routing (ip rule
		// fwmark) that the user set up around the tunnel. The Reconciler reads only the
		// main routing table and does not see such rules.
		fmt.Fprintf(&b, "fwmark=%d\n", p.fwmark)
	}
	b.WriteString("replace_peers=true\n")
	for i, peer := range p.peers {
		fmt.Fprintf(&b, "public_key=%x\n", peer.publicKey)
		if peer.presharedKey != ([32]byte{}) {
			fmt.Fprintf(&b, "preshared_key=%x\n", peer.presharedKey)
		}
		if endpoints[i].IsValid() {
			fmt.Fprintf(&b, "endpoint=%s\n", endpoints[i])
		}
		fmt.Fprintf(&b, "persistent_keepalive_interval=%d\n", peer.keepalive)
		b.WriteString("replace_allowed_ips=true\n")
		allowed := peer.allowedIPs
		if p.excludePrivate {
			allowed = p.withoutPrivate(allowed, i)
		}
		for _, prefix := range allowed {
			fmt.Fprintf(&b, "allowed_ip=%s\n", prefix)
		}
	}
	return b.String()
}

// endpointUpdate renders the change of endpoints on a running device. Peers
// whose endpoint did not change are left alone so that an endpoint wireguard-go
// learned from a packet is not overwritten.
func (p *profile) endpointUpdate(old, updated []netip.AddrPort) string {
	var b strings.Builder
	for i, peer := range p.peers {
		if updated[i] == old[i] || !updated[i].IsValid() {
			continue
		}
		fmt.Fprintf(&b, "public_key=%x\nupdate_only=true\nendpoint=%s\n", peer.publicKey, updated[i])
	}
	return b.String()
}

// deviceStats is what the engine reads from a device's UAPI "get" output.
type deviceStats struct {
	// listenPort is the port the device is bound to; 0 when it is not bound.
	listenPort       uint16
	rxBytes, txBytes uint64
	// lastHandshake is the newest handshake of any peer; zero when none yet.
	lastHandshake time.Time
}

// parseDeviceStats reads the listen port and, of every peer, rx, tx and
// handshake time. The output also holds the private key, which is skipped here
// and must never be logged.
func parseDeviceStats(uapi string) (deviceStats, error) {
	var stats deviceStats
	var handshakeSec int64
	for _, line := range strings.Split(uapi, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		var err error
		switch key {
		case "listen_port":
			var port uint64
			if port, err = strconv.ParseUint(value, 10, 16); err == nil {
				stats.listenPort = uint16(port)
			}
		case "rx_bytes", "tx_bytes":
			var n uint64
			if n, err = strconv.ParseUint(value, 10, 64); err == nil {
				if key == "rx_bytes" {
					stats.rxBytes += n
				} else {
					stats.txBytes += n
				}
			}
		case "last_handshake_time_sec":
			handshakeSec, err = strconv.ParseInt(value, 10, 64)
		case "last_handshake_time_nsec":
			var nsec int64
			if nsec, err = strconv.ParseInt(value, 10, 64); err == nil && (handshakeSec != 0 || nsec != 0) {
				if at := time.Unix(handshakeSec, nsec); at.After(stats.lastHandshake) {
					stats.lastHandshake = at
				}
			}
		}
		if err != nil {
			return deviceStats{}, fmt.Errorf("parse %s: %w", key, err)
		}
	}
	return stats, nil
}
