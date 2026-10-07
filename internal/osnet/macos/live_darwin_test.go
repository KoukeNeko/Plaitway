package macos

// Live tests read this Mac: the routing table, the interfaces, the dynamic
// store. They write nothing: lookups are RTM_GET requests, which an
// unprivileged socket may send, and scutil is only given "list" and the local
// dictionary commands. The writing code is tested by
// rootintegration_darwin_test.go, which needs the rootintegration tag and root.

import (
	"context"
	"encoding/binary"
	"errors"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

func TestSystemConstantsMatch(t *testing.T) {
	tests := []struct {
		name      string
		got, want int
	}{
		{"RTM_VERSION", rtmVersion, unix.RTM_VERSION},
		{"RTM_ADD", rtmAdd, unix.RTM_ADD},
		{"RTM_DELETE", rtmDelete, unix.RTM_DELETE},
		{"RTM_CHANGE", rtmChange, unix.RTM_CHANGE},
		{"RTM_GET", rtmGet, unix.RTM_GET},
		{"RTM_MISS", rtmMiss, unix.RTM_MISS},
		{"RTM_NEWADDR", rtmNewAddr, unix.RTM_NEWADDR},
		{"RTM_DELADDR", rtmDelAddr, unix.RTM_DELADDR},
		{"RTM_IFINFO", rtmIfInfo, unix.RTM_IFINFO},
		{"RTF_UP", rtfUp, unix.RTF_UP},
		{"RTF_GATEWAY", rtfGateway, unix.RTF_GATEWAY},
		{"RTF_HOST", rtfHost, unix.RTF_HOST},
		{"RTF_LLINFO", rtfLLInfo, unix.RTF_LLINFO},
		{"RTF_STATIC", rtfStatic, unix.RTF_STATIC},
		{"RTF_BLACKHOLE", rtfBlackhole, unix.RTF_BLACKHOLE},
		{"RTF_WASCLONED", rtfWasCloned, unix.RTF_WASCLONED},
		{"RTF_BROADCAST", rtfBroadcast, unix.RTF_BROADCAST},
		{"RTF_MULTICAST", rtfMulticast, unix.RTF_MULTICAST},
		{"RTF_IFSCOPE", rtfIfScope, unix.RTF_IFSCOPE},
		{"RTA_DST", rtaDst, unix.RTA_DST},
		{"RTA_GATEWAY", rtaGateway, unix.RTA_GATEWAY},
		{"RTA_NETMASK", rtaNetmask, unix.RTA_NETMASK},
		{"RTA_IFA", rtaIFA, unix.RTA_IFA},
		{"RTAX_DST", rtaxDst, unix.RTAX_DST},
		{"RTAX_GATEWAY", rtaxGateway, unix.RTAX_GATEWAY},
		{"RTAX_NETMASK", rtaxNetmask, unix.RTAX_NETMASK},
		{"RTAX_IFA", rtaxIFA, unix.RTAX_IFA},
		{"RTAX_MAX", rtaxMax, unix.RTAX_MAX},
		{"AF_INET", afInet, unix.AF_INET},
		{"AF_LINK", afLink, unix.AF_LINK},
		{"AF_INET6", afInet6, unix.AF_INET6},
		{"sizeof sockaddr_in", sockaddrInLen, unix.SizeofSockaddrInet4},
		{"sizeof sockaddr_in6", sockaddrIn6Len, unix.SizeofSockaddrInet6},
		{"sizeof sockaddr_dl", sockaddrDLLen, unix.SizeofSockaddrDatalink},
		{"sizeof rt_msghdr", rtMsghdrLen, unix.SizeofRtMsghdr},
		{"sizeof ifa_msghdr", ifaMsghdrLen, unix.SizeofIfaMsghdr},
		{"sizeof if_msghdr", ifMsghdrLen, unix.SizeofIfMsghdr},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %d, the system has %d", tt.name, tt.got, tt.want)
		}
	}
}

// lookup asks the kernel for a route with an RTM_GET request built by the
// encoder under test, and decodes the answer with the parser under test.
func lookup(t *testing.T, req routeRequest) (osnet.Route, error) {
	t.Helper()
	const seq = 4242
	req.Type, req.Flags = rtmGet, req.Flags|rtfUp
	req.PID, req.Seq = int32(os.Getpid()), seq
	sock, err := openRouteSocket()
	if err != nil {
		t.Fatal(err)
	}
	defer sock.Close()
	if _, err := sock.Write(req.marshal()); err != nil {
		return osnet.Route{}, routeError("get", req.Dst, err)
	}
	names, err := interfaceNames()
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1<<16)
	if err := sock.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		n, err := sock.Read(buf)
		if err != nil {
			t.Fatalf("read the answer for %s: %v", req.Dst, err)
		}
		// Every route socket gets every answer; ours has our pid and sequence.
		if binary.NativeEndian.Uint32(buf[16:]) != uint32(os.Getpid()) || binary.NativeEndian.Uint32(buf[20:]) != seq {
			continue
		}
		msgs, err := parseMessages(buf[:n])
		if err != nil || len(msgs) != 1 {
			t.Fatalf("answer for %s: %d messages, %v", req.Dst, len(msgs), err)
		}
		route, ok := routeFromMessage(msgs[0], func(i int) string { return names[i] })
		if !ok {
			t.Fatalf("answer for %s is not a route", req.Dst)
		}
		return route, nil
	}
}

func dump(t *testing.T) []osnet.Route {
	t.Helper()
	routes, err := NewRouteTable().Dump()
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) == 0 {
		t.Fatal("the routing table is empty")
	}
	return routes
}

func TestLiveDump(t *testing.T) {
	routes := dump(t)
	var defaults []osnet.Route
	for _, r := range routes {
		if !r.Dst.IsValid() || r.Dst != r.Dst.Masked() {
			t.Errorf("route with destination %v", r.Dst)
		}
		if r.Scoped != (r.Flags&unix.RTF_IFSCOPE != 0) || r.Static != (r.Flags&unix.RTF_STATIC != 0) {
			t.Errorf("%v: flags %#x, scoped %v, static %v", r.Dst, r.Flags, r.Scoped, r.Static)
		}
		if r.Flags&unix.RTF_UP == 0 {
			t.Errorf("%v is in the table but not up: %#x", r.Dst, r.Flags)
		}
		if r.Dst.Bits() == 0 {
			defaults = append(defaults, r)
		}
	}
	if len(defaults) == 0 {
		t.Skip("this Mac has no default route")
	}
	for _, r := range defaults {
		if r.Iface == "" {
			t.Errorf("default route %+v has no interface", r)
		}
	}
}

// route(8) is the reference for what the system uses as its default.
func TestLiveDumpAgreesWithRouteGet(t *testing.T) {
	out, err := exec.Command("/sbin/route", "-n", "get", "default").Output()
	if err != nil {
		t.Skipf("route -n get default: %v", err)
	}
	var gateway, iface string
	for _, line := range strings.Split(string(out), "\n") {
		key, value, _ := strings.Cut(strings.TrimSpace(line), ": ")
		switch key {
		case "gateway":
			gateway = value
		case "interface":
			iface = value
		}
	}
	for _, r := range dump(t) {
		if r.Dst != netip.MustParsePrefix("0.0.0.0/0") || r.Scoped {
			continue
		}
		if r.Iface != iface || r.Gateway.String() != orInvalid(gateway) {
			t.Errorf("Dump has default via %s on %s, route -n get default says %q on %q", r.Gateway, r.Iface, gateway, iface)
		}
		return
	}
	t.Errorf("Dump has no unscoped IPv4 default, route -n get default says %q on %q", gateway, iface)
}

func orInvalid(gateway string) string {
	if gateway == "" {
		return netip.Addr{}.String()
	}
	return gateway
}

// Every route in the table can be found by asking the kernel for its prefix
// with the encoder's destination and netmask, scope included, and the answer
// decodes to the same route. This is the encoder's proof against the real
// kernel, without writing anything.
func TestLiveKernelFindsEveryRouteOfTheTable(t *testing.T) {
	routes := dump(t)
	names, err := interfaceNames()
	if err != nil {
		t.Fatal(err)
	}
	index := map[string]int{}
	for i, n := range names {
		index[n] = i
	}
	checked := 0
	for _, r := range routes {
		if r.Dst.Bits() == r.Dst.Addr().BitLen() || coveredByMoreSpecific(r, routes) {
			continue
		}
		req := routeRequest{Dst: r.Dst, Netmask: true}
		if r.Scoped {
			req.Flags, req.Index = rtfIfScope, index[r.Iface]
		}
		got, err := lookup(t, req)
		if err != nil {
			t.Errorf("%v on %s (scoped %v): %v", r.Dst, r.Iface, r.Scoped, err)
			continue
		}
		checked++
		got.Flags, r.Flags = 0, 0
		if got != r {
			t.Errorf("asked for %+v\n  got       %+v", r, got)
		}
	}
	if checked == 0 {
		t.Error("no route was looked up")
	}
}

// coveredByMoreSpecific is true when another route of the same scope has the
// address of r.Dst and a longer prefix: the kernel would answer with that one.
func coveredByMoreSpecific(r osnet.Route, routes []osnet.Route) bool {
	for _, other := range routes {
		if other.Scoped == r.Scoped && other.Iface == r.Iface && other.Dst.Bits() > r.Dst.Bits() && other.Dst.Contains(r.Dst.Addr()) {
			return true
		}
	}
	return false
}

// An address no route covers is answered with ESRCH, which is ErrNotFound; an
// address that some route covers is answered with the most specific one.
func TestLiveKernelAnswersLikeLongestPrefixMatch(t *testing.T) {
	routes := dump(t)
	for _, probe := range []string{"198.51.100.77", "2001:db8:ffff::1", "192.0.2.1", "203.0.113.200"} {
		addr := netip.MustParseAddr(probe)
		var want *osnet.Route
		for i, r := range routes {
			if !r.Scoped && r.Dst.Contains(addr) && (want == nil || r.Dst.Bits() > want.Dst.Bits()) {
				want = &routes[i]
			}
		}
		got, err := lookup(t, routeRequest{Dst: netip.PrefixFrom(addr, addr.BitLen()), Flags: rtfHost})
		switch {
		case want == nil && !errors.Is(err, osnet.ErrNotFound):
			t.Errorf("%s: no route covers it, kernel answered %+v, %v; want ErrNotFound", probe, got, err)
		case want != nil && err != nil:
			t.Errorf("%s: %v, want %v", probe, err, want.Dst)
		case want != nil && (got.Dst != want.Dst || got.Iface != want.Iface || got.Gateway != want.Gateway):
			t.Errorf("%s: kernel answered %+v, want %+v", probe, got, want)
		}
	}
}

func TestLiveSnapshot(t *testing.T) {
	m := NewNetMonitor(NetMonitorOptions{Logger: discardLog()})
	state, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if state.Epoch == 0 {
		t.Error("epoch is 0")
	}
	var lo *osnet.Interface
	for i, iface := range state.Interfaces {
		if iface.Tunnel != isTunnel(iface.Name) {
			t.Errorf("%s: Tunnel = %v", iface.Name, iface.Tunnel)
		}
		if iface.Name == "lo0" {
			lo = &state.Interfaces[i]
		}
	}
	if lo == nil || !lo.Up || !slicesContainsPrefix(lo.Addrs, "127.0.0.1/8") {
		t.Errorf("lo0 is %+v", lo)
	}

	again, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if again.Epoch < state.Epoch {
		t.Errorf("epoch went back from %d to %d", state.Epoch, again.Epoch)
	}

	if state.DefaultV4 == nil {
		t.Skip("this Mac has no physical IPv4 default route")
	}
	def := state.DefaultV4
	// The WireGuard tunnel (utun10) and OpenVPN Connect (utun11) of the machine
	// this was written on must never be taken for the way out.
	if def.Iface == "utun10" || def.Iface == "utun11" || !isPhysical(def.Iface) {
		t.Errorf("IPv4 default leaves through %s", def.Iface)
	}
	if def.Gateway.IsValid() {
		onLink := false
		for _, subnet := range state.Connected {
			onLink = onLink || subnet.Contains(def.Gateway)
		}
		if !onLink {
			t.Errorf("gateway %s is in none of the connected subnets %v", def.Gateway, state.Connected)
		}
	}
	if state.DefaultV6 != nil && !isPhysical(state.DefaultV6.Iface) {
		t.Errorf("IPv6 default leaves through %s", state.DefaultV6.Iface)
	}
}

func slicesContainsPrefix(prefixes []netip.Prefix, want string) bool {
	for _, p := range prefixes {
		if p == netip.MustParsePrefix(want) {
			return true
		}
	}
	return false
}

// recordingRunner runs the system tools for real and remembers what it ran.
type recordingRunner struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingRunner) run(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	r.mu.Lock()
	r.calls = append(r.calls, strings.Join(append([]string{name}, args...), " "))
	r.mu.Unlock()
	return runCommand(ctx, name, args, stdin)
}

// kernelType is the type line of ifconfig -v, the kernel's own description of
// an interface ("Wi-Fi", "USB Ethernet", "IP over Thunderbolt"); empty when
// there is none or the interface is gone.
func kernelType(device string) string {
	out, err := exec.Command("/sbin/ifconfig", "-v", device).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if typ, ok := strings.CutPrefix(strings.TrimSpace(line), "type: "); ok {
			return typ
		}
	}
	return ""
}

// plausibleKind is what a device should be called given its hardware port and
// what the kernel says it is, a source independent of the port names. A phone
// tethered over USB is a USB Ethernet link to the kernel and not to us.
func plausibleKind(port, typ string) osnet.LinkKind {
	switch {
	case typ == "Wi-Fi":
		return osnet.LinkWiFi
	case strings.Contains(port, "iPhone"), strings.Contains(port, "iPad"):
		return osnet.LinkOther
	case strings.Contains(typ, "Ethernet"):
		return osnet.LinkEthernet
	}
	return osnet.LinkOther
}

func liveHardwarePorts(t *testing.T) map[string]string {
	t.Helper()
	out, err := runCommand(context.Background(), networksetupPath, []string{"-listallhardwareports"}, "")
	if err != nil {
		t.Skipf("networksetup: %v: %s", err, out)
	}
	ports := parseHardwarePorts(string(out))
	if len(ports) == 0 {
		t.Fatalf("no hardware port in the output of networksetup:\n%s", out)
	}
	return ports
}

// Every hardware port of this Mac, classified from networksetup's names, agrees
// with what the kernel says each interface is.
func TestLiveLinkKindsAgreeWithTheKernel(t *testing.T) {
	kinds := newLinkKinds(runCommand, discardLog())
	checked := 0
	for device, port := range liveHardwarePorts(t) {
		typ := kernelType(device)
		if typ == "" {
			continue // listed, but there is no such interface now
		}
		checked++
		got, want := kinds.kind(device), plausibleKind(port, typ)
		t.Logf("%s: %q, the kernel says %q: %s", device, port, typ, kindName(got))
		if got != want {
			t.Errorf("%s (%q, the kernel says %q) is %s, want %s", device, port, typ, kindName(got), kindName(want))
		}
	}
	if checked == 0 {
		t.Skip("no hardware port of this Mac has an interface")
	}
}

// Snapshot of this Mac gives each physical default the kind of its interface,
// and asks networksetup for it and nothing else.
func TestLiveSnapshotGivesTheDefaultsTheirKind(t *testing.T) {
	var runner recordingRunner
	state, err := NewNetMonitor(NetMonitorOptions{Logger: discardLog(), Run: runner.run}).Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if state.DefaultV4 == nil && state.DefaultV6 == nil {
		t.Skip("this Mac has no physical default route")
	}
	ports := liveHardwarePorts(t)
	for family, def := range map[string]*osnet.Nexthop{"IPv4": state.DefaultV4, "IPv6": state.DefaultV6} {
		if def == nil {
			continue
		}
		want := osnet.LinkOther // not a hardware port
		if port, ok := ports[def.Iface]; ok {
			want = plausibleKind(port, kernelType(def.Iface))
		}
		t.Logf("%s default leaves through %s: %s", family, def.Iface, kindName(def.Kind))
		if def.Kind != want {
			t.Errorf("%s default through %s has kind %s, want %s", family, def.Iface, kindName(def.Kind), kindName(want))
		}
	}
	if want := []string{networksetupPath + " -listallhardwareports"}; !slices.Equal(runner.calls, want) {
		t.Errorf("Snapshot ran %q, want %q", runner.calls, want)
	}
}

// Another process's route lookup is an RTM_GET that every route socket sees: a
// real kernel message through the real socket and parser.
func TestLiveRouteSocketDeliversKernelMessages(t *testing.T) {
	listener, err := openRouteSocket()
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := listener.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := lookup(t, routeRequest{Dst: netip.MustParsePrefix("127.0.0.1/32"), Flags: rtfHost}); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 1<<16)
	for {
		n, err := listener.Read(buf)
		if err != nil {
			t.Fatalf("no RTM_GET reached the listening socket: %v", err)
		}
		msgs, err := parseMessages(buf[:n])
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range msgs {
			if m.Type == rtmGet {
				return
			}
		}
	}
}

func TestLiveWatchRouteSocketStopsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- watchRouteSocket(ctx, discardLog(), func() {}) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Errorf("watchRouteSocket = %v after cancel", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watchRouteSocket did not stop")
	}
}

func TestLiveEventsEndWithTheirContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	events := NewNetMonitor(NetMonitorOptions{Logger: discardLog()}).Events(ctx)
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case _, open := <-events:
		if open {
			t.Error("a change arrived right after start")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the channel stays open")
	}
}

func TestLiveSleepTime(t *testing.T) {
	got, err := kernSleepTime()
	if err != nil {
		t.Fatal(err)
	}
	if got.After(time.Now()) {
		t.Errorf("the machine last slept in the future: %v", got)
	}
}

func TestLiveRouteTableRefusesBadRoutesBeforeWriting(t *testing.T) {
	table := NewRouteTable()
	for name, r := range map[string]osnet.Route{
		"no destination":    {},
		"unknown interface": {Dst: netip.MustParsePrefix("198.51.100.0/24"), Iface: "nonexistent9"},
		"no way out":        {Dst: netip.MustParsePrefix("198.51.100.0/24")},
	} {
		if err := table.Add(r); err == nil {
			t.Errorf("Add accepted %s", name)
		}
		if err := table.Delete(r); err == nil {
			t.Errorf("Delete accepted %s", name)
		}
	}
}

func TestLiveOwned(t *testing.T) {
	keys, err := NewDNS(DNSOptions{}).Owned()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if _, ok := ownerOfKey(k); !ok {
			t.Errorf("Owned returned %q", k)
		}
	}
	t.Logf("%d keys carry the marker", len(keys))
}

// The scutil dictionary commands run in memory. The script of a key, without
// its "set", is given to the real scutil to see that it parses the way the
// store would get it.
func TestLiveScutilReadsTheScript(t *testing.T) {
	keys, err := planDNSKeys("office", []osnet.DNSEntry{{
		Servers:      []netip.Addr{netip.MustParseAddr("10.6.0.1"), netip.MustParseAddr("fd00::1")},
		MatchDomains: []string{".", "Corp.Example.com."},
		Order:        5000,
	}})
	if err != nil {
		t.Fatal(err)
	}
	var local []string
	for _, line := range strings.Split(applyScript("office", keys, nil), "\n") {
		// Only the commands that stay inside scutil: nothing may reach the store.
		if line == "d.init" || strings.HasPrefix(line, "d.add ") {
			local = append(local, line)
		}
	}
	if len(local) != 6 {
		t.Fatalf("%d local commands in the script: %q", len(local), local)
	}
	out, err := runCommand(context.Background(), scutilPath, nil, strings.Join(local, "\n")+"\nd.show\nquit\n")
	if err != nil {
		t.Fatalf("scutil: %v: %s", err, out)
	}
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		got = append(got, strings.TrimRight(line, " "))
	}
	want := `<dictionary> {
  PlaitwayOwner : office
  ServerAddresses : <array> {
    0 : 10.6.0.1
    1 : fd00::1
  }
  SupplementalMatchDomains : <array> {
    0 :
    1 : corp.example.com
  }
  SupplementalMatchDomainsNoSearch : 1
  SupplementalMatchOrders : <array> {
    0 : 5000
    1 : 5000
  }
}`
	if strings.Join(got, "\n") != want {
		t.Errorf("scutil shows\n%s\nwant\n%s", strings.Join(got, "\n"), want)
	}
}
