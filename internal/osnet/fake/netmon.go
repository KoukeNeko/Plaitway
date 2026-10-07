package fake

import (
	"context"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

const eventBuffer = 256

// NetMonitor is an osnet.NetMonitor whose state is scripted by the test: Set
// the NetState, then Emit the Change a real monitor would have sent.
type NetMonitor struct {
	mu          sync.Mutex
	state       osnet.NetState
	snapshotErr error
	subs        []chan osnet.Change
}

func NewNetMonitor() *NetMonitor { return &NetMonitor{} }

func (m *NetMonitor) Snapshot() (osnet.NetState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.snapshotErr != nil {
		return osnet.NetState{}, m.snapshotErr
	}
	return cloneState(m.state), nil
}

// Events returns a channel that receives every Change emitted while ctx is
// alive and is closed when ctx ends. A subscriber that stops reading loses
// events once its buffer is full.
func (m *NetMonitor) Events(ctx context.Context) <-chan osnet.Change {
	ch := make(chan osnet.Change, eventBuffer)
	m.mu.Lock()
	m.subs = append(m.subs, ch)
	m.mu.Unlock()
	go func() {
		<-ctx.Done()
		m.mu.Lock()
		defer m.mu.Unlock()
		if i := slices.Index(m.subs, ch); i >= 0 {
			m.subs = slices.Delete(m.subs, i, i+1)
		}
		close(ch)
	}()
	return ch
}

// Set replaces the scripted state. The epoch advances exactly when the content
// differs from the previous state, whatever Epoch the caller put in ns.
func (m *NetMonitor) Set(ns osnet.NetState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := cloneState(ns)
	next.Epoch = m.state.Epoch
	if !reflect.DeepEqual(m.state, next) {
		next.Epoch++
	}
	m.state = next
}

// Emit delivers a Change to every subscriber, stamping At when it is zero.
func (m *NetMonitor) Emit(c osnet.Change) {
	if c.At.IsZero() {
		c.At = time.Now()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ch := range m.subs {
		select {
		case ch <- c:
		default:
		}
	}
}

// FailSnapshot makes Snapshot return err until it is called again with nil.
func (m *NetMonitor) FailSnapshot(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshotErr = err
}

func cloneState(ns osnet.NetState) osnet.NetState {
	out := ns
	out.Interfaces = make([]osnet.Interface, len(ns.Interfaces))
	for i, ifc := range ns.Interfaces {
		out.Interfaces[i] = ifc
		out.Interfaces[i].Addrs = slices.Clone(ifc.Addrs)
	}
	if ns.Interfaces == nil {
		out.Interfaces = nil
	}
	if ns.DefaultV4 != nil {
		nh := *ns.DefaultV4
		out.DefaultV4 = &nh
	}
	if ns.DefaultV6 != nil {
		nh := *ns.DefaultV6
		out.DefaultV6 = &nh
	}
	out.Connected = slices.Clone(ns.Connected)
	return out
}
