package macos

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func fixtureName(index int) (string, bool) {
	name, ok := fixtureNames[index]
	return name, ok
}

func TestRelevantMessage(t *testing.T) {
	route := func(name string, typ int, edit func(*rtMessage)) func(testing.TB) rtMessage {
		return func(t testing.TB) rtMessage {
			m := fixtureMessage(t, ribMessages, name)
			m.Type = typ
			if edit != nil {
				edit(&m)
			}
			return m
		}
	}
	iface := func(name string) func(testing.TB) rtMessage {
		return func(t testing.TB) rtMessage { return fixtureMessage(t, ifaceMessages, name) }
	}
	tests := []struct {
		name string
		msg  func(testing.TB) rtMessage
		want bool
	}{
		{"lookup that found no route (RTM_MISS)", func(t testing.TB) rtMessage { return fixtureMessage(t, missMessage, "rtm-miss") }, false},
		{"route added", route("v4-default-en0", rtmAdd, nil), true},
		{"route deleted", route("v4-default-en0", rtmDelete, nil), true},
		{"route changed", route("v4-net24-en0", rtmChange, nil), true},
		{"route lookup of another process (RTM_GET)", route("v4-default-en0", rtmGet, nil), false},
		{"link-local route", route("v4-linklocal-en0", rtmAdd, nil), false},
		{"IPv6 link-local route", route("v6-linklocal-net64-en0", rtmAdd, nil), false},
		{"multicast route", route("v4-multicast-en0", rtmAdd, nil), false},
		{"IPv6 multicast route", route("v6-multicast8-lo0", rtmDelete, func(m *rtMessage) { m.Index = 14 }), false},
		{"neighbor entry", route("v4-default-en0", rtmAdd, func(m *rtMessage) { m.Flags |= rtfLLInfo | rtfWasCloned }), false},
		{"route on a tunnel", route("v4-net24-utun10", rtmAdd, nil), false},
		{"route on loopback", route("v4-net8-lo0", rtmAdd, nil), false},
		{"route on awdl", route("v4-default-en0", rtmAdd, func(m *rtMessage) { m.Index = 16 }), false},
		{"route on a bridge", route("v4-default-scoped-bridge100", rtmAdd, nil), false},
		{"route on an interface that is gone", route("v4-default-en0", rtmDelete, func(m *rtMessage) { m.Index = 99 }), true},
		{"address added to en0", iface("newaddr-en0-v4"), true},
		{"link-local address added to en0", iface("newaddr-en0-linklocal"), false},
		{"address on loopback", iface("newaddr-lo0-v4"), false},
		{"address on a tunnel", iface("newaddr-utun10-v4"), false},
		{"en0 changed state", iface("ifinfo-en0"), true},
		{"lo0 changed state", iface("ifinfo-lo0"), false},
		{"awdl0 changed state", iface("ifinfo-awdl0"), false},
		{"bridge100 changed state", iface("ifinfo-bridge100"), false},
		{"utun10 changed state", iface("ifinfo-utun10"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := relevantMessage(tt.msg(t), fixtureName); got != tt.want {
				t.Errorf("relevant = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRouteMessagesRelevant(t *testing.T) {
	var logged bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logged, nil))
	names := nameCache{names: fixtureNames}

	if routeMessagesRelevant(fixture(t, missMessage, "rtm-miss"), &names, log) {
		t.Error("RTM_MISS is relevant")
	}
	if !routeMessagesRelevant(fixture(t, ifaceMessages, "newaddr-en0-v4"), &names, log) {
		t.Error("a new address on en0 is not relevant")
	}
	if logged.Len() != 0 {
		t.Errorf("logged %q for good messages", logged.String())
	}
	if !routeMessagesRelevant([]byte{1, 2, 3}, &names, log) {
		t.Error("an unreadable message is not treated as a change")
	}
	if !strings.Contains(logged.String(), "unreadable") {
		t.Errorf("unreadable message was not logged: %q", logged.String())
	}
}

func TestNameCache(t *testing.T) {
	loads := 0
	cache := nameCache{load: func() (map[int]string, error) {
		loads++
		return map[int]string{14: "en0", 50: "utun10"}, nil
	}}
	for range 3 {
		if name, ok, err := cache.lookup(14); name != "en0" || !ok || err != nil {
			t.Fatalf("lookup(14) = %q, %v, %v", name, ok, err)
		}
	}
	if loads != 1 {
		t.Errorf("loaded %d times for a known index, want 1", loads)
	}
	if _, ok, err := cache.lookup(99); ok || err != nil {
		t.Errorf("lookup(99) = %v, %v", ok, err)
	}
	if loads != 2 {
		t.Errorf("an unknown index was not looked for in the kernel's list")
	}

	failing := nameCache{load: func() (map[int]string, error) { return nil, errors.New("boom") }}
	if _, ok, err := failing.lookup(1); ok || err == nil {
		t.Errorf("lookup with failing load = %v, %v", ok, err)
	}

	// An interface that went away keeps its name.
	gone := nameCache{names: map[int]string{7: "en5"}, load: func() (map[int]string, error) { return map[int]string{}, nil }}
	if name, ok, _ := gone.lookup(7); name != "en5" || !ok {
		t.Errorf("lookup of cached interface = %q, %v", name, ok)
	}
}

func TestBurst(t *testing.T) {
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	t.Run("waits for quiet", func(t *testing.T) {
		var b burst
		b.add(ms(1000), osnet.ChangeRoute)
		if got, want := b.due(), ms(1300); got != want {
			t.Errorf("due %v, want %v", got, want)
		}
		b.add(ms(1200), osnet.ChangeRoute)
		if got, want := b.due(), ms(1500); got != want {
			t.Errorf("due after a second message %v, want %v", got, want)
		}
	})
	t.Run("never waits longer than the maximum", func(t *testing.T) {
		var b burst
		for at := 0; at <= 5000; at += 100 {
			b.add(ms(at), osnet.ChangeRoute)
		}
		if got, want := b.due(), ms(2000); got != want {
			t.Errorf("due %v, want %v", got, want)
		}
	})
	t.Run("starts over after a flush", func(t *testing.T) {
		var b burst
		b.add(ms(0), osnet.ChangeWake)
		if got := b.take(); got != osnet.ChangeWake {
			t.Errorf("take = %v", got)
		}
		b.add(ms(10000), osnet.ChangeHeartbeat)
		if got, want := b.due(), ms(10300); got != want {
			t.Errorf("due %v, want %v", got, want)
		}
		if got := b.take(); got != osnet.ChangeHeartbeat {
			t.Errorf("reason leaked from the last burst: %v", got)
		}
	})
	t.Run("the most telling reason wins", func(t *testing.T) {
		orders := [][]osnet.ChangeReason{
			{osnet.ChangeHeartbeat, osnet.ChangeRoute, osnet.ChangeWake},
			{osnet.ChangeWake, osnet.ChangeRoute, osnet.ChangeHeartbeat},
			{osnet.ChangeRoute, osnet.ChangeWake, osnet.ChangeHeartbeat},
		}
		for _, order := range orders {
			var b burst
			for _, r := range order {
				b.add(0, r)
			}
			if got := b.take(); got != osnet.ChangeWake {
				t.Errorf("%v merged to %v, want wake", order, got)
			}
		}
		var b burst
		b.add(0, osnet.ChangeHeartbeat)
		b.add(0, osnet.ChangeRoute)
		if got := b.take(); got != osnet.ChangeRoute {
			t.Errorf("heartbeat and route merged to %v, want route", got)
		}
	})
}

func TestWakeDetector(t *testing.T) {
	wall := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	slept := time.Date(2026, 10, 6, 22, 0, 0, 0, time.UTC)
	at := func(sinceStart, wallAhead time.Duration, sleepTime time.Time) wakeSample {
		return wakeSample{wall: wall.Add(sinceStart + wallAhead), mono: sinceStart, sleepTime: sleepTime}
	}
	steps := []struct {
		name   string
		sample wakeSample
		want   bool
	}{
		{"the first sample is the baseline", at(0, 0, slept), false},
		{"time passes", at(5*time.Second, 0, slept), false},
		{"the kernel's last sleep moved", at(10*time.Second, 0, slept.Add(9*time.Hour)), true},
		{"time passes again", at(15*time.Second, 0, slept.Add(9*time.Hour)), false},
		{"the wall clock ran ahead: the machine slept", at(20*time.Second, 40*time.Minute, slept.Add(9*time.Hour)), true},
		{"and keeps the offset without waking again", at(25*time.Second, 40*time.Minute, slept.Add(9*time.Hour)), false},
		{"a small clock step is not a sleep", at(30*time.Second, 40*time.Minute+wakeGap, slept.Add(9*time.Hour)), false},
		{"a step past the threshold is", at(35*time.Second, 40*time.Minute+2*wakeGap+1, slept.Add(9*time.Hour)), true},
	}
	var d wakeDetector
	for _, step := range steps {
		if got := d.woke(step.sample); got != step.want {
			t.Errorf("%s: woke = %v, want %v", step.name, got, step.want)
		}
	}
}

// manualClock is a clock that moves only when the test says so, and timers that
// fire only when the test fires them: fire(d) fires the oldest timer set for d,
// waiting for the code under test to set it.
type manualClock struct {
	mu        sync.Mutex
	wall      time.Time
	mono      time.Duration
	monoReads int
	timers    []manualTimer
}

type manualTimer struct {
	d  time.Duration
	ch chan time.Time
}

func newManualClock() *manualClock {
	return &manualClock{wall: time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wall
}

func (c *manualClock) Mono() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.monoReads++
	return c.mono
}

// reads is how often Mono was read: a way to know that the code under test has
// looked at the clock.
func (c *manualClock) reads() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.monoReads
}

func (c *manualClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := manualTimer{d: d, ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, t)
	return t.ch
}

// advance moves both clocks.
func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mono += d
	c.wall = c.wall.Add(d)
}

// suspend moves the wall clock only, like a machine that sleeps.
func (c *manualClock) suspend(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wall = c.wall.Add(d)
}

func (c *manualClock) fire(t testing.TB, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		c.mu.Lock()
		for i, timer := range c.timers {
			if timer.d == d {
				c.timers = slices.Delete(c.timers, i, i+1)
				timer.ch <- c.wall
				c.mu.Unlock()
				return
			}
		}
		pending := make([]time.Duration, len(c.timers))
		for i, timer := range c.timers {
			pending[i] = timer.d
		}
		c.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("no timer set for %v; pending: %v", d, pending)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitPending waits until a timer is set for d, without firing it.
func (c *manualClock) waitPending(t testing.TB, d time.Duration) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		c.mu.Lock()
		found := slices.ContainsFunc(c.timers, func(timer manualTimer) bool { return timer.d == d })
		c.mu.Unlock()
		if found {
			return
		}
	}
	t.Fatalf("no timer set for %v", d)
}

// mergeHarness runs netMonitor.merge on a manual clock.
type mergeHarness struct {
	clk          *manualClock
	routeChanged chan struct{}
	out          chan osnet.Change
	sleepTime    func() (time.Time, error)
	setSleepTime func(time.Time)
	cancel       context.CancelFunc
	done         chan struct{}
}

func startMerge(t *testing.T) *mergeHarness {
	t.Helper()
	h := &mergeHarness{
		clk:          newManualClock(),
		routeChanged: make(chan struct{}), // unbuffered: a send returns once the loop has taken it
		out:          make(chan osnet.Change),
		done:         make(chan struct{}),
	}
	var mu sync.Mutex
	sleepTime := time.Date(2026, 10, 6, 22, 0, 0, 0, time.UTC)
	h.setSleepTime = func(v time.Time) { mu.Lock(); sleepTime = v; mu.Unlock() }
	m := &netMonitor{
		log: discardLog(),
		clk: h.clk,
		sleepTime: func() (time.Time, error) {
			mu.Lock()
			defer mu.Unlock()
			return sleepTime, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() {
		defer close(h.done)
		m.merge(ctx, h.routeChanged, h.out)
	}()
	t.Cleanup(func() {
		cancel()
		<-h.done
	})
	h.clk.waitPending(t, wakePollInterval) // the loop has taken its baseline
	return h
}

// route reports a route change and waits until the loop has read the clock
// for it, so the test can move the clock on.
func (h *mergeHarness) route() {
	before := h.clk.reads()
	h.routeChanged <- struct{}{}
	for deadline := time.Now().Add(2 * time.Second); h.clk.reads() == before; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			panic("the loop did not take the route change")
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

func TestMergeDebouncesARouteBurst(t *testing.T) {
	h := startMerge(t)
	h.route() // t = 0
	h.clk.advance(200 * time.Millisecond)
	h.route() // t = 200 ms: the quiet period starts over
	h.clk.advance(100 * time.Millisecond)
	h.clk.fire(t, burstQuiet) // the first timer is due at 300 ms, the burst is not
	h.expectNothing(t)

	h.clk.advance(200 * time.Millisecond) // t = 500 ms: 300 ms after the last message
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
	// A message every 100 ms keeps the burst alive, but not past 2 s.
	for range 19 {
		h.clk.advance(100 * time.Millisecond)
		h.route()
	}
	h.clk.advance(100 * time.Millisecond) // t = 2 s; 300 ms after the last message is 2.2 s
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
	h.setSleepTime(time.Date(2026, 10, 7, 8, 59, 0, 0, time.UTC))
	h.clk.advance(wakePollInterval)
	h.clk.fire(t, wakePollInterval)
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

	// The next poll sees the same sleep time: nothing.
	h.clk.advance(wakePollInterval)
	h.clk.fire(t, wakePollInterval)
	h.expectNothing(t)
}

func TestMergeNoticesSleepFromTheClocks(t *testing.T) {
	h := startMerge(t)
	h.clk.suspend(2 * time.Hour) // the wall clock ran, the monotonic clock did not
	h.clk.advance(wakePollInterval)
	h.clk.fire(t, wakePollInterval)
	h.clk.waitPending(t, wakeFollowUp)
	h.clk.advance(burstQuiet)
	h.clk.fire(t, burstQuiet)
	h.expect(t, osnet.ChangeWake)
}

func TestMergeReportsAWakeThatCameWithARouteChangeAsAWake(t *testing.T) {
	h := startMerge(t)
	h.route()
	h.setSleepTime(time.Date(2026, 10, 7, 8, 59, 0, 0, time.UTC))
	h.clk.advance(wakePollInterval)
	h.clk.fire(t, wakePollInterval)
	h.clk.waitPending(t, wakeFollowUp) // the wake is in the burst now
	h.clk.fire(t, burstQuiet)          // the timer of the route change
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

func newEventsMonitor(clk *manualClock, watch func(ctx context.Context, log *slog.Logger, changed func()) error) *netMonitor {
	return &netMonitor{
		log:         discardLog(),
		clk:         clk,
		sleepTime:   func() (time.Time, error) { return time.Time{}, nil },
		watchRoutes: watch,
	}
}

func TestEventsReportsRouteSocketMessages(t *testing.T) {
	clk := newManualClock()
	started := make(chan func(), 1)
	m := newEventsMonitor(clk, func(ctx context.Context, _ *slog.Logger, changed func()) error {
		started <- changed
		<-ctx.Done()
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	events := m.Events(ctx)
	changed := <-started

	changed()
	clk.waitPending(t, burstQuiet)
	clk.advance(burstQuiet)
	clk.fire(t, burstQuiet)
	select {
	case got := <-events:
		if got.Reason != osnet.ChangeRoute {
			t.Errorf("reason %q", got.Reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no change")
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

// The reader of the route socket must not wait for whoever reads the changes.
func TestEventsRouteSocketReaderNeverBlocks(t *testing.T) {
	clk := newManualClock()
	started := make(chan func(), 1)
	m := newEventsMonitor(clk, func(ctx context.Context, _ *slog.Logger, changed func()) error {
		started <- changed
		<-ctx.Done()
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = m.Events(ctx) // nobody reads the changes
	changed := <-started

	changed()
	clk.waitPending(t, burstQuiet)
	clk.advance(burstQuiet)
	clk.fire(t, burstQuiet) // the loop is now stuck handing the change over

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			changed()
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("changed blocked")
	}
}

// When the route socket fails, messages may have been lost: that is a change
// in itself, and the socket is opened again.
func TestEventsReopensTheRouteSocket(t *testing.T) {
	clk := newManualClock()
	var mu sync.Mutex
	opened := 0
	reopened := make(chan struct{})
	m := newEventsMonitor(clk, func(ctx context.Context, _ *slog.Logger, changed func()) error {
		mu.Lock()
		opened++
		n := opened
		mu.Unlock()
		if n == 1 {
			return errors.New("read routing socket: no buffer space")
		}
		close(reopened)
		<-ctx.Done()
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := m.Events(ctx)

	clk.waitPending(t, burstQuiet) // the failure was reported as a change
	clk.advance(burstQuiet)
	clk.fire(t, burstQuiet)
	if got := <-events; got.Reason != osnet.ChangeRoute {
		t.Errorf("reason %q", got.Reason)
	}
	clk.fire(t, routeSocketRetry)
	select {
	case <-reopened:
	case <-time.After(2 * time.Second):
		t.Fatal("the route socket was not opened again")
	}
}
