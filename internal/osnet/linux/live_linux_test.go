package linux

// Live tests read this machine: the routing table and the interfaces, and
// compare what the adapter makes of them with what ip(8) says. They write
// nothing: a dump is a request any process may send, and ip is only asked to
// show. The writing code is tested by rootintegration_linux_test.go, in a
// private network namespace; the live reading of systemd-resolved is
// TestLiveResolvedReadOnly.
//
// The checks do not know this machine; they compare the adapter with the
// oracle, so they hold on any Linux box. The same checks run on a recording
// (testdata) of a VM with a wg-quick style WireGuard full tunnel (policy
// routing, table 51820), a DHCP uplink and IPv6 on the tunnel only, so that
// they run without the live system. Each ip-*.json file is the output of "ip
// -j" with the arguments of ipCommands; sys-class-net.json holds the files of
// /sys/class/net that the adapter reads, and resolvectl-*.txt what resolvectl
// printed. The global IPv6 address of the WireGuard account, the MAC address and
// the link-local address made from it were replaced by addresses of the
// documentation ranges.

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// liveAttempts: the machine can change between the adapter's reading and
// ip(8)'s, so a disagreement counts only when it stays.
const liveAttempts = 5

// ipCommands are the arguments of "ip -j" that produce each file of testdata.
var ipCommands = map[string][]string{
	"ip-addr.json":         {"-d", "addr", "show"},
	"ip-route-main-4.json": {"-4", "route", "show", "table", "main"},
	"ip-route-main-6.json": {"-6", "route", "show", "table", "main"},
	"ip-route-all-4.json":  {"-4", "route", "show", "table", "all"},
	"ip-route-all-6.json":  {"-6", "route", "show", "table", "all"},
	"ip-rule-4.json":       {"-4", "rule", "show"},
	"ip-rule-6.json":       {"-6", "rule", "show"},
}

// The numbers behind the names ip(8) prints, for the names the kernel itself
// defines (linux/rtnetlink.h) and the two its protocols get from rt_protos. A
// name it takes from a file of the machine is not here.
var (
	ipRouteTypes = map[string]uint32{
		"": rtnUnicast, "unicast": rtnUnicast, "local": 2, "broadcast": 3, "anycast": 4, "multicast": 5,
		"blackhole": rtnBlackhole, "unreachable": rtnUnreachable, "prohibit": rtnProhibit, "throw": 9,
	}
	// ip does not print the protocol "boot", the default of "ip route add".
	ipProtocols = map[string]uint32{"": 3, "unspec": 0, "redirect": rtprotRedirect, "kernel": rtprotKernel, "boot": 3, "static": 4, "ra": rtprotRA, "dhcp": 16}
	ipScopes    = map[string]uint32{"": rtScopeUniverse, "global": rtScopeUniverse, "site": 200, "link": rtScopeLink, "host": 254, "nowhere": rtScopeNowhere}
	ipTables    = map[string]uint32{"": rtTableMain, "main": rtTableMain, "local": 255, "default": 253}
	// ipLinkTypes are the link types ip prints (ARPHRD_*) in the recording.
	ipLinkTypes = map[string]uint16{
		"ether": 1, "loopback": arphrdLoopback, "none": arphrdNone, "ipip": arphrdTunnel, "tunnel6": arphrdTunnel6,
		"sit": arphrdSIT, "gre": arphrdIPGRE, "ppp": arphrdPPP,
	}
)

// ipNumber is the number of a name from names, or the number the name spells.
func ipNumber(names map[string]uint32, name string) (uint32, bool) {
	if n, ok := names[name]; ok {
		return n, true
	}
	n, err := strconv.ParseUint(name, 10, 32)
	return uint32(n), err == nil
}

// Devices that encapsulate traffic, and plain devices, by the names ip prints.
// A kind in neither list is one this test has no opinion on.
var (
	ipTunnelKinds = []string{"tun", "wireguard", "ipip", "sit", "gre", "gretap", "ip6gre", "ip6gretap", "ip6tnl", "vti", "vti6", "geneve", "vxlan"}
	ipTunnelTypes = []string{"ipip", "tunnel6", "sit", "gre"}
	ipPlainKinds  = []string{"", "bridge", "veth", "vlan", "bond", "dummy", "macvlan", "macvtap", "ipvlan", "team", "vrf"}
)

// ipLink is an interface as "ip -j -d addr show" prints it.
type ipLink struct {
	Index    int      `json:"ifindex"`
	Name     string   `json:"ifname"`
	Flags    []string `json:"flags"`
	LinkType string   `json:"link_type"`
	LinkInfo struct {
		Kind string `json:"info_kind"`
	} `json:"linkinfo"`
	ParentBus string `json:"parentbus"`
	Addrs     []struct {
		Local     string `json:"local"`
		PrefixLen int    `json:"prefixlen"`
	} `json:"addr_info"`

	// addrs are Addrs as the adapter lists them: sorted, without repeats.
	addrs []netip.Prefix
}

func (l ipLink) up() bool {
	return slices.Contains(l.Flags, "UP") && slices.Contains(l.Flags, "LOWER_UP")
}

// role says what the adapter must make of the device: whether it is a Tunnel
// for the Reconciler and whether it can be the way out (the internal uplink
// flag). known is false for a device this test has no opinion on.
func (l ipLink) role() (tunnel, uplink, known bool) {
	kind := l.LinkInfo.Kind
	switch {
	case l.LinkType == "loopback":
		return false, false, true
	case slices.Contains(ipTunnelKinds, kind), slices.Contains(ipTunnelTypes, l.LinkType):
		return true, false, true
	case l.LinkType == "ppp", l.LinkType == "none" && kind == "":
		// A PPP link or a modem without link layer: no subnet to find the peer
		// in, so a tunnel to the Reconciler, and still where the default may lead.
		return true, true, true
	case l.LinkType == "ether" && slices.Contains(ipPlainKinds, kind):
		return false, true, true
	}
	return false, false, false
}

// ipRoute is a route as "ip -j route" prints it.
type ipRoute struct {
	Dst      string          `json:"dst"`
	Gateway  string          `json:"gateway"`
	Dev      string          `json:"dev"`
	Protocol string          `json:"protocol"`
	Scope    string          `json:"scope"`
	Type     string          `json:"type"`
	Table    string          `json:"table"`
	Metric   uint32          `json:"metric"`
	Via      json.RawMessage `json:"via"`
	Nexthops []struct {
		Gateway string          `json:"gateway"`
		Dev     string          `json:"dev"`
		Via     json.RawMessage `json:"via"`
	} `json:"nexthops"`

	family int // 4 or 6; the JSON does not say
	prefix netip.Prefix
	hops   []ipHop
}

// ipHop is one next hop of a route.
type ipHop struct {
	gateway netip.Addr
	dev     string
	// via: the next hop is an address of another family (RTA_VIA).
	via bool
}

func (r ipRoute) blackhole() bool {
	return slices.Contains([]string{"blackhole", "unreachable", "prohibit"}, r.Type)
}

// static: the route was added by a person or a program, not made by the kernel.
func (r ipRoute) static() bool {
	n, known := ipNumber(ipProtocols, r.Protocol)
	return !known || (n != rtprotKernel && n != rtprotRedirect && n != rtprotRA)
}

// ipRule is a policy routing rule as "ip -j rule" prints it.
type ipRule struct {
	Table string `json:"table"`
}

// ipOracle is the machine as ip(8) describes it.
type ipOracle struct {
	links []ipLink
	// main is the main table; other holds the routes of the other tables.
	main, other []ipRoute
	rules       []ipRule
	// sys is /sys/class/net.
	sys fs.FS
}

func unmarshalIP(t testing.TB, what string, raw []byte, v any) {
	t.Helper()
	if len(bytes.TrimSpace(raw)) == 0 {
		return // an empty listing
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("%s: %v: %s", what, err, raw)
	}
}

func ipAddr(t testing.TB, s string) netip.Addr {
	t.Helper()
	if s == "" {
		return netip.Addr{}
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("ip printed the address %q: %v", s, err)
	}
	return a
}

func ipPrefix(t testing.TB, dst string, family int) netip.Prefix {
	t.Helper()
	switch {
	case dst == "default" && family == 6:
		return netip.PrefixFrom(netip.IPv6Unspecified(), 0)
	case dst == "default":
		return netip.PrefixFrom(netip.IPv4Unspecified(), 0)
	case strings.Contains(dst, "/"):
		p, err := netip.ParsePrefix(dst)
		if err != nil {
			t.Fatalf("ip printed the destination %q: %v", dst, err)
		}
		return p
	}
	a := ipAddr(t, dst)
	return netip.PrefixFrom(a, a.BitLen())
}

// newIPOracle reads the files of ipCommands through read. Without IPv6 on the
// machine, ip is not asked about it.
func newIPOracle(t testing.TB, sys fs.FS, ipv6 bool, read func(file string) []byte) *ipOracle {
	t.Helper()
	o := &ipOracle{sys: sys}
	unmarshalIP(t, "ip addr", read("ip-addr.json"), &o.links)
	for i := range o.links {
		l := &o.links[i]
		for _, a := range l.Addrs {
			l.addrs = append(l.addrs, netip.PrefixFrom(ipAddr(t, a.Local).Unmap(), a.PrefixLen))
		}
		slices.SortFunc(l.addrs, comparePrefix)
		l.addrs = slices.Compact(l.addrs)
	}
	for _, family := range []int{4, 6} {
		if family == 6 && !ipv6 {
			continue
		}
		suffix := "-" + strconv.Itoa(family) + ".json"
		o.main = append(o.main, parseIPRoutes(t, read("ip-route-main"+suffix), family)...)
		for _, r := range parseIPRoutes(t, read("ip-route-all"+suffix), family) {
			if n, _ := ipNumber(ipTables, r.Table); n != rtTableMain {
				o.other = append(o.other, r)
			}
		}
		var rules []ipRule
		unmarshalIP(t, "ip rule", read("ip-rule"+suffix), &rules)
		o.rules = append(o.rules, rules...)
	}
	return o
}

func parseIPRoutes(t testing.TB, raw []byte, family int) []ipRoute {
	t.Helper()
	var routes []ipRoute
	unmarshalIP(t, "ip route", raw, &routes)
	for i := range routes {
		r := &routes[i]
		r.family = family
		r.prefix = ipPrefix(t, r.Dst, family)
		if len(r.Nexthops) == 0 {
			r.hops = []ipHop{{gateway: ipAddr(t, r.Gateway), dev: r.Dev}}
		}
		for _, h := range r.Nexthops {
			r.hops = append(r.hops, ipHop{gateway: ipAddr(t, h.Gateway), dev: h.Dev, via: len(h.Via) > 0})
		}
	}
	return routes
}

func readLiveOracle(t testing.TB) *ipOracle {
	t.Helper()
	_, err := os.Stat("/proc/net/if_inet6")
	return newIPOracle(t, os.DirFS(sysClassNet), err == nil, func(file string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		args := append([]string{"-j"}, ipCommands[file]...)
		out, err := exec.CommandContext(ctx, "ip", args...).Output()
		if err != nil {
			t.Fatalf("ip %s: %v", strings.Join(args, " "), err)
		}
		return out
	})
}

func readTestdata(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// readRecordedOracle is the recorded machine as ip describes it, with its
// /sys/class/net.
func readRecordedOracle(t testing.TB) (*ipOracle, fstest.MapFS) {
	t.Helper()
	var files map[string]*string
	unmarshalIP(t, "sys-class-net.json", readTestdata(t, "sys-class-net.json"), &files)
	sys := fstest.MapFS{}
	for path, content := range files {
		if content == nil {
			sys[path] = &fstest.MapFile{Mode: fs.ModeDir | 0o755}
		} else {
			sys[path] = &fstest.MapFile{Data: []byte(*content)}
		}
	}
	return newIPOracle(t, sys, true, func(file string) []byte { return readTestdata(t, file) }), sys
}

// requireNetlink skips the test unless the adapter can read the network.
func requireNetlink(t *testing.T) {
	t.Helper()
	conn, err := openNetlink(0)
	if err != nil {
		t.Skipf("the adapter cannot read the network: %v", err)
	}
	conn.close()
}

// requireLive skips the test unless this machine can be read with ip and with
// the adapter.
func requireLive(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("ip(8) is not installed")
	}
	if out, err := exec.Command("ip", "-j", "-d", "addr", "show").CombinedOutput(); err != nil {
		t.Skipf("ip -j does not work here: %v: %s", err, bytes.TrimSpace(out))
	}
	requireNetlink(t)
}

// findings are the disagreements a check found, one line each.
type findings []string

func (f *findings) add(format string, args ...any) { *f = append(*f, fmt.Sprintf(format, args...)) }

// agree runs check until it finds nothing, up to liveAttempts times.
func agree(t *testing.T, check func() findings) {
	t.Helper()
	var f findings
	for attempt := range liveAttempts {
		if attempt > 0 {
			time.Sleep(200 * time.Millisecond)
		}
		if f = check(); len(f) == 0 {
			return
		}
	}
	for _, line := range f {
		t.Error(line)
	}
}

// routeKey is what Dump and ip route must agree on for each route; the
// gateway is without the zone Dump gives a link-local one.
type routeKey struct {
	Dst       netip.Prefix
	Gateway   netip.Addr
	Iface     string
	IfIndex   uint32
	Metric    uint32
	Blackhole bool
	Static    bool
}

func keyOf(r osnet.Route) routeKey {
	return routeKey{r.Dst, r.Gateway.WithZone(""), r.Iface, r.IfIndex, r.Metric, r.Blackhole, r.Static}
}

func (k routeKey) String() string {
	s := k.Dst.String()
	if k.Blackhole {
		s = "blackhole " + s
	}
	if k.Gateway.IsValid() {
		s += " via " + k.Gateway.String()
	}
	s += fmt.Sprintf(" dev %s(%d) metric %d", k.Iface, k.IfIndex, k.Metric)
	if k.Static {
		s += " static"
	}
	return s
}

// wantRoute is a route as Dump must show it.
type wantRoute struct {
	routeKey
	table string
	// flags is the type, protocol and scope packed as Route.Flags does, when ip
	// printed names that are known.
	flags      uint32
	flagsKnown bool
}

func (o *ipOracle) index(name string) uint32 {
	for _, l := range o.links {
		if l.Name == name {
			return uint32(l.Index)
		}
	}
	return 0
}

func (o *ipOracle) link(name string) (ipLink, bool) {
	for _, l := range o.links {
		if l.Name == name {
			return l, true
		}
	}
	return ipLink{}, false
}

// wantRoutes lists what Dump must make of these routes: one route for each next
// hop of a route that sends traffic somewhere. Local, broadcast and multicast
// entries are bookkeeping for addresses, and a route that leads nowhere in
// particular or through a next hop of another family cannot be an osnet.Route.
func (o *ipOracle) wantRoutes(routes []ipRoute) []wantRoute {
	var out []wantRoute
	for _, r := range routes {
		if (r.Type != "" && r.Type != "unicast" && !r.blackhole()) || len(r.Via) > 0 {
			continue
		}
		typ, typeKnown := ipNumber(ipRouteTypes, r.Type)
		proto, protoKnown := ipNumber(ipProtocols, r.Protocol)
		scope, scopeKnown := ipNumber(ipScopes, r.Scope)
		for _, h := range r.hops {
			if h.via || (!r.blackhole() && h.dev == "" && !h.gateway.IsValid()) ||
				(h.gateway.IsValid() && h.gateway.Is4() != r.prefix.Addr().Is4()) {
				continue
			}
			out = append(out, wantRoute{
				routeKey:   routeKey{r.prefix, h.gateway, h.dev, o.index(h.dev), r.Metric, r.blackhole(), r.static()},
				table:      cmp.Or(r.Table, "main"),
				flags:      typ<<16 | proto<<8 | scope,
				flagsKnown: typeKnown && protoKnown && scopeKnown && typ < 256 && proto < 256 && scope < 256,
			})
		}
	}
	return out
}

// checkDump compares the routes Dump returned with the main table ip shows.
func checkDump(o *ipOracle, got []osnet.Route) findings {
	var f findings
	want := o.wantRoutes(o.main)
	elsewhere := map[routeKey]string{}
	for _, w := range o.wantRoutes(o.other) {
		elsewhere[w.routeKey] = w.table
	}
	unmatched := map[routeKey]int{}
	flagsOf := map[routeKey][]uint32{}
	for _, w := range want {
		unmatched[w.routeKey]++
		if w.flagsKnown {
			flagsOf[w.routeKey] = append(flagsOf[w.routeKey], w.flags)
		}
	}
	for _, r := range got {
		k := keyOf(r)
		switch table, other := elsewhere[k]; {
		case unmatched[k] > 0:
			unmatched[k]--
			if flags, ok := flagsOf[k]; ok && !slices.Contains(flags, r.Flags) {
				f.add("%s: Flags %#x, ip route shows (type, protocol, scope) %#x", k, r.Flags, flags)
			}
		case other:
			f.add("Dump has %s, which is in table %s and not in the main table", k, table)
		default:
			f.add("Dump has %s, ip route does not", k)
		}
		if r.Dst != r.Dst.Masked() {
			f.add("%s: destination is not masked", k)
		}
		if r.Scoped {
			f.add("%s: Scoped is set, which is macOS only", k)
		}
		// A link-local gateway means nothing without its interface.
		if r.Gateway.Is6() && r.Gateway.IsLinkLocalUnicast() && r.Gateway.Zone() != r.Iface {
			f.add("%s: link-local gateway %s has the zone %q, want %q", k, r.Gateway, r.Gateway.Zone(), r.Iface)
		}
	}
	for _, w := range want {
		if unmatched[w.routeKey] > 0 {
			unmatched[w.routeKey]--
			f.add("ip route has %s, Dump does not", w.routeKey)
		}
	}
	sorted := slices.Clone(got)
	sortRoutes(sorted)
	if !slices.Equal(sorted, got) {
		f.add("Dump is not sorted")
	}
	return f
}

// checkInterfaces compares the interfaces of a snapshot, and the internal
// uplink flag of the links they come from, with what ip addr shows.
func checkInterfaces(o *ipOracle, got []osnet.Interface, links []link) findings {
	var f findings
	if !slices.IsSortedFunc(got, func(a, b osnet.Interface) int { return cmp.Compare(a.Index, b.Index) }) {
		f.add("interfaces are not in the order of their indexes")
	}
	byIndex := map[int]osnet.Interface{}
	for _, g := range got {
		byIndex[g.Index] = g
	}
	uplinkByIndex := map[int]bool{}
	for _, l := range links {
		uplinkByIndex[l.Index] = l.Uplink
	}
	for _, l := range o.links {
		g, ok := byIndex[l.Index]
		if !ok {
			f.add("interface %s (%d) is in ip addr, not in the snapshot", l.Name, l.Index)
			continue
		}
		delete(byIndex, l.Index)
		if g.Name != l.Name || g.Up != l.up() || g.Metric != 0 {
			f.add("interface %d is %+v, ip addr shows %s, up %v", l.Index, g, l.Name, l.up())
		}
		if !slices.Equal(g.Addrs, l.addrs) {
			f.add("%s: addresses %v, ip addr shows %v", l.Name, g.Addrs, l.addrs)
		}
		if tunnel, uplink, known := l.role(); known {
			if g.Tunnel != tunnel {
				f.add("%s (%s, %s): Tunnel is %v", l.Name, l.LinkType, cmp.Or(l.LinkInfo.Kind, "no kind"), g.Tunnel)
			}
			if uplinkByIndex[l.Index] != uplink {
				f.add("%s (%s, %s): uplink is %v", l.Name, l.LinkType, cmp.Or(l.LinkInfo.Kind, "no kind"), uplinkByIndex[l.Index])
			}
		}
	}
	for _, g := range byIndex {
		f.add("interface %s (%d) is in the snapshot, not in ip addr", g.Name, g.Index)
	}
	return f
}

// uplinks says for each interface index whether it can be the way out: what
// ip says of it, or what the adapter says where ip leaves it open.
func (o *ipOracle) uplinks(links []link) map[int]bool {
	adapter := map[int]bool{}
	for _, l := range links {
		adapter[l.Index] = l.Uplink
	}
	out := map[int]bool{}
	for _, l := range o.links {
		_, uplink, known := l.role()
		out[l.Index] = uplink
		if !known {
			out[l.Index] = adapter[l.Index]
		}
	}
	return out
}

// wantDefault is the default route of one family that the Reconciler should
// take as the way out: of the ones that leave through an uplink that is up, the
// lowest metric, then the lowest interface index.
func (o *ipOracle) wantDefault(links []link, v6 bool) (wantRoute, bool) {
	uplinks := o.uplinks(links)
	var best wantRoute
	found := false
	for _, w := range o.wantRoutes(o.main) {
		l, ok := o.link(w.Iface)
		if w.Dst.Bits() != 0 || w.Dst.Addr().Is6() != v6 || w.Blackhole || !ok || !l.up() || !uplinks[l.Index] {
			continue
		}
		if !found || cmp.Or(cmp.Compare(w.Metric, best.Metric), cmp.Compare(w.IfIndex, best.IfIndex), w.Gateway.Compare(best.Gateway)) < 0 {
			best, found = w, true
		}
	}
	return best, found
}

// devtype is the DEVTYPE line of the uevent file of an interface.
func (o *ipOracle) devtype(name string) string {
	b, err := fs.ReadFile(o.sys, name+"/uevent")
	if err != nil {
		return ""
	}
	for line := range strings.Lines(string(b)) {
		if devtype, ok := strings.CutPrefix(strings.TrimSpace(line), "DEVTYPE="); ok {
			return devtype
		}
	}
	return ""
}

// wantKind is the kind of network an interface connects to: a wireless device
// for the kernel (DEVTYPE wlan), an Ethernet-like device with a hardware device
// behind it (the bus ip prints; the device link of sysfs where it does not
// print one), and anything else otherwise.
func (o *ipOracle) wantKind(l ipLink) osnet.LinkKind {
	devtype := o.devtype(l.Name)
	_, err := fs.Stat(o.sys, l.Name+"/device")
	switch {
	case devtype == "wlan":
		return osnet.LinkWiFi
	case l.LinkType == "ether" && (l.ParentBus != "" || err == nil) && devtype != "wwan":
		return osnet.LinkEthernet
	}
	return osnet.LinkOther
}

// checkState compares a snapshot with the machine ip describes: the interfaces,
// the way out with the kind of its interface, and the connected subnets.
func checkState(o *ipOracle, state osnet.NetState, links []link) findings {
	f := checkInterfaces(o, state.Interfaces, links)
	if state.Epoch == 0 {
		f.add("epoch is 0")
	}
	for _, v6 := range []bool{false, true} {
		family, got := "IPv4", state.DefaultV4
		if v6 {
			family, got = "IPv6", state.DefaultV6
		}
		want, ok := o.wantDefault(links, v6)
		switch {
		case !ok && got == nil:
		case !ok:
			f.add("%s default is %+v, ip route has no default through an uplink that is up", family, got)
		case got == nil:
			f.add("no %s default, ip route has %s", family, want.routeKey)
		default:
			if got.Iface != want.Iface || got.Gateway.WithZone("") != want.Gateway {
				f.add("%s default is via %v on %s, ip route says %s", family, got.Gateway, got.Iface, want.routeKey)
			}
			if got.Gateway.Is6() && got.Gateway.IsLinkLocalUnicast() && got.Gateway.Zone() != got.Iface {
				f.add("%s default: link-local gateway %s has the zone %q, want %q", family, got.Gateway, got.Gateway.Zone(), got.Iface)
			}
			if l, _ := o.link(want.Iface); got.Kind != o.wantKind(l) {
				f.add("%s default through %s has kind %s, want %s", family, got.Iface, kindName(got.Kind), kindName(o.wantKind(l)))
			}
		}
	}

	uplinks := o.uplinks(links)
	var wantConnected []netip.Prefix
	for _, l := range o.links {
		if !l.up() || !uplinks[l.Index] {
			continue
		}
		for _, a := range l.addrs {
			if !a.Addr().IsLinkLocalUnicast() {
				wantConnected = append(wantConnected, a.Masked())
			}
		}
	}
	slices.SortFunc(wantConnected, comparePrefix)
	if wantConnected = slices.Compact(wantConnected); !slices.Equal(state.Connected, wantConnected) {
		f.add("connected subnets %v, ip addr says %v", state.Connected, wantConnected)
	}

	// Whatever else is true, a tunnel is neither the way out nor on-link.
	for _, l := range o.links {
		if tunnel, uplink, known := l.role(); !known || !tunnel || uplink {
			continue
		}
		for _, next := range []*osnet.Nexthop{state.DefaultV4, state.DefaultV6} {
			if next != nil && next.Iface == l.Name {
				f.add("the tunnel %s is the default", l.Name)
			}
		}
		for _, a := range l.addrs {
			if slices.Contains(state.Connected, a.Masked()) && !slices.Contains(wantConnected, a.Masked()) {
				f.add("the subnet %v of the tunnel %s is connected", a.Masked(), l.Name)
			}
		}
	}
	return f
}

// policyTables are the tables the rules send traffic to besides the three the
// system has by itself.
func (o *ipOracle) policyTables() []string {
	var tables []string
	for _, r := range o.rules {
		if r.Table != "" && !slices.Contains([]string{"local", "main", "default"}, r.Table) {
			tables = append(tables, r.Table)
		}
	}
	slices.Sort(tables)
	return slices.Compact(tables)
}

func (o *ipOracle) describe() string {
	return fmt.Sprintf("%d interfaces, %d routes in the main table, %d in other tables, policy routing to %v",
		len(o.links), len(o.main), len(o.other), o.policyTables())
}

// Dump agrees with ip route about the main table, and nothing from another
// table is in it.
func TestLiveDumpAgreesWithIP(t *testing.T) {
	requireLive(t)
	var o *ipOracle
	var routes []osnet.Route
	agree(t, func() findings {
		var err error
		if routes, err = NewRouteTable().Dump(); err != nil {
			t.Fatal(err)
		}
		o = readLiveOracle(t)
		return checkDump(o, routes)
	})
	linkLocal := 0
	for _, r := range routes {
		if r.Dst.Addr().Is6() && r.Dst.Addr().IsLinkLocalUnicast() || r.Gateway.Is6() && r.Gateway.IsLinkLocalUnicast() {
			linkLocal++
		}
	}
	t.Logf("%d routes dumped, %d of them link-local; %s", len(routes), linkLocal, o.describe())
}

// Snapshot agrees with ip addr and ip route about the interfaces, the way out
// and the connected subnets.
func TestLiveSnapshotAgreesWithIP(t *testing.T) {
	requireLive(t)
	monitor := NewNetMonitor(NetMonitorOptions{Logger: discardLog()})
	var state osnet.NetState
	agree(t, func() findings {
		var err error
		if state, err = monitor.Snapshot(); err != nil {
			t.Fatal(err)
		}
		links, err := listLinks()
		if err != nil {
			t.Fatal(err)
		}
		return checkState(readLiveOracle(t), state, links)
	})
	t.Logf("epoch %d, IPv4 default %+v, IPv6 default %+v, connected %v", state.Epoch, state.DefaultV4, state.DefaultV6, state.Connected)
}

// Two snapshots of a machine that did not change in between have one epoch.
func TestLiveEpochIsStable(t *testing.T) {
	requireNetlink(t)
	monitor := NewNetMonitor(NetMonitorOptions{Logger: discardLog()})
	for range liveAttempts {
		first, err := monitor.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		second, err := monitor.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if second.Epoch < first.Epoch || first.Epoch == 0 {
			t.Fatalf("epochs %d, %d", first.Epoch, second.Epoch)
		}
		changed := first.Epoch != second.Epoch
		first.Epoch, second.Epoch = 0, 0
		if reflect.DeepEqual(first, second) {
			if changed {
				t.Fatal("the epoch moved between two identical snapshots")
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Skip("the network kept changing")
}

// replay turns what ip recorded into the messages the kernel would send for
// it, and those into what the adapter makes of them: the interfaces and the
// routes of every table.
func (o *ipOracle) replay(t testing.TB) ([]link, []osnet.Route) {
	t.Helper()
	flagBits := map[string]uint32{"UP": iffUp, "LOOPBACK": iffLoopback, "LOWER_UP": iffLowerUp}
	var msgs []rtMessage
	for _, l := range o.links {
		typ, ok := ipLinkTypes[l.LinkType]
		if !ok {
			t.Fatalf("the replay does not know the link type %q", l.LinkType)
		}
		var flags uint32
		for _, name := range l.Flags {
			flags |= flagBits[name]
		}
		msgs = append(msgs, rtMessage{Type: rtmNewLink, Link: &linkMsg{Type: typ, Index: uint32(l.Index), Flags: flags, Name: l.Name, Kind: l.LinkInfo.Kind}})
		for _, a := range l.addrs {
			family := uint8(afInet6)
			if a.Addr().Is4() {
				family = afInet
			}
			msgs = append(msgs, rtMessage{Type: rtmNewAddr, Addr: &addrMsg{Family: family, PrefixLen: uint8(a.Bits()), Index: uint32(l.Index), Address: a.Addr(), Local: a.Addr()}})
		}
	}
	names := map[uint32]string{}
	for _, l := range o.links {
		names[uint32(l.Index)] = l.Name
	}
	var routes []osnet.Route
	for _, r := range slices.Concat(o.main, o.other) {
		routes = append(routes, routesFromMessage(r.message(t, o), func(index uint32) string { return names[index] })...)
	}
	sortRoutes(routes)
	return buildLinks(msgs), routes
}

// message is the RTM_NEWROUTE the kernel would send for the route.
func (r ipRoute) message(t testing.TB, o *ipOracle) routeMsg {
	t.Helper()
	number := func(names map[string]uint32, name string) uint32 {
		n, ok := ipNumber(names, name)
		if !ok {
			n = 200 // a name from a file of the machine: the number does not matter
		}
		return n
	}
	typ, ok := ipNumber(ipRouteTypes, r.Type)
	if !ok {
		t.Fatalf("the replay does not know the route type %q", r.Type)
	}
	m := routeMsg{
		Family:   afInet,
		DstLen:   uint8(r.prefix.Bits()),
		Table:    number(ipTables, r.Table),
		Protocol: uint8(number(ipProtocols, r.Protocol)),
		Scope:    uint8(number(ipScopes, r.Scope)),
		Type:     uint8(typ),
		Priority: r.Metric,
		Via:      len(r.Via) > 0,
	}
	if r.family == 6 {
		m.Family = afInet6
	}
	// A default route has no destination attribute.
	if r.prefix.Bits() > 0 {
		m.Dst = r.prefix.Addr()
	}
	if len(r.Nexthops) == 0 {
		m.Gateway, m.OIf = r.hops[0].gateway, o.index(r.hops[0].dev)
	} else {
		for _, h := range r.hops {
			m.NextHops = append(m.NextHops, nextHop{OIf: o.index(h.dev), Gateway: h.gateway, Via: h.via})
		}
	}
	return m
}

// recordedMonitor is a monitor that reads the recorded machine.
func recordedMonitor(links []link, routes []osnet.Route, sys fs.FS) *netMonitor {
	return &netMonitor{
		routes: &stubRoutes{routes: routes},
		links:  func() ([]link, error) { return links, nil },
		kinds:  newLinkKinds(sys),
	}
}

// The checks of the live tests, on the recording.
func TestRecordedMachineAgreesWithIP(t *testing.T) {
	o, sys := readRecordedOracle(t)
	links, routes := o.replay(t)
	for _, line := range checkDump(o, routes) {
		t.Error(line)
	}
	state, err := recordedMonitor(links, routes, sys).Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range checkState(o, state, links) {
		t.Error(line)
	}
}

// What the recording shows, written down: a wg-quick style full tunnel keeps its
// default route in table 51820 where the Reconciler does not look, the uplink is
// the way out, and the tunnel is neither.
func TestRecordedMachineFacts(t *testing.T) {
	o, sys := readRecordedOracle(t)
	links, routes := o.replay(t)

	var dumped []string
	for _, r := range routes {
		dumped = append(dumped, keyOf(r).String())
	}
	wantRoutes := []string{
		"0.0.0.0/0 via 192.168.50.1 dev ens18(2) metric 100 static",
		"10.6.0.0/24 via 192.168.50.1 dev ens18(2) metric 100 static",
		"192.168.50.0/24 dev ens18(2) metric 100",
		"2001:db8:110:8a92:c758:82fe:a69d:38f3/128 dev wgcf(3) metric 256",
		"fe80::/64 dev ens18(2) metric 1024",
	}
	if !slices.Equal(dumped, wantRoutes) {
		t.Errorf("routes\n%s\nwant\n%s", strings.Join(dumped, "\n"), strings.Join(wantRoutes, "\n"))
	}
	if got := o.policyTables(); !slices.Equal(got, []string{"51820"}) {
		t.Errorf("policy routing to %v, want table 51820", got)
	}
	var tunnelDefaults int
	for _, w := range o.wantRoutes(o.other) {
		if w.table == "51820" && w.Dst.Bits() == 0 && w.Iface == "wgcf" {
			tunnelDefaults++
		}
	}
	if tunnelDefaults != 2 {
		t.Errorf("the recording has %d default routes through wgcf in table 51820, want 2 (the test needs them to show that they stay out)", tunnelDefaults)
	}

	monitor := recordedMonitor(links, routes, sys)
	state, err := monitor.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	tunnels := map[string]bool{}
	for _, iface := range state.Interfaces {
		tunnels[iface.Name] = iface.Tunnel
	}
	if !tunnels["wgcf"] || tunnels["ens18"] || tunnels["lo"] {
		t.Errorf("Tunnel is %v, want wgcf only", tunnels)
	}
	if want := (&osnet.Nexthop{Gateway: addr("192.168.50.1"), Iface: "ens18", Kind: osnet.LinkEthernet}); state.DefaultV4 == nil || *state.DefaultV4 != *want {
		t.Errorf("IPv4 default %+v, want %+v", state.DefaultV4, want)
	}
	if state.DefaultV6 != nil {
		t.Errorf("IPv6 default %+v, but the machine has no IPv6 default route outside table 51820", state.DefaultV6)
	}
	if want := prefixes("192.168.50.0/24"); !slices.Equal(state.Connected, want) {
		t.Errorf("connected %v, want %v", state.Connected, want)
	}
	for _, l := range links {
		if l.Name == "wgcf" && (l.Uplink || !l.Tunnel || !slices.Contains(l.Addrs, prefix("172.16.0.2/32"))) {
			t.Errorf("wgcf is %+v", l)
		}
	}

	// The tunnel comes and goes on its own and does not move the epoch; the
	// uplink losing its address does.
	again, err := monitor.Snapshot()
	if err != nil || again.Epoch != state.Epoch {
		t.Errorf("epoch %d then %d, %v, without a change", state.Epoch, again.Epoch, err)
	}
	withoutTunnel := slices.DeleteFunc(slices.Clone(links), func(l link) bool { return l.Name == "wgcf" })
	monitor.links = func() ([]link, error) { return withoutTunnel, nil }
	if gone, err := monitor.Snapshot(); err != nil || gone.Epoch != state.Epoch {
		t.Errorf("epoch %d after the tunnel went away, %v, want %d", gone.Epoch, err, state.Epoch)
	}
	for i := range withoutTunnel {
		if withoutTunnel[i].Name == "ens18" {
			withoutTunnel[i].Addrs = slices.Clone(withoutTunnel[i].Addrs[1:])
		}
	}
	if moved, err := monitor.Snapshot(); err != nil || moved.Epoch == state.Epoch {
		t.Errorf("epoch %d after the uplink lost its address, %v, want a new one", moved.Epoch, err)
	}
}

// A check that cannot fail would pass on every machine: each difference between
// the adapter and ip that these checks exist for is noticed, and named.
func TestChecksNoticeDifferences(t *testing.T) {
	o, sys := readRecordedOracle(t)
	links, routes := o.replay(t)

	dumps := []struct {
		name, want string
		mutate     func(r []osnet.Route) []osnet.Route
	}{
		{"a route is missing", "Dump does not", func(r []osnet.Route) []osnet.Route { return r[1:] }},
		{"a metric differs", "metric 101", func(r []osnet.Route) []osnet.Route { r[0].Metric++; return r }},
		{"the protocol differs", "Dump has 0.0.0.0/0 via 192.168.50.1 dev ens18(2) metric 100,", func(r []osnet.Route) []osnet.Route { r[0].Static = false; return r }},
		{"the flags differ", "Flags", func(r []osnet.Route) []osnet.Route { r[0].Flags ^= 0x100; return r }},
		{"a route of table 51820 leaks in", "in table 51820", func(r []osnet.Route) []osnet.Route {
			return append(r, osnet.Route{Dst: prefix("0.0.0.0/0"), Iface: "wgcf", IfIndex: 3, Static: true})
		}},
		{"the order differs", "not sorted", func(r []osnet.Route) []osnet.Route { r[0], r[1] = r[1], r[0]; return r }},
	}
	for _, tt := range dumps {
		t.Run(tt.name, func(t *testing.T) {
			if f := checkDump(o, tt.mutate(slices.Clone(routes))); !strings.Contains(strings.Join(f, "\n"), tt.want) {
				t.Errorf("findings %q, want one with %q", f, tt.want)
			}
		})
	}

	states := []struct {
		name, want string
		mutate     func(s *osnet.NetState)
	}{
		{"the epoch is 0", "epoch is 0", func(s *osnet.NetState) { s.Epoch = 0 }},
		{"the tunnel is the default", "the tunnel wgcf is the default", func(s *osnet.NetState) { s.DefaultV4.Iface = "wgcf" }},
		{"the way out is lost", "no IPv4 default", func(s *osnet.NetState) { s.DefaultV4 = nil }},
		{"the kind is wrong", "has kind LinkWiFi, want LinkEthernet", func(s *osnet.NetState) { s.DefaultV4.Kind = osnet.LinkWiFi }},
		{"the tunnel's address is connected", "of the tunnel wgcf is connected", func(s *osnet.NetState) {
			s.Connected = append(s.Connected, prefix("172.16.0.2/32"))
		}},
		{"the tunnel is no tunnel", "Tunnel is false", func(s *osnet.NetState) { s.Interfaces[2].Tunnel = false }},
		{"an address is lost", "ip addr shows", func(s *osnet.NetState) { s.Interfaces[1].Addrs = s.Interfaces[1].Addrs[:1] }},
	}
	for _, tt := range states {
		t.Run(tt.name, func(t *testing.T) {
			state, err := recordedMonitor(links, routes, sys).Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			tt.mutate(&state)
			if f := checkState(o, state, links); !strings.Contains(strings.Join(f, "\n"), tt.want) {
				t.Errorf("findings %q, want one with %q", f, tt.want)
			}
		})
	}

	t.Run("a tunnel is an uplink", func(t *testing.T) {
		state, err := recordedMonitor(links, routes, sys).Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		wrong := slices.Clone(links)
		for i := range wrong {
			wrong[i].Uplink = wrong[i].Uplink || wrong[i].Name == "wgcf"
		}
		if f := checkState(o, state, wrong); !strings.Contains(strings.Join(f, "\n"), "wgcf (none, wireguard): uplink is true") {
			t.Errorf("findings %q", f)
		}
	})
}

// statusLink is the line that starts the section of a link in resolvectl status.
var statusLink = regexp.MustCompile(`^Link [0-9]+ \((.+)\)$`)

// statusLinks reads the sections of the links in the output of resolvectl
// status: the servers, the domains and the default-route setting of each link,
// which readLink reads with three other commands.
func statusLinks(status string) map[string]linkConfig {
	links := map[string]linkConfig{}
	var name string
	for line := range strings.Lines(status) {
		if m := statusLink.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			name = m[1]
			links[name] = linkConfig{}
			continue
		}
		key, value, _ := strings.Cut(strings.TrimSpace(line), ":")
		cfg := links[name]
		switch key {
		case "DNS Servers":
			cfg.Servers = canonicalServers(strings.Fields(value))
		case "DNS Domain":
			cfg.Domains = canonicalDomains(strings.Fields(value))
		case "Default Route":
			cfg.DefaultRoute = strings.TrimSpace(value) == "yes"
		}
		if name != "" {
			links[name] = cfg
		}
	}
	return links
}

// recordedResolvectl plays resolvectl with what it printed on the recorded
// machine. "resolvectl dns|domain|default-route -- IFACE" prints the line of
// IFACE in the listing of the verb without an interface; that was checked
// against the real command for each link when the recording was made.
func recordedResolvectl(t *testing.T) CommandRunner {
	listings := map[string]string{}
	for _, verb := range []string{"dns", "domain", "default-route"} {
		listings[verb] = string(readTestdata(t, "resolvectl-"+verb+".txt"))
	}
	return func(_ context.Context, name string, args []string, _ string) ([]byte, error) {
		listing, ok := listings[args[0]]
		switch {
		case !ok:
			t.Errorf("%s ran with %q", name, args)
			return nil, errors.New("unexpected command")
		case len(args) == 1:
			return []byte(listing), nil
		case len(args) == 3 && args[1] == "--":
			for line := range strings.Lines(listing) {
				if m := linkLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil && m[1] == args[2] {
					return []byte(line), nil
				}
			}
		}
		t.Errorf("%s ran with %q, which the recording has no answer to", name, args)
		return nil, errors.New("unexpected command")
	}
}

// The read-back of the DNS adapter accepts what resolvectl printed on the
// recorded machine, and agrees with resolvectl status about every link.
func TestRecordedResolvectlIsRead(t *testing.T) {
	d := newDNSConfigurator(DNSOptions{Run: recordedResolvectl(t), Logger: dnsTestLog()})
	if err := d.checkResolved(); err != nil {
		t.Fatalf("checkResolved: %v", err)
	}
	status := statusLinks(string(readTestdata(t, "resolvectl-status.txt")))
	if len(status) < 3 {
		t.Fatalf("resolvectl status shows %d links", len(status))
	}
	for name, want := range status {
		got, err := d.readLink(name)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !got.equal(want) {
			t.Errorf("%s: readLink reads %s, resolvectl status shows %s", name, got, want)
		}
	}

	wgcf, err := d.readLink("wgcf")
	want := linkConfig{Servers: []string{"1.1.1.1", "1.0.0.1", "2606:4700:4700::1111", "2606:4700:4700::1001"}, Domains: []string{"~."}, DefaultRoute: true}
	if err != nil || !wgcf.equal(want) {
		t.Errorf("wgcf is %s, %v, want %s", wgcf, err, want)
	}
	ens18, err := d.readLink("ens18")
	want = linkConfig{Servers: []string{"192.168.50.1"}, DefaultRoute: true}
	if err != nil || !ens18.equal(want) {
		t.Errorf("ens18 is %s, %v, want %s", ens18, err, want)
	}
}
