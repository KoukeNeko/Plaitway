//go:build rootintegration && linux

package linux

// These tests change the routing table, the links and the addresses of the
// network namespace they run in, so they refuse to run anywhere else: they need
// the rootintegration build tag, PLAITWAY_ROOT_TESTS=1, root, and a namespace
// with no interface but lo. Run them in a private user and network namespace,
// which needs no privileges:
//
//	go test -c -tags rootintegration -o linux.test ./internal/osnet/linux
//	unshare -Urn sh -c 'ip link set lo up; PLAITWAY_ROOT_TESTS=1 ./linux.test -test.v'
//
// ip(8) from iproute2 sets up and checks what the code under test did. What the
// tests add lives in the documentation ranges 198.51.100.0/24, 203.0.113.0/24 and
// 2001:db8::/32.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

var (
	netnsOnce sync.Once
	netnsErr  error
)

// requireNetns refuses to go on anywhere but in a namespace that has nothing
// in it but loopback, and puts the namespace back to that when the test is done.
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
			{"-4", "route", "flush", "table", "main"}, {"-6", "route", "flush", "table", "main"},
			{"-4", "route", "flush", "table", "100"}, {"-6", "route", "flush", "table", "100"},
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

// runIP runs ip(8) and fails the test when it fails.
func runIP(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("ip", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("ip %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// newDummy creates an interface that is up and has carrier, with addresses
// (IPv6 ones without duplicate address detection), and returns its index.
func newDummy(t *testing.T, name string, addrs ...string) uint32 {
	t.Helper()
	runIP(t, "link", "add", name, "type", "dummy")
	runIP(t, "link", "set", name, "up")
	for _, a := range addrs {
		if strings.Contains(a, ":") {
			runIP(t, "addr", "add", a, "dev", name, "nodad")
		} else {
			runIP(t, "addr", "add", a, "dev", name)
		}
	}
	return ifIndexOf(t, name)
}

func ifIndexOf(t *testing.T, name string) uint32 {
	t.Helper()
	iface, err := net.InterfaceByName(name)
	if err != nil {
		t.Fatal(err)
	}
	return uint32(iface.Index)
}

func dumpRoutes(t *testing.T) []osnet.Route {
	t.Helper()
	routes, err := NewRouteTable().Dump()
	if err != nil {
		t.Fatal(err)
	}
	return routes
}

// routesTo returns the routes of the table to dst.
func routesTo(t *testing.T, dst string) []osnet.Route {
	t.Helper()
	return slices.DeleteFunc(dumpRoutes(t), func(r osnet.Route) bool { return r.Dst != netip.MustParsePrefix(dst) })
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

// kernelRoutes is what ip(8) shows for the routes of a protocol, one line per route.
func kernelRoutes(t *testing.T, family string, args ...string) []string {
	t.Helper()
	out := runIP(t, append([]string{family, "route", "show"}, args...)...)
	return strings.Split(strings.TrimSpace(out), "\n")
}

// A route is added, shows up in the table as what it is, cannot be added twice,
// is deleted from the table's own copy of it, and cannot be deleted twice.
func checkRouteLifecycle(t *testing.T, table osnet.RouteTable, route osnet.Route, check func(t *testing.T, got osnet.Route)) osnet.Route {
	t.Helper()
	cleanUp(t, table, route)
	if before := routesTo(t, route.Dst.String()); len(before) != 0 {
		t.Fatalf("%s is in the table before the test: %+v", route.Dst, before)
	}
	if err := table.Add(route); err != nil {
		t.Fatalf("Add: %v", err)
	}
	found := routesTo(t, route.Dst.String())
	if len(found) != 1 {
		t.Fatalf("%s: %d routes after Add: %+v", route.Dst, len(found), found)
	}
	got := found[0]
	if !got.Static || got.Flags>>8&0xff != RouteProtocol {
		t.Errorf("a route we added is not static and ours: %+v", got)
	}
	check(t, got)
	if err := table.Add(route); !errors.Is(err, osnet.ErrExists) {
		t.Errorf("second Add = %v, want ErrExists", err)
	}
	if err := table.Delete(got); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if still := routesTo(t, route.Dst.String()); len(still) != 0 {
		t.Errorf("%s is still in the table after Delete: %+v", route.Dst, still)
	}
	if err := table.Delete(got); !errors.Is(err, osnet.ErrNotFound) {
		t.Errorf("second Delete = %v, want ErrNotFound", err)
	}
	return got
}

func TestRootIPv4RouteLifecycle(t *testing.T) {
	requireNetns(t)
	table := NewRouteTable()
	d0 := newDummy(t, "d0", "192.0.2.2/24")
	d1 := newDummy(t, "d1", "198.18.0.2/24")

	t.Run("through a gateway", func(t *testing.T) {
		route := osnet.Route{Dst: prefix("198.51.100.0/24"), Gateway: addr("192.0.2.1"), IfIndex: d0, Metric: 5}
		checkRouteLifecycle(t, table, route, func(t *testing.T, got osnet.Route) {
			if got.Gateway != route.Gateway || got.Iface != "d0" || got.IfIndex != d0 || got.Metric != 5 || got.Blackhole || got.Scoped {
				t.Errorf("got %+v", got)
			}
			if want := uint32(rtnUnicast)<<16 | RouteProtocol<<8 | rtScopeUniverse; got.Flags != want {
				t.Errorf("flags %#x, want %#x", got.Flags, want)
			}
		})
	})
	t.Run("bound to an interface, named by name", func(t *testing.T) {
		route := osnet.Route{Dst: prefix("198.51.100.0/24"), Iface: "d1", Metric: 7}
		checkRouteLifecycle(t, table, route, func(t *testing.T, got osnet.Route) {
			if got.Gateway.IsValid() || got.Iface != "d1" || got.IfIndex != d1 || got.Metric != 7 {
				t.Errorf("got %+v", got)
			}
			if want := uint32(rtnUnicast)<<16 | RouteProtocol<<8 | rtScopeLink; got.Flags != want {
				t.Errorf("flags %#x, want %#x", got.Flags, want)
			}
		})
	})
	t.Run("host route through a gateway without naming the interface", func(t *testing.T) {
		route := osnet.Route{Dst: prefix("198.51.100.7/32"), Gateway: addr("192.0.2.1"), Metric: 1}
		checkRouteLifecycle(t, table, route, func(t *testing.T, got osnet.Route) {
			if got.Gateway != route.Gateway || got.Iface != "d0" || got.Metric != 1 {
				t.Errorf("got %+v", got)
			}
		})
	})
	t.Run("blackhole", func(t *testing.T) {
		route := osnet.Route{Dst: prefix("198.51.100.128/28"), Blackhole: true}
		checkRouteLifecycle(t, table, route, func(t *testing.T, got osnet.Route) {
			if !got.Blackhole || got.Gateway.IsValid() {
				t.Errorf("got %+v", got)
			}
		})
	})
	t.Run("default route", func(t *testing.T) {
		route := osnet.Route{Dst: prefix("0.0.0.0/0"), Gateway: addr("192.0.2.1"), IfIndex: d0, Metric: 300}
		checkRouteLifecycle(t, table, route, func(t *testing.T, got osnet.Route) {
			if got.Gateway != route.Gateway || got.Metric != 300 {
				t.Errorf("got %+v", got)
			}
		})
	})
	t.Run("the kernel shows our protocol", func(t *testing.T) {
		route := osnet.Route{Dst: prefix("198.51.100.0/24"), Gateway: addr("192.0.2.1"), IfIndex: d0, Metric: 5}
		cleanUp(t, table, route)
		if err := table.Add(route); err != nil {
			t.Fatal(err)
		}
		lines := kernelRoutes(t, "-4")
		if !slices.Contains(lines, "198.51.100.0/24 via 192.0.2.1 dev d0 proto 199 metric 5 ") && !slices.Contains(lines, "198.51.100.0/24 via 192.0.2.1 dev d0 proto 199 metric 5") {
			t.Errorf("ip route show: %q", lines)
		}
	})
}

func TestRootIPv6RouteLifecycle(t *testing.T) {
	requireNetns(t)
	table := NewRouteTable()
	d0 := newDummy(t, "d0", "2001:db8:1::2/64")
	d1 := newDummy(t, "d1", "2001:db8:2::2/64")

	t.Run("through a gateway", func(t *testing.T) {
		route := osnet.Route{Dst: prefix("2001:db8:9::/64"), Gateway: addr("2001:db8:1::1"), IfIndex: d0, Metric: 5}
		checkRouteLifecycle(t, table, route, func(t *testing.T, got osnet.Route) {
			if got.Gateway != route.Gateway || got.Iface != "d0" || got.IfIndex != d0 || got.Metric != 5 {
				t.Errorf("got %+v", got)
			}
		})
	})
	t.Run("bound to an interface", func(t *testing.T) {
		route := osnet.Route{Dst: prefix("2001:db8:a::/64"), Iface: "d1", Metric: 5}
		checkRouteLifecycle(t, table, route, func(t *testing.T, got osnet.Route) {
			if got.Gateway.IsValid() || got.Iface != "d1" || got.IfIndex != d1 || got.Metric != 5 {
				t.Errorf("got %+v", got)
			}
		})
	})
	t.Run("host route", func(t *testing.T) {
		route := osnet.Route{Dst: prefix("2001:db8:a::7/128"), Gateway: addr("2001:db8:2::1"), IfIndex: d1, Metric: 1}
		checkRouteLifecycle(t, table, route, func(t *testing.T, got osnet.Route) {
			if got.Gateway != route.Gateway || got.Iface != "d1" {
				t.Errorf("got %+v", got)
			}
		})
	})
	t.Run("blackhole", func(t *testing.T) {
		route := osnet.Route{Dst: prefix("2001:db8:ffff::/48"), Blackhole: true}
		checkRouteLifecycle(t, table, route, func(t *testing.T, got osnet.Route) {
			if !got.Blackhole || got.Gateway.IsValid() {
				t.Errorf("got %+v", got)
			}
		})
	})
	// The normal default route of IPv6 on Linux, as a router advertisement makes it.
	t.Run("default route over a link-local gateway", func(t *testing.T) {
		route := osnet.Route{Dst: prefix("::/0"), Gateway: addr("fe80::1%d0"), IfIndex: d0, Metric: 5}
		checkRouteLifecycle(t, table, route, func(t *testing.T, got osnet.Route) {
			if got.Gateway != addr("fe80::1%d0") || got.Iface != "d0" || got.IfIndex != d0 || got.Metric != 5 {
				t.Errorf("got %+v", got)
			}
		})
	})
	t.Run("link-local gateway whose interface is only its zone", func(t *testing.T) {
		route := osnet.Route{Dst: prefix("::/0"), Gateway: addr("fe80::1%d1"), Metric: 5}
		checkRouteLifecycle(t, table, route, func(t *testing.T, got osnet.Route) {
			if got.Gateway != addr("fe80::1%d1") || got.Iface != "d1" {
				t.Errorf("got %+v", got)
			}
		})
	})
	t.Run("the kernel shows our protocol", func(t *testing.T) {
		route := osnet.Route{Dst: prefix("::/0"), Gateway: addr("fe80::1%d0"), IfIndex: d0, Metric: 5}
		cleanUp(t, table, route)
		if err := table.Add(route); err != nil {
			t.Fatal(err)
		}
		lines := kernelRoutes(t, "-6")
		if !slices.Contains(lines, "default via fe80::1 dev d0 proto 199 metric 5 pref medium") {
			t.Errorf("ip -6 route show: %q", lines)
		}
	})
}

// The Reconciler keys a route by destination, interface and gateway, and the
// kernel keeps several routes to one prefix apart by their metric.
func TestRootSeveralRoutesToOnePrefix(t *testing.T) {
	requireNetns(t)
	table := NewRouteTable()
	d0 := newDummy(t, "d0", "192.0.2.2/24", "2001:db8:1::2/64")
	d1 := newDummy(t, "d1", "198.18.0.2/24", "2001:db8:2::2/64")

	for _, tt := range []struct {
		name         string
		dst          string
		gw0, gw1     string
		wrongGateway string
	}{
		{"IPv4", "198.51.100.0/24", "192.0.2.1", "198.18.0.1", "192.0.2.9"},
		{"IPv6", "2001:db8:9::/64", "2001:db8:1::1", "2001:db8:2::1", "2001:db8:1::9"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			first := osnet.Route{Dst: prefix(tt.dst), Gateway: addr(tt.gw0), IfIndex: d0, Metric: 5}
			second := osnet.Route{Dst: prefix(tt.dst), Gateway: addr(tt.gw1), IfIndex: d1, Metric: 6}
			cleanUp(t, table, first)
			cleanUp(t, table, second)
			for _, r := range []osnet.Route{second, first} { // added in the other order than they sort
				if err := table.Add(r); err != nil {
					t.Fatalf("Add %+v: %v", r, err)
				}
			}
			got := routesTo(t, tt.dst)
			if len(got) != 2 || got[0].IfIndex != d0 || got[0].Metric != 5 || got[1].IfIndex != d1 || got[1].Metric != 6 {
				t.Fatalf("two routes to one prefix: %+v", got)
			}

			// The key is destination, interface and gateway; the metric ranks.
			sameMetric := osnet.Route{Dst: prefix(tt.dst), Gateway: addr(tt.gw1), IfIndex: d1, Metric: 5}
			if err := table.Add(sameMetric); !errors.Is(err, osnet.ErrExists) {
				t.Errorf("Add with the metric of another route = %v, want ErrExists", err)
			}
			for name, wrong := range map[string]osnet.Route{
				"metric":    {Dst: prefix(tt.dst), Gateway: addr(tt.gw0), IfIndex: d0, Metric: 9},
				"gateway":   {Dst: prefix(tt.dst), Gateway: addr(tt.wrongGateway), IfIndex: d0, Metric: 5},
				"interface": {Dst: prefix(tt.dst), Gateway: addr(tt.gw0), IfIndex: d1, Metric: 5},
			} {
				if err := table.Delete(wrong); !errors.Is(err, osnet.ErrNotFound) {
					t.Errorf("Delete with the wrong %s = %v, want ErrNotFound", name, err)
				}
			}
			if got := routesTo(t, tt.dst); len(got) != 2 {
				t.Fatalf("a delete that named no route removed one: %+v", got)
			}

			if err := table.Delete(first); err != nil {
				t.Fatal(err)
			}
			if got := routesTo(t, tt.dst); len(got) != 1 || got[0].IfIndex != d1 {
				t.Errorf("after deleting the first: %+v", got)
			}
		})
	}
}

// Another program can change the protocol of a route we added. It is still the
// route with our key, and Delete finds it.
func TestRootDeleteMatchesWhateverProtocol(t *testing.T) {
	requireNetns(t)
	table := NewRouteTable()
	d0 := newDummy(t, "d0", "192.0.2.2/24")
	route := osnet.Route{Dst: prefix("198.51.100.0/24"), Gateway: addr("192.0.2.1"), IfIndex: d0, Metric: 5}
	cleanUp(t, table, route)
	if err := table.Add(route); err != nil {
		t.Fatal(err)
	}
	runIP(t, "route", "replace", "198.51.100.0/24", "via", "192.0.2.1", "dev", "d0", "metric", "5", "proto", "static")
	if got := routesTo(t, "198.51.100.0/24"); len(got) != 1 || got[0].Flags>>8&0xff != 4 {
		t.Fatalf("the route was not replaced by a static one: %+v", got)
	}
	if err := table.Delete(route); err != nil {
		t.Fatalf("Delete of a route whose protocol changed: %v", err)
	}
	if got := routesTo(t, "198.51.100.0/24"); len(got) != 0 {
		t.Errorf("still there: %+v", got)
	}
}

func TestRootErrors(t *testing.T) {
	requireNetns(t)
	table := NewRouteTable()
	d0 := newDummy(t, "d0", "192.0.2.2/24", "2001:db8:1::2/64")
	runIP(t, "link", "add", "down0", "type", "dummy")

	t.Run("a gateway on no connected network is unreachable", func(t *testing.T) {
		for _, route := range []osnet.Route{
			{Dst: prefix("198.51.100.0/24"), Gateway: addr("203.0.113.9"), IfIndex: d0, Metric: 5},
			{Dst: prefix("198.51.100.0/24"), Gateway: addr("203.0.113.9"), Metric: 5},
			{Dst: prefix("2001:db8:9::/64"), Gateway: addr("2001:db8:77::1"), IfIndex: d0, Metric: 5},
		} {
			cleanUp(t, table, route)
			if err := table.Add(route); !errors.Is(err, osnet.ErrUnreachable) {
				t.Errorf("Add %+v = %v, want ErrUnreachable", route, err)
			}
		}
	})
	t.Run("routes of an interface that does not exist", func(t *testing.T) {
		route := osnet.Route{Dst: prefix("198.51.100.0/24"), Iface: "nosuch"}
		err := table.Add(route)
		if err == nil || !strings.Contains(err.Error(), "nosuch") || !errors.Is(err, errNoSuchInterface) {
			t.Errorf("Add on an unknown interface name = %v", err)
		}
		if err := table.Delete(route); !errors.Is(err, osnet.ErrNotFound) {
			t.Errorf("Delete on an unknown interface name = %v, want ErrNotFound", err)
		}
		for _, dst := range []string{"198.51.100.0/24", "2001:db8:9::/64"} {
			if err := table.Delete(osnet.Route{Dst: prefix(dst), IfIndex: 9999}); !errors.Is(err, osnet.ErrNotFound) {
				t.Errorf("Delete %s on an index that does not exist = %v, want ErrNotFound", dst, err)
			}
		}
	})
	t.Run("the kernel explains a refusal", func(t *testing.T) {
		err := table.Add(osnet.Route{Dst: prefix("198.51.100.0/24"), Iface: "down0"})
		if !errors.Is(err, syscall.ENETDOWN) || !strings.Contains(err.Error(), "Device for nexthop is not up") || !strings.Contains(err.Error(), "add route 198.51.100.0/24") {
			t.Errorf("Add on a down interface = %v", err)
		}
	})
	t.Run("a route without destination or next hop", func(t *testing.T) {
		if err := table.Add(osnet.Route{}); err == nil {
			t.Error("Add of nothing succeeded")
		}
		if err := table.Add(osnet.Route{Dst: prefix("198.51.100.0/24")}); err == nil {
			t.Error("Add of a route that leads nowhere succeeded")
		}
	})
}

func TestRootDumpListsTheMainTable(t *testing.T) {
	requireNetns(t)
	d0 := newDummy(t, "d0", "192.0.2.2/24", "2001:db8:1::2/64")
	d1 := newDummy(t, "d1", "198.18.0.2/24", "2001:db8:2::2/64")
	runIP(t, "route", "add", "default", "via", "192.0.2.1", "dev", "d0", "proto", "dhcp", "metric", "100")
	runIP(t, "route", "add", "198.51.100.64/28", "via", "198.18.0.1", "proto", "199", "metric", "5")
	runIP(t, "route", "add", "blackhole", "198.51.100.128/28", "proto", "199")
	runIP(t, "route", "add", "unreachable", "198.51.100.144/28")
	runIP(t, "route", "add", "prohibit", "198.51.100.160/28")
	runIP(t, "route", "add", "203.0.113.0/24", "nexthop", "via", "192.0.2.1", "dev", "d0", "weight", "1", "nexthop", "via", "198.18.0.1", "dev", "d1", "weight", "2")
	runIP(t, "route", "add", "203.0.113.64/26", "via", "192.0.2.1", "dev", "d0", "table", "100")
	runIP(t, "-6", "route", "add", "default", "via", "fe80::1", "dev", "d0", "proto", "ra", "metric", "1024")
	runIP(t, "-6", "route", "add", "default", "via", "fe80::1", "dev", "d0", "proto", "199", "metric", "5")
	runIP(t, "-6", "route", "add", "2001:db8:b::/64", "nexthop", "via", "fe80::1", "dev", "d0", "nexthop", "via", "fe80::2", "dev", "d1")
	runIP(t, "-6", "route", "add", "unreachable", "2001:db8:c::/64", "metric", "7")
	runIP(t, "-6", "route", "add", "2001:db8:d::/64", "via", "2001:db8:2::1", "table", "100")

	const (
		unicast     = uint32(rtnUnicast) << 16
		kernel      = uint32(rtprotKernel) << 8
		boot        = 3 << 8
		ours        = RouteProtocol << 8
		link        = rtScopeLink
		unicastBoot = unicast | boot
	)
	want := []osnet.Route{
		{Dst: prefix("0.0.0.0/0"), Gateway: addr("192.0.2.1"), Iface: "d0", IfIndex: d0, Metric: 100, Static: true, Flags: unicast | 16<<8},
		{Dst: prefix("192.0.2.0/24"), Iface: "d0", IfIndex: d0, Flags: unicast | kernel | link},
		{Dst: prefix("198.18.0.0/24"), Iface: "d1", IfIndex: d1, Flags: unicast | kernel | link},
		{Dst: prefix("198.51.100.64/28"), Gateway: addr("198.18.0.1"), Iface: "d1", IfIndex: d1, Metric: 5, Static: true, Flags: unicast | ours},
		{Dst: prefix("198.51.100.128/28"), Blackhole: true, Static: true, Flags: uint32(rtnBlackhole)<<16 | ours},
		{Dst: prefix("198.51.100.144/28"), Blackhole: true, Static: true, Flags: uint32(rtnUnreachable)<<16 | boot},
		{Dst: prefix("198.51.100.160/28"), Blackhole: true, Static: true, Flags: uint32(rtnProhibit)<<16 | boot},
		{Dst: prefix("203.0.113.0/24"), Gateway: addr("192.0.2.1"), Iface: "d0", IfIndex: d0, Static: true, Flags: unicastBoot},
		{Dst: prefix("203.0.113.0/24"), Gateway: addr("198.18.0.1"), Iface: "d1", IfIndex: d1, Static: true, Flags: unicastBoot},
		{Dst: prefix("::/0"), Gateway: addr("fe80::1%d0"), Iface: "d0", IfIndex: d0, Metric: 5, Static: true, Flags: unicast | ours},
		{Dst: prefix("::/0"), Gateway: addr("fe80::1%d0"), Iface: "d0", IfIndex: d0, Metric: 1024, Flags: unicast | rtprotRA<<8},
		{Dst: prefix("2001:db8:1::/64"), Iface: "d0", IfIndex: d0, Metric: 256, Flags: unicast | kernel},
		{Dst: prefix("2001:db8:2::/64"), Iface: "d1", IfIndex: d1, Metric: 256, Flags: unicast | kernel},
		{Dst: prefix("2001:db8:b::/64"), Gateway: addr("fe80::1%d0"), Iface: "d0", IfIndex: d0, Metric: 1024, Static: true, Flags: unicastBoot},
		{Dst: prefix("2001:db8:b::/64"), Gateway: addr("fe80::2%d1"), Iface: "d1", IfIndex: d1, Metric: 1024, Static: true, Flags: unicastBoot},
		{Dst: prefix("2001:db8:c::/64"), Iface: "lo", IfIndex: 1, Metric: 7, Blackhole: true, Static: true, Flags: uint32(rtnUnreachable)<<16 | boot},
		{Dst: prefix("fe80::/64"), Iface: "d0", IfIndex: d0, Metric: 256, Flags: unicast | kernel},
		{Dst: prefix("fe80::/64"), Iface: "d1", IfIndex: d1, Metric: 256, Flags: unicast | kernel},
	}
	got := dumpRoutes(t)
	sortRoutes(want)
	if !slices.Equal(got, want) {
		t.Errorf("Dump:\n got %d routes\n%s\n want %d routes\n%s", len(got), formatRoutes(got), len(want), formatRoutes(want))
	}
	if again := dumpRoutes(t); !slices.Equal(again, got) {
		t.Errorf("a second Dump of the same table differs:\n%s\n%s", formatRoutes(got), formatRoutes(again))
	}
}

func formatRoutes(routes []osnet.Route) string {
	var b strings.Builder
	for _, r := range routes {
		fmt.Fprintf(&b, "\t%+v\n", r)
	}
	return b.String()
}

// tryIP runs ip(8) and returns its failure.
func tryIP(args ...string) error {
	if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("ip %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// holdTun creates a tun device and keeps it open, so that it has carrier like
// the device of a VPN engine that runs. It lives until the test ends.
func holdTun(t *testing.T, name string) uint32 {
	t.Helper()
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("open /dev/net/tun: %v", err)
	}
	t.Cleanup(func() { unix.Close(fd) })
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		t.Fatal(err)
	}
	ifr.SetUint16(unix.IFF_TUN | unix.IFF_NO_PI)
	if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, ifr); err != nil {
		t.Fatalf("create tun device %s: %v", name, err)
	}
	return ifIndexOf(t, name)
}

func snapshot(t *testing.T) osnet.NetState {
	t.Helper()
	state, err := NewNetMonitor(NetMonitorOptions{Logger: discardLog()}).Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func interfaceNamed(t *testing.T, state osnet.NetState, name string) osnet.Interface {
	t.Helper()
	for _, iface := range state.Interfaces {
		if iface.Name == name {
			return iface
		}
	}
	t.Fatalf("no interface %s in %+v", name, state.Interfaces)
	return osnet.Interface{}
}

// withoutLinkLocal drops the addresses that the kernel makes up for an interface.
func withoutLinkLocal(addrs []netip.Prefix) []netip.Prefix {
	return slices.DeleteFunc(slices.Clone(addrs), func(p netip.Prefix) bool { return p.Addr().IsLinkLocalUnicast() })
}

func TestRootInterfaces(t *testing.T) {
	requireNetns(t)
	runIP(t, "link", "set", "lo", "up")
	d0 := newDummy(t, "d0", "192.0.2.2/24", "2001:db8:1::2/64")
	runIP(t, "link", "add", "d1", "type", "dummy") // down
	holdTun(t, "tun7")
	runIP(t, "link", "set", "tun7", "up")
	runIP(t, "addr", "add", "10.8.0.2", "peer", "10.8.0.1/32", "dev", "tun7")
	runIP(t, "addr", "add", "2001:db8:8::2/64", "dev", "tun7", "nodad")
	runIP(t, "tuntap", "add", "dev", "tun8", "mode", "tun") // nobody holds it open: no carrier
	runIP(t, "link", "set", "tun8", "up")
	runIP(t, "tuntap", "add", "dev", "tap0", "mode", "tap")
	runIP(t, "link", "set", "tap0", "up")
	runIP(t, "link", "add", "wg0", "type", "wireguard")
	runIP(t, "link", "set", "wg0", "up")
	runIP(t, "addr", "add", "10.9.0.2/32", "dev", "wg0")
	runIP(t, "link", "add", "br0", "type", "bridge")
	runIP(t, "link", "set", "br0", "up")
	runIP(t, "addr", "add", "172.17.0.1/16", "dev", "br0")
	runIP(t, "link", "add", "v0", "type", "veth", "peer", "name", "v1")
	runIP(t, "link", "set", "v0", "up")
	runIP(t, "link", "set", "v1", "up")
	runIP(t, "link", "add", "link", "d0", "name", "d0.100", "type", "vlan", "id", "100")
	runIP(t, "link", "set", "d0.100", "up")
	// Encapsulating devices need kernel modules that a minimal kernel may lack.
	optional := map[string][]string{
		"gre1":  {"link", "add", "gre1", "type", "gre", "local", "192.0.2.2", "remote", "192.0.2.99"},
		"ipip1": {"link", "add", "ipip1", "type", "ipip", "local", "192.0.2.2", "remote", "192.0.2.99"},
		"sit1":  {"link", "add", "sit1", "type", "sit", "local", "192.0.2.2", "remote", "192.0.2.99"},
		"vx0":   {"link", "add", "vx0", "type", "vxlan", "id", "42", "local", "192.0.2.2", "dstport", "4789"},
	}
	for name, args := range optional {
		if err := tryIP(args...); err != nil {
			t.Logf("no %s on this kernel: %v", name, err)
			delete(optional, name)
		}
	}

	state := snapshot(t)
	tests := []struct {
		name   string
		up     bool
		tunnel bool
		addrs  []netip.Prefix
	}{
		{"lo", true, false, prefixes("127.0.0.1/8 ::1/128")},
		{"d0", true, false, prefixes("192.0.2.2/24 2001:db8:1::2/64")},
		{"d1", false, false, nil},
		{"tun7", true, true, prefixes("10.8.0.2/32 2001:db8:8::2/64")},
		{"tun8", false, true, nil},
		{"tap0", false, true, nil},
		{"wg0", true, true, prefixes("10.9.0.2/32")},
		{"br0", true, false, prefixes("172.17.0.1/16")},
		{"v0", true, false, nil},
		{"v1", true, false, nil},
		{"d0.100", true, false, nil},
	}
	for name := range optional {
		tests = append(tests, struct {
			name   string
			up     bool
			tunnel bool
			addrs  []netip.Prefix
		}{name, false, true, nil})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := interfaceNamed(t, state, tt.name)
			if got.Index != int(ifIndexOf(t, tt.name)) || got.Up != tt.up {
				t.Errorf("got index %d, up %v: %+v", got.Index, got.Up, got)
			}
			if got.Tunnel != tt.tunnel {
				t.Errorf("tunnel = %v, want %v", got.Tunnel, tt.tunnel)
			}
			if addrs := withoutLinkLocal(got.Addrs); !slices.Equal(addrs, tt.addrs) {
				t.Errorf("addresses %v, want %v", addrs, tt.addrs)
			}
			if got.Metric != 0 {
				t.Errorf("metric %d: Linux has no interface metric", got.Metric)
			}
		})
	}

	// The kernel gave d0 a link-local address; it is listed, and listed sorted.
	d0iface := interfaceNamed(t, state, "d0")
	if len(d0iface.Addrs) != 3 || !d0iface.Addrs[2].Addr().IsLinkLocalUnicast() || !slices.IsSortedFunc(d0iface.Addrs, comparePrefix) {
		t.Errorf("addresses of d0: %v", d0iface.Addrs)
	}
	if got := interfaceNamed(t, state, "d0.100"); got.Index == int(d0) || !got.Up {
		t.Errorf("d0.100: %+v", got)
	}

	// What the Reconciler may count as connected: the uplinks that are up.
	want := prefixes("172.17.0.0/16 192.0.2.0/24 2001:db8:1::/64")
	if !slices.Equal(state.Connected, want) {
		t.Errorf("connected %v, want %v", state.Connected, want)
	}
}

func TestRootSnapshotPicksTheDefaultRoute(t *testing.T) {
	requireNetns(t)
	table := NewRouteTable()
	d0 := newDummy(t, "d0", "192.0.2.2/24", "2001:db8:1::2/64")
	d1 := newDummy(t, "d1", "198.18.0.2/24", "2001:db8:2::2/64")
	holdTun(t, "tun7")
	runIP(t, "link", "set", "tun7", "up")
	runIP(t, "addr", "add", "10.8.0.2", "peer", "10.8.0.1/32", "dev", "tun7")
	runIP(t, "tuntap", "add", "dev", "tap1", "mode", "tap") // up, but no carrier
	runIP(t, "link", "set", "tap1", "up")
	runIP(t, "addr", "add", "198.19.0.2/24", "dev", "tap1")

	defaultV4 := func(t *testing.T) *osnet.Nexthop {
		t.Helper()
		return snapshot(t).DefaultV4
	}
	expect := func(t *testing.T, got *osnet.Nexthop, gateway, iface string) {
		t.Helper()
		var want netip.Addr
		if gateway != "" {
			want = addr(gateway)
		}
		if got == nil || got.Gateway != want || got.Iface != iface {
			t.Errorf("default = %+v, want gateway %q through %s", got, gateway, iface)
		}
	}

	if got := snapshot(t); got.DefaultV4 != nil || got.DefaultV6 != nil {
		t.Fatalf("defaults without routes: %+v, %+v", got.DefaultV4, got.DefaultV6)
	}
	runIP(t, "route", "add", "default", "via", "198.18.0.1", "dev", "d1", "metric", "700")
	expect(t, defaultV4(t), "198.18.0.1", "d1")
	runIP(t, "route", "add", "default", "via", "192.0.2.1", "dev", "d0", "metric", "100")
	expect(t, defaultV4(t), "192.0.2.1", "d0")

	// A tunnel, however eager, is not the way out; nor is an interface without carrier.
	runIP(t, "route", "add", "default", "dev", "tun7", "metric", "1")
	runIP(t, "route", "add", "default", "via", "198.19.0.1", "dev", "tap1", "metric", "2")
	runIP(t, "route", "add", "blackhole", "default", "metric", "3")
	expect(t, defaultV4(t), "192.0.2.1", "d0")

	// Our own default, through Reconciler's table, ranks like any other.
	own := osnet.Route{Dst: prefix("0.0.0.0/0"), Gateway: addr("198.18.0.1"), IfIndex: d1, Metric: 50}
	cleanUp(t, table, own)
	if err := table.Add(own); err != nil {
		t.Fatal(err)
	}
	expect(t, defaultV4(t), "198.18.0.1", "d1")
	if err := table.Delete(own); err != nil {
		t.Fatal(err)
	}

	// Equal metrics, which only an appended route can have: the lower interface index.
	runIP(t, "route", "append", "default", "via", "198.18.0.1", "dev", "d1", "metric", "100")
	if d0 >= d1 {
		t.Fatalf("test setup: d0 has index %d, d1 %d", d0, d1)
	}
	expect(t, defaultV4(t), "192.0.2.1", "d0")

	// A default without gateway is on-link, as over a PPP or a WWAN link.
	runIP(t, "route", "add", "default", "dev", "d1", "metric", "10")
	expect(t, defaultV4(t), "", "d1")
	runIP(t, "route", "del", "default", "dev", "d1", "metric", "10")

	// IPv6: the usual default route goes through a link-local gateway.
	runIP(t, "-6", "route", "add", "default", "via", "fe80::1", "dev", "d0", "metric", "1024")
	runIP(t, "-6", "route", "add", "default", "dev", "tun7", "metric", "1")
	v6 := snapshot(t).DefaultV6
	if v6 == nil || v6.Gateway != addr("fe80::1%d0") || v6.Iface != "d0" {
		t.Errorf("IPv6 default = %+v, want fe80::1%%d0 through d0", v6)
	}

	// The epoch moves with the default route and stays when nothing changed.
	before := snapshot(t)
	if again := snapshot(t); again.Epoch != before.Epoch {
		t.Errorf("epoch moved from %d to %d without a change", before.Epoch, again.Epoch)
	}
	runIP(t, "link", "add", "wg0", "type", "wireguard")
	runIP(t, "link", "set", "wg0", "up")
	runIP(t, "addr", "add", "10.9.0.2/32", "dev", "wg0")
	monitor := NewNetMonitor(NetMonitorOptions{Logger: discardLog()})
	first, err := monitor.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	runIP(t, "link", "add", "wg1", "type", "wireguard")
	runIP(t, "link", "set", "wg1", "up")
	runIP(t, "addr", "add", "10.9.1.2/32", "dev", "wg1")
	runIP(t, "route", "add", "10.9.1.0/24", "dev", "wg1")
	if tunnelOnly, _ := monitor.Snapshot(); tunnelOnly.Epoch != first.Epoch {
		t.Errorf("a new tunnel moved the epoch from %d to %d", first.Epoch, tunnelOnly.Epoch)
	}
	runIP(t, "route", "del", "default", "via", "192.0.2.1", "dev", "d0", "metric", "100")
	if moved, _ := monitor.Snapshot(); moved.Epoch == first.Epoch {
		t.Errorf("the default route changed and the epoch stayed at %d", moved.Epoch)
	}
}

// Events are debounced: a change comes burstQuiet after the last message.
const (
	eventWait = burstQuiet + 2*time.Second
	quietWait = burstQuiet + 600*time.Millisecond
)

// startEvents starts a monitor and waits until its socket is open: the monitor
// opens it in the background, and a change made before is not seen. It changes
// a route on d0 until the monitor reports it, and leaves the channel quiet.
func startEvents(t *testing.T) <-chan osnet.Change {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	events := NewNetMonitor(NetMonitorOptions{Logger: discardLog()}).Events(ctx)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		runIP(t, "route", "add", "198.51.100.250/32", "dev", "d0")
		runIP(t, "route", "del", "198.51.100.250/32", "dev", "d0")
		select {
		case <-events:
			// The change comes after the burst of both messages; let the last of them settle.
			time.Sleep(quietWait)
			drain(events)
			return events
		case <-time.After(eventWait):
		}
	}
	t.Fatal("the monitor never reported a change")
	return nil
}

func drain(events <-chan osnet.Change) {
	for {
		select {
		case <-events:
		default:
			return
		}
	}
}

func expectChange(t *testing.T, events <-chan osnet.Change, what string) {
	t.Helper()
	select {
	case got := <-events:
		if got.Reason != osnet.ChangeRoute {
			t.Errorf("%s: change reason %q, want %q", what, got.Reason, osnet.ChangeRoute)
		}
	case <-time.After(eventWait):
		t.Errorf("%s: no change", what)
	}
	// One burst is one change.
	time.Sleep(quietWait)
	drain(events)
}

func expectQuiet(t *testing.T, events <-chan osnet.Change, what string) {
	t.Helper()
	select {
	case got := <-events:
		t.Errorf("%s: unexpected change %+v", what, got)
	case <-time.After(quietWait):
	}
}

func TestRootEvents(t *testing.T) {
	requireNetns(t)
	table := NewRouteTable()
	newDummy(t, "d0", "192.0.2.2/24", "2001:db8:1::2/64")
	d1 := newDummy(t, "d1", "198.18.0.2/24")
	holdTun(t, "tun7")
	runIP(t, "link", "set", "tun7", "up")
	events := startEvents(t)

	t.Run("what changes the way out is reported", func(t *testing.T) {
		runIP(t, "route", "add", "198.51.100.0/24", "via", "192.0.2.1", "dev", "d0")
		expectChange(t, events, "route added")
		runIP(t, "route", "del", "198.51.100.0/24")
		expectChange(t, events, "route deleted")
		runIP(t, "-6", "route", "add", "default", "via", "fe80::1", "dev", "d0")
		expectChange(t, events, "IPv6 default route added")

		route := osnet.Route{Dst: prefix("198.51.100.64/28"), Gateway: addr("198.18.0.1"), IfIndex: d1, Metric: 5}
		cleanUp(t, table, route)
		if err := table.Add(route); err != nil {
			t.Fatal(err)
		}
		expectChange(t, events, "route added through the route table")

		runIP(t, "addr", "add", "192.0.2.9/24", "dev", "d0")
		expectChange(t, events, "address added")
		runIP(t, "addr", "add", "2001:db8:1::9/64", "dev", "d0", "nodad")
		expectChange(t, events, "IPv6 address added")
		runIP(t, "addr", "del", "192.0.2.9/24", "dev", "d0")
		expectChange(t, events, "address deleted")

		runIP(t, "link", "set", "d1", "down")
		expectChange(t, events, "link down")
		runIP(t, "link", "set", "d1", "up")
		expectChange(t, events, "link up")
		runIP(t, "link", "add", "br0", "type", "bridge")
		expectChange(t, events, "bridge created")

		// A VPN that another program runs connects: its routes may outrank ours.
		runIP(t, "route", "add", "198.51.100.160/28", "dev", "tun7", "metric", "1")
		expectChange(t, events, "route of another program over a tun device")
		runIP(t, "route", "del", "198.51.100.160/28", "dev", "tun7")
		expectChange(t, events, "route of another program removed from a tun device")
	})

	t.Run("what is no way out is not", func(t *testing.T) {
		runIP(t, "route", "add", "203.0.113.0/24", "via", "192.0.2.1", "dev", "d0", "table", "100")
		expectQuiet(t, events, "route in another table")
		runIP(t, "-6", "route", "add", "2001:db8:d::/64", "via", "2001:db8:1::1", "dev", "d0", "table", "100")
		expectQuiet(t, events, "IPv6 route in another table")
		// Loopback and the local table are not flushed with the rest.
		runIP(t, "route", "add", "local", "198.51.100.77", "dev", "lo", "table", "local")
		t.Cleanup(func() {
			exec.Command("ip", "route", "del", "local", "198.51.100.77", "dev", "lo", "table", "local").Run()
		})
		expectQuiet(t, events, "route in the local table")
		runIP(t, "addr", "add", "127.0.0.2/8", "dev", "lo")
		t.Cleanup(func() { exec.Command("ip", "addr", "del", "127.0.0.2/8", "dev", "lo").Run() })
		expectQuiet(t, events, "address on loopback")
		runIP(t, "-6", "addr", "add", "fe80::dead:beef/64", "dev", "d0", "nodad")
		expectQuiet(t, events, "link-local address")
		runIP(t, "route", "add", "169.254.7.0/24", "dev", "d0")
		expectQuiet(t, events, "link-local route")

		runIP(t, "route", "add", "198.51.100.128/28", "dev", "tun7", "proto", strconv.Itoa(RouteProtocol))
		expectQuiet(t, events, "route of ours over a tun device")
		runIP(t, "addr", "add", "10.8.0.2", "peer", "10.8.0.1/32", "dev", "tun7")
		expectQuiet(t, events, "address on a tun device")
		runIP(t, "link", "set", "tun7", "down")
		expectQuiet(t, events, "tun device down")
		runIP(t, "link", "set", "tun7", "up")
		expectQuiet(t, events, "tun device up")
		runIP(t, "link", "add", "wg0", "type", "wireguard")
		runIP(t, "link", "set", "wg0", "up")
		runIP(t, "addr", "add", "10.9.0.2/32", "dev", "wg0")
		runIP(t, "route", "add", "10.9.0.0/24", "dev", "wg0", "proto", strconv.Itoa(RouteProtocol))
		expectQuiet(t, events, "wireguard device with address and route")
		runIP(t, "link", "del", "wg0")
		expectQuiet(t, events, "wireguard device removed")
	})

	// Not even the quiet ones above are quiet because the monitor stopped.
	t.Run("and the monitor is still listening", func(t *testing.T) {
		runIP(t, "route", "add", "198.51.100.0/24", "via", "192.0.2.1", "dev", "d0")
		expectChange(t, events, "route added after all that")
	})
}

// The receive buffer of the netlink socket overflows when messages come faster
// than they are read; the kernel then says so once and drops what did not fit.
func TestRootLostMessagesAreAChange(t *testing.T) {
	requireNetns(t)
	table := NewRouteTable()
	newDummy(t, "d0", "192.0.2.2/24")
	tun := holdTun(t, "tun7")
	runIP(t, "link", "set", "tun7", "up")

	conn, err := openNetlink(netlinkGroups)
	if err != nil {
		t.Fatal(err)
	}
	// The smallest buffer the kernel allows: a few messages.
	if err := unix.SetsockoptInt(int(connFd(t, conn)), unix.SOL_SOCKET, unix.SO_RCVBUF, 1); err != nil {
		t.Fatal(err)
	}
	// Nobody reads while the table changes a few hundred times. The routes are
	// over a tunnel, which is no change by itself: the one change that comes is
	// the report of the lost messages.
	for i := range 300 {
		route := osnet.Route{Dst: netip.PrefixFrom(netip.AddrFrom4([4]byte{198, 51, 100, byte(i)}), 32), IfIndex: tun, Metric: 5}
		if err := table.Add(route); err != nil {
			t.Fatal(err)
		}
		if err := table.Delete(route); err != nil {
			t.Fatal(err)
		}
	}

	var logged lockedBuffer
	log := slog.New(slog.NewTextHandler(&logged, nil))
	changes := make(chan struct{}, 1000)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- watchConn(ctx, conn, log, func() { changes <- struct{}{} })
	}()
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if ok() {
				return
			}
		}
		t.Fatalf("timed out waiting for %s; log: %s", what, logged.String())
	}
	waitFor("the report of lost messages", func() bool { return strings.Contains(logged.String(), "were lost") })
	waitFor("the change for them", func() bool { return len(changes) == 1 })
	// What was buffered is read and is no change.
	time.Sleep(300 * time.Millisecond)
	if n := len(changes); n != 1 {
		t.Errorf("%d changes after the overflow, want the one for the lost messages", n)
	}

	// The socket goes on working after the overflow.
	runIP(t, "route", "add", "198.51.100.0/24", "via", "192.0.2.1", "dev", "d0")
	waitFor("a change after the overflow", func() bool { return len(changes) == 2 })

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("watchConn after cancel = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watchConn does not stop with its context")
	}
}

func connFd(t *testing.T, conn *netlinkConn) uintptr {
	t.Helper()
	var fd uintptr
	if err := conn.raw.Control(func(f uintptr) { fd = f }); err != nil {
		t.Fatal(err)
	}
	return fd
}

// lockedBuffer is a log destination that two goroutines can use.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
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

// kernelAddrs are the addresses of an interface as the standard library reads
// them from the kernel, without the link-local ones.
func kernelAddrs(t *testing.T, name string) []netip.Prefix {
	t.Helper()
	iface, err := net.InterfaceByName(name)
	if err != nil {
		t.Fatal(err)
	}
	addrs, err := iface.Addrs()
	if err != nil {
		t.Fatal(err)
	}
	var out []netip.Prefix
	for _, a := range addrs {
		ipNet := a.(*net.IPNet)
		ones, _ := ipNet.Mask.Size()
		ip, _ := netip.AddrFromSlice(ipNet.IP)
		out = append(out, netip.PrefixFrom(ip.Unmap(), ones))
	}
	slices.SortFunc(out, comparePrefix)
	return withoutLinkLocal(out)
}

func kernelLink(t *testing.T, name string) *net.Interface {
	t.Helper()
	iface, err := net.InterfaceByName(name)
	if err != nil {
		t.Fatal(err)
	}
	return iface
}

func TestRootConfigureLink(t *testing.T) {
	requireNetns(t)
	holdTun(t, "tun7")
	holdTun(t, "tun8")
	runIP(t, "link", "add", "wg0", "type", "wireguard")

	t.Run("addresses, MTU and up", func(t *testing.T) {
		addrs := prefixes("10.8.0.2/24 2001:db8:8::2/64")
		if err := ConfigureLink("tun7", addrs, 1400); err != nil {
			t.Fatal(err)
		}
		iface := kernelLink(t, "tun7")
		if iface.MTU != 1400 || iface.Flags&net.FlagUp == 0 {
			t.Errorf("tun7: mtu %d, flags %v", iface.MTU, iface.Flags)
		}
		if got := kernelAddrs(t, "tun7"); !slices.Equal(got, addrs) {
			t.Errorf("addresses %v, want %v", got, addrs)
		}
		// An IPv4 address of a network names its broadcast address, as ip(8) does.
		if out := runIP(t, "-4", "addr", "show", "dev", "tun7"); !strings.Contains(out, "inet 10.8.0.2/24 brd 10.8.0.255 scope global tun7") {
			t.Errorf("ip addr show: %s", out)
		}

		// Up with carrier, and the subnet is on-link.
		state := snapshot(t)
		if tun := interfaceNamed(t, state, "tun7"); !tun.Up || !tun.Tunnel || !slices.Contains(tun.Addrs, addrs[0]) {
			t.Errorf("tun7 in the snapshot: %+v", tun)
		}
		routes := routesTo(t, "10.8.0.0/24")
		if len(routes) != 1 || routes[0].Iface != "tun7" || routes[0].Gateway.IsValid() || routes[0].Static {
			t.Errorf("the connected route of tun7: %+v", routes)
		}

		// Again, as when the engine restarts: nothing to add, nothing fails.
		if err := ConfigureLink("tun7", addrs, 1400); err != nil {
			t.Errorf("second ConfigureLink = %v", err)
		}
		if got := kernelAddrs(t, "tun7"); !slices.Equal(got, addrs) {
			t.Errorf("addresses after the second call %v, want %v", got, addrs)
		}
	})
	// What "ip tuntap add" makes: persistent, and without carrier until a process opens it.
	t.Run("a persistent tun device", func(t *testing.T) {
		runIP(t, "tuntap", "add", "dev", "tun9", "mode", "tun")
		addrs := prefixes("10.6.0.2/24 2001:db8:6::2/64")
		if err := ConfigureLink("tun9", addrs, 1380); err != nil {
			t.Fatal(err)
		}
		iface := kernelLink(t, "tun9")
		if iface.MTU != 1380 || iface.Flags&net.FlagUp == 0 {
			t.Errorf("tun9: mtu %d, flags %v", iface.MTU, iface.Flags)
		}
		if got := kernelAddrs(t, "tun9"); !slices.Equal(got, addrs) {
			t.Errorf("addresses %v, want %v", got, addrs)
		}
		// It is up, but nothing has it open, so it carries nothing yet.
		if tun := interfaceNamed(t, snapshot(t), "tun9"); tun.Up || !tun.Tunnel {
			t.Errorf("tun9 in the snapshot: %+v", tun)
		}
	})
	t.Run("a point-to-point address and the MTU left alone", func(t *testing.T) {
		if err := ConfigureLink("tun8", prefixes("10.9.0.2/32"), 0); err != nil {
			t.Fatal(err)
		}
		iface := kernelLink(t, "tun8")
		if iface.MTU != 1500 || iface.Flags&net.FlagUp == 0 {
			t.Errorf("tun8: mtu %d, flags %v", iface.MTU, iface.Flags)
		}
		if out := runIP(t, "-4", "addr", "show", "dev", "tun8"); !strings.Contains(out, "inet 10.9.0.2/32 scope global tun8") {
			t.Errorf("ip addr show: %s", out)
		}
	})
	t.Run("a wireguard device", func(t *testing.T) {
		if err := ConfigureLink("wg0", prefixes("10.7.0.2/24 2001:db8:7::2/128"), 1380); err != nil {
			t.Fatal(err)
		}
		iface := kernelLink(t, "wg0")
		if iface.MTU != 1380 || iface.Flags&net.FlagUp == 0 {
			t.Errorf("wg0: mtu %d, flags %v", iface.MTU, iface.Flags)
		}
		if tun := interfaceNamed(t, snapshot(t), "wg0"); !tun.Up || !tun.Tunnel {
			t.Errorf("wg0 in the snapshot: %+v", tun)
		}
	})
	t.Run("no addresses", func(t *testing.T) {
		runIP(t, "link", "add", "d5", "type", "dummy")
		if err := ConfigureLink("d5", nil, 1300); err != nil {
			t.Fatal(err)
		}
		if iface := kernelLink(t, "d5"); iface.MTU != 1300 || iface.Flags&net.FlagUp == 0 {
			t.Errorf("d5: mtu %d, flags %v", iface.MTU, iface.Flags)
		}
	})
	t.Run("a small MTU is fine without IPv6", func(t *testing.T) {
		runIP(t, "link", "add", "d6", "type", "dummy")
		if err := ConfigureLink("d6", prefixes("198.18.6.2/24"), 1000); err != nil {
			t.Fatal(err)
		}
		if iface := kernelLink(t, "d6"); iface.MTU != 1000 {
			t.Errorf("d6: mtu %d", iface.MTU)
		}
	})
	t.Run("a link that does not exist", func(t *testing.T) {
		err := ConfigureLink("nosuch0", prefixes("10.8.0.2/24"), 1400)
		if !errors.Is(err, errNoSuchInterface) || !strings.Contains(err.Error(), "nosuch0") {
			t.Errorf("ConfigureLink = %v", err)
		}
	})
	t.Run("a refusal names the link and the step", func(t *testing.T) {
		// A link with an MTU below the IPv6 minimum takes no IPv6 address.
		runIP(t, "link", "add", "d7", "type", "dummy")
		runIP(t, "link", "set", "d7", "mtu", "1000")
		err := ConfigureLink("d7", prefixes("198.18.7.2/24 2001:db8:7::2/64"), 1400)
		var refusal *netlinkError
		if !errors.As(err, &refusal) || !strings.Contains(err.Error(), "configure link d7: add address 2001:db8:7::2/64: ") {
			t.Errorf("ConfigureLink = %v", err)
		}
		if iface := kernelLink(t, "d7"); iface.Flags&net.FlagUp != 0 {
			t.Error("the link was brought up after a refusal")
		}
	})
	t.Run("an invalid address", func(t *testing.T) {
		runIP(t, "link", "add", "d8", "type", "dummy")
		if err := ConfigureLink("d8", []netip.Prefix{{}}, 1400); err == nil {
			t.Error("ConfigureLink accepted an invalid prefix")
		}
		if iface := kernelLink(t, "d8"); iface.Flags&net.FlagUp != 0 {
			t.Error("the link was brought up after an invalid address")
		}
	})
	t.Run("a small MTU with IPv6 would remove the addresses", func(t *testing.T) {
		runIP(t, "link", "add", "d9", "type", "dummy")
		if err := ConfigureLink("d9", prefixes("2001:db8:9::2/64"), 1000); err == nil {
			t.Error("ConfigureLink accepted an MTU that removes IPv6 addresses")
		}
		if iface := kernelLink(t, "d9"); iface.MTU != 1500 || iface.Flags&net.FlagUp != 0 || len(kernelAddrs(t, "d9")) != 0 {
			t.Errorf("d9 was touched: mtu %d, flags %v", iface.MTU, iface.Flags)
		}
	})
}
