package linux

import (
	"log/slog"
	"slices"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

const (
	// A burst of netlink messages becomes one Change: it is sent after burstQuiet
	// without a new message, and at the latest burstMax after the first one.
	burstQuiet = 300 * time.Millisecond
	burstMax   = 2 * time.Second

	heartbeatInterval = 60 * time.Second
	wakePollInterval  = 5 * time.Second
	// wakeGap is how far the wall clock may run ahead of the monotonic clock
	// between two polls before the machine is taken to have slept.
	wakeGap = 10 * time.Second
	// DHCP often finishes after the wake, so a wake is reported again later.
	wakeFollowUp = 4 * time.Second

	netlinkRetry = 3 * time.Second
)

// clock is the time the monitor lives by. The monotonic clock does not advance
// while the machine sleeps (CLOCK_MONOTONIC on Linux); the wall clock does, and
// the difference is how a wake is noticed.
type clock interface {
	// Now is the wall clock. It carries no monotonic reading, so that
	// subtracting two values measures wall time.
	Now() time.Time
	// Mono is a monotonic reading from an arbitrary start.
	Mono() time.Duration
	After(d time.Duration) <-chan time.Time
}

type realClock struct{ start time.Time }

func newRealClock() realClock { return realClock{start: time.Now()} }

func (realClock) Now() time.Time                         { return time.Now().Round(0) }
func (c realClock) Mono() time.Duration                  { return time.Since(c.start) }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// relevantMessage reports whether a netlink message can change what Snapshot
// returns. Messages about routes of other tables and about the kernel's own
// bookkeeping, about link-local and multicast addresses and prefixes, and about
// interfaces that are not a way out to the internet are noise. lookup resolves
// an interface index; an interface that is not known counts as relevant,
// because it may have been an uplink that just went away. The one exception to
// the interfaces: a route that another program adds over a tunnel device (see
// relevantRoute).
func relevantMessage(m rtMessage, lookup func(index uint32) (link, bool)) bool {
	switch {
	case m.Route != nil:
		return relevantRoute(*m.Route, lookup)
	case m.Link != nil:
		return linkFromMessage(*m.Link).Uplink
	case m.Addr != nil:
		return relevantAddr(*m.Addr, lookup)
	}
	return false
}

func relevantRoute(r routeMsg, lookup func(index uint32) (link, bool)) bool {
	if !r.listed() || r.Dst.IsLinkLocalUnicast() || r.Dst.IsMulticast() {
		return false
	}
	indexes := []uint32{r.OIf}
	if len(r.NextHops) > 0 {
		indexes = indexes[:0]
		for _, hop := range r.NextHops {
			indexes = append(indexes, hop.OIf)
		}
	}
	// A route without an interface, such as a blackhole, says nothing that rules
	// it out.
	if indexes[0] == 0 && len(indexes) == 1 {
		return true
	}
	// A route over a tunnel device matters when another program added it: that is
	// a VPN connecting, and its route may outrank one of ours, which the
	// Reconciler reports. The routes of ours and the kernel's own say nothing new.
	if r.static() && r.Protocol != RouteProtocol {
		return true
	}
	return slices.ContainsFunc(indexes, func(index uint32) bool {
		l, known := lookup(index)
		return !known || l.Uplink
	})
}

func relevantAddr(a addrMsg, lookup func(index uint32) (link, bool)) bool {
	if prefix, ok := a.prefix(); ok && (prefix.Addr().IsLinkLocalUnicast() || prefix.Addr().IsMulticast()) {
		return false
	}
	l, known := lookup(a.Index)
	return !known || l.Uplink
}

// messagesRelevant parses what one read of the netlink socket returned and
// reports whether it can change the network. A message that does not parse
// counts as a change: there is no telling, and a spurious Change costs one
// Snapshot.
func messagesRelevant(b []byte, links *linkCache, log *slog.Logger) bool {
	msgs, err := parseMessages(b)
	if err != nil {
		log.Warn("unreadable netlink message; treating it as a change", "err", err)
		return true
	}
	lookup := func(index uint32) (link, bool) {
		l, ok, err := links.lookup(index)
		if err != nil {
			log.Warn("resolve interface index", "index", index, "err", err)
		}
		return l, ok
	}
	return slices.ContainsFunc(msgs, func(m rtMessage) bool { return relevantMessage(m, lookup) })
}

// burst merges triggers into one Change. Times are monotonic readings.
type burst struct {
	pending     bool
	first, last time.Duration
	reason      osnet.ChangeReason
}

func (b *burst) add(now time.Duration, reason osnet.ChangeReason) {
	if !b.pending {
		*b = burst{pending: true, first: now, reason: reason}
	} else if reasonRank(reason) > reasonRank(b.reason) {
		b.reason = reason
	}
	b.last = now
}

// due is when the pending Change should go out.
func (b *burst) due() time.Duration {
	return min(b.last+burstQuiet, b.first+burstMax)
}

func (b *burst) take() osnet.ChangeReason {
	b.pending = false
	return b.reason
}

// reasonRank lets the most telling reason of a burst win: a wake says more than
// a route change, which says more than a heartbeat.
func reasonRank(r osnet.ChangeReason) int {
	switch r {
	case osnet.ChangeWake:
		return 3
	case osnet.ChangeRoute:
		return 2
	case osnet.ChangeHeartbeat:
		return 1
	}
	return 0
}

type wakeSample struct {
	wall time.Time
	mono time.Duration
}

// wakeDetector notices that the machine slept between two samples: the wall
// clock ran ahead of the monotonic clock. The first sample only sets the
// baseline.
type wakeDetector struct {
	last   wakeSample
	primed bool
}

func (d *wakeDetector) woke(s wakeSample) bool {
	last, primed := d.last, d.primed
	d.last, d.primed = s, true
	if !primed {
		return false
	}
	return s.wall.Sub(last.wall)-(s.mono-last.mono) > wakeGap
}
