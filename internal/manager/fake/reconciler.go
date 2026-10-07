package fake

import (
	"context"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// The pretend physical network, and the routes a "# fake: conflict" profile
// announces on top of its own.
var (
	localSubnet     = netip.MustParsePrefix("192.168.0.0/24") // BLOCKED: the machine is attached to it
	localGateway    = netip.MustParseAddr("192.168.0.1")
	shadowedRoute   = netip.MustParsePrefix("10.99.0.0/16") // SHADOWED: held by phantomOwner
	phantomOwner    = tunnel.OwnerID("fake-peer")
	defaultV4Halves = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/1"), netip.MustParsePrefix("128.0.0.0/1")}
)

const (
	localIface  = "en0"
	maxJournal  = 200
	staleKey    = "fake-stale-1"
	staleReason = "the gateway is not on the interface's subnet (simulated)"
)

var staleRoute = tunnel.StaleRoute{
	Key: staleKey,
	Route: osnet.Route{
		Dst:     netip.MustParsePrefix("203.0.113.9/32"),
		Gateway: netip.MustParseAddr("192.168.0.254"),
		Iface:   localIface,
		Static:  true,
	},
	Reason: staleReason,
}

var (
	_ tunnel.Reconciler = (*Reconciler)(nil)
	_ osnet.NetMonitor  = (*Reconciler)(nil)
)

// Reconciler is a tunnel.Reconciler and osnet.NetMonitor that touches nothing.
// It records what the engines announce and reports it as installed, with these
// exceptions so that every status of the API can be seen:
//
//   - a route inside the pretend local network 192.168.0.0/24 is BLOCKED;
//   - the route 10.99.0.0/16 is SHADOWED by a profile "fake-peer";
//   - one stale route, "fake-stale-1", is reported until RemoveStale deletes
//     it; Resync brings it back.
//
// Resync and NetworkChanged also call the rebind callback, which makes the
// connected profiles show RECONNECTING for a moment.
//
// As a NetMonitor it reports a primary network that is Wi-Fi and stays so, and
// never delivers an event.
type Reconciler struct {
	mu      sync.Mutex
	intents map[tunnel.OwnerID]tunnel.Intent
	stale   []tunnel.StaleRoute
	last    osnet.Change
	epoch   uint64
	journal []tunnel.JournalRecord
	rebind  func()
	changed chan struct{}
}

func NewReconciler() *Reconciler {
	return &Reconciler{
		intents: map[tunnel.OwnerID]tunnel.Intent{},
		stale:   []tunnel.StaleRoute{staleRoute},
		last:    osnet.Change{Reason: osnet.ChangeHeartbeat, At: time.Now()},
		epoch:   1,
		changed: make(chan struct{}, 1),
	}
}

func (r *Reconciler) Announce(in tunnel.Intent) error {
	r.mu.Lock()
	r.intents[in.Owner] = in
	if in.State == tunnel.StateUp {
		for _, p := range in.Routes {
			r.recordLocked(in.Owner, "route", p.String(), "applied")
		}
	}
	r.mu.Unlock()
	r.signal()
	return nil
}

func (r *Reconciler) Withdraw(owner tunnel.OwnerID) {
	r.mu.Lock()
	in, had := r.intents[owner]
	delete(r.intents, owner)
	if had && in.State == tunnel.StateUp {
		for _, p := range in.Routes {
			r.recordLocked(owner, "route", p.String(), "removed")
		}
	}
	r.mu.Unlock()
	if had {
		r.signal()
	}
}

// Run blocks until ctx ends, like the real one, and then forgets everything.
func (r *Reconciler) Run(ctx context.Context) error {
	<-ctx.Done()
	r.mu.Lock()
	clear(r.intents)
	r.mu.Unlock()
	return nil
}

func (r *Reconciler) Changed() <-chan struct{} { return r.changed }

func (r *Reconciler) SetRebind(f func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rebind = f
}

func (r *Reconciler) Resync() error {
	r.mu.Lock()
	if !slices.ContainsFunc(r.stale, func(s tunnel.StaleRoute) bool { return s.Key == staleKey }) {
		r.stale = append(r.stale, staleRoute)
	}
	r.mu.Unlock()
	r.networkChanged(osnet.ChangeManual)
	return nil
}

func (r *Reconciler) RemoveStale(key string) error {
	r.mu.Lock()
	i := slices.IndexFunc(r.stale, func(s tunnel.StaleRoute) bool { return s.Key == key })
	if i < 0 {
		r.mu.Unlock()
		return fmt.Errorf("no stale route %q", key)
	}
	removed := r.stale[i]
	r.stale = slices.Delete(r.stale, i, i+1)
	r.recordLocked("", "route", removed.Route.Dst.String(), "removed")
	r.mu.Unlock()
	r.signal()
	return nil
}

// NetworkChanged pretends that the network changed: the engines are told to
// rebind, as after a real change.
func (r *Reconciler) NetworkChanged() { r.networkChanged(osnet.ChangeRoute) }

func (r *Reconciler) networkChanged(reason osnet.ChangeReason) {
	r.mu.Lock()
	r.last = osnet.Change{Reason: reason, At: time.Now()}
	r.epoch++
	rebind := r.rebind
	r.mu.Unlock()
	r.signal()
	if rebind != nil {
		rebind()
	}
}

func (r *Reconciler) Report() tunnel.Report {
	r.mu.Lock()
	defer r.mu.Unlock()
	report := tunnel.Report{
		Net:        r.netStateLocked(),
		LastChange: r.last,
		Stale:      slices.Clone(r.stale),
		Journal:    slices.Clone(r.journal),
	}
	for _, owner := range slices.Sorted(maps.Keys(r.intents)) {
		in := r.intents[owner]
		if in.State != tunnel.StateUp {
			continue
		}
		report.Routes = append(report.Routes, routeReports(in)...)
		for _, d := range in.DNS {
			report.DNS = append(report.DNS, tunnel.DNSReport{
				Owner:        owner,
				Servers:      slices.Clone(d.Servers),
				MatchDomains: slices.Clone(d.MatchDomains),
				State:        tunnel.RouteInstalled,
			})
		}
		if len(in.DNS) > 0 {
			report.ResolverEntries = append(report.ResolverEntries, fmt.Sprintf("State:/Network/Service/plaitway-%s/DNS", owner))
		}
	}
	return report
}

// routeReports expands an intent the way the real Reconciler does: a default
// route becomes two halves, and a full tunnel keeps its endpoints reachable.
func routeReports(in tunnel.Intent) []tunnel.RouteReport {
	var out []tunnel.RouteReport
	if in.Role == tunnel.RoleFull {
		for _, ep := range in.Endpoints {
			out = append(out, tunnel.RouteReport{
				Prefix: netip.PrefixFrom(ep, ep.BitLen()),
				Owner:  in.Owner,
				Kind:   tunnel.RouteBypass,
				Via:    localGateway.String(),
				State:  tunnel.RouteInstalled,
			})
		}
	}
	for _, p := range in.Routes {
		if p == netip.MustParsePrefix("0.0.0.0/0") {
			for _, half := range defaultV4Halves {
				out = append(out, tunnel.RouteReport{Prefix: half, Owner: in.Owner, Kind: tunnel.RouteDefaultHalf, Via: in.Iface, State: tunnel.RouteInstalled})
			}
			continue
		}
		report := tunnel.RouteReport{Prefix: p, Owner: in.Owner, Kind: tunnel.RouteTunnel, Via: in.Iface, State: tunnel.RouteInstalled}
		switch {
		case p.Overlaps(localSubnet):
			report.State = tunnel.RouteBlocked
			report.Detail = "overlaps the local network " + localSubnet.String()
		case p == shadowedRoute:
			report.State = tunnel.RouteShadowed
			report.ShadowedBy = phantomOwner
			report.Detail = "held by a profile with higher priority"
		}
		out = append(out, report)
	}
	return out
}

func (r *Reconciler) netStateLocked() osnet.NetState {
	state := osnet.NetState{
		Epoch: r.epoch,
		Interfaces: []osnet.Interface{
			{Name: "lo0", Index: 1, Up: true},
			{Name: localIface, Index: 4, Up: true, Addrs: []netip.Prefix{netip.MustParsePrefix("192.168.0.10/24")}},
		},
		DefaultV4: &osnet.Nexthop{Gateway: localGateway, Iface: localIface, Kind: osnet.LinkWiFi},
		Connected: []netip.Prefix{localSubnet},
	}
	for _, owner := range slices.Sorted(maps.Keys(r.intents)) {
		if in := r.intents[owner]; in.State == tunnel.StateUp {
			state.Interfaces = append(state.Interfaces, osnet.Interface{Name: in.Iface, Index: 100 + len(state.Interfaces), Up: true, Tunnel: true})
		}
	}
	return state
}

func (r *Reconciler) recordLocked(owner tunnel.OwnerID, kind, key, state string) {
	r.journal = append(r.journal, tunnel.JournalRecord{Time: time.Now(), Owner: owner, Kind: kind, Key: key, State: state})
	if len(r.journal) > maxJournal {
		r.journal = slices.Delete(r.journal, 0, len(r.journal)-maxJournal)
	}
}

// signal coalesces: one pending signal is enough to say that Report changed.
func (r *Reconciler) signal() {
	select {
	case r.changed <- struct{}{}:
	default:
	}
}

// Snapshot and Events make the stand-in usable as the engines' osnet.NetMonitor.
func (r *Reconciler) Snapshot() (osnet.NetState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.netStateLocked(), nil
}

// Events never delivers: the fake engines do not watch the network.
func (r *Reconciler) Events(ctx context.Context) <-chan osnet.Change {
	ch := make(chan osnet.Change)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch
}
