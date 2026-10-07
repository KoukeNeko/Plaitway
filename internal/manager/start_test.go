package manager_test

import (
	"context"
	"testing"
	"time"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// within runs f and fails the test when it does not return in time. A bug that
// makes f hang is then a failure, not a hung test run. f must not call
// t.Fatal: it runs on another goroutine.
func within(t *testing.T, what string, d time.Duration, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not return within %v", what, d)
	}
}

// At boot an engine can take a long time to start: a lookup waits for the
// network to come up. That must not hold up the daemon's API.
func TestStartReturnsWhileAutoConnectEnginesAreStillStarting(t *testing.T) {
	dir := t.TempDir()
	first := startEnv(t, dir)
	auto := first.importWith(&pb.ImportProfileRequest{Name: "auto", Content: []byte(ovpnProfile), Settings: &pb.ProfileSettings{AutoConnect: true}}).Profile
	first.shutdown()

	gate := make(chan struct{})
	stub := &stubBackend{kind: tunnel.KindOpenVPN, startGate: gate}
	var second *env
	within(t, "Manager.Start with an engine that is slow to start", time.Second, func() {
		second = startEnv(t, dir, withBackends(stub.backend()))
	})

	got := second.get(auto.Id)
	if !got.DesiredEnabled || got.State != pb.ProfileState_PROFILE_STATE_CONNECTING {
		t.Fatalf("right after Start the auto_connect profile is %v, want it wanted and connecting", got)
	}

	close(gate)
	eventually(t, "the engine to be started", func() bool {
		engine := stub.engine(0)
		return engine != nil && engine.started.Load()
	})
	if got := second.get(auto.Id); got.State != pb.ProfileState_PROFILE_STATE_CONNECTING || got.LastError != "" {
		t.Fatalf("profile = %v", got)
	}
}

// Enabling a profile whose engine is slow to start returns when the engine has
// not reported yet; the start goes on and the profile stays CONNECTING.
func TestEnableDoesNotWaitForeverForAnEngineThatIsSlowToStart(t *testing.T) {
	gate := make(chan struct{})
	stub := &stubBackend{kind: tunnel.KindOpenVPN, startGate: gate}
	e := newEnv(t, withBackends(stub.backend()), withStartWait(50*time.Millisecond))
	p := e.importProfile("a", ovpnProfile)

	var got *pb.Profile
	within(t, "SetEnabled", time.Second, func() {
		var err error
		if got, err = e.m.SetEnabled(p.Id, true); err != nil {
			t.Error(err)
		}
	})
	if got.State != pb.ProfileState_PROFILE_STATE_CONNECTING || !got.DesiredEnabled || got.LastError != "" {
		t.Fatalf("profile = %v, want it connecting and not failed", got)
	}

	close(gate)
	eventually(t, "the engine to be started", func() bool {
		engine := stub.engine(0)
		return engine != nil && engine.started.Load()
	})
	if cur := e.get(p.Id); cur.State == pb.ProfileState_PROFILE_STATE_FAILED {
		t.Fatalf("a slow start failed the profile: %v", cur)
	}
}

func TestEnablingAProfileThatIsStartingStartsNoSecondEngine(t *testing.T) {
	gate := make(chan struct{})
	stub := &stubBackend{kind: tunnel.KindOpenVPN, startGate: gate}
	e := newEnv(t, withBackends(stub.backend()))
	p := e.importProfile("a", ovpnProfile)
	e.waitStarting(p.Id, stub)

	within(t, "SetEnabled of a profile that is starting", time.Second, func() {
		if _, err := e.m.SetEnabled(p.Id, true); err != nil {
			t.Error(err)
		}
	})
	close(gate)
	eventually(t, "the engine to be started", func() bool { return stub.engine(0).started.Load() })
	if n := stub.created.Load(); n != 1 {
		t.Fatalf("%d engines were created, want 1", n)
	}
}

// The user can turn a profile off while its engine is still starting, however
// long that takes.
func TestProfileCanBeDisabledWhileItsEngineIsStarting(t *testing.T) {
	stub := &stubBackend{kind: tunnel.KindOpenVPN, startGate: make(chan struct{})}
	e := newEnv(t, withBackends(stub.backend()))
	p := e.importProfile("a", ovpnProfile)
	engine := e.waitStarting(p.Id, stub)

	var got *pb.Profile
	within(t, "disabling a profile that is starting", 2*time.Second, func() {
		var err error
		if got, err = e.m.SetEnabled(p.Id, false); err != nil {
			t.Error(err)
		}
	})
	if got.State != pb.ProfileState_PROFILE_STATE_DISCONNECTED || got.DesiredEnabled || got.LastError != "" {
		t.Fatalf("after disabling: %v", got)
	}
	if engine.started.Load() {
		t.Fatal("the engine finished starting although it was cancelled")
	}

	// The profile can be started again.
	close(stub.startGate)
	e.setEnabled(p.Id, true)
	eventually(t, "the second engine", func() bool { return stub.created.Load() == 2 })
}

func TestDeleteCancelsAnEngineThatIsStarting(t *testing.T) {
	stub := &stubBackend{kind: tunnel.KindOpenVPN, startGate: make(chan struct{})}
	e := newEnv(t, withBackends(stub.backend()))
	p := e.importProfile("a", ovpnProfile)
	e.waitStarting(p.Id, stub)

	within(t, "deleting a profile that is starting", 2*time.Second, func() {
		if err := e.m.Delete(p.Id); err != nil {
			t.Error(err)
		}
	})
	if e.get(p.Id) != nil {
		t.Fatal("profile still listed")
	}
}

func TestShutdownCancelsEnginesThatAreStarting(t *testing.T) {
	stub := &stubBackend{kind: tunnel.KindOpenVPN, startGate: make(chan struct{})}
	e := newEnv(t, withBackends(stub.backend()))
	p := e.importProfile("a", ovpnProfile)
	engine := e.waitStarting(p.Id, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	within(t, "Shutdown", 3*time.Second, func() {
		if err := e.m.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	if engine.started.Load() {
		t.Fatal("an engine that was still starting at shutdown ended up running")
	}
}

// An engine that finishes starting just as the daemon shuts down must be
// stopped by whoever started it, before Shutdown returns.
func TestEngineThatFinishesStartingDuringShutdownIsStopped(t *testing.T) {
	gate := make(chan struct{})
	stub := &stubBackend{kind: tunnel.KindOpenVPN, startGate: gate, stopDelay: 50 * time.Millisecond}
	e := newEnv(t, withBackends(stub.backend()))
	p := e.importProfile("a", ovpnProfile)
	engine := e.waitStarting(p.Id, stub)

	close(gate) // the engine starts at the same time as Shutdown cancels the start
	e.shutdown()
	if engine.started.Load() && !engine.stopped.Load() {
		t.Fatal("an engine was left running by Shutdown")
	}
}
