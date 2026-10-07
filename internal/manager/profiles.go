package manager

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/proto"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/profile"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// List returns every profile in priority order.
func (m *Manager) List() []*pb.Profile {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked()
}

// Import validates and stores a profile. The new profile starts disconnected,
// also when it comes with on-demand rules: they apply from the next change of
// the network, or when rules are saved with Update.
func (m *Manager) Import(req *pb.ImportProfileRequest) (*pb.ImportProfileResponse, error) {
	kind, err := kindFromProto(req.Kind)
	if err != nil {
		return nil, err
	}
	settings, err := settingsFromProto(req.Settings)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return nil, ErrShuttingDown
	}
	res, err := m.store.Import(profile.ImportRequest{
		Name:           req.Name,
		Content:        req.Content,
		SourceFilename: req.SourceFilename,
		Kind:           kind,
		Settings:       settings,
	})
	if err != nil {
		return nil, err
	}
	e := m.addEntryLocked(res.Profile)
	m.log.Info("profile imported", "profile", e.id, "kind", kindToProto(res.Profile.Kind), "warnings", len(res.Warnings))

	return &pb.ImportProfileResponse{Profile: proto.Clone(e.last).(*pb.Profile), Warnings: warningsToProto(res.Warnings)}, nil
}

// warningsToProto lists what Parse stripped or ignored. The text comes from the
// profile and need not be valid UTF-8.
func warningsToProto(warnings []tunnel.Warning) []*pb.ImportWarning {
	var out []*pb.ImportWarning
	for _, w := range warnings {
		out = append(out, &pb.ImportWarning{
			Line:      int32(w.Line),
			Directive: sanitizeText(w.Directive, maxErrorText),
			Message:   sanitizeText(w.Message, maxErrorText),
		})
	}
	return out
}

// GetContent returns the stored profile text, with its private keys.
func (m *Manager) GetContent(id string) ([]byte, error) {
	return m.store.Content(id)
}

// UpdateContent replaces the text of a profile. With reconnect, an enabled
// profile (one that is connected, connecting or failed to start) is restarted
// with the new text at once; otherwise the text applies the next time the
// profile connects and a running engine is left alone. The text is never logged.
func (m *Manager) UpdateContent(req *pb.UpdateProfileContentRequest) (*pb.ImportProfileResponse, error) {
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return nil, ErrShuttingDown
	}
	e, ok := m.entries[req.Id]
	if !ok {
		m.mu.Unlock()
		return nil, notFound(req.Id)
	}
	res, err := m.store.UpdateContent(req.Id, req.Content)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	m.refreshLocked(e)
	restart := req.Reconnect && e.desired
	m.mu.Unlock()
	m.log.Info("profile text updated", "profile", e.id, "warnings", len(res.Warnings), "restart", restart)

	if restart {
		e.op.Lock()
		m.disable(context.Background(), e)
		e.op.Unlock()
		if err := m.enable(e); err != nil {
			return nil, err
		}
	}
	p, err := m.current(req.Id)
	if err != nil {
		return nil, err
	}
	return &pb.ImportProfileResponse{Profile: p, Warnings: warningsToProto(res.Warnings)}, nil
}

// Update changes the name or the settings. A connected profile keeps running
// with the settings it was started with, except for its priority, which the
// Reconciler uses from now on. New on-demand rules are applied to the current
// network before Update returns.
func (m *Manager) Update(req *pb.UpdateProfileRequest) (*pb.Profile, error) {
	change := profile.Change{Name: req.Name}
	if req.Settings != nil {
		settings, err := settingsFromProto(req.Settings)
		if err != nil {
			return nil, err
		}
		change.Settings = &settings
	}

	rulesChanged, err := m.updateStored(req.Id, change)
	if err != nil {
		return nil, err
	}
	m.refreshPriorities()
	if rulesChanged {
		m.applyOnDemand(req.Id)
	}
	return m.current(req.Id)
}

// updateStored stores the change and publishes the profile. It reports whether
// the on-demand rules are other than they were.
func (m *Manager) updateStored(id string, change profile.Change) (rulesChanged bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok {
		return false, notFound(id)
	}
	before, err := m.store.Get(id)
	if err != nil {
		return false, err
	}
	after, err := m.store.Update(id, change)
	if err != nil {
		return false, err
	}
	m.refreshLocked(e)
	return after.Settings.OnDemand != before.Settings.OnDemand, nil
}

// Delete disconnects the profile, then removes it.
func (m *Manager) Delete(id string) error {
	e, err := m.lookup(id)
	if err != nil {
		return err
	}
	e.op.Lock()
	defer e.op.Unlock()

	m.disable(context.Background(), e)

	m.mu.Lock()
	defer m.mu.Unlock()
	if e.deleted {
		return notFound(id)
	}
	if err := m.store.Delete(id); err != nil {
		return err
	}
	e.deleted = true
	delete(m.entries, id)
	e.logs.Close()
	m.broadcastLocked(&pb.ProfileEvent{Event: &pb.ProfileEvent_Removed{Removed: id}})
	m.log.Info("profile deleted", "profile", id)
	return nil
}

// Reorder sets the priority order: ids lists every profile, highest first. The
// tunnels that are up are arbitrated with the new order at once.
func (m *Manager) Reorder(ids []string) ([]*pb.Profile, error) {
	defer m.refreshPriorities() // after the unlock below
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.store.Reorder(ids); err != nil {
		return nil, err
	}
	m.refreshLocked()
	return m.snapshotLocked(), nil
}

// SetEnabled starts or stops the profile's engine. Enabling returns when the
// engine has started or failed to, or after StartWait; a profile whose engine
// fails is FAILED, not an error.
func (m *Manager) SetEnabled(id string, enabled bool) (*pb.Profile, error) {
	e, err := m.lookup(id)
	if err != nil {
		return nil, err
	}
	if enabled {
		if err := m.enable(e); err != nil {
			return nil, err
		}
	} else {
		e.op.Lock()
		m.disable(context.Background(), e)
		e.op.Unlock()
	}
	return m.current(id)
}

// ProvideCredentials forwards credentials to the engine that asked for them.
// They are not kept: not here, not on disk, not in a log.
func (m *Manager) ProvideCredentials(req *pb.ProvideCredentialsRequest) (*pb.Profile, error) {
	kind := credentialKindFromProto(req.Kind)

	m.mu.Lock()
	e, ok := m.entries[req.ProfileId]
	if !ok {
		m.mu.Unlock()
		return nil, notFound(req.ProfileId)
	}
	r := e.run
	if r == nil || e.stopping || r.status.State != tunnel.StateAwaitingCredentials {
		m.mu.Unlock()
		return nil, ErrNotAwaitingCredentials
	}
	if kind != r.status.NeedsCredentials {
		m.mu.Unlock()
		return nil, &profile.InvalidError{Err: fmt.Errorf("the profile did not ask for this kind of credentials")}
	}
	engine := r.engine
	m.mu.Unlock()

	if err := engine.ProvideCredentials(kind, req.Username, req.Password); err != nil {
		return nil, &profile.InvalidError{Err: err}
	}
	e.logs.Add(pb.LogLevel_LOG_LEVEL_INFO, "credentials provided")

	// The engine has the credentials and will say what became of them. Until
	// its next snapshot arrives the profile is connecting, not still asking.
	m.mu.Lock()
	defer m.mu.Unlock()
	if e.run == r && r.status.State == tunnel.StateAwaitingCredentials {
		r.status.State = tunnel.StateConnecting
		r.status.NeedsCredentials = tunnel.CredentialNone
		r.status.Err = ""
		m.refreshLocked(e)
	}
	if e.deleted {
		return nil, notFound(req.ProfileId)
	}
	return proto.Clone(e.last).(*pb.Profile), nil
}

// addEntryLocked starts tracking a stored profile and tells the watchers.
func (m *Manager) addEntryLocked(meta profile.Profile) *entry {
	e := &entry{
		id:    meta.ID,
		logs:  NewLogBuffer(meta.ID),
		state: pb.ProfileState_PROFILE_STATE_DISCONNECTED,
	}
	m.entries[meta.ID] = e
	m.refreshLocked(e)
	return e
}

// refreshPriorities has the Network of every running engine announce its
// intent again if the profile's priority changed since. It makes the calls to
// the Reconciler without Manager.mu.
func (m *Manager) refreshPriorities() {
	m.mu.Lock()
	var nets []*ownerNetwork
	for _, e := range m.entries {
		if e.run != nil && !e.stopping {
			nets = append(nets, e.run.net)
		}
	}
	m.mu.Unlock()
	for _, n := range nets {
		if err := n.refresh(); err != nil {
			m.log.Warn("could not apply the new priority", "profile", n.owner, "err", err)
		}
	}
}
