//go:build rootintegration

package ovpn

import (
	"net"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// TestRootRealTunnel brings a real utun tunnel up against a loopback openvpn
// server and checks what only root can show: the interface exists with the
// pushed address, it survives a Rebind (persist-tun), and it is gone after
// Stop. Routes and DNS are not touched: the engine runs openvpn with
// --route-noexec, and the Network here is a recorder.
//
// It creates and destroys a utun interface on the host, so it refuses to run
// unless PLAITWAY_ROOT_TESTS=1 and the effective uid is 0:
//
//	sudo env PLAITWAY_ROOT_TESTS=1 go test -tags rootintegration -run TestRootRealTunnel ./internal/ovpn
func TestRootRealTunnel(t *testing.T) {
	if os.Getenv("PLAITWAY_ROOT_TESTS") != "1" || os.Geteuid() != 0 {
		t.Skip("set PLAITWAY_ROOT_TESTS=1 and run as root to create a real utun interface")
	}
	bin := realBinary(t)
	p := newPKI(t)
	srv := startLoopbackServer(t, bin, p, serverOpts{
		password: "root test",
		pushes:   []string{"route 10.20.0.0 255.255.0.0"},
	})
	// "dev tun": a real interface, unlike the unprivileged tests.
	h := realHarness(t, bin, asusLoopbackProfile(p, srv.port, "tun"), tunnel.Spec{Owner: "root-test"})
	h.start()
	h.waitFor("a request for credentials", h.stateIs(tunnel.StateAwaitingCredentials))
	if err := h.eng.ProvideCredentials(tunnel.CredentialUserPassword, "alice", "root test"); err != nil {
		t.Fatal(err)
	}
	up := h.waitFor("Up", h.stateIs(tunnel.StateUp))

	if !regexp.MustCompile(`^utun\d+$`).MatchString(up.Iface) {
		t.Fatalf("Iface = %q, want a utun interface", up.Iface)
	}
	requireInterfaceAddress(t, up.Iface, "10.8.0.2")
	for _, c := range h.net.snapshot() {
		if c.intent.State == tunnel.StateUp && c.intent.Iface != up.Iface {
			t.Errorf("the Up intent names %q, the tunnel is %q", c.intent.Iface, up.Iface)
		}
	}

	// persist-tun: a restart of the connection keeps the same interface.
	h.eng.Rebind()
	h.waitForNew("Reconnecting", h.stateIs(tunnel.StateReconnecting))
	again := h.waitForNew("Up again", h.stateIs(tunnel.StateUp))
	if again.Iface != up.Iface {
		t.Errorf("the interface changed across a rebind: %q then %q", up.Iface, again.Iface)
	}
	requireInterfaceAddress(t, up.Iface, "10.8.0.2")

	h.stop()
	h.requireProcessGone()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if _, err := net.InterfaceByName(up.Iface); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("interface %s still exists after Stop", up.Iface)
		}
	}
}

func requireInterfaceAddress(t *testing.T, name, want string) {
	t.Helper()
	iface, err := net.InterfaceByName(name)
	if err != nil {
		t.Fatalf("interface %s: %v", name, err)
	}
	addrs, err := iface.Addrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.String() == want {
			return
		}
	}
	t.Fatalf("%s has addresses %v, want %s", name, addrs, want)
}
