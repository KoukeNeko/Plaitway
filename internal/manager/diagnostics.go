package manager

import (
	"fmt"
	"slices"

	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/profile"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// Info describes the daemon and whether each engine can run here.
func (m *Manager) Info() *pb.DaemonInfo {
	info := &pb.DaemonInfo{Version: m.version, ProtocolVersion: ProtocolVersion, Privileged: m.privileged}
	kinds := make([]tunnel.Kind, 0, len(m.backends))
	for kind := range m.backends {
		kinds = append(kinds, kind)
	}
	slices.Sort(kinds)
	for _, kind := range kinds {
		probe := m.backends[kind].Probe()
		info.Engines = append(info.Engines, &pb.EngineInfo{
			Kind:      kindToProto(kind),
			Available: probe.Available,
			Version:   probe.Version,
			Detail:    probe.Detail,
		})
	}
	return info
}

// Diagnostics is what the Reconciler believes and has done, with the engines'
// availability.
func (m *Manager) Diagnostics() *pb.Diagnostics {
	report := m.rec.Report()
	d := &pb.Diagnostics{
		Daemon:          m.Info(),
		Network:         networkToProto(report),
		ResolverEntries: slices.Clone(report.ResolverEntries),
	}
	for _, r := range report.Routes {
		d.OwnedRoutes = append(d.OwnedRoutes, &pb.OwnedRoute{
			Prefix: r.Prefix.String(),
			Owner:  string(r.Owner),
			Kind:   routeKindToProto(r.Kind),
			Via:    r.Via,
			State:  routeStateToProto(r.State),
		})
	}
	for _, s := range report.Stale {
		stale := &pb.StaleRoute{
			Key:       s.Key,
			Prefix:    s.Route.Dst.String(),
			Interface: s.Route.Iface,
			Reason:    s.Reason,
			Owned:     s.Owned,
		}
		if s.Route.Gateway.IsValid() {
			stale.Gateway = s.Route.Gateway.String()
		}
		d.StaleRoutes = append(d.StaleRoutes, stale)
	}
	for _, j := range report.Journal {
		d.RecentJournal = append(d.RecentJournal, &pb.JournalEntry{
			Time:  timestamppb.New(j.Time),
			Owner: string(j.Owner),
			Kind:  j.Kind,
			Key:   j.Key,
			State: j.State,
		})
	}
	return d
}

func networkToProto(report tunnel.Report) *pb.NetworkInfo {
	n := &pb.NetworkInfo{LastChangeReason: string(report.LastChange.Reason)}
	if hop := report.Net.DefaultV4; hop != nil {
		n.DefaultGatewayV4, n.DefaultInterfaceV4 = hop.Gateway.String(), hop.Iface
	}
	if hop := report.Net.DefaultV6; hop != nil {
		n.DefaultGatewayV6, n.DefaultInterfaceV6 = hop.Gateway.String(), hop.Iface
	}
	for _, iface := range report.Net.Interfaces {
		n.Interfaces = append(n.Interfaces, iface.Name)
	}
	if !report.LastChange.At.IsZero() {
		n.LastChange = timestamppb.New(report.LastChange.At)
	}
	return n
}

// Resync makes the Reconciler re-read the network and rebuild what it owns.
func (m *Manager) Resync() error {
	if err := m.rec.Resync(); err != nil {
		return fmt.Errorf("resync: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshLocked()
	return nil
}

// RemoveStale deletes a route that Diagnostics reported as stale.
func (m *Manager) RemoveStale(key string) error {
	report := m.rec.Report()
	if !slices.ContainsFunc(report.Stale, func(s tunnel.StaleRoute) bool { return s.Key == key }) {
		return fmt.Errorf("stale route %q: %w", key, profile.ErrNotFound)
	}
	if err := m.rec.RemoveStale(key); err != nil {
		return fmt.Errorf("remove stale route %q: %w", key, err)
	}
	m.log.Info("removed a stale route", "key", key)
	return nil
}

// Logs returns the last tail lines of a profile's log, or of the daemon's own
// when profileID is empty, and then the live lines.
func (m *Manager) Logs(profileID string, tail int) ([]*pb.LogLine, <-chan *pb.LogLine, func(), error) {
	buffer := m.daemonLog
	if profileID != "" {
		e, err := m.lookup(profileID)
		if err != nil {
			return nil, nil, nil, err
		}
		buffer = e.logs
	}
	lines, live, cancel := buffer.Subscribe(tail)
	return lines, live, cancel, nil
}
