//go:build rootintegration && darwin

package reconciler

// These tests drive the Reconciler against the real routing table and the real
// dynamic store, so they refuse to run unless asked twice: the rootintegration
// build tag, PLAITWAY_ROOT_TESTS=1 and root.
//
//	PLAITWAY_ROOT_TESTS=1 sudo -E go test -tags rootintegration -count=1 -run Real ./internal/reconciler/
//
// What they add lives in 198.51.100.0/24 (documentation range) and under the
// owner plaitway-test, and is removed again, also when a test fails. lo0 stands
// in for a tunnel interface. They are meant for a spare Mac or a VM: they have
// not been run on the machine they were written on.

import (
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/osnet/macos"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

func requireRoot(t *testing.T) {
	t.Helper()
	if os.Getenv("PLAITWAY_ROOT_TESTS") != "1" || os.Geteuid() != 0 {
		t.Fatal("refusing to run: this test changes the host; set PLAITWAY_ROOT_TESTS=1 and run it as root")
	}
}

type realHost struct {
	routes  osnet.RouteTable
	dns     osnet.DNSConfigurator
	net     osnet.NetMonitor
	nexthop osnet.Nexthop
}

func newRealHost(t *testing.T) *realHost {
	t.Helper()
	requireRoot(t)
	h := &realHost{
		routes: macos.NewRouteTable(),
		dns:    macos.NewDNS(macos.DNSOptions{}),
		net:    macos.NewNetMonitor(macos.NetMonitorOptions{}),
	}
	ns, err := h.net.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if ns.DefaultV4 == nil {
		t.Skip("no IPv4 default route: nothing to build a bypass route from")
	}
	h.nexthop = *ns.DefaultV4
	// Whatever a test leaves, the host is cleaned.
	t.Cleanup(func() {
		// A route is deleted as the kernel reports it: its flags decide how it is
		// addressed, so a bare destination is not enough.
		routes, err := h.routes.Dump()
		if err != nil {
			t.Errorf("clean up: %v", err)
			return
		}
		for _, r := range routes {
			if r.Scoped || (r.Dst != netip.MustParsePrefix("198.51.100.0/24") && r.Dst != netip.MustParsePrefix("198.51.100.7/32")) {
				continue
			}
			if err := h.routes.Delete(r); err != nil && !errors.Is(err, osnet.ErrNotFound) {
				t.Errorf("clean up %s: %v", r.Dst, err)
			}
		}
		if err := h.dns.Remove("plaitway-test"); err != nil {
			t.Errorf("clean up DNS: %v", err)
		}
	})
	return h
}

func (h *realHost) reconciler(t *testing.T, journal string) *Reconciler {
	t.Helper()
	r, err := New(Config{
		Routes: h.routes, DNS: h.dns, Net: h.net, JournalPath: journal,
		Log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (h *realHost) route(t *testing.T, dst string) (osnet.Route, bool) {
	t.Helper()
	routes, err := h.routes.Dump()
	if err != nil {
		t.Fatal(err)
	}
	want := netip.MustParsePrefix(dst)
	for _, r := range routes {
		if r.Dst == want && !r.Scoped {
			return r, true
		}
	}
	return osnet.Route{}, false
}

func (h *realHost) ownedKeys(t *testing.T) []string {
	t.Helper()
	keys, err := h.dns.Owned()
	if err != nil {
		t.Fatal(err)
	}
	return slices.DeleteFunc(keys, func(k string) bool { return !strings.Contains(k, "plaitway-test") })
}

func testIntent() tunnel.Intent {
	return tunnel.Intent{
		Owner:     "plaitway-test",
		State:     tunnel.StateUp,
		Iface:     "lo0",
		Role:      tunnel.RoleSplit,
		Endpoints: []netip.Addr{netip.MustParseAddr("198.51.100.7")},
		Routes:    []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")},
		DNS: []tunnel.DNSIntent{{
			Servers:      []netip.Addr{netip.MustParseAddr("198.51.100.53")},
			MatchDomains: []string{"plaitway-test.invalid"},
		}},
	}
}

// What a crash leaves behind is removed by the next start, from the journal:
// the bypass route through the physical gateway and the resolver entry. The
// interface-bound route would vanish with its tunnel; lo0 does not, so the test
// cleans it up itself.
func TestRealHostCrashRecovery(t *testing.T) {
	h := newRealHost(t)
	journal := filepath.Join(t.TempDir(), "journal")
	r1 := h.reconciler(t, journal)
	if err := r1.Announce(testIntent()); err != nil {
		t.Fatal(err)
	}

	bypass, ok := h.route(t, "198.51.100.7/32")
	if !ok || bypass.Gateway.WithZone("") != h.nexthop.Gateway.WithZone("") {
		t.Fatalf("the bypass route is %+v (present %v), want one through %v", bypass, ok, h.nexthop.Gateway)
	}
	if tunnelRoute, ok := h.route(t, "198.51.100.0/24"); !ok || tunnelRoute.Iface != "lo0" {
		t.Fatalf("the tunnel route is %+v (present %v)", tunnelRoute, ok)
	}
	if len(h.ownedKeys(t)) == 0 {
		t.Fatal("no resolver entry was written")
	}

	// The daemon dies here: r1 is simply abandoned.
	r2 := h.reconciler(t, journal)
	t.Cleanup(func() { r2.stop(nil) })

	if got, ok := h.route(t, "198.51.100.7/32"); ok {
		t.Errorf("the journaled bypass route survived the restart: %+v", got)
	}
	if keys := h.ownedKeys(t); len(keys) != 0 {
		t.Errorf("resolver entries survived the restart: %v", keys)
	}
	if stale := r2.Report().Stale; len(stale) != 0 {
		t.Errorf("stale routes after recovery: %+v", stale)
	}
}

// Without a journal the resolver entries are found by their marker. This is the
// one place where Reconciler and adapter must agree on what Owned returns and
// Remove accepts: the Reconciler hands every key from Owned to Remove.
func TestRealHostSweepWithoutJournal(t *testing.T) {
	h := newRealHost(t)
	r1 := h.reconciler(t, filepath.Join(t.TempDir(), "first"))
	if err := r1.Announce(testIntent()); err != nil {
		t.Fatal(err)
	}
	if len(h.ownedKeys(t)) == 0 {
		t.Fatal("no resolver entry was written")
	}

	r2 := h.reconciler(t, filepath.Join(t.TempDir(), "another-journal"))
	t.Cleanup(func() { r2.stop(nil) })

	if keys := h.ownedKeys(t); len(keys) != 0 {
		t.Errorf("marked resolver entries survived: %v; Remove must accept what Owned returns", keys)
	}
}
