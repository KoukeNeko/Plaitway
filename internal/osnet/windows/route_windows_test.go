package windows

import (
	"errors"
	"net/netip"
	"slices"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/winiface/inet"
)

// row builds a row of the forwarding table the way GetIpForwardTable2 returns it.
func row(dst string, nextHop string, a adapter, metric, protocol, origin uint32) *windows.MibIpForwardRow2 {
	prefix := netip.MustParsePrefix(dst)
	r := &windows.MibIpForwardRow2{
		InterfaceLuid:  a.LUID,
		InterfaceIndex: a.Index,
		Metric:         metric,
		Protocol:       protocol,
		Origin:         origin,
	}
	inet.Set(&r.DestinationPrefix.Prefix, prefix.Addr())
	r.DestinationPrefix.PrefixLength = uint8(prefix.Bits())
	next := unspecifiedOf(prefix.Addr())
	if nextHop != "" {
		next = netip.MustParseAddr(nextHop)
	}
	inet.Set(&r.NextHop, next)
	return r
}

func luids(adapters ...adapter) map[uint64]adapter {
	m := make(map[uint64]adapter)
	for _, a := range adapters {
		m[a.LUID] = a
	}
	return m
}

func TestRouteFromRow(t *testing.T) {
	known := luids(intelEthernet, loopback, openVPNTap)
	tests := []struct {
		name string
		row  *windows.MibIpForwardRow2
		want osnet.Route
		ok   bool
	}{
		{
			name: "the default route from the router",
			row:  row("0.0.0.0/0", "192.168.1.1", intelEthernet, 0, windows.MIB_IPPROTO_NETMGMT, windows.NlroManual),
			want: osnet.Route{Dst: netip.MustParsePrefix("0.0.0.0/0"), Gateway: ip("192.168.1.1"), Iface: intelEthernet.Name, IfIndex: 2, Static: true, Flags: 0x3},
			ok:   true,
		},
		{
			name: "an IPv6 default learned from a router advertisement",
			row:  row("::/0", "fe80::324f:75ff:fed5:f0d7", intelEthernet, 256, windows.MIB_IPPROTO_NETMGMT, windows.NlroRouterAdvertisement),
			want: osnet.Route{Dst: netip.MustParsePrefix("::/0"), Gateway: ip("fe80::324f:75ff:fed5:f0d7"), Iface: intelEthernet.Name, IfIndex: 2, Metric: 256, Static: true, Flags: 0x30003},
			ok:   true,
		},
		{
			name: "the on-link route the stack derives from an address is not static",
			row:  row("192.168.1.0/24", "", intelEthernet, 256, windows.MIB_IPPROTO_LOCAL, windows.NlroWellKnown),
			want: osnet.Route{Dst: netip.MustParsePrefix("192.168.1.0/24"), Iface: intelEthernet.Name, IfIndex: 2, Metric: 256, Flags: 0x10002},
			ok:   true,
		},
		{
			name: "a route we added on a tunnel, bound to its interface",
			row:  row("10.0.0.0/8", "", openVPNTap, 0, windows.MIB_IPPROTO_NETMGMT, windows.NlroManual),
			want: osnet.Route{Dst: netip.MustParsePrefix("10.0.0.0/8"), Iface: openVPNTap.Name, IfIndex: 20, Static: true, Flags: 0x3},
			ok:   true,
		},
		{
			name: "host bits of the destination are masked",
			row:  row("203.0.113.9/24", "192.168.1.1", intelEthernet, 5, windows.MIB_IPPROTO_NETMGMT, windows.NlroManual),
			want: osnet.Route{Dst: netip.MustParsePrefix("203.0.113.0/24"), Gateway: ip("192.168.1.1"), Iface: intelEthernet.Name, IfIndex: 2, Metric: 5, Static: true, Flags: 0x3},
			ok:   true,
		},
		{
			name: "a blackhole is a static on-link route on the loopback pseudo-interface",
			row:  row("198.51.100.64/28", "0.0.0.0", loopback, 0, windows.MIB_IPPROTO_NETMGMT, windows.NlroManual),
			want: osnet.Route{Dst: netip.MustParsePrefix("198.51.100.64/28"), Iface: loopback.Name, IfIndex: 1, Blackhole: true, Static: true, Flags: 0x3},
			ok:   true,
		},
		{
			name: "an IPv6 blackhole",
			row:  row("2001:db8:ffff:1::/64", "::", loopback, 0, windows.MIB_IPPROTO_NETMGMT, windows.NlroManual),
			want: osnet.Route{Dst: netip.MustParsePrefix("2001:db8:ffff:1::/64"), Iface: loopback.Name, IfIndex: 1, Blackhole: true, Static: true, Flags: 0x3},
			ok:   true,
		},
		{
			name: "the loopback's own routes are on-link, not blackholes",
			row:  row("127.0.0.0/8", "", loopback, 256, windows.MIB_IPPROTO_LOCAL, windows.NlroWellKnown),
			want: osnet.Route{Dst: netip.MustParsePrefix("127.0.0.0/8"), Iface: loopback.Name, IfIndex: 1, Metric: 256, Flags: 0x10002},
			ok:   true,
		},
		{
			name: "a loopback next hop on another interface is a gateway, not a blackhole",
			row:  row("203.0.113.0/24", "127.0.0.1", intelEthernet, 0, windows.MIB_IPPROTO_NETMGMT, windows.NlroManual),
			want: osnet.Route{Dst: netip.MustParsePrefix("203.0.113.0/24"), Gateway: ip("127.0.0.1"), Iface: intelEthernet.Name, IfIndex: 2, Static: true, Flags: 0x3},
			ok:   true,
		},
		{
			name: "an interface the list does not know keeps its index",
			row:  row("203.0.113.7/32", "10.99.0.1", adapter{LUID: 0xdead, Index: 77}, 0, windows.MIB_IPPROTO_NETMGMT, windows.NlroManual),
			want: osnet.Route{Dst: netip.MustParsePrefix("203.0.113.7/32"), Gateway: ip("10.99.0.1"), IfIndex: 77, Static: true, Flags: 0x3},
			ok:   true,
		},
		{
			name: "a family that is neither IPv4 nor IPv6",
			row:  &windows.MibIpForwardRow2{InterfaceLuid: intelEthernet.LUID},
			ok:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := routeFromRow(tt.row, known)
			if ok != tt.ok || got != tt.want {
				t.Errorf("got %+v, %v\nwant %+v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
	bad := row("10.0.0.0/8", "", intelEthernet, 0, windows.MIB_IPPROTO_NETMGMT, windows.NlroManual)
	bad.DestinationPrefix.PrefixLength = 40
	if _, ok := routeFromRow(bad, known); ok {
		t.Error("a prefix length of 40 on an IPv4 destination was accepted")
	}
}

func TestForwardRow(t *testing.T) {
	tests := []struct {
		name    string
		route   osnet.Route
		at      target
		wantHop netip.Addr
	}{
		{"gateway route", osnet.Route{Dst: netip.MustParsePrefix("203.0.113.7/32"), Metric: 7}, target{LUID: 0x1002, Index: 2, Gateway: ip("192.168.1.1")}, ip("192.168.1.1")},
		{"bound to the interface: unspecified next hop of the family", osnet.Route{Dst: netip.MustParsePrefix("10.0.0.0/8")}, target{LUID: 0x1014, Index: 20}, ip("0.0.0.0")},
		{"IPv6 bound to the interface", osnet.Route{Dst: netip.MustParsePrefix("2001:db8::/32")}, target{Index: 20}, ip("::")},
		{"IPv6 gateway", osnet.Route{Dst: netip.MustParsePrefix("::/1")}, target{Index: 2, Gateway: ip("fe80::1")}, ip("fe80::1")},
		{"host bits are cleared", osnet.Route{Dst: netip.MustParsePrefix("10.1.2.3/8")}, target{Index: 20}, ip("0.0.0.0")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := forwardRow(tt.route, tt.at)
			if got.InterfaceLuid != tt.at.LUID || got.InterfaceIndex != tt.at.Index {
				t.Errorf("interface = %#x / %d", got.InterfaceLuid, got.InterfaceIndex)
			}
			prefix := netip.PrefixFrom(inet.Addr(&got.DestinationPrefix.Prefix), int(got.DestinationPrefix.PrefixLength))
			if prefix != tt.route.Dst.Masked() {
				t.Errorf("destination = %v, want %v", prefix, tt.route.Dst.Masked())
			}
			if hop := inet.Addr(&got.NextHop); hop != tt.wantHop {
				t.Errorf("next hop = %v, want %v", hop, tt.wantHop)
			}
			if got.NextHop.Family != got.DestinationPrefix.Prefix.Family {
				t.Errorf("next hop family %d differs from the destination's %d", got.NextHop.Family, got.DestinationPrefix.Prefix.Family)
			}
			if got.Metric != tt.route.Metric {
				t.Errorf("metric = %d, want %d", got.Metric, tt.route.Metric)
			}
			if got.Protocol != windows.MIB_IPPROTO_NETMGMT || got.Origin != windows.NlroManual {
				t.Errorf("protocol %d origin %d: a route we add must read back as static", got.Protocol, got.Origin)
			}
			if got.ValidLifetime != infiniteLifetime || got.PreferredLifetime != infiniteLifetime {
				t.Errorf("lifetimes %d / %d, want infinite", got.ValidLifetime, got.PreferredLifetime)
			}
		})
	}
}

// What forwardRow writes is read back by routeFromRow as the route that was
// asked for.
func TestForwardRowRoundTrip(t *testing.T) {
	known := luids(intelEthernet, loopback)
	for _, r := range []osnet.Route{
		{Dst: netip.MustParsePrefix("203.0.113.7/32"), Gateway: ip("192.168.1.1"), Iface: intelEthernet.Name, IfIndex: 2, Metric: 3, Static: true, Flags: 0x3},
		{Dst: netip.MustParsePrefix("2001:db8::/32"), Iface: intelEthernet.Name, IfIndex: 2, Static: true, Flags: 0x3},
	} {
		row := forwardRow(r, target{LUID: intelEthernet.LUID, Index: 2, Gateway: r.Gateway})
		got, ok := routeFromRow(&row, known)
		if !ok || got != r {
			t.Errorf("round trip of %+v gave %+v, %v", r, got, ok)
		}
	}
}

// readAdapters, the part of Dump that only reads, runs on this machine.
func TestRealDump(t *testing.T) {
	routes, err := NewRouteTable().Dump()
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) == 0 {
		t.Fatal("the routing table is empty")
	}
	if !slices.IsSortedFunc(routes, compareRoutes) {
		t.Error("the dump is not sorted")
	}
	var sawLoopback bool
	for _, r := range routes {
		if !r.Dst.IsValid() || r.Dst != r.Dst.Masked() {
			t.Errorf("destination %v is not a valid masked prefix", r.Dst)
		}
		if r.IfIndex == 0 {
			t.Errorf("route %v has no interface index", r.Dst)
		}
		if r.Gateway.IsValid() && r.Gateway.Is4() != r.Dst.Addr().Is4() {
			t.Errorf("route %v has a gateway of the other family: %v", r.Dst, r.Gateway)
		}
		if r.Dst == netip.MustParsePrefix("127.0.0.0/8") {
			sawLoopback = true
			if r.Static || r.Blackhole || r.Gateway.IsValid() {
				t.Errorf("the loopback route is %+v", r)
			}
		}
	}
	if !sawLoopback {
		t.Error("127.0.0.0/8 is not in the dump")
	}
}

func TestRealSnapshot(t *testing.T) {
	monitor := NewNetMonitor(NetMonitorOptions{Logger: discardLog()})
	state, err := monitor.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if state.Epoch != 1 {
		t.Errorf("first epoch = %d", state.Epoch)
	}
	again, err := monitor.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if again.Epoch < state.Epoch {
		t.Errorf("epoch went back: %d -> %d", state.Epoch, again.Epoch)
	}
	var sawLoopback bool
	for _, ifc := range state.Interfaces {
		if ifc.Index == 1 {
			sawLoopback = true
			if ifc.Tunnel || !ifc.Up || !slices.Contains(ifc.Addrs, netip.MustParsePrefix("127.0.0.1/8")) {
				t.Errorf("loopback = %+v", ifc)
			}
		}
		if ifc.Name == "" {
			t.Errorf("interface %d has no name", ifc.Index)
		}
	}
	if !sawLoopback {
		t.Error("the loopback pseudo-interface is not among the interfaces")
	}
	for _, nh := range []*osnet.Nexthop{state.DefaultV4, state.DefaultV6} {
		if nh == nil {
			continue // a machine may have no IPv6 default, or no network at all
		}
		i := slices.IndexFunc(state.Interfaces, func(i osnet.Interface) bool { return i.Name == nh.Iface })
		if i < 0 || state.Interfaces[i].Tunnel || !state.Interfaces[i].Up {
			t.Errorf("default %+v does not leave through an interface that is up and physical", nh)
		}
	}
	for _, subnet := range state.Connected {
		if subnet.Addr().IsLinkLocalUnicast() || subnet != subnet.Masked() {
			t.Errorf("connected subnet %v", subnet)
		}
	}
}

// Writing needs an elevated process. Run without one, the call must reach the
// system, be refused, and change nothing: it shows that the row is well formed
// enough to get as far as the access check and that the refusal is not turned
// into one of the osnet errors. It is skipped when elevated, so that it never
// adds a route for real.
func TestRealAddAndDeleteAreRefusedWithoutElevation(t *testing.T) {
	if isElevated(t) {
		t.Skip("elevated: the rootintegration tests cover writing")
	}
	table := NewRouteTable()
	route := osnet.Route{Dst: netip.MustParsePrefix("192.0.2.0/24"), Iface: "Loopback Pseudo-Interface 1"}
	err := table.Add(route)
	if err == nil {
		t.Fatal("Add succeeded without elevation")
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Errorf("Add error = %v, want ERROR_ACCESS_DENIED", err)
	}
	for _, mapped := range []error{osnet.ErrExists, osnet.ErrNotFound, osnet.ErrUnreachable} {
		if errors.Is(err, mapped) {
			t.Errorf("the refusal is reported as %v", mapped)
		}
	}
	if err := table.Delete(route); err == nil {
		t.Error("Delete succeeded without elevation")
	}
	routes, err := table.Dump()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range routes {
		if r.Dst == route.Dst {
			t.Errorf("the route is in the table: %+v", r)
		}
	}
}

// isElevated reports whether this process runs with the full administrator
// token.
func isElevated(t *testing.T) bool {
	t.Helper()
	return windows.GetCurrentProcessToken().IsElevated()
}

func ifRow(name, description string, ifType uint32, flags uint8, oper uint32) windows.MibIfRow2 {
	row := windows.MibIfRow2{
		InterfaceLuid:               0x1000 + uint64(len(name)),
		InterfaceIndex:              uint32(len(description)),
		Type:                        ifType,
		InterfaceAndOperStatusFlags: flags,
		OperStatus:                  oper,
	}
	copy(row.Alias[:], windows.StringToUTF16(name))
	copy(row.Description[:], windows.StringToUTF16(description))
	return row
}

// The interface table lists the NDIS filter drivers next to the adapter they
// filter, as interfaces of their own that are up and carry nothing.
func TestAdaptersOfRowsSkipsFilterInterfaces(t *testing.T) {
	rows := []windows.MibIfRow2{
		ifRow("乙太網路 2", "Intel(R) Ethernet Controller (3) I225-V", ifTypeEthernet, 0x05, windows.IfOperStatusUp),
		ifRow("乙太網路 2-QoS Packet Scheduler-0000", "Intel(R) Ethernet Controller (3) I225-V-QoS Packet Scheduler-0000", ifTypeEthernet, 0x02, windows.IfOperStatusUp),
		ifRow("Wi-Fi", "Realtek 8821CU Wireless LAN 802.11ac USB NIC", ifTypeIEEE80211, 0x01, windows.IfOperStatusNotPresent),
		ifRow("vSwitch (Default Switch)-Hyper-V Virtual Switch Extension Filter-0000", "Hyper-V Virtual Switch Extension Adapter-Hyper-V Virtual Switch Extension Filter-0000", ifTypeEthernet, 0x12, windows.IfOperStatusDown),
		ifRow("OpenVPN TAP-Windows6", "TAP-Windows Adapter V9", ifTypePropVirtual, 0x00, windows.IfOperStatusUp),
	}
	got := adaptersOfRows(rows)
	var names []string
	for _, a := range got {
		names = append(names, a.Name)
	}
	if want := []string{"乙太網路 2", "Wi-Fi", "OpenVPN TAP-Windows6"}; !slices.Equal(names, want) {
		t.Fatalf("adapters = %q, want %q", names, want)
	}
	if !got[0].Hardware || !got[0].Up || got[0].Type != ifTypeEthernet || got[0].Description == "" {
		t.Errorf("Ethernet adapter = %+v", got[0])
	}
	if got[1].Up || !got[1].Hardware {
		t.Errorf("an adapter that is not present is up, or not hardware: %+v", got[1])
	}
	if got[2].Hardware || !got[2].Up {
		t.Errorf("TAP adapter = %+v", got[2])
	}
}

func addressRow(luid uint64, text string, bits uint8, dad uint32) windows.MibUnicastIpAddressRow {
	row := windows.MibUnicastIpAddressRow{InterfaceLuid: luid, OnLinkPrefixLength: bits, DadState: dad}
	inet.Set((*windows.RawSockaddrInet)(unsafe.Pointer(&row.Address)), netip.MustParseAddr(text))
	return row
}

// An address that failed duplicate address detection is not usable and must not
// count as the subnet of its interface.
func TestAddressesOfRows(t *testing.T) {
	rows := []windows.MibUnicastIpAddressRow{
		addressRow(1, "192.168.1.101", 24, windows.IpDadStatePreferred),
		addressRow(1, "2001:db8::1", 64, windows.IpDadStateTentative),
		addressRow(1, "192.168.1.55", 24, windows.IpDadStateDuplicate),
		addressRow(2, "10.6.0.2", 24, windows.IpDadStateDeprecated),
		addressRow(2, "10.6.0.3", 24, windows.IpDadStateInvalid),
		addressRow(3, "fe80::1", 64, windows.IpDadStatePreferred),
	}
	got := addressesOfRows(rows)
	want := map[uint64][]netip.Prefix{
		1: pfx("192.168.1.101/24", "2001:db8::1/64"),
		2: pfx("10.6.0.2/24"),
		3: pfx("fe80::1/64"),
	}
	if len(got) != len(want) {
		t.Fatalf("addresses = %v, want %v", got, want)
	}
	for luid, prefixes := range want {
		if !slices.Equal(got[luid], prefixes) {
			t.Errorf("interface %d: %v, want %v", luid, got[luid], prefixes)
		}
	}
}

func TestResolveTarget(t *testing.T) {
	adapters := []adapter{loopback, intelEthernet, realtekWiFi}
	table := &routeTable{
		readAdapters: func() ([]adapter, error) { return adapters, nil },
		listAdapters: func() ([]adapter, error) { return adapters, nil },
	}
	realLoopback := "Loopback Pseudo-Interface 1"
	tests := []struct {
		name    string
		route   osnet.Route
		byGate  bool
		want    target
		wantErr error
		bad     bool
	}{
		{"a blackhole leaves through the loopback, on-link", osnet.Route{Dst: netip.MustParsePrefix("198.51.100.0/24"), Blackhole: true}, true,
			target{LUID: loopback.LUID, Index: 1}, nil, false},
		{"an IPv6 blackhole", osnet.Route{Dst: netip.MustParsePrefix("2001:db8::/32"), Blackhole: true}, true,
			target{LUID: loopback.LUID, Index: 1}, nil, false},
		{"a blackhole ignores a gateway and an interface it was given", osnet.Route{Dst: netip.MustParsePrefix("198.51.100.0/24"), Blackhole: true, Gateway: ip("192.168.1.1"), IfIndex: 2}, true,
			target{LUID: loopback.LUID, Index: 1}, nil, false},
		{"the index wins over the name", osnet.Route{Dst: netip.MustParsePrefix("203.0.113.0/24"), IfIndex: 23, Iface: intelEthernet.Name, Gateway: ip("10.20.0.1")}, true,
			target{Index: 23, Gateway: ip("10.20.0.1")}, nil, false},
		{"an unknown interface name", osnet.Route{Dst: netip.MustParsePrefix("203.0.113.0/24"), Iface: "Plaitway-test-no-such-adapter"}, true,
			target{}, osnet.ErrUnreachable, false},
		{"a gateway finds its interface when adding", osnet.Route{Dst: netip.MustParsePrefix("203.0.113.7/32"), Gateway: ip("10.20.0.1")}, true,
			target{LUID: realtekWiFi.LUID, Index: 23, Gateway: ip("10.20.0.1")}, nil, false},
		{"a gateway on no subnet is unreachable", osnet.Route{Dst: netip.MustParsePrefix("203.0.113.7/32"), Gateway: ip("172.16.9.9")}, true,
			target{}, osnet.ErrUnreachable, false},
		{"a gateway does not find its interface when deleting", osnet.Route{Dst: netip.MustParsePrefix("203.0.113.7/32"), Gateway: ip("10.20.0.1")}, false,
			target{}, nil, true},
		{"a gateway of the other family", osnet.Route{Dst: netip.MustParsePrefix("203.0.113.7/32"), Gateway: ip("fe80::1"), IfIndex: 2}, true,
			target{}, nil, true},
		{"nothing to go by", osnet.Route{Dst: netip.MustParsePrefix("203.0.113.0/24")}, true,
			target{}, nil, true},
		{"the zone of a link-local gateway names the interface", osnet.Route{Dst: netip.MustParsePrefix("::/1"), Gateway: ip("fe80::1").WithZone(realLoopback)}, true,
			target{Gateway: ip("fe80::1")}, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := table.resolveTarget(tt.route, tt.byGate)
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("got %+v, %v; want %v", got, err, tt.wantErr)
				}
			case tt.bad:
				if err == nil {
					t.Fatalf("got %+v, want an error", got)
				}
			case err != nil:
				t.Fatal(err)
			default:
				// A name or zone is looked up on this machine: the real loopback.
				if tt.route.Gateway.Zone() != "" {
					if got.LUID == 0 || got.Index == 0 || got.Gateway != tt.want.Gateway {
						t.Fatalf("got %+v", got)
					}
					return
				}
				if got != tt.want {
					t.Errorf("got %+v, want %+v", got, tt.want)
				}
			}
		})
	}
	named, err := table.resolveTarget(osnet.Route{Dst: netip.MustParsePrefix("203.0.113.0/24"), Iface: realLoopback}, true)
	if err != nil || named.LUID == 0 || named.Index != 1 || named.Gateway.IsValid() {
		t.Errorf("an interface by name: %+v, %v", named, err)
	}
}
