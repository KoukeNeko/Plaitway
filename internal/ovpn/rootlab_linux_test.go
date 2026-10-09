//go:build rootintegration && linux

package ovpn

// The Linux root tests change the links, addresses and routes of the network
// namespace they run in, so they refuse to run anywhere but in a private one:
// they need the rootintegration build tag, PLAITWAY_ROOT_TESTS=1, root, and a
// namespace with no interface but lo. A private user and network namespace
// gives all of it without privileges and leaves the host alone:
//
//	go test -c -tags rootintegration -o ovpn.test ./internal/ovpn
//	unshare -Urn sh -c 'ip link set lo up; PLAITWAY_ROOT_TESTS=1 ./ovpn.test -test.v -test.run Root'
//
// ip(8) from iproute2, nsenter(1) and ping(8) are used to set up and to check.
//
// The lab is two network namespaces joined by a veth pair:
//
//	this namespace (the client)                the server's namespace
//	  vethc 10.99.0.1/24, fd99::1/64   <---->   veths 10.99.0.2/24, fd99::2/64
//	  default route via 10.99.0.2                lo: 198.51.100.1  the server's address
//	                                                 10.20.0.5, fd00:20::5
//	                                                               hosts behind the server
//	                                                 203.0.113.50, 2001:db8:200::50
//	                                                               hosts "on the internet"
//
// The client's default route leads to the server's namespace, so a tunnel has
// something to be kept out of by the bypass route of its server, and anything
// the server's namespace owns is reachable with or without the tunnel; the
// tests tell the two apart by the route the kernel picks and the counters of
// the tunnel interface. The server is a real openvpn in the other namespace.

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	labClientV4, labServerV4 = "10.99.0.1", "10.99.0.2"
	labClientV6, labServerV6 = "fd99::1", "fd99::2"
	// labServerPublic is the address of the openvpn server, the remote of the
	// profiles: a documentation address that only the server's namespace owns, so
	// that the client reaches it through its default route.
	labServerPublic = "198.51.100.1"
	// labServerPublicV6 is the same for a server that is reached over IPv6.
	labServerPublicV6 = "2001:db8:100::1"
	// The server's tunnel is 10.8.0.0/24 and fd00:8::/64. These addresses are
	// the hosts behind it.
	labBehindV4, labBehindV6     = "10.20.0.5", "fd00:20::5"
	labInternetV4, labInternetV6 = "203.0.113.50", "2001:db8:200::50"
)

var (
	netnsOnce sync.Once
	netnsErr  error
)

// requirePrivateNetns skips unless the tests are asked for, and refuses to go
// on anywhere but in a namespace that has nothing in it but loopback.
func requirePrivateNetns(t *testing.T) {
	t.Helper()
	if os.Getenv("PLAITWAY_ROOT_TESTS") != "1" || os.Geteuid() != 0 {
		t.Skip("set PLAITWAY_ROOT_TESTS=1 and run as root in a private network namespace to change the network")
	}
	netnsOnce.Do(func() {
		ifaces, err := net.Interfaces()
		if err != nil {
			netnsErr = err
			return
		}
		for _, iface := range ifaces {
			if iface.Flags&net.FlagLoopback == 0 && !slices.Contains(fallbackDevices, iface.Name) {
				netnsErr = fmt.Errorf("refusing to run: the namespace has an interface %s besides lo; this is not a private network namespace", iface.Name)
				return
			}
		}
	})
	if netnsErr != nil {
		t.Fatal(netnsErr)
	}
}

// fallbackDevices are the devices that a tunnel module makes in every network
// namespace when it is loaded, and that cannot be deleted.
var fallbackDevices = []string{"tunl0", "sit0", "gre0", "gretap0", "erspan0", "ip6tnl0", "ip6gre0"}

// ipIn runs ip(8) in the namespace of pid, or in this one when pid is 0, and
// fails the test when it fails.
func ipIn(t *testing.T, pid int, args ...string) string {
	t.Helper()
	cmd := exec.Command("ip", args...)
	if pid != 0 {
		cmd = inNetns(pid, "ip", args...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ip %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func runIP(t *testing.T, args ...string) string {
	t.Helper()
	return ipIn(t, 0, args...)
}

// inNetns is a command that runs in the network namespace of pid.
func inNetns(pid int, name string, args ...string) *exec.Cmd {
	return exec.Command("nsenter", append([]string{"-t", strconv.Itoa(pid), "-n", "--", name}, args...)...)
}

// startTied starts cmd so that the kernel ends it with the test: it is killed
// when the test's goroutine ends, and with the process, whatever happens to it.
// A test that dies must not leave an openvpn server or a namespace behind.
func startTied(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	prepareCommand(cmd, "") // locks the test's goroutine to its thread: see there
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
}

// stopTied ends a process started with startTied.
func stopTied(cmd *exec.Cmd) {
	cmd.Process.Kill()
	cmd.Wait()
}

// netns is a network namespace of its own, held by a process that does nothing.
type netns struct {
	t      *testing.T
	holder *exec.Cmd
	pid    int
}

func newNetns(t *testing.T) *netns {
	t.Helper()
	cmd := exec.Command("sleep", "3600")
	prepareCommand(cmd, "")
	cmd.SysProcAttr.Cloneflags = syscall.CLONE_NEWNET
	if err := cmd.Start(); err != nil {
		t.Fatalf("start a network namespace: %v", err)
	}
	t.Cleanup(func() { stopTied(cmd) })
	return &netns{t: t, holder: cmd, pid: cmd.Process.Pid}
}

func (n *netns) ip(args ...string) string {
	n.t.Helper()
	return ipIn(n.t, n.pid, args...)
}

// lab is the two namespaces and what runs in them.
type lab struct {
	t      *testing.T
	bin    string
	pki    *pki
	server *netns
	dir    string
	remote string // the address of the server the profiles name
}

func newLab(t *testing.T) *lab {
	t.Helper()
	requirePrivateNetns(t)
	l := &lab{t: t, bin: realBinary(t), pki: newPKI(t), dir: shortTempDir(t), remote: labServerPublic}
	l.server = newNetns(t)

	runIP(t, "link", "set", "lo", "up")
	runIP(t, "link", "add", "vethc", "type", "veth", "peer", "name", "veths")
	t.Cleanup(func() {
		exec.Command("ip", "link", "del", "vethc").Run()
		exec.Command("ip", "route", "flush", "proto", "199").Run()
	})
	runIP(t, "link", "set", "veths", "netns", strconv.Itoa(l.server.pid))
	runIP(t, "addr", "add", labClientV4+"/24", "dev", "vethc")
	runIP(t, "addr", "add", labClientV6+"/64", "dev", "vethc", "nodad")
	runIP(t, "link", "set", "vethc", "up")
	runIP(t, "route", "add", "default", "via", labServerV4)
	runIP(t, "-6", "route", "add", "default", "via", labServerV6, "dev", "vethc")

	s := l.server
	s.ip("link", "set", "lo", "up")
	s.ip("addr", "add", labServerV4+"/24", "dev", "veths")
	s.ip("addr", "add", labServerV6+"/64", "dev", "veths", "nodad")
	s.ip("link", "set", "veths", "up")
	for _, addr := range []string{labServerPublic, labBehindV4, labInternetV4} {
		s.ip("addr", "add", addr+"/32", "dev", "lo")
	}
	for _, addr := range []string{labBehindV6, labInternetV6, labServerPublicV6} {
		s.ip("addr", "add", addr+"/128", "dev", "lo", "nodad")
	}
	return l
}

// labServerOpts describe the openvpn server of a lab.
type labServerOpts struct {
	proto    string // "udp" or "tcp"; udp when empty
	topology string // "subnet" or "net30"; subnet when empty
	ipv6     bool   // the tunnel has fd00:8::/64 as well
	// pushes are "--push" values.
	pushes []string
	// password, when set, makes the server demand user "alice" with it, through
	// an auth-user-pass-verify script of the server's own.
	password string
	// network is the server's tunnel network, a /24; 10.8.0.0 when empty.
	network string
	// transportV6 makes the server listen on, and the profiles name, its IPv6
	// address.
	transportV6 bool
	// extra are more options for the server.
	extra []string
	// dev is the server's "--dev"; tun when empty.
	dev string
}

type labServer struct {
	l   *lab
	cmd *exec.Cmd
	log string
}

func (l *lab) startServer(o labServerOpts) *labServer {
	t := l.t
	t.Helper()
	files := map[string]string{"ca.crt": l.pki.CA, "server.crt": l.pki.ServerCert, "server.key": l.pki.ServerKey}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(l.dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	proto, local := "udp", labServerPublic
	switch {
	case o.proto == "tcp" && o.transportV6:
		proto = "tcp6-server"
	case o.proto == "tcp":
		proto = "tcp-server"
	case o.transportV6:
		proto = "udp6"
	}
	if o.transportV6 {
		local = labServerPublicV6
		l.remote = labServerPublicV6
	}
	topology := o.topology
	if topology == "" {
		topology = "subnet"
	}
	dev := o.dev
	if dev == "" {
		dev = "tun"
	}
	args := []string{
		"--server", cmp.Or(o.network, "10.8.0.0"), "255.255.255.0", "--topology", topology, "--dev", dev, "--disable-dco",
		"--local", local, "--port", "1194", "--proto", proto,
		"--ca", filepath.Join(l.dir, "ca.crt"), "--cert", filepath.Join(l.dir, "server.crt"), "--key", filepath.Join(l.dir, "server.key"),
		"--dh", "none", "--keepalive", "10", "30", "--verb", "3", "--cd", l.dir,
		"--data-ciphers", "AES-256-GCM:AES-128-GCM",
	}
	if o.ipv6 {
		args = append(args, "--server-ipv6", "fd00:8::/64")
	}
	for _, push := range o.pushes {
		args = append(args, "--push", push)
	}
	if o.password != "" {
		script := writeStub(t, l.dir, "auth", stubBehavior{VerifyUser: "alice", VerifyPassword: o.password})
		// The server may run a script; the engine under test never does.
		args = append(args, "--script-security", "2", "--auth-user-pass-verify", script, "via-file")
	}
	args = append(args, o.extra...)

	srv := &labServer{l: l, log: filepath.Join(l.dir, "server.log")}
	srv.cmd = inNetns(l.server.pid, l.bin, args...)
	logFile, err := os.Create(srv.log)
	if err != nil {
		t.Fatal(err)
	}
	srv.cmd.Stdout, srv.cmd.Stderr = logFile, logFile
	startTied(t, srv.cmd)
	t.Cleanup(func() {
		stopTied(srv.cmd)
		logFile.Close()
		if t.Failed() {
			t.Logf("server log:\n%s", srv.readLog())
		}
	})
	for deadline := time.Now().Add(15 * time.Second); !strings.Contains(srv.readLog(), "Initialization Sequence Completed"); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the server did not start:\n%s", srv.readLog())
		}
	}
	return srv
}

func (s *labServer) readLog() string {
	data, _ := os.ReadFile(s.log)
	return string(data)
}

// profile is a client profile for the lab's server: the PKI of the lab and the
// server's address. extra is more lines of the profile.
func (l *lab) profile(proto string, extra ...string) string {
	return l.profileFor("tun", proto, extra...)
}

// profileFor is profile for another "dev".
func (l *lab) profileFor(dev, proto string, extra ...string) string {
	transport := "udp"
	if proto == "tcp" {
		transport = "tcp-client"
	}
	return fmt.Sprintf("client\ndev %s\nproto %s\nremote %s 1194\nnobind\nremote-cert-tls server\n%s\n<ca>\n%s</ca>\n<cert>\n%s</cert>\n<key>\n%s</key>\n",
		dev, transport, l.remote, strings.Join(extra, "\n"), l.pki.CA, l.pki.ClientCert, l.pki.ClientKey)
}

// Behind the server there can be a host that only the server can reach, which
// the server forwards to: a third namespace, joined to the server's by another
// veth pair.
const (
	labHostServerV4, labHostV4 = "10.30.0.1", "10.30.0.5"
	labHostNet                 = "10.30.0.0/24"
)

func (l *lab) addHostBehindServer() {
	t := l.t
	t.Helper()
	host := newNetns(t)
	s := l.server
	s.ip("link", "add", "vethf", "type", "veth", "peer", "name", "vethg")
	s.ip("link", "set", "vethg", "netns", strconv.Itoa(host.pid))
	s.ip("addr", "add", labHostServerV4+"/24", "dev", "vethf")
	s.ip("link", "set", "vethf", "up")
	host.ip("link", "set", "lo", "up")
	host.ip("addr", "add", labHostV4+"/24", "dev", "vethg")
	host.ip("link", "set", "vethg", "up")
	host.ip("route", "add", "default", "via", labHostServerV4)
	if out, err := inNetns(s.pid, "sysctl", "-w", "net.ipv4.ip_forward=1").CombinedOutput(); err != nil {
		t.Fatalf("let the server forward: %v: %s", err, out)
	}
}

// ping sends three echo requests to addr from this namespace and fails the
// test unless every one is answered.
func ping(t *testing.T, addr string) {
	t.Helper()
	args := []string{"-c", "3", "-i", "0.2", "-W", "2", addr}
	if strings.Contains(addr, ":") {
		args = append([]string{"-6"}, args...)
	}
	if out, err := exec.Command("ping", args...).CombinedOutput(); err != nil {
		t.Fatalf("ping %s: %v\n%s", addr, err, out)
	}
}

// linkJSON is what "ip -j -s link show" says about an interface.
type linkJSON struct {
	Stats64 struct {
		Rx struct{ Packets uint64 } `json:"rx"`
		Tx struct{ Packets uint64 } `json:"tx"`
	} `json:"stats64"`
}

// linkStats returns the packet counters of an interface.
func linkStats(t *testing.T, name string) (rx, tx uint64) {
	t.Helper()
	var links []linkJSON
	if err := json.Unmarshal([]byte(runIP(t, "-j", "-s", "link", "show", "dev", name)), &links); err != nil || len(links) != 1 {
		t.Fatalf("ip link show %s: %v, %d links", name, err, len(links))
	}
	return links[0].Stats64.Rx.Packets, links[0].Stats64.Tx.Packets
}

// routeDev is the interface "ip route get" says the kernel sends traffic to dst
// through.
func routeDev(t *testing.T, dst string) string {
	t.Helper()
	args := []string{"route", "get", dst}
	if strings.Contains(dst, ":") {
		args = append([]string{"-6"}, args...)
	}
	fields := strings.Fields(runIP(t, args...))
	if i := slices.Index(fields, "dev"); i >= 0 && i+1 < len(fields) {
		return fields[i+1]
	}
	t.Fatalf("ip route get %s: no device in %q", dst, fields)
	return ""
}

// plaitwayRoutes are the routes of the main table that carry Plaitway's
// protocol number, as ip(8) shows them, one string per route without the
// attributes that do not matter here: "10.20.0.0/16 dev tun0 metric 5".
func plaitwayRoutes(t *testing.T) []string {
	t.Helper()
	var routes []string
	for _, family := range []string{"-4", "-6"} {
		for _, line := range strings.Split(strings.TrimSpace(runIP(t, "-o", family, "route", "show", "proto", "199")), "\n") {
			if line == "" {
				continue
			}
			// "dst [via gw] dev X [proto 199] [scope link] metric N [pref medium]"
			var kept []string
			for fields := strings.Fields(line); len(fields) > 0; fields = fields[1:] {
				if slices.Contains([]string{"proto", "scope", "pref"}, fields[0]) && len(fields) > 1 {
					fields = fields[1:] // the word and its value
					continue
				}
				kept = append(kept, fields[0])
			}
			routes = append(routes, strings.Join(kept, " "))
		}
	}
	slices.Sort(routes)
	return routes
}
