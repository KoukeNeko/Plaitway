package manager

import (
	"errors"
	"fmt"
	"sync"

	"github.com/KoukeNeko/Plaitway/internal/profile"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

var errOwnerStopped = errors.New("the profile is not running")

// ownerNetwork is the tunnel.Network that one engine gets. It stands between
// the engine and the Reconciler for two reasons.
//
// Priority is the user's to change while a tunnel is up, but an engine copies
// it from its Spec when it starts. The wrapper stamps the profile's current
// priority on every Intent, and announces the last Intent again when that
// priority changes, so that the Reconciler arbitrates overlapping routes and
// DNS between the tunnels that are already up with the order the user sees.
//
// And once the profile has been stopped, an engine that outlives its Stop (it
// ran into the stop timeout, or announces from a goroutine that was already
// running) must not bring routes back: close refuses every later Announce.
type ownerNetwork struct {
	rec   tunnel.Network
	store *profile.Store
	owner tunnel.OwnerID

	// mu is held across the calls to rec, so that what is stamped and what
	// was announced last can never disagree.
	mu     sync.Mutex
	last   *tunnel.Intent // nil until announced and after a Withdraw
	closed bool
}

func (n *ownerNetwork) Announce(in tunnel.Intent) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return errOwnerStopped
	}
	priority, err := n.priority()
	if err != nil {
		return err
	}
	in.Priority = priority
	if err := n.rec.Announce(in); err != nil {
		return err
	}
	n.last = &in
	return nil
}

func (n *ownerNetwork) Withdraw(owner tunnel.OwnerID) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.last = nil
	n.rec.Withdraw(owner)
}

// refresh announces the last Intent again when the profile has another
// priority than the one it was announced with.
func (n *ownerNetwork) refresh() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed || n.last == nil {
		return nil
	}
	priority, err := n.priority()
	if err != nil {
		return err
	}
	if priority == n.last.Priority {
		return nil
	}
	next := *n.last
	next.Priority = priority
	if err := n.rec.Announce(next); err != nil {
		return err
	}
	n.last = &next
	return nil
}

// close withdraws the owner and refuses every Announce from now on. Whatever
// the engine did, nothing of it stays in the Reconciler afterwards.
func (n *ownerNetwork) close() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.closed = true
	n.last = nil
	n.rec.Withdraw(n.owner)
}

// priority is the stored priority of the profile, which Reorder and Update change.
func (n *ownerNetwork) priority() (int, error) {
	meta, err := n.store.Get(string(n.owner))
	if err != nil {
		return 0, fmt.Errorf("priority of the profile: %w", err)
	}
	return meta.Settings.Priority, nil
}
