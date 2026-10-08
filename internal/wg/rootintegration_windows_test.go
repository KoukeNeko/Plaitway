//go:build rootintegration && windows

package wg

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/tun"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
	"github.com/KoukeNeko/Plaitway/internal/winiface"
)

// These tests create real wintun adapters, so they need an elevated shell, and
// refuse to run otherwise. The first adapter creates installs the wintun driver
// from wintun.dll; that is the only change they leave on the machine. Build
// the DLL into bin\wintun first, from the repository root:
//
//	powershell -File packaging\windows\fetch-wintun.ps1 -OutputDirectory bin\wintun
//
// then, in an elevated PowerShell:
//
//	$env:PLAITWAY_ROOT_TESTS = '1'
//	go test -count=1 -tags rootintegration -run 'TestRoot' -v ./internal/wg
//
// PLAITWAY_WINTUN_DLL names another signed wintun.dll to use.
//
// Everything is scratch: adapters named Plaitway-test-*, the documentation
// ranges 198.51.100.0/24 and 203.0.113.0/24 on those adapters and nowhere
// else, and UDP on 127.0.0.1. No route, DNS entry, firewall rule or service is
// written, and every adapter is removed again, also when a test fails.
const (
	rootTestsEnv = "PLAITWAY_ROOT_TESTS"

	// rootAdapterPrefix starts every adapter of these tests. The engines use
	// it for theirs, and a test that fails midway has them removed by it.
	rootAdapterPrefix = "Plaitway-test-"
	// staleAdapterPrefix is the prefix of the adapters the crash tests leave
	// behind on purpose.
	staleAdapterPrefix = rootAdapterPrefix + "stale-"

	// The tunnel addresses, in two /30s of the documentation ranges. The
	// server's end is not an address of this machine: a datagram to it leaves
	// through the client's adapter instead of being delivered locally.
	clientAddress = "198.51.100.2"
	serverFarEnd  = "198.51.100.1"
	serverAddress = "203.0.113.2"
	subnetBits    = 30

	// The first adapter installs the driver, which takes a while.
	adapterTimeout = 90 * time.Second

	crashChildEnv      = "PLAITWAY_WG_CRASH_CHILD_ADAPTER"
	crashChildExitCode = 3
	datagramSize       = 1000
	discardPort        = 9
)

func requireElevated(t *testing.T) {
	t.Helper()
	if os.Getenv(rootTestsEnv) != "1" {
		t.Skipf("needs %s=1 and an elevated shell", rootTestsEnv)
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("needs an elevated shell")
	}
}

// requireWintun skips without a signed wintun.dll and makes its folder the one
// the engines load it from.
func requireWintun(t *testing.T) {
	t.Helper()
	useExecutableDir(t, filepath.Dir(signedWintun(t)))
}

// removeScratchAdapters removes whatever a failed run left, now and when the
// test ends. Adapters of the daemon have another prefix and are not touched.
func removeScratchAdapters(t *testing.T) {
	t.Helper()
	remove := func() {
		if removed, err := removeStaleAdapters(rootAdapterPrefix); err != nil {
			t.Errorf("remove scratch adapters: %v (removed %q)", err, removed)
		}
	}
	remove()
	t.Cleanup(remove)
}

func interfaceListed(name string) bool {
	_, err := net.InterfaceByName(name)
	return err == nil
}

func requireGone(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		eventuallyWithin(t, adapterTimeout, "adapter "+name+" to disappear", func() bool { return !interfaceListed(name) })
	}
	adapters, err := listWintunAdapters()
	if err != nil {
		t.Fatal(err)
	}
	for _, adapter := range adapters {
		if slices.Contains(names, adapter.name) {
			t.Errorf("adapter %s is still known to the device list", adapter.name)
		}
	}
}

func newWintunEngine(t *testing.T, owner, content string) (*engine, *statusLog, *recorder) {
	t.Helper()
	rec := newRecorder()
	cfg := Config{adapterPrefix: rootAdapterPrefix, pollInterval: testPoll, stallGrace: adapterTimeout}
	deps := tunnel.Deps{Network: rec, Net: fakeMonitor{}, Log: (&logSink{}).log}
	eng, err := newEngine(cfg.withDefaults(), tunnel.Spec{Owner: tunnel.OwnerID(owner), Name: owner, Content: []byte(content)}, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := eng.Stop(context.Background()); err != nil {
			t.Errorf("Stop(%s): %v", owner, err)
		}
	})
	return eng, collectStatus(eng.Status()), rec
}

func addressesOf(t *testing.T, iface string) []string {
	t.Helper()
	netIface, err := net.InterfaceByName(iface)
	if err != nil {
		t.Fatalf("interface %s: %v", iface, err)
	}
	addrs, err := netIface.Addrs()
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, addr := range addrs {
		texts = append(texts, addr.String())
	}
	return texts
}

// TestRootTwoWintunTunnelsHandshakeAndCarryADatagram runs two engines in this
// process, a server and a client that reaches it over 127.0.0.1, each on its
// own scratch adapter, and sends one datagram through the client's adapter.
func TestRootTwoWintunTunnelsHandshakeAndCarryADatagram(t *testing.T) {
	requireElevated(t)
	requireWintun(t)
	checkLeaks(t)
	removeScratchAdapters(t)

	serverKey, clientKey := newKeyPair(t), newKeyPair(t)
	port := freeUDPPort(t)
	serverContent := fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = %s/%d\nListenPort = %d\n\n[Peer]\nPublicKey = %s\nAllowedIPs = %s/32\n",
		serverKey.private, serverAddress, subnetBits, port, clientKey.public, clientAddress)
	clientContent := fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = %s/%d\n\n[Peer]\nPublicKey = %s\nEndpoint = 127.0.0.1:%d\nAllowedIPs = %s/32\n",
		clientKey.private, clientAddress, subnetBits, serverKey.public, port, serverFarEnd)

	server, serverStatus, _ := newWintunEngine(t, "root-server", serverContent)
	client, clientStatus, clientNetwork := newWintunEngine(t, "root-client", clientContent)
	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// A client that shakes hands before the server's socket is bound waits
	// RekeyTimeout for the next try, so it starts once the server's adapter has
	// its address, which the server's setup outlasts.
	eventuallyWithin(t, adapterTimeout, "the server's adapter to have its address", func() bool {
		iface := serverStatus.last().Iface
		return iface != "" && slices.Contains(addressesOf(t, iface), fmt.Sprintf("%s/%d", serverAddress, subnetBits))
	})
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	eventuallyWithin(t, adapterTimeout, "both tunnels to come up", func() bool {
		return serverStatus.last().State == tunnel.StateUp && clientStatus.last().State == tunnel.StateUp
	})

	serverIface, clientIface := serverStatus.last().Iface, clientStatus.last().Iface
	if want := adapterName(rootAdapterPrefix, "root-client"); clientIface != want {
		t.Errorf("client adapter = %q, want %q", clientIface, want)
	}
	if !strings.HasPrefix(serverIface, rootAdapterPrefix) || serverIface == clientIface {
		t.Errorf("server adapter = %q, want a scratch adapter of its own", serverIface)
	}
	assertAdapterIdentity(t, clientIface, "root-client")
	if addrs := addressesOf(t, clientIface); !slices.Contains(addrs, fmt.Sprintf("%s/%d", clientAddress, subnetBits)) {
		t.Errorf("%s has addresses %v, want %s", clientIface, addrs, clientAddress)
	}
	if netIface, err := net.InterfaceByName(clientIface); err != nil || netIface.MTU != 1420 {
		t.Errorf("client MTU = %v (%v), want 1420", netIface, err)
	}
	assertEndpointAnnounced(t, clientNetwork, clientIface)

	sentBefore, receivedBefore := clientStatus.last().Stats.TxBytes, serverStatus.last().Stats.RxBytes
	sendDatagram(t)
	eventuallyWithin(t, adapterTimeout, "the client's tx counter to count the datagram", func() bool {
		return clientStatus.last().Stats.TxBytes >= sentBefore+datagramSize
	})
	eventuallyWithin(t, adapterTimeout, "the server's rx counter to count the datagram", func() bool {
		return serverStatus.last().Stats.RxBytes >= receivedBefore+datagramSize
	})
	if shook := clientStatus.last(); shook.Since.IsZero() {
		t.Errorf("client status has no Since although it is up: %+v", shook)
	}

	for _, eng := range []*engine{client, server} {
		if err := eng.Stop(context.Background()); err != nil {
			t.Errorf("Stop: %v", err)
		}
	}
	requireGone(t, serverIface, clientIface)
	if got := clientNetwork.kinds(); got[len(got)-1] != "withdraw" {
		t.Errorf("client events = %q, want the owner withdrawn when Stop returns", got)
	}
}

// assertAdapterIdentity checks that the adapter has the GUID of its profile
// and that winiface finds it by name, which is how the Reconciler will.
func assertAdapterIdentity(t *testing.T, name, owner string) {
	t.Helper()
	link, err := winiface.FindByName(name)
	if err != nil {
		t.Fatalf("winiface cannot find %s: %v", name, err)
	}
	if link.Index == 0 || link.Name != name {
		t.Errorf("link = %+v, want the index and name of %s", link, name)
	}
	adapters, err := listWintunAdapters()
	if err != nil {
		t.Fatal(err)
	}
	want := adapterGUID(tunnel.OwnerID(owner))
	for _, adapter := range adapters {
		if adapter.name == name {
			if !strings.EqualFold(adapter.instanceID, want.String()) {
				t.Errorf("%s has the GUID %s, want %s", name, adapter.instanceID, want)
			}
			return
		}
	}
	t.Errorf("%s is not among the wintun adapters %+v", name, adapters)
}

func assertEndpointAnnounced(t *testing.T, network *recorder, iface string) {
	t.Helper()
	announces := network.announces()
	if len(announces) == 0 {
		t.Fatal("the client announced nothing")
	}
	for _, intent := range announces {
		if intent.Iface != iface {
			t.Errorf("announced interface %q, want %q", intent.Iface, iface)
		}
		if !slices.Contains(intent.Endpoints, netip.MustParseAddr("127.0.0.1")) {
			t.Errorf("announced endpoints %v, want 127.0.0.1 for the bypass route", intent.Endpoints)
		}
	}
}

// sendDatagram sends one UDP datagram from the client's tunnel address to the
// server's end of the tunnel, which only the client's adapter leads to.
func sendDatagram(t *testing.T) {
	t.Helper()
	conn, err := net.DialUDP("udp4",
		&net.UDPAddr{IP: net.ParseIP(clientAddress)},
		&net.UDPAddr{IP: net.ParseIP(serverFarEnd), Port: discardPort})
	if err != nil {
		t.Fatalf("dial through the tunnel: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write(make([]byte, datagramSize)); err != nil {
		t.Fatalf("send through the tunnel: %v", err)
	}
}

// leakAdapter creates an adapter and keeps it open, as a daemon that crashed
// after creating it would have. The returned device keeps the adapter alive
// until the test ends.
func leakAdapter(t *testing.T, name string) tun.Device {
	t.Helper()
	guid := adapterGUID(tunnel.OwnerID(name))
	device, err := tun.CreateTUNWithRequestedGUID(name, &guid, 1420)
	if err != nil {
		t.Fatalf("create the leftover adapter %s: %v", name, err)
	}
	t.Cleanup(func() { device.Close() })
	return device
}

func TestRootStaleAdaptersAreRemoved(t *testing.T) {
	requireElevated(t)
	requireWintun(t)
	removeScratchAdapters(t)

	left := staleAdapterPrefix + "left"
	other := rootAdapterPrefix + "not-stale"
	leakAdapter(t, left)
	leakAdapter(t, other)
	for _, name := range []string{left, other} {
		eventuallyWithin(t, adapterTimeout, "adapter "+name, func() bool { return interfaceListed(name) })
	}

	removed, err := removeStaleAdapters(staleAdapterPrefix)

	if err != nil || !slices.Equal(removed, []string{left}) {
		t.Fatalf("removeStaleAdapters = %q, %v; want only %q removed", removed, err, left)
	}
	requireGone(t, left)
	if !interfaceListed(other) {
		t.Errorf("%s was removed although its name does not start with %s", other, staleAdapterPrefix)
	}
}

// The engine removes the leftovers of its prefix before it creates its own
// adapter, once per process.
func TestRootEngineStartRemovesAdaptersLeftByACrash(t *testing.T) {
	requireElevated(t)
	requireWintun(t)
	checkLeaks(t)
	removeScratchAdapters(t)

	left := rootAdapterPrefix + "left-by-a-crash"
	leakAdapter(t, left)
	eventuallyWithin(t, adapterTimeout, "the leftover adapter", func() bool { return interfaceListed(left) })
	forgetCleanup := func() {
		cleanedPrefixes.Lock()
		defer cleanedPrefixes.Unlock()
		delete(cleanedPrefixes.done, rootAdapterPrefix)
	}
	forgetCleanup()
	t.Cleanup(forgetCleanup)

	key := newKeyPair(t)
	eng, status, _ := newWintunEngine(t, "root-after-crash",
		fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = %s/%d\n\n[Peer]\nPublicKey = %s\nAllowedIPs = %s/32\n",
			key.private, clientAddress, subnetBits, newKeyPair(t).public, serverFarEnd))
	if err := eng.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	eventuallyWithin(t, adapterTimeout, "the engine's adapter", func() bool { return status.last().Iface != "" })

	requireGone(t, left)
	if iface := status.last().Iface; !interfaceListed(iface) {
		t.Errorf("the engine's own adapter %s was removed with the leftovers", iface)
	}
	if err := eng.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestRootCrashChild is the daemon that crashes: it creates an adapter and
// ends without closing it. It runs only as the child of the test below.
func TestRootCrashChild(t *testing.T) {
	name := os.Getenv(crashChildEnv)
	if name == "" {
		t.Skip("runs only as the child of TestRootAdapterOfAKilledProcessIsGone")
	}
	requireWintun(t)
	leakAdapter(t, name)
	if err := windows.TerminateProcess(windows.CurrentProcess(), crashChildExitCode); err != nil {
		t.Fatal(err)
	}
}

// TestRootAdapterOfAKilledProcessIsGone ends a process that holds an adapter
// without a chance to close it, and shows what is left: wintun's software
// device is meant to go with the process, and the cleanup is the net under it.
func TestRootAdapterOfAKilledProcessIsGone(t *testing.T) {
	requireElevated(t)
	requireWintun(t)
	removeScratchAdapters(t)

	name := staleAdapterPrefix + "crashed"
	cmd := exec.Command(os.Args[0], "-test.run=^TestRootCrashChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), crashChildEnv+"="+name, rootTestsEnv+"=1")
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != crashChildExitCode {
		t.Fatalf("child: %v, want exit code %d after creating %s", err, crashChildExitCode, name)
	}

	time.Sleep(time.Second) // the system removes the device after the process is gone
	leftover := interfaceListed(name)
	t.Logf("adapter %s after the process was killed: left behind = %v", name, leftover)
	removed, err := removeStaleAdapters(staleAdapterPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if leftover && !slices.Equal(removed, []string{name}) {
		t.Errorf("the leftover adapter was not removed: removed %q", removed)
	}
	requireGone(t, name)
}
