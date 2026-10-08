package wg

import (
	"fmt"
	"testing"
)

// The engine uses wireguard-go's default socket for Windows, which is built on
// registered I/O when the system has it. The suite below runs on whichever the
// machine gives; this says which that was.
func TestNewBindIsTheWindowsDefault(t *testing.T) {
	// platformBind, not newBind: the suite swaps newBind for a loopback-only bind.
	name := fmt.Sprintf("%T", platformBind())
	switch name {
	case "*conn.WinRingBind":
	case "*conn.StdNetBind":
		t.Log("registered I/O is not available on this machine: the standard sockets are in use")
	default:
		t.Errorf("platformBind() is %s, want the default bind of wireguard-go for Windows", name)
	}
}

// A rebind closes the sockets and opens them again; the profile's ListenPort
// must come back, or the peers that know it would never reach the device again.
func TestRebindOfTheServerKeepsItsPortAndTheTunnel(t *testing.T) {
	checkLeaks(t)
	p := newPair(t, pairOptions{})
	p.up()
	before := p.server.stats().listenPort
	if int(before) != p.port {
		t.Fatalf("server listens on %d, want %d", before, p.port)
	}

	p.server.eng.Rebind()

	eventuallyWithin(t, promptly, "the server to rebind", func() bool { return p.server.logs.has("network changed, rebinding") })
	eventuallyWithin(t, promptly, "the server's socket to be back", func() bool { return p.server.stats().listenPort != 0 })
	if after := p.server.stats().listenPort; after != before {
		t.Errorf("listen port = %d after the rebind, want %d", after, before)
	}
	sendPacket(t, p.client.rec.tun, p.server.rec.tun, ping(serverTunnel, clientTunnel))
	sendPacket(t, p.server.rec.tun, p.client.rec.tun, ping(clientTunnel, serverTunnel))
}
