//go:build rootintegration

package macos

// These tests change the routing table and the dynamic store of the machine
// they run on, so they refuse to run unless asked twice: the rootintegration
// build tag, PLAITWAY_ROOT_TESTS=1 and root.
//
//	PLAITWAY_ROOT_TESTS=1 sudo -E go test -tags rootintegration -count=1 ./internal/osnet/macos/
//
// What they add lives in the documentation ranges 198.51.100.0/24 and
// 2001:db8:ffff::/48 and under the DNS owner plaitway-test, and is removed
// again, also when a test fails. They are meant for a spare Mac or a VM: they
// have not been run on the machine they were written on.

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

func requireRoot(t *testing.T) {
	t.Helper()
	if os.Getenv("PLAITWAY_ROOT_TESTS") != "1" || os.Geteuid() != 0 {
		t.Fatal("refusing to run: this test changes the host; set PLAITWAY_ROOT_TESTS=1 and run it as root")
	}
}

// tableRoute returns the route of dst from the kernel's table.
func tableRoute(t *testing.T, table osnet.RouteTable, dst netip.Prefix, scoped bool) (osnet.Route, bool) {
	t.Helper()
	routes, err := table.Dump()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range routes {
		if r.Dst == dst && r.Scoped == scoped {
			return r, true
		}
	}
	return osnet.Route{}, false
}

// cleanUp deletes route when the test ends, whatever state it was left in.
func cleanUp(t *testing.T, table osnet.RouteTable, route osnet.Route) {
	t.Helper()
	t.Cleanup(func() {
		if err := table.Delete(route); err != nil && !errors.Is(err, osnet.ErrNotFound) {
			t.Errorf("clean up %s: %v", route.Dst, err)
		}
	})
}

// A route is added, shows up in the table as what it is, cannot be added twice,
// is deleted from the table's own copy of it, and cannot be deleted twice.
func checkRouteLifecycle(t *testing.T, table osnet.RouteTable, route osnet.Route, check func(t *testing.T, got osnet.Route)) {
	t.Helper()
	cleanUp(t, table, route)
	if _, exists := tableRoute(t, table, route.Dst, route.Scoped); exists {
		t.Fatalf("%s is in the table before the test", route.Dst)
	}
	if err := table.Add(route); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, ok := tableRoute(t, table, route.Dst, route.Scoped)
	if !ok {
		t.Fatalf("%s is not in the table after Add", route.Dst)
	}
	if !got.Static {
		t.Errorf("a route we added is not static: %+v", got)
	}
	check(t, got)
	if err := table.Add(route); !errors.Is(err, osnet.ErrExists) {
		t.Errorf("second Add = %v, want ErrExists", err)
	}
	// What Dump returned carries the kernel's flags, which decide how the route
	// is addressed when it is deleted.
	if err := table.Delete(got); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, still := tableRoute(t, table, route.Dst, route.Scoped); still {
		t.Errorf("%s is still in the table after Delete", route.Dst)
	}
	if err := table.Delete(got); !errors.Is(err, osnet.ErrNotFound) {
		t.Errorf("second Delete = %v, want ErrNotFound", err)
	}
}

func TestRootBlackholeRoutes(t *testing.T) {
	requireRoot(t)
	table := NewRouteTable()
	for _, prefix := range []string{"198.51.100.77/32", "198.51.100.64/28", "2001:db8:ffff::77/128", "2001:db8:ffff:1::/64"} {
		t.Run(prefix, func(t *testing.T) {
			route := osnet.Route{Dst: netip.MustParsePrefix(prefix), Blackhole: true}
			checkRouteLifecycle(t, table, route, func(t *testing.T, got osnet.Route) {
				if !got.Blackhole {
					t.Errorf("not a blackhole: %+v", got)
				}
			})
		})
	}
}

// Routes bound to an interface, as a tunnel's routes are: lo0 stands in for a
// utun that this test cannot create.
func TestRootInterfaceBoundRoutes(t *testing.T) {
	requireRoot(t)
	table := NewRouteTable()
	for _, prefix := range []string{"198.51.100.128/25", "198.51.100.200/32", "2001:db8:ffff:2::/64"} {
		t.Run(prefix, func(t *testing.T) {
			route := osnet.Route{Dst: netip.MustParsePrefix(prefix), Iface: "lo0"}
			checkRouteLifecycle(t, table, route, func(t *testing.T, got osnet.Route) {
				if got.Iface != "lo0" || got.Blackhole {
					t.Errorf("not bound to lo0: %+v", got)
				}
			})
		})
	}
}

func TestRootScopedRoute(t *testing.T) {
	requireRoot(t)
	route := osnet.Route{Dst: netip.MustParsePrefix("198.51.100.240/28"), Iface: "lo0", Scoped: true}
	checkRouteLifecycle(t, NewRouteTable(), route, func(t *testing.T, got osnet.Route) {
		if !got.Scoped || got.Iface != "lo0" {
			t.Errorf("not scoped to lo0: %+v", got)
		}
	})
}

// The kernel does not insist that a gateway is on a connected network: while
// any default route exists, a gateway that is only reachable through it is
// accepted (ENETUNREACH needs no route to the gateway at all, which cannot be
// arranged on a host in use). This is how a route through a gateway of a
// network the host has left stays in the table instead of failing, so
// ErrUnreachable is not a defence against stale routes and the Reconciler has
// to find them itself.
func TestRootGatewayBehindTheDefaultRouteIsAccepted(t *testing.T) {
	requireRoot(t)
	table := NewRouteTable()
	route := osnet.Route{Dst: netip.MustParsePrefix("198.51.100.32/28"), Gateway: netip.MustParseAddr("203.0.113.250")}
	checkRouteLifecycle(t, table, route, func(t *testing.T, got osnet.Route) {
		if got.Gateway != route.Gateway {
			t.Errorf("gateway = %v, want %v: %+v", got.Gateway, route.Gateway, got)
		}
	})
}

func TestRootDeleteOfAnAbsentRoute(t *testing.T) {
	requireRoot(t)
	table := NewRouteTable()
	for _, prefix := range []string{"198.51.100.96/28", "198.51.100.99/32", "2001:db8:ffff:3::/64"} {
		if err := table.Delete(osnet.Route{Dst: netip.MustParsePrefix(prefix), Blackhole: true}); !errors.Is(err, osnet.ErrNotFound) {
			t.Errorf("Delete %s = %v, want ErrNotFound", prefix, err)
		}
	}
}

// resolverFor returns the unscoped resolver that configd made for domain.
func resolverFor(t *testing.T, domain string) (resolver, bool) {
	t.Helper()
	out, err := runCommand(context.Background(), scutilPath, []string{"--dns"}, "")
	if err != nil {
		t.Fatalf("scutil --dns: %v: %s", err, out)
	}
	for _, r := range parseResolvers(t, string(out)) {
		if r.Domain == domain && !r.Scoped {
			return r, true
		}
	}
	return resolver{}, false
}

// waitForResolver waits for configd to publish the resolver of domain, or to
// drop it, which it does a moment after the dynamic store changed.
func waitForResolver(t *testing.T, domain string, present bool) resolver {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if r, ok := resolverFor(t, domain); ok == present {
			return r
		}
	}
	t.Fatalf("scutil --dns did not show the resolver of %s present=%v", domain, present)
	return resolver{}
}

func expectResolver(t *testing.T, domain, server string, order int) {
	t.Helper()
	r := waitForResolver(t, domain, true)
	if !slices.Equal(r.Nameservers, []string{server}) || r.Order != order || !r.Supplemental {
		t.Errorf("resolver of %s = %+v, want nameserver %s, order %d, supplemental", domain, r, server, order)
	}
}

// The owner is plaitway-test, the domains are .invalid and the server is in
// TEST-NET-1: no real name is resolved through this. The catch-all form (".")
// is not tested because it would put an unreachable resolver in front of
// every query of the host.
func TestRootDNS(t *testing.T) {
	requireRoot(t)
	const owner = "plaitway-test"
	d := NewDNS(DNSOptions{})
	if err := d.Remove(owner); err != nil {
		t.Fatalf("clean start: %v", err)
	}
	t.Cleanup(func() {
		if err := d.Remove(owner); err != nil {
			t.Errorf("clean up: %v", err)
		}
	})
	server := netip.MustParseAddr("192.0.2.53")

	one := []osnet.DNSEntry{{Servers: []netip.Addr{server}, MatchDomains: []string{"plaitway-test-a.invalid", "plaitway-test-b.invalid"}, Order: 50000}}
	if err := d.Apply(owner, one); err != nil {
		t.Fatal(err)
	}
	owned, err := d.Owned()
	if err != nil {
		t.Fatal(err)
	}
	if want := dnsKeyName(owner, 0); !slices.Contains(owned, want) {
		t.Errorf("Owned = %v, does not list %s", owned, want)
	}
	expectResolver(t, "plaitway-test-a.invalid", "192.0.2.53", 50000)
	expectResolver(t, "plaitway-test-b.invalid", "192.0.2.53", 50000)

	two := []osnet.DNSEntry{
		{Servers: []netip.Addr{server}, MatchDomains: []string{"plaitway-test-c.invalid"}, Order: 50000},
		{Servers: []netip.Addr{netip.MustParseAddr("2001:db8::53")}, MatchDomains: []string{"plaitway-test-d.invalid"}, Order: 50100},
	}
	if err := d.Apply(owner, two); err != nil {
		t.Fatal(err)
	}
	expectResolver(t, "plaitway-test-c.invalid", "192.0.2.53", 50000)
	expectResolver(t, "plaitway-test-d.invalid", "2001:db8::53", 50100)
	waitForResolver(t, "plaitway-test-a.invalid", false)
	waitForResolver(t, "plaitway-test-b.invalid", false)

	if err := d.Apply(owner, one[:1]); err != nil {
		t.Fatal(err)
	}
	if owned, err = d.Owned(); err != nil || slices.Contains(owned, dnsKeyName(owner, 1)) {
		t.Errorf("after replacing two keys with one: Owned = %v, %v", owned, err)
	}

	if err := d.Remove(owner); err != nil {
		t.Fatal(err)
	}
	if owned, err = d.Owned(); err != nil || slices.Contains(owned, dnsKeyName(owner, 0)) {
		t.Errorf("after Remove: Owned = %v, %v", owned, err)
	}
	for _, domain := range []string{"plaitway-test-a.invalid", "plaitway-test-b.invalid", "plaitway-test-c.invalid", "plaitway-test-d.invalid"} {
		waitForResolver(t, domain, false)
	}
	if err := d.Remove(owner); err != nil {
		t.Errorf("removing nothing: %v", err)
	}
	if err := d.Flush(); err != nil {
		t.Errorf("Flush: %v", err)
	}
}
