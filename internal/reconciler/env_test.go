package reconciler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/osnet/fake"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// syncBuffer collects the Reconciler's log; it is printed when a test fails.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// env is a fake machine with one physical interface, en0 on 192.168.51.0/24
// behind the router 192.168.51.1, and a Reconciler on top of it.
type env struct {
	t       *testing.T
	host    *fake.Host
	r       *Reconciler
	journal string
	logs    *syncBuffer
}

func newEnv(t *testing.T, opts ...func(*Config)) *env {
	t.Helper()
	host := fake.NewHost()
	host.AddPhysical("en0", pfx("192.168.51.185/24"), ip("192.168.51.1"))
	e := &env{t: t, host: host, journal: filepath.Join(t.TempDir(), "state", "journal"), logs: &syncBuffer{}}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("daemon log:\n%s", e.logs)
		}
	})
	e.r = e.newReconciler(opts...)
	return e
}

// newReconciler starts a Reconciler on the machine, as the daemon does after a
// restart. Its timers are slow unless a test asks for faster ones.
func (e *env) newReconciler(opts ...func(*Config)) *Reconciler {
	e.t.Helper()
	cfg := Config{
		Routes:      e.host.Routes,
		DNS:         e.host.DNS,
		Net:         e.host.Net,
		JournalPath: e.journal,
		Log:         slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now:         func() time.Time { return t0 },
		WakeDelay:   time.Hour,
		RetryDelay:  time.Hour,
		MaxPasses:   3,
	}
	for _, o := range opts {
		o(&cfg)
	}
	r, err := New(cfg)
	if err != nil {
		e.t.Fatalf("New: %v", err)
	}
	return r
}

func (e *env) addTunnel(name, addr string) {
	e.host.AddTunnel(name, pfx(addr))
	e.change(osnet.ChangeRoute)
}

func (e *env) announce(in tunnel.Intent) {
	e.t.Helper()
	if err := e.r.Announce(in); err != nil {
		e.t.Fatalf("Announce(%s): %v", in.Owner, err)
	}
}

// change hands the Reconciler a change the way Run does, and returns when it
// has been handled.
func (e *env) change(reason osnet.ChangeReason) {
	e.r.handle(osnet.Change{Reason: reason, At: t0}, true)
}

// run starts Run and returns once it has taken its first look at the network,
// which is when it is subscribed to events. The returned function ends it.
func (e *env) run() (stop func() error) {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.r.Run(ctx) }()
	var once sync.Once
	var runErr error
	stop = func() error {
		once.Do(func() {
			cancel()
			runErr = <-done
		})
		return runErr
	}
	e.t.Cleanup(func() { stop() })
	e.eventually("Run to start", func() bool { return e.r.Report().LastChange.Reason == osnet.ChangeManual })
	return stop
}

func (e *env) eventually(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func routeLine(rt osnet.Route) string {
	if rt.Gateway.IsValid() {
		return fmt.Sprintf("%s via %s dev %s", rt.Dst, rt.Gateway, rt.Iface)
	}
	return fmt.Sprintf("%s dev %s", rt.Dst, rt.Iface)
}

// table lists the static routes in the table except the system default, which
// is what Plaitway and other programs add.
func (e *env) table() []string {
	e.t.Helper()
	routes, err := e.host.Routes.Dump()
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, rt := range routes {
		if rt.Static && rt.Dst.Bits() != 0 && !rt.Scoped {
			out = append(out, routeLine(rt))
		}
	}
	return out
}

func (e *env) checkTable(want ...string) {
	e.t.Helper()
	checkLines(e.t, "routing table", e.table(), want)
}

// lookup is the route the kernel would use for addr.
func (e *env) lookup(addr string) osnet.Route {
	e.t.Helper()
	rt, ok := e.host.Routes.Lookup(ip(addr))
	if !ok {
		e.t.Fatalf("no route to %s", addr)
	}
	return rt
}

// opLines is the route table's operation log in words.
func (e *env) opLines() []string {
	var out []string
	for _, op := range e.host.Routes.Ops() {
		var line string
		switch op.Kind {
		case fake.OpAdd:
			line = "add " + op.Route.Dst.String()
			if op.Route.Gateway.IsValid() {
				line += " via " + op.Route.Gateway.String()
			}
		case fake.OpDelete:
			line = "delete " + op.Route.Dst.String()
		case fake.OpMark:
			line = "mark " + op.Label
		}
		if op.Err != nil {
			line += " failed"
		}
		out = append(out, line)
	}
	return out
}

func count(lines []string, line string) int {
	n := 0
	for _, l := range lines {
		if l == line {
			n++
		}
	}
	return n
}

func (e *env) routeReport(dst string, owner tunnel.OwnerID) tunnel.RouteReport {
	e.t.Helper()
	for _, rr := range e.r.Report().Routes {
		if rr.Prefix == pfx(dst) && rr.Owner == owner {
			return rr
		}
	}
	e.t.Fatalf("no report for %s of %s in %+v", dst, owner, e.r.Report().Routes)
	return tunnel.RouteReport{}
}

func (e *env) dnsReport(owner tunnel.OwnerID) tunnel.DNSReport {
	e.t.Helper()
	for _, dr := range e.r.Report().DNS {
		if dr.Owner == owner {
			return dr
		}
	}
	e.t.Fatalf("no DNS report for %s in %+v", owner, e.r.Report().DNS)
	return tunnel.DNSReport{}
}

// journalFile reads every record in the journal file.
func (e *env) journalFile() []record {
	e.t.Helper()
	f, err := os.Open(e.journal)
	if err != nil {
		e.t.Fatal(err)
	}
	defer f.Close()
	var out []record
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var rec record
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			e.t.Fatalf("journal line %q: %v", sc.Text(), err)
		}
		out = append(out, rec)
	}
	return out
}

// journalFor summarizes the records for one key as "state gateway" strings.
func (e *env) journalFor(kind, key string) []string {
	e.t.Helper()
	var out []string
	for _, rec := range e.journalFile() {
		if rec.Kind == kind && rec.Key == key {
			out = append(out, strings.TrimSpace(rec.State+" "+rec.Gateway))
		}
	}
	return out
}

// unresolved is what a restart would find in the journal still to clean up.
func (e *env) unresolved() []record {
	e.t.Helper()
	live := make(map[string]record)
	for _, rec := range e.journalFile() {
		if rec.State == stateRemoved {
			delete(live, liveKey(rec.Kind, rec.Key))
		} else {
			live[liveKey(rec.Kind, rec.Key)] = rec
		}
	}
	out := make([]record, 0, len(live))
	for _, rec := range live {
		out = append(out, rec)
	}
	slices.SortFunc(out, func(a, b record) int { return strings.Compare(a.Key, b.Key) })
	return out
}

// The profiles of the owner's real setup: a WireGuard full tunnel to a home
// router and the ASUS OpenVPN split tunnel to a remote site.
func wgIntent() tunnel.Intent {
	return nameserver(endpoints(up("wg", 1, "utun10", tunnel.RoleFull, "0.0.0.0/0", "::/0"), "203.0.113.10"), "192.168.50.1", ".")
}

func asusIntent() tunnel.Intent {
	return nameserver(endpoints(up("asus", 2, "utun11", tunnel.RoleSplit, "192.168.1.0/24"), "198.51.100.7"), "192.168.1.1", "asus.lan")
}

func (e *env) bothTunnels() {
	e.addTunnel("utun10", "10.6.0.2/24")
	e.addTunnel("utun11", "10.8.0.6/24")
}

var errBoom = errors.New("boom")
