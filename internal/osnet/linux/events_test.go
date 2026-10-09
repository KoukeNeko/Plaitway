package linux

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// The protocols ip route gives the routes that a person or a program adds.
const (
	protoBoot   = 3
	protoStatic = 4
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fixtureLookup knows the interfaces of the namespace the fixtures come from.
func fixtureLookup(t testing.TB) func(index uint32) (link, bool) {
	t.Helper()
	links := map[uint32]link{
		1: fixtureLink(t, "lo"), 2: fixtureLink(t, "d0-dummy"), 3: uplink("d1", 3),
		4: fixtureLink(t, "tun0-nocarrier"), 6: fixtureLink(t, "wg0"), 7: fixtureLink(t, "br0-bridge"),
	}
	return func(index uint32) (link, bool) {
		l, ok := links[index]
		return l, ok
	}
}

func TestRelevantMessage(t *testing.T) {
	route := func(name string, typ uint16, edit func(*routeMsg)) func(testing.TB) rtMessage {
		return func(t testing.TB) rtMessage {
			m := fixtureMessage(t, routeMessages, name)
			m.Type = typ
			if edit != nil {
				edit(m.Route)
			}
			return m
		}
	}
	addrMsgOf := func(name string, edit func(*addrMsg)) func(testing.TB) rtMessage {
		return func(t testing.TB) rtMessage {
			m := fixtureMessage(t, addrMessages, name)
			if edit != nil {
				edit(m.Addr)
			}
			return m
		}
	}
	linkMsgOf := func(name string) func(testing.TB) rtMessage {
		return func(t testing.TB) rtMessage { return fixtureMessage(t, linkMessages, name) }
	}
	tests := []struct {
		name string
		msg  func(testing.TB) rtMessage
		want bool
	}{
		{"route added", route("v4-gateway-d0", rtmNewRoute, nil), true},
		{"route deleted", route("v4-gateway-d0", rtmDelRoute, nil), true},
		{"default route added", route("v4-default-dhcp-d0", rtmNewRoute, nil), true},
		{"IPv6 default route of a router advertisement", route("v6-default-ra", rtmNewRoute, nil), true},
		{"subnet route the kernel derived from an address", route("v4-net24-d0", rtmNewRoute, nil), true},
		{"route over a bridge", route("v4-net24-d0", rtmNewRoute, func(r *routeMsg) { r.OIf = 7 }), true},
		{"blackhole route, which names no interface", route("v4-blackhole", rtmNewRoute, nil), true},
		{"route of an interface that is gone", route("v4-gateway-d0", rtmDelRoute, func(r *routeMsg) { r.OIf = 99 }), true},
		{"multipath over uplinks", route("v4-multipath", rtmNewRoute, nil), true},
		{"multipath over a tunnel and an uplink", route("v4-multipath", rtmNewRoute, func(r *routeMsg) { r.NextHops[0].OIf = 4 }), true},
		{"multipath of ours over tunnels only", route("v4-multipath", rtmNewRoute, func(r *routeMsg) { r.NextHops[0].OIf, r.NextHops[1].OIf, r.Protocol = 4, 6, RouteProtocol }), false},
		{"multipath of another program over tunnels only", route("v4-multipath", rtmNewRoute, func(r *routeMsg) { r.NextHops[0].OIf, r.NextHops[1].OIf, r.Protocol = 4, 6, protoBoot }), true},

		{"route of another table", route("v4-table100-gateway", rtmNewRoute, nil), false},
		{"route of the local table", route("v4-local", rtmNewRoute, nil), false},
		{"broadcast route", route("v4-broadcast", rtmNewRoute, nil), false},
		{"IPv6 local route", route("v6-local", rtmNewRoute, nil), false},
		{"IPv6 multicast route", route("v6-multicast-local", rtmNewRoute, nil), false},
		{"IPv6 multicast route in the main table", route("v6-net64-d0", rtmNewRoute, func(r *routeMsg) { r.Dst, r.DstLen = addr("ff00::"), 8 }), false},
		{"link-local IPv6 prefix", route("v6-linklocal-net64-d0", rtmNewRoute, nil), false},
		{"link-local IPv4 prefix", route("v4-net24-d0", rtmNewRoute, func(r *routeMsg) { r.Dst, r.DstLen = addr("169.254.0.0"), 16 }), false},
		{"multicast IPv4 prefix", route("v4-net24-d0", rtmNewRoute, func(r *routeMsg) { r.Dst, r.DstLen = addr("224.0.0.0"), 4 }), false},
		{"cache entry", route("v4-gateway-d0", rtmNewRoute, func(r *routeMsg) { r.Flags |= rtmFCloned }), false},
		{"route of another family", route("v4-gateway-d0", rtmNewRoute, func(r *routeMsg) { r.Family = 28 }), false},
		{"route of ours over a tun device", route("v4-dev-tun0", rtmNewRoute, nil), false},
		{"route of another program over a tun device", route("v4-dev-tun0", rtmNewRoute, func(r *routeMsg) { r.Protocol = protoBoot }), true},
		{"route of another program deleted from a tun device", route("v4-dev-tun0", rtmDelRoute, func(r *routeMsg) { r.Protocol = protoStatic }), true},
		{"route of another program, protocol unspecified, over a tun device", route("v4-dev-tun0", rtmNewRoute, func(r *routeMsg) { r.Protocol = 0 }), true},
		{"route the kernel made over a tun device", route("v4-dev-tun0", rtmNewRoute, func(r *routeMsg) { r.Protocol = rtprotKernel }), false},
		{"route of a router advertisement over a tun device", route("v4-dev-tun0", rtmNewRoute, func(r *routeMsg) { r.Protocol = rtprotRA }), false},
		{"route of another program over a wireguard device", route("v4-gateway-d0", rtmNewRoute, func(r *routeMsg) { r.OIf, r.Protocol = 6, protoBoot }), true},
		{"route the kernel derived for the peer of a tun device", route("v4-peer-linkdown-tun0", rtmNewRoute, nil), false},
		{"IPv6 blackhole, which sits on loopback", route("v6-blackhole", rtmNewRoute, nil), false},
		{"route over a wireguard device", route("v4-gateway-d0", rtmNewRoute, func(r *routeMsg) { r.OIf = 6 }), false},

		{"address added to d0", addrMsgOf("v4-d0", nil), true},
		{"global IPv6 address added to d0", addrMsgOf("v6-global-d0", nil), true},
		{"address of an interface that is gone", addrMsgOf("v4-d0", func(a *addrMsg) { a.Index = 99 }), true},
		{"address without any address", addrMsgOf("v4-d0", func(a *addrMsg) { a.Local, a.Address = netip.Addr{}, netip.Addr{} }), true},
		{"link-local IPv6 address", addrMsgOf("v6-linklocal-d0", nil), false},
		{"link-local IPv4 address", addrMsgOf("v4-d0", func(a *addrMsg) { a.Local, a.Address, a.PrefixLen = addr("169.254.7.7"), addr("169.254.7.7"), 16 }), false},
		{"multicast address", addrMsgOf("v6-global-d0", func(a *addrMsg) { a.Address, a.PrefixLen = addr("ff02::1"), 128 }), false},
		{"address on loopback", addrMsgOf("v4-lo", nil), false},
		{"address on a tun device", addrMsgOf("v4-peer-tun0", nil), false},

		{"d0 changed state", linkMsgOf("d0-dummy"), true},
		{"bridge changed state", linkMsgOf("br0-bridge"), true},
		{"loopback changed state", linkMsgOf("lo"), false},
		{"tun device changed state", linkMsgOf("tun0-nocarrier"), false},
		{"wireguard device changed state", linkMsgOf("wg0"), false},

		{"acknowledgement", func(t testing.TB) rtMessage { return rtMessage{Type: nlmsgError} }, false},
		{"end of a dump", func(t testing.TB) rtMessage { return rtMessage{Type: nlmsgDone} }, false},
	}
	lookup := fixtureLookup(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := relevantMessage(tt.msg(t), lookup); got != tt.want {
				t.Errorf("relevant = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMessagesRelevant(t *testing.T) {
	var logged bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logged, nil))
	loads := 0
	links := linkCache{load: func() ([]link, error) {
		loads++
		return []link{fixtureLink(t, "d0-dummy"), fixtureLink(t, "wg0")}, nil
	}}

	// The way the monitor receives them: the datagrams of a multicast group.
	if !messagesRelevant(fixture(t, routeMessages, "v4-gateway-d0"), &links, log) {
		t.Error("a route over d0 is not relevant")
	}
	if messagesRelevant(fixture(t, routeMessages, "v4-table100-gateway"), &links, log) {
		t.Error("a route of another table is relevant")
	}
	if messagesRelevant(fixture(t, addrMessages, "v6-linklocal-d0"), &links, log) {
		t.Error("a link-local address is relevant")
	}
	if loads != 1 {
		t.Errorf("the interfaces were read %d times, want once: the cache keeps them", loads)
	}

	// One relevant message among others is enough.
	datagram := slices.Concat(fixture(t, routeMessages, "v4-dev-tun0"), fixture(t, addrMessages, "v4-peer-tun0"), fixture(t, routeMessages, "v4-gateway-d0"))
	if !messagesRelevant(datagram, &links, log) {
		t.Error("a datagram with a relevant message is not relevant")
	}
	if logged.Len() != 0 {
		t.Errorf("logged %q for good messages", logged.String())
	}

	if !messagesRelevant([]byte{1, 2, 3}, &links, log) {
		t.Error("an unreadable message is not treated as a change")
	}
	if !strings.Contains(logged.String(), "unreadable") {
		t.Errorf("unreadable message was not logged: %q", logged.String())
	}

	// An interface the kernel cannot be asked about is unknown, which is relevant.
	logged.Reset()
	failing := linkCache{load: func() ([]link, error) { return nil, errors.New("boom") }}
	if !messagesRelevant(fixture(t, routeMessages, "v4-dev-tun0"), &failing, log) || !strings.Contains(logged.String(), "boom") {
		t.Errorf("a message about an interface that cannot be looked up: logged %q", logged.String())
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
	at := func(sinceStart, wallAhead time.Duration) wakeSample {
		return wakeSample{wall: wall.Add(sinceStart + wallAhead), mono: sinceStart}
	}
	steps := []struct {
		name   string
		sample wakeSample
		want   bool
	}{
		{"the first sample is the baseline", at(0, 0), false},
		{"time passes", at(5*time.Second, 0), false},
		{"the wall clock ran ahead: the machine slept", at(10*time.Second, 40*time.Minute), true},
		{"and keeps the offset without waking again", at(15*time.Second, 40*time.Minute), false},
		{"a small clock step is not a sleep", at(20*time.Second, 40*time.Minute+wakeGap), false},
		{"a step past the threshold is", at(25*time.Second, 40*time.Minute+2*wakeGap+1), true},
		{"the clock set back is not a sleep", at(30*time.Second, 30*time.Minute), false},
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
	clk        *manualClock
	netChanged chan struct{}
	out        chan osnet.Change
	cancel     context.CancelFunc
	done       chan struct{}
}

func startMerge(t *testing.T) *mergeHarness {
	t.Helper()
	h := &mergeHarness{
		clk:        newManualClock(),
		netChanged: make(chan struct{}), // unbuffered: a send returns once the loop has taken it
		out:        make(chan osnet.Change),
		done:       make(chan struct{}),
	}
	m := &netMonitor{log: discardLog(), clk: h.clk}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() {
		defer close(h.done)
		m.merge(ctx, h.netChanged, h.out)
	}()
	t.Cleanup(func() {
		cancel()
		<-h.done
	})
	h.clk.waitPending(t, wakePollInterval) // the loop has taken its baseline
	return h
}

// route reports a network change and waits until the loop has read the clock
// for it, so the test can move the clock on.
func (h *mergeHarness) route() {
	before := h.clk.reads()
	h.netChanged <- struct{}{}
	for deadline := time.Now().Add(2 * time.Second); h.clk.reads() == before; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			panic("the loop did not take the network change")
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

// The wall clock ran ahead of the monotonic clock, as CLOCK_MONOTONIC does not
// count the time of a suspend. After a wake DHCP is often not done yet, so a
// second change follows.
func TestMergeReportsAWakeTwice(t *testing.T) {
	h := startMerge(t)
	h.clk.suspend(2 * time.Hour)
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

	// The next poll sees no further gap: nothing.
	h.clk.advance(wakePollInterval)
	h.clk.fire(t, wakePollInterval)
	h.expectNothing(t)
}

func TestMergeReportsAWakeThatCameWithANetworkChangeAsAWake(t *testing.T) {
	h := startMerge(t)
	h.route()
	h.clk.suspend(2 * time.Hour)
	h.clk.advance(wakePollInterval)
	h.clk.fire(t, wakePollInterval)
	h.clk.waitPending(t, wakeFollowUp) // the wake is in the burst now
	h.clk.fire(t, burstQuiet)          // the timer of the network change
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
	return &netMonitor{log: discardLog(), clk: clk, watchChanges: watch}
}

func TestEventsReportsNetlinkMessages(t *testing.T) {
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

// The reader of the netlink socket must not wait for whoever reads the changes.
func TestEventsNetlinkReaderNeverBlocks(t *testing.T) {
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

// When the netlink socket fails, messages may have been lost: that is a change
// in itself, and the socket is opened again.
func TestEventsReopensTheNetlinkSocket(t *testing.T) {
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
			return errors.New("read netlink socket: bad file descriptor")
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
	clk.fire(t, netlinkRetry)
	select {
	case <-reopened:
	case <-time.After(2 * time.Second):
		t.Fatal("the netlink socket was not opened again")
	}
}
