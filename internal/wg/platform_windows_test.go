package wg

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

func TestWaitForRemovalReturnsOnceTheInterfaceIsGone(t *testing.T) {
	var polls atomic.Int32
	listed := func(name string) bool {
		if name != "Plaitway-1" {
			t.Errorf("asked about %q", name)
		}
		return polls.Add(1) <= 3
	}
	var logged []string
	log := func(_ tunnel.LogLevel, text string) { logged = append(logged, text) }

	waitForRemoval("Plaitway-1", log, listed, 5*time.Second, time.Millisecond)

	if got := polls.Load(); got != 4 {
		t.Errorf("polled %d times, want 4 (listed three times, then gone)", got)
	}
	if len(logged) != 0 {
		t.Errorf("logged %q, want nothing", logged)
	}
}

func TestWaitForRemovalGivesUpWithAWarning(t *testing.T) {
	var level tunnel.LogLevel
	var text string
	log := func(l tunnel.LogLevel, s string) { level, text = l, s }

	started := time.Now()
	waitForRemoval("Plaitway-1", log, func(string) bool { return true }, 30*time.Millisecond, time.Millisecond)

	if time.Since(started) < 30*time.Millisecond {
		t.Error("gave up before the timeout")
	}
	if level != tunnel.LogWarn || !strings.Contains(text, "Plaitway-1") {
		t.Errorf("logged %v %q, want a warning that names the adapter", level, text)
	}
}

func TestAwaitInterfaceRemovalIgnoresAnEngineWithoutAnInterface(t *testing.T) {
	started := time.Now()
	awaitInterfaceRemoval("", func(tunnel.LogLevel, string) { t.Error("logged") })
	if time.Since(started) > time.Second {
		t.Error("waited for an interface that was never created")
	}
}

func TestInterfaceExistsIsFalseForAnUnknownName(t *testing.T) {
	if interfaceExists("Plaitway-test-there-is-no-such-adapter") {
		t.Error("an interface that does not exist is listed")
	}
}

// Stop promises that the adapter is gone when it returns, and the system
// removes a closed adapter a little later than Close returns.
func TestStopWaitsUntilTheClosedInterfaceIsGone(t *testing.T) {
	checkLeaks(t)
	var polls atomic.Int32
	const listedPolls = 4
	original := isInterfaceListed
	isInterfaceListed = func(name string) bool {
		if name != tunnelName {
			t.Errorf("asked about %q, want the interface of the engine %q", name, tunnelName)
		}
		return polls.Add(1) <= listedPolls
	}
	t.Cleanup(func() { isInterfaceListed = original })

	p := newPair(t, pairOptions{})
	p.up()
	started := time.Now()
	if err := p.client.eng.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := polls.Load(); got < listedPolls+1 {
		t.Errorf("Stop asked %d times whether the interface was gone, want it to wait until the answer was yes (%d)", got, listedPolls+1)
	}
	if elapsed := time.Since(started); elapsed < (listedPolls-1)*adapterRemovalPoll {
		t.Errorf("Stop returned after %s, before the interface was gone", elapsed)
	}
}

// Without a trusted wintun.dll the engine does not even try an adapter, and
// the status says why.
func TestEngineFailsWithThePreciseReasonWithoutWintun(t *testing.T) {
	checkLeaks(t)
	useExecutableDir(t, t.TempDir())
	n := newNode(t, "no-wintun", clientProfile(newKeyPair(t), newKeyPair(t), "127.0.0.1:1", "", ""), nodeOptions{})
	n.eng.cfg.TunFactory, n.eng.cfg.Interfaces = nil, nil // the platform's own

	n.start()

	status := n.waitState(tunnel.StateFailed)
	if !strings.Contains(status.Err, "create tunnel device") || !strings.Contains(status.Err, wintunDLLName+" is missing") {
		t.Errorf("Err = %q, want the creation of the device to fail because %s is missing", status.Err, wintunDLLName)
	}
	if kinds := n.rec.kinds(); slices.Contains(kinds, "announce:connecting") || slices.Contains(kinds, "announce:up") {
		t.Errorf("events %q: a tunnel that never existed announced itself", kinds)
	}
}
