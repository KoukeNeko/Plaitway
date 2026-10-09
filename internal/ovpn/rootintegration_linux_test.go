//go:build rootintegration && linux

package ovpn

// The engine against a real openvpn server, with the real Reconciler on the real
// Linux routing table as its Network: the whole path of a tunnel, from the
// profile to the traffic that crosses it. See rootlab_linux_test.go for the
// namespaces these run in and how to run them.
//
// DNS is the one thing that is not real. systemd-resolved is reached over a
// socket in the file system, which no network namespace separates, so the real
// adapter would change the settings of the host's own link with the same index.
// The Reconciler gets an in-memory configurator and the tests look at what it
// was asked to apply.

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/osnet/fake"
	"github.com/KoukeNeko/Plaitway/internal/osnet/linux"
	"github.com/KoukeNeko/Plaitway/internal/reconciler"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// labNetwork is the Reconciler of a lab, with the DNS it was given.
type labNetwork struct {
	*reconciler.Reconciler
	dns *fake.DNS
}

// startReconciler runs a Reconciler on the real routing table and the real
// network monitor, and stops it, removing what it owns, when the test ends.
func (l *lab) startReconciler() *labNetwork {
	t := l.t
	t.Helper()
	dns := fake.NewDNS()
	r, err := reconciler.New(reconciler.Config{
		Routes:      linux.NewRouteTable(),
		DNS:         dns,
		Net:         linux.NewNetMonitor(linux.NetMonitorOptions{Logger: slog.New(slog.NewTextHandler(testLogWriter{t}, nil))}),
		JournalPath: filepath.Join(l.dir, "journal", "journal"),
		Keying:      reconciler.KeyLinux,
		Log:         slog.New(slog.NewTextHandler(testLogWriter{t}, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("the Reconciler did not stop cleanly: %v", err)
		}
	})
	return &labNetwork{Reconciler: r, dns: dns}
}

// engine creates the engine of a profile against the real openvpn and the
// Reconciler. The harness records what the engine tells the Reconciler and
// stops the engine when the test ends.
func (l *lab) engine(profile string, spec tunnel.Spec, network *labNetwork, mods ...func(*engine)) *harness {
	l.t.Helper()
	if spec.Owner == "" {
		spec.Owner = "lab"
	}
	h := newHarness(l.t, harnessOpts{binary: l.bin, profile: profile, spec: spec, network: network, cfgMod: func(e *engine) {
		for _, mod := range mods {
			mod(e)
		}
	}})
	// The Reconciler can only bind routes to an interface that exists.
	h.net.announceErr = func(_ int, it tunnel.Intent) error {
		if it.State != tunnel.StateUp {
			return nil
		}
		if _, err := net.InterfaceByName(it.Iface); err != nil {
			l.t.Errorf("the engine announced the tunnel %q before the interface existed: %v", it.Iface, err)
		}
		return nil
	}
	return h
}

// routesOf are the Reconciler's reports of the routes of an owner.
func (n *labNetwork) routesOf(owner tunnel.OwnerID) []tunnel.RouteReport {
	var out []tunnel.RouteReport
	for _, rr := range n.Report().Routes {
		if rr.Owner == owner {
			out = append(out, rr)
		}
	}
	return out
}

// announcedUp is the last tunnel statement the engine made.
func announcedUp(h *harness) tunnel.Intent {
	h.t.Helper()
	var got tunnel.Intent
	for _, c := range h.net.snapshot() {
		if c.intent.State == tunnel.StateUp {
			got = c.intent
		}
	}
	if got.Owner == "" {
		h.t.Fatal("the engine never announced a tunnel")
	}
	return got
}

// interfaceGone fails unless the interface is gone within a few seconds.
func requireInterfaceGone(t *testing.T, name string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := net.InterfaceByName(name); err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("interface %s still exists", name)
		}
	}
}

// addressesOf are the addresses of an interface, as the kernel has them.
func addressesOf(t *testing.T, name string) []netip.Prefix {
	t.Helper()
	iface, err := net.InterfaceByName(name)
	if err != nil {
		t.Fatalf("interface %s: %v", name, err)
	}
	addrs, err := iface.Addrs()
	if err != nil {
		t.Fatal(err)
	}
	var out []netip.Prefix
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok {
			prefix, _ := netip.ParsePrefix(ipnet.String())
			out = append(out, prefix)
		}
	}
	return out
}

func containsPrefix(list []netip.Prefix, want string) bool {
	for _, p := range list {
		if p == netip.MustParsePrefix(want) {
			return true
		}
	}
	return false
}

// The split tunnel in each shape a server can give it: the transport, the
// topology and the address families. Everything the engine learns from the
// up event of a real openvpn on a real tun device is checked against the
// kernel: the interface and its addresses, the routes the Reconciler put in
// the table, the traffic through them, the reconnect that keeps all of it, and
// the stop that leaves nothing.
func TestRootLinuxSplitTunnel(t *testing.T) {
	for _, tt := range []struct {
		name     string
		proto    string
		topology string
		ipv6     bool
		// dns is the nameserver the server pushes: its own tunnel address, unless
		// that is the far end of a point-to-point link (p2p), which the Reconciler
		// does not know to be reached through the tunnel: nothing routes it.
		dns string
	}{
		{"udp subnet", "udp", "subnet", false, "10.8.0.1"},
		{"tcp subnet", "tcp", "subnet", false, "10.8.0.1"},
		{"udp net30", "udp", "net30", false, "10.8.0.1"},
		{"tcp net30", "tcp", "net30", false, "10.8.0.1"},
		{"udp p2p", "udp", "p2p", false, labBehindV4},
		{"udp subnet with IPv6", "udp", "subnet", true, "10.8.0.1"},
		{"tcp net30 with IPv6", "tcp", "net30", true, "10.8.0.1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			l := newLab(t)
			// The route around the tunnel, to the router the client already has, is not a
			// tunnel route: the engine leaves it out.
			pushes := []string{"route 10.20.0.0 255.255.0.0", "route 10.50.0.0 255.255.0.0 net_gateway", "dhcp-option DNS " + tt.dns, "dhcp-option DOMAIN corp.example"}
			if tt.ipv6 {
				pushes = append(pushes, "route-ipv6 fd00:20::/64")
			}
			l.startServer(labServerOpts{proto: tt.proto, topology: tt.topology, ipv6: tt.ipv6, pushes: pushes})
			network := l.startReconciler()
			h := l.engine(l.profile(tt.proto), tunnel.Spec{Owner: "lab", Priority: 5}, network)
			h.start()
			up := h.waitFor("Up", h.stateIs(tunnel.StateUp))

			// The interface and its addresses.
			if !regexp.MustCompile(`^tun\d+$`).MatchString(up.Iface) {
				t.Fatalf("Iface = %q", up.Iface)
			}
			wantAddrs := []string{"10.8.0.2/24"}
			switch tt.topology {
			case "net30":
				wantAddrs = []string{"10.8.0.6/32"}
			case "p2p":
				wantAddrs = []string{"10.8.0.4/32"}
			}
			if tt.ipv6 {
				wantAddrs = append(wantAddrs, "fd00:8::1000/64")
			}
			if fmt.Sprint(up.Addresses) != fmt.Sprint(prefixes(wantAddrs...)) {
				t.Errorf("Addresses = %v, want %v", up.Addresses, wantAddrs)
			}
			onLink := addressesOf(t, up.Iface)
			for _, want := range wantAddrs {
				if !containsPrefix(onLink, want) {
					t.Errorf("%s has %v, want %s", up.Iface, onLink, want)
				}
			}
			if want := labServerPublic + ":1194"; up.Remote != want {
				t.Errorf("Remote = %q, want %q", up.Remote, want)
			}

			// What the engine announced, and what the Reconciler made of it.
			intent := announcedUp(h)
			wantRoutes := []string{"10.20.0.0/16"}
			if tt.topology == "net30" {
				wantRoutes = append(wantRoutes, "10.8.0.1/32") // the server's own address, which net30 pushes
			}
			if tt.ipv6 {
				wantRoutes = append(wantRoutes, "fd00:20::/64")
			}
			if intent.Iface != up.Iface || intent.Role != tunnel.RoleSplit || fmt.Sprint(intent.Routes) != fmt.Sprint(prefixes(wantRoutes...)) {
				t.Errorf("Up intent = %+v\nwant interface %s, routes %v, split", intent, up.Iface, wantRoutes)
			}
			if want := []netip.Addr{netip.MustParseAddr(labServerPublic)}; fmt.Sprint(intent.Endpoints) != fmt.Sprint(want) {
				t.Errorf("Endpoints = %v, want %v", intent.Endpoints, want)
			}
			wantDNS := []tunnel.DNSIntent{{Servers: []netip.Addr{netip.MustParseAddr(tt.dns)}, MatchDomains: []string{"corp.example"}}}
			if fmt.Sprint(intent.DNS) != fmt.Sprint(wantDNS) {
				t.Errorf("DNS = %+v, want %+v", intent.DNS, wantDNS)
			}
			for _, rr := range network.routesOf("lab") {
				if rr.State != tunnel.RouteInstalled || rr.Via != up.Iface {
					t.Errorf("the Reconciler reports %+v, want it installed through %s", rr, up.Iface)
				}
			}
			var wantTable []string
			for _, r := range wantRoutes {
				wantTable = append(wantTable, fmt.Sprintf("%s dev %s metric 5", strings.TrimSuffix(r, "/32"), up.Iface))
			}
			if got := plaitwayRoutes(t); !sameStrings(got, wantTable) {
				t.Errorf("routes of Plaitway in the table:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(wantTable, "\n"))
			}
			wantEntries := []osnet.DNSEntry{{Servers: intent.DNS[0].Servers, MatchDomains: []string{"corp.example"}, Order: 5, Iface: up.Iface}}
			if got := network.dns.Entries("lab"); fmt.Sprint(got) != fmt.Sprint(wantEntries) {
				t.Errorf("resolver entries = %+v, want %+v\nthe Reconciler says %+v", got, wantEntries, network.Report().DNS)
			}

			// Traffic through the tunnel.
			rx, tx := linkStats(t, up.Iface)
			ping(t, "10.8.0.1")
			ping(t, labBehindV4)
			if dev := routeDev(t, labBehindV4); dev != up.Iface {
				t.Errorf("%s is reached through %s, want %s", labBehindV4, dev, up.Iface)
			}
			if tt.ipv6 {
				ping(t, "fd00:8::1")
				ping(t, labBehindV6)
			}
			if rx2, tx2 := linkStats(t, up.Iface); rx2 <= rx || tx2 <= tx {
				t.Errorf("the counters of %s did not move: rx %d to %d, tx %d to %d", up.Iface, rx, rx2, tx, tx2)
			}
			if dev := routeDev(t, labInternetV4); dev != "vethc" {
				t.Errorf("a split tunnel carries %s through %s", labInternetV4, dev)
			}

			// A network change: openvpn restarts its connection, and the interface
			// and the routes stay.
			before := plaitwayRoutes(t)
			h.eng.Rebind()
			h.waitForNew("Reconnecting", h.stateIs(tunnel.StateReconnecting))
			again := h.waitForNew("Up again", h.stateIs(tunnel.StateUp))
			if again.Iface != up.Iface {
				t.Errorf("the interface changed across a rebind: %q then %q", up.Iface, again.Iface)
			}
			if after := plaitwayRoutes(t); !sameStrings(after, before) {
				t.Errorf("the routes changed across a rebind:\n%s\nthen\n%s", strings.Join(before, "\n"), strings.Join(after, "\n"))
			}
			ping(t, "10.8.0.1")
			ping(t, labBehindV4)

			// Stop leaves nothing.
			pid := h.childPID()
			h.stop()
			h.requireProcessGone()
			h.requireWorkspaceGone()
			requireInterfaceGone(t, up.Iface)
			if left := plaitwayRoutes(t); len(left) != 0 {
				t.Errorf("routes left after Stop: %v", left)
			}
			if left := network.routesOf("lab"); len(left) != 0 {
				t.Errorf("the Reconciler still reports routes: %+v", left)
			}
			if owned, _ := network.dns.Owned(); len(owned) != 0 {
				t.Errorf("resolver entries left after Stop: %v", owned)
			}
			if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err == nil {
				t.Errorf("process %d is still there", pid)
			}
			requireNoEngineGoroutines(t)
		})
	}
}

func prefixes(texts ...string) []netip.Prefix {
	var out []netip.Prefix
	for _, text := range texts {
		out = append(out, netip.MustParsePrefix(text))
	}
	return out
}

func sameStrings(a, b []string) bool {
	return slices.Equal(slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b)))
}

// requireTable fails unless the routes of Plaitway in the table are exactly
// want, each as plaitwayRoutes writes it.
func requireTable(t *testing.T, want ...string) {
	t.Helper()
	if got := plaitwayRoutes(t); !sameStrings(got, want) {
		t.Errorf("routes of Plaitway in the table:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// requireNothingLeft fails unless a stopped engine left nothing of itself: no
// process, no interface, no route, no resolver entry, no workspace.
func requireNothingLeft(t *testing.T, h *harness, network *labNetwork, iface string) {
	t.Helper()
	h.requireProcessGone()
	h.requireWorkspaceGone()
	requireInterfaceGone(t, iface)
	if left := plaitwayRoutes(t); len(left) != 0 {
		t.Errorf("routes left: %v", left)
	}
	if left := network.routesOf("lab"); len(left) != 0 {
		t.Errorf("the Reconciler still reports routes: %+v", left)
	}
	if owned, _ := network.dns.Owned(); len(owned) != 0 {
		t.Errorf("resolver entries left: %v", owned)
	}
}

// childArgs is the command line of the engine's openvpn, as the kernel has it.
func childArgs(t *testing.T, h *harness) []string {
	t.Helper()
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", h.childPID()))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
}

// requireNoScripts fails when the engine's openvpn could run a program of the
// profile: it must be told script-security 1 and be given no script option.
func requireNoScripts(t *testing.T, h *harness) {
	t.Helper()
	args := childArgs(t, h)
	if i := slices.Index(args, "--script-security"); i < 0 || args[i+1] != "1" {
		t.Errorf("openvpn was not told script-security 1: %v", args)
	}
	// Since 2.7 openvpn runs a helper that sets the resolver of the host whenever
	// DNS options are pushed, even at script-security 1.
	if h.eng.info().hasDNSUpdown() {
		if i := slices.Index(args, "--dns-updown"); i < 0 || args[i+1] != "disable" {
			t.Errorf("openvpn was not told --dns-updown disable: %v", args)
		}
	}
	for _, a := range args {
		switch a {
		case "--up", "--down", "--route-up", "--route-pre-down", "--ipchange", "--client-connect", "--learn-address", "--plugin", "--tls-verify", "--auth-user-pass-verify":
			t.Errorf("openvpn was given %s: %v", a, args)
		}
	}
}

// A server that sends the whole internet through the tunnel, in the shapes it
// can ask for it. The Reconciler splits the default route into halves, keeps
// the server itself out of the tunnel with a route of its own, and points the
// resolver of the tunnel at everything.
func TestRootLinuxFullTunnel(t *testing.T) {
	catchAll := []tunnel.DNSIntent{{Servers: []netip.Addr{netip.MustParseAddr("10.8.0.1")}, MatchDomains: []string{"."}}}
	for _, tt := range []struct {
		name   string
		pushes []string
		ipv6   bool
	}{
		{"def1 and the dhcp-options of old servers", []string{"redirect-gateway def1", "dhcp-option DNS 10.8.0.1", "dhcp-option DOMAIN corp.example"}, false},
		{"def1 and the dns options of openvpn 2.6", []string{"redirect-gateway def1", "dns server 1 address 10.8.0.1", "dns search-domains corp.example"}, false},
		// bypass-dhcp and bypass-dns are for Windows; Linux must accept and ignore them.
		{"def1 with the Windows bypass flags", []string{"redirect-gateway def1 bypass-dhcp bypass-dns", "dhcp-option DNS 10.8.0.1"}, false},
		{"redirect-gateway without def1", []string{"redirect-gateway", "dhcp-option DNS 10.8.0.1"}, false},
		{"def1 for both families", []string{"redirect-gateway ipv6 def1", "dhcp-option DNS 10.8.0.1"}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			l := newLab(t)
			l.startServer(labServerOpts{proto: "tcp", ipv6: tt.ipv6, pushes: tt.pushes})
			network := l.startReconciler()
			h := l.engine(l.profile("tcp"), tunnel.Spec{Owner: "lab"}, network)
			h.start()
			up := h.waitFor("Up", h.stateIs(tunnel.StateUp))

			intent := announcedUp(h)
			if intent.Role != tunnel.RoleFull || fmt.Sprint(intent.DNS) != fmt.Sprint(catchAll) {
				t.Errorf("Up intent = %+v\nwant a full tunnel with the catch-all resolver %+v", intent, catchAll)
			}
			table := []string{
				"0.0.0.0/1 dev " + up.Iface + " metric 5", "128.0.0.0/1 dev " + up.Iface + " metric 5",
				"::/1 dev " + up.Iface + " metric 5", "8000::/1 dev " + up.Iface + " metric 5",
				// the server is kept out of the tunnel, through the router of the default route
				labServerPublic + " via " + labServerV4 + " dev vethc metric 1",
			}
			if tt.ipv6 {
				// what "redirect-gateway ipv6 def1" makes openvpn push for IPv6
				for _, dst := range []string{"::/3", "2000::/4", "3000::/4", "fc00::/7"} {
					table = append(table, dst+" dev "+up.Iface+" metric 5")
				}
			}
			requireTable(t, table...)
			for _, rr := range network.routesOf("lab") {
				if rr.State != tunnel.RouteInstalled {
					t.Errorf("the Reconciler reports %+v, want it installed", rr)
				}
			}
			wantEntries := []osnet.DNSEntry{{Servers: catchAll[0].Servers, MatchDomains: []string{"."}, Iface: up.Iface}}
			if got := network.dns.Entries("lab"); fmt.Sprint(got) != fmt.Sprint(wantEntries) {
				t.Errorf("resolver entries = %+v, want %+v", got, wantEntries)
			}

			// The internet is behind the tunnel, the server itself is not.
			if dev := routeDev(t, labInternetV4); dev != up.Iface {
				t.Errorf("%s is reached through %s, want %s", labInternetV4, dev, up.Iface)
			}
			if dev := routeDev(t, labServerPublic); dev != "vethc" {
				t.Errorf("the server is reached through %s: its own packets would go into the tunnel", dev)
			}
			rx, tx := linkStats(t, up.Iface)
			ping(t, "10.8.0.1")
			ping(t, labInternetV4)
			if tt.ipv6 {
				ping(t, "fd00:8::1")
				ping(t, labInternetV6)
			}
			if rx2, tx2 := linkStats(t, up.Iface); rx2 <= rx || tx2 <= tx {
				t.Errorf("the counters of %s did not move: rx %d to %d, tx %d to %d", up.Iface, rx, rx2, tx, tx2)
			}

			h.eng.Rebind()
			h.waitForNew("Reconnecting", h.stateIs(tunnel.StateReconnecting))
			h.waitForNew("Up again", h.stateIs(tunnel.StateUp))
			requireTable(t, table...)
			ping(t, labInternetV4)

			h.stop()
			requireNothingLeft(t, h, network, up.Iface)
			if dev := routeDev(t, labInternetV4); dev != "vethc" {
				t.Errorf("after Stop %s is reached through %s, want the default route", labInternetV4, dev)
			}
		})
	}
}

// The credentials come through the management interface from the server's own
// auth-user-pass-verify script; a profile cannot make the engine run a script
// of its own, however it asks.
func TestRootLinuxCredentialsAndNoScripts(t *testing.T) {
	l := newLab(t)
	l.startServer(labServerOpts{password: "correct horse", pushes: []string{"route 10.20.0.0 255.255.0.0"}})
	network := l.startReconciler()
	marker := filepath.Join(l.dir, "script-ran")
	script := writeStub(t, l.dir, "poison", stubBehavior{Counter: marker})
	profile := l.profile("udp", "auth-user-pass", "auth-nocache",
		"script-security 2", "up "+script, "down "+script, "route-up "+script, "route-pre-down "+script, "ipchange "+script,
		"client-connect "+script, "tls-verify "+script, "plugin "+script, "setenv foo bar")
	h := l.engine(profile, tunnel.Spec{Owner: "lab"}, network)
	h.start()

	waiting := h.waitFor("a request for credentials", h.stateIs(tunnel.StateAwaitingCredentials))
	if want := 10; len(waiting.Warnings) != want { // every line of the profile that asks for a program
		t.Errorf("%d warnings, want %d: %q", len(waiting.Warnings), want, waiting.Warnings)
	}
	if waiting.NeedsCredentials != tunnel.CredentialUserPassword {
		t.Errorf("NeedsCredentials = %v", waiting.NeedsCredentials)
	}
	if err := h.eng.ProvideCredentials(tunnel.CredentialUserPassword, "alice", "wrong horse"); err != nil {
		t.Fatal(err)
	}
	h.waitFor("a rejected password", func(s tunnel.Status) bool {
		return s.State == tunnel.StateAwaitingCredentials && s.Err == "authentication failed"
	})
	if len(network.routesOf("lab")) != 0 {
		t.Errorf("routes were installed for a tunnel that is not up: %+v", network.routesOf("lab"))
	}
	if err := h.eng.ProvideCredentials(tunnel.CredentialUserPassword, "alice", "correct horse"); err != nil {
		t.Fatal(err)
	}
	up := h.waitFor("Up", h.stateIs(tunnel.StateUp))
	requireTable(t, "10.20.0.0/16 dev "+up.Iface+" metric 5")
	ping(t, labBehindV4)
	requireNoScripts(t, h)

	// auth-nocache: openvpn asks again after a reconnect, and the engine answers
	// from memory.
	h.eng.Rebind()
	h.waitForNew("Reconnecting", h.stateIs(tunnel.StateReconnecting))
	h.waitForNew("Up again", h.stateIs(tunnel.StateUp))
	ping(t, labBehindV4)
	h.mu.Lock()
	seenUp, asked := false, 0
	for _, s := range h.statuses {
		seenUp = seenUp || s.State == tunnel.StateUp
		if seenUp && s.State == tunnel.StateAwaitingCredentials {
			asked++
		}
	}
	h.mu.Unlock()
	if asked != 0 {
		t.Errorf("the user was asked for credentials again after connecting (%d times)", asked)
	}

	h.stop()
	requireNothingLeft(t, h, network, up.Iface)
	if _, err := os.Stat(marker); err == nil {
		t.Error("a program of the profile was run")
	}
	logs := h.logText()
	for _, secret := range []string{"correct horse", "wrong horse"} {
		if strings.Contains(logs, secret) {
			t.Errorf("a password reached the log:\n%s", logs)
		}
	}
}

// openvpn dying under the engine is a failure that says why, and the engine
// cleans up after it.
func TestRootLinuxKilledChildIsAFailure(t *testing.T) {
	l := newLab(t)
	l.startServer(labServerOpts{pushes: []string{"route 10.20.0.0 255.255.0.0", "redirect-gateway def1"}})
	network := l.startReconciler()
	h := l.engine(l.profile("udp"), tunnel.Spec{Owner: "lab"}, network)
	h.start()
	up := h.waitFor("Up", h.stateIs(tunnel.StateUp))
	ping(t, labInternetV4)

	if err := syscall.Kill(h.childPID(), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	failed := h.waitFor("Failed", h.stateIs(tunnel.StateFailed))
	if !strings.Contains(failed.Err, "openvpn exited: signal: killed") {
		t.Errorf("Err = %q", failed.Err)
	}
	// The last lines of what openvpn printed are in the report.
	tail := readableTail(h.eng.ring.tail(outputTailLines))
	if len(tail) == 0 || !strings.Contains(failed.Err, strings.Join(tail, " | ")) {
		t.Errorf("Err = %q, want it to end with the last lines of the output %q", failed.Err, tail)
	}
	select {
	case <-h.collect:
	case <-time.After(5 * time.Second):
		t.Fatal("the Status channel stayed open")
	}
	requireNothingLeft(t, h, network, up.Iface)
	if dev := routeDev(t, labInternetV4); dev != "vethc" {
		t.Errorf("after the failure %s is reached through %s, want the default route", labInternetV4, dev)
	}
}

// Why the engine refuses a profile with a tap device (tapRefusal). A tap is an
// Ethernet link, and the Reconciler binds every tunnel route to the device
// without a next hop, as it must for tun and WireGuard. The server's own
// address is on the subnet of the link and answers there, but a host behind the
// server is looked for with ARP, which only the server's own addresses answer,
// so the same route with the server as its next hop is the one that works.
//
// The engine is made to run the tap profile (the refusal is taken off), and the
// rest is as it would be: the real tap device, the up event, the intent, the
// Reconciler and the kernel.
func TestRootLinuxTapRoutesNeedANextHop(t *testing.T) {
	l := newLab(t)
	l.addHostBehindServer()
	l.startServer(labServerOpts{dev: "tap", pushes: []string{"route " + strings.Replace(labHostNet, "/24", " 255.255.255.0", 1)}})
	network := l.startReconciler()
	h := l.engine(l.profileFor("tap", "udp"), tunnel.Spec{Owner: "lab"}, network, func(e *engine) { e.prof.device = "" })
	h.start()
	up := h.waitFor("Up", h.stateIs(tunnel.StateUp))
	if !regexp.MustCompile(`^tap\d+$`).MatchString(up.Iface) {
		t.Fatalf("Iface = %q, want a tap device", up.Iface)
	}
	requireTable(t, labHostNet+" dev "+up.Iface+" metric 5")

	// The link itself works.
	ping(t, "10.8.0.1")

	// The route the Reconciler made does not reach the host behind the server.
	// The kernel gives up on ARP after about three seconds.
	if out, err := exec.Command("ping", "-c", "1", "-W", "6", labHostV4).CombinedOutput(); err == nil {
		t.Fatalf("ping %s through an on-link route over a tap device worked; the refusal would be needless:\n%s", labHostV4, out)
	} else if !strings.Contains(string(out), "Destination Host Unreachable") {
		t.Errorf("ping %s failed, but not for want of an answer to ARP:\n%s", labHostV4, out)
	}
	if neigh := runIP(t, "neigh", "show", labHostV4, "dev", up.Iface); !strings.Contains(neigh, "FAILED") && !strings.Contains(neigh, "INCOMPLETE") {
		t.Errorf("neighbour %s = %q, want ARP to have failed", labHostV4, neigh)
	}

	// The same destination with the server as its next hop works.
	runIP(t, "route", "add", labHostNet, "via", "10.8.0.1", "dev", up.Iface, "metric", "1")
	t.Cleanup(func() {
		exec.Command("ip", "route", "del", labHostNet, "via", "10.8.0.1", "dev", up.Iface, "metric", "1").Run()
	})
	ping(t, labHostV4)

	h.stop()
}

// The names a profile can give its device, and what the engine reports for
// them: the interface is the one openvpn made, whatever its name.
func TestRootLinuxDeviceNames(t *testing.T) {
	for _, tt := range []struct{ name, lines, iface string }{
		{"tun", "dev tun", "tun0"},
		{"tun7", "dev tun7", "tun7"},
		{"a name of its own and a type", "dev vpn0\ndev-type tun", "vpn0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			l := newLab(t)
			l.startServer(labServerOpts{pushes: []string{"route 10.20.0.0 255.255.0.0"}})
			network := l.startReconciler()
			profile := strings.Replace(l.profile("udp"), "dev tun\n", "", 1) + tt.lines + "\n"
			h := l.engine(profile, tunnel.Spec{Owner: "lab"}, network)
			h.start()
			up := h.waitFor("Up", h.stateIs(tunnel.StateUp))
			if up.Iface != tt.iface {
				t.Errorf("Iface = %q, want %q", up.Iface, tt.iface)
			}
			requireTable(t, "10.20.0.0/16 dev "+up.Iface+" metric 5")
			ping(t, labBehindV4)
			h.stop()
			requireNothingLeft(t, h, network, up.Iface)
		})
	}
}

// openvpn refusing the profile it was given, after it connected, ends the
// engine with the reason and leaves no interface: the engine switches data
// channel offload off, which a device named for it (ovpn0) cannot work without.
func TestRootLinuxProfileOpenVPNRefusesAfterConnecting(t *testing.T) {
	l := newLab(t)
	l.startServer(labServerOpts{pushes: []string{"route 10.20.0.0 255.255.0.0"}})
	network := l.startReconciler()
	h := l.engine(strings.Replace(l.profile("udp"), "dev tun\n", "dev ovpn0\n", 1), tunnel.Spec{Owner: "lab"}, network)
	h.start()
	failed := h.waitFor("Failed", h.stateIs(tunnel.StateFailed))
	if !strings.Contains(failed.Err, "openvpn exited: exit status 1") {
		t.Errorf("Err = %q", failed.Err)
	}
	<-h.collect
	requireNothingLeft(t, h, network, "ovpn0")
}

// Two profiles at once: each tunnel has a device of its own, and the routes of
// each lead into its own.
func TestRootLinuxTwoTunnels(t *testing.T) {
	l := newLab(t)
	l.startServer(labServerOpts{extra: []string{"--duplicate-cn"}})
	network := l.startReconciler()
	for _, addr := range []string{"10.21.0.5", "10.22.0.5"} {
		l.server.ip("addr", "add", addr+"/32", "dev", "lo")
	}
	a := l.engine(l.profile("udp", "route 10.21.0.0 255.255.0.0"), tunnel.Spec{Owner: "a", Priority: 1}, network)
	b := l.engine(l.profile("udp", "route 10.22.0.0 255.255.0.0"), tunnel.Spec{Owner: "b", Priority: 2}, network)
	a.start()
	upA := a.waitFor("Up", a.stateIs(tunnel.StateUp))
	b.start()
	upB := b.waitFor("Up", b.stateIs(tunnel.StateUp))
	if upA.Iface == upB.Iface || upA.Iface == "" || upB.Iface == "" {
		t.Fatalf("the tunnels are on %q and %q", upA.Iface, upB.Iface)
	}
	requireTable(t, "10.21.0.0/16 dev "+upA.Iface+" metric 5", "10.22.0.0/16 dev "+upB.Iface+" metric 5")
	ping(t, "10.21.0.5")
	ping(t, "10.22.0.5")
	if dev := routeDev(t, "10.21.0.5"); dev != upA.Iface {
		t.Errorf("10.21.0.5 is reached through %s, want %s", dev, upA.Iface)
	}
	if dev := routeDev(t, "10.22.0.5"); dev != upB.Iface {
		t.Errorf("10.22.0.5 is reached through %s, want %s", dev, upB.Iface)
	}
	a.stop()
	requireTable(t, "10.22.0.0/16 dev "+upB.Iface+" metric 5")
	ping(t, "10.22.0.5")
	b.stop()
	requireInterfaceGone(t, upA.Iface)
	requireInterfaceGone(t, upB.Iface)
	requireTable(t)
}

// A server that is reached over IPv6: the route around the tunnel that keeps the
// server out of it is an IPv6 host route through the router of the IPv6 default
// route.
func TestRootLinuxIPv6Server(t *testing.T) {
	for _, proto := range []string{"udp", "tcp"} {
		t.Run(proto, func(t *testing.T) {
			l := newLab(t)
			l.startServer(labServerOpts{proto: proto, transportV6: true, ipv6: true, pushes: []string{"redirect-gateway ipv6 def1", "route-ipv6 2000::/3"}})
			network := l.startReconciler()
			h := l.engine(l.profile(proto), tunnel.Spec{Owner: "lab"}, network)
			h.start()
			up := h.waitFor("Up", h.stateIs(tunnel.StateUp))
			if want := fmt.Sprintf("[%s]:1194", labServerPublicV6); up.Remote != want && up.Remote != labServerPublicV6+":1194" {
				t.Errorf("Remote = %q, want the server's IPv6 address", up.Remote)
			}
			intent := announcedUp(h)
			if want := []netip.Addr{netip.MustParseAddr(labServerPublicV6)}; fmt.Sprint(intent.Endpoints) != fmt.Sprint(want) {
				t.Errorf("Endpoints = %v, want %v", intent.Endpoints, want)
			}
			bypass := labServerPublicV6 + " via " + labServerV6 + " dev vethc metric 1"
			if got := plaitwayRoutes(t); !slices.Contains(got, bypass) {
				t.Errorf("no route keeps the server out of the tunnel (%q) in\n%s", bypass, strings.Join(got, "\n"))
			}
			if dev := routeDev(t, labServerPublicV6); dev != "vethc" {
				t.Errorf("the server is reached through %s: its own packets would go into the tunnel", dev)
			}
			if dev := routeDev(t, labInternetV6); dev != up.Iface {
				t.Errorf("%s is reached through %s, want %s", labInternetV6, dev, up.Iface)
			}
			ping(t, "fd00:8::1")
			ping(t, labInternetV6)
			ping(t, labInternetV4)
			h.stop()
			requireNothingLeft(t, h, network, up.Iface)
		})
	}
}

// openvpn's own restarts, when the server goes away and comes back, keep the
// interface and the routes, and the status follows: this is the reconnect no
// network event asks for.
func TestRootLinuxServerRestart(t *testing.T) {
	l := newLab(t)
	opts := labServerOpts{extra: []string{"--keepalive", "1", "3"}, pushes: []string{"route 10.20.0.0 255.255.0.0"}}
	srv := l.startServer(opts)
	network := l.startReconciler()
	h := l.engine(l.profile("udp", "connect-retry 1 2"), tunnel.Spec{Owner: "lab"}, network)
	h.start()
	up := h.waitFor("Up", h.stateIs(tunnel.StateUp))
	table := "10.20.0.0/16 dev " + up.Iface + " metric 5"
	requireTable(t, table)
	announced := len(h.net.snapshot())

	stopTied(srv.cmd)
	h.waitForNew("Reconnecting", h.stateIs(tunnel.StateReconnecting))
	if _, err := net.InterfaceByName(up.Iface); err != nil {
		t.Errorf("the interface went away while openvpn was waiting for the server: %v", err)
	}
	requireTable(t, table)

	l.startServer(opts)
	again := h.waitForNew("Up again", h.stateIs(tunnel.StateUp))
	if again.Iface != up.Iface {
		t.Errorf("the interface changed: %q then %q", up.Iface, again.Iface)
	}
	requireTable(t, table)
	ping(t, labBehindV4)
	if after := h.net.snapshot(); len(after) != announced {
		t.Errorf("the engine talked to the Reconciler during the reconnect: %+v", after[announced:])
	}
	h.stop()
	requireNothingLeft(t, h, network, up.Iface)
}

// A server that comes back with another tunnel network makes openvpn close its
// device and open a new one, which is a down and an up for the engine. The
// routes and the resolver entry go to the new device, and none of the old ones
// stays.
func TestRootLinuxServerChangesTheTunnelNetwork(t *testing.T) {
	l := newLab(t)
	opts := labServerOpts{
		extra:  []string{"--keepalive", "1", "3"},
		pushes: []string{"route 10.20.0.0 255.255.0.0", "dhcp-option DNS 10.8.0.1", "dhcp-option DOMAIN corp.example"},
	}
	srv := l.startServer(opts)
	network := l.startReconciler()
	h := l.engine(l.profile("udp", "connect-retry 1 2"), tunnel.Spec{Owner: "lab", Priority: 5}, network)
	h.start()
	up := h.waitFor("Up", h.stateIs(tunnel.StateUp))
	if !containsPrefix(up.Addresses, "10.8.0.2/24") {
		t.Fatalf("Addresses = %v", up.Addresses)
	}
	first := linkIndex(t, up.Iface)

	stopTied(srv.cmd)
	h.waitForNew("Reconnecting", h.stateIs(tunnel.StateReconnecting))
	opts.network = "10.9.0.0"
	opts.pushes = []string{"route 10.20.0.0 255.255.0.0", "dhcp-option DNS 10.9.0.1", "dhcp-option DOMAIN corp.example"}
	l.startServer(opts)
	again := h.waitForNew("Up on the new network", func(s tunnel.Status) bool {
		return s.State == tunnel.StateUp && containsPrefix(s.Addresses, "10.9.0.2/24")
	})

	if second := linkIndex(t, again.Iface); second == first {
		t.Fatalf("%s is the device it was (index %d): openvpn did not make a new one, and this test shows nothing", again.Iface, first)
	}
	if onLink := addressesOf(t, again.Iface); containsPrefix(onLink, "10.8.0.2/24") || !containsPrefix(onLink, "10.9.0.2/24") {
		t.Errorf("%s has %v", again.Iface, onLink)
	}
	requireTable(t, "10.20.0.0/16 dev "+again.Iface+" metric 5")
	for _, rr := range network.routesOf("lab") {
		if rr.State != tunnel.RouteInstalled || rr.Via != again.Iface {
			t.Errorf("the Reconciler reports %+v, want it installed through %s", rr, again.Iface)
		}
	}
	wantEntries := []osnet.DNSEntry{{Servers: []netip.Addr{netip.MustParseAddr("10.9.0.1")}, MatchDomains: []string{"corp.example"}, Order: 5, Iface: again.Iface}}
	if got := network.dns.Entries("lab"); fmt.Sprint(got) != fmt.Sprint(wantEntries) {
		t.Errorf("resolver entries = %+v, want %+v", got, wantEntries)
	}
	ping(t, "10.9.0.1")
	ping(t, labBehindV4)
	h.stop()
	requireNothingLeft(t, h, network, again.Iface)
}

func linkIndex(t *testing.T, name string) int {
	t.Helper()
	iface, err := net.InterfaceByName(name)
	if err != nil {
		t.Fatal(err)
	}
	return iface.Index
}

// The whole loop of a network change, as the daemon wires it: the router of the
// default route changes, the monitor sees it, the Reconciler moves the route
// that keeps the server out of the tunnel and asks the engine to rebind, and
// openvpn restarts its connection through the new router with the interface and
// the routes of the tunnel in place.
func TestRootLinuxNetworkChangeRebindsTheTunnel(t *testing.T) {
	l := newLab(t)
	l.startServer(labServerOpts{pushes: []string{"redirect-gateway def1", "dhcp-option DNS 10.8.0.1"}})
	network := l.startReconciler()
	h := l.engine(l.profile("udp"), tunnel.Spec{Owner: "lab"}, network)
	network.SetRebind(h.eng.Rebind)
	h.start()
	up := h.waitFor("Up", h.stateIs(tunnel.StateUp))
	halves := []string{
		"0.0.0.0/1 dev " + up.Iface + " metric 5", "128.0.0.0/1 dev " + up.Iface + " metric 5",
		"::/1 dev " + up.Iface + " metric 5", "8000::/1 dev " + up.Iface + " metric 5",
	}
	requireTable(t, append(halves, labServerPublic+" via "+labServerV4+" dev vethc metric 1")...)

	// A second router, which the default route moves to.
	const newRouter = "10.99.0.3"
	l.server.ip("addr", "add", newRouter+"/24", "dev", "veths")
	runIP(t, "route", "replace", "default", "via", newRouter)
	h.waitForNew("Reconnecting", h.stateIs(tunnel.StateReconnecting))
	again := h.waitForNew("Up again", h.stateIs(tunnel.StateUp))
	if again.Iface != up.Iface {
		t.Errorf("the interface changed across the rebind: %q then %q", up.Iface, again.Iface)
	}
	requireTable(t, append(halves, labServerPublic+" via "+newRouter+" dev vethc metric 1")...)
	if dev := routeDev(t, labInternetV4); dev != up.Iface {
		t.Errorf("%s is reached through %s, want %s", labInternetV4, dev, up.Iface)
	}
	ping(t, labInternetV4)
	h.stop()
	requireNothingLeft(t, h, network, up.Iface)
}
