package windows

import (
	"context"
	"net/netip"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/winiface/inet"
)

// waitFor polls until cond holds, failing after a generous bound.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// Every notification registered with the system is released when ctx ends, and
// the call returns.
func TestRealWatchChangesReleasesEveryRegistration(t *testing.T) {
	before := activeNotifications.Load()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- watchChanges(ctx, discardLog(), func() {}) }()

	waitFor(t, "three registrations", func() bool { return activeNotifications.Load() == before+3 })
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("watchChanges: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watchChanges did not return after cancel")
	}
	if got := activeNotifications.Load(); got != before {
		t.Errorf("%d notifications are still registered", got-before)
	}
	count := 0
	registrations.Range(func(any, any) bool { count++; return true })
	if count != 0 {
		t.Errorf("%d callback contexts are still kept", count)
	}
}

func TestRealWatchWakeReleasesItsRegistration(t *testing.T) {
	before := activeNotifications.Load()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- watchWake(ctx, discardLog(), func() {}) }()

	waitFor(t, "the power registration", func() bool { return activeNotifications.Load() == before+1 })
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("watchWake: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watchWake did not return after cancel")
	}
	if got := activeNotifications.Load(); got != before {
		t.Errorf("%d notifications are still registered", got-before)
	}
}

// The real monitor registers for everything, delivers a heartbeat-free channel
// that stays quiet while nothing happens, and closes it when ctx ends.
func TestRealEventsClosesTheChannelAndReleases(t *testing.T) {
	before := activeNotifications.Load()
	monitor := NewNetMonitor(NetMonitorOptions{Logger: discardLog()})
	ctx, cancel := context.WithCancel(context.Background())
	events := monitor.Events(ctx)
	waitFor(t, "all registrations", func() bool { return activeNotifications.Load() == before+4 })

	cancel()
	select {
	case _, open := <-events:
		if open {
			t.Error("a change after cancel")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the channel stays open after cancel")
	}
	waitFor(t, "all registrations to be released", func() bool { return activeNotifications.Load() == before })
}

// Callbacks of the system find their registration by the context pointer, and
// ignore a context that has been released.
func TestCallbacksFindTheirRegistration(t *testing.T) {
	var signals atomic.Int32
	reg, key := addRegistration(func() { signals.Add(1) })
	defer registrations.Delete(key)
	_ = reg

	forwardRow := row("203.0.113.0/24", "192.168.1.1", intelEthernet, 0, windows.MIB_IPPROTO_NETMGMT, windows.NlroManual)
	multicastRow := row("224.0.0.0/4", "", intelEthernet, 256, windows.MIB_IPPROTO_LOCAL, windows.NlroWellKnown)
	linkLocalRow := row("fe80::/64", "", intelEthernet, 256, windows.MIB_IPPROTO_LOCAL, windows.NlroWellKnown)

	onRouteNotification(key, forwardRow)
	if signals.Load() != 1 {
		t.Errorf("a route to a destination that matters gave %d signals, want 1", signals.Load())
	}
	onRouteNotification(key, multicastRow)
	onRouteNotification(key, linkLocalRow)
	if signals.Load() != 1 {
		t.Errorf("housekeeping routes signalled: %d", signals.Load())
	}
	onRouteNotification(key, nil)
	if signals.Load() != 2 {
		t.Errorf("a notification without a row must count: %d", signals.Load())
	}

	var global, linkLocal windows.MibUnicastIpAddressRow
	inet.Set((*windows.RawSockaddrInet)(unsafe.Pointer(&global.Address)), netip.MustParseAddr("192.168.1.101"))
	inet.Set((*windows.RawSockaddrInet)(unsafe.Pointer(&linkLocal.Address)), netip.MustParseAddr("fe80::1"))
	onAddressNotification(key, &global)
	onAddressNotification(key, &linkLocal)
	if signals.Load() != 3 {
		t.Errorf("addresses gave %d signals, want 3 in all", signals.Load())
	}

	signalRegistration(key)
	registrations.Delete(key)
	signalRegistration(key) // released: nobody to tell, and no panic
	onRouteNotification(key, forwardRow)
	if signals.Load() != 4 {
		t.Errorf("signals = %d: a released registration was signalled", signals.Load())
	}
}

// The addresses the system hands to a callback come back as pointers.
func TestPointerOf(t *testing.T) {
	value := windows.MibIpForwardRow2{Metric: 42}
	got := (*windows.MibIpForwardRow2)(pointerOf(uintptr(unsafe.Pointer(&value))))
	if got != &value || got.Metric != 42 {
		t.Errorf("pointerOf returned %p, want %p", got, &value)
	}
}

// The first notification of the real stack: a Change arrives within the debounce
// time after a notification, on the real clock.
func TestRealEventsDeliverAChangeOnTheRealClock(t *testing.T) {
	monitor := NewNetMonitor(NetMonitorOptions{Logger: discardLog()}).(*netMonitor)
	signal := make(chan func(), 1)
	monitor.watchChanges = func(ctx context.Context, changed func()) error {
		signal <- changed
		<-ctx.Done()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := monitor.Events(ctx)
	changed := <-signal
	start := time.Now()
	changed()
	select {
	case got := <-events:
		if got.Reason != osnet.ChangeRoute {
			t.Errorf("reason %q", got.Reason)
		}
		if elapsed := time.Since(start); elapsed < burstQuiet-50*time.Millisecond {
			t.Errorf("the change came after %v, before the quiet period of %v", elapsed, burstQuiet)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no change")
	}
}

// netshMetrics reads the interface metrics of one family the way an
// administrator sees them: the table that netsh prints (index, metric, MTU,
// state, name; the header and the state are localized, the numbers are not).
func netshMetrics(t *testing.T, family string) map[int]uint32 {
	t.Helper()
	out, err := exec.Command("netsh", "interface", family, "show", "interfaces").Output()
	if err != nil {
		t.Skipf("netsh: %v", err)
	}
	row := regexp.MustCompile(`^\s*(\d+)\s+(\d+)\s+\d+\s+\S+\s+\S`)
	metrics := map[int]uint32{}
	for _, line := range strings.Split(string(out), "\n") {
		if m := row.FindStringSubmatch(line); m != nil {
			index, _ := strconv.Atoi(m[1])
			metric, _ := strconv.ParseUint(m[2], 10, 32)
			metrics[index] = uint32(metric)
		}
	}
	return metrics
}

// The metric of the snapshot is the effective one: the value Windows adds to
// route metrics, which for an adapter with an automatic metric is the one it
// computed from the link speed, not a stored zero. netsh shows the same value.
func TestRealSnapshotInterfaceMetricsAreTheEffectiveOnes(t *testing.T) {
	state, err := NewNetMonitor(NetMonitorOptions{Logger: discardLog()}).Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	ipv4 := netshMetrics(t, "ipv4")
	ipv6 := netshMetrics(t, "ipv6")
	compared := 0
	for _, iface := range state.Interfaces {
		if !iface.Up {
			continue
		}
		want, ok := ipv4[iface.Index]
		if !ok {
			want, ok = ipv6[iface.Index]
		}
		if !ok {
			// netsh lists IP interfaces only. An adapter without an IP stack (a
			// virtual switch extension, Wi-Fi Direct) has no metric to report.
			if iface.Metric != 0 {
				t.Errorf("%s (%d) is not an IP interface and reports metric %d", iface.Name, iface.Index, iface.Metric)
			}
			continue
		}
		if iface.Metric == 0 {
			t.Errorf("%s (%d) is an IP interface that is up and reports no metric", iface.Name, iface.Index)
			continue
		}
		compared++
		if iface.Metric != want {
			t.Errorf("%s (%d): metric %d, netsh says %d", iface.Name, iface.Index, iface.Metric, want)
		}
	}
	if compared == 0 {
		t.Fatal("no interface was compared with netsh")
	}
	t.Logf("%d interfaces agree with netsh", compared)
}
