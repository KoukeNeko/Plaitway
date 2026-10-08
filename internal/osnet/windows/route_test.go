package windows

import (
	"errors"
	"net/netip"
	"syscall"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

func TestRouteErrorMapping(t *testing.T) {
	dst := netip.MustParsePrefix("203.0.113.0/24")
	denied := syscall.Errno(5) // ERROR_ACCESS_DENIED
	tests := []struct {
		name string
		op   routeOp
		err  error
		want error // nil: the error comes back unmapped
	}{
		{"add of an existing key", opAdd, errorObjectAlreadyExists, osnet.ErrExists},
		{"delete of a missing key", opDelete, errorNotFound, osnet.ErrNotFound},
		{"delete through an interface that is gone", opDelete, errorFileNotFound, osnet.ErrNotFound},
		{"add with a network that is unreachable", opAdd, errorNetworkUnreachable, osnet.ErrUnreachable},
		{"add with a host that is unreachable", opAdd, errorHostUnreachable, osnet.ErrUnreachable},
		{"add with a bad network path", opAdd, errorBadNetPath, osnet.ErrUnreachable},
		{"add with Winsock network unreachable", opAdd, wsaeNetUnreachable, osnet.ErrUnreachable},
		{"add with Winsock host unreachable", opAdd, wsaeHostUnreachable, osnet.ErrUnreachable},
		{"add through an interface that is gone", opAdd, errorNotFound, osnet.ErrUnreachable},
		{"add through an interface that is gone, other code", opAdd, errorFileNotFound, osnet.ErrUnreachable},
		{"add: access denied stays what it is", opAdd, denied, nil},
		{"delete: access denied stays what it is", opDelete, denied, nil},
		{"delete: an existing key is no answer to a delete", opDelete, errorObjectAlreadyExists, nil},
		{"add: not found is not ErrNotFound", opAdd, errorNotFound, osnet.ErrUnreachable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := routeError(tt.op, dst, tt.err)
			if tt.want != nil {
				if !errors.Is(got, tt.want) {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
				for _, other := range []error{osnet.ErrExists, osnet.ErrNotFound, osnet.ErrUnreachable} {
					if other != tt.want && errors.Is(got, other) {
						t.Errorf("%v also matches %v", got, other)
					}
				}
				return
			}
			if !errors.Is(got, tt.err) {
				t.Errorf("the cause is lost: %v", got)
			}
			for _, mapped := range []error{osnet.ErrExists, osnet.ErrNotFound, osnet.ErrUnreachable} {
				if errors.Is(got, mapped) {
					t.Errorf("%v is mapped to %v", tt.err, mapped)
				}
			}
		})
	}
	if got := routeError(opAdd, dst, errorObjectAlreadyExists).Error(); got != "add route 203.0.113.0/24: route already exists" {
		t.Errorf("message = %q", got)
	}
}

func TestIsStaticProtocol(t *testing.T) {
	for protocol, want := range map[uint32]bool{
		1:     false, // other
		2:     false, // local: the stack's own on-link routes
		3:     true,  // NETMGMT: an administrator or a program
		14:    false, // BGP
		19:    false, // DHCP, a protocol of its own
		10002: true,
		10006: true,
		10007: true,
	} {
		if got := isStaticProtocol(protocol); got != want {
			t.Errorf("protocol %d: static = %v, want %v", protocol, got, want)
		}
	}
}

func TestRouteFlags(t *testing.T) {
	if got := routeFlags(3, 3); got != 0x30003 {
		t.Errorf("NETMGMT from router advertisements = %#x, want 0x30003", got)
	}
	if got := routeFlags(10006, 0); got != 10006 {
		t.Errorf("legacy static = %#x", got)
	}
	// A protocol that does not fit the lower half must not spill into the origin.
	if got := routeFlags(0x1_0003, 0); got != 3 {
		t.Errorf("oversized protocol = %#x", got)
	}
}

func TestInterfaceForGateway(t *testing.T) {
	down := intelEthernet
	down.Up = false
	cheap := usbGigabit
	cheap.Addrs = pfx("192.168.1.55/24")
	cheap.Metric4 = 10
	tests := []struct {
		name     string
		adapters []adapter
		gateway  netip.Addr
		want     string
		unreach  bool
	}{
		{"the gateway is in the subnet of an adapter", []adapter{intelEthernet, realtekWiFi}, ip("192.168.1.1"), intelEthernet.Name, false},
		{"another subnet", []adapter{intelEthernet, realtekWiFi}, ip("10.20.0.1"), realtekWiFi.Name, false},
		{"an adapter that is down does not reach it", []adapter{down}, ip("192.168.1.1"), "", true},
		{"two adapters on the subnet: the lowest interface metric", []adapter{intelEthernet, cheap}, ip("192.168.1.1"), cheap.Name, false},
		{"equal metrics: the lowest index", []adapter{withMetrics(cheap, 25, 25), intelEthernet}, ip("192.168.1.1"), intelEthernet.Name, false},
		{"the loopback never is the way to a gateway", []adapter{loopback}, ip("127.0.0.1"), "", true},
		{"no subnet", []adapter{intelEthernet}, ip("172.16.0.1"), "", true},
		{"an IPv6 gateway in a global subnet", []adapter{intelEthernet}, ip("2001:b011:c006:bf71::1"), intelEthernet.Name, false},
		{"a link-local gateway names its adapter by zone", []adapter{intelEthernet, realtekWiFi}, ip("fe80::1").WithZone(realtekWiFi.Name), realtekWiFi.Name, false},
		{"a link-local gateway on an adapter that does not exist", []adapter{intelEthernet}, ip("fe80::1").WithZone("nope"), "", true},
		{"a link-local gateway without a zone is on no subnet we can name", []adapter{intelEthernet}, ip("fe80::1"), intelEthernet.Name, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := interfaceForGateway(tt.adapters, tt.gateway)
			if tt.unreach {
				if !errors.Is(err, osnet.ErrUnreachable) {
					t.Fatalf("got %+v, %v; want ErrUnreachable", got.Name, err)
				}
				return
			}
			if err != nil || got.Name != tt.want {
				t.Fatalf("got %q, %v; want %q", got.Name, err, tt.want)
			}
		})
	}
}

func TestSameRouteKey(t *testing.T) {
	base := osnet.Route{Dst: netip.MustParsePrefix("203.0.113.7/32"), Gateway: ip("192.168.1.1"), IfIndex: 2, Metric: 5}
	same := base
	same.Metric = 99
	same.Iface = "whatever"
	other := base
	for name, change := range map[string]func(*osnet.Route){
		"destination": func(r *osnet.Route) { r.Dst = netip.MustParsePrefix("203.0.113.8/32") },
		"interface":   func(r *osnet.Route) { r.IfIndex = 23 },
		"next hop":    func(r *osnet.Route) { r.Gateway = ip("192.168.1.2") },
	} {
		other = base
		change(&other)
		if sameRouteKey(base, other) {
			t.Errorf("a different %s is the same key", name)
		}
	}
	if !sameRouteKey(base, same) {
		t.Error("the metric and the name of the interface are not part of the key")
	}
	zoned := base
	zoned.Gateway = ip("fe80::1").WithZone("Ethernet")
	unzoned := base
	unzoned.Gateway = ip("fe80::1")
	if !sameRouteKey(zoned, unzoned) {
		t.Error("the zone of a gateway is not part of the key")
	}
}

func TestMatchDelete(t *testing.T) {
	a := osnet.Route{Dst: netip.MustParsePrefix("203.0.113.0/24"), Gateway: ip("192.168.1.1"), IfIndex: 2}
	b := osnet.Route{Dst: netip.MustParsePrefix("203.0.113.0/24"), Gateway: ip("10.20.0.1"), IfIndex: 23}
	c := osnet.Route{Dst: netip.MustParsePrefix("198.51.100.0/24"), IfIndex: 20}
	table := []osnet.Route{a, b, c}

	if got, err := matchDelete(table, osnet.Route{Dst: c.Dst}); err != nil || got != c {
		t.Errorf("the only route to a destination: %+v, %v", got, err)
	}
	if got, err := matchDelete(table, osnet.Route{Dst: a.Dst, Gateway: b.Gateway}); err != nil || got != b {
		t.Errorf("singled out by gateway: %+v, %v", got, err)
	}
	if _, err := matchDelete(table, osnet.Route{Dst: a.Dst}); err == nil || errors.Is(err, osnet.ErrNotFound) {
		t.Errorf("two routes match, but the answer is %v", err)
	}
	if _, err := matchDelete(table, osnet.Route{Dst: netip.MustParsePrefix("192.0.2.0/24")}); !errors.Is(err, osnet.ErrNotFound) {
		t.Errorf("no route: %v, want ErrNotFound", err)
	}
	if _, err := matchDelete(table, osnet.Route{Dst: a.Dst, Gateway: ip("1.2.3.4")}); !errors.Is(err, osnet.ErrNotFound) {
		t.Errorf("no route through that gateway: %v, want ErrNotFound", err)
	}
	zoned := b.Gateway.WithZone("Wi-Fi")
	if got, err := matchDelete(table, osnet.Route{Dst: a.Dst, Gateway: zoned}); err != nil || got != b {
		t.Errorf("a gateway with a zone: %+v, %v", got, err)
	}
}
