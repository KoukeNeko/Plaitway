//go:build rootintegration && linux

package wg

// These tests create real tun devices, let the Reconciler change the routing
// table of the network namespace they run in, and talk to the kernel's own
// WireGuard in a second namespace. They refuse to run anywhere but in a private
// user and network namespace, which needs no privileges:
//
//	go test -c -tags rootintegration -o wg.test ./internal/wg
//	unshare -Urnm sh -c 'mount -t tmpfs none /run; ip link set lo up;
//	    PLAITWAY_ROOT_TESTS=1 ./wg.test -test.v -test.run TestRoot'
//
// The tmpfs on /run is where ip-netns(8) keeps the peer's namespace, and the
// mount namespace (-m) lets two tests hide /dev/net/tun and replace /etc/hosts for
// themselves alone. ip(8), ping(8) and wg(8) set up and check what the engine did.
// The lab looks like this:
//
//	this namespace                              namespace wgpeer
//	veth0 10.0.1.1/24, 2001:db8:1::1/64  <--->  veth1 10.0.1.2/24, 2001:db8:1::2/64
//	veth2 10.0.2.1/24, 2001:db8:2::1/64  <--->  veth3 10.0.2.2/24, 2001:db8:2::2/64
//	default routes through veth0 (metric 100)   lo: 198.51.100.1 and 2001:db8:e::1, the
//	and veth2 (200), the second as standby          endpoints, 192.0.2.99 and 2001:db8:99::99
//	plaitway0 (the engine under test)           wg0: kernel WireGuard 10.200.0.1/24, fd00:200::1/64
//
// The endpoints are on the peer's loopback so that they are off-link: the engine
// reaches them only through a default route, as a full tunnel must.

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/tun"

	"github.com/KoukeNeko/Plaitway/internal/osnet/fake"
	"github.com/KoukeNeko/Plaitway/internal/osnet/linux"
	"github.com/KoukeNeko/Plaitway/internal/reconciler"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

const (
	peerNS       = "wgpeer"
	peerPort     = 51820
	endpointAddr = "198.51.100.1"
	endpoint6    = "2001:db8:e::1"
	farAddr      = "192.0.2.99"
	farAddr6     = "2001:db8:99::99"
	peerTunnel6  = "fd00:200::1"
	client6      = "fd00:200::2"

	// The metrics of the Reconciler: its routes for a tunnel, and the host routes
	// that keep the endpoints reachable around it.
	tunnelMetric = 5
	bypassMetric = 1
)

var ifaceNamePattern = regexp.MustCompile(`^plaitway[0-9]+$`)

// fallbackDevices are the devices that a tunnel module makes in every network
// namespace when it is loaded, and that cannot be deleted.
var fallbackDevices = []string{"tunl0", "sit0", "gre0", "gretap0", "erspan0", "ip6tnl0", "ip6gre0", "ip_vti0", "ip6_vti0"}

func nonLoopbackInterfaces(t *testing.T) []string {
	t.Helper()
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback == 0 && !slices.Contains(fallbackDevices, iface.Name) {
			names = append(names, iface.Name)
		}
	}
	return names
}

// requireNetns skips unless it was asked to run, and refuses to go on anywhere
// but in a user namespace of its own, with a network namespace that has nothing
// in it but loopback. The machine's own routes and interfaces are never touched.
func requireNetns(t *testing.T) {
	t.Helper()
	if os.Getenv("PLAITWAY_ROOT_TESTS") != "1" || os.Geteuid() != 0 {
		t.Skip("needs PLAITWAY_ROOT_TESTS=1 and effective uid 0, in a private user and network namespace")
	}
	uidMap, err := os.ReadFile("/proc/self/uid_map")
	if err != nil {
		t.Fatalf("refusing to run: cannot tell whether this is a user namespace: %v", err)
	}
	// The machine's own user namespace maps every id to itself.
	if strings.Join(strings.Fields(string(uidMap)), " ") == "0 0 4294967295" {
		t.Fatal("refusing to run: this is the machine's own user namespace; start the test with unshare -Urnm")
	}
	if names := nonLoopbackInterfaces(t); len(names) != 0 {
		t.Fatalf("refusing to run: the namespace has interfaces besides lo (%v); this is not a private network namespace", names)
	}
	t.Cleanup(func() {
		// The veth pairs go with the peer's namespace, which the kernel deletes
		// in the background.
		var left []string
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if left = nonLoopbackInterfaces(t); len(left) == 0 {
				break
			}
		}
		if len(left) != 0 {
			t.Errorf("interfaces left behind: %v", left)
		}
		if left := ownRoutes(t); len(left) != 0 {
			t.Errorf("routes left behind: %v", left)
		}
	})
}

// cmd runs a program and fails the test when it fails.
func cmd(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v: %s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

func ip(t *testing.T, args ...string) string { return cmd(t, "ip", args...) }

// setSysctl writes a sysctl of this network namespace and puts the old value
// back when the test ends.
func setSysctl(t *testing.T, name, value string) {
	t.Helper()
	path := "/proc/sys/net/" + name
	old, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.WriteFile(path, old, 0) })
}

// tunnelRoutes are the routes the Reconciler installs through iface for dsts.
func tunnelRoutes(iface string, dsts ...string) []string {
	var routes []string
	for _, dst := range dsts {
		routes = append(routes, fmt.Sprintf("%s dev %s metric %d", dst, iface, tunnelMetric))
	}
	return routes
}

// bypassRoute is the host route that keeps an endpoint reachable around the tunnel.
func bypassRoute(endpoint, gateway, iface string) string {
	return fmt.Sprintf("%s via %s dev %s metric %d", endpoint, gateway, iface, bypassMetric)
}

// peerCmd runs a program in the peer's namespace.
func peerCmd(t *testing.T, args ...string) string {
	t.Helper()
	return cmd(t, "ip", append([]string{"netns", "exec", peerNS}, args...)...)
}

func requireTools(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("needs %s: %v", tool, err)
		}
	}
}

// ownRoutes lists the routes Plaitway installed (protocol 199) as ip route
// shows them, sorted.
func ownRoutes(t *testing.T) []string {
	t.Helper()
	routes, err := linux.NewRouteTable().Dump()
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, rt := range routes {
		if rt.Flags>>8&0xff != linux.RouteProtocol {
			continue
		}
		line := rt.Dst.String()
		if rt.Gateway.IsValid() {
			line += " via " + rt.Gateway.WithZone("").String()
		}
		lines = append(lines, fmt.Sprintf("%s dev %s metric %d", line, rt.Iface, rt.Metric))
	}
	slices.Sort(lines)
	return lines
}

func checkRoutes(t *testing.T, want ...string) {
	t.Helper()
	slices.Sort(want)
	if got := ownRoutes(t); !slices.Equal(got, want) {
		t.Errorf("routes of Plaitway:\n got %q\nwant %q\n\nip route:\n%s\nip -6 route:\n%s", got, want, ip(t, "route"), ip(t, "-6", "route"))
	}
}

// lockedBuffer is a log destination that tests read while the code under test
// still writes.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// lab is the network of the diagram above, with the real Reconciler on this
// namespace's routing table. The engines are the daemon's tunnel.Engine: the
// Reconciler is their Network and calls their Rebind, as the Manager does.
type lab struct {
	t            *testing.T
	peer, client keyPair
	rec          *reconciler.Reconciler

	mu      sync.Mutex
	engines []*engine
}

func newLab(t *testing.T) *lab {
	t.Helper()
	// The cleanups run last to first: the check comes after everything stopped.
	checkLeaks(t)
	requireNetns(t)
	requireTools(t, "ip", "ping", "wg")
	l := &lab{t: t, peer: newKeyPair(t), client: newKeyPair(t)}

	// Strict reverse path filtering, as many distributions have it: a packet
	// that arrives on an interface other than the one the reply would leave by
	// is dropped.
	setSysctl(t, "ipv4/conf/all/rp_filter", "1")
	setSysctl(t, "ipv4/conf/default/rp_filter", "1")
	if out, err := exec.Command("ip", "netns", "add", peerNS).CombinedOutput(); err != nil {
		t.Fatalf("ip netns add: %v: %s\nstart the test with unshare -Urnm and mount a tmpfs on /run", err, out)
	}
	t.Cleanup(func() {
		// Deleting the namespace takes its end of the veth pairs, and so ours, with it.
		if out, err := exec.Command("ip", "netns", "del", peerNS).CombinedOutput(); err != nil {
			t.Errorf("ip netns del: %v: %s", err, out)
		}
	})
	ip(t, "-n", peerNS, "link", "set", "lo", "up")
	for _, link := range []struct{ here, there, hereV4, thereV4, hereV6, thereV6 string }{
		{"veth0", "veth1", "10.0.1.1/24", "10.0.1.2/24", "2001:db8:1::1/64", "2001:db8:1::2/64"},
		{"veth2", "veth3", "10.0.2.1/24", "10.0.2.2/24", "2001:db8:2::1/64", "2001:db8:2::2/64"},
	} {
		ip(t, "link", "add", link.here, "type", "veth", "peer", "name", link.there, "netns", peerNS)
		for _, side := range []struct {
			ns     []string
			name   string
			v4, v6 string
		}{{nil, link.here, link.hereV4, link.hereV6}, {[]string{"-n", peerNS}, link.there, link.thereV4, link.thereV6}} {
			ip(t, append(side.ns, "link", "set", side.name, "up")...)
			ip(t, append(side.ns, "addr", "add", side.v4, "dev", side.name)...)
			ip(t, append(side.ns, "addr", "add", side.v6, "dev", side.name, "nodad")...)
		}
	}
	ip(t, "route", "add", "default", "via", "10.0.1.2", "dev", "veth0", "metric", "100")
	ip(t, "route", "add", "default", "via", "10.0.2.2", "dev", "veth2", "metric", "200")
	ip(t, "-6", "route", "add", "default", "via", "2001:db8:1::2", "dev", "veth0", "metric", "100")
	ip(t, "-6", "route", "add", "default", "via", "2001:db8:2::2", "dev", "veth2", "metric", "200")

	for _, addr := range []string{endpointAddr + "/32", farAddr + "/32", endpoint6 + "/128", farAddr6 + "/128"} {
		ip(t, "-n", peerNS, "addr", "add", addr, "dev", "lo")
	}
	l.setUpPeerWireGuard()
	l.rec = startReconciler(t, func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		for _, eng := range l.engines {
			eng.Rebind()
		}
	})
	t.Cleanup(func() {
		if t.Failed() {
			for _, args := range [][]string{{"addr"}, {"route"}, {"-6", "route"}} {
				out, _ := exec.Command("ip", args...).CombinedOutput()
				t.Logf("ip %s:\n%s", strings.Join(args, " "), out)
			}
			out, _ := exec.Command("ip", "netns", "exec", peerNS, "wg", "show").CombinedOutput()
			t.Logf("peer wg show:\n%s", out)
		}
	})
	return l
}

// setUpPeerWireGuard makes the kernel's WireGuard in the peer namespace, with
// the client as its only peer.
func (l *lab) setUpPeerWireGuard() {
	t := l.t
	ip(t, "-n", peerNS, "link", "add", "wg0", "type", "wireguard")
	conf := fmt.Sprintf("[Interface]\nPrivateKey = %s\nListenPort = %d\n\n[Peer]\nPublicKey = %s\nAllowedIPs = %s/32, %s/128\n",
		l.peer.private, peerPort, l.client.public, clientTunnel, client6)
	path := t.TempDir() + "/peer.conf"
	if err := os.WriteFile(path, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	peerCmd(t, "wg", "setconf", "wg0", path)
	ip(t, "-n", peerNS, "addr", "add", serverTunnel+"/24", "dev", "wg0")
	ip(t, "-n", peerNS, "addr", "add", peerTunnel6+"/64", "dev", "wg0", "nodad")
	ip(t, "-n", peerNS, "link", "set", "wg0", "up")
}

// startReconciler runs the real Reconciler on this namespace's routing table,
// with rebind as what the Manager does after the routes of a changed network are
// repaired. It stops, and must find nothing wrong, when the test ends.
func startReconciler(t *testing.T, rebind func()) *reconciler.Reconciler {
	t.Helper()
	var logs lockedBuffer
	rec, err := reconciler.New(reconciler.Config{
		Routes:      linux.NewRouteTable(),
		DNS:         fake.NewDNS(),
		Net:         linux.NewNetMonitor(linux.NetMonitorOptions{Logger: slog.New(slog.NewTextHandler(&logs, nil))}),
		JournalPath: t.TempDir() + "/journal",
		Keying:      reconciler.KeyLinux,
		Log:         slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatal(err)
	}
	if rebind != nil {
		rec.SetRebind(rebind)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rec.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Reconciler.Run: %v", err)
		}
		if t.Failed() {
			t.Logf("reconciler log:\n%s", &logs)
		}
	})
	return rec
}

type profileOpts struct {
	addresses  string // default: both families
	allowedIPs string
	endpoint   string // default: the IPv4 endpoint
	extra      string // more lines for [Interface]
	keepalive  int
}

func (l *lab) profile(o profileOpts) string {
	if o.addresses == "" {
		o.addresses = clientTunnel + "/24, " + client6 + "/64"
	}
	if o.endpoint == "" {
		o.endpoint = fmt.Sprintf("%s:%d", endpointAddr, peerPort)
	}
	return fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = %s\n%s\n\n[Peer]\nPublicKey = %s\nEndpoint = %s\nAllowedIPs = %s\nPersistentKeepalive = %d\n",
		l.client.private, o.addresses, o.extra, l.peer.public, o.endpoint, o.allowedIPs, o.keepalive)
}

// rootEngine is an engine with the log and the status it produced.
type rootEngine struct {
	*engine
	t      *testing.T
	logs   *logSink
	status *statusLog
}

// engine creates an engine of the real platform for content, in the lab; it is
// stopped, and must have left nothing behind, when the test ends.
func (l *lab) engine(name, content string, mode tunnel.Mode) *rootEngine {
	t := l.t
	t.Helper()
	cfg := Config{pollInterval: testPoll, stallGrace: 3 * time.Second}
	logs := &logSink{}
	spec := tunnel.Spec{Owner: tunnel.OwnerID(name), Name: name, Content: []byte(content), Mode: mode, Priority: 7}
	eng, err := newEngine(cfg.withDefaults(), spec, tunnel.Deps{Network: l.rec, Net: fakeMonitor{}, Log: logs.log})
	if err != nil {
		t.Fatalf("newEngine(%s): %v", name, err)
	}
	l.mu.Lock()
	l.engines = append(l.engines, eng)
	l.mu.Unlock()
	re := &rootEngine{engine: eng, t: t, logs: logs, status: collectStatus(eng.Status())}
	t.Cleanup(func() {
		if err := eng.Stop(context.Background()); err != nil {
			t.Errorf("Stop(%s): %v", name, err)
		}
		if t.Failed() {
			logs.mu.Lock()
			defer logs.mu.Unlock()
			t.Logf("%s log:\n%s", name, strings.Join(logs.lines, "\n"))
		}
	})
	return re
}

func (e *rootEngine) start() {
	e.t.Helper()
	if err := e.Start(context.Background()); err != nil {
		e.t.Fatalf("Start: %v", err)
	}
}

// up waits until the engine reports Up and returns its interface.
func (e *rootEngine) up(t *testing.T) string {
	t.Helper()
	eventually(t, "the tunnel to come up", func() bool {
		s := e.status.last()
		if s.State == tunnel.StateFailed {
			t.Fatalf("the tunnel failed: %s", s.Err)
		}
		return s.State == tunnel.StateUp
	})
	return e.status.last().Iface
}

// seenIface is the interface that any snapshot of the status named.
func (l *statusLog) seenIface() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.all {
		if s.Iface != "" {
			return s.Iface
		}
	}
	return ""
}

func ifaceMissing(name string) bool {
	_, err := net.InterfaceByName(name)
	return err != nil
}

// pingOK sends pings from this namespace to dst, which has to answer.
func pingOK(t *testing.T, dst string) {
	t.Helper()
	args := []string{"-c", "2", "-W", "3", "-i", "0.2", dst}
	if strings.Contains(dst, ":") {
		args = append([]string{"-6"}, args...)
	}
	if out, err := exec.Command("ping", args...).CombinedOutput(); err != nil {
		t.Fatalf("ping %s: %v\n%s\nip route get: %s", dst, err, out, routeGet(t, dst))
	}
}

// routeGet is the route the kernel picks for dst, as one line.
func routeGet(t *testing.T, dst string) string {
	t.Helper()
	args := []string{"route", "get", dst}
	if strings.Contains(dst, ":") {
		args = append([]string{"-6"}, args...)
	}
	out, err := exec.Command("ip", args...).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("%v: %s", err, out)
	}
	line, _, _ := strings.Cut(string(out), "\n")
	return line
}

// peerEndpoint is the address the kernel's WireGuard last heard the client from.
func peerEndpoint(t *testing.T, clientPublic string) string {
	t.Helper()
	for line := range strings.Lines(peerCmd(t, "wg", "show", "wg0", "endpoints")) {
		if key, endpoint, ok := strings.Cut(strings.TrimSpace(line), "\t"); ok && key == clientPublic {
			return endpoint
		}
	}
	return ""
}

func TestRootEngineLifecycleAgainstTheKernelPeer(t *testing.T) {
	l := newLab(t)
	eng := l.engine("lifecycle", l.profile(profileOpts{
		allowedIPs: "10.200.0.0/24, " + "fd00:200::/64, 192.0.2.0/24, 2001:db8:99::/64",
		extra:      "MTU = 1380",
		keepalive:  1,
	}), tunnel.ModeAuto)
	eng.start()

	iface := eng.up(t)
	if !ifaceNamePattern.MatchString(iface) {
		t.Errorf("interface %q, want a name the kernel completed from plaitway%%d", iface)
	}
	if want := []tunnel.State{tunnel.StateConnecting, tunnel.StateUp}; !slices.Equal(eng.status.states(), want) {
		t.Errorf("states = %v, want %v", eng.status.states(), want)
	}

	netIface, err := net.InterfaceByName(iface)
	if err != nil {
		t.Fatal(err)
	}
	if netIface.MTU != 1380 || netIface.Flags&net.FlagUp == 0 {
		t.Errorf("%s: mtu %d, flags %v; want mtu 1380 and up", iface, netIface.MTU, netIface.Flags)
	}
	addrs, _ := netIface.Addrs()
	var got []string
	for _, a := range addrs {
		// The kernel gives the interface a link-local IPv6 address when it comes up.
		if ipNet, ok := a.(*net.IPNet); ok && !ipNet.IP.IsLinkLocalUnicast() {
			got = append(got, a.String())
		}
	}
	slices.Sort(got)
	if want := []string{clientTunnel + "/24", client6 + "/64"}; !slices.Equal(got, want) {
		t.Errorf("%s has addresses %v, want %v", iface, got, want)
	}
	// The tun device takes no duplicate address detection: the address is usable
	// at once, and does not expire.
	if out := ip(t, "-6", "addr", "show", "dev", iface); strings.Contains(out, "tentative") || !strings.Contains(out, "preferred_lft forever") {
		t.Errorf("the IPv6 address is not ready for use:\n%s", out)
	}

	// The kernel's connected routes of the addresses overlap the AllowedIPs
	// routes; ours are beside them, and both families carry traffic.
	checkRoutes(t, tunnelRoutes(iface, "10.200.0.0/24", "192.0.2.0/24", "fd00:200::/64", "2001:db8:99::/64")...)
	for _, rr := range l.rec.Report().Routes {
		if rr.State != tunnel.RouteInstalled {
			t.Errorf("route %s is %v (%s), want installed", rr.Prefix, rr.State, rr.Detail)
		}
	}
	for _, dst := range []string{serverTunnel, farAddr, peerTunnel6, farAddr6} {
		pingOK(t, dst)
	}
	eventually(t, "the counters to rise", func() bool {
		s := eng.status.last()
		return s.Stats.RxBytes > 0 && s.Stats.TxBytes > 0
	})
	if got := peerEndpoint(t, l.client.public); !strings.HasPrefix(got, "10.0.1.1:") {
		t.Errorf("the peer heard the client from %q, want 10.0.1.1 (the address of veth0)", got)
	}

	if err := eng.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Stop promises that the interface is gone and the routes with it.
	if !ifaceMissing(iface) {
		t.Errorf("%s is still there after Stop", iface)
	}
	checkRoutes(t)
	// The status channel keeps only the latest snapshot for a slow reader, so the
	// states in between may be missing; the first two and the end are not.
	select {
	case <-eng.status.done:
	case <-time.After(waitTimeout):
		t.Fatal("the status channel was not closed")
	}
	if states := eng.status.states(); len(states) < 3 || !slices.Equal(states[:2], []tunnel.State{tunnel.StateConnecting, tunnel.StateUp}) || states[len(states)-1] != tunnel.StateDisconnected {
		t.Errorf("states = %v, want Connecting, Up, ... and Disconnected at the end", states)
	}
}

// fullTunnelRoutes is what a profile that routes everything installs: the halves
// of both default routes, and the route that keeps the endpoint outside.
func fullTunnelRoutes(iface, bypass string) []string {
	return append(tunnelRoutes(iface, "0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1"), bypass)
}

// viaVeth0 is the bypass route to the IPv4 endpoint while the default route leads
// through veth0.
var viaVeth0 = bypassRoute(endpointAddr+"/32", "10.0.1.2", "veth0")

func TestRootFullTunnelInstallsTheHalvesAndTheEndpointBypass(t *testing.T) {
	l := newLab(t)
	eng := l.engine("full", l.profile(profileOpts{allowedIPs: "0.0.0.0/0, ::/0", keepalive: 1}), tunnel.ModeAuto)
	eng.start()
	iface := eng.up(t)

	checkRoutes(t, fullTunnelRoutes(iface, viaVeth0)...)
	// The handshake and the data both ran: the endpoint is reached around the
	// tunnel, everything else through it.
	if got := routeGet(t, endpointAddr); !strings.Contains(got, "dev veth0") {
		t.Errorf("route to the endpoint: %s, want it through veth0", got)
	}
	for _, dst := range []string{"8.8.8.8", "203.0.113.9", "2001:4860:4860::8888"} {
		if got := routeGet(t, dst); !strings.Contains(got, "dev "+iface) {
			t.Errorf("route to %s: %s, want it through %s", dst, got, iface)
		}
	}
	for _, dst := range []string{serverTunnel, farAddr, peerTunnel6, farAddr6} {
		pingOK(t, dst)
	}

	if err := eng.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	checkRoutes(t)
	if got := routeGet(t, "8.8.8.8"); !strings.Contains(got, "dev veth0") {
		t.Errorf("route to 8.8.8.8 after Stop: %s, want the system's default again", got)
	}
}

func TestRootSplitModeKeepsTheSystemRoutesOfAFullProfile(t *testing.T) {
	l := newLab(t)
	// The profile redirects everything; the user's mode keeps only what it names
	// besides the default, which here is the tunnel's own network.
	eng := l.engine("split", l.profile(profileOpts{allowedIPs: "0.0.0.0/0, ::/0, 10.200.0.0/24", keepalive: 1}), tunnel.ModeSplit)
	eng.start()
	iface := eng.up(t)
	checkRoutes(t, tunnelRoutes(iface, "10.200.0.0/24")...)
	if got := routeGet(t, "8.8.8.8"); !strings.Contains(got, "dev veth0") {
		t.Errorf("route to 8.8.8.8: %s, want the system's default", got)
	}
	pingOK(t, serverTunnel)
}

func TestRootRebindFollowsTheEndpointRoute(t *testing.T) {
	l := newLab(t)
	eng := l.engine("rebind", l.profile(profileOpts{allowedIPs: "0.0.0.0/0, ::/0", keepalive: 1}), tunnel.ModeAuto)
	eng.start()
	iface := eng.up(t)
	pingOK(t, serverTunnel)
	if got := peerEndpoint(t, l.client.public); !strings.HasPrefix(got, "10.0.1.1:") {
		t.Fatalf("the peer heard the client from %q, want 10.0.1.1", got)
	}

	// The physical network changes: the default route through veth0 goes, the
	// standby through veth2 stays. veth0 still has its address and the other end
	// still answers, so a socket that keeps its source address and interface
	// keeps sending through the old way, which the peer would see.
	ip(t, "route", "del", "default", "dev", "veth0")
	ip(t, "-6", "route", "del", "default", "dev", "veth0")
	viaVeth2 := bypassRoute(endpointAddr+"/32", "10.0.2.2", "veth2")
	eventually(t, "the Reconciler to move the endpoint route", func() bool { return slices.Contains(ownRoutes(t), viaVeth2) })
	checkRoutes(t, fullTunnelRoutes(iface, viaVeth2)...)

	eventually(t, "the peer to hear the client from veth2", func() bool {
		return strings.HasPrefix(peerEndpoint(t, l.client.public), "10.0.2.1:")
	})
	eventually(t, "the tunnel to be up again", func() bool { return eng.status.last().State == tunnel.StateUp })
	pingOK(t, serverTunnel)
	pingOK(t, farAddr6)
	if !eng.logs.has("network changed, rebinding") {
		t.Error("the engine did not rebind")
	}
}

// The endpoint of a profile is usually a name, which the engine resolves with the
// system's resolver before there is a tunnel, and the Reconciler keeps that
// address outside it.
func TestRootEndpointNameIsResolvedBySystem(t *testing.T) {
	l := newLab(t)
	// A hosts file of our own in this mount namespace, which the test runs in alone.
	hosts := t.TempDir() + "/hosts"
	if err := os.WriteFile(hosts, []byte(endpointAddr+" vpn.plaitway.test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("mount", "--bind", hosts, "/etc/hosts").CombinedOutput(); err != nil {
		t.Skipf("cannot replace /etc/hosts (start the test with unshare -m): %v: %s", err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command("umount", "/etc/hosts").CombinedOutput(); err != nil {
			t.Errorf("umount /etc/hosts: %v: %s", err, out)
		}
	})

	eng := l.engine("named", l.profile(profileOpts{allowedIPs: "0.0.0.0/0, ::/0", endpoint: fmt.Sprintf("vpn.plaitway.test:%d", peerPort), keepalive: 1}), tunnel.ModeAuto)
	eng.start()
	iface := eng.up(t)
	checkRoutes(t, fullTunnelRoutes(iface, viaVeth0)...)
	pingOK(t, serverTunnel)
}

// Opening the sockets again after a change of the network can fail while the
// daemon forks other programs: a child holds the old sockets, and their port,
// until it executes. The tunnel has to get its sockets back by itself.
func TestRootRebindWhileProcessesAreForked(t *testing.T) {
	l := newLab(t)
	eng := l.engine("forked", l.profile(profileOpts{allowedIPs: "10.200.0.0/24", keepalive: 1}), tunnel.ModeAuto)
	eng.start()
	eng.up(t)

	stop := make(chan struct{})
	forking := make(chan struct{})
	go func() {
		defer close(forking)
		for {
			select {
			case <-stop:
				return
			default:
				exec.Command("true").Run()
			}
		}
	}()
	// The kernel's WireGuard ignores handshakes less than 20 ms apart as a flood.
	for range 60 {
		eng.Rebind()
		time.Sleep(40 * time.Millisecond)
	}
	close(stop)
	<-forking

	if eng.logs.has("rebind sockets") {
		eventually(t, "the sockets to be open again", func() bool { return eng.logs.has("UDP sockets are open again") })
	}
	eventually(t, "the tunnel to be up", func() bool { return eng.status.last().State == tunnel.StateUp })
	pingOK(t, serverTunnel)
}

// FwMark is how wg-quick keeps the tunnel's own packets out of the tunnel when
// the user does the routing with policy rules; the engine only has to put it on
// its sockets.
func TestRootFwMarkIsSetOnTheSockets(t *testing.T) {
	l := newLab(t)
	ip(t, "rule", "add", "fwmark", "1", "lookup", "100")
	t.Cleanup(func() { exec.Command("ip", "rule", "del", "fwmark", "1", "lookup", "100").Run() })
	// The main table leads to the endpoint through veth0; marked packets go through veth2.
	ip(t, "route", "add", endpointAddr+"/32", "via", "10.0.2.2", "dev", "veth2", "table", "100")
	// The answers come back on veth2, which the main table does not lead to: strict
	// reverse path filtering would drop them, and the user has to relax it as well.
	setSysctl(t, "ipv4/conf/all/rp_filter", "2")
	setSysctl(t, "ipv4/conf/veth2/rp_filter", "2")

	eng := l.engine("fwmark", l.profile(profileOpts{allowedIPs: "10.200.0.0/24", extra: "FwMark = 1", keepalive: 1}), tunnel.ModeAuto)
	eng.start()
	eng.up(t)
	if got := peerEndpoint(t, l.client.public); !strings.HasPrefix(got, "10.0.2.1:") {
		t.Errorf("the peer heard the client from %q, want 10.0.2.1: the packets were not marked", got)
	}
	pingOK(t, serverTunnel)
}

func TestRootStopAtAnyPointOfTheSetupLeavesNothingBehind(t *testing.T) {
	l := newLab(t)
	profile := l.profile(profileOpts{allowedIPs: "0.0.0.0/0, ::/0", keepalive: 1})
	for i, delay := range []time.Duration{0, time.Millisecond, 3 * time.Millisecond, 10 * time.Millisecond, 30 * time.Millisecond, 100 * time.Millisecond, 300 * time.Millisecond} {
		eng := l.engine(fmt.Sprintf("stop%d", i), profile, tunnel.ModeAuto)
		eng.start()
		time.Sleep(delay)
		if err := eng.Stop(context.Background()); err != nil {
			t.Fatalf("Stop after %v: %v", delay, err)
		}
		if iface := eng.status.seenIface(); iface != "" && !ifaceMissing(iface) {
			t.Fatalf("Stop after %v returned while %s was still there", delay, iface)
		}
		if names := nonLoopbackInterfaces(t); slices.ContainsFunc(names, ifaceNamePattern.MatchString) {
			t.Fatalf("Stop after %v left %v", delay, names)
		}
		checkRoutes(t)
		if t.Failed() {
			return
		}
	}
}

func TestRootAListenPortInUseFailsTheTunnel(t *testing.T) {
	l := newLab(t)
	taken, err := net.ListenPacket("udp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	port := taken.LocalAddr().(*net.UDPAddr).Port

	eng := l.engine("port", l.profile(profileOpts{allowedIPs: "10.200.0.0/24", extra: fmt.Sprintf("ListenPort = %d", port)}), tunnel.ModeAuto)
	eng.start()
	eventually(t, "the tunnel to fail", func() bool { return eng.status.last().State == tunnel.StateFailed })
	if err := eng.status.last().Err; !strings.Contains(err, "port") && !strings.Contains(err, "address already in use") {
		t.Errorf("Err = %q, want it to name the port", err)
	}
	if err := eng.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	checkRoutes(t)
}

func TestRootAFixedListenPortIsUsedAndFreedAtStop(t *testing.T) {
	l := newLab(t)
	port := freeUDPPort(t)
	profile := l.profile(profileOpts{allowedIPs: "10.200.0.0/24", extra: fmt.Sprintf("ListenPort = %d", port), keepalive: 1})
	// The next start finds the port free again at once, as a reconnect does.
	for i := range 2 {
		eng := l.engine(fmt.Sprintf("port%d", i), profile, tunnel.ModeAuto)
		eng.start()
		eng.up(t)
		if got := peerEndpoint(t, l.client.public); !strings.HasSuffix(got, fmt.Sprintf(":%d", port)) {
			t.Errorf("the peer heard the client from %q, want port %d", got, port)
		}
		if err := eng.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRootANameTakenByAnotherDeviceIsSkipped(t *testing.T) {
	l := newLab(t)
	// A persistent device, as a crashed program of another kind could leave.
	ip(t, "tuntap", "add", "dev", "plaitway0", "mode", "tun")
	t.Cleanup(func() { exec.Command("ip", "link", "del", "plaitway0").Run() })

	eng := l.engine("taken", l.profile(profileOpts{allowedIPs: "10.200.0.0/24", keepalive: 1}), tunnel.ModeAuto)
	eng.start()
	if iface := eng.up(t); iface != "plaitway1" {
		t.Errorf("interface %q, want plaitway1 beside the existing plaitway0", iface)
	}
	if err := eng.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ifaceMissing("plaitway0") {
		t.Error("Stop removed a device it did not make")
	}
}

func TestRootADeviceRemovedFromOutsideEndsTheTunnel(t *testing.T) {
	l := newLab(t)
	eng := l.engine("removed", l.profile(profileOpts{allowedIPs: "0.0.0.0/0, ::/0", keepalive: 1}), tunnel.ModeAuto)
	eng.start()
	iface := eng.up(t)
	checkRoutes(t, fullTunnelRoutes(iface, viaVeth0)...)

	ip(t, "link", "del", iface)
	eventually(t, "the tunnel to fail", func() bool { return eng.status.last().State == tunnel.StateFailed })
	// The bypass route would send the endpoint around a tunnel that is gone.
	eventually(t, "the routes to be withdrawn", func() bool { return len(ownRoutes(t)) == 0 })
}

func TestRootAnMTUBelowTheIPv6MinimumFailsClearly(t *testing.T) {
	l := newLab(t)
	eng := l.engine("smallmtu", l.profile(profileOpts{allowedIPs: "10.200.0.0/24", extra: "MTU = 1000"}), tunnel.ModeAuto)
	eng.start()
	eventually(t, "the tunnel to fail", func() bool { return eng.status.last().State == tunnel.StateFailed })
	if err := eng.status.last().Err; !strings.Contains(err, "1280") || !strings.Contains(err, "IPv6") {
		t.Errorf("Err = %q, want it to name the IPv6 minimum of 1280", err)
	}
	if err := eng.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if names := nonLoopbackInterfaces(t); slices.ContainsFunc(names, ifaceNamePattern.MatchString) {
		t.Errorf("left %v", names)
	}

	// 1280 is the smallest MTU that works with both families.
	exact := l.engine("exactmtu", l.profile(profileOpts{allowedIPs: "10.200.0.0/24, fd00:200::/64", extra: "MTU = 1280", keepalive: 1}), tunnel.ModeAuto)
	exact.start()
	iface := exact.up(t)
	if out := ip(t, "-6", "addr", "show", "dev", iface); !strings.Contains(out, client6) {
		t.Errorf("the IPv6 address is gone at MTU 1280:\n%s", out)
	}
	pingOK(t, peerTunnel6)
}

func TestRootIPv6IsEnabledOnATunnelThatTheHostCreatesWithoutIt(t *testing.T) {
	l := newLab(t)
	// A host that disables IPv6 for the interfaces to come. The lab's veth pairs
	// exist already.
	setSysctl(t, "ipv6/conf/default/disable_ipv6", "1")

	for _, tt := range []struct {
		name      string
		addresses string
		v6Traffic bool
	}{
		{"with an IPv6 address", clientTunnel + "/24, " + client6 + "/64", true},
		// The halves of ::/0 need IPv6 on the interface as much as the address does.
		{"without an IPv6 address", clientTunnel + "/24", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// The lab's cleanups belong to the parent test; this engine is stopped before the next.
			eng := l.engine("noipv6", l.profile(profileOpts{addresses: tt.addresses, allowedIPs: "0.0.0.0/0, ::/0", keepalive: 1}), tunnel.ModeAuto)
			eng.start()
			iface := eng.up(t)
			if state, _ := os.ReadFile("/proc/sys/net/ipv6/conf/" + iface + "/disable_ipv6"); strings.TrimSpace(string(state)) != "0" {
				t.Errorf("disable_ipv6 of %s = %q, want 0", iface, state)
			}
			if !eng.logs.has("IPv6 is disabled by default") {
				t.Error("the engine did not say that it enabled IPv6")
			}
			checkRoutes(t, fullTunnelRoutes(iface, viaVeth0)...)
			pingOK(t, serverTunnel)
			if tt.v6Traffic {
				pingOK(t, peerTunnel6)
				pingOK(t, farAddr6)
			}
			if err := eng.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRootAnIPv6EndpointIsReachedAroundTheTunnel(t *testing.T) {
	l := newLab(t)
	eng := l.engine("v6endpoint", l.profile(profileOpts{
		allowedIPs: "0.0.0.0/0, ::/0",
		endpoint:   fmt.Sprintf("[%s]:%d", endpoint6, peerPort),
		keepalive:  1,
	}), tunnel.ModeAuto)
	eng.start()
	iface := eng.up(t)
	checkRoutes(t, fullTunnelRoutes(iface, bypassRoute(endpoint6+"/128", "2001:db8:1::2", "veth0"))...)
	if got := peerEndpoint(t, l.client.public); !strings.HasPrefix(got, "[2001:db8:1::1]:") {
		t.Errorf("the peer heard the client from %q, want 2001:db8:1::1", got)
	}
	pingOK(t, serverTunnel)
	pingOK(t, farAddr6)

	// An IPv6 socket follows its route too.
	ip(t, "-6", "route", "del", "default", "dev", "veth0")
	ip(t, "route", "del", "default", "dev", "veth0")
	eventually(t, "the peer to hear the client from veth2", func() bool {
		return strings.HasPrefix(peerEndpoint(t, l.client.public), "[2001:db8:2::1]:")
	})
	pingOK(t, farAddr6)
}

func TestRootTunDevicesGetTheNextFreeNameAndGoWithTheirDescriptor(t *testing.T) {
	requireNetns(t)
	var logs logSink
	var names []string
	var devs []tun.Device
	for range 2 {
		dev, err := createPlatformTun("owner", defaultAdapterPrefix, 1400, logs.log)
		if err != nil {
			t.Fatal(err)
		}
		devs = append(devs, dev)
		name, err := dev.Name()
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
		if iface, err := net.InterfaceByName(name); err != nil || iface.MTU != 1400 {
			t.Errorf("%s: %+v, %v; want an interface with MTU 1400", name, iface, err)
		}
	}
	if want := []string{"plaitway0", "plaitway1"}; !slices.Equal(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
	// Closing the first frees its name for the next device.
	if err := devs[0].Close(); err != nil {
		t.Fatal(err)
	}
	if !ifaceMissing(names[0]) {
		t.Errorf("%s is still there after Close", names[0])
	}
	again, err := createPlatformTun("owner", defaultAdapterPrefix, 1400, logs.log)
	if err != nil {
		t.Fatal(err)
	}
	if name, _ := again.Name(); name != names[0] {
		t.Errorf("the next device is %s, want %s again", name, names[0])
	}
	again.Close()
	devs[1].Close()
	for _, name := range names {
		if !ifaceMissing(name) {
			t.Errorf("%s is still there after Close", name)
		}
	}
}

func TestRootAMissingTunDeviceIsExplained(t *testing.T) {
	requireNetns(t)
	// An empty /dev/net in this mount namespace, which the test runs in alone.
	if out, err := exec.Command("mount", "-t", "tmpfs", "none", "/dev/net").CombinedOutput(); err != nil {
		t.Skipf("cannot hide /dev/net/tun (start the test with unshare -m): %v: %s", err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command("umount", "/dev/net").CombinedOutput(); err != nil {
			t.Errorf("umount /dev/net: %v: %s", err, out)
		}
	})

	_, err := createPlatformTun("owner", defaultAdapterPrefix, 1400, func(tunnel.LogLevel, string) {})
	if err == nil || !strings.Contains(err.Error(), "/dev/net/tun does not exist") || !strings.Contains(err.Error(), "modprobe tun") {
		t.Errorf("createPlatformTun = %v, want an error that says the device is missing and how to get it", err)
	}
	if info := Backend(Config{}).Probe(); info.Available || !strings.Contains(info.Detail, "/dev/net/tun does not exist") {
		t.Errorf("Probe() = %+v, want an unavailable engine that says why", info)
	}
}

// A process that dies takes its tun devices with it: nothing for a restarted
// daemon to find, and no name that stays taken.
func TestRootTunDeviceOfACrashedProcessIsGone(t *testing.T) {
	requireNetns(t)
	child := exec.Command(os.Args[0], "-test.run=^TestTunChildProcess$")
	child.Env = append(os.Environ(), "PLAITWAY_WG_TUN_CHILD=1")
	out, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer child.Process.Kill()
	var name string
	if _, err := fmt.Fscanln(out, &name); err != nil {
		t.Fatalf("the child printed no interface name: %v", err)
	}
	if ifaceMissing(name) {
		t.Fatalf("%s is not there while its process runs", name)
	}
	child.Process.Signal(syscall.SIGKILL)
	child.Wait()
	eventually(t, name+" to disappear", func() bool { return ifaceMissing(name) })
	t.Logf("%s was made by process %d, which was killed", name, child.Process.Pid)
}

// TestTunChildProcess is the process that TestRootTunDeviceOfACrashedProcessIsGone
// kills; on its own it does nothing.
func TestTunChildProcess(t *testing.T) {
	if os.Getenv("PLAITWAY_WG_TUN_CHILD") != "1" {
		t.Skip("runs only as the child of another test")
	}
	dev, err := createPlatformTun("child", defaultAdapterPrefix, 1400, func(tunnel.LogLevel, string) {})
	if err != nil {
		t.Fatal(err)
	}
	name, err := dev.Name()
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(name)
	select {}
}

// spyTun is a real tun device that also reports the packets wireguard-go writes
// into it, as the far end of a tunnel on a host that has no second network stack.
type spyTun struct {
	tun.Device
	written chan []byte
}

func (s *spyTun) Write(bufs [][]byte, offset int) (int, error) {
	n, err := s.Device.Write(bufs, offset)
	if err != nil {
		return n, err
	}
	// Linux counts the bytes it wrote, not the packets, so n says nothing here.
	for _, buf := range bufs {
		select {
		case s.written <- append([]byte(nil), buf[offset:]...):
		default:
		}
	}
	return n, nil
}

// isEchoRequestTo reports whether packet is an IPv4 ICMP echo request to dst.
func isEchoRequestTo(packet []byte, dst string) bool {
	const ipv4HeaderLen = 20
	return len(packet) > ipv4HeaderLen && packet[0]>>4 == 4 && packet[9] == 1 &&
		net.IP(packet[16:20]).String() == dst && packet[ipv4HeaderLen] == 8
}

func waitForEchoRequest(packets <-chan []byte, dst string, limit time.Duration) bool {
	deadline := time.After(limit)
	for {
		select {
		case packet := <-packets:
			if isEchoRequestTo(packet, dst) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

func containsIP(addrs []net.Addr, want string) bool {
	for _, a := range addrs {
		if ipNet, ok := a.(*net.IPNet); ok && ipNet.IP.String() == want {
			return true
		}
	}
	return false
}

// Two engines of this process are the two ends of a tunnel over loopback, and
// the Reconciler installs the route that sends a ping into the client's tun.
func TestRootTwoTunTunnelsCarryAPing(t *testing.T) {
	checkLeaks(t)
	requireNetns(t)
	requireTools(t, "ip", "ping")
	ip(t, "link", "set", "lo", "up")

	const (
		serverAddr = "198.51.100.1"
		clientAddr = "198.51.100.2"
		farNet     = "203.0.113.0/24"
		farHost    = "203.0.113.7"
	)
	serverKey, clientKey := newKeyPair(t), newKeyPair(t)
	port := freeUDPPort(t)
	serverContent := fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = %s/32\nListenPort = %d\n\n[Peer]\nPublicKey = %s\nAllowedIPs = %s/32\n",
		serverKey.private, serverAddr, port, clientKey.public, clientAddr)
	clientContent := fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = %s/32\n\n[Peer]\nPublicKey = %s\nEndpoint = 127.0.0.1:%d\nAllowedIPs = %s\n",
		clientKey.private, clientAddr, serverKey.public, port, farNet)

	rec := startReconciler(t, nil)

	newEng := func(name, content string, cfg Config) (*engine, *statusLog) {
		cfg.pollInterval, cfg.stallGrace = testPoll, testGrace
		deps := tunnel.Deps{Network: rec, Net: fakeMonitor{}, Log: (&logSink{}).log}
		eng, err := newEngine(cfg.withDefaults(), tunnel.Spec{Owner: tunnel.OwnerID(name), Name: name, Content: []byte(content)}, deps)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := eng.Stop(context.Background()); err != nil {
				t.Errorf("Stop(%s): %v", name, err)
			}
		})
		return eng, collectStatus(eng.Status())
	}
	spy := &spyTun{written: make(chan []byte, 64)}
	server, serverStatus := newEng("root-server", serverContent, Config{
		TunFactory: func(mtu int) (tun.Device, error) {
			dev, err := createPlatformTun("root-server", defaultAdapterPrefix, mtu, func(tunnel.LogLevel, string) {})
			if err != nil {
				return nil, err
			}
			spy.Device = dev
			return spy, nil
		},
	})
	client, clientStatus := newEng("root-client", clientContent, Config{})
	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Start returns at once. A client that shakes hands before the server's
	// socket is bound waits RekeyTimeout for its next try, so the client starts
	// when the server's interface has its address, which its own setup outlasts.
	eventually(t, "the server's interface to have its address", func() bool {
		netIface, err := net.InterfaceByName(serverStatus.last().Iface)
		if err != nil {
			return false
		}
		addrs, _ := netIface.Addrs()
		return containsIP(addrs, serverAddr)
	})
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	eventually(t, "both tunnels to come up", func() bool {
		return serverStatus.last().State == tunnel.StateUp && clientStatus.last().State == tunnel.StateUp
	})

	serverIface, clientIface := serverStatus.last().Iface, clientStatus.last().Iface
	for iface, want := range map[string]string{serverIface: serverAddr, clientIface: clientAddr} {
		netIface, err := net.InterfaceByName(iface)
		if err != nil {
			t.Fatalf("interface %s: %v", iface, err)
		}
		addrs, _ := netIface.Addrs()
		if !containsIP(addrs, want) {
			t.Errorf("%s has addresses %v, want %s", iface, addrs, want)
		}
	}
	// The server routes the client's address into its tun, the client the far network.
	checkRoutes(t, append(tunnelRoutes(serverIface, clientAddr+"/32"), tunnelRoutes(clientIface, farNet)...)...)

	// Nothing answers on the far side, so the exit status of ping means nothing;
	// what counts is that the request came out of the server's tun.
	pingOutput, _ := exec.Command("ping", "-c", "3", "-W", "1", "-I", clientAddr, farHost).CombinedOutput()
	if !waitForEchoRequest(spy.written, farHost, waitTimeout) {
		t.Fatalf("no echo request to %s came out of %s; ping said:\n%s", farHost, serverIface, pingOutput)
	}
	eventually(t, "the client's counters", func() bool { return clientStatus.last().Stats.TxBytes > 0 })

	for _, eng := range []*engine{client, server} {
		if err := eng.Stop(context.Background()); err != nil {
			t.Errorf("Stop: %v", err)
		}
	}
	for _, iface := range []string{serverIface, clientIface} {
		if !ifaceMissing(iface) {
			t.Errorf("%s is still there after Stop", iface)
		}
	}
	checkRoutes(t)
}
