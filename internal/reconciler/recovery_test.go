package reconciler

import (
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/osnet/fake"
)

// crash simulates the daemon dying without cleaning up: its tunnel interfaces
// go with it, whatever it added through a gateway stays in the table. Its open
// files go too, which a restart on Windows depends on to replace the journal.
func (e *env) crash(ifaces ...string) {
	e.r.journal.close()
	for _, name := range ifaces {
		e.host.DestroyInterface(name)
	}
}

// Everything the last run journaled is removed at start-up, resolver entries
// are swept by their marker, and routes that are not in the journal are left
// alone and reported.
func TestCrashRecoveryRemovesWhatTheLastRunLeft(t *testing.T) {
	e := newEnv(t)
	e.bothTunnels()
	e.announce(wgIntent())
	e.announce(asusIntent())
	healthy := osnet.Route{Dst: pfx("8.8.4.4/32"), Gateway: ip("192.168.51.1"), Iface: "en0", Static: true}
	broken := osnet.Route{Dst: pfx("9.9.9.9/32"), Gateway: ip("10.99.0.1"), Iface: "en0", Static: true}
	e.host.Routes.Inject(healthy)
	e.host.Routes.Inject(broken)

	e.crash("utun10", "utun11")
	e.checkTable(
		"8.8.4.4/32 via 192.168.51.1 dev en0",
		"9.9.9.9/32 via 10.99.0.1 dev en0",
		"198.51.100.7/32 via 192.168.51.1 dev en0",
		"203.0.113.10/32 via 192.168.51.1 dev en0",
	)
	if owned, _ := e.host.DNS.Owned(); len(owned) != 2 {
		t.Fatalf("setup: resolver entries %v", owned)
	}

	r2 := e.newReconciler()

	e.checkTable("8.8.4.4/32 via 192.168.51.1 dev en0", "9.9.9.9/32 via 10.99.0.1 dev en0")
	if owned, _ := e.host.DNS.Owned(); len(owned) != 0 {
		t.Errorf("resolver entries left: %v", owned)
	}
	if left := e.unresolved(); len(left) != 0 {
		t.Errorf("the journal still lists %+v", left)
	}
	rep := r2.Report()
	if len(rep.Stale) != 1 || rep.Stale[0].Key != "9.9.9.9/32" || rep.Stale[0].Owned {
		t.Errorf("only the broken foreign route is reported, as not ours: %+v", rep.Stale)
	}
	removed := 0
	for _, rec := range rep.Journal {
		if rec.State == stateRemoved {
			removed++
		}
	}
	if removed != 4 {
		t.Errorf("recovery should journal 2 routes and 2 resolver entries as removed, got %d: %+v", removed, rep.Journal)
	}
	e.r = r2
	if err := r2.RemoveStale("9.9.9.9/32"); err != nil {
		t.Fatal(err)
	}
	e.checkTable("8.8.4.4/32 via 192.168.51.1 dev en0")
}

// A route that somebody else changed since is theirs now: not even a leftover
// of ours is deleted when its fingerprint no longer matches.
func TestRecoveryLeavesRoutesChangedByOthers(t *testing.T) {
	e := newEnv(t)
	e.bothTunnels()
	e.announce(wgIntent())
	e.crash("utun10", "utun11")
	// A different router for one endpoint; same router but no longer static for the other.
	e.host.Routes.Inject(osnet.Route{Dst: pfx("203.0.113.10/32"), Gateway: ip("192.168.51.77"), Iface: "en0", Static: true})

	e.newReconciler()

	e.checkTable("203.0.113.10/32 via 192.168.51.77 dev en0")
	var notes []string
	for _, rec := range e.journalFile() {
		if rec.State == stateRemoved && rec.Kind == kindRoute && rec.Note != "" {
			notes = append(notes, rec.Note)
		}
	}
	if !slices.Contains(notes, "changed by another program, left in place") {
		t.Errorf("journal notes: %v", notes)
	}
}

func TestRecoveryComparesTheFlagsInTheFingerprint(t *testing.T) {
	e := newEnv(t)
	e.bothTunnels()
	e.announce(wgIntent())
	e.crash("utun10", "utun11")
	// Same router and interface, but another program owns it now: not static.
	e.host.Routes.Inject(osnet.Route{Dst: pfx("203.0.113.10/32"), Gateway: ip("192.168.51.1"), Iface: "en0"})

	e.newReconciler()

	if _, ok := e.host.Routes.Get(pfx("203.0.113.10/32")); !ok {
		t.Error("a route with another fingerprint was deleted")
	}
}

// writeJournal leaves a journal file behind, as an earlier run would have.
func writeJournal(t *testing.T, path string, recs ...record) {
	t.Helper()
	j, err := openJournal(path, func() time.Time { return t0 }, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range recs {
		if err := j.append(rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.close(); err != nil {
		t.Fatal(err)
	}
}

// A crash between the journal write and the add, or between the add and the
// confirmation, leaves a pending record with only the asked-for gateway.
func TestRecoveryFromAPendingRecord(t *testing.T) {
	e := newEnv(t)
	// Crashed right after the add: the route is there, the record is pending.
	e.host.Routes.Inject(osnet.Route{Dst: pfx("203.0.113.10/32"), Gateway: ip("192.168.51.1"), Iface: "en0", Static: true})
	e.crash() // the journal belongs to a run that is gone
	writeJournal(t, e.journal,
		record{Owner: "wg", Kind: kindRoute, Key: "203.0.113.10/32", State: statePending, Gateway: "192.168.51.1", Iface: "en0"},
		// Crashed right after the journal write: the route was never added.
		record{Owner: "wg", Kind: kindRoute, Key: "198.51.100.7/32", State: statePending, Gateway: "192.168.51.1", Iface: "en0"},
	)

	e.newReconciler()

	e.checkTable()
	if left := e.unresolved(); len(left) != 0 {
		t.Errorf("unresolved: %+v", left)
	}
}

func TestRecoverySweepsResolverEntriesWithoutAJournal(t *testing.T) {
	e := newEnv(t)
	e.host.DNS.Leave("ghost", osnet.DNSEntry{Servers: ips("10.0.0.53"), MatchDomains: []string{"corp.lan"}})

	e.crash() // the daemon that left the entry is gone
	r := e.newReconciler()

	if owned, _ := e.host.DNS.Owned(); len(owned) != 0 {
		t.Errorf("marked entries survived: %v", owned)
	}
	if got := r.Report().ResolverEntries; len(got) != 0 {
		t.Errorf("report: %v", got)
	}
}

// When the delete fails, the route stays ours and the next pass tries again.
func TestRecoveryKeepsARouteItCouldNotDelete(t *testing.T) {
	e := newEnv(t)
	e.bothTunnels()
	e.announce(wgIntent())
	e.crash("utun10", "utun11")
	e.host.Routes.InjectFault(fake.Fault{Op: fake.OpDelete, Dst: pfx("203.0.113.10/32"), Err: errBoom, Times: 2})

	e.r = e.newReconciler() // the recovery's delete and the first pass's both fail

	e.checkTable("203.0.113.10/32 via 192.168.51.1 dev en0")
	if left := e.unresolved(); len(left) != 1 || left[0].Key != "203.0.113.10/32" {
		t.Fatalf("the journal must keep it: %+v", left)
	}
	e.change(osnet.ChangeHeartbeat)
	e.checkTable()
	if left := e.unresolved(); len(left) != 0 {
		t.Errorf("unresolved: %+v", left)
	}
}

// A line cut short by the crash is skipped; what comes before it is still
// recovered, and the journal can be appended to afterwards.
func TestRecoveryToleratesATornJournal(t *testing.T) {
	e := newEnv(t)
	e.host.Routes.Inject(osnet.Route{Dst: pfx("203.0.113.10/32"), Gateway: ip("192.168.51.1"), Iface: "en0", Static: true})
	e.crash()
	writeJournal(t, e.journal,
		record{Owner: "wg", Kind: kindRoute, Key: "203.0.113.10/32", State: statePending, Gateway: "192.168.51.1", Iface: "en0"})
	f, err := os.OpenFile(e.journal, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"seq":9,"owner":"wg","kind":"route","key":"198.51`)
	f.Close()

	r := e.newReconciler()
	e.checkTable()

	e.r = r
	e.bothTunnels()
	e.announce(wgIntent())
	// journalFile fails on a line that does not parse: the torn one is gone, and
	// what the new run appended did not glue itself to it.
	want := []string{"pending 192.168.51.1", "removed 192.168.51.1", "pending 192.168.51.1", "applied 192.168.51.1"}
	if got := e.journalFor(kindRoute, "203.0.113.10/32"); !slices.Equal(got, want) {
		t.Errorf("journal after the torn line: %v", got)
	}
}

func TestNewChecksItsArguments(t *testing.T) {
	host := fake.NewHost()
	base := Config{Routes: host.Routes, DNS: host.DNS, Net: host.Net, JournalPath: filepath.Join(t.TempDir(), "j")}
	for name, mutate := range map[string]func(*Config){
		"no routes":  func(c *Config) { c.Routes = nil },
		"no dns":     func(c *Config) { c.DNS = nil },
		"no monitor": func(c *Config) { c.Net = nil },
		"no journal": func(c *Config) { c.JournalPath = "" },
	} {
		cfg := base
		mutate(&cfg)
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	host.Net.FailSnapshot(errBoom)
	if _, err := New(base); err == nil {
		t.Error("a machine whose network cannot be read: accepted")
	}
	host.Net.FailSnapshot(nil)

	// With leftovers in the journal, not being able to read the table is fatal:
	// starting without cleaning up would hide them.
	writeJournal(t, base.JournalPath, record{Owner: "wg", Kind: kindRoute, Key: "203.0.113.10/32", State: statePending})
	host.Routes.InjectFault(fake.Fault{Op: fake.OpDump, Err: errBoom})
	if _, err := New(base); err == nil {
		t.Error("recovery without a readable routing table: accepted")
	}
}
