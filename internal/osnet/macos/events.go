package macos

import (
	"log/slog"
	"slices"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

const (
	// A burst of routing messages becomes one Change: it is sent after burstQuiet
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

	routeSocketRetry = 3 * time.Second
)

// clock is the time the monitor lives by. The monotonic clock does not advance
// while the machine sleeps; the wall clock does, and the difference is how a
// wake is noticed.
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

// relevantMessage reports whether a routing message can change what Snapshot
// returns. Messages about routes of neighbors and clones, about link-local and
// multicast addresses, and about interfaces that are not a way out to the
// internet are noise. name resolves an interface index; an interface that is
// not known counts as relevant, because it may have been a physical one that
// just went away.
func relevantMessage(m rtMessage, name func(index int) (string, bool)) bool {
	switch m.Type {
	case rtmAdd, rtmDelete, rtmChange:
		if m.Flags&(rtfLLInfo|rtfWasCloned|rtfMulticast|rtfBroadcast) != 0 {
			return false
		}
		if m.has(rtaDst) && isLinkLocalOrMulticast(m.Addrs[rtaxDst]) {
			return false
		}
	case rtmNewAddr, rtmDelAddr:
		if m.has(rtaIFA) && isLinkLocalOrMulticast(m.Addrs[rtaxIFA]) {
			return false
		}
	case rtmIfInfo:
	default:
		return false
	}
	if iface, ok := name(m.Index); ok {
		return isPhysical(iface)
	}
	return true
}

// routeMessagesRelevant parses what one read of the routing socket returned
// and reports whether it can change the network. A message that does not parse
// counts as a change: there is no telling, and a spurious Change costs one
// Snapshot.
func routeMessagesRelevant(b []byte, names *nameCache, log *slog.Logger) bool {
	msgs, err := parseMessages(b)
	if err != nil {
		log.Warn("unreadable routing message; treating it as a change", "err", err)
		return true
	}
	name := func(index int) (string, bool) {
		name, ok, err := names.lookup(index)
		if err != nil {
			log.Warn("resolve interface index", "index", index, "err", err)
		}
		return name, ok
	}
	return slices.ContainsFunc(msgs, func(m rtMessage) bool { return relevantMessage(m, name) })
}

func isLinkLocalOrMulticast(sa []byte) bool {
	addr, _, ok := ipFromSockaddr(sa)
	return ok && (addr.IsLinkLocalUnicast() || addr.IsMulticast())
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
	wall      time.Time
	mono      time.Duration
	sleepTime time.Time // kern.sleeptime
}

// wakeDetector notices that the machine slept between two samples: either the
// kernel's last-sleep time changed, or the wall clock ran ahead of the
// monotonic clock. The first sample only sets the baseline.
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
	if !s.sleepTime.Equal(last.sleepTime) {
		return true
	}
	return s.wall.Sub(last.wall)-(s.mono-last.mono) > wakeGap
}
