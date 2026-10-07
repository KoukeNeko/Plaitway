// Package manager owns the VPN engines. It turns the stored profiles, the
// engines' own status and the Reconciler's Report into the API's Profile
// messages, tells watchers about every change, and keeps the logs.
//
// Locking: Manager.mu guards all state and is held while calling the store and
// the Reconciler's Report. It is never held while calling an engine or
// announcing to the Reconciler, and nothing the Reconciler calls takes it
// (SetRebind's callback only signals), so the two cannot deadlock. entry.op
// serializes beginning a start and stopping one profile's engine; it is taken
// before Manager.mu and never held while an engine starts, which can take as
// long as a lookup that waits for the network (see startOp). The on-demand
// controller (ondemand.go) acts through SetEnabled, as any client does, and so
// holds none of these locks while it acts.
package manager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/profile"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

const (
	// ProtocolVersion is DaemonInfo.protocol_version.
	ProtocolVersion = 1

	defaultStopTimeout = 10 * time.Second
	defaultStartWait   = 3 * time.Second
	watchBuffer        = 64
	maxErrorText       = 1024
	maxHostText        = 255
)

// ErrShuttingDown is returned by calls that arrive while the daemon stops.
var ErrShuttingDown = errors.New("daemon is shutting down")

// ErrNotAwaitingCredentials is returned by ProvideCredentials for a profile
// that has no credential request open.
var ErrNotAwaitingCredentials = errors.New("profile is not awaiting credentials")

type Config struct {
	Log *slog.Logger
	// StateDir holds the profile store.
	StateDir string
	// Backends are the VPN implementations, one per kind.
	Backends   []tunnel.Backend
	Reconciler tunnel.Reconciler
	// Net is handed to the engines, and tells the on-demand profiles which
	// network the Mac is on.
	Net osnet.NetMonitor
	// DaemonLog serves WatchLogs with an empty profile id. Nil gives the
	// manager a buffer of its own that nothing writes to.
	DaemonLog *LogBuffer
	// Version and Privileged describe the daemon in DaemonInfo.
	Version    string
	Privileged bool
	// StopTimeout bounds how long one engine may take to stop; zero means 10 s.
	StopTimeout time.Duration
	// StartWait is how long enabling a profile waits for its engine to start
	// before it returns with the profile still connecting; zero means 3 s.
	StartWait time.Duration
	// OnDemandSettle is how long the network has to be quiet before the
	// on-demand profiles follow it; zero means 2 s.
	OnDemandSettle time.Duration
}

type Manager struct {
	log            *slog.Logger
	store          *profile.Store
	backends       map[tunnel.Kind]tunnel.Backend
	rec            tunnel.Reconciler
	net            osnet.NetMonitor
	daemonLog      *LogBuffer
	version        string
	privileged     bool
	stopTimeout    time.Duration
	startWait      time.Duration
	onDemandSettle time.Duration

	engineCtx     context.Context // parent of every engine's context
	cancelEngines context.CancelFunc
	recCtx        context.Context
	cancelRec     context.CancelFunc
	recFinished   chan struct{} // closed when the Reconciler's Run has returned
	fatal         chan error    // the Reconciler stopped on its own
	rebind        chan struct{}
	stopped       chan struct{} // closed by Shutdown
	started       bool
	starts        sync.WaitGroup // the engines being started, see startOp

	mu      sync.Mutex
	closing bool
	entries map[string]*entry
	subs    map[chan *pb.ProfileEvent]struct{}
}

// entry is the runtime state of one profile.
type entry struct {
	id   string
	logs *LogBuffer
	op   sync.Mutex

	// Guarded by Manager.mu from here on.
	desired bool
	// starting is set while the engine is being created and started, when run
	// is nil.
	starting *startOp
	run      *run
	// state and failure describe the profile while it has no run: DISCONNECTED,
	// CONNECTING (the engine is being created) or FAILED (it could not start).
	state    pb.ProfileState
	failure  string
	stopping bool // the engine is being stopped; the profile shows DISCONNECTING
	deleted  bool
	last     *pb.Profile // what watchers were last told; the source for List and snapshots
}

// run is one engine and the latest status it reported.
type run struct {
	owner  tunnel.OwnerID
	engine tunnel.Engine
	net    *ownerNetwork // the engine's view of the Reconciler
	cancel context.CancelFunc
	status tunnel.Status
}

// startOp is an engine being created and started. That can take a long time (a
// lookup that waits for the network to come up), so it runs on a goroutine of
// its own and without entry.op: a profile can be disabled or deleted and the
// daemon can shut down meanwhile, each of which cancels the start and waits for
// done.
type startOp struct {
	cancel context.CancelFunc // ends the start, and the engine's context after it
	done   chan struct{}      // closed when the engine is attached to the profile or the start has failed

	aborted bool // disable or Shutdown cancelled the start; guarded by Manager.mu
}

// New opens the profile store and wires the Reconciler. Nothing runs until Start.
func New(cfg Config) (*Manager, error) {
	backends := map[tunnel.Kind]tunnel.Backend{}
	for _, b := range cfg.Backends {
		if b.Kind == 0 || b.Parse == nil || b.New == nil || b.Probe == nil {
			return nil, fmt.Errorf("incomplete backend for kind %d", b.Kind)
		}
		if _, dup := backends[b.Kind]; dup {
			return nil, fmt.Errorf("two backends for kind %d", b.Kind)
		}
		backends[b.Kind] = b
	}
	store, err := profile.Open(cfg.StateDir, backends, cfg.Log)
	if err != nil {
		return nil, err
	}
	m := &Manager{
		log:            cfg.Log,
		store:          store,
		backends:       backends,
		rec:            cfg.Reconciler,
		net:            cfg.Net,
		daemonLog:      cfg.DaemonLog,
		version:        cfg.Version,
		privileged:     cfg.Privileged,
		stopTimeout:    cfg.StopTimeout,
		startWait:      cfg.StartWait,
		onDemandSettle: cfg.OnDemandSettle,
		recFinished:    make(chan struct{}),
		fatal:          make(chan error, 1),
		rebind:         make(chan struct{}, 1),
		stopped:        make(chan struct{}),
		entries:        map[string]*entry{},
		subs:           map[chan *pb.ProfileEvent]struct{}{},
	}
	if m.daemonLog == nil {
		m.daemonLog = NewLogBuffer("")
	}
	if m.stopTimeout == 0 {
		m.stopTimeout = defaultStopTimeout
	}
	if m.startWait == 0 {
		m.startWait = defaultStartWait
	}
	if m.onDemandSettle == 0 {
		m.onDemandSettle = defaultOnDemandSettle
	}
	m.engineCtx, m.cancelEngines = context.WithCancel(context.Background())
	m.recCtx, m.cancelRec = context.WithCancel(context.Background())

	m.mu.Lock()
	for _, meta := range store.List() {
		m.addEntryLocked(meta)
	}
	m.mu.Unlock()
	m.rec.SetRebind(m.requestRebind)
	return m, nil
}

// Start runs the Reconciler and connects the profiles with auto_connect set.
// It returns at once: those profiles are CONNECTING when it returns, and each
// engine starts in the background. Engines retry what can be transient (the
// network is often not up yet when the daemon starts at boot) and publish why
// they cannot connect, so the Manager neither waits for them nor retries.
func (m *Manager) Start() {
	m.started = true
	go m.runReconciler()
	go m.watchReconciler()
	go m.rebindLoop()

	for _, meta := range m.store.List() {
		if !meta.Settings.AutoConnect {
			continue
		}
		e, err := m.lookup(meta.ID)
		if err != nil {
			continue // deleted since the list was read
		}
		m.log.Info("connecting profile at startup", "profile", meta.ID)
		if _, err := m.begin(e); err != nil {
			m.log.Warn("profile not started", "profile", meta.ID, "err", err)
		}
	}
}

// Fatal delivers the error when the Reconciler stops without being asked to:
// nothing owns the routes then and the daemon should exit.
func (m *Manager) Fatal() <-chan error { return m.fatal }

// Shutdown stops every engine in parallel, each within the stop timeout and
// ctx, and then ends the Reconciler, which removes what it installed.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return nil
	}
	m.closing = true
	var runs []*run
	var starting []*startOp
	for _, e := range m.entries {
		if e.run != nil {
			e.stopping = true
			runs = append(runs, e.run)
		}
		if e.starting != nil {
			e.starting.aborted = true
			starting = append(starting, e.starting)
		}
	}
	m.mu.Unlock()

	// An engine that is still starting would hold up the shutdown for as long
	// as it takes; it stops itself once it is cancelled.
	for _, op := range starting {
		op.cancel()
	}
	errs := make([]error, len(runs))
	var wg sync.WaitGroup
	for i, r := range runs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = m.stopRun(ctx, r)
		}()
	}
	wg.Wait()
	m.cancelEngines()

	// The goroutines that start engines stop what they started once the
	// daemon is closing, and that must be done before the Reconciler ends.
	startsDone := make(chan struct{})
	go func() {
		m.starts.Wait()
		close(startsDone)
	}()
	select {
	case <-startsDone:
	case <-ctx.Done():
		errs = append(errs, fmt.Errorf("engines still starting: %w", ctx.Err()))
	}

	m.cancelRec()
	if m.started {
		select {
		case <-m.recFinished:
		case <-ctx.Done():
			errs = append(errs, fmt.Errorf("reconciler did not finish: %w", ctx.Err()))
		}
	}
	close(m.stopped)

	m.mu.Lock()
	for _, e := range m.entries {
		e.logs.Close()
	}
	m.mu.Unlock()
	return errors.Join(errs...)
}

func (m *Manager) runReconciler() {
	err := m.rec.Run(m.recCtx)
	close(m.recFinished)
	if m.recCtx.Err() != nil {
		return
	}
	if err == nil {
		err = errors.New("returned without being asked to stop")
	}
	m.fatal <- fmt.Errorf("reconciler: %w", err)
}

// watchReconciler republishes the profiles whenever the Reconciler's Report
// changed, which carries their route and DNS statuses.
func (m *Manager) watchReconciler() {
	changed := m.rec.Changed()
	for {
		select {
		case <-changed:
			m.mu.Lock()
			m.refreshLocked()
			m.mu.Unlock()
		case <-m.stopped:
			return
		}
	}
}

// requestRebind is what the Reconciler calls after repairing the routes of a
// changed network. It must not block, so it only signals rebindLoop.
func (m *Manager) requestRebind() {
	select {
	case m.rebind <- struct{}{}:
	default: // one is already pending
	}
}

func (m *Manager) rebindLoop() {
	for {
		select {
		case <-m.rebind:
			m.mu.Lock()
			var engines []tunnel.Engine
			for _, e := range m.entries {
				if e.run != nil && !e.stopping {
					engines = append(engines, e.run.engine)
				}
			}
			m.mu.Unlock()
			// Engines are independent, and one that is slow must not hold up the rest.
			for _, eng := range engines {
				go eng.Rebind()
			}
		case <-m.stopped:
			return
		}
	}
}

// Watchers is the number of open WatchProfiles subscriptions.
func (m *Manager) Watchers() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.subs)
}

// LogWatchers is the number of open WatchLogs subscriptions on a profile's log.
func (m *Manager) LogWatchers(profileID string) int {
	e, err := m.lookup(profileID)
	if err != nil {
		return 0
	}
	return e.logs.subscribers()
}

// Watch returns the current profiles and then every change. The channel is
// closed when the watcher falls too far behind; cancel releases it.
func (m *Manager) Watch() (snapshot []*pb.Profile, events <-chan *pb.ProfileEvent, cancel func()) {
	ch := make(chan *pb.ProfileEvent, watchBuffer)
	m.mu.Lock()
	defer m.mu.Unlock()
	// Registering and taking the snapshot in one hold of the lock leaves no
	// change between the two.
	m.subs[ch] = struct{}{}
	return m.snapshotLocked(), ch, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.subs, ch) // already gone if broadcastLocked dropped it
	}
}

// broadcastLocked never blocks: a watcher whose buffer is full is dropped
// (closing its channel makes its handler return) instead of stalling the daemon.
func (m *Manager) broadcastLocked(ev *pb.ProfileEvent) {
	for ch := range m.subs {
		select {
		case ch <- ev:
		default:
			delete(m.subs, ch)
			close(ch)
			m.log.Warn("dropped a profile watcher that was too slow")
		}
	}
}

// snapshotLocked returns the last published view of every profile, in
// priority order. Using the published views keeps a snapshot consistent with
// the events that follow it.
func (m *Manager) snapshotLocked() []*pb.Profile {
	metas := m.store.List()
	out := make([]*pb.Profile, 0, len(metas))
	for _, meta := range metas {
		if e := m.entries[meta.ID]; e != nil {
			out = append(out, proto.Clone(e.last).(*pb.Profile))
		}
	}
	return out
}

// refreshLocked rebuilds the view of each target (every profile when there are
// none) and publishes those that differ from what watchers last saw.
func (m *Manager) refreshLocked(targets ...*entry) {
	report := m.rec.Report()
	if len(targets) == 0 {
		for _, e := range m.entries {
			targets = append(targets, e)
		}
	}
	for _, e := range targets {
		meta, err := m.store.Get(e.id)
		if err != nil {
			continue // only a profile that is being deleted is missing
		}
		view := m.buildLocked(meta, e, &report)
		if e.last != nil && proto.Equal(e.last, view) {
			continue
		}
		e.last = view
		m.broadcastLocked(&pb.ProfileEvent{Event: &pb.ProfileEvent_Changed{Changed: view}})
	}
}

func (m *Manager) buildLocked(meta profile.Profile, e *entry, report *tunnel.Report) *pb.Profile {
	p := &pb.Profile{
		Id:             meta.ID,
		Name:           meta.Name,
		Kind:           kindToProto(meta.Kind),
		DesiredEnabled: e.desired,
		Settings:       settingsToProto(meta.Settings),
		Summary:        summaryToProto(meta.Summary),
	}
	if e.run == nil {
		p.State = e.state
		p.LastError = e.failure
		return p
	}
	st := e.run.status
	p.State = stateToProto(st.State)
	p.LastError = sanitizeText(st.Err, maxErrorText)
	p.Status = tunnelStatusToProto(st, meta.ID, report)
	switch {
	case e.stopping:
		p.State = pb.ProfileState_PROFILE_STATE_DISCONNECTING
		p.LastError = ""
	case st.State == tunnel.StateAwaitingCredentials:
		p.CredentialRequest = &pb.CredentialRequest{Kind: credentialKindToProto(st.NeedsCredentials)}
	}
	return p
}

// lookup finds the entry of an existing profile.
func (m *Manager) lookup(id string) (*entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok {
		return nil, notFound(id)
	}
	return e, nil
}

// current returns the last published view of a profile.
func (m *Manager) current(id string) (*pb.Profile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok {
		return nil, notFound(id)
	}
	return proto.Clone(e.last).(*pb.Profile), nil
}

func notFound(id string) error { return fmt.Errorf("profile %q: %w", id, profile.ErrNotFound) }
