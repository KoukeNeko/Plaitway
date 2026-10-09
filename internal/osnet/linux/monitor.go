package linux

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

// netMonitor implements osnet.NetMonitor. The Linux constructor wires it to the
// kernel; tests wire it to fakes.
type netMonitor struct {
	log    *slog.Logger
	clk    clock
	routes osnet.RouteTable
	links  func() ([]link, error)
	kinds  *linkKinds
	// watchChanges calls changed for every netlink message that can matter,
	// until ctx ends or the socket fails.
	watchChanges func(ctx context.Context, log *slog.Logger, changed func()) error

	mu    sync.Mutex
	epoch epochTracker
}

func (m *netMonitor) Snapshot() (osnet.NetState, error) {
	links, err := m.links()
	if err != nil {
		return osnet.NetState{}, err
	}
	routes, err := m.routes.Dump()
	if err != nil {
		return osnet.NetState{}, fmt.Errorf("read routes: %w", err)
	}
	state := buildNetState(links, routes)
	for _, next := range []*osnet.Nexthop{state.DefaultV4, state.DefaultV6} {
		if next != nil {
			next.Kind = m.kinds.kind(next.Iface)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.epoch.stamp(links, state), nil
}

func (m *netMonitor) Events(ctx context.Context) <-chan osnet.Change {
	out := make(chan osnet.Change)
	netChanged := make(chan struct{}, 1)
	go m.watch(ctx, func() {
		select {
		case netChanged <- struct{}{}:
		default: // one is already waiting; a Change carries no detail
		}
	})
	go func() {
		defer close(out)
		m.merge(ctx, netChanged, out)
	}()
	return out
}

// watch keeps the netlink socket open. After a failure messages may have been
// missed, so it reports a change before it tries again.
func (m *netMonitor) watch(ctx context.Context, changed func()) {
	for ctx.Err() == nil {
		err := m.watchChanges(ctx, m.log, changed)
		if ctx.Err() != nil {
			return
		}
		m.log.Error("netlink socket stopped; retrying", "err", err, "after", netlinkRetry)
		changed()
		select {
		case <-ctx.Done():
		case <-m.clk.After(netlinkRetry):
		}
	}
}

func (m *netMonitor) sample() wakeSample {
	return wakeSample{wall: m.clk.Now(), mono: m.clk.Mono()}
}

// merge turns network changes, wakes and the heartbeat into debounced Changes.
func (m *netMonitor) merge(ctx context.Context, netChanged <-chan struct{}, out chan<- osnet.Change) {
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
		case <-netChanged:
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
