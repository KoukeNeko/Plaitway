package windows

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// NetMonitorOptions configures NewNetMonitor.
type NetMonitorOptions struct {
	// Logger receives the monitor's diagnostics; nil uses slog.Default().
	Logger *slog.Logger
}

// netMonitor implements osnet.NetMonitor. The Windows constructor wires it to
// the IP Helper API; tests wire it to fakes.
type netMonitor struct {
	log      *slog.Logger
	clk      clock
	routes   osnet.RouteTable
	adapters func() ([]adapter, error)
	// watchChanges calls changed for every route, address and interface
	// notification that can matter, until ctx ends or the registration fails.
	// It has released every registration when it returns.
	watchChanges func(ctx context.Context, changed func()) error
	// watchWake calls woke whenever the machine resumes from sleep, in the same
	// way.
	watchWake func(ctx context.Context, woke func()) error

	mu    sync.Mutex
	epoch epochTracker
}

func (m *netMonitor) Snapshot() (osnet.NetState, error) {
	adapters, err := m.adapters()
	if err != nil {
		return osnet.NetState{}, err
	}
	// The routes are read after the adapters. A route of an adapter that has
	// appeared in between is skipped; one of an adapter that has vanished went
	// with it.
	routes, err := m.routes.Dump()
	if err != nil {
		return osnet.NetState{}, fmt.Errorf("read routes: %w", err)
	}
	state := buildNetState(adapters, routes)
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.epoch.stamp(state), nil
}

func (m *netMonitor) Events(ctx context.Context) <-chan osnet.Change {
	out := make(chan osnet.Change)
	routeChanged := make(chan struct{}, 1)
	woke := make(chan struct{}, 1)
	go m.supervise(ctx, "network changes", func(ctx context.Context) error {
		return m.watchChanges(ctx, notify(routeChanged))
	}, notify(routeChanged))
	go m.supervise(ctx, "wake from sleep", func(ctx context.Context) error {
		return m.watchWake(ctx, notify(woke))
	}, notify(routeChanged)) // a failed wake registration is not a wake, but a reason to look
	go func() {
		defer close(out)
		m.merge(ctx, routeChanged, woke, out)
	}()
	return out
}

// notify returns a function that never blocks, as it runs on a thread of the
// system's pool: a Change carries no detail, so a signal that is already
// waiting is as good as a second one.
func notify(c chan<- struct{}) func() {
	return func() {
		select {
		case c <- struct{}{}:
		default:
		}
	}
}

// supervise keeps one registration alive. After a failure notifications may
// have been missed, so it signals a change before it tries again.
func (m *netMonitor) supervise(ctx context.Context, what string, run func(context.Context) error, signal func()) {
	for ctx.Err() == nil {
		err := run(ctx)
		if ctx.Err() != nil {
			return
		}
		m.log.Error("notification stopped; retrying", "what", what, "err", err, "after", watchRetry)
		signal()
		select {
		case <-ctx.Done():
		case <-m.clk.After(watchRetry):
		}
	}
}

// merge turns notifications, wakes and the heartbeat into debounced Changes.
func (m *netMonitor) merge(ctx context.Context, routeChanged, woke <-chan struct{}, out chan<- osnet.Change) {
	var (
		pending   burst
		flushC    <-chan time.Time // armed while a Change is pending
		followUpC <-chan time.Time // armed after a wake
	)
	heartbeatC := m.clk.After(heartbeatInterval)
	add := func(reason osnet.ChangeReason) {
		now := m.clk.Mono()
		pending.add(now, reason)
		if flushC == nil {
			flushC = m.clk.After(pending.due() - now)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-routeChanged:
			add(osnet.ChangeRoute)
		case <-woke:
			add(osnet.ChangeWake)
			followUpC = m.clk.After(wakeFollowUp)
		case <-followUpC:
			followUpC = nil
			add(osnet.ChangeWake)
		case <-heartbeatC:
			heartbeatC = m.clk.After(heartbeatInterval)
			add(osnet.ChangeHeartbeat)
		case <-flushC:
			// New notifications may have moved the due time since the timer was armed.
			if wait := pending.due() - m.clk.Mono(); wait > 0 {
				flushC = m.clk.After(wait)
				continue
			}
			flushC = nil
			change := osnet.Change{Reason: pending.take(), At: m.clk.Now()}
			select {
			case out <- change:
			case <-ctx.Done():
				return
			}
		}
	}
}
