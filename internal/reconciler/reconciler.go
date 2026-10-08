// Package reconciler owns the host's routes and DNS. Engines announce what each
// tunnel wants; the Reconciler computes the one consistent result, applies the
// difference, and keeps it right when the physical network changes. It is the
// only caller of osnet.RouteTable and osnet.DNSConfigurator.
//
// Everything it installs that does not disappear with a tunnel interface (host
// routes through a gateway, resolver entries) is written to a journal before it
// is applied, so that a crash leaves a record for the next run to clean up.
//
// # Routing tables on macOS, Windows and Linux
//
// What the routing table calls "the same route" differs, and Config.Keying says
// which table the Reconciler is talking to (RouteKeying). The zero value is the
// macOS table. Everything below is chosen by it and by nothing else:
//
//   - Identity. macOS: one unscoped route per destination prefix; the kernel
//     picks the interface of a gateway route, so the interface is not compared,
//     and any other route to the prefix is a conflict ("held by another
//     program"). Windows: destination, interface index and next hop; several
//     routes can share a prefix, so a route through another interface or router
//     is a neighbour, not a conflict, and Plaitway adds its own beside it.
//     Linux: destination and metric. The kernel refuses a second route with both
//     whatever interface and next hop it has, so a foreign route with the metric
//     of ours is a conflict, and one with another metric is a neighbour. The
//     interface and next hop are attributes there, compared like the gateway on
//     macOS. The owned routes, the table read back and the planned routes are
//     all keyed this way (routeKey), and a route is deleted by its whole key.
//   - What a route carries. macOS: destination, gateway or interface name.
//     Windows and Linux: also the interface index, which Compute resolves from
//     the NetState it is given (tunnel.Intent names only the interface), and a
//     metric: tunnelMetric for tunnel routes and bypassMetric, lower, for the
//     host routes that keep endpoints reachable through the physical interface
//     and router of the current default route. The split halves of a default
//     route beat the system's default by their prefix length, so the metric only
//     decides between routes of one prefix. A route whose interface is not in
//     the NetState (just removed, not seen yet) waits as pending. On Windows it
//     is not retried on a timer: the interface appearing is a change of the
//     network. On Linux it is, since the monitor reports no event for the
//     devices of tunnels (see retriesUnplaced).
//   - The next hop of a tunnel route. macOS and Linux: none, the route is bound
//     to the interface; a tunnel device there is point-to-point and takes
//     on-link routes in both address families, whatever gateways the tunnel
//     announced. Windows: the tunnel's own gateway (tunnel.Intent.Gateway) when
//     it has one of the route's family, otherwise none. A point-to-point adapter
//     (Wintun) takes on-link routes; an Ethernet-like one (tap-windows6,
//     OpenVPN) answers only for its gateway's address, so a route without it
//     would blackhole while the tunnel is up. The gateway is part of the key,
//     the journal record and the fingerprint like any next hop, and a tunnel
//     that announces another gateway has its routes replaced: the new ones are
//     added before the old ones are deleted, so that a full tunnel never lets
//     traffic out in between.
//   - Neighbours that win. On Windows and Linux a route of another program to
//     the same prefix, which is not a conflict, can be the one in use: the
//     lowest effective metric decides (route metric plus, on Windows, interface
//     metric), unless it leads where ours does (the kernel's own on-link route of
//     a tunnel's subnet), and a default route's halves are also lost to foreign
//     routes that are more specific and cover the whole internet. Such a route
//     of ours stays in the table, is reported RouteFailed with Overridden set and
//     the winner in Detail (markOverridden), and is looked at again on every
//     network event, route events included: the other program may disconnect or
//     connect at any time. Plaitway never lowers its metric to win and never
//     deletes a foreign route.
//   - Journal. A route is journaled (pending, then applied) before and after it
//     is added. macOS journals only routes through a gateway, since the others go
//     with their interface; Windows and Linux journal every route, because a
//     tunnel adapter or device that survives a crash keeps its routes. A record
//     has the interface index in addition to destination and gateway, and on
//     Linux the metric, which is its identity in the journal and, with the
//     fingerprint (gateway, index, metric, flags), what a restart compares before
//     it deletes anything. On Linux the interface name is compared as well, since
//     the kernel can give the index of a removed interface to another one. macOS
//     records and fingerprints are as they always were. A start that finds a
//     record written for another table (no index, on Linux no metric) does not
//     know which route it meant and removes nothing for it.
//   - Stale routes. Besides the checks of macOS, a Windows or Linux endpoint
//     route that leaves through another interface than the default route is
//     stale, since the route is bound to the interface it names. A stale route is
//     named by its destination on macOS, by destination, index and next hop on
//     Windows, and by those and the metric on Linux (tunnel.StaleRoute.Key).
//   - Resolver entries. Where the system keeps resolver settings per interface
//     (Linux, systemd-resolved) an entry names the interface of the tunnel it
//     belongs to (osnet.DNSEntry.Iface), and a tunnel that moves to another
//     interface has its entry written again. macOS and Windows entries name none.
//   - Interface names. At most 15 characters on macOS (IFNAMSIZ) and Linux, 256
//     on Windows.
//
// What a vanished interface does to its routes is the same on all: they go with
// it, the Reconciler finds them gone and says nothing about another program.
//
// Not yet per platform, and written for macOS: the lookup that decides whether a
// nameserver is reached through the tunnel breaks a tie between prefixes of equal
// length by the order the kernel acquired them in (Windows and Linux use the
// metric, which the lookup does not read); and a tunnel route that names the same
// prefix as an endpoint is shadowed by that endpoint's bypass route, although a
// Windows or Linux table could hold both. Neither reads policy routing (ip rule)
// either: a program that sends traffic around the main table cannot be seen.
//
// # Policy routing on Linux
//
// A VPN that routes by rules instead of routes in the main table (wg-quick with
// a Table, Cloudflare WARP: a rule that sends all traffic without its mark to a
// table of its own, and one that suppresses the default route of the main table)
// is invisible to the network state and to the verdict on overridden routes. The
// kernel tests (rootintegration_linux_test.go) show what that means:
//   - The interface of such a VPN is a tunnel and the default route of the main
//     table stays the physical one.
//   - Routes of ours in the main table, which have a prefix length above zero,
//     are not suppressed and win against its catch-all, the halves of a default
//     route included. So does a more specific route of the main table that
//     another program added for a network of its own.
//   - A tunnel's endpoint that one of our routes captures has a host route
//     through the physical router, and the tunnel's own traffic keeps out of both
//     VPNs. An endpoint that none of our routes captures has none, and the
//     traffic to it follows the catch-all of the other VPN: for the Reconciler
//     there is nothing to protect it from.
//   - The Reconciler neither reads nor changes its rules and its table.
package reconciler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

const (
	defaultWakeDelay  = 4 * time.Second
	defaultRetryDelay = 5 * time.Second
	defaultMaxPasses  = 3
)

// ErrStopped is returned by calls made after Run has ended.
var ErrStopped = errors.New("reconciler stopped")

// Config is what New needs. Only the adapters and JournalPath are required.
type Config struct {
	Routes osnet.RouteTable
	DNS    osnet.DNSConfigurator
	Net    osnet.NetMonitor
	// JournalPath is the write-ahead journal file; its directory is created.
	JournalPath string
	Log         *slog.Logger
	// Keying is how the routing table identifies a route. The zero value is the
	// macOS table; a Windows daemon sets KeyByPrefixInterfaceNextHop and a Linux
	// one KeyLinux.
	Keying RouteKeying

	// Now stamps journal records; time.Now when nil.
	Now func() time.Time
	// WakeDelay is how long after a wake the second pass runs, because DHCP
	// often finishes after the wake event. Default 4 seconds.
	WakeDelay time.Duration
	// RetryDelay is how long to wait before trying again after a pass left
	// work undone. Default 5 seconds.
	RetryDelay time.Duration
	// MaxPasses is how many consecutive passes may fail to converge before
	// everything is removed and rebuilt. Default 3.
	MaxPasses int
}

// Reconciler implements tunnel.Reconciler.
//
// Locking: applyMu serializes everything that reads or changes the host
// (Announce, Withdraw, handle, Resync, RemoveStale and stop) and owns the
// fields below it. It is held across calls into the adapters, which can be slow:
// scutil waits for configd for up to 10 s per call. Nothing that has to stay
// responsive may take it. Report reads the snapshot that each of those
// operations publishes when it ends, and the timers, SetRebind and Run use mu,
// which guards a few fields and is never held across a call into an adapter.
// Methods whose name ends in Locked need applyMu. The order is applyMu, then mu.
type Reconciler struct {
	routes  osnet.RouteTable
	dns     osnet.DNSConfigurator
	monitor osnet.NetMonitor
	journal *journal
	keying  RouteKeying
	log     *slog.Logger
	now     func() time.Time

	wakeDelay, retryDelay time.Duration
	maxPasses             int

	changed chan struct{}                 // coalesced "Report has something new"
	recheck chan struct{}                 // timers ask Run for another pass
	report  atomic.Pointer[tunnel.Report] // what Report returns

	applyMu    sync.Mutex
	intents    map[tunnel.OwnerID]tunnel.Intent
	netState   osnet.NetState
	lastChange osnet.Change

	// What this process has installed. owned holds the routes it will delete;
	// a route in the table that is not in owned is somebody else's. An empty
	// appliedDNS in dnsApplied is an owner whose last write failed: it may have
	// left entries behind, so they are removed when the owner has none to want.
	owned      map[routeKey]ownedRoute
	dnsApplied map[tunnel.OwnerID]appliedDNS
	// overridden says, by destination, why a route we installed is not the one in
	// use (markOverridden), so that a change is logged once.
	overridden map[netip.Prefix]string
	// unlisted holds the foreign routes whose interface the network state did not
	// list at the last pass (markOverridden).
	unlisted map[routeKey]bool

	desired         Desired
	routeReports    []tunnel.RouteReport
	dnsReports      []tunnel.DNSReport
	stale           []tunnel.StaleRoute
	resolverEntries []string

	failStreak   int  // consecutive passes that failed with something new, for the reset
	failedPasses int  // consecutive passes that failed at all, for the retry delay
	dirty        bool // the last pass left work for a later one
	// futile holds the errors that were still there after the last reset. A
	// reset cannot cure what it has just failed to cure; a pass that fails with
	// nothing else does not count toward the next one.
	futile map[string]bool

	mu         sync.Mutex
	rebind     func()
	running    bool
	closed     bool
	wakeTimer  *time.Timer
	retryTimer *time.Timer
}

var _ tunnel.Reconciler = (*Reconciler)(nil)

// New opens the journal, undoes what a previous run left behind (its journaled
// routes, and every resolver entry carrying our marker), and reads the network.
func New(cfg Config) (*Reconciler, error) {
	if cfg.Routes == nil || cfg.DNS == nil || cfg.Net == nil {
		return nil, errors.New("reconciler: Routes, DNS and Net are required")
	}
	if cfg.JournalPath == "" {
		return nil, errors.New("reconciler: JournalPath is required")
	}
	if !cfg.Keying.valid() {
		return nil, fmt.Errorf("reconciler: unknown route keying %d", cfg.Keying)
	}
	r := &Reconciler{
		routes:     cfg.Routes,
		dns:        cfg.DNS,
		monitor:    cfg.Net,
		keying:     cfg.Keying,
		log:        cfg.Log,
		now:        cfg.Now,
		wakeDelay:  cfg.WakeDelay,
		retryDelay: cfg.RetryDelay,
		maxPasses:  cfg.MaxPasses,
		changed:    make(chan struct{}, 1),
		recheck:    make(chan struct{}, 1),
		intents:    make(map[tunnel.OwnerID]tunnel.Intent),
		owned:      make(map[routeKey]ownedRoute),
		dnsApplied: make(map[tunnel.OwnerID]appliedDNS),
	}
	if r.log == nil {
		r.log = slog.New(slog.DiscardHandler)
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.wakeDelay <= 0 {
		r.wakeDelay = defaultWakeDelay
	}
	if r.retryDelay <= 0 {
		r.retryDelay = defaultRetryDelay
	}
	if r.maxPasses <= 0 {
		r.maxPasses = defaultMaxPasses
	}

	var err error
	if r.journal, err = openJournal(cfg.JournalPath, r.now, r.log); err != nil {
		return nil, err
	}
	r.journal.pendingOnly = r.keying.syncsPendingOnly()
	if err := r.recover(); err != nil {
		r.journal.close()
		return nil, err
	}
	if r.netState, err = r.monitor.Snapshot(); err != nil {
		r.journal.close()
		return nil, fmt.Errorf("reading the network: %w", err)
	}
	// With no intents this only reads the host, so that Report lists the stale
	// routes and the resolver entries from the start.
	r.reconcileLocked(nil, false)
	r.storeReportLocked()
	return r, nil
}

// Announce replaces the intent of in.Owner and returns once the difference has
// been applied. Routes and DNS that could not be installed are not an error
// here; Report says what is in place.
func (r *Reconciler) Announce(in tunnel.Intent) error {
	if err := validateIntent(r.keying, in); err != nil {
		return fmt.Errorf("announce: %w", err)
	}
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	if r.isClosed() {
		return ErrStopped
	}
	r.intents[in.Owner] = cloneIntent(in)
	r.refreshNetLocked()
	r.passLocked(false)
	r.notifyLocked()
	return nil
}

// Withdraw removes everything owner announced and installed.
func (r *Reconciler) Withdraw(owner tunnel.OwnerID) {
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	if r.isClosed() {
		return
	}
	if _, ok := r.intents[owner]; !ok {
		return
	}
	delete(r.intents, owner)
	r.refreshNetLocked()
	r.passLocked(false)
	r.notifyLocked()
}

// refreshNetLocked takes a fresh look at the network before the pass that an
// Announce or Withdraw causes. The monitor sends no event for a tunnel
// interface, so the one an engine has just created is not in r.netState, and
// Compute needs it to see that a nameserver on the tunnel's own subnet is
// reached through the tunnel. A change of the underlay is left to handle, which
// has to see it to rebind.
func (r *Reconciler) refreshNetLocked() {
	ns, err := r.monitor.Snapshot()
	if err != nil {
		r.log.Warn("reading the network failed", "err", err)
		return
	}
	if !underlayChanged(r.netState, ns) {
		r.netState = ns
	}
}

// SetRebind registers what to call after the routes were repaired following a
// change of the physical network while a tunnel is up. It runs without the
// Reconciler's lock held.
func (r *Reconciler) SetRebind(f func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rebind = f
}

// Changed signals, coalesced, that Report would now return something new.
func (r *Reconciler) Changed() <-chan struct{} { return r.changed }

// notifyLocked publishes what an operation did and signals Changed.
func (r *Reconciler) notifyLocked() {
	r.storeReportLocked()
	select {
	case r.changed <- struct{}{}:
	default:
	}
}

func (r *Reconciler) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// Run applies network changes until ctx ends, then removes everything it
// owns. It returns the errors of that removal; after it returns the
// Reconciler accepts nothing more.
func (r *Reconciler) Run(ctx context.Context) error {
	r.mu.Lock()
	if r.running || r.closed {
		r.mu.Unlock()
		return errors.New("reconciler: Run can only be called once")
	}
	r.running = true
	r.mu.Unlock()

	events := r.monitor.Events(ctx)
	r.handle(osnet.Change{Reason: osnet.ChangeManual, At: r.now()}, true)
	for {
		select {
		case <-ctx.Done():
			return r.stop(nil)
		case c, ok := <-events:
			if !ok {
				if ctx.Err() != nil {
					return r.stop(nil)
				}
				return r.stop(errors.New("network event stream ended"))
			}
			r.handle(c, true)
		case <-r.recheck:
			r.handle(osnet.Change{Reason: osnet.ChangeHeartbeat, At: r.now()}, false)
		}
	}
}

// handle processes one hint that the network may have changed. External
// changes come from the monitor; the others are our own timers.
func (r *Reconciler) handle(c osnet.Change, external bool) {
	if rebind := r.handleChange(c, external); rebind != nil {
		rebind()
	}
}

// handleChange does the work of handle and returns what is left to call once
// applyMu is released: the rebind, when the engines are to reconnect.
func (r *Reconciler) handleChange(c osnet.Change, external bool) (rebind func()) {
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	if r.isClosed() {
		return nil
	}
	if external {
		r.lastChange = c
	}
	ns, err := r.monitor.Snapshot()
	if err != nil {
		r.log.Error("reading the network failed", "err", err)
		r.failedPasses++
		r.armRetry(r.retryAfter())
		r.notifyLocked()
		return nil
	}
	underlay := underlayChanged(r.netState, ns)
	// Route events that changed nothing we look at are skipped; anything else,
	// the periodic heartbeat included, is a full pass that also catches drift. On
	// Windows and Linux what another program does to the table decides whether
	// our routes are in use (markOverridden), and the NetState does not show it,
	// so every route event is looked at. A pass on an unchanged table writes
	// nothing.
	full := netChanged(r.netState, ns) || c.Reason != osnet.ChangeRoute || r.dirty || r.keying.byInterface()
	r.netState = ns
	if c.Reason == osnet.ChangeWake {
		r.armWake()
	}
	if full {
		r.passLocked(underlay || c.Reason == osnet.ChangeWake)
	}
	r.notifyLocked()
	// Without a default route there is nothing to rebind to; the change that
	// brings one back will trigger it.
	if underlay && r.liveTunnelLocked() && (ns.DefaultV4 != nil || ns.DefaultV6 != nil) {
		return r.rebindFunc()
	}
	return nil
}

func (r *Reconciler) rebindFunc() func() {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rebind
}

func (r *Reconciler) liveTunnelLocked() bool {
	for _, in := range r.intents {
		if carriesRoutes(in.State) {
			return true
		}
	}
	return false
}

func (r *Reconciler) armWake() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.wakeTimer != nil {
		r.wakeTimer.Stop()
	}
	r.wakeTimer = time.AfterFunc(r.wakeDelay, r.requestRecheck)
}

// armRetry asks Run for another pass after d, unless one is already pending.
func (r *Reconciler) armRetry(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retryTimer != nil || r.closed {
		return
	}
	r.retryTimer = time.AfterFunc(d, func() {
		r.mu.Lock()
		r.retryTimer = nil
		r.mu.Unlock()
		r.requestRecheck()
	})
}

func (r *Reconciler) requestRecheck() {
	select {
	case r.recheck <- struct{}{}:
	default:
	}
}

// stop removes everything this process owns and ends the Reconciler.
func (r *Reconciler) stop(cause error) error {
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	r.mu.Lock()
	r.closed = true
	if r.wakeTimer != nil {
		r.wakeTimer.Stop()
	}
	if r.retryTimer != nil {
		r.retryTimer.Stop()
	}
	r.mu.Unlock()
	clear(r.intents)
	res := r.reconcileLocked(nil, true)
	if err := r.sweepResolvers(); err != nil {
		res.fail(err)
	}
	if err := r.journal.close(); err != nil {
		res.fail(fmt.Errorf("closing the journal: %w", err))
	}
	r.notifyLocked()
	return errors.Join(append([]error{cause}, res.errs...)...)
}

// Resync re-reads the network, removes everything this process owns and builds
// it again from the announced intents.
func (r *Reconciler) Resync() error {
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	if r.isClosed() {
		return ErrStopped
	}
	ns, err := r.monitor.Snapshot()
	if err != nil {
		return fmt.Errorf("reading the network: %w", err)
	}
	r.netState = ns
	res := r.resetLocked()
	r.failStreak = 0
	r.settleLocked(res)
	r.notifyLocked()
	return errors.Join(res.errs...)
}

// RemoveStale deletes one route that Report listed as stale. The route is
// checked again first, so a route that was repaired in the meantime is safe.
func (r *Reconciler) RemoveStale(key string) error {
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	if r.isClosed() {
		return ErrStopped
	}
	table, err := r.dumpTable()
	if err != nil {
		return err
	}
	var target *tunnel.StaleRoute
	stale := findStale(r.keying, table, r.netState, r.endpointsLocked(), r.isOwned)
	if i := slices.IndexFunc(stale, func(s tunnel.StaleRoute) bool { return s.Key == key }); i >= 0 {
		target = &stale[i]
	}
	if target == nil {
		return fmt.Errorf("%s is not a stale route", key)
	}
	if err := r.routes.Delete(target.Route); err != nil && !errors.Is(err, osnet.ErrNotFound) {
		return fmt.Errorf("deleting stale route %s: %w", key, err)
	}
	r.release(r.keying.key(target.Route), "removed as stale")
	r.passLocked(false) // the route may have been in the way of one we want
	r.notifyLocked()
	return nil
}

func (r *Reconciler) isOwned(rt osnet.Route) bool {
	_, ok := r.owned[r.keying.key(rt)]
	return ok
}

// Report returns what the Reconciler believed and had done when its last
// operation ended. It does not wait for one that is still running.
func (r *Reconciler) Report() tunnel.Report {
	return cloneReport(*r.report.Load())
}

// storeReportLocked makes the current state what Report returns.
func (r *Reconciler) storeReportLocked() {
	rep := cloneReport(tunnel.Report{
		Net:             r.netState,
		LastChange:      r.lastChange,
		Routes:          r.routeReports,
		DNS:             r.dnsReports,
		Stale:           r.stale,
		ResolverEntries: r.resolverEntries,
		Journal:         r.journal.view(),
	})
	r.report.Store(&rep)
}

// cloneReport copies what the callers of Report could otherwise change.
func cloneReport(rep tunnel.Report) tunnel.Report {
	rep.Net = cloneNetState(rep.Net)
	rep.Routes = slices.Clone(rep.Routes)
	rep.DNS = slices.Clone(rep.DNS)
	for i := range rep.DNS {
		rep.DNS[i].Servers = slices.Clone(rep.DNS[i].Servers)
		rep.DNS[i].MatchDomains = slices.Clone(rep.DNS[i].MatchDomains)
	}
	rep.Stale = slices.Clone(rep.Stale)
	rep.ResolverEntries = slices.Clone(rep.ResolverEntries)
	rep.Journal = slices.Clone(rep.Journal)
	return rep
}

func (r *Reconciler) intentList() []tunnel.Intent {
	return slices.Collect(maps.Values(r.intents))
}

// endpointsLocked returns the host prefixes of every announced endpoint.
func (r *Reconciler) endpointsLocked() map[netip.Prefix]bool {
	out := make(map[netip.Prefix]bool)
	for _, in := range r.intents {
		if !carriesEndpoints(in.State) {
			continue
		}
		for _, ep := range in.Endpoints {
			ep = ep.Unmap().WithZone("")
			out[netip.PrefixFrom(ep, ep.BitLen())] = true
		}
	}
	return out
}
