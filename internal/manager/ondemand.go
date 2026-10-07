package manager

import (
	"context"
	"errors"
	"time"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/profile"
)

// defaultOnDemandSettle is how long the network has to stay quiet before the
// on-demand profiles follow it, so that a burst of changes (a dock reconnecting,
// roaming between access points) is judged by where it ended.
const defaultOnDemandSettle = 2 * time.Second

// StartOnDemand connects and disconnects the profiles that have on-demand
// rules so that they match the Mac's primary network: it looks at the network
// once now and then each time the kind of the primary network changes. Call it
// once the server is serving.
//
// The controller acts only through SetEnabled, as a user does, and only when the
// kind changes, so a manual connect or disconnect holds until it does. A network
// that is gone changes nothing: coming back as the same kind is no change.
func (m *Manager) StartOnDemand() {
	ctx, cancel := context.WithCancel(context.Background())
	// Subscribing before the first look leaves no change between the two.
	events := m.net.Events(ctx)
	go func() {
		defer cancel()
		m.followNetwork(events)
	}()
}

func (m *Manager) followNetwork(events <-chan osnet.Change) {
	var (
		last    osnet.LinkKind
		known   bool
		settle  *time.Timer
		settled <-chan time.Time
	)
	defer func() {
		if settle != nil {
			settle.Stop()
		}
	}()
	evaluate := func() {
		kind, ok := m.primaryKind()
		if !ok || (known && kind == last) {
			return
		}
		last, known = kind, true
		m.matchOnDemand(kind, "")
	}

	evaluate()
	for {
		select {
		case _, ok := <-events:
			if !ok {
				return
			}
			if settle == nil {
				settle = time.NewTimer(m.onDemandSettle)
			} else {
				settle.Reset(m.onDemandSettle)
			}
			settled = settle.C
		case <-settled:
			settled = nil
			evaluate()
		case <-m.stopped:
			return
		}
	}
}

// primaryKind is the kind of the primary network: that of the IPv4 default
// route, or of the IPv6 one when there is no IPv4 default. ok is false when
// there is no network at all, or it could not be read.
func (m *Manager) primaryKind() (kind osnet.LinkKind, ok bool) {
	state, err := m.net.Snapshot()
	if err != nil {
		m.log.Warn("on demand: could not read the network", "err", err)
		return 0, false
	}
	switch {
	case state.DefaultV4 != nil:
		return state.DefaultV4.Kind, true
	case state.DefaultV6 != nil:
		return state.DefaultV6.Kind, true
	}
	return 0, false
}

// applyOnDemand makes one profile match the network as it is now. Saving new
// rules does this, and nothing else does it for a single profile.
func (m *Manager) applyOnDemand(id string) {
	if kind, ok := m.primaryKind(); ok {
		m.matchOnDemand(kind, id)
	}
}

// matchOnDemand connects the on-demand profiles whose rules want the network
// kind and disconnects those that do not; only limits it to one profile when it
// is not empty. A profile already in the wanted state is left alone, which is
// also how a manual choice survives.
func (m *Manager) matchOnDemand(kind osnet.LinkKind, only string) {
	type action struct {
		e       *entry
		connect bool
	}
	var actions []action
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return
	}
	for _, meta := range m.store.List() {
		rules := meta.Settings.OnDemand
		e := m.entries[meta.ID]
		if (only != "" && meta.ID != only) || !rules.Active() || e == nil {
			continue
		}
		if connect := wantsNetwork(rules, kind); connect != e.desired {
			actions = append(actions, action{e, connect})
		}
	}
	m.mu.Unlock()

	for _, a := range actions {
		verb := "disconnecting"
		if a.connect {
			verb = "connecting"
		}
		m.log.Info("on demand", "profile", a.e.id, "network", linkKindName(kind), "connect", a.connect)
		a.e.logs.Add(pb.LogLevel_LOG_LEVEL_INFO, "on demand: "+verb+", the network is "+linkKindName(kind))
		if _, err := m.SetEnabled(a.e.id, a.connect); err != nil && !errors.Is(err, profile.ErrNotFound) && !errors.Is(err, ErrShuttingDown) {
			m.log.Warn("on demand: could not change the profile", "profile", a.e.id, "connect", a.connect, "err", err)
		}
	}
}

func wantsNetwork(rules profile.OnDemand, kind osnet.LinkKind) bool {
	switch kind {
	case osnet.LinkEthernet:
		return rules.Ethernet
	case osnet.LinkWiFi:
		return rules.WiFi
	}
	return false
}

func linkKindName(kind osnet.LinkKind) string {
	switch kind {
	case osnet.LinkEthernet:
		return "Ethernet"
	case osnet.LinkWiFi:
		return "Wi-Fi"
	}
	return "other"
}
