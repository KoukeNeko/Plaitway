package reconciler

import (
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// slowDNS holds every Apply until the test lets it go, as a configd that does
// not answer would.
type slowDNS struct {
	osnet.DNSConfigurator
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (d *slowDNS) Apply(owner string, entries []osnet.DNSEntry) error {
	d.once.Do(func() { close(d.entered) })
	<-d.release
	return d.DNSConfigurator.Apply(owner, entries)
}

// within fails the test when f does not return in time.
func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s is stuck behind a slow adapter call", what)
	}
}

// The daemon asks for the Report on every status update of every engine, with
// its own lock held. While a call into the system is slow, the Reconciler may
// not hold anything that Report, Changed or SetRebind need.
func TestReportDoesNotWaitForASlowAdapter(t *testing.T) {
	eachKeying(t, testReportDoesNotWaitForASlowAdapter)
}

func testReportDoesNotWaitForASlowAdapter(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	e.bothTunnels()
	slow := &slowDNS{DNSConfigurator: e.host.DNS, entered: make(chan struct{}), release: make(chan struct{})}
	e.r.dns = slow
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(slow.release) }) }
	t.Cleanup(release)

	announced := make(chan error, 1)
	go func() { announced <- e.r.Announce(asusIntent()) }()
	<-slow.entered

	within(t, "Report", func() { e.r.Report() })
	within(t, "SetRebind", func() { e.r.SetRebind(func() {}) })
	within(t, "Changed", func() {
		select {
		case <-e.r.Changed():
		default:
		}
	})
	select {
	case err := <-announced:
		t.Fatalf("Announce returned while the resolver was still being written: %v", err)
	default:
	}

	release()
	if err := <-announced; err != nil {
		t.Fatal(err)
	}
	if dr := e.dnsReport("asus"); dr.State != tunnel.RouteInstalled {
		t.Errorf("report after the pass: %+v", dr)
	}
}

// Report hands out a snapshot that many callers share the source of: changing
// what one of them got must not show in what the next one gets.
func TestReportCannotBeChangedByItsCaller(t *testing.T) {
	eachKeying(t, testReportCannotBeChangedByItsCaller)
}

func testReportCannotBeChangedByItsCaller(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	e.bothTunnels()
	e.announce(wgIntent())
	e.announce(asusIntent())
	before := e.r.Report()
	if len(before.Routes) == 0 || len(before.DNS) == 0 || len(before.Net.Interfaces) == 0 || len(before.ResolverEntries) == 0 || len(before.Journal) == 0 {
		t.Fatalf("setup: %+v", before)
	}

	rep := e.r.Report()
	rep.Routes[0].Owner = "changed"
	rep.DNS[0].Servers[0] = ip("203.0.113.99")
	rep.DNS[0].MatchDomains[0] = "changed"
	rep.Net.Interfaces[0].Addrs[0] = pfx("203.0.113.0/24")
	rep.Net.DefaultV4.Iface = "changed"
	rep.ResolverEntries[0] = "changed"
	rep.Journal[0].Key = "changed"

	if after := e.r.Report(); !reflect.DeepEqual(before, after) {
		t.Errorf("a caller changed the Reconciler's report:\n before %+v\n after  %+v", before, after)
	}
}
