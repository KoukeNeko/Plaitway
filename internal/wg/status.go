package wg

import (
	"slices"
	"sync"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// statusFeed holds an engine's current status and delivers snapshots on a
// channel with room for one: a slow reader loses intermediate snapshots and
// always finds the latest.
type statusFeed struct {
	mu     sync.Mutex
	cur    tunnel.Status
	ch     chan tunnel.Status
	closed bool
}

func newStatusFeed() *statusFeed {
	return &statusFeed{ch: make(chan tunnel.Status, 1)}
}

// update changes the status and delivers it when it differs from the one
// before. It does nothing once the feed is closed.
func (f *statusFeed) update(change func(*tunnel.Status)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	before := f.cur
	before.Warnings = slices.Clone(f.cur.Warnings)
	change(&f.cur)
	if sameStatus(before, f.cur) {
		return
	}
	snapshot := f.cur
	snapshot.Warnings = slices.Clone(f.cur.Warnings)
	select {
	case <-f.ch:
	default:
	}
	f.ch <- snapshot
}

// close ends the channel; a snapshot still in it can be read first.
func (f *statusFeed) close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.ch)
	}
}

func sameStatus(a, b tunnel.Status) bool {
	return a.State == b.State && a.Err == b.Err && a.Iface == b.Iface && a.Remote == b.Remote &&
		a.Since.Equal(b.Since) && a.Stats == b.Stats && a.NeedsCredentials == b.NeedsCredentials &&
		slices.Equal(a.Addresses, b.Addresses) && slices.Equal(a.Warnings, b.Warnings)
}
