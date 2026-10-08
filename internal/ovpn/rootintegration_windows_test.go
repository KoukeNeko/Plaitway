//go:build rootintegration && windows

package ovpn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
	"github.com/KoukeNeko/Plaitway/internal/winiface/rootguard"
)

// These tests make real TAP-Windows6 adapters and run the installed OpenVPN
// against a server of its own on 127.0.0.1. They are the only ones that need
// administrator rights, and they are written to be safe on a machine with an
// OpenVPN of its own:
//
//   - every adapter they make is named Plaitway-test-ovpn-<8 hex digits>, and is
//     removed before the test, after it, and when it fails; no other adapter is
//     named, so the adapters of the OpenVPN GUI and its services are never
//     touched, and openvpn is always given --dev-node;
//   - the tunnel uses 198.51.100.0/24, the route 203.0.113.0/24 and the DNS
//     server 192.0.2.53 (documentation networks, which no real network uses)
//     and the domain corp.invalid;
//   - the server pushes no redirect-gateway: if openvpn did apply what it is
//     told (the engine runs it so that it does not), the worst it could do is
//     put a route to a documentation network on a scratch adapter;
//   - the Network of the engine here is a recorder: the Reconciler is not
//     involved, and nothing writes a route or a DNS entry.
//
// Run from an elevated shell:
//
//	$env:PLAITWAY_ROOT_TESTS = '1'
//	go test -count=1 -tags rootintegration -run '^TestRootOpenVPN' -v ./internal/ovpn
//
// The first run may install the tap-windows6 device for the first adapter if
// the driver store has no copy of it for this machine; OpenVPN's own installer
// has put one there.
//
// The OpenVPN of the person whose PC this is must be disconnected while the
// tests run (see packaging/windows/openvpn/README.md): the TAP driver is shared,
// and a connection that is up has routes and DNS of its own. The tests refuse to
// run while an openvpn.exe that is not theirs is running, and say which; set
// PLAITWAY_ROOT_ALLOW_OWNER_OPENVPN=1 to run next to it anyway.
//
// The adapters are removed also when a test is killed (Ctrl+C, -timeout): a
// guard process (internal/winiface/rootguard, this test binary started again) is
// armed before the first adapter and removes every adapter named
// Plaitway-test-ovpn-* when this process ends without releasing it, or after
// ten minutes. Its log is the folder of temporary files, plaitway-rootguard-*.log, and it prints the command
// that does the same by hand.

const (
	scratchTunnelNetwork = "198.51.100.0"
	scratchTunnelAddress = "198.51.100.2"
	scratchRoute         = "203.0.113.0"
	scratchDNS           = "192.0.2.53"
	scratchDomain        = "corp.invalid"
)

func requireRootTests(t *testing.T) {
	t.Helper()
	if os.Getenv("PLAITWAY_ROOT_TESTS") != "1" {
		t.Skip("set PLAITWAY_ROOT_TESTS=1 and run from an elevated shell to make real adapters")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("the process is not elevated; tapctl makes adapters only for an administrator")
	}
}

func rootLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

const (
	recoveryTapAdapters = "tap-adapters"
	// rootGuardWindow is how long a root test may run before its guard removes
	// the adapters: the first adapter installs a driver, and the connection tests
	// wait for openvpn.
	rootGuardWindow = 10 * time.Minute
)

func init() { rootguard.Register(recoveryTapAdapters, removeScratchAdapters) }

// TestRootGuardHelper is the entry of the guards that these tests arm: the test
// binary started again (see internal/winiface/rootguard).
func TestRootGuardHelper(t *testing.T) { rootguard.RunHelper(t) }

// removeScratchAdapters removes every adapter this test file could have made,
// through the tapctl beside binary. It is the clean-up of the tests and the
// recovery of the guard.
func removeScratchAdapters(binary string) error {
	interfaces, err := net.Interfaces()
	if err != nil {
		return fmt.Errorf("list the interfaces: %w", err)
	}
	tool := tapctl{path: tapctlPath(binary), log: rootLogger()}
	var failures []error
	for _, name := range scratchAdapterNames(interfaceNamesOf(interfaces)) {
		ctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
		err := tool.remove(ctx, name)
		cancel()
		if err != nil {
			failures = append(failures, fmt.Errorf("remove the scratch adapter %s: %w", name, err))
		}
	}
	return errors.Join(failures...)
}

// manualRemoveCommand does by hand what removeScratchAdapters does.
func manualRemoveCommand(binary string) string {
	return fmt.Sprintf(`Get-NetAdapter -Name '%s*' | ForEach-Object { & '%s' delete $_.Name }`, scratchPrefix, tapctlPath(binary))
}

// rootSetup is the beginning of every root test: it checks that the test may
// run, arms the guard that removes the scratch adapters if the process is
// killed, removes what an earlier run left, and registers the removal for the
// end of the test. It returns the installed openvpn.
func rootSetup(t *testing.T) string {
	t.Helper()
	requireRootTests(t)
	binary := rootBinary(t)
	requireNoOwnerOpenVPN(t)
	rootguard.Arm(t, rootguard.Plan{Action: recoveryTapAdapters, Argument: binary, Within: rootGuardWindow, Manual: manualRemoveCommand(binary)})
	remove := func() {
		if err := removeScratchAdapters(binary); err != nil {
			t.Errorf("%v", err)
		}
	}
	remove()
	t.Cleanup(remove)
	return binary
}

func interfaceNamesOf(interfaces []net.Interface) []string {
	names := make([]string, len(interfaces))
	for i, iface := range interfaces {
		names[i] = iface.Name
	}
	return names
}

func scratchAdapterExists(name string) bool {
	_, err := net.InterfaceByName(name)
	return err == nil
}

// rootBinary is the installed OpenVPN, which must pass the daemon's own checks.
func rootBinary(t *testing.T) string {
	t.Helper()
	bin := installedOpenVPN(t)
	if err := VerifyBinary(bin, ""); err != nil {
		t.Fatalf("the installed openvpn would not be trusted by the daemon: %v", err)
	}
	return bin
}

func rootProfile(p *pki, port int) string {
	return fmt.Sprintf(`client
dev tun
proto tcp-client
remote 127.0.0.1 %d
nobind
keepalive 10 30
comp-lzo yes
auth-user-pass
auth-nocache
auth SHA1
cipher AES-128-CBC
data-ciphers AES-128-CBC
remote-cert-tls server
<ca>
%s</ca>
<cert>
%s</cert>
<key>
%s</key>
`, port, p.CA, p.ClientCert, p.ClientKey)
}

// outputOf runs a system tool and returns what it printed.
func outputOf(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

// TestRootOpenVPNConnectsOnItsOwnAdapter brings a tunnel up for real: the
// adapter is made, openvpn gets it by name and connects to a local server, the
// engine puts the address on it, and then nothing but that is on the machine:
// no route, no DNS server. It survives a Rebind on the same adapter, and the
// adapter is gone after Stop.
func TestRootOpenVPNConnectsOnItsOwnAdapter(t *testing.T) {
	bin := rootSetup(t)

	p := newPKI(t)
	srv := startLoopbackServer(t, bin, p, serverOpts{
		password: "root test",
		network:  scratchTunnelNetwork,
		pushes: []string{
			"route " + scratchRoute + " 255.255.255.0",
			"dhcp-option DNS " + scratchDNS,
			"dhcp-option DOMAIN " + scratchDomain,
		},
	})
	spec := tunnel.Spec{Owner: "root-test", Priority: 5}
	device := newAdapterDevice(adapterDeviceConfig{
		tool:   tapctl{path: tapctlPath(bin), log: rootLogger()},
		prefix: scratchPrefix,
		owner:  spec.Owner,
		log:    rootLogger(),
	})
	h := newHarness(t, harnessOpts{binary: bin, profile: rootProfile(p, srv.port), spec: spec, realTrust: true, device: device})
	routesBefore := outputOf(t, "route", "print", "-4")
	h.start()
	h.waitFor("a request for credentials", h.stateIs(tunnel.StateAwaitingCredentials))
	if !scratchAdapterExists(device.adapter) {
		t.Fatalf("the adapter %s was not made", device.adapter)
	}
	if err := h.eng.ProvideCredentials(tunnel.CredentialUserPassword, "alice", "root test"); err != nil {
		t.Fatal(err)
	}
	up := h.waitFor("Up", h.stateIs(tunnel.StateUp))

	if up.Iface != device.adapter || !strings.HasPrefix(up.Iface, scratchPrefix) {
		t.Fatalf("Iface = %q, want the scratch adapter %q", up.Iface, device.adapter)
	}
	requireInterfaceAddress(t, up.Iface, scratchTunnelAddress)

	var upIntent tunnel.Intent
	for _, c := range h.net.snapshot() {
		if c.intent.State == tunnel.StateUp {
			upIntent = c.intent
		}
	}
	if upIntent.Iface != up.Iface {
		t.Errorf("the Up intent names %q, the tunnel is %q", upIntent.Iface, up.Iface)
	}
	if want := mustPrefixes(scratchRoute + "/24"); fmt.Sprint(upIntent.Routes) != fmt.Sprint(want) {
		t.Errorf("the intent's routes = %v, want %v", upIntent.Routes, want)
	}
	wantDNS := []tunnel.DNSIntent{{Servers: addrs(scratchDNS), MatchDomains: []string{scratchDomain}}}
	if fmt.Sprint(upIntent.DNS) != fmt.Sprint(wantDNS) {
		t.Errorf("the intent's DNS = %+v, want %+v", upIntent.DNS, wantDNS)
	}

	// What openvpn was told it must not do, and whether it listened: the
	// pushed route and DNS server are not on the machine.
	if routes := outputOf(t, "route", "print", "-4"); strings.Contains(routes, scratchRoute) && !strings.Contains(routesBefore, scratchRoute) {
		t.Errorf("openvpn added the pushed route to the table:\n%s", routes)
	}
	if dns := outputOf(t, "netsh", "interface", "ip", "show", "dns", "name="+up.Iface); strings.Contains(dns, scratchDNS) {
		t.Errorf("openvpn set the pushed DNS server on the adapter:\n%s", dns)
	}
	if dns := outputOf(t, "netsh", "interface", "ip", "show", "dnsservers"); strings.Contains(dns, scratchDNS) {
		t.Errorf("a DNS server of the tunnel is on the machine:\n%s", dns)
	}

	// persist-tun: a restart of the connection keeps the adapter and its address.
	h.eng.Rebind()
	h.waitForNew("Reconnecting", h.stateIs(tunnel.StateReconnecting))
	again := h.waitForNew("Up again", h.stateIs(tunnel.StateUp))
	if again.Iface != up.Iface {
		t.Errorf("the interface changed across a rebind: %q then %q", up.Iface, again.Iface)
	}
	requireInterfaceAddress(t, up.Iface, scratchTunnelAddress)

	h.stop()
	h.requireProcessGone()
	h.requireWorkspaceGone()
	for deadline := time.Now().Add(15 * time.Second); scratchAdapterExists(up.Iface); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the adapter %s is still there after Stop", up.Iface)
		}
	}
	requireNoEngineGoroutines(t)
}

// TestRootOpenVPNKilledLeavesNoAdapter ends openvpn from outside, as a crash
// would: the engine fails, withdraws its owner, and still removes the adapter.
func TestRootOpenVPNKilledLeavesNoAdapter(t *testing.T) {
	bin := rootSetup(t)

	p := newPKI(t)
	srv := startLoopbackServer(t, bin, p, serverOpts{network: scratchTunnelNetwork})
	spec := tunnel.Spec{Owner: "root-killed"}
	device := newAdapterDevice(adapterDeviceConfig{
		tool:   tapctl{path: tapctlPath(bin), log: rootLogger()},
		prefix: scratchPrefix,
		owner:  spec.Owner,
		log:    rootLogger(),
	})
	profile := strings.Replace(rootProfile(p, srv.port), "auth-user-pass\n", "", 1)
	h := newHarness(t, harnessOpts{binary: bin, profile: profile, spec: spec, realTrust: true, device: device})
	h.start()
	h.waitFor("Up", h.stateIs(tunnel.StateUp))

	c := h.eng.child.Load()
	if c == nil {
		t.Fatal("no child")
	}
	c.kill()
	h.waitFor("Failed", h.stateIs(tunnel.StateFailed))
	<-h.collect
	if calls := h.net.snapshot(); !calls[len(calls)-1].withdraw {
		t.Errorf("the owner was not withdrawn after the kill: %+v", calls)
	}
	for deadline := time.Now().Add(15 * time.Second); scratchAdapterExists(device.adapter); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the adapter %s is still there after the engine failed", device.adapter)
		}
	}
	h.requireProcessGone()
}

// TestRootAdapterLifecycle is the adapter alone, without openvpn: made, known
// to the IP stack by name, and removed. It shows what tapctl does on this machine
// before openvpn is involved. The address is not set here: the system holds the
// address of an adapter nobody has opened as tentative, and the engine sets it
// only once openvpn has opened the adapter.
func TestRootAdapterLifecycle(t *testing.T) {
	bin := rootSetup(t)

	device := newAdapterDevice(adapterDeviceConfig{
		tool:   tapctl{path: tapctlPath(bin), log: rootLogger()},
		prefix: scratchPrefix,
		owner:  "root-lifecycle",
		log:    rootLogger(),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := device.acquire(ctx); err != nil {
		t.Fatal(err)
	}
	if !scratchAdapterExists(device.adapter) {
		t.Fatalf("the adapter %s is not there", device.adapter)
	}
	device.release()
	for deadline := time.Now().Add(15 * time.Second); scratchAdapterExists(device.adapter); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the adapter %s is still there after release", device.adapter)
		}
	}
}

// TestRootStaleAdaptersAreRemoved makes two scratch adapters and starts a
// device for a third: the first start of its kind in the process removes the
// one that looks like a leftover of the engine, and only that one.
func TestRootStaleAdaptersAreRemoved(t *testing.T) {
	bin := rootSetup(t)

	tool := tapctl{path: tapctlPath(bin), log: rootLogger()}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	left := adapterName(scratchPrefix, "left-by-a-crash")
	kept := scratchPrefix + "not-ours" // does not end in eight hex digits
	for _, name := range []string{left, kept} {
		if err := tool.create(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	sweptPrefixes.Delete(scratchPrefix) // this process has not swept it yet

	device := newAdapterDevice(adapterDeviceConfig{tool: tool, prefix: scratchPrefix, owner: "root-stale", log: rootLogger()})
	if err := device.acquire(ctx); err != nil {
		t.Fatal(err)
	}
	defer device.release()
	if scratchAdapterExists(left) {
		t.Errorf("the leftover %s was not removed", left)
	}
	if !scratchAdapterExists(kept) {
		t.Errorf("%s does not look like the engine's, and was removed", kept)
	}
	if !scratchAdapterExists(device.adapter) {
		t.Errorf("the device's own adapter %s is not there", device.adapter)
	}
}
