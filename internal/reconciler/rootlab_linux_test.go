//go:build rootintegration && linux

package reconciler

// The machine the kernel tests run on: a private network namespace with an
// uplink that looks like a physical one (a veth whose far end lives in a second
// namespace, so that a packet sent to an address there really leaves through the
// uplink), tun devices that stand in for the tunnels of the engines, and ip(8)
// for everything that another program would do to the host.
//
// The tests refuse to run anywhere but in a namespace that has nothing in it but
// loopback (see requireNetns). Build the test binary and run it as the fake root
// of a user and network namespace:
//
//	go test -c -tags rootintegration -o x.test ./internal/reconciler
//	unshare -Urn sh -c 'ip link set lo up; PLAITWAY_ROOT_TESTS=1 ./x.test -test.v -test.run Kernel'
//
// What the tests add lives in the documentation ranges 198.51.100.0/24,
// 203.0.113.0/24 and 2001:db8::/32, and 10.0.0.0/8 for what a VPN pushes.

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/osnet/fake"
	"github.com/KoukeNeko/Plaitway/internal/osnet/linux"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

var (
	netnsOnce sync.Once
	netnsErr  error
)

// requireNetns refuses to go on anywhere but in a namespace that has nothing in
// it but loopback, and puts the namespace back to that when the test is done.
func requireNetns(t *testing.T) {
	t.Helper()
	if os.Getenv("PLAITWAY_ROOT_TESTS") != "1" || os.Geteuid() != 0 {
		t.Fatal("refusing to run: this test changes the network; set PLAITWAY_ROOT_TESTS=1 and run it as root in a private network namespace")
	}
	netnsOnce.Do(func() {
		for _, name := range nonLoopbackInterfaces(t) {
			netnsErr = fmt.Errorf("refusing to run: the namespace has an interface %s besides lo; this is not a private network namespace", name)
			return
		}
	})
	if netnsErr != nil {
		t.Fatal(netnsErr)
	}
	t.Cleanup(func() {
		for _, name := range nonLoopbackInterfaces(t) {
			// Deleting one end of a veth pair deletes the other.
			exec.Command("ip", "link", "del", name).Run()
		}
		for _, args := range [][]string{
			// The rules the tests add; the ones every namespace has stay.
			{"-4", "rule", "del", "pref", "32764"}, {"-4", "rule", "del", "pref", "32765"},
			{"-6", "rule", "del", "pref", "32764"}, {"-6", "rule", "del", "pref", "32765"},
			{"-4", "route", "flush", "table", "main"}, {"-6", "route", "flush", "table", "main"},
			{"-4", "route", "flush", "table", "51820"}, {"-6", "route", "flush", "table", "51820"},
		} {
			exec.Command("ip", args...).Run()
		}
		if left := nonLoopbackInterfaces(t); len(left) != 0 {
			t.Errorf("interfaces left behind: %v", left)
		}
	})
}

// fallbackDevices are the devices that a tunnel module makes in every network
// namespace when it is loaded, and that cannot be deleted.
var fallbackDevices = []string{"tunl0", "sit0", "gre0", "gretap0", "erspan0", "ip6tnl0", "ip6gre0"}

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

// runIP runs ip(8) and fails the test when it fails.
func runIP(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("ip", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("ip %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out)
}

// tryIP runs ip(8) and returns its failure.
func tryIP(args ...string) error {
	if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("ip %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func ifIndexOf(t *testing.T, name string) uint32 {
	t.Helper()
	iface, err := net.InterfaceByName(name)
	if err != nil {
		t.Fatal(err)
	}
	return uint32(iface.Index)
}

func eventually(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// farNS is the other end of the uplink: a process that lives in a network
// namespace of its own and does nothing, so that the namespace stays.
type farNS struct {
	t    *testing.T
	cmd  *exec.Cmd
	file *os.File
	held map[netip.Addr]bool
}

func newFarNS(t *testing.T) *farNS {
	t.Helper()
	cmd := exec.Command("sleep", "900")
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNET, Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the far namespace: %v", err)
	}
	f, err := os.Open(fmt.Sprintf("/proc/%d/ns/net", cmd.Process.Pid))
	if err != nil {
		cmd.Process.Kill()
		t.Fatal(err)
	}
	far := &farNS{t: t, cmd: cmd, file: f, held: make(map[netip.Addr]bool)}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		f.Close()
	})
	far.ip("link", "set", "lo", "up")
	// As in the host namespace: no duplicate address detection, whose wait the
	// first packet of a probe could otherwise run into.
	err = far.do(func() error {
		for _, scope := range []string{"all", "default"} {
			if err := os.WriteFile("/proc/sys/net/ipv6/conf/"+scope+"/accept_dad", []byte("0"), 0o644); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return far
}

func (f *farNS) pid() int { return f.cmd.Process.Pid }

// do runs fn on a thread that has entered the far namespace. Sockets made there
// stay there, and so does a process started from there.
func (f *farNS) do(fn func() error) error {
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		home, err := os.Open("/proc/thread-self/ns/net")
		if err != nil {
			done <- err
			runtime.UnlockOSThread()
			return
		}
		defer home.Close()
		if err := unix.Setns(int(f.file.Fd()), unix.CLONE_NEWNET); err != nil {
			done <- fmt.Errorf("enter the far namespace: %w", err)
			runtime.UnlockOSThread()
			return
		}
		fnErr := fn()
		if err := unix.Setns(int(home.Fd()), unix.CLONE_NEWNET); err != nil {
			// The thread is stuck in the wrong namespace: it stays locked to this
			// goroutine, so that it ends with it.
			done <- fmt.Errorf("leave the far namespace: %w", err)
			return
		}
		runtime.UnlockOSThread()
		done <- fnErr
	}()
	return <-done
}

// ip runs ip(8) in the far namespace.
func (f *farNS) ip(args ...string) {
	f.t.Helper()
	err := f.do(func() error { return tryIP(args...) })
	if err != nil {
		f.t.Fatal(err)
	}
}

// hold gives the far namespace an address of its own, so that what is sent
// there is delivered.
func (f *farNS) hold(addr netip.Addr) {
	f.t.Helper()
	if f.held[addr] {
		return
	}
	f.held[addr] = true
	f.ip("addr", "add", netip.PrefixFrom(addr, addr.BitLen()).String(), "dev", "lo")
}

// sink listens for UDP datagrams on a port, whatever the address.
func (f *farNS) sink(port int) *sink {
	f.t.Helper()
	s := &sink{arrived: make(chan struct{})}
	err := f.do(func() (err error) {
		s.conn, err = net.ListenPacket("udp", ":"+strconv.Itoa(port))
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	go func() {
		buf := make([]byte, 64)
		if _, _, err := s.conn.ReadFrom(buf); err == nil {
			close(s.arrived)
		}
	}()
	return s
}

type sink struct {
	conn    net.PacketConn
	arrived chan struct{}
}

// seenPacket is a datagram or a connection attempt that a tunnel device took.
type seenPacket struct {
	where string
	dst   netip.Addr
	dport uint16
}

// labTun is a tun device held open the way an engine holds its own, so that it
// has carrier. Everything that is sent into it is recorded.
type labTun struct {
	name  string
	file  *os.File
	index uint32
}

func (l *lab) tun(name string, addrs ...string) *labTun {
	t := l.t
	t.Helper()
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatalf("open /dev/net/tun: %v", err)
	}
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		unix.Close(fd)
		t.Fatal(err)
	}
	ifr.SetUint16(unix.IFF_TUN | unix.IFF_NO_PI)
	if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, ifr); err != nil {
		unix.Close(fd)
		t.Fatalf("create tun device %s: %v", name, err)
	}
	tn := &labTun{name: name, file: os.NewFile(uintptr(fd), name)}
	t.Cleanup(func() { tn.file.Close() })
	runIP(t, "link", "set", name, "up")
	for _, a := range addrs {
		args := append([]string{"addr", "add"}, strings.Fields(a)...)
		if strings.Contains(a, ":") {
			args = append(args, "nodad")
		}
		runIP(t, append(args, "dev", name)...)
	}
	tn.index = ifIndexOf(t, name)
	go tn.sniff(l.packets)
	return tn
}

// destroy closes the device as a process that dies does: the interface and its
// routes go with it.
func (tn *labTun) destroy() { tn.file.Close() }

func (tn *labTun) sniff(out chan<- seenPacket) {
	buf := make([]byte, 1<<16)
	for {
		n, err := tn.file.Read(buf)
		if err != nil {
			if errors.Is(err, syscall.EAGAIN) {
				continue
			}
			return
		}
		if dst, dport, ok := parseTransport(buf[:n]); ok {
			select {
			case out <- seenPacket{tn.name, dst, dport}:
			default:
			}
		}
	}
}

// parseTransport reads the destination address and port of a UDP or TCP packet.
func parseTransport(b []byte) (dst netip.Addr, dport uint16, ok bool) {
	var proto byte
	var port []byte
	switch {
	case len(b) >= 20 && b[0]>>4 == 4:
		ihl := int(b[0]&0xf) * 4
		if len(b) < ihl+4 {
			return netip.Addr{}, 0, false
		}
		dst, _ = netip.AddrFromSlice(b[16:20])
		proto, port = b[9], b[ihl+2:ihl+4]
	case len(b) >= 44 && b[0]>>4 == 6:
		dst, _ = netip.AddrFromSlice(b[24:40])
		proto, port = b[6], b[42:44]
	default:
		return netip.Addr{}, 0, false
	}
	return dst, binary.BigEndian.Uint16(port), proto == syscall.IPPROTO_UDP || proto == syscall.IPPROTO_TCP
}

// lab is the machine: the uplink ens18 on 192.168.50.0/24 and 2001:db8:50::/64,
// behind the routers 192.168.50.1 and fe80::1 that the default routes lead to,
// as a DHCP client and the router advertisements of a home network leave them.
type lab struct {
	t       *testing.T
	far     *farNS
	dns     *fake.DNS
	logs    *syncBuffer
	journal string
	packets chan seenPacket
}

// probePort numbers the ports of the probes, across tests: a connection attempt
// that a test left behind keeps sending for a while.
var probePort atomic.Int32

const (
	uplinkName = "ens18"
	routerV4   = "192.168.50.1"
	routerV6   = "fe80::1"
)

func newLab(t *testing.T) *lab {
	t.Helper()
	requireNetns(t)
	l := &lab{
		t:       t,
		far:     newFarNS(t),
		dns:     fake.NewDNS(),
		logs:    &syncBuffer{},
		journal: t.TempDir() + "/state/journal",
		packets: make(chan seenPacket, 1024),
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("daemon log:\n%s", l.logs)
			t.Logf("ip rule:\n%s\nip route:\n%s\nip -6 route:\n%s", runIP(t, "rule"), runIP(t, "route"), runIP(t, "-6", "route"))
		}
	})
	// A link-local address is usable at once, not after duplicate address detection.
	for _, scope := range []string{"all", "default"} {
		if err := os.WriteFile("/proc/sys/net/ipv6/conf/"+scope+"/accept_dad", []byte("0"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	l.addUplink(uplinkName, "far0", "192.168.50", "2001:db8:50::")
	runIP(t, "route", "add", "default", "via", routerV4, "dev", uplinkName, "proto", "dhcp", "metric", "100")
	runIP(t, "-6", "route", "add", "default", "via", routerV6, "dev", uplinkName, "proto", "ra", "metric", "100")
	return l
}

// addUplink makes a physical-looking interface: name on v4 (".2/24"; the router
// is ".1") and on v6 ("2::" prefix of a /64), joined to the far namespace by far.
// The router also answers as fe80::1 on the link.
func (l *lab) addUplink(name, far, v4net, v6net string) {
	t := l.t
	t.Helper()
	runIP(t, "link", "add", name, "type", "veth", "peer", "name", far)
	runIP(t, "link", "set", far, "netns", strconv.Itoa(l.far.pid()))
	l.far.ip("link", "set", far, "up")
	l.far.ip("addr", "add", v4net+".1/24", "dev", far)
	l.far.ip("-6", "addr", "add", "fe80::1/64", "dev", far, "nodad")
	l.far.ip("-6", "addr", "add", v6net+"1/64", "dev", far, "nodad")
	runIP(t, "link", "set", name, "up")
	runIP(t, "addr", "add", v4net+".2/24", "dev", name)
	runIP(t, "-6", "addr", "add", v6net+"2/64", "dev", name, "nodad")
	eventually(t, "the link-local address of "+name, 5*time.Second, func() bool {
		return strings.Contains(runIP(t, "-6", "addr", "show", "dev", name, "scope", "link"), "fe80::")
	})
}

// warp reproduces the layout of a wg-quick full tunnel that routes with policy
// rules instead of default routes in the main table (Cloudflare WARP): the
// default route of the main table is suppressed for traffic without the mark,
// the marked traffic of the tunnel itself uses the main table, and the table of
// the tunnel holds nothing but the default route through it. Besides, the main
// table has a static route of a home network behind the router. kind is the type
// of the tunnel device: "wireguard", or "tun" when the test wants to see what is
// sent into it.
func (l *lab) warp(kind string) *labTun {
	t := l.t
	t.Helper()
	var tn *labTun
	switch kind {
	case "wireguard":
		runIP(t, "link", "add", "wgcf", "type", "wireguard")
		runIP(t, "link", "set", "wgcf", "mtu", "1280", "up")
		runIP(t, "addr", "add", "172.16.0.2/32", "dev", "wgcf")
		runIP(t, "-6", "addr", "add", "2606:4700:110:8a36::2/128", "dev", "wgcf", "nodad")
	case "tun":
		tn = l.tun("wgcf", "172.16.0.2/32", "2606:4700:110:8a36::2/128")
	default:
		t.Fatalf("unknown kind %q", kind)
	}
	runIP(t, "route", "add", "10.6.0.0/24", "via", routerV4, "proto", "static")
	for _, family := range []string{"-4", "-6"} {
		runIP(t, family, "rule", "add", "pref", "32764", "table", "main", "suppress_prefixlength", "0")
		runIP(t, family, "rule", "add", "pref", "32765", "not", "fwmark", "0xca6c", "table", "51820")
		runIP(t, family, "route", "add", "default", "dev", "wgcf", "table", "51820")
	}
	return tn
}

// probe sends a datagram to dst and says where it left the machine: the name of
// the tunnel device that took it, "uplink" when it arrived in the far namespace,
// "unreachable" when the kernel had no route, or "none".
//
// The first packet to a router that the kernel has not talked to yet is rarely
// lost in the neighbour discovery of the veth pair (about one in a hundred on a
// loaded machine), whatever the routes are: a datagram that does not arrive is
// sent once more.
func (l *lab) probe(dst string) string {
	l.t.Helper()
	got := l.probeOnce(dst)
	if got == "none" {
		got = l.probeOnce(dst)
	}
	return got
}

func (l *lab) probeOnce(dst string) string {
	t := l.t
	t.Helper()
	addr := netip.MustParseAddr(dst)
	l.far.hold(addr)
	port := int(probePort.Add(1)) + 40000
	s := l.far.sink(port)
	defer s.conn.Close()
	for len(l.packets) > 0 {
		<-l.packets
	}
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: addr.AsSlice(), Port: port})
	if err != nil {
		return "unreachable"
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("plaitway")); err != nil {
		return "unreachable"
	}
	timeout := time.After(1500 * time.Millisecond)
	for {
		select {
		case p := <-l.packets:
			if p.dst == addr && int(p.dport) == port {
				return p.where
			}
		case <-s.arrived:
			return "uplink"
		case <-timeout:
			return "none"
		}
	}
}

// probeTCP is probe for a connection: "uplink" when the far namespace accepted
// it, so that the answer found its way back as well, or the tunnel device that
// took the first packet.
func (l *lab) probeTCP(dst string) string {
	t := l.t
	t.Helper()
	addr := netip.MustParseAddr(dst)
	l.far.hold(addr)
	port := int(probePort.Add(1)) + 40000
	var ln net.Listener
	err := l.far.do(func() (err error) {
		ln, err = net.Listen("tcp", ":"+strconv.Itoa(port))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan struct{})
	go func() {
		if c, err := ln.Accept(); err == nil {
			c.Close()
			close(accepted)
		}
	}()
	for len(l.packets) > 0 {
		<-l.packets
	}
	go func() {
		if c, err := net.DialTimeout("tcp", net.JoinHostPort(dst, strconv.Itoa(port)), 1500*time.Millisecond); err == nil {
			c.Close()
		}
	}()
	timeout := time.After(1500 * time.Millisecond)
	for {
		select {
		case p := <-l.packets:
			if p.dst == addr && int(p.dport) == port {
				return p.where
			}
		case <-accepted:
			return "uplink"
		case <-timeout:
			return "none"
		}
	}
}

// expectProbe fails the test unless the datagram to dst leaves through where.
func (l *lab) expectProbe(dst, where string) {
	l.t.Helper()
	if got := l.probe(dst); got != where {
		l.t.Errorf("a datagram to %s left through %s, want %s", dst, got, where)
	}
}

// decision is what the kernel would do with a packet to an address.
type decision struct{ Dev, Via string }

func (d decision) String() string {
	if d.Via != "" {
		return "via " + d.Via + " dev " + d.Dev
	}
	return "dev " + d.Dev
}

// routeGet asks the kernel, rules included, where a packet to dst goes. Extra
// arguments are options of ip route get, such as "mark", "0xca6c".
func (l *lab) routeGet(dst string, extra ...string) decision {
	l.t.Helper()
	family := "-4"
	if strings.Contains(dst, ":") {
		family = "-6"
	}
	out, err := exec.Command("ip", append([]string{"-j", family, "route", "get", dst}, extra...)...).Output()
	if err != nil {
		return decision{Dev: "unreachable"}
	}
	var got []struct{ Gateway, Dev string }
	if err := json.Unmarshal(out, &got); err != nil || len(got) != 1 {
		l.t.Fatalf("ip route get %s: %v: %s", dst, err, out)
	}
	return decision{Dev: got[0].Dev, Via: got[0].Gateway}
}

func (l *lab) expectRoute(dst string, want decision, extra ...string) {
	l.t.Helper()
	if got := l.routeGet(dst, extra...); got != want {
		l.t.Errorf("ip route get %s %s: %v, want %v", dst, strings.Join(extra, " "), got, want)
	}
}

// reconciler starts a Reconciler as the Linux daemon does: the real routing
// table and network monitor, the Linux keying, and a recording stand-in for
// systemd-resolved, which the namespace cannot reach. Its timers are slow unless
// a test asks for faster ones.
func (l *lab) reconciler(opts ...func(*Config)) *Reconciler {
	l.t.Helper()
	log := slog.New(slog.NewTextHandler(l.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := Config{
		Routes:      linux.NewRouteTable(),
		DNS:         l.dns,
		Net:         linux.NewNetMonitor(linux.NetMonitorOptions{Logger: log}),
		JournalPath: l.journal,
		Keying:      KeyLinux,
		Log:         log,
		WakeDelay:   time.Hour,
		RetryDelay:  time.Hour,
	}
	for _, o := range opts {
		o(&cfg)
	}
	r, err := New(cfg)
	if err != nil {
		l.t.Fatalf("New: %v", err)
	}
	l.t.Cleanup(func() { r.journal.close() })
	return r
}

// run starts Run, waits until it listens to the kernel, and returns what ends it.
func (l *lab) run(r *Reconciler) (stop func() error) {
	l.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	var once sync.Once
	var runErr error
	stop = func() error {
		once.Do(func() {
			cancel()
			runErr = <-done
		})
		return runErr
	}
	l.t.Cleanup(func() { stop() })
	eventually(l.t, "Run to start", 5*time.Second, func() bool { return r.Report().LastChange.Reason == osnet.ChangeManual })
	// The monitor opens its socket in the background, and a change made before is
	// not seen: change a route until the Reconciler says it has seen one.
	l.waitForEvents(r)
	return stop
}

func (l *lab) waitForEvents(r *Reconciler) {
	l.t.Helper()
	before := r.Report().LastChange.At
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		runIP(l.t, "route", "add", "198.51.100.250/32", "dev", uplinkName)
		runIP(l.t, "route", "del", "198.51.100.250/32", "dev", uplinkName)
		time.Sleep(500 * time.Millisecond)
		if r.Report().LastChange.At.After(before) {
			return
		}
	}
	l.t.Fatal("the Reconciler never saw a change of the network")
}

// protocol is the rtm_protocol of a route as the table reports it.
func protocol(rt osnet.Route) uint32 { return rt.Flags >> 8 & 0xff }

// ipLine is a route as ip route shows it, with the metric.
func ipLine(rt osnet.Route) string {
	line := rt.Dst.String()
	if rt.Gateway.IsValid() {
		line += " via " + rt.Gateway.WithZone("").String()
	}
	return fmt.Sprintf("%s dev %s metric %d", line, rt.Iface, rt.Metric)
}

func (l *lab) table() []osnet.Route {
	l.t.Helper()
	routes, err := linux.NewRouteTable().Dump()
	if err != nil {
		l.t.Fatal(err)
	}
	return routes
}

// ours lists the routes of the main table that carry the protocol Plaitway adds
// them with.
func (l *lab) ours() []string {
	l.t.Helper()
	var out []string
	for _, rt := range l.table() {
		if protocol(rt) == linux.RouteProtocol {
			out = append(out, ipLine(rt))
		}
	}
	slices.Sort(out)
	return out
}

func (l *lab) expectOurs(want ...string) {
	l.t.Helper()
	slices.Sort(want)
	if got := l.ours(); !slices.Equal(got, want) {
		l.t.Errorf("routes with protocol %d\n got:\n  %s\nwant:\n  %s", linux.RouteProtocol, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// route returns the route to dst with this metric.
func (l *lab) route(dst string, metric uint32) (osnet.Route, bool) {
	l.t.Helper()
	for _, rt := range l.table() {
		if rt.Dst == pfx(dst) && rt.Metric == metric {
			return rt, true
		}
	}
	return osnet.Route{}, false
}

func announce(t *testing.T, r *Reconciler, in tunnel.Intent) {
	t.Helper()
	if err := r.Announce(in); err != nil {
		t.Fatalf("Announce(%s): %v", in.Owner, err)
	}
}

func reportOf(t *testing.T, r *Reconciler, dst string, owner tunnel.OwnerID) tunnel.RouteReport {
	t.Helper()
	for _, rr := range r.Report().Routes {
		if rr.Prefix == pfx(dst) && rr.Owner == owner {
			return rr
		}
	}
	t.Fatalf("no report for %s of %s in %+v", dst, owner, r.Report().Routes)
	return tunnel.RouteReport{}
}

func expectInstalled(t *testing.T, r *Reconciler, owner tunnel.OwnerID, dsts ...string) {
	t.Helper()
	for _, dst := range dsts {
		if rr := reportOf(t, r, dst, owner); rr.State != tunnel.RouteInstalled || rr.Overridden || rr.Detail != "" {
			t.Errorf("%s of %s: %+v, want installed", dst, owner, rr)
		}
	}
}

// eventuallyInstalled waits until the report, which a pass publishes when it
// ends, says that the routes are installed. The table has them a little earlier.
func eventuallyInstalled(t *testing.T, r *Reconciler, owner tunnel.OwnerID, dsts ...string) {
	t.Helper()
	eventually(t, "the report to say that the routes are installed", 3*time.Second, func() bool {
		for _, dst := range dsts {
			if rr := reportOf(t, r, dst, owner); rr.State != tunnel.RouteInstalled || rr.Overridden {
				return false
			}
		}
		return true
	})
	expectInstalled(t, r, owner, dsts...)
}

// unresolvedRoutes are the routes the journal still counts as installed.
func unresolvedRoutes(r *Reconciler) []record {
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	var out []record
	for _, rec := range r.journal.unresolved() {
		if rec.Kind == kindRoute {
			out = append(out, rec)
		}
	}
	return out
}

// ownsRoute says whether the Reconciler considers a route to dst its own.
func ownsRoute(r *Reconciler, dst string) bool {
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	for key := range r.owned {
		if key.dst == pfx(dst) {
			return true
		}
	}
	return false
}
