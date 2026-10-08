package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/manager/fake"
	"github.com/KoukeNeko/Plaitway/internal/osnet"
	netfake "github.com/KoukeNeko/Plaitway/internal/osnet/fake"
	"github.com/KoukeNeko/Plaitway/internal/reconciler"
	"github.com/KoukeNeko/Plaitway/internal/transport"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

const (
	// slowDelete is how long the pretend route table takes to delete a route,
	// as the IP Helper API can when the stack is busy. The shutdown has to wait
	// for every one of them.
	slowDelete = 150 * time.Millisecond

	hostedOwner  = tunnel.OwnerID("shutdown-test")
	hostedTunnel = "Plaitway-test-shutdown"
)

var (
	hostedServer = netip.MustParseAddr("203.0.113.5") // TEST-NET-3
	hostedDNS    = netip.MustParseAddr("10.8.0.1")
)

// slowRoutes is a route table whose deletes take their time.
type slowRoutes struct {
	osnet.RouteTable
	deleteTakes time.Duration
}

func (s slowRoutes) Delete(route osnet.Route) error {
	time.Sleep(s.deleteTakes)
	return s.RouteTable.Delete(route)
}

// hostedDaemon is the daemon core on the real Reconciler, which manages the
// routes and DNS entries of a pretend Windows host and not of this machine, and
// on the fake backends: everything but the adapters is the daemon's own.
type hostedDaemon struct {
	host *netfake.Host
	rec  *reconciler.Reconciler
	d    *daemon
	lis  net.Listener
	// systemRoutes is the number of routes before any tunnel: the default route.
	systemRoutes int
	// tunnelRoutes is how many routes the announced tunnel installed.
	tunnelRoutes int
}

func newHostedDaemon(t *testing.T, deleteTakes time.Duration) *hostedDaemon {
	t.Helper()
	host := netfake.NewWindowsHost()
	addInterface(host, osnet.Interface{Name: "Ethernet", Index: 1, Up: true, Addrs: []netip.Prefix{netip.MustParsePrefix("192.168.1.20/24")}})
	if err := host.Routes.Add(osnet.Route{Dst: netip.MustParsePrefix("0.0.0.0/0"), Gateway: netip.MustParseAddr("192.168.1.1"), Iface: "Ethernet", Static: true}); err != nil {
		t.Fatal(err)
	}
	addInterface(host, osnet.Interface{Name: hostedTunnel, Index: 2, Up: true, Tunnel: true, Addrs: []netip.Prefix{netip.MustParsePrefix("10.8.0.2/32")}})
	systemRoutes := dumpRoutes(t, host)

	rec, err := reconciler.New(reconciler.Config{
		Routes:      slowRoutes{RouteTable: host.Routes, deleteTakes: deleteTakes},
		DNS:         host.DNS,
		Net:         host.Net,
		JournalPath: filepath.Join(t.TempDir(), "journal"),
		Log:         discardLog(),
		Keying:      reconciler.KeyByPrefixInterfaceNextHop,
	})
	if err != nil {
		t.Fatal(err)
	}
	socket := newSocketPath(t)
	lis, err := transport.Listen(socket, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config{socket: socket, stateDir: t.TempDir()}
	log, daemonLog := newLogger(io.Discard, slog.LevelInfo)
	who := everyone()
	d, err := assembleDaemon(log, daemonLog, cfg, who.pol, who.credentials(),
		engines{backends: fake.Backends(fastFake), reconciler: rec, net: host.Net})
	if err != nil {
		lis.Close()
		t.Fatal(err)
	}
	return &hostedDaemon{host: host, rec: rec, d: d, lis: lis, systemRoutes: len(systemRoutes)}
}

func addInterface(host *netfake.Host, ifc osnet.Interface) {
	host.Routes.AddInterface(ifc)
	host.Sync()
}

func dumpRoutes(t *testing.T, host *netfake.Host) []osnet.Route {
	t.Helper()
	routes, err := host.Routes.Dump()
	if err != nil {
		t.Fatal(err)
	}
	return routes
}

// announceFullTunnel is a profile that is up and sends all traffic through its
// tunnel: the two halves of the default route, a route to the server that
// bypasses them, and a catch-all DNS entry.
func (h *hostedDaemon) announceFullTunnel(t *testing.T) {
	t.Helper()
	err := h.rec.Announce(tunnel.Intent{
		Owner:     hostedOwner,
		State:     tunnel.StateUp,
		Iface:     hostedTunnel,
		Role:      tunnel.RoleFull,
		Priority:  1,
		UpSince:   time.Now(),
		Endpoints: []netip.Addr{hostedServer},
		Routes:    []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")},
		DNS:       []tunnel.DNSIntent{{Servers: []netip.Addr{hostedDNS}, MatchDomains: []string{"."}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.tunnelRoutes = len(dumpRoutes(t, h.host)) - h.systemRoutes
	if h.tunnelRoutes <= 0 {
		t.Fatalf("the tunnel installed no route: %d routes more than the system's", h.tunnelRoutes)
	}
	if len(h.host.DNS.All()) == 0 {
		t.Fatal("the tunnel installed no DNS entry")
	}
}

// requireHostRestored checks that nothing of the tunnel is left.
func (h *hostedDaemon) requireHostRestored(t *testing.T) {
	t.Helper()
	if got := dumpRoutes(t, h.host); len(got) != h.systemRoutes {
		t.Errorf("routes left after the shutdown: %v, want only the %d of the system", got, h.systemRoutes)
	}
	if left := h.host.DNS.All(); len(left) != 0 {
		t.Errorf("DNS entries left after the shutdown: %v", left)
	}
}

// Stopping the daemon removes the routes and DNS entries of a tunnel that is
// still up, and does so before serve returns: the engines are told to stop
// first, but the Reconciler's own cleanup must not depend on them withdrawing.
func TestShutdownRemovesTheRoutesAndDNSOfATunnelThatIsStillUp(t *testing.T) {
	h := newHostedDaemon(t, slowDelete)
	h.announceFullTunnel(t)

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- h.d.serve(ctx, h.lis) }()
	stopRequested := time.Now()
	cancel()

	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serve returned %v", err)
		}
	case <-time.After(serverStopTimeout + shutdownTimeout):
		t.Fatal("the daemon did not stop within its shutdown budget")
	}
	if took, atLeast := time.Since(stopRequested), time.Duration(h.tunnelRoutes)*slowDelete; took < atLeast {
		t.Errorf("serve returned after %s, before the %d slow deletes could have finished (%s)", took, h.tunnelRoutes, atLeast)
	}
	h.requireHostRestored(t)
}
