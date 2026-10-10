package wg

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/tun/tuntest"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// promptly is how long a handshake forced by Rebind may take. It has to stay
// below wireguard-go's own retry, which waits RekeyTimeout (5 s), or the
// tests could not tell the two apart.
const promptly = 2 * time.Second

func ping(dst, src string) []byte {
	return tuntest.Ping(netip.MustParseAddr(dst), netip.MustParseAddr(src))
}

// hostnameEndpoint makes the client look its server up by name.
func hostnameEndpoint(port int) string { return fmt.Sprintf("vpn.test:%d", port) }

func loopbackResolver(addr string) *fakeResolver {
	r := &fakeResolver{}
	r.set("vpn.test", addr)
	return r
}

func TestConnectingIsAnnouncedBeforeTheDeviceHasPeers(t *testing.T) {
	checkLeaks(t)
	p := newPair(t, pairOptions{})
	reached, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseAnnouncement := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseAnnouncement) // a failure must not leave the engine stuck for Stop
	p.client.rec.onAnnounce = func(in tunnel.Intent) error {
		if in.State == tunnel.StateConnecting {
			close(reached)
			<-release
		}
		return nil
	}
	p.startServer()

	// Start does not wait for the announcement, which is still pending below.
	p.client.start()
	select {
	case <-reached:
	case <-time.After(waitTimeout):
		t.Fatal("the Connecting intent was never announced")
	}

	// The Network is still working on the announcement: nothing may have been
	// sent, and the device must not even know its peer yet.
	time.Sleep(10 * testPoll)
	dump, err := p.client.eng.dev.IpcGet()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dump, "public_key=") || strings.Contains(dump, "private_key=") {
		t.Errorf("device was configured before the announcement returned:\n%s", redactKeys(dump))
	}
	if got := p.server.stats(); got.rxBytes != 0 || !got.lastHandshake.IsZero() {
		t.Errorf("the server heard from the client before the announcement returned: %+v", got)
	}
	if got, want := p.client.rec.kinds(), []string{"tun", "configure"}; !slices.Equal(got, want) {
		t.Errorf("events while announcing = %v, want %v", got, want)
	}

	releaseAnnouncement()
	p.client.waitState(tunnel.StateUp)
	if got, want := p.client.rec.kinds(), []string{"tun", "configure", "announce:connecting", "announce:up"}; !slices.Equal(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
}

func TestHandshakePacketsAndCounters(t *testing.T) {
	checkLeaks(t)
	p := newPair(t, pairOptions{extraInterface: "DNS = 10.200.0.1, corp.test\nMTU = 1380"})
	p.up()
	client, server := p.client, p.server

	t.Run("the client reports what it is", func(t *testing.T) {
		st := client.status.last()
		if st.State != tunnel.StateUp || st.Err != "" || st.Iface != tunnelName || st.Since.IsZero() {
			t.Errorf("status = %+v", st)
		}
		if want := fmt.Sprintf("127.0.0.1:%d", p.port); st.Remote != want {
			t.Errorf("Remote = %q, want %q", st.Remote, want)
		}
		if want := pfx("10.200.0.2/24"); !slices.Equal(st.Addresses, want) {
			t.Errorf("Addresses = %v, want %v", st.Addresses, want)
		}
		if got, want := client.status.states(), []tunnel.State{tunnel.StateConnecting, tunnel.StateUp}; !slices.Equal(got, want) {
			t.Errorf("states = %v, want %v", got, want)
		}
	})

	t.Run("the tunnel interface is set up with the profile", func(t *testing.T) {
		tuns, configs := client.rec.find("tun"), client.rec.find("configure")
		if len(tuns) != 1 || tuns[0].mtu != 1380 {
			t.Errorf("tun events = %+v, want one with MTU 1380", tuns)
		}
		if len(configs) != 1 || configs[0].name != tunnelName || configs[0].mtu != 1380 ||
			!slices.Equal(configs[0].addrs, pfx("10.200.0.2/24")) {
			t.Errorf("configure events = %+v", configs)
		}
	})

	t.Run("the client announces Connecting and then Up", func(t *testing.T) {
		got := client.rec.announces()
		if len(got) != 2 {
			t.Fatalf("announcements = %d, want 2: %+v", len(got), got)
		}
		connecting, up := got[0], got[1]
		wantEndpoints := addrs("127.0.0.1")
		if connecting.State != tunnel.StateConnecting || !slices.Equal(connecting.Endpoints, wantEndpoints) ||
			len(connecting.Routes) != 0 || len(connecting.DNS) != 0 {
			t.Errorf("connecting intent = %+v", connecting)
		}
		if up.State != tunnel.StateUp || up.Owner != "client" || up.Iface != tunnelName || up.Priority != 7 || up.Role != tunnel.RoleSplit {
			t.Errorf("up intent = %+v", up)
		}
		if want := pfx("10.200.0.0/24", "192.168.77.0/24"); !slices.Equal(up.Routes, want) {
			t.Errorf("up routes = %v, want %v", up.Routes, want)
		}
		if len(up.DNS) != 1 || !slices.Equal(up.DNS[0].Servers, addrs("10.200.0.1")) || !slices.Equal(up.DNS[0].MatchDomains, []string{"corp.test"}) {
			t.Errorf("up DNS = %+v", up.DNS)
		}
		if !slices.Equal(up.Endpoints, wantEndpoints) {
			t.Errorf("up endpoints = %v, want %v", up.Endpoints, wantEndpoints)
		}
		if up.UpSince.IsZero() || !up.UpSince.Equal(client.status.last().Since) {
			t.Errorf("UpSince = %v, status Since = %v", up.UpSince, client.status.last().Since)
		}
	})

	t.Run("the server, which has no endpoint to reach, announces none", func(t *testing.T) {
		got := server.rec.announces()
		if len(got) != 2 || got[0].State != tunnel.StateConnecting || got[1].State != tunnel.StateUp {
			t.Fatalf("announcements = %+v", got)
		}
		if len(got[0].Endpoints) != 0 || len(got[1].Endpoints) != 0 {
			t.Errorf("endpoints = %v, %v", got[0].Endpoints, got[1].Endpoints)
		}
		if want := pfx("10.200.0.2/32"); !slices.Equal(got[1].Routes, want) {
			t.Errorf("up routes = %v, want %v", got[1].Routes, want)
		}
	})

	t.Run("packets pass both ways and the counters move", func(t *testing.T) {
		before := client.stats()
		for range 3 {
			sendPacket(t, client.rec.tun, server.rec.tun, ping(serverTunnel, clientTunnel))
			sendPacket(t, server.rec.tun, client.rec.tun, ping(clientTunnel, serverTunnel))
		}
		eventually(t, "the client status to count the packets", func() bool {
			s := client.status.last().Stats
			return s.TxBytes > before.txBytes && s.RxBytes > before.rxBytes
		})
		eventually(t, "the server status to count the packets", func() bool {
			s := server.status.last().Stats
			return s.TxBytes > 0 && s.RxBytes > 0
		})
		if got, want := client.status.last().Stats.TxBytes, client.stats().txBytes; got > want {
			t.Errorf("status TxBytes %d exceeds the device's %d", got, want)
		}
	})
}

func TestStopLeavesNothingBehind(t *testing.T) {
	checkLeaks(t)
	p := newPair(t, pairOptions{})
	p.up()
	client := p.client

	withdrawing, proceed := make(chan struct{}), make(chan struct{})
	client.rec.onWithdraw = func() {
		close(withdrawing)
		<-proceed
	}
	stopped := make(chan error, 1)
	go func() { stopped <- client.eng.Stop(context.Background()) }()
	select {
	case <-withdrawing:
	case <-time.After(waitTimeout):
		t.Fatal("Stop never withdrew the intent")
	}
	// The status says so before the teardown starts, and the channel is open.
	eventually(t, "Disconnecting", func() bool { return client.status.last().State == tunnel.StateDisconnecting })
	select {
	case <-client.status.done:
		t.Fatal("the status channel closed before the teardown finished")
	default:
	}
	close(proceed)
	if err := <-stopped; err != nil {
		t.Fatalf("Stop: %v", err)
	}

	select {
	case <-client.status.done:
	case <-time.After(waitTimeout):
		t.Fatal("the status channel was not closed")
	}
	want := []tunnel.State{tunnel.StateConnecting, tunnel.StateUp, tunnel.StateDisconnecting, tunnel.StateDisconnected}
	if got := client.status.states(); !slices.Equal(got, want) {
		t.Errorf("states = %v, want %v", got, want)
	}
	if st := client.status.last(); st.Iface != "" || !st.Since.IsZero() {
		t.Errorf("final status = %+v, want no interface", st)
	}
	wantKinds := []string{"tun", "configure", "announce:connecting", "announce:up", "withdraw"}
	if got := client.rec.kinds(); !slices.Equal(got, wantKinds) {
		t.Errorf("events = %v, want %v", got, wantKinds)
	}
	select {
	case _, open := <-client.rec.tun.TUN().Events():
		if open {
			t.Error("the tunnel device is still open")
		}
	case <-time.After(waitTimeout):
		t.Error("the tunnel device was not closed")
	}

	// Stop is idempotent and the other calls are harmless afterwards.
	if err := client.eng.Stop(context.Background()); err != nil {
		t.Errorf("second Stop: %v", err)
	}
	client.eng.Rebind()
	if err := client.eng.Start(context.Background()); err == nil {
		t.Error("Start after Stop succeeded")
	}
	if got := len(client.rec.find("withdraw")); got != 1 {
		t.Errorf("withdrew %d times, want 1", got)
	}
}

func TestStopBeforeStartClosesTheChannel(t *testing.T) {
	checkLeaks(t)
	n := newNode(t, "idle", clientProfile(newKeyPair(t), newKeyPair(t), "127.0.0.1:1", "", ""), nodeOptions{})
	if err := n.eng.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-n.status.done:
	case <-time.After(waitTimeout):
		t.Fatal("the status channel was not closed")
	}
	if got := n.rec.kinds(); len(got) != 0 {
		t.Errorf("events = %v, want none", got)
	}
}

func TestStartTwiceFails(t *testing.T) {
	checkLeaks(t)
	n := newNode(t, "twice", clientProfile(newKeyPair(t), newKeyPair(t), "127.0.0.1:1", "", ""), nodeOptions{})
	n.start()
	if err := n.eng.Start(context.Background()); err == nil {
		t.Error("second Start succeeded")
	}
	eventually(t, "the tunnel device", func() bool { return len(n.rec.find("tun")) > 0 })
	if got := len(n.rec.find("tun")); got != 1 {
		t.Errorf("created %d tunnel devices, want 1", got)
	}
}

func TestProvideCredentialsIsRefused(t *testing.T) {
	n := newNode(t, "creds", clientProfile(newKeyPair(t), newKeyPair(t), "127.0.0.1:1", "", ""), nodeOptions{})
	if err := n.eng.ProvideCredentials(tunnel.CredentialUserPassword, "u", "p"); err == nil {
		t.Error("ProvideCredentials succeeded")
	}
}

func TestRebindShakesHandsAgain(t *testing.T) {
	checkLeaks(t)
	resolver := loopbackResolver("127.0.0.1")
	p := newPair(t, pairOptions{endpoint: hostnameEndpoint, client: nodeOptions{resolver: resolver}})
	p.up()
	client, server := p.client, p.server
	if got := resolver.callCount(); got != 1 {
		t.Fatalf("lookups before Rebind = %d, want 1", got)
	}
	clientShook, serverShook := client.stats().lastHandshake, server.stats().lastHandshake
	announced := len(client.rec.announces())

	client.eng.Rebind()

	want := []tunnel.State{tunnel.StateConnecting, tunnel.StateUp, tunnel.StateReconnecting, tunnel.StateUp}
	eventuallyWithin(t, promptly, "Reconnecting and then Up", func() bool { return slices.Equal(client.status.states(), want) })
	if !client.stats().lastHandshake.After(clientShook) {
		t.Error("Rebind did not lead to a new handshake")
	}
	// The server counts the handshake once the client's first packet arrives.
	eventuallyWithin(t, promptly, "the server to see the new handshake", func() bool {
		return server.stats().lastHandshake.After(serverShook)
	})
	if got := resolver.callCount(); got != 2 {
		t.Errorf("lookups after Rebind = %d, want 2", got)
	}
	if got := len(client.rec.announces()); got != announced {
		t.Errorf("Rebind announced %d more times although the endpoint is the same", got-announced)
	}
	// The tunnel still carries packets.
	sendPacket(t, client.rec.tun, server.rec.tun, ping(serverTunnel, clientTunnel))
	sendPacket(t, server.rec.tun, client.rec.tun, ping(clientTunnel, serverTunnel))
}

// testListenHost is where a test binds a UDP port it wants the device to find
// taken: where the device itself listens. On Windows that is loopback only (see
// loopbackbind_windows_test.go); a socket on every address makes the firewall
// ask the person at the keyboard.
func testListenHost() string {
	if runtime.GOOS == "windows" {
		return "127.0.0.1"
	}
	return "0.0.0.0"
}

// otherLocalIPv4 is an address of this host besides 127.0.0.1 that the server
// socket, bound to every address, answers on. On Windows the test sockets
// listen on 127.0.0.2 as well, which keeps the test on loopback.
func otherLocalIPv4(t *testing.T) netip.Addr {
	t.Helper()
	if runtime.GOOS == "windows" {
		return netip.MustParseAddr("127.0.0.2")
	}
	ifaceAddrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Skipf("cannot list the host's addresses: %v", err)
	}
	for _, a := range ifaceAddrs {
		if ipNet, ok := a.(*net.IPNet); ok && ipNet.IP.To4() != nil && ipNet.IP.IsGlobalUnicast() {
			addr, _ := netip.AddrFromSlice(ipNet.IP.To4())
			return addr
		}
	}
	t.Skip("the host has no IPv4 address besides loopback")
	return netip.Addr{}
}

// The new address is IPv4 as well: wireguard-go (the pinned version) sends to a
// wrong address when a pooled address that last served IPv4 serves an IPv6
// endpoint, so a test must not switch families.
func TestRebindFollowsANewEndpointAddress(t *testing.T) {
	other := otherLocalIPv4(t)
	checkLeaks(t)
	resolver := loopbackResolver("127.0.0.1")
	p := newPair(t, pairOptions{endpoint: hostnameEndpoint, client: nodeOptions{resolver: resolver}})
	p.up()
	client := p.client
	first := client.rec.announces()[1]

	// The sockets shake hands with the old address while the lookup runs; the
	// server drops an initiation that follows another within 20 ms, which a
	// real lookup does not beat either.
	resolver.setDelay(50 * time.Millisecond)
	resolver.set("vpn.test", other.String())
	client.eng.Rebind()

	eventually(t, "a new announcement", func() bool { return len(client.rec.announces()) == 3 })
	eventuallyWithin(t, promptly, "the server to hear the client at the new address", func() bool {
		dump, err := p.server.eng.dev.IpcGet()
		if err != nil {
			t.Fatal(err)
		}
		return strings.Contains(dump, "endpoint="+other.String()+":")
	})
	wantStates := []tunnel.State{tunnel.StateConnecting, tunnel.StateUp, tunnel.StateReconnecting, tunnel.StateUp}
	eventuallyWithin(t, promptly, "Reconnecting and then Up", func() bool { return slices.Equal(client.status.states(), wantStates) })
	again := client.rec.announces()[2]
	if again.State != tunnel.StateUp || !slices.Equal(again.Endpoints, []netip.Addr{other}) {
		t.Errorf("announcement after Rebind = %+v, want Up with the endpoint %v", again, other)
	}
	if !again.UpSince.Equal(first.UpSince) || !slices.Equal(again.Routes, first.Routes) || again.Iface != first.Iface {
		t.Errorf("announcement after Rebind = %+v, want the same tunnel as %+v", again, first)
	}
	dump, err := client.eng.dev.IpcGet()
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("endpoint=%v:%d", other, p.port); !strings.Contains(dump, want) {
		t.Errorf("device state lacks %q:\n%s", want, redactKeys(dump))
	}
	sendPacket(t, client.rec.tun, p.server.rec.tun, ping(serverTunnel, clientTunnel))
}

func TestRebindKeepsTheOldAddressWhenResolvingFails(t *testing.T) {
	checkLeaks(t)
	resolver := loopbackResolver("127.0.0.1")
	p := newPair(t, pairOptions{endpoint: hostnameEndpoint, client: nodeOptions{resolver: resolver}})
	p.up()
	client := p.client
	shook := client.stats().lastHandshake
	announced := len(client.rec.announces())

	resolver.setFailure(errors.New("no network"))
	client.eng.Rebind()

	eventuallyWithin(t, promptly, "a new handshake to the old address", func() bool {
		return client.stats().lastHandshake.After(shook) && client.status.last().State == tunnel.StateUp
	})
	eventually(t, "the failed lookup to be logged", func() bool {
		return client.logs.has("keeping the previous address: vpn.test: no network")
	})
	if got := len(client.rec.announces()); got != announced {
		t.Errorf("announced %d more times after a failed lookup", got-announced)
	}
}

// The sockets and the handshake do not wait for the lookup: the name usually
// still points to the same server, and a lookup right after a network change
// can take until its timeout.
func TestRebindShakesHandsWhileTheLookupHangs(t *testing.T) {
	checkLeaks(t)
	resolver := loopbackResolver("127.0.0.1")
	p := newPair(t, pairOptions{endpoint: hostnameEndpoint, client: nodeOptions{resolver: resolver}})
	p.up()
	client := p.client
	shook := client.stats().lastHandshake

	resolver.setHang(true)
	client.eng.Rebind()

	eventuallyWithin(t, promptly, "a new handshake while the lookup hangs", func() bool {
		return client.stats().lastHandshake.After(shook)
	})
	eventually(t, "Reconnecting", func() bool { return slices.Contains(client.status.states(), tunnel.StateReconnecting) })
	if got := resolver.callCount(); got != 2 {
		t.Errorf("lookups = %d, want 2", got)
	}
}

// flakyBind is a bind whose Open fails on demand, as the sockets of a device do
// while their port is still held.
type flakyBind struct {
	conn.Bind
	mu    sync.Mutex
	fail  error    // what Open returns while it is set
	opens []uint16 // the port of every Open
}

func (b *flakyBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	b.mu.Lock()
	b.opens = append(b.opens, port)
	fail := b.fail
	b.mu.Unlock()
	if fail != nil {
		return nil, 0, fail
	}
	return b.Bind.Open(port)
}

func (b *flakyBind) setFailure(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fail = err
}

func (b *flakyBind) openAttempts() []uint16 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.opens)
}

// useFlakyBinds makes the engines of the test open their sockets through
// flakyBinds, which it returns in the order the devices were made.
func useFlakyBinds(t *testing.T) func() []*flakyBind {
	t.Helper()
	var mu sync.Mutex
	var binds []*flakyBind
	old := newBind
	t.Cleanup(func() { newBind = old })
	newBind = func() conn.Bind {
		mu.Lock()
		defer mu.Unlock()
		binds = append(binds, &flakyBind{Bind: old()})
		return binds[len(binds)-1]
	}
	return func() []*flakyBind {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(binds)
	}
}

// Closing the sockets and opening them again can fail. The tunnel has no sockets
// then, and has to get them back without waiting for the next change of the network.
func TestRebindGetsTheSocketsBackWhenOpeningThemFailsAtFirst(t *testing.T) {
	checkLeaks(t)
	binds := useFlakyBinds(t)
	p := newPair(t, pairOptions{})
	p.up()
	clientBind := binds()[1] // the server's device was made first
	inUse := errors.New("listen udp4: bind: address already in use")
	clientBind.setFailure(inUse)

	p.client.eng.Rebind()
	eventually(t, "the failure to be logged", func() bool { return p.client.logs.has("rebind sockets, trying again: " + inUse.Error()) })
	eventually(t, "more attempts", func() bool { return len(clientBind.openAttempts()) >= 4 })
	// One line for one reason, however many times it is tried.
	if lines := p.client.logs.count("rebind sockets"); lines != 1 {
		t.Errorf("%d log lines about the sockets, want 1", lines)
	}

	clientBind.setFailure(nil)
	eventually(t, "the sockets to be open again", func() bool { return p.client.logs.has("UDP sockets are open again") })
	p.client.waitState(tunnel.StateUp)
	sendPacket(t, p.client.rec.tun, p.server.rec.tun, ping(serverTunnel, clientTunnel))
	sendPacket(t, p.server.rec.tun, p.client.rec.tun, ping(clientTunnel, serverTunnel))
}

// A failed update leaves wireguard-go on a random port; a profile that fixes
// the port has to get it back.
func TestReopeningTheSocketsKeepsTheFixedListenPort(t *testing.T) {
	checkLeaks(t)
	binds := useFlakyBinds(t)
	port := freeUDPPort(t)
	p := newPair(t, pairOptions{extraInterface: fmt.Sprintf("ListenPort = %d", port)})
	p.up()
	clientBind := binds()[1]
	clientBind.setFailure(errors.New("address already in use"))

	p.client.eng.Rebind()
	eventually(t, "the sockets to be gone", func() bool { return p.client.logs.has("rebind sockets") })
	clientBind.setFailure(nil)
	eventually(t, "the sockets to be open again", func() bool { return p.client.logs.has("UDP sockets are open again") })

	attempts := clientBind.openAttempts()
	if last := attempts[len(attempts)-1]; last != uint16(port) {
		t.Errorf("the last Open asked for port %d, want the fixed port %d; all: %v", last, port, attempts)
	}
	if got := p.client.stats().listenPort; got != uint16(port) {
		t.Errorf("the device listens on port %d, want %d", got, port)
	}
	p.client.waitState(tunnel.StateUp)
}

func TestRebindBeforeStartIsForgotten(t *testing.T) {
	checkLeaks(t)
	resolver := loopbackResolver("127.0.0.1")
	n := newNode(t, "early", clientProfile(newKeyPair(t), newKeyPair(t), hostnameEndpoint(freeUDPPort(t)), "", ""), nodeOptions{resolver: resolver})
	n.eng.Rebind()
	n.start()
	time.Sleep(10 * testPoll)
	if got := resolver.callCount(); got != 1 {
		t.Errorf("lookups = %d, want only Start's own", got)
	}
}

func TestWithoutAHandshakeTheTunnelStaysConnecting(t *testing.T) {
	checkLeaks(t)
	// ModeFull on a split profile adds a warning of its own.
	n := newNode(t, "lonely", clientProfile(newKeyPair(t), newKeyPair(t), fmt.Sprintf("127.0.0.1:%d", freeUDPPort(t)), "", ""),
		nodeOptions{mode: tunnel.ModeFull})
	n.start()

	fullWarning := "Full tunnel ignored: AllowedIPs do not cover all addresses"
	eventually(t, "the first status", func() bool { return n.status.first().State == tunnel.StateConnecting })
	if got := n.status.first().Warnings; !slices.Equal(got, []string{fullWarning}) {
		t.Errorf("warnings at the start = %q", got)
	}
	if got := n.status.last().Err; got != "" {
		t.Errorf("Err = %q before the grace period is over", got)
	}
	eventually(t, "the reason in the status", func() bool { return n.status.last().Err != "" })
	time.Sleep(5 * testPoll)

	const reason = "no handshake with the peer yet"
	st := n.status.last()
	if st.State != tunnel.StateConnecting || st.Err != reason {
		t.Errorf("status = %+v, want Connecting with the error %q", st, reason)
	}
	if !slices.Equal(st.Warnings, []string{fullWarning}) {
		t.Errorf("warnings = %q, want only %q", st.Warnings, fullWarning)
	}
	if !n.logs.has(reason) {
		t.Error("the missing handshake was not logged")
	}
	if got, want := n.rec.kinds(), []string{"tun", "configure", "announce:connecting"}; !slices.Equal(got, want) {
		t.Errorf("events = %v, want %v: nothing may be announced as Up without a handshake", got, want)
	}
	if st.Stats.TxBytes == 0 {
		t.Error("no handshake initiation was sent")
	}
}

func TestLateHandshakeClearsTheReason(t *testing.T) {
	checkLeaks(t)
	p := newPair(t, pairOptions{})
	p.client.start()
	eventually(t, "the reason in the status", func() bool { return p.client.status.last().Err != "" })

	p.startServer()
	// wireguard-go would try again within RekeyTimeout; a change of the network
	// does not wait for that.
	p.client.eng.Rebind()
	p.client.waitState(tunnel.StateUp)
	if st := p.client.status.last(); st.Err != "" {
		t.Errorf("Err = %q after the handshake", st.Err)
	}
	if got, want := p.client.status.states(), []tunnel.State{tunnel.StateConnecting, tunnel.StateUp}; !slices.Equal(got, want) {
		t.Errorf("states = %v, want %v", got, want)
	}
}

// The endpoint name may not resolve because the network is not up yet, which
// is the case when the daemon starts at boot. The tunnel waits and tries
// again; it does not fail.
func TestEndpointThatDoesNotResolveYetKeepsTheTunnelConnecting(t *testing.T) {
	checkLeaks(t)
	resolver := loopbackResolver("127.0.0.1")
	resolver.setFailure(errors.New("no such host"))
	p := newPair(t, pairOptions{endpoint: hostnameEndpoint, client: nodeOptions{resolver: resolver}})
	p.startServer()
	p.client.start()
	client := p.client

	if got := client.status.last(); got.Err != "" {
		t.Errorf("Err = %q before the grace period is over", got.Err)
	}
	eventually(t, "the reason in the status", func() bool { return client.status.last().Err != "" })
	time.Sleep(5 * testPoll)

	st := client.status.last()
	if want := "resolve endpoint: vpn.test: no such host"; st.State != tunnel.StateConnecting || st.Err != want {
		t.Errorf("status = %+v, want Connecting with the error %q", st, want)
	}
	if got := resolver.callCount(); got < 3 {
		t.Errorf("lookups = %d, want it to try again", got)
	}
	if got := client.rec.kinds(); len(got) != 0 {
		t.Errorf("events = %v, want none before the endpoint is known", got)
	}

	resolver.setFailure(nil)
	client.waitState(tunnel.StateUp)
	if st := client.status.last(); st.Err != "" {
		t.Errorf("Err = %q after the tunnel came up", st.Err)
	}
	if got, want := client.status.states(), []tunnel.State{tunnel.StateConnecting, tunnel.StateUp}; !slices.Equal(got, want) {
		t.Errorf("states = %v, want %v", got, want)
	}
	if got, want := client.rec.kinds(), []string{"tun", "configure", "announce:connecting", "announce:up"}; !slices.Equal(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
}

func TestLookupRetriesBackOffUpToACap(t *testing.T) {
	checkLeaks(t)
	resolver := &fakeResolver{err: errors.New("no such host")}
	const retryMin, retryMax = 20 * time.Millisecond, 80 * time.Millisecond
	n := newNode(t, "backoff", clientProfile(newKeyPair(t), newKeyPair(t), "vpn.test:51820", "", ""),
		nodeOptions{resolver: resolver, retryMin: retryMin, retryMax: retryMax})
	n.start()

	const lookups = 8
	eventually(t, "the lookups", func() bool { return resolver.callCount() >= lookups })
	times := resolver.callTimes()
	// A timer never fires early, so these are lower bounds. Without the cap the
	// last pause alone would be 1.28 s.
	for i, want := range []time.Duration{retryMin, 2 * retryMin, retryMax, retryMax, retryMax, retryMax, retryMax} {
		if got := times[i+1].Sub(times[i]); got < want {
			t.Errorf("pause %d = %v, want at least %v", i+1, got, want)
		}
	}
	if got := times[lookups-1].Sub(times[0]); got > 1500*time.Millisecond {
		t.Errorf("%d lookups took %v, the pause is not capped", lookups, got)
	}
}

func TestRebindRetriesTheLookupAtOnce(t *testing.T) {
	checkLeaks(t)
	resolver := loopbackResolver("127.0.0.1")
	resolver.setFailure(errors.New("no such host"))
	p := newPair(t, pairOptions{endpoint: hostnameEndpoint, client: nodeOptions{resolver: resolver, retryMin: time.Hour, retryMax: time.Hour}})
	p.startServer()
	p.client.start()
	eventually(t, "the first lookup", func() bool { return resolver.callCount() == 1 })

	p.client.eng.Rebind()
	eventuallyWithin(t, promptly, "a second lookup without the pause", func() bool { return resolver.callCount() == 2 })

	resolver.setFailure(nil)
	p.client.eng.Rebind()
	p.client.waitState(tunnel.StateUp)
}

// Start has to return at once: the daemon starts the auto-connect profiles
// before it serves its socket, and a lookup may take until its timeout.
func TestStopDuringTheLookupLeavesNothingBehind(t *testing.T) {
	checkLeaks(t)
	resolver := &fakeResolver{hang: true}
	n := newNode(t, "slow", clientProfile(newKeyPair(t), newKeyPair(t), "vpn.test:51820", "", ""), nodeOptions{resolver: resolver})
	n.start()
	eventually(t, "the lookup", func() bool { return resolver.callCount() == 1 })
	// The status reaches the recorder on a goroutine of its own, so it is waited for and not read at once.
	if st := n.waitState(tunnel.StateConnecting); st.Err != "" {
		t.Errorf("status = %+v, want Connecting without an error", st)
	}

	stopWithin(t, n, promptly)
	if slices.Contains(n.status.states(), tunnel.StateFailed) {
		t.Errorf("states = %v: ending the lookup is not a failure", n.status.states())
	}
	if got := n.rec.kinds(); len(got) != 0 {
		t.Errorf("events = %v, want none", got)
	}
}

func TestStopDuringTheInterfaceSetupLeavesNothingBehind(t *testing.T) {
	checkLeaks(t)
	rec := newRecorder()
	configuring := make(chan struct{})
	rec.onConfigure = func(ctx context.Context) error {
		close(configuring)
		<-ctx.Done()
		return errors.New("signal: killed") // what exec reports for a killed command
	}
	n := newNode(t, "slow-configure", clientProfile(newKeyPair(t), newKeyPair(t), "127.0.0.1:1", "", ""), nodeOptions{rec: rec})
	n.start()
	select {
	case <-configuring:
	case <-time.After(waitTimeout):
		t.Fatal("the interface was never configured")
	}

	stopWithin(t, n, promptly)
	if slices.Contains(n.status.states(), tunnel.StateFailed) {
		t.Errorf("states = %v: ending the setup is not a failure", n.status.states())
	}
	if got, want := rec.kinds(), []string{"tun", "configure"}; !slices.Equal(got, want) {
		t.Errorf("events = %v, want %v: nothing may be announced after Stop", got, want)
	}
	requireTunClosed(t, rec)
}

func TestStopWhileWaitingForTheHandshakeLeavesNothingBehind(t *testing.T) {
	checkLeaks(t)
	n := newNode(t, "lonely", clientProfile(newKeyPair(t), newKeyPair(t), fmt.Sprintf("127.0.0.1:%d", freeUDPPort(t)), "", ""), nodeOptions{})
	n.start()
	eventually(t, "the Connecting announcement", func() bool { return slices.Contains(n.rec.kinds(), "announce:connecting") })
	eventually(t, "a handshake initiation", func() bool { return n.status.last().Stats.TxBytes > 0 })

	stopWithin(t, n, promptly)
	if got, want := n.rec.kinds(), []string{"tun", "configure", "announce:connecting", "withdraw"}; !slices.Equal(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
	requireTunClosed(t, n.rec)
}

// stopWithin stops the engine, which must not take longer than limit, and
// checks what Stop promises: a closed status channel that ends in Disconnected.
func stopWithin(t *testing.T, n *node, limit time.Duration) {
	t.Helper()
	stopped := make(chan error, 1)
	go func() { stopped <- n.eng.Stop(context.Background()) }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(limit):
		t.Fatalf("Stop did not return within %v", limit)
	}
	select {
	case <-n.status.done:
	case <-time.After(waitTimeout):
		t.Fatal("the status channel was not closed")
	}
	if st := n.status.last(); st.State != tunnel.StateDisconnected || st.Err != "" || st.Iface != "" {
		t.Errorf("final status = %+v, want Disconnected without an interface or an error", st)
	}
}

// requireTunClosed waits for the tunnel device's event channel to close, which
// is what closing the device does. An event wireguard-go did not get to read
// before the close may still be in it.
func requireTunClosed(t *testing.T, rec *recorder) {
	t.Helper()
	events := rec.tun.TUN().Events()
	deadline := time.After(waitTimeout)
	for {
		select {
		case _, open := <-events:
			if !open {
				return
			}
		case <-deadline:
			t.Error("the tunnel device was not closed")
			return
		}
	}
}

// portTakenMessage is how this OS words "that UDP port is taken". It reaches
// the engine only as text inside wireguard-go's IPC error, so the test asks
// the OS for it by binding the port a second time.
func portTakenMessage(t *testing.T, port int) string {
	t.Helper()
	second, err := net.ListenPacket("udp4", fmt.Sprintf("%s:%d", testListenHost(), port))
	if err == nil {
		second.Close()
		t.Fatalf("UDP port %d can be bound twice", port)
	}
	var sysErr *os.SyscallError
	if !errors.As(err, &sysErr) {
		t.Fatalf("binding a taken port failed with %v, want a system error", err)
	}
	return sysErr.Err.Error()
}

// Start itself fails only when it is given up on before it begins; the engine
// reports what goes wrong afterwards in its status, and these failures do not
// pass by themselves.
func TestStartFailures(t *testing.T) {
	busy, err := net.ListenPacket("udp4", testListenHost()+":0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	busyPort := busy.LocalAddr().(*net.UDPAddr).Port
	portTaken := portTakenMessage(t, busyPort)

	tests := []struct {
		name      string
		setup     func(t *testing.T, rec *recorder) (profile string)
		wantErr   []string // the error contains one of these
		wantKinds []string
	}{
		{
			name: "tunnel device cannot be created",
			setup: func(t *testing.T, rec *recorder) string {
				rec.tunErr = errors.New("operation not permitted")
				return clientProfile(newKeyPair(t), newKeyPair(t), "127.0.0.1:1", "", "")
			},
			wantErr: []string{"create tunnel device: operation not permitted"},
		},
		{
			name: "interface cannot be configured",
			setup: func(t *testing.T, rec *recorder) string {
				rec.configureErr = errors.New("configure failed")
				return clientProfile(newKeyPair(t), newKeyPair(t), "127.0.0.1:1", "", "")
			},
			wantErr:   []string{"configure loopbackTun1: configure failed"},
			wantKinds: []string{"tun", "configure"},
		},
		{
			name: "listen port is in use",
			setup: func(t *testing.T, _ *recorder) string {
				return serverProfile(newKeyPair(t), newKeyPair(t), busyPort)
			},
			// wireguard-go reports a port that is taken itself, or leaves the
			// device on another port, which the engine notices.
			wantErr:   []string{portTaken, "cannot listen on port"},
			wantKinds: []string{"tun", "configure", "announce:connecting", "withdraw"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkLeaks(t)
			rec := newRecorder()
			n := newNode(t, "failing", tt.setup(t, rec), nodeOptions{rec: rec})
			n.start()

			eventually(t, "the Failed status", func() bool { return n.status.last().State == tunnel.StateFailed })
			st := n.status.last()
			if !slices.ContainsFunc(tt.wantErr, func(want string) bool { return strings.Contains(st.Err, want) }) {
				t.Errorf("status Err = %q, want it to contain one of %q", st.Err, tt.wantErr)
			}
			if got := rec.kinds(); !slices.Equal(got, tt.wantKinds) {
				t.Errorf("events = %v, want %v", got, tt.wantKinds)
			}

			if err := n.eng.Stop(context.Background()); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			<-n.status.done
			if final := n.status.last(); final.State != tunnel.StateFailed || final.Err != st.Err {
				t.Errorf("final status = %+v, want the failure kept", final)
			}
			if got := rec.kinds(); !slices.Equal(got, tt.wantKinds) {
				t.Errorf("events after Stop = %v, want %v", got, tt.wantKinds)
			}
			if slices.Contains(tt.wantKinds, "tun") {
				requireTunClosed(t, rec)
			}
		})
	}
}

func TestStartWithACancelledContextDoesNothing(t *testing.T) {
	checkLeaks(t)
	n := newNode(t, "cancelled", clientProfile(newKeyPair(t), newKeyPair(t), "127.0.0.1:1", "", ""), nodeOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := n.eng.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Start error = %v, want context.Canceled", err)
	}
	if got := n.rec.kinds(); len(got) != 0 {
		t.Errorf("events = %v, want none", got)
	}
}

func TestAnnounceFailureDoesNotStopTheTunnel(t *testing.T) {
	checkLeaks(t)
	p := newPair(t, pairOptions{})
	p.client.rec.onAnnounce = func(tunnel.Intent) error { return errors.New("route table busy") }
	p.up()

	for _, want := range []string{"announce connecting: route table busy", "announce up: route table busy"} {
		if !p.client.logs.has(want) {
			t.Errorf("log lacks %q", want)
		}
	}
	sendPacket(t, p.client.rec.tun, p.server.rec.tun, ping(serverTunnel, clientTunnel))
}

// breakableTun is a channel TUN whose reads start failing on demand, as a
// tun device does when its interface is destroyed from outside.
type breakableTun struct {
	tun.Device
	source *tuntest.ChannelTUN
	broken chan struct{}
	done   chan struct{}
	once   sync.Once
}

func (b *breakableTun) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	select {
	case <-b.broken:
		return 0, errors.New("interface destroyed")
	case <-b.done:
		return 0, os.ErrClosed
	case packet := <-b.source.Outbound:
		sizes[0] = copy(bufs[0][offset:], packet)
		return 1, nil
	}
}

func (b *breakableTun) Close() error {
	b.once.Do(func() { close(b.done) })
	return b.Device.Close()
}

func TestTunnelDeviceFailingEndsTheTunnel(t *testing.T) {
	checkLeaks(t)
	rec := newRecorder()
	broken := make(chan struct{})
	rec.wrapTun = func(c *tuntest.ChannelTUN) tun.Device {
		return &breakableTun{Device: c.TUN(), source: c, broken: broken, done: make(chan struct{})}
	}
	p := newPair(t, pairOptions{client: nodeOptions{rec: rec}})
	p.up()

	close(broken)
	eventually(t, "the Failed status", func() bool { return p.client.status.last().State == tunnel.StateFailed })
	if got := p.client.status.last().Err; got != "tunnel device closed" {
		t.Errorf("Err = %q", got)
	}
	// The routes belong to an interface that no longer exists.
	eventually(t, "the withdrawal", func() bool { return len(rec.find("withdraw")) == 1 })

	if err := p.client.eng.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-p.client.status.done
	if got := p.client.status.last().State; got != tunnel.StateFailed {
		t.Errorf("final state = %v, want the failure kept", got)
	}
	if got := len(rec.find("withdraw")); got != 1 {
		t.Errorf("withdrew %d times, want 1", got)
	}
}

func TestModeShapesTheAnnouncement(t *testing.T) {
	tests := []struct {
		name       string
		mode       tunnel.Mode
		wantRole   tunnel.Role
		wantRoutes []netip.Prefix
		wantDNS    []string
	}{
		{"auto follows the profile", tunnel.ModeAuto, tunnel.RoleFull, pfx("10.200.0.0/24", "192.168.77.0/24", "0.0.0.0/0"), []string{"corp.test", "."}},
		{"split drops the default route", tunnel.ModeSplit, tunnel.RoleSplit, pfx("10.200.0.0/24", "192.168.77.0/24"), []string{"corp.test"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkLeaks(t)
			p := newPair(t, pairOptions{
				extraInterface: "DNS = 10.200.0.1, corp.test",
				extraPeer:      "AllowedIPs = 0.0.0.0/0",
				client:         nodeOptions{mode: tt.mode},
			})
			p.up()
			up := p.client.rec.announces()[1]
			if up.Role != tt.wantRole || !slices.Equal(up.Routes, tt.wantRoutes) {
				t.Errorf("role %v routes %v, want role %v routes %v", up.Role, up.Routes, tt.wantRole, tt.wantRoutes)
			}
			if len(up.DNS) != 1 || !slices.Equal(up.DNS[0].MatchDomains, tt.wantDNS) {
				t.Errorf("DNS = %+v, want domains %v", up.DNS, tt.wantDNS)
			}
		})
	}
}

// TestExcludePrivateIPs runs a real tunnel whose peer accepts everything: with
// the option the device and the announcement hold everything except the
// private ranges, the handshake happens all the same, and a packet for a
// private address is no longer sent into the tunnel.
func TestExcludePrivateIPs(t *testing.T) {
	tests := []struct {
		name        string
		exclude     bool
		wantAllowed []netip.Prefix // on the client's device
		wantRoutes  []netip.Prefix
		wantSent    bool // a packet for 192.168.77.5 reaches the server
	}{
		{"off keeps AllowedIPs as written", false,
			pfx("10.200.0.0/24", "192.168.77.0/24", "0.0.0.0/0"), pfx("10.200.0.0/24", "192.168.77.0/24", "0.0.0.0/0"), true},
		{"on keeps everything but the private ranges", true, publicV4, publicV4, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkLeaks(t)
			p := newPair(t, pairOptions{
				extraPeer: "AllowedIPs = 0.0.0.0/0",
				client:    nodeOptions{excludePrivate: tt.exclude},
			})
			p.up()

			if got, want := p.client.allowedIPs(), prefixStrings(tt.wantAllowed); !slices.Equal(got, want) {
				t.Errorf("device AllowedIPs = %v, want %v", got, want)
			}
			up := p.client.rec.announces()[1]
			if up.Role != tunnel.RoleFull || !slices.Equal(up.Routes, tt.wantRoutes) {
				t.Errorf("role %v routes %v, want a full tunnel with routes %v", up.Role, up.Routes, tt.wantRoutes)
			}

			private := ping("192.168.77.5", clientTunnel)
			if tt.wantSent {
				sendPacket(t, p.client.rec.tun, p.server.rec.tun, private)
			} else {
				// Dropped by the client's device: the next packet to arrive at
				// the server is the one sent after it.
				select {
				case p.client.rec.tun.Outbound <- private:
				case <-time.After(waitTimeout):
					t.Fatal("tunnel device did not read the packet")
				}
			}
			sendPacket(t, p.client.rec.tun, p.server.rec.tun, ping("203.0.113.5", clientTunnel))
		})
	}
}

func TestExcludePrivateIPsWithNothingLeft(t *testing.T) {
	checkLeaks(t)
	p := newPair(t, pairOptions{client: nodeOptions{excludePrivate: true}})
	p.up() // the handshake does not depend on AllowedIPs

	if got := p.client.allowedIPs(); len(got) != 0 {
		t.Errorf("device AllowedIPs = %v, want none", got)
	}
	up := p.client.rec.announces()[1]
	if up.State != tunnel.StateUp || up.Role != tunnel.RoleSplit || len(up.Routes) != 0 {
		t.Errorf("up intent = %+v, want Up without routes", up)
	}
	if want := []string{"No route left after excluding private ranges"}; !slices.Equal(p.client.status.last().Warnings, want) {
		t.Errorf("warnings = %q, want %q", p.client.status.last().Warnings, want)
	}
}

func TestEndpointLookupPrefersIPv4(t *testing.T) {
	tests := []struct {
		name    string
		result  []string
		err     error
		want    string
		wantErr bool
	}{
		{"IPv4 first", []string{"192.0.2.1", "2001:db8::1"}, nil, "192.0.2.1", false},
		{"IPv4 after IPv6", []string{"2001:db8::1", "192.0.2.1"}, nil, "192.0.2.1", false},
		{"only IPv6", []string{"2001:db8::1", "2001:db8::2"}, nil, "2001:db8::1", false},
		{"IPv4 mapped into IPv6", []string{"::ffff:192.0.2.7"}, nil, "192.0.2.7", false},
		{"no addresses", nil, nil, "", true},
		{"lookup error", nil, errors.New("timeout"), "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := &fakeResolver{err: tt.err}
			resolver.set("vpn.test", tt.result...)
			n := newNode(t, "lookup", clientProfile(newKeyPair(t), newKeyPair(t), "vpn.test:51820", "", ""), nodeOptions{resolver: resolver})
			got, err := n.eng.lookup(context.Background(), "vpn.test")
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && got != netip.MustParseAddr(tt.want) {
				t.Errorf("address = %v, want %v", got, tt.want)
			}
		})
	}
}
