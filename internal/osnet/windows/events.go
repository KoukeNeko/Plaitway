package windows

import (
	"net/netip"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

const (
	// A burst of notifications becomes one Change: it is sent after burstQuiet
	// without a new notification, and at the latest burstMax after the first one.
	burstQuiet = 300 * time.Millisecond
	burstMax   = 2 * time.Second

	heartbeatInterval = 60 * time.Second
	// DHCP often finishes after the wake, so a wake is reported again later.
	wakeFollowUp = 4 * time.Second

	// watchRetry is how long a failed registration waits before it is tried again.
	watchRetry = 3 * time.Second
)

// clock is the time the monitor lives by.
type clock interface {
	// Now is the wall clock.
	Now() time.Time
	// Mono is a monotonic reading from an arbitrary start.
	Mono() time.Duration
	After(d time.Duration) <-chan time.Time
}

type realClock struct{ start time.Time }

func newRealClock() realClock { return realClock{start: time.Now()} }

func (realClock) Now() time.Time                         { return time.Now() }
func (c realClock) Mono() time.Duration                  { return time.Since(c.start) }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// routeNotificationRelevant reports whether a change of the route to dst can
// alter what Snapshot returns. Multicast, broadcast and link-local routes are
// the system's own housekeeping: Windows adds and removes them for every
// interface that goes up or down.
func routeNotificationRelevant(dst netip.Prefix) bool {
	return dst.IsValid() && addressNotificationRelevant(dst.Addr()) && dst.Addr() != broadcastV4
}

// addressNotificationRelevant reports whether a change of this address can
// alter what Snapshot returns. IPv6 link-local addresses appear for every
// adapter and again whenever one is regenerated, and an IPv4 link-local one
// (169.254/16) only says that DHCP has not answered.
func addressNotificationRelevant(addr netip.Addr) bool {
	return addr.IsValid() && !addr.IsLinkLocalUnicast() && !addr.IsMulticast() && !addr.IsLoopback()
}

var broadcastV4 = netip.AddrFrom4([4]byte{255, 255, 255, 255})

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
