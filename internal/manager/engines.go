package manager

import (
	"context"
	"fmt"
	"time"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// enable starts the profile's engine, or restarts it when the engine has
// failed, and waits for the start to end, at most StartWait. A failure to start
// is not returned: it makes the profile FAILED and keeps it desired, which is
// what the user sees. A start that takes longer goes on in the background and
// the profile stays CONNECTING until it ends.
func (m *Manager) enable(e *entry) error {
	return m.awaitStart(m.begin(e))
}

// awaitStart waits for the start that begin returned, as enable describes.
func (m *Manager) awaitStart(op *startOp, err error) error {
	if op == nil {
		return err
	}
	timer := time.NewTimer(m.startWait)
	defer timer.Stop()
	select {
	case <-op.done:
	case <-timer.C:
	}
	return nil
}

// begin marks the profile CONNECTING and starts its engine on a goroutine of
// its own, which attaches the engine to the profile or fails the profile. It
// returns the start, or nil when there is nothing to start because the engine
// is already starting or running.
func (m *Manager) begin(e *entry) (*startOp, error) {
	e.op.Lock()
	defer e.op.Unlock()
	return m.beginLocked(e)
}

// beginLocked is begin for a caller that holds e.op.
func (m *Manager) beginLocked(e *entry) (*startOp, error) {
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return nil, ErrShuttingDown
	}
	if e.deleted {
		m.mu.Unlock()
		return nil, notFound(e.id)
	}
	if e.starting != nil || (e.run != nil && e.run.status.State != tunnel.StateFailed) {
		m.mu.Unlock() // already starting or running
		return nil, nil
	}
	failed := e.run
	e.run = nil
	e.desired = true
	e.state = pb.ProfileState_PROFILE_STATE_CONNECTING
	e.failure = ""
	ctx, cancel := context.WithCancel(m.engineCtx)
	op := &startOp{cancel: cancel, done: make(chan struct{})}
	e.starting = op
	m.starts.Add(1) // under the lock that checked closing: Shutdown waits for it
	m.refreshLocked(e)
	m.mu.Unlock()

	if failed != nil {
		if err := m.stopRun(context.Background(), failed); err != nil {
			m.log.Warn("could not stop the failed engine before restarting", "profile", e.id, "err", err)
		}
	}
	e.logs.Add(pb.LogLevel_LOG_LEVEL_INFO, "starting")
	go m.start(ctx, e, op)
	return op, nil
}

// start creates and starts the engine, then attaches it to the profile, or fails
// the profile when it could not start. A start that disable or Shutdown
// aborted leaves the state to them.
func (m *Manager) start(ctx context.Context, e *entry, op *startOp) {
	defer m.starts.Done()
	defer close(op.done)

	r, err := m.startEngine(ctx, op.cancel, e)
	if err != nil {
		op.cancel() // no engine holds ctx; startEngine may have failed before there was one
	}

	m.mu.Lock()
	e.starting = nil
	switch {
	case err != nil && op.aborted:
		m.mu.Unlock()
	case err != nil:
		e.state = pb.ProfileState_PROFILE_STATE_FAILED
		e.failure = sanitizeText(err.Error(), maxErrorText)
		m.refreshLocked(e)
		m.mu.Unlock()
		e.logs.Add(pb.LogLevel_LOG_LEVEL_ERROR, "could not start: "+e.failure)
		m.log.Warn("profile did not start", "profile", e.id, "err", err)
	case m.closing:
		m.mu.Unlock()
		if err := m.stopRun(context.Background(), r); err != nil {
			m.log.Warn("could not stop an engine started during shutdown", "profile", e.id, "err", err)
		}
	default:
		e.run = r
		m.refreshLocked(e)
		m.mu.Unlock()
		go m.consume(e, r)
		// The priority may have changed while the engine was starting.
		m.refreshPriorities()
	}
}

// startEngine creates the engine for the stored profile and starts it. cancel
// ends ctx, which is the engine's context for as long as it runs.
func (m *Manager) startEngine(ctx context.Context, cancel context.CancelFunc, e *entry) (*run, error) {
	meta, err := m.store.Get(e.id)
	if err != nil {
		return nil, err
	}
	content, err := m.store.Content(e.id)
	if err != nil {
		return nil, err
	}
	backend, ok := m.backends[meta.Kind]
	if !ok {
		return nil, fmt.Errorf("no engine for this kind of profile")
	}
	if info := backend.Probe(); !info.Available {
		return nil, fmt.Errorf("engine not available: %s", info.Detail)
	}
	owner := tunnel.OwnerID(e.id)
	net := &ownerNetwork{rec: m.rec, store: m.store, owner: owner}
	eng, err := backend.New(tunnel.Spec{
		Owner:             owner,
		Name:              meta.Name,
		Content:           content,
		Mode:              meta.Settings.TunnelMode,
		Priority:          meta.Settings.Priority,
		ExcludePrivateIPs: meta.Settings.ExcludePrivateIPs,
	}, tunnel.Deps{
		Network: net,
		Net:     m.net,
		Log:     func(level tunnel.LogLevel, text string) { e.logs.Add(tunnelLevelToProto(level), text) },
	})
	if err != nil {
		return nil, err
	}

	r := &run{owner: owner, engine: eng, net: net, cancel: cancel, status: tunnel.Status{State: tunnel.StateConnecting}}
	if err := eng.Start(ctx); err != nil {
		// A half-started engine may hold an interface or a child process.
		if stopErr := m.stopRun(context.Background(), r); stopErr != nil {
			m.log.Warn("could not clean up an engine that failed to start", "profile", e.id, "err", stopErr)
		}
		return nil, err
	}
	return r, nil
}

// restart stops the engine of a profile that is switched on and starts it again
// with the text now stored, and does nothing for one that is not. It is one step
// under e.op: a disconnect that arrives meanwhile (the user's, or the on-demand
// controller's after a network change) either comes first, and the profile
// stays off, or comes after, and ends the restarted engine. Two separate steps
// let it fall in between and be undone by the start.
func (m *Manager) restart(e *entry) error {
	e.op.Lock()
	m.mu.Lock()
	switched := e.desired
	m.mu.Unlock()
	if !switched {
		e.op.Unlock()
		return nil
	}
	m.disable(context.Background(), e)
	op, err := m.beginLocked(e)
	e.op.Unlock()
	return m.awaitStart(op, err)
}

// disable cancels the start of the profile's engine, or stops the engine, and
// waits for it, within the stop timeout. The caller holds e.op. It never fails:
// an engine that cannot be stopped leaves the profile FAILED with the reason.
func (m *Manager) disable(ctx context.Context, e *entry) {
	m.mu.Lock()
	e.desired = false
	starting := e.starting
	if starting != nil {
		starting.aborted = true
	}
	m.mu.Unlock()
	if starting != nil {
		// Cancelling ends the start promptly, however long it would have taken.
		starting.cancel()
		<-starting.done
	}

	m.mu.Lock()
	r := e.run
	if r == nil {
		e.state = pb.ProfileState_PROFILE_STATE_DISCONNECTED
		e.failure = ""
		m.refreshLocked(e)
		m.mu.Unlock()
		return
	}
	e.stopping = true
	m.refreshLocked(e)
	m.mu.Unlock()

	e.logs.Add(pb.LogLevel_LOG_LEVEL_INFO, "stopping")
	err := m.stopRun(ctx, r)

	m.mu.Lock()
	defer m.mu.Unlock()
	e.run = nil
	e.stopping = false
	if err != nil {
		e.state = pb.ProfileState_PROFILE_STATE_FAILED
		e.failure = sanitizeText("could not stop: "+err.Error(), maxErrorText)
		e.logs.Add(pb.LogLevel_LOG_LEVEL_ERROR, e.failure)
		m.log.Warn("engine did not stop cleanly", "profile", e.id, "err", err)
	} else {
		e.state = pb.ProfileState_PROFILE_STATE_DISCONNECTED
		e.failure = ""
	}
	m.refreshLocked(e)
}

// stopRun stops the engine, which has to remove its interface and withdraw
// from the Reconciler within the stop timeout, and then releases its context.
// Whatever the engine did, nothing of it stays in the Reconciler afterwards.
func (m *Manager) stopRun(ctx context.Context, r *run) error {
	ctx, cancel := context.WithTimeout(ctx, m.stopTimeout)
	defer cancel()
	err := r.engine.Stop(ctx)
	r.cancel()
	r.net.close()
	return err
}

// consume applies the engine's status snapshots until the engine closes its
// channel, which it does when it has been stopped.
func (m *Manager) consume(e *entry, r *run) {
	for st := range r.engine.Status() {
		m.mu.Lock()
		if e.run == r && !e.stopping {
			r.status = st
			m.refreshLocked(e)
		}
		m.mu.Unlock()
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if e.run != r || e.stopping {
		return
	}
	// The engine closed its channel without being stopped, so nothing will
	// update this profile any more. A failure it reported before closing has
	// the real reason (openvpn's own error, for example); keep that one.
	if r.status.State != tunnel.StateFailed || r.status.Err == "" {
		r.status = tunnel.Status{State: tunnel.StateFailed, Err: "the engine stopped unexpectedly"}
		e.logs.Add(pb.LogLevel_LOG_LEVEL_ERROR, r.status.Err)
	}
	m.refreshLocked(e)
}
