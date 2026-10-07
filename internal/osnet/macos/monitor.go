package macos

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
	// Run runs networksetup; nil runs it for real.
	Run CommandRunner
}

// netMonitor implements osnet.NetMonitor. The darwin constructor wires it to
// the kernel; tests wire it to fakes.
type netMonitor struct {
	log        *slog.Logger
	clk        clock
	routes     osnet.RouteTable
	interfaces func() ([]osnet.Interface, error)
	kinds      *linkKinds
	// sleepTime is kern.sleeptime: when the machine last went to sleep.
	sleepTime func() (time.Time, error)
	// watchRoutes calls changed for every routing message that can matter, until
	// ctx ends or the route socket fails.
	watchRoutes func(ctx context.Context, log *slog.Logger, changed func()) error

	mu    sync.Mutex
	epoch epochTracker
}

func (m *netMonitor) Snapshot() (osnet.NetState, error) {
	ifaces, err := m.interfaces()
	if err != nil {
		return osnet.NetState{}, err
	}
	routes, err := m.routes.Dump()
	if err != nil {
		return osnet.NetState{}, fmt.Errorf("read routes: %w", err)
	}
	state := buildNetState(ifaces, routes)
	for _, next := range []*osnet.Nexthop{state.DefaultV4, state.DefaultV6} {
		if next != nil {
			next.Kind = m.kinds.kind(next.Iface)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.epoch.stamp(state), nil
}

func (m *netMonitor) Events(ctx context.Context) <-chan osnet.Change {
	out := make(chan osnet.Change)
	routeChanged := make(chan struct{}, 1)
	go m.watch(ctx, func() {
		select {
		case routeChanged <- struct{}{}:
		default: // one is already waiting; a Change carries no detail
		}
	})
	go func() {
		defer close(out)
		m.merge(ctx, routeChanged, out)
	}()
	return out
}

// watch keeps the route socket open. After a failure messages may have been
// missed, so it reports a change before it tries again.
func (m *netMonitor) watch(ctx context.Context, changed func()) {
	for ctx.Err() == nil {
		err := m.watchRoutes(ctx, m.log, changed)
		if ctx.Err() != nil {
			return
		}
		m.log.Error("route socket stopped; retrying", "err", err, "after", routeSocketRetry)
		changed()
		select {
		case <-ctx.Done():
		case <-m.clk.After(routeSocketRetry):
		}
	}
}

func (m *netMonitor) sample() wakeSample {
	s := wakeSample{wall: m.clk.Now(), mono: m.clk.Mono()}
	var err error
	if s.sleepTime, err = m.sleepTime(); err != nil {
		// The wall clock gap still detects most sleeps.
		m.log.Warn("read kern.sleeptime", "err", err)
	}
	return s
}

// merge turns route changes, wakes and the heartbeat into debounced Changes.
func (m *netMonitor) merge(ctx context.Context, routeChanged <-chan struct{}, out chan<- osnet.Change) {
	var (
		pending   burst
		wake      wakeDetector
		flushC    <-chan time.Time // armed while a Change is pending
		followUpC <-chan time.Time // armed after a wake
	)
	wake.woke(m.sample())
	wakePollC := m.clk.After(wakePollInterval)
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
		case <-wakePollC:
			wakePollC = m.clk.After(wakePollInterval)
			if wake.woke(m.sample()) {
				add(osnet.ChangeWake)
				followUpC = m.clk.After(wakeFollowUp)
			}
		case <-followUpC:
			followUpC = nil
			add(osnet.ChangeWake)
		case <-heartbeatC:
			heartbeatC = m.clk.After(heartbeatInterval)
			add(osnet.ChangeHeartbeat)
		case <-flushC:
			// New messages may have moved the due time since the timer was armed.
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
