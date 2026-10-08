//go:build rootintegration

package windows

// These tests change the routing table and the DNS Client's policy of the
// machine they run on, so they refuse to run in a process that is not elevated.
// From an elevated shell:
//
//	go test -tags rootintegration -count=1 -run TestRoot -v ./internal/osnet/windows
//
// What they touch is scratch: routes in the documentation ranges 192.0.2.0/24,
// 198.51.100.0/24 and 2001:db8::/32 on the loopback pseudo-interface, and
// NRPT rules for names under .invalid, served by a throw-away resolver on
// 127.0.0.1:53. All of it is removed again, also when a test fails.
//
// It is also removed when a test is killed (Ctrl+C, -timeout): a guard process
// (internal/winiface/rootguard, this test binary started again) is armed
// before the first change and removes the scratch routes and the DNS rules when
// this process ends without releasing it, or when its deadline passes. Its log
// is the folder of temporary files, plaitway-rootguard-*.log, and each guard prints the command that
// does by hand what it does. Every test refuses to run while the PlaitwayHelper
// service is running (or its state cannot be read), and the DNS tests on a PC
// that has Plaitway rules already: a daemon that connects a profile with DNS in
// the minutes a test takes would have its live rules removed by the clean-up, and
// its start-up sweep would remove the test's. The clean-up and the guard remove
// only the rules of the owners the tests write for.
//
// TestRootDNSCatchAll sends every name lookup of the PC to the throw-away
// resolver for a moment, so it runs only when PLAITWAY_ROOT_CATCHALL=1.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/winiface/rootguard"
)

const (
	loopbackAlias = "Loopback Pseudo-Interface 1"
	// answerAddress is what the throw-away resolver answers: documentation range.
	answerAddress = "192.0.2.53"
	// resolverTimeout bounds how long a rule may take to be picked up. The
	// time it did take is logged, because it is what the Reconciler can expect.
	pickupTimeout = 15 * time.Second
	// dnsQueryBypassCache is DNS_QUERY_BYPASS_CACHE.
	dnsQueryBypassCache = 0x8
)

func requireElevated(t *testing.T) {
	t.Helper()
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Fatal("refusing to run: this test changes the host; run it from an elevated shell")
	}
	requireHelperServiceIdle(t)
}

// helperServiceName is the service of the product; the tests never touch it, but
// what it does while they run would touch them.
const helperServiceName = "PlaitwayHelper"

// readHelperServiceState reads the state of the real service with the right to
// read it and no other. installed is false when there is no such service.
func readHelperServiceState() (state svc.State, installed bool, err error) {
	manager, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return 0, false, fmt.Errorf("connect to the Service Control Manager: %w", err)
	}
	defer windows.CloseServiceHandle(manager)
	name, err := windows.UTF16PtrFromString(helperServiceName)
	if err != nil {
		return 0, false, err
	}
	handle, err := windows.OpenService(manager, name, windows.SERVICE_QUERY_STATUS)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("open %s: %w", helperServiceName, err)
	}
	service := &mgr.Service{Name: helperServiceName, Handle: handle}
	defer service.Close()
	status, err := service.Query()
	if err != nil {
		return 0, true, fmt.Errorf("query %s: %w", helperServiceName, err)
	}
	return status.State, true, nil
}

// helperServiceBlocksTests says why the tests must not run, or "" when the
// service is not installed or stopped. A state that could not be read blocks them
// too: a running service is the case this guards against.
func helperServiceBlocksTests(state svc.State, installed bool, readErr error) string {
	switch {
	case readErr != nil:
		return fmt.Sprintf("cannot tell whether %s is running: %v", helperServiceName, readErr)
	case installed && state != svc.Stopped:
		return fmt.Sprintf("%s is not stopped (state %d): its rules and routes would meet the ones of this test", helperServiceName, state)
	}
	return ""
}

func requireHelperServiceIdle(t *testing.T) {
	t.Helper()
	state, installed, err := readHelperServiceState()
	if why := helperServiceBlocksTests(state, installed, err); why != "" {
		t.Fatalf("refusing to run: %s; stop it with 'plaitwayd stop' first", why)
	}
}

// The check reads the real service, which needs no elevation, and refuses in
// each state the service can be in.
func TestRootHelperServiceCheck(t *testing.T) {
	for _, tt := range []struct {
		name      string
		state     svc.State
		installed bool
		readErr   error
		blocks    bool
	}{
		{"not installed", 0, false, nil, false},
		{"stopped", svc.Stopped, true, nil, false},
		{"running", svc.Running, true, nil, true},
		{"starting", svc.StartPending, true, nil, true},
		{"stopping", svc.StopPending, true, nil, true},
		{"paused", svc.Paused, true, nil, true},
		{"state unreadable", 0, true, errors.New("access denied"), true},
		{"the manager cannot be reached", 0, false, errors.New("access denied"), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if why := helperServiceBlocksTests(tt.state, tt.installed, tt.readErr); (why != "") != tt.blocks {
				t.Errorf("helperServiceBlocksTests = %q, want blocking %v", why, tt.blocks)
			}
		})
	}
	state, installed, err := readHelperServiceState()
	if err != nil {
		t.Fatalf("reading %s without elevation: %v", helperServiceName, err)
	}
	t.Logf("%s on this PC: installed %v, state %d", helperServiceName, installed, state)
}

// presentRoutes returns the routes in the table to dst.
func presentRoutes(t *testing.T, table osnet.RouteTable, dst netip.Prefix) []osnet.Route {
	t.Helper()
	routes, err := table.Dump()
	if err != nil {
		t.Fatal(err)
	}
	return slices.DeleteFunc(routes, func(r osnet.Route) bool { return r.Dst != dst })
}

const (
	recoveryRoutes   = "routes"
	recoveryDNSRules = "dns-rules"
	// routeGuardWindow is how long a route test may take before its guard
	// removes the route. The DNS tests wait for the DNS Client, which takes
	// longer; the catch-all test has a window of its own because it is the one
	// that takes all name resolution of the PC away while it runs.
	routeGuardWindow    = 2 * time.Minute
	dnsGuardWindow      = 5 * time.Minute
	catchAllGuardWindow = 90 * time.Second
)

func init() {
	rootguard.Register(recoveryRoutes, deleteRoutesTo)
	rootguard.Register(recoveryDNSRules, func(owners string) error {
		return removeRulesOf(NewDNS(DNSOptions{}), strings.Split(owners, ownerSeparator))
	})
}

// The owners the tests write rules for. Clean-up and the guard remove the rules
// of these and of no other owner.
const (
	ruleOwner          = "plaitway-root-test"
	ruleOwnerNoRefresh = "plaitway-root-test-norefresh"
	ruleOwnerCatchAll  = "plaitway-root-test-catchall"
	ruleOwnerSweep     = "plaitway-root-test-sweep"

	ownerSeparator = ","
)

var ruleOwners = []string{ruleOwner, ruleOwnerNoRefresh, ruleOwnerCatchAll, ruleOwnerSweep}

// removeRulesOf removes everything applied for the owners and flushes the
// resolver cache.
func removeRulesOf(dns osnet.DNSConfigurator, owners []string) error {
	var failures []error
	for _, owner := range owners {
		if err := dns.Remove(owner); err != nil {
			failures = append(failures, err)
		}
	}
	if err := dns.Flush(); err != nil {
		failures = append(failures, fmt.Errorf("flush: %w", err))
	}
	return errors.Join(failures...)
}

// TestRootGuardHelper is the entry of the guards that these tests arm: the
// test binary started again (see internal/winiface/rootguard).
func TestRootGuardHelper(t *testing.T) { rootguard.RunHelper(t) }

// deleteRoutesTo is the recovery of the route tests: it deletes every route to
// the destinations in list (separated by ';'), through whatever interface.
func deleteRoutesTo(list string) error {
	table := NewRouteTable()
	routes, err := table.Dump()
	if err != nil {
		return err
	}
	var failures []error
	for _, text := range strings.Split(list, ";") {
		dst, err := netip.ParsePrefix(text)
		if err != nil {
			return err
		}
		for _, r := range routes {
			if r.Dst != dst {
				continue
			}
			if err := table.Delete(r); err != nil && !errors.Is(err, osnet.ErrNotFound) {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}

// scratchRoute refuses a destination that already has a route, and removes
// the route when the test ends, whatever state it is in, and when the test
// process is killed.
func scratchRoute(t *testing.T, table osnet.RouteTable, route osnet.Route) {
	t.Helper()
	if found := presentRoutes(t, table, route.Dst); len(found) != 0 {
		t.Fatalf("%s is in the table before the test: %+v", route.Dst, found)
	}
	rootguard.Arm(t, rootguard.Plan{
		Action:   recoveryRoutes,
		Argument: route.Dst.String(),
		Within:   routeGuardWindow,
		Manual:   fmt.Sprintf("Remove-NetRoute -DestinationPrefix %s -Confirm:$false", route.Dst),
	})
	t.Cleanup(func() {
		for _, r := range presentRoutes(t, table, route.Dst) {
			if err := table.Delete(r); err != nil && !errors.Is(err, osnet.ErrNotFound) {
				t.Errorf("clean up %s: %v", route.Dst, err)
			}
		}
		if left := presentRoutes(t, table, route.Dst); len(left) != 0 {
			t.Errorf("%s is still in the table after the clean-up: %+v", route.Dst, left)
		}
	})
}

func loopbackIndex(t *testing.T) uint32 {
	t.Helper()
	adapters, err := readAdapters()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range adapters {
		if isLoopback(a) {
			return a.Index
		}
	}
	t.Fatal("no loopback pseudo-interface")
	return 0
}

// An on-link route on the loopback pseudo-interface goes through its whole
// life: added, shown by Dump as what it is, refused when added again, deleted
// from the table's own copy of it, refused when deleted again, gone.
func TestRootRouteLifecycleOnLoopback(t *testing.T) {
	requireElevated(t)
	table := NewRouteTable()
	route := osnet.Route{Dst: netip.MustParsePrefix("192.0.2.0/24"), Iface: loopbackAlias, Static: true}
	scratchRoute(t, table, route)

	if err := table.Add(route); err != nil {
		t.Fatalf("Add: %v", err)
	}
	found := presentRoutes(t, table, route.Dst)
	if len(found) != 1 {
		t.Fatalf("Dump shows %d routes to %s: %+v", len(found), route.Dst, found)
	}
	got := found[0]
	// The loopback is the scratch carrier of this test, and a static on-link route
	// on the loopback is, on Windows, a route that discards its traffic: that is
	// what a blackhole is here, so it reads back as one.
	if !got.Static || !got.Blackhole || got.Gateway.IsValid() || got.Iface != loopbackAlias || got.IfIndex != loopbackIndex(t) || got.Metric != 0 {
		t.Errorf("the route reads back as %+v", got)
	}
	if err := table.Add(route); !errors.Is(err, osnet.ErrExists) {
		t.Errorf("second Add = %v, want ErrExists", err)
	}
	if err := table.Delete(got); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if left := presentRoutes(t, table, route.Dst); len(left) != 0 {
		t.Fatalf("still in the table after Delete: %+v", left)
	}
	if err := table.Delete(got); !errors.Is(err, osnet.ErrNotFound) {
		t.Errorf("second Delete = %v, want ErrNotFound", err)
	}
}

// What Windows keys a route by decides what the Reconciler may assume: the
// metric is not part of it (documented, to be confirmed here), a delete does
// not need it, and a delete that names only the destination finds the route.
func TestRootRouteKeyIgnoresTheMetric(t *testing.T) {
	requireElevated(t)
	table := NewRouteTable()
	route := osnet.Route{Dst: netip.MustParsePrefix("192.0.2.128/25"), IfIndex: loopbackIndex(t), Metric: 7, Static: true}
	scratchRoute(t, table, route)

	if err := table.Add(route); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got := presentRoutes(t, table, route.Dst); len(got) != 1 || got[0].Metric != 7 || got[0].Iface != loopbackAlias {
		t.Fatalf("the route reads back as %+v", got)
	}
	other := route
	other.Metric = 9
	if err := table.Add(other); !errors.Is(err, osnet.ErrExists) {
		t.Errorf("Add with another metric = %v, want ErrExists (the metric is not part of the key)", err)
	}
	if err := table.Delete(osnet.Route{Dst: route.Dst}); err != nil { // no interface, no metric
		t.Fatalf("Delete by destination only: %v", err)
	}
	if err := table.Delete(osnet.Route{Dst: route.Dst}); !errors.Is(err, osnet.ErrNotFound) {
		t.Errorf("second Delete = %v, want ErrNotFound", err)
	}
	// Deleting with the wrong metric must still find the route.
	if err := table.Add(route); err != nil {
		t.Fatal(err)
	}
	wrongMetric := route
	wrongMetric.Metric = 99
	if err := table.Delete(wrongMetric); err != nil {
		t.Errorf("Delete with another metric = %v: the metric is part of the key after all", err)
	}
}

func TestRootBlackholeRoutes(t *testing.T) {
	requireElevated(t)
	table := NewRouteTable()
	for _, prefix := range []string{"198.51.100.64/28", "198.51.100.77/32", "2001:db8:ffff:1::/64", "2001:db8:ffff::77/128"} {
		t.Run(prefix, func(t *testing.T) {
			route := osnet.Route{Dst: netip.MustParsePrefix(prefix), Blackhole: true, Static: true}
			scratchRoute(t, table, route)
			if err := table.Add(route); err != nil {
				t.Fatalf("Add: %v", err)
			}
			found := presentRoutes(t, table, route.Dst)
			if len(found) != 1 || !found[0].Blackhole || !found[0].Static || found[0].Iface != loopbackAlias || found[0].Gateway.IsValid() {
				t.Fatalf("the blackhole reads back as %+v", found)
			}
			if err := table.Add(route); !errors.Is(err, osnet.ErrExists) {
				t.Errorf("second Add = %v, want ErrExists", err)
			}
			if err := table.Delete(found[0]); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if err := table.Delete(found[0]); !errors.Is(err, osnet.ErrNotFound) {
				t.Errorf("second Delete = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestRootRouteErrors(t *testing.T) {
	requireElevated(t)
	table := NewRouteTable()
	unreachableGateway := netip.MustParseAddr("203.0.113.99") // on no subnet of this PC
	// A gateway on no connected subnet with the interface named is not a case here:
	// the loopback pseudo-interface takes any next hop, so it cannot show what a
	// real adapter does, and no scratch route may go on a real adapter.
	tests := []struct {
		name  string
		route osnet.Route
		want  error
	}{
		{"a gateway on no connected subnet, interface not named", osnet.Route{Dst: netip.MustParsePrefix("198.51.100.0/25"), Gateway: unreachableGateway}, osnet.ErrUnreachable},

		{"an interface that does not exist", osnet.Route{Dst: netip.MustParsePrefix("203.0.113.0/25"), Iface: "Plaitway-test-no-such-adapter"}, osnet.ErrUnreachable},
		{"an interface index that does not exist", osnet.Route{Dst: netip.MustParsePrefix("203.0.113.128/25"), IfIndex: 0x7fffff00}, osnet.ErrUnreachable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scratchRoute(t, table, tt.route)
			if err := table.Add(tt.route); !errors.Is(err, tt.want) {
				t.Errorf("Add = %v, want %v", err, tt.want)
			}
		})
	}
	if err := table.Delete(osnet.Route{Dst: netip.MustParsePrefix("203.0.113.0/25"), Iface: "Plaitway-test-no-such-adapter"}); !errors.Is(err, osnet.ErrNotFound) {
		t.Errorf("Delete through an interface that does not exist = %v, want ErrNotFound", err)
	}
	if err := table.Delete(osnet.Route{Dst: netip.MustParsePrefix("203.0.113.0/25")}); !errors.Is(err, osnet.ErrNotFound) {
		t.Errorf("Delete of a destination with no route = %v, want ErrNotFound", err)
	}
}

// The notifications the monitor registers for really fire: a route added and
// removed behind the monitor's back is reported as a Change on the real clock,
// and a route on the loopback does not move the epoch.
func TestRootNotificationsFireOnRouteChanges(t *testing.T) {
	requireElevated(t)
	table := NewRouteTable()
	route := osnet.Route{Dst: netip.MustParsePrefix("192.0.2.0/25"), Iface: loopbackAlias, Static: true}
	scratchRoute(t, table, route)

	before := activeNotifications.Load()
	monitor := NewNetMonitor(NetMonitorOptions{Logger: discardLog()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := monitor.Events(ctx)
	waitFor(t, "the registrations", func() bool { return activeNotifications.Load() == before+4 })
	first, err := monitor.Snapshot()
	if err != nil {
		t.Fatal(err)
	}

	expectChange := func(what string) {
		t.Helper()
		select {
		case got := <-events:
			if got.Reason != osnet.ChangeRoute {
				t.Errorf("%s: reason %q, want route", what, got.Reason)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: no Change within 10 s", what)
		}
	}
	if err := table.Add(route); err != nil {
		t.Fatal(err)
	}
	expectChange("after adding a route")
	if err := table.Delete(route); err != nil {
		t.Fatal(err)
	}
	expectChange("after deleting it")

	after, err := monitor.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if after.Epoch != first.Epoch {
		t.Errorf("a route on the loopback moved the epoch from %d to %d", first.Epoch, after.Epoch)
	}
}

// udpResolver answers A queries for the names it is given with answerAddress,
// AAAA queries with an empty answer, and every other name with NXDOMAIN. With
// wildcard set it answers every A query.
type udpResolver struct {
	conn     *net.UDPConn
	wildcard bool
	names    map[string]bool
	mu       sync.Mutex
	seen     []string
	done     chan struct{}
}

func startResolver(t *testing.T, wildcard bool, names ...string) *udpResolver {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 53})
	if err != nil {
		t.Fatalf("cannot listen on 127.0.0.1:53 (is another resolver running?): %v", err)
	}
	r := &udpResolver{conn: conn, wildcard: wildcard, names: map[string]bool{}, done: make(chan struct{})}
	for _, n := range names {
		r.names[strings.ToLower(n)+"."] = true
	}
	go r.serve()
	t.Cleanup(func() {
		conn.Close()
		<-r.done
	})
	return r
}

func (r *udpResolver) serve() {
	defer close(r.done)
	buf := make([]byte, 1500)
	for {
		n, from, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		var query dnsmessage.Message
		if query.Unpack(buf[:n]) != nil || len(query.Questions) != 1 {
			continue
		}
		reply, err := r.answer(query)
		if err == nil {
			r.conn.WriteToUDP(reply, from)
		}
	}
}

func (r *udpResolver) answer(query dnsmessage.Message) ([]byte, error) {
	q := query.Questions[0]
	name := strings.ToLower(q.Name.String())
	r.mu.Lock()
	r.seen = append(r.seen, name)
	r.mu.Unlock()
	known := r.wildcard || r.names[name]
	reply := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: query.ID, Response: true, Authoritative: true, RecursionAvailable: true, RCode: dnsmessage.RCodeSuccess},
		Questions: query.Questions,
	}
	switch {
	case !known:
		reply.RCode = dnsmessage.RCodeNameError
	case q.Type == dnsmessage.TypeA:
		reply.Answers = []dnsmessage.Resource{{
			Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 1},
			Body:   &dnsmessage.AResource{A: netip.MustParseAddr(answerAddress).As4()},
		}}
	}
	return reply.Pack()
}

func (r *udpResolver) queried(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Contains(r.seen, strings.ToLower(name)+".")
}

// lookupSystem resolves name the way applications do: through the DNS Client
// service, with getaddrinfo.
func lookupSystem(name string) ([]netip.Addr, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return (&net.Resolver{PreferGo: false}).LookupNetIP(ctx, "ip4", name)
}

// lookupDnsQuery resolves name with DnsQuery_W, which is what Resolve-DnsName
// uses, past the cache.
func lookupDnsQuery(name string) ([]netip.Addr, error) {
	var records *windows.DNSRecord
	if err := windows.DnsQuery(name, windows.DNS_TYPE_A, dnsQueryBypassCache, nil, &records, nil); err != nil {
		return nil, err
	}
	defer windows.DnsRecordListFree(records, 1)
	var addrs []netip.Addr
	for r := records; r != nil; r = r.Next {
		if r.Type == windows.DNS_TYPE_A && r.Dw&3 == windows.DnsSectionAnswer {
			addrs = append(addrs, netip.AddrFrom4([4]byte(r.Data[:4])))
		}
	}
	return addrs, nil
}

// resolvesTo polls both resolvers until both agree that name resolves (want
// true) or does not resolve to answerAddress (want false), and returns how
// long it took.
func resolvesTo(t *testing.T, name string, want bool) time.Duration {
	t.Helper()
	answer := netip.MustParseAddr(answerAddress)
	start := time.Now()
	var last string
	for time.Since(start) < pickupTimeout {
		sys, sysErr := lookupSystem(name)
		dq, dqErr := lookupDnsQuery(name)
		last = fmt.Sprintf("getaddrinfo: %v, %v; DnsQuery: %v, %v", sys, sysErr, dq, dqErr)
		if slices.Contains(sys, answer) == want && slices.Contains(dq, answer) == want {
			return time.Since(start)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("%s does not %s %s within %v; last: %s", name, map[bool]string{true: "resolve to", false: "stop resolving to"}[want], answerAddress, pickupTimeout, last)
	return 0
}

// sweepRules refuses to run on a PC that has Plaitway rules, arms the guard
// that removes the rules of the test owners if this process is killed, and
// removes them when the test ends. The window is how long the test may run
// before the guard acts.
func sweepRules(t *testing.T, dns osnet.DNSConfigurator, window time.Duration) {
	t.Helper()
	requireHelperServiceIdle(t)
	if owned, err := dns.Owned(); err != nil || len(owned) != 0 {
		t.Fatalf("Plaitway rules are on this machine already: %v, %v", owned, err)
	}
	rootguard.Arm(t, rootguard.Plan{
		Action:   recoveryDNSRules,
		Argument: strings.Join(ruleOwners, ownerSeparator),
		Within:   window,
		Manual:   ManualSweepCommand(),
	})
	t.Cleanup(func() {
		if err := removeRulesOf(dns, ruleOwners); err != nil {
			t.Errorf("clean up: %v", err)
		}
	})
}

// A rule for plaitway-test.invalid sends that name, and only that name, to the
// throw-away resolver: it resolves through the system resolver, stops when the
// rule is removed, and a rule left behind by a crash is found again.
func TestRootDNSRuleResolvesThroughTheSystemResolver(t *testing.T) {
	requireElevated(t)
	server := startResolver(t, false, "plaitway-test.invalid", "host.plaitway-test.invalid")
	dns := NewDNS(DNSOptions{Logger: discardLog()})
	sweepRules(t, dns, dnsGuardWindow)
	const owner = ruleOwner
	const name = "plaitway-test.invalid"

	if err := dns.Apply(owner, []osnet.DNSEntry{{
		Servers:      []netip.Addr{netip.MustParseAddr("127.0.0.1")},
		MatchDomains: []string{name},
	}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := dns.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	t.Logf("the rule was picked up after %v", resolvesTo(t, name, true))
	// The domain means itself and everything below it.
	resolvesTo(t, "host."+name, true)
	if !server.queried(name) {
		t.Error("the throw-away resolver never saw the query: the answer came from elsewhere")
	}
	// A name the rule does not cover is not sent there.
	_, _ = lookupSystem("plaitway-other.invalid")
	if server.queried("plaitway-other.invalid") {
		t.Error("a name outside the rule reached the throw-away resolver")
	}

	// Replace the rule by one for another name: the old one must stop at once.
	if err := dns.Apply(owner, []osnet.DNSEntry{{
		Servers:      []netip.Addr{netip.MustParseAddr("127.0.0.1")},
		MatchDomains: []string{"plaitway-test2.invalid"},
	}}); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if err := dns.Flush(); err != nil {
		t.Fatal(err)
	}
	resolvesTo(t, name, false)

	// Crash recovery: forget the configurator, find the rule through Owned, remove it.
	if err := dns.Apply(owner, []osnet.DNSEntry{{
		Servers:      []netip.Addr{netip.MustParseAddr("127.0.0.1")},
		MatchDomains: []string{name},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := dns.Flush(); err != nil {
		t.Fatal(err)
	}
	resolvesTo(t, name, true)
	restarted := NewDNS(DNSOptions{Logger: discardLog()})
	keys, err := restarted.Owned()
	if err != nil || len(keys) != 1 {
		t.Fatalf("after the restart Owned = %v, %v", keys, err)
	}
	if err := restarted.Remove(keys[0]); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := restarted.Flush(); err != nil {
		t.Fatal(err)
	}
	resolvesTo(t, name, false)
	if left, _ := restarted.Owned(); len(left) != 0 {
		t.Errorf("still listed after Remove: %v", left)
	}
}

// Whether writing the registry is enough, or the DNS Client must be told to
// reread its policy, decides whether RefreshPolicyEx stays in Apply. This
// reports what this Windows does; it does not fail on either answer.
func TestRootDNSRegistryWriteWithoutPolicyRefresh(t *testing.T) {
	requireElevated(t)
	server := startResolver(t, false, "plaitway-norefresh.invalid")
	dns := NewDNS(DNSOptions{Logger: discardLog()}).(*dnsConfigurator)
	sweepRules(t, dns, dnsGuardWindow)
	dns.sys.refresh = func() error { return nil }
	const name = "plaitway-norefresh.invalid"

	if err := dns.Apply(ruleOwnerNoRefresh, []osnet.DNSEntry{{
		Servers:      []netip.Addr{netip.MustParseAddr("127.0.0.1")},
		MatchDomains: []string{name},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := dns.Flush(); err != nil {
		t.Fatal(err)
	}
	answer := netip.MustParseAddr(answerAddress)
	start := time.Now()
	for time.Since(start) < 10*time.Second {
		if addrs, _ := lookupDnsQuery(name); slices.Contains(addrs, answer) {
			t.Logf("DIAGNOSTIC: a registry write alone was picked up after %v; the policy refresh is not needed on this Windows (resolver saw it: %v)", time.Since(start), server.queried(name))
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("DIAGNOSTIC: a registry write alone was NOT picked up within 10 s; Apply must tell the DNS Client (RefreshPolicyEx)")
}

// "." sends every name to the rule's servers, and a more specific rule still
// wins. It redirects the whole PC's lookups for a moment, hence the guard.
func TestRootDNSCatchAll(t *testing.T) {
	requireElevated(t)
	if os.Getenv("PLAITWAY_ROOT_CATCHALL") != "1" {
		t.Skip("set PLAITWAY_ROOT_CATCHALL=1: this test sends every lookup of the PC to a throw-away resolver for a moment")
	}
	server := startResolver(t, true)
	dns := NewDNS(DNSOptions{Logger: discardLog()})
	sweepRules(t, dns, catchAllGuardWindow)
	err := dns.Apply(ruleOwnerCatchAll, []osnet.DNSEntry{{
		Servers:      []netip.Addr{netip.MustParseAddr("127.0.0.1")},
		MatchDomains: []string{"."},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := dns.Flush(); err != nil {
		t.Fatal(err)
	}
	// Remove the rule again as soon as the answer is seen, to keep the window short.
	resolvesTo(t, "plaitway-catchall-a.invalid", true)
	resolvesTo(t, "plaitway-catchall-b.invalid", true)
	if !server.queried("plaitway-catchall-b.invalid") {
		t.Error("the throw-away resolver never saw the second name")
	}
	if err := dns.Remove(ruleOwnerCatchAll); err != nil {
		t.Fatal(err)
	}
	if err := dns.Flush(); err != nil {
		t.Fatal(err)
	}
	resolvesTo(t, "plaitway-catchall-a.invalid", false)
}

// What uninstall does after a daemon that died with a rule in place: a new
// configurator, which knows nothing, finds the rule by its marker and removes
// it, through SweepDNS and the real registry.
func TestRootSweepDNSRemovesTheRulesOfADeadDaemon(t *testing.T) {
	requireElevated(t)
	dns := NewDNS(DNSOptions{Logger: discardLog()})
	sweepRules(t, dns, dnsGuardWindow)

	err := dns.Apply(ruleOwnerSweep, []osnet.DNSEntry{
		{Servers: []netip.Addr{netip.MustParseAddr("127.0.0.1")}, MatchDomains: []string{"plaitway-sweep.invalid"}},
		{Servers: []netip.Addr{netip.MustParseAddr("127.0.0.1")}, MatchDomains: []string{"plaitway-sweep2.invalid"}, Order: 1},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if owned, err := dns.Owned(); err != nil || len(owned) != 2 {
		t.Fatalf("Owned after Apply = %v, %v", owned, err)
	}

	result, err := SweepDNS(discardLog())
	if err != nil || len(result.Removed) != 2 || len(result.Remaining) != 0 {
		t.Fatalf("SweepDNS = %+v, %v; want both rules removed", result, err)
	}
	if owned, err := dns.Owned(); err != nil || len(owned) != 0 {
		t.Errorf("Owned after the sweep = %v, %v", owned, err)
	}
}
