package manager_test

import (
	"net/netip"
	"sync"
	"testing"
	"time"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/manager/fake"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// announceRecorder is a Reconciler that notes the intents announced through it.
type announceRecorder struct {
	tunnel.Reconciler

	mu      sync.Mutex
	intents []tunnel.Intent
}

func (r *announceRecorder) Announce(in tunnel.Intent) error {
	r.mu.Lock()
	r.intents = append(r.intents, in)
	r.mu.Unlock()
	return r.Reconciler.Announce(in)
}

// latest returns the priority of the last intent announced for owner.
func (r *announceRecorder) latest(owner string) (priority int, announced bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.intents) - 1; i >= 0; i-- {
		if r.intents[i].Owner == tunnel.OwnerID(owner) {
			return r.intents[i].Priority, true
		}
	}
	return 0, false
}

// announceUp does what a real engine does once its tunnel is up: it announces
// the routes with the priority its spec carried when it was started.
func announceUp(t *testing.T, s *stubEngine) {
	t.Helper()
	err := s.deps.Network.Announce(tunnel.Intent{
		Owner:    s.spec.Owner,
		State:    tunnel.StateUp,
		Iface:    "utun7",
		Priority: s.spec.Priority,
		Routes:   []netip.Prefix{netip.MustParsePrefix("10.5.0.0/16")},
	})
	if err != nil {
		t.Errorf("announce: %v", err)
	}
}

func wantPriority(t *testing.T, rec *announceRecorder, owner string, want int) {
	t.Helper()
	eventually(t, "the priority of "+owner+" to be announced", func() bool {
		got, ok := rec.latest(owner)
		return ok && got == want
	})
}

// The Reconciler arbitrates overlapping routes and DNS with the priority of
// each intent, which an engine copies from its spec when it starts. Moving a
// profile must reach the tunnels that are already up.
func TestPriorityChangesReachTunnelsThatAreAlreadyUp(t *testing.T) {
	rec := &announceRecorder{Reconciler: fake.NewReconciler()}
	stub := &stubBackend{kind: tunnel.KindOpenVPN, onEngine: func(s *stubEngine) { announceUp(t, s) }}
	e := newEnv(t, withBackends(stub.backend()), withReconciler(rec))
	a := e.importProfile("a", ovpnProfile)
	b := e.importProfile("b", ovpnProfile)
	e.setEnabled(a.Id, true)
	e.setEnabled(b.Id, true)
	wantPriority(t, rec, a.Id, 1)
	wantPriority(t, rec, b.Id, 2)

	if _, err := e.m.Reorder([]string{b.Id, a.Id}); err != nil {
		t.Fatal(err)
	}
	wantPriority(t, rec, a.Id, 2)
	wantPriority(t, rec, b.Id, 1)

	// An engine announces again whenever its state changes, still with the
	// priority it was started with. The profile's current one wins.
	announceUp(t, stub.engine(0))
	if got, _ := rec.latest(a.Id); got != 2 {
		t.Fatalf("an engine announced its old priority and it reached the Reconciler: %d, want 2", got)
	}

	if _, err := e.m.Update(&pb.UpdateProfileRequest{Id: a.Id, Settings: &pb.ProfileSettings{Priority: 9}}); err != nil {
		t.Fatal(err)
	}
	wantPriority(t, rec, a.Id, 9)
}

// A reorder that lands while an engine is still starting is not seen by the
// running tunnels, so it has to be applied when the engine is attached.
func TestPriorityChangedWhileAnEngineIsStartingIsAppliedWhenItIsUp(t *testing.T) {
	rec := &announceRecorder{Reconciler: fake.NewReconciler()}
	gate := make(chan struct{})
	stub := &stubBackend{kind: tunnel.KindOpenVPN, startGate: gate, onEngine: func(s *stubEngine) { announceUp(t, s) }}
	e := newEnv(t, withBackends(stub.backend()), withReconciler(rec))
	a := e.importProfile("a", ovpnProfile)
	b := e.importProfile("b", ovpnProfile)

	go e.m.SetEnabled(a.Id, true) // returns once the engine has started or startWait has passed
	wantPriority(t, rec, a.Id, 1)
	if _, err := e.m.Reorder([]string{b.Id, a.Id}); err != nil {
		t.Fatal(err)
	}
	if got, _ := rec.latest(a.Id); got != 1 {
		t.Fatalf("a tunnel that is not up yet was announced again: priority %d", got)
	}

	close(gate)
	wantPriority(t, rec, a.Id, 2)
}

// An engine that outlives its Stop (it ignored the timeout, or announces from
// a goroutine that was already running) must not bring routes back for a
// profile that is no longer running.
func TestStoppedEngineCannotAnnounceAgain(t *testing.T) {
	rec := fake.NewReconciler()
	stub := &stubBackend{kind: tunnel.KindOpenVPN, onEngine: func(s *stubEngine) { announceUp(t, s) }}
	e := newEnv(t, withBackends(stub.backend()), withReconciler(rec))
	p := e.importProfile("a", ovpnProfile)
	e.setEnabled(p.Id, true)
	engine := stub.engine(0)
	if len(rec.Report().Routes) != 1 {
		t.Fatalf("routes = %+v, want the announced one", rec.Report().Routes)
	}

	e.setEnabled(p.Id, false)
	err := engine.deps.Network.Announce(tunnel.Intent{
		Owner:  engine.spec.Owner,
		State:  tunnel.StateUp,
		Iface:  "utun7",
		Routes: []netip.Prefix{netip.MustParsePrefix("10.5.0.0/16")},
	})
	if err == nil {
		t.Error("the Reconciler accepted an announcement from a stopped engine")
	}
	time.Sleep(10 * time.Millisecond) // the fake Reconciler applies synchronously; this only lets a bug show
	if routes := rec.Report().Routes; len(routes) != 0 {
		t.Fatalf("a stopped engine brought its routes back: %+v", routes)
	}
}
