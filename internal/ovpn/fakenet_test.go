package ovpn

import (
	"sync"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// --- the other side: what the daemon gives the engine ---

type netCall struct {
	withdraw bool
	intent   tunnel.Intent
}

// fakeNetwork records what the engine asks of the Reconciler.
type fakeNetwork struct {
	mu          sync.Mutex
	calls       []netCall
	announceErr func(n int, it tunnel.Intent) error
	onWithdraw  func()
}

func (n *fakeNetwork) Announce(it tunnel.Intent) error {
	n.mu.Lock()
	n.calls = append(n.calls, netCall{intent: it})
	count := len(n.calls)
	hook := n.announceErr
	n.mu.Unlock()
	if hook != nil {
		return hook(count, it)
	}
	return nil
}

func (n *fakeNetwork) Withdraw(owner tunnel.OwnerID) {
	n.mu.Lock()
	n.calls = append(n.calls, netCall{withdraw: true, intent: tunnel.Intent{Owner: owner}})
	hook := n.onWithdraw
	n.mu.Unlock()
	if hook != nil {
		hook()
	}
}

func (n *fakeNetwork) snapshot() []netCall {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]netCall(nil), n.calls...)
}
