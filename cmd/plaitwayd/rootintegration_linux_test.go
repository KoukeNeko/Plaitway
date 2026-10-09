//go:build rootintegration && linux

package main

// These tests run the real daemon, the code path of main with the real engines
// and the Linux adapters, and drive it as a client of its socket. The daemon
// adds routes and creates tun devices in the network namespace it runs in, so
// the tests refuse to run anywhere but in a private user and network namespace,
// which needs no privileges and keeps the machine's own network out of reach:
//
//	go test -c -tags rootintegration -o plaitwayd.test ./cmd/plaitwayd
//	unshare -Urnm sh -c 'mount -t tmpfs none /run; ip link set lo up;
//	    PLAITWAY_ROOT_TESTS=1 ./plaitwayd.test -test.v -test.run TestRoot'
//
// The tmpfs on /run is where ip-netns(8) keeps the peer's namespace, and it
// hides the machine's D-Bus and systemd-resolved sockets, which the DNS
// adapter would otherwise reach through the file system. ip(8), ping(8) and
// wg(8) set up and check what the daemon did. The lab looks like this:
//
//	this namespace                           namespace plwpeer
//	veth0 10.77.1.1/24             <--->     veth1 10.77.1.2/24
//	default route via 10.77.1.2              lo: 198.51.100.1, the endpoint
//	plaitway0 (the tunnel)         <--->     wg0 10.77.9.1/24, the kernel's WireGuard
//
// The daemon runs as a process of its own, the test binary itself with
// TestRootDaemonProcess as its main, so that a test can stop it the way systemd
// does (SIGTERM) and the way a crash does (SIGKILL).

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/osnet/linux"
	"github.com/KoukeNeko/Plaitway/internal/transport"
)

const (
	peerNS       = "plwpeer"
	peerPort     = 51820
	endpointAddr = "198.51.100.1"
	hostAddr     = "10.77.1.1"
	peerHostAddr = "10.77.1.2"
	peerTunnel   = "10.77.9.1"
	clientTunnel = "10.77.9.2"

	// The routes of the Reconciler on Linux: the tunnel's metric and the
	// metric of the routes that keep the endpoint reachable.
	tunnelMetric = 5
	bypassMetric = 1

	// How long the tests wait for the kernel, the handshake and the daemon.
	labTimeout = 30 * time.Second

	// helperArgsEnv carries the command line of the daemon process, and
	// helperNoTunEnv asks for a process that does not see /dev/net/tun.
	helperArgsEnv  = "PLAITWAYD_TEST_DAEMON_ARGS"
	helperArgsSep  = "\x1f"
	helperNoTunEnv = "PLAITWAYD_TEST_NO_TUN"
	helperProcName = "TestRootDaemonProcess"
)

var tunnelInterface = regexp.MustCompile(`^plaitway[0-9]+$`)

// TestRootDaemonProcess is the main of the daemon processes the tests start.
func TestRootDaemonProcess(t *testing.T) {
	args := os.Getenv(helperArgsEnv)
	if args == "" {
		t.Skip("only runs as the daemon process of another test")
	}
	if os.Getenv(helperNoTunEnv) != "" {
		// The process has a mount namespace of its own (see startRootDaemon):
		// the tun device is hidden from it and from nothing else.
		if err := syscall.Mount("tmpfs", "/dev/net", "tmpfs", 0, ""); err != nil {
			t.Fatalf("hide /dev/net: %v", err)
		}
	}
	os.Args = append([]string{"plaitwayd"}, strings.Split(args, helperArgsSep)...)
	main()
}

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

// requireLab skips unless it was asked to run, and refuses to go on anywhere
// but in a user namespace of its own, with a network namespace that has nothing
// in it but loopback and a /run that does not lead to the machine's D-Bus or
// systemd-resolved.
func requireLab(t *testing.T) {
	t.Helper()
	if os.Getenv("PLAITWAY_ROOT_TESTS") != "1" || os.Geteuid() != 0 {
		t.Skip("needs PLAITWAY_ROOT_TESTS=1 and effective uid 0, in a private user and network namespace")
	}
	uidMap, err := os.ReadFile("/proc/self/uid_map")
	if err != nil {
		t.Fatalf("refusing to run: cannot tell whether this is a user namespace: %v", err)
	}
	if strings.Fields(string(uidMap))[1] == "0" {
		t.Fatal("refusing to run: this is the machine's own user namespace; start the test with unshare -Urnm")
	}
	if names := nonLoopbackInterfaces(t); len(names) != 0 {
		t.Fatalf("refusing to run: the namespace has interfaces besides lo (%v); this is not a private network namespace", names)
	}
	for _, socket := range []string{"/run/dbus/system_bus_socket", "/run/systemd/resolve/io.systemd.Resolve"} {
		if _, err := os.Stat(socket); err == nil {
			t.Fatalf("refusing to run: %s is reachable and would let the daemon change the machine's DNS; mount a tmpfs on /run", socket)
		}
	}
	for _, tool := range []string{"ip", "ping", "wg"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("needs %s: %v", tool, err)
		}
	}
}

func cmd(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v: %s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

func ip(t *testing.T, args ...string) string { return cmd(t, "ip", args...) }

type keyPair struct{ private, public string }

func newKeyPair(t *testing.T) keyPair {
	t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return keyPair{
		private: base64.StdEncoding.EncodeToString(key.Bytes()),
		public:  base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()),
	}
}

// lab is the network of the diagram above, without the daemon.
type lab struct {
	t            *testing.T
	peer, client keyPair
}

func newLab(t *testing.T) *lab {
	t.Helper()
	requireLab(t)
	l := &lab{t: t, peer: newKeyPair(t), client: newKeyPair(t)}

	ip(t, "netns", "add", peerNS)
	t.Cleanup(func() {
		// Deleting the namespace takes its end of the veth pair, and so ours,
		// with it.
		if out, err := exec.Command("ip", "netns", "del", peerNS).CombinedOutput(); err != nil {
			t.Errorf("ip netns del: %v: %s", err, out)
		}
		// The kernel deletes the namespace in the background, and the next
		// test starts only when the interface is gone.
		var left []string
		for deadline := time.Now().Add(labTimeout); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if left = nonLoopbackInterfaces(t); len(left) == 0 {
				return
			}
		}
		t.Errorf("interfaces left behind: %v", left)
	})
	ip(t, "-n", peerNS, "link", "set", "lo", "up")
	ip(t, "link", "add", "veth0", "type", "veth", "peer", "name", "veth1", "netns", peerNS)
	ip(t, "link", "set", "veth0", "up")
	ip(t, "addr", "add", hostAddr+"/24", "dev", "veth0")
	ip(t, "-n", peerNS, "link", "set", "veth1", "up")
	ip(t, "-n", peerNS, "addr", "add", peerHostAddr+"/24", "dev", "veth1")
	ip(t, "-n", peerNS, "addr", "add", endpointAddr+"/32", "dev", "lo")
	// The endpoint is off-link: the tunnel's own traffic reaches it through the
	// default route, as a full tunnel's must.
	ip(t, "route", "add", "default", "via", peerHostAddr, "dev", "veth0", "metric", "100")

	ip(t, "-n", peerNS, "link", "add", "wg0", "type", "wireguard")
	conf := fmt.Sprintf("[Interface]\nPrivateKey = %s\nListenPort = %d\n\n[Peer]\nPublicKey = %s\nAllowedIPs = %s/32\n",
		l.peer.private, peerPort, l.client.public, clientTunnel)
	path := filepath.Join(t.TempDir(), "peer.conf")
	if err := os.WriteFile(path, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd(t, "ip", "netns", "exec", peerNS, "wg", "setconf", "wg0", path)
	ip(t, "-n", peerNS, "addr", "add", peerTunnel+"/24", "dev", "wg0")
	ip(t, "-n", peerNS, "link", "set", "wg0", "up")
	return l
}

// profile is a WireGuard profile that sends all IPv4 traffic to the peer. extra
// is more lines for the [Interface] section.
func (l *lab) profile(extra string) string {
	return fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = %s/24\n%s\n[Peer]\nPublicKey = %s\nEndpoint = %s:%d\nAllowedIPs = 0.0.0.0/0\nPersistentKeepalive = 1\n",
		l.client.private, clientTunnel, extra, l.peer.public, endpointAddr, peerPort)
}

// plaitwayRoutes lists the routes Plaitway installed (rtm_protocol 199) as
// "prefix dev interface metric n", sorted.
func plaitwayRoutes(t *testing.T) []string {
	t.Helper()
	routes, err := linux.NewRouteTable().Dump()
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, rt := range routes {
		if rt.Flags>>8&0xff == linux.RouteProtocol {
			lines = append(lines, fmt.Sprintf("%s dev %s metric %d", rt.Dst, rt.Iface, rt.Metric))
		}
	}
	slices.Sort(lines)
	return lines
}

func tunnelInterfaces(t *testing.T) []string {
	t.Helper()
	var names []string
	for _, name := range nonLoopbackInterfaces(t) {
		if tunnelInterface.MatchString(name) {
			names = append(names, name)
		}
	}
	return names
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(labTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// rootDaemon is a daemon process and a client of its socket.
type rootDaemon struct {
	t       *testing.T
	cmd     *exec.Cmd
	logPath string
	exited  chan error
	conn    *grpc.ClientConn
	client  pb.DaemonServiceClient
}

// daemonDirs are what a daemon keeps, which the next one on the same directories
// finds again.
type daemonDirs struct {
	socket, state, run, openvpn string
}

func newDaemonDirs(t *testing.T) daemonDirs {
	t.Helper()
	dir := shortDir(t)
	d := daemonDirs{
		socket:  filepath.Join(dir, "plaitwayd.sock"),
		state:   filepath.Join(dir, "state"),
		run:     filepath.Join(dir, "run"),
		openvpn: filepath.Join(dir, "openvpn"),
	}
	// An openvpn that is there, but that the daemon cannot trust: this
	// namespace's root is the user who runs the tests, and everything above the
	// file belongs to nobody.
	if err := os.WriteFile(d.openvpn, []byte(fakeOpenVPN), 0o755); err != nil {
		t.Fatal(err)
	}
	return d
}

// startRootDaemon starts the daemon the way the unit does, with the flags of
// the production layout pointed at dirs, and waits until it answers. logName
// names its log in the directory of the socket.
func startRootDaemon(t *testing.T, dirs daemonDirs, logName string) *rootDaemon {
	t.Helper()
	return startDaemonProcess(t, dirs, logName, false)
}

func startDaemonProcess(t *testing.T, dirs daemonDirs, logName string, hideTun bool) *rootDaemon {
	t.Helper()
	args := []string{
		"-socket", dirs.socket, "-socket-mode", "0666",
		"-state-dir", dirs.state, "-run-dir", dirs.run, "-openvpn", dirs.openvpn,
	}
	d := &rootDaemon{t: t, logPath: filepath.Join(filepath.Dir(dirs.socket), logName), exited: make(chan error, 1)}
	logFile, err := os.Create(d.logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	d.cmd = exec.Command(os.Args[0], "-test.run=^"+helperProcName+"$")
	d.cmd.Env = append(os.Environ(), helperArgsEnv+"="+strings.Join(args, helperArgsSep))
	if hideTun {
		d.cmd.Env = append(d.cmd.Env, helperNoTunEnv+"=1")
		d.cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNS}
	}
	d.cmd.Stderr = logFile
	if err := d.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { d.exited <- d.cmd.Wait() }()
	t.Cleanup(func() {
		select {
		case err := <-d.exited:
			d.exited <- err
		default:
			d.cmd.Process.Kill()
			<-d.exited
		}
	})

	// gRPC would wait a second before it tries again.
	waitFor(t, "the socket", func() bool { _, err := os.Lstat(dirs.socket); return err == nil })
	d.conn, err = grpc.NewClient(transport.Target(dirs.socket),
		append(transport.DialOptions(), grpc.WithTransportCredentials(insecure.NewCredentials()))...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.conn.Close() })
	d.client = pb.NewDaemonServiceClient(d.conn)
	ctx, cancel := context.WithTimeout(context.Background(), labTimeout)
	defer cancel()
	if _, err := d.client.GetDaemonInfo(ctx, &pb.GetDaemonInfoRequest{}, grpc.WaitForReady(true)); err != nil {
		t.Fatalf("the daemon does not answer: %v\nlog:\n%s", err, d.log())
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("daemon log %s:\n%s", logName, d.log())
		}
	})
	return d
}

func (d *rootDaemon) log() string {
	out, err := os.ReadFile(d.logPath)
	if err != nil {
		return err.Error()
	}
	return string(out)
}

func (d *rootDaemon) ctx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), labTimeout)
	d.t.Cleanup(cancel)
	return ctx
}

// wait returns how the process ended, and fails the test if it does not within
// labTimeout.
func (d *rootDaemon) wait() error {
	d.t.Helper()
	select {
	case err := <-d.exited:
		d.exited <- err
		return err
	case <-time.After(labTimeout):
		d.t.Fatalf("the daemon did not stop\nlog:\n%s", d.log())
		return nil
	}
}

func (d *rootDaemon) info() *pb.DaemonInfo {
	d.t.Helper()
	info, err := d.client.GetDaemonInfo(d.ctx(), &pb.GetDaemonInfoRequest{})
	if err != nil {
		d.t.Fatalf("GetDaemonInfo: %v", err)
	}
	return info
}

func (d *rootDaemon) diagnostics() *pb.Diagnostics {
	d.t.Helper()
	diag, err := d.client.GetDiagnostics(d.ctx(), &pb.GetDiagnosticsRequest{})
	if err != nil {
		d.t.Fatalf("GetDiagnostics: %v", err)
	}
	return diag
}

func (d *rootDaemon) importProfile(name, content string) *pb.Profile {
	d.t.Helper()
	resp, err := d.client.ImportProfile(d.ctx(), &pb.ImportProfileRequest{Name: name, Content: []byte(content)})
	if err != nil {
		d.t.Fatalf("ImportProfile: %v", err)
	}
	return resp.Profile
}

func (d *rootDaemon) setEnabled(id string, enabled bool) {
	d.t.Helper()
	if _, err := d.client.SetProfileEnabled(d.ctx(), &pb.SetProfileEnabledRequest{Id: id, Enabled: enabled}); err != nil {
		d.t.Fatalf("SetProfileEnabled(%v): %v", enabled, err)
	}
}

func (d *rootDaemon) profile(id string) *pb.Profile {
	d.t.Helper()
	resp, err := d.client.ListProfiles(d.ctx(), &pb.ListProfilesRequest{})
	if err != nil {
		d.t.Fatalf("ListProfiles: %v", err)
	}
	for _, p := range resp.Profiles {
		if p.Id == id {
			return p
		}
	}
	d.t.Fatalf("profile %s is not listed", id)
	return nil
}

// waitState waits for the profile to reach state and returns it.
func (d *rootDaemon) waitState(id string, want pb.ProfileState) *pb.Profile {
	d.t.Helper()
	var p *pb.Profile
	waitFor(d.t, fmt.Sprintf("profile to be %v (log:\n%s)", want, d.log()), func() bool {
		p = d.profile(id)
		return p.State == want
	})
	return p
}

// A route the daemon owns for the profile, by prefix.
func ownedRoutes(diag *pb.Diagnostics, owner string) map[string]*pb.OwnedRoute {
	routes := map[string]*pb.OwnedRoute{}
	for _, r := range diag.OwnedRoutes {
		if r.Owner == owner {
			routes[r.Prefix] = r
		}
	}
	return routes
}

func ping(t *testing.T, addr string) {
	t.Helper()
	out, err := exec.Command("ping", "-c", "1", "-W", "3", addr).CombinedOutput()
	if err != nil {
		t.Fatalf("ping %s: %v\n%s\nip route:\n%s", addr, err, out, ip(t, "route"))
	}
}

func wantRoutes(iface string) []string {
	return []string{
		fmt.Sprintf("0.0.0.0/1 dev %s metric %d", iface, tunnelMetric),
		fmt.Sprintf("128.0.0.0/1 dev %s metric %d", iface, tunnelMetric),
		fmt.Sprintf("%s/32 dev veth0 metric %d", endpointAddr, bypassMetric),
	}
}

// The profile's tunnel: up, with the interface and the routes of a full tunnel,
// which carries traffic to the peer.
func (d *rootDaemon) requireTunnelUp(id string) string {
	d.t.Helper()
	p := d.waitState(id, pb.ProfileState_PROFILE_STATE_CONNECTED)
	iface := p.GetStatus().GetInterfaceName()
	if !tunnelInterface.MatchString(iface) {
		d.t.Fatalf("the tunnel interface is %q", iface)
	}
	waitFor(d.t, "the routes of the tunnel", func() bool { return slices.Equal(plaitwayRoutes(d.t), wantRoutes(iface)) })
	ping(d.t, peerTunnel)
	return iface
}

func TestRootDaemonRunsAWireGuardTunnelFromStartToSIGTERM(t *testing.T) {
	l := newLab(t)
	dirs := newDaemonDirs(t)
	d := startRootDaemon(t, dirs, "daemon.log")

	// The daemon is privileged and says which engines it has. WireGuard is part
	// of the daemon; openvpn is there but not trusted in this namespace.
	info := d.info()
	if !info.Privileged {
		t.Error("the daemon does not report that it is privileged")
	}
	engines := map[pb.ProfileKind]*pb.EngineInfo{}
	for _, e := range info.Engines {
		engines[e.Kind] = e
	}
	if e := engines[pb.ProfileKind_PROFILE_KIND_WIREGUARD]; e == nil || !e.Available {
		t.Fatalf("WireGuard engine: %v, want it available", e)
	}
	if e := engines[pb.ProfileKind_PROFILE_KIND_OPENVPN]; e == nil || e.Available || !strings.Contains(e.Detail, "cannot be trusted") {
		t.Errorf("OpenVPN engine: %v, want it unavailable because root does not own it", e)
	}
	// What each engine can do is in the log once, from the start.
	log := d.log()
	for _, want := range []string{"engine available", "engine=wireguard", "engine unavailable", "engine=openvpn"} {
		if !strings.Contains(log, want) {
			t.Errorf("the log lacks %q:\n%s", want, log)
		}
	}
	// No resolved here: resolvectl finds nobody, and /etc/resolv.conf leads to
	// its files, which are not here either (if it does not, there is no warning
	// about it).
	warnings := []string{"DNS settings of tunnels cannot be applied"}
	if resolvConfProblem(resolvConfPath, resolvedDir) != "" {
		warnings = append(warnings, "may have no effect on programs")
	}
	for _, want := range warnings {
		waitFor(t, "the warning "+want, func() bool { return strings.Contains(d.log(), want) })
	}

	p := d.importProfile("lab", l.profile(""))
	if p.State != pb.ProfileState_PROFILE_STATE_DISCONNECTED {
		t.Fatalf("a new profile is %v, want disconnected", p.State)
	}
	d.setEnabled(p.Id, true)
	iface := d.requireTunnelUp(p.Id)

	// The kernel's WireGuard saw the handshake of the client.
	if out := cmd(t, "ip", "netns", "exec", peerNS, "wg", "show", "wg0", "latest-handshakes"); strings.HasSuffix(strings.TrimSpace(out), "\t0") {
		t.Errorf("the peer saw no handshake: %s", out)
	}

	diag := d.diagnostics()
	if !diag.Daemon.Privileged || len(diag.Network.Interfaces) == 0 || diag.Network.DefaultInterfaceV4 != "veth0" {
		t.Errorf("network in the diagnostics: %v, privileged %v; want the default route through veth0", diag.Network, diag.Daemon.Privileged)
	}
	owned := ownedRoutes(diag, p.Id)
	for prefix, kind := range map[string]pb.RouteKind{
		"0.0.0.0/1":          pb.RouteKind_ROUTE_KIND_DEFAULT,
		"128.0.0.0/1":        pb.RouteKind_ROUTE_KIND_DEFAULT,
		endpointAddr + "/32": pb.RouteKind_ROUTE_KIND_BYPASS,
	} {
		if r := owned[prefix]; r == nil || r.Kind != kind || r.State != pb.RouteState_ROUTE_STATE_INSTALLED {
			t.Errorf("owned route %s: %v, want installed as %v", prefix, r, kind)
		}
	}
	if len(diag.StaleRoutes) != 0 {
		t.Errorf("stale routes: %v", diag.StaleRoutes)
	}
	if len(diag.RecentJournal) == 0 {
		t.Error("the journal is empty although routes were added")
	}
	if fi, err := os.Stat(filepath.Join(dirs.state, "journal")); err != nil || fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("journal in the state directory: %v, %v; want it there and private", fi, err)
	}
	if fi, err := os.Stat(dirs.state); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("state directory: %v, %v; want mode 0700", fi, err)
	}

	// Resync rebuilds what the daemon owns, and nothing changes for the tunnel.
	if _, err := d.client.Resync(d.ctx(), &pb.ResyncRequest{}); err != nil {
		t.Fatalf("Resync: %v", err)
	}
	if got := d.requireTunnelUp(p.Id); got != iface {
		t.Errorf("the tunnel interface changed from %s to %s", iface, got)
	}

	// Disabling takes the interface and the routes away.
	d.setEnabled(p.Id, false)
	d.waitState(p.Id, pb.ProfileState_PROFILE_STATE_DISCONNECTED)
	waitFor(t, "the routes and the interface to go", func() bool {
		return len(plaitwayRoutes(t)) == 0 && len(tunnelInterfaces(t)) == 0
	})

	// A stop by SIGTERM, as systemd stops the unit, leaves nothing behind either,
	// and ends within the budget the unit's TimeoutStopSec is set against.
	d.setEnabled(p.Id, true)
	d.requireTunnelUp(p.Id)
	stopped := time.Now()
	if err := d.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := d.wait(); err != nil {
		t.Fatalf("the daemon ended with %v after SIGTERM\nlog:\n%s", err, d.log())
	}
	took := time.Since(stopped)
	t.Logf("the daemon stopped %s after SIGTERM", took.Round(time.Millisecond))
	if took > serverStopTimeout+shutdownTimeout {
		t.Errorf("the daemon took %s to stop, more than its budget of %s", took, serverStopTimeout+shutdownTimeout)
	}
	if routes := plaitwayRoutes(t); len(routes) != 0 {
		t.Errorf("routes left after SIGTERM: %q", routes)
	}
	if left := tunnelInterfaces(t); len(left) != 0 {
		t.Errorf("interfaces left after SIGTERM: %v", left)
	}
	if _, err := os.Lstat(dirs.socket); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the socket was left behind: %v", err)
	}
}

// What a crash leaves behind is removed by the next start, from the journal: the
// route that keeps the endpoint reachable belongs to veth0 and outlives the
// tunnel, whose device the kernel destroys with the process.
func TestRootDaemonRecoversAfterBeingKilled(t *testing.T) {
	l := newLab(t)
	dirs := newDaemonDirs(t)
	first := startRootDaemon(t, dirs, "first.log")
	p := first.importProfile("lab", l.profile(""))
	first.setEnabled(p.Id, true)
	iface := first.requireTunnelUp(p.Id)

	if err := first.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	var exit *exec.ExitError
	if err := first.wait(); !errors.As(err, &exit) || exit.ProcessState.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("the daemon ended with %v, want SIGKILL", err)
	}
	waitFor(t, "the kernel to destroy the tunnel device", func() bool { return len(tunnelInterfaces(t)) == 0 })
	bypass := fmt.Sprintf("%s/32 dev veth0 metric %d", endpointAddr, bypassMetric)
	if left := plaitwayRoutes(t); !slices.Equal(left, []string{bypass}) {
		t.Fatalf("after the kill the routes of Plaitway are %q, want only %q: it is what the next start has to remove (tunnel %s)", left, bypass, iface)
	}

	second := startRootDaemon(t, dirs, "second.log")
	if left := plaitwayRoutes(t); len(left) != 0 {
		t.Errorf("routes left after the restart: %q", left)
	}
	if log := second.log(); !strings.Contains(log, "removed a route of an earlier run") || !strings.Contains(log, endpointAddr) {
		t.Errorf("the second daemon's log does not say that it removed the route:\n%s", log)
	}
	// The profile survived, is not connected, and the daemon is as good as new.
	if got := second.profile(p.Id); got.State != pb.ProfileState_PROFILE_STATE_DISCONNECTED {
		t.Errorf("after the restart the profile is %v, want disconnected", got.State)
	}
	if diag := second.diagnostics(); len(diag.OwnedRoutes) != 0 || len(diag.StaleRoutes) != 0 {
		t.Errorf("after the restart the daemon owns %v and finds %v stale", diag.OwnedRoutes, diag.StaleRoutes)
	}
	second.setEnabled(p.Id, true)
	second.requireTunnelUp(p.Id)
	second.setEnabled(p.Id, false)
	second.waitState(p.Id, pb.ProfileState_PROFILE_STATE_DISCONNECTED)
	waitFor(t, "the routes and the interface to go", func() bool {
		return len(plaitwayRoutes(t)) == 0 && len(tunnelInterfaces(t)) == 0
	})
}

// DNS goes through systemd-resolved, which this namespace does not have: the
// failure is on the profile, and the tunnel carries traffic.
func TestRootDaemonReportsAFailedDNSEntryOnTheProfileAndKeepsTheTunnel(t *testing.T) {
	l := newLab(t)
	d := startRootDaemon(t, newDaemonDirs(t), "daemon.log")
	p := d.importProfile("lab", l.profile("DNS = "+peerTunnel))
	d.setEnabled(p.Id, true)
	d.requireTunnelUp(p.Id)

	var dns *pb.DnsStatus
	waitFor(t, "the DNS entry to be reported as failed", func() bool {
		if st := d.profile(p.Id).GetStatus(); len(st.GetDns()) == 1 && st.Dns[0].State == pb.RouteState_ROUTE_STATE_FAILED {
			dns = st.Dns[0]
			return true
		}
		return false
	})
	t.Logf("DNS status of the profile: %v", dns)
	if !slices.Equal(dns.Servers, []string{peerTunnel}) || !strings.Contains(dns.Detail, "systemd-resolved") {
		t.Errorf("DNS status %v, want the server of the profile and the reason: systemd-resolved", dns)
	}
	got := d.profile(p.Id)
	if got.State != pb.ProfileState_PROFILE_STATE_CONNECTED {
		t.Errorf("the profile is %v, want connected: only its DNS failed", got.State)
	}
	for _, r := range got.Status.Routes {
		if r.State != pb.RouteState_ROUTE_STATE_INSTALLED {
			t.Errorf("route %s is %v (%s) although only DNS failed", r.Prefix, r.State, r.Detail)
		}
	}
	for _, w := range got.Status.Warnings {
		if strings.Contains(w, "not installed") {
			t.Errorf("warning %q on a tunnel whose routes are installed", w)
		}
	}
	ping(t, peerTunnel)
}

// A host without the tun device, or a container that was not given it, still
// gets a daemon: the engine says what is missing, in DaemonInfo and on the
// profile, and nothing is installed.
func TestRootDaemonStartsWithoutTheTunDevice(t *testing.T) {
	l := newLab(t)
	d := startDaemonProcess(t, newDaemonDirs(t), "daemon.log", true)

	var wireguard *pb.EngineInfo
	for _, e := range d.info().Engines {
		if e.Kind == pb.ProfileKind_PROFILE_KIND_WIREGUARD {
			wireguard = e
		}
	}
	if wireguard == nil || wireguard.Available || !strings.Contains(wireguard.Detail, "/dev/net/tun") {
		t.Fatalf("WireGuard engine: %v, want it unavailable and /dev/net/tun named", wireguard)
	}
	if log := d.log(); !strings.Contains(log, "engine unavailable") || !strings.Contains(log, "engine=wireguard") || !strings.Contains(log, "/dev/net/tun") {
		t.Errorf("the log does not say that the WireGuard engine is unavailable and why:\n%s", log)
	}

	p := d.importProfile("lab", l.profile(""))
	d.setEnabled(p.Id, true)
	failed := d.waitState(p.Id, pb.ProfileState_PROFILE_STATE_FAILED)
	if !strings.Contains(failed.LastError, "/dev/net/tun") {
		t.Errorf("last error %q, want the missing device named", failed.LastError)
	}
	if routes, ifaces := plaitwayRoutes(t), tunnelInterfaces(t); len(routes) != 0 || len(ifaces) != 0 {
		t.Errorf("a tunnel that never started left routes %q and interfaces %v", routes, ifaces)
	}
}
