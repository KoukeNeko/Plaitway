package windows

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

func TestRouteNotificationRelevant(t *testing.T) {
	for dst, want := range map[string]bool{
		"0.0.0.0/0":          true, // the default route is what matters most
		"::/0":               true,
		"10.0.0.0/8":         true,
		"203.0.113.7/32":     true,
		"2001:db8::/32":      true,
		"192.168.1.0/24":     true,
		"224.0.0.0/4":        false, // multicast: Windows adds one per interface
		"ff00::/8":           false,
		"255.255.255.255/32": false, // limited broadcast, one per interface
		"169.254.0.0/16":     false, // DHCP has not answered
		"fe80::/64":          false,
		"fe80::1234/128":     false,
		"127.0.0.0/8":        false,
		"::1/128":            false,
	} {
		if got := routeNotificationRelevant(netip.MustParsePrefix(dst)); got != want {
			t.Errorf("route %s: relevant = %v, want %v", dst, got, want)
		}
	}
	if routeNotificationRelevant(netip.Prefix{}) {
		t.Error("an invalid prefix is relevant")
	}
}

func TestAddressNotificationRelevant(t *testing.T) {
	for addr, want := range map[string]bool{
		"192.168.1.101":                  true,
		"10.6.0.2":                       true,
		"2001:b011:c006:bf71:eadc::859f": true,
		"fe80::316b:5a55:6fe7:c264":      false,
		"169.254.188.17":                 false,
		"127.0.0.1":                      false,
		"::1":                            false,
		"224.0.0.251":                    false,
		"ff02::fb":                       false,
	} {
		if got := addressNotificationRelevant(netip.MustParseAddr(addr)); got != want {
			t.Errorf("address %s: relevant = %v, want %v", addr, got, want)
		}
	}
	if addressNotificationRelevant(netip.Addr{}) {
		t.Error("the zero address is relevant")
	}
}

func TestBurst(t *testing.T) {
	var b burst
	b.add(0, osnet.ChangeHeartbeat)
	if got := b.due(); got != burstQuiet {
		t.Errorf("due after one trigger = %v, want %v", got, burstQuiet)
	}
	b.add(200*time.Millisecond, osnet.ChangeRoute)
	if got := b.due(); got != 200*time.Millisecond+burstQuiet {
		t.Errorf("a new trigger must restart the quiet period: due = %v", got)
	}
	for i := 3; i < 40; i++ {
		b.add(time.Duration(i)*100*time.Millisecond, osnet.ChangeHeartbeat)
	}
	if got := b.due(); got != burstMax {
		t.Errorf("a burst that never calms down is sent at the maximum: due = %v, want %v", got, burstMax)
	}
	if got := b.take(); got != osnet.ChangeRoute {
		t.Errorf("reason = %q, want the most telling one (route) and not the last", got)
	}
	b.add(10*time.Second, osnet.ChangeHeartbeat)
	if got := b.take(); got != osnet.ChangeHeartbeat {
		t.Errorf("a new burst starts over: reason = %q", got)
	}
}

func TestReasonRank(t *testing.T) {
	ranks := []osnet.ChangeReason{osnet.ChangeManual, osnet.ChangeHeartbeat, osnet.ChangeRoute, osnet.ChangeWake}
	for i := 1; i < len(ranks); i++ {
		if reasonRank(ranks[i]) <= reasonRank(ranks[i-1]) {
			t.Errorf("%q does not rank above %q", ranks[i], ranks[i-1])
		}
	}
}

// mergeHarness runs netMonitor.merge on a manual clock.
type mergeHarness struct {
	clk          *manualClock
	routeChanged chan struct{}
	woke         chan struct{}
	out          chan osnet.Change
	cancel       context.CancelFunc
	done         chan struct{}
}

func startMerge(t *testing.T) *mergeHarness {
	t.Helper()
	h := &mergeHarness{
		clk:          newManualClock(),
		routeChanged: make(chan struct{}), // unbuffered: a send returns once the loop has taken it
		woke:         make(chan struct{}),
		out:          make(chan osnet.Change),
		done:         make(chan struct{}),
	}
	m := &netMonitor{log: discardLog(), clk: h.clk}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() {
		defer close(h.done)
		m.merge(ctx, h.routeChanged, h.woke, h.out)
	}()
	t.Cleanup(func() {
		cancel()
		<-h.done
	})
	h.clk.waitPending(t, heartbeatInterval) // the loop has started
	return h
}

// route reports a change and waits until the loop has read the clock for it, so
// the test can move the clock on.
func (h *mergeHarness) route() {
	before := h.clk.reads()
	h.routeChanged <- struct{}{}
	waitForReads(h.clk, before)
}

func (h *mergeHarness) wake() {
	before := h.clk.reads()
	h.woke <- struct{}{}
	waitForReads(h.clk, before)
}

func waitForReads(clk *manualClock, before int) {
	for deadline := time.Now().Add(2 * time.Second); clk.reads() == before; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			panic("the loop did not take the notification")
		}
	}
}

func (h *mergeHarness) expect(t *testing.T, reason osnet.ChangeReason) osnet.Change {
	t.Helper()
	select {
	case got := <-h.out:
		if got.Reason != reason {
			t.Fatalf("change reason %q, want %q", got.Reason, reason)
		}
		return got
	case <-time.After(2 * time.Second):
		t.Fatalf("no %q change", reason)
		return osnet.Change{}
	}
}

func (h *mergeHarness) expectNothing(t *testing.T) {
	t.Helper()
	select {
	case got := <-h.out:
		t.Fatalf("unexpected change %+v", got)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestMergeDebouncesABurst(t *testing.T) {
	h := startMerge(t)
	h.route() // t = 0
	h.clk.advance(200 * time.Millisecond)
	h.route() // t = 200 ms: the quiet period starts over
	h.clk.advance(100 * time.Millisecond)
	h.clk.fire(t, burstQuiet) // the first timer is due at 300 ms, the burst is not
	h.expectNothing(t)

	h.clk.advance(200 * time.Millisecond) // t = 500 ms: 300 ms after the last notification
	h.clk.fire(t, 200*time.Millisecond)
	got := h.expect(t, osnet.ChangeRoute)
	if want := time.Date(2026, 10, 7, 9, 0, 0, 500_000_000, time.UTC); !got.At.Equal(want) {
		t.Errorf("change at %v, want %v", got.At, want)
	}
	h.expectNothing(t)
}

func TestMergeFlushesALongBurstAtTheMaximum(t *testing.T) {
	h := startMerge(t)
	h.route()
	// A notification every 100 ms keeps the burst alive, but not past 2 s.
	for range 19 {
		h.clk.advance(100 * time.Millisecond)
		h.route()
	}
	h.clk.advance(100 * time.Millisecond) // t = 2 s; 300 ms after the last one is 2.2 s
	h.clk.fire(t, burstQuiet)
	h.expect(t, osnet.ChangeRoute)
}

func TestMergeSendsHeartbeats(t *testing.T) {
	h := startMerge(t)
	for range 2 {
		h.clk.advance(heartbeatInterval)
		h.clk.fire(t, heartbeatInterval)
		h.clk.waitPending(t, burstQuiet)
		h.clk.advance(burstQuiet)
		h.clk.fire(t, burstQuiet)
		h.expect(t, osnet.ChangeHeartbeat)
	}
}

// After a wake DHCP is often not done yet, so a second change follows.
func TestMergeReportsAWakeTwice(t *testing.T) {
	h := startMerge(t)
	h.wake()
	h.clk.waitPending(t, wakeFollowUp)
	h.clk.advance(burstQuiet)
	h.clk.fire(t, burstQuiet)
	first := h.expect(t, osnet.ChangeWake)

	h.clk.advance(wakeFollowUp - burstQuiet)
	h.clk.fire(t, wakeFollowUp)
	h.clk.waitPending(t, burstQuiet)
	h.clk.advance(burstQuiet)
	h.clk.fire(t, burstQuiet)
	second := h.expect(t, osnet.ChangeWake)
	if got := second.At.Sub(first.At); got != wakeFollowUp {
		t.Errorf("second wake change %v after the first, want %v", got, wakeFollowUp)
	}
	h.expectNothing(t)
}

func TestMergeReportsAWakeThatCameWithARouteChangeAsAWake(t *testing.T) {
	h := startMerge(t)
	h.route()
	h.wake()
	h.clk.waitPending(t, wakeFollowUp) // the wake is in the burst now
	h.clk.advance(burstQuiet)
	h.clk.fire(t, burstQuiet) // the timer of the route change
	h.expect(t, osnet.ChangeWake)
}

func TestMergeStopsWhileAChangeIsWaitingForItsReader(t *testing.T) {
	h := startMerge(t)
	h.route()
	h.clk.advance(burstQuiet)
	h.clk.fire(t, burstQuiet) // the loop now tries to hand the change over; nobody reads
	h.cancel()
	select {
	case <-h.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the loop did not stop")
	}
}

// eventsMonitor is a netMonitor whose registrations are the test's.
func eventsMonitor(clk *manualClock, changes, wake func(context.Context, func()) error) *netMonitor {
	return &netMonitor{log: discardLog(), clk: clk, watchChanges: changes, watchWake: wake}
}

// holdUntilDone is a registration that works and waits for ctx.
func holdUntilDone(ctx context.Context, _ func()) error {
	<-ctx.Done()
	return nil
}

func TestEventsReportsNotifications(t *testing.T) {
	clk := newManualClock()
	changed := make(chan func(), 1)
	woke := make(chan func(), 1)
	m := eventsMonitor(clk,
		func(ctx context.Context, notify func()) error { changed <- notify; <-ctx.Done(); return nil },
		func(ctx context.Context, notify func()) error { woke <- notify; <-ctx.Done(); return nil })
	ctx, cancel := context.WithCancel(context.Background())
	events := m.Events(ctx)
	notifyChange, notifyWake := <-changed, <-woke

	notifyChange()
	clk.waitPending(t, burstQuiet)
	clk.advance(burstQuiet)
	clk.fire(t, burstQuiet)
	if got := receive(t, events); got.Reason != osnet.ChangeRoute {
		t.Errorf("reason %q, want route", got.Reason)
	}

	notifyWake()
	clk.waitPending(t, wakeFollowUp)
	clk.advance(burstQuiet)
	clk.fire(t, burstQuiet)
	if got := receive(t, events); got.Reason != osnet.ChangeWake {
		t.Errorf("reason %q, want wake", got.Reason)
	}

	cancel()
	select {
	case _, open := <-events:
		if open {
			t.Error("a change after cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the channel stays open after cancel")
	}
}

func receive(t *testing.T, events <-chan osnet.Change) osnet.Change {
	t.Helper()
	select {
	case got := <-events:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("no change")
		return osnet.Change{}
	}
}

// The system's thread that calls the callback must not wait for whoever reads
// the changes.
func TestEventsNotifyNeverBlocks(t *testing.T) {
	clk := newManualClock()
	changed := make(chan func(), 1)
	m := eventsMonitor(clk,
		func(ctx context.Context, notify func()) error { changed <- notify; <-ctx.Done(); return nil },
		holdUntilDone)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = m.Events(ctx) // nobody reads the changes
	notify := <-changed

	notify()
	clk.waitPending(t, burstQuiet)
	clk.advance(burstQuiet)
	clk.fire(t, burstQuiet) // the loop is now stuck handing the change over

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			notify()
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("notify blocked")
	}
}

// When a registration fails, notifications may have been lost: that is a change
// in itself, and the registration is made again.
func TestEventsRegistersAgainAfterAFailure(t *testing.T) {
	clk := newManualClock()
	var mu sync.Mutex
	attempts := 0
	registered := make(chan struct{})
	m := eventsMonitor(clk,
		func(ctx context.Context, _ func()) error {
			mu.Lock()
			attempts++
			n := attempts
			mu.Unlock()
			if n == 1 {
				return errors.New("NotifyRouteChange2: out of resources")
			}
			close(registered)
			<-ctx.Done()
			return nil
		},
		holdUntilDone)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := m.Events(ctx)

	clk.waitPending(t, burstQuiet) // the failure was reported as a change
	clk.advance(burstQuiet)
	clk.fire(t, burstQuiet)
	if got := receive(t, events); got.Reason != osnet.ChangeRoute {
		t.Errorf("reason %q, want route", got.Reason)
	}
	clk.fire(t, watchRetry)
	select {
	case <-registered:
	case <-time.After(2 * time.Second):
		t.Fatal("the registration was not made again")
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
}

// A machine without the power notification API still gets route changes and the
// heartbeat; the failing registration is retried but never stops the others.
func TestEventsWakeFailureDoesNotStopRouteChanges(t *testing.T) {
	clk := newManualClock()
	changed := make(chan func(), 1)
	m := eventsMonitor(clk,
		func(ctx context.Context, notify func()) error { changed <- notify; <-ctx.Done(); return nil },
		func(context.Context, func()) error {
			return errors.New("PowerRegisterSuspendResumeNotification: not supported")
		})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := m.Events(ctx)
	notify := <-changed

	notify()
	clk.waitPending(t, watchRetry)
	clk.waitPending(t, burstQuiet)
	clk.advance(burstQuiet)
	// The failed wake registration also signalled a change; both are in one burst.
	clk.fire(t, burstQuiet)
	if got := receive(t, events); got.Reason != osnet.ChangeRoute {
		t.Errorf("reason %q, want route", got.Reason)
	}
}
