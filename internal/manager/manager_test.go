package manager_test

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/manager"
	"github.com/KoukeNeko/Plaitway/internal/manager/fake"
	"github.com/KoukeNeko/Plaitway/internal/profile"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

func ids(profiles []*pb.Profile) []string {
	var out []string
	for _, p := range profiles {
		out = append(out, p.Name)
	}
	return out
}

func TestImportListAndReorder(t *testing.T) {
	e := newEnv(t)
	a := e.importProfile("a", ovpnProfile)
	b := e.importProfile("b", wgProfile)
	c := e.importProfile("c", ovpnProfile)

	if got := ids(e.m.List()); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("order = %v", got)
	}
	if a.Kind != pb.ProfileKind_PROFILE_KIND_OPENVPN || b.Kind != pb.ProfileKind_PROFILE_KIND_WIREGUARD {
		t.Fatalf("kinds = %v, %v", a.Kind, b.Kind)
	}
	if a.State != pb.ProfileState_PROFILE_STATE_DISCONNECTED || a.DesiredEnabled || a.Status != nil {
		t.Fatalf("a new profile must be disconnected and idle: %v", a)
	}
	if a.Summary.Endpoints[0].Host != "vpn.example.com" || a.Summary.Routes[0] != "10.1.0.0/16" {
		t.Fatalf("summary = %v", a.Summary)
	}

	ordered, err := e.m.Reorder([]string{c.Id, a.Id, b.Id})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(ordered); !slices.Equal(got, []string{"c", "a", "b"}) {
		t.Fatalf("Reorder returned %v", got)
	}
	if got := ids(e.m.List()); !slices.Equal(got, []string{"c", "a", "b"}) {
		t.Fatalf("List after Reorder = %v", got)
	}
	if _, err := e.m.Reorder([]string{c.Id}); err == nil {
		t.Fatal("Reorder with a missing id succeeded")
	}
}

func TestImportErrorsAndWarnings(t *testing.T) {
	e := newEnv(t)

	_, err := e.m.Import(&pb.ImportProfileRequest{Content: []byte("client\n# fake: reject\n")})
	var invalid *profile.InvalidError
	if !errors.As(err, &invalid) || !strings.Contains(invalid.Error(), "line 2") {
		t.Fatalf("rejected import: %v, want an InvalidError naming the line", err)
	}
	if n := len(e.m.List()); n != 0 {
		t.Fatalf("%d profiles after a rejected import", n)
	}

	resp := e.importWith(&pb.ImportProfileRequest{Content: []byte("client\nremote h 1\nscript-security 2\nup /bin/x\n")})
	var directives []string
	for _, w := range resp.Warnings {
		directives = append(directives, fmt.Sprintf("%d:%s", w.Line, w.Directive))
	}
	if !slices.Equal(directives, []string{"3:script-security", "4:up"}) {
		t.Fatalf("warnings = %v", directives)
	}

	if _, err := e.m.Import(&pb.ImportProfileRequest{Content: []byte(ovpnProfile), Kind: 99}); !errors.As(err, &invalid) {
		t.Fatalf("unknown kind: %v, want an InvalidError", err)
	}
}

func TestEnableAndDisableLifecycle(t *testing.T) {
	e := newEnv(t)
	p := e.importProfile("office", ovpnProfile)
	w := e.watch()

	started := e.setEnabled(p.Id, true)
	if started.State != pb.ProfileState_PROFILE_STATE_CONNECTING || !started.DesiredEnabled {
		t.Fatalf("right after enabling: state %v desired %v", started.State, started.DesiredEnabled)
	}
	up := e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
	if up.Status.GetInterfaceName() == "" || up.Status.GetRemote() != "vpn.example.com:1194" || up.Status.GetConnectedSince() == nil || len(up.Status.GetAddresses()) == 0 {
		t.Fatalf("connected status incomplete: %v", up.Status)
	}
	eventually(t, "traffic counters to move", func() bool {
		cur := e.get(p.Id).Status
		return cur.GetRxBytes() > up.Status.GetRxBytes() && cur.GetTxBytes() > up.Status.GetTxBytes()
	})

	stopped := e.setEnabled(p.Id, false)
	if stopped.State != pb.ProfileState_PROFILE_STATE_DISCONNECTED || stopped.DesiredEnabled || stopped.Status != nil || stopped.LastError != "" {
		t.Fatalf("after disabling: %v", stopped)
	}
	want := []pb.ProfileState{
		pb.ProfileState_PROFILE_STATE_CONNECTING,
		pb.ProfileState_PROFILE_STATE_CONNECTED,
		pb.ProfileState_PROFILE_STATE_DISCONNECTING,
		pb.ProfileState_PROFILE_STATE_DISCONNECTED,
	}
	eventually(t, "the watcher to see every state", func() bool { return slices.Equal(w.states(p.Id), want) })
	if rep := e.rec.Report(); len(rep.Routes) != 0 || len(rep.DNS) != 0 {
		t.Fatalf("the engine left routes or DNS behind: %+v", rep)
	}
}

func TestEnableTwiceStartsOneEngine(t *testing.T) {
	stub := &stubBackend{kind: tunnel.KindOpenVPN}
	e := newEnv(t, withBackends(stub.backend()))
	p := e.importProfile("a", ovpnProfile)

	e.setEnabled(p.Id, true)
	e.setEnabled(p.Id, true)
	if n := stub.created.Load(); n != 1 {
		t.Fatalf("%d engines created, want 1", n)
	}
}

func TestSeveralProfilesRunAtOnce(t *testing.T) {
	e := newEnv(t)
	a := e.importProfile("a", ovpnProfile)
	b := e.importProfile("b", wgProfile)
	e.setEnabled(a.Id, true)
	e.setEnabled(b.Id, true)
	e.waitState(a.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
	e.waitState(b.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)

	ia, ib := e.get(a.Id).Status.InterfaceName, e.get(b.Id).Status.InterfaceName
	if ia == ib {
		t.Fatalf("both profiles use interface %s", ia)
	}
	// The WireGuard profile has AllowedIPs 0.0.0.0/0, the OpenVPN one a single route.
	var aRoutes, bRoutes []string
	for _, r := range e.get(a.Id).Status.Routes {
		aRoutes = append(aRoutes, r.Prefix)
	}
	for _, r := range e.get(b.Id).Status.Routes {
		bRoutes = append(bRoutes, r.Prefix)
	}
	if !slices.Equal(aRoutes, []string{"10.1.0.0/16"}) || !slices.Equal(bRoutes, []string{"0.0.0.0/1", "128.0.0.0/1"}) {
		t.Fatalf("routes: a %v, b %v", aRoutes, bRoutes)
	}
}

func TestTunnelModeOverridesTheProfile(t *testing.T) {
	e := newEnv(t)
	wg := e.importWith(&pb.ImportProfileRequest{Name: "wg", Content: []byte(wgProfile), Settings: &pb.ProfileSettings{TunnelMode: pb.TunnelMode_TUNNEL_MODE_SPLIT}}).Profile
	e.setEnabled(wg.Id, true)
	e.waitState(wg.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
	// Split mode drops the default route the profile asks for.
	if routes := e.get(wg.Id).Status.Routes; len(routes) != 0 {
		t.Fatalf("a split-tunnel profile got routes %v", routes)
	}

	// Settings of a connected profile apply to the next connection only.
	if _, err := e.m.Update(&pb.UpdateProfileRequest{Id: wg.Id, Settings: &pb.ProfileSettings{TunnelMode: pb.TunnelMode_TUNNEL_MODE_FULL}}); err != nil {
		t.Fatal(err)
	}
	if routes := e.get(wg.Id).Status.Routes; len(routes) != 0 {
		t.Fatalf("a running tunnel changed its routes after Update: %v", routes)
	}
	e.setEnabled(wg.Id, false)
	e.setEnabled(wg.Id, true)
	eventually(t, "the full tunnel", func() bool {
		p := e.get(wg.Id)
		return p.State == pb.ProfileState_PROFILE_STATE_CONNECTED && len(p.Status.Routes) == 2
	})
}

// What Parse reads from the profile text need not be valid UTF-8, which a
// protobuf string requires: it must not make ImportProfile or ListProfiles fail.
func TestStringsFromParseAreMadeValidForTheWire(t *testing.T) {
	backend := tunnel.Backend{
		Kind: tunnel.KindOpenVPN,
		Parse: func(content []byte) (tunnel.Parsed, error) {
			return tunnel.Parsed{
				Content:       content,
				Summary:       tunnel.Summary{Endpoints: []tunnel.Endpoint{{Host: "bad\xff.example", Port: 1}}},
				Warnings:      []tunnel.Warning{{Line: 1, Directive: "dir\xfe", Message: "msg\xff"}},
				SuggestedName: "sugg\xffested",
			}, nil
		},
		New:   (&stubBackend{kind: tunnel.KindOpenVPN}).backend().New,
		Probe: func() tunnel.EngineInfo { return tunnel.EngineInfo{Available: true} },
	}
	e := newEnv(t, withBackends(backend))
	resp := e.importWith(&pb.ImportProfileRequest{Content: []byte(ovpnProfile)})

	if _, err := proto.Marshal(resp); err != nil {
		t.Fatalf("the import response cannot be sent: %v", err)
	}
	for _, p := range e.m.List() {
		if _, err := proto.Marshal(p); err != nil {
			t.Fatalf("the profile cannot be sent: %v", err)
		}
	}
	if resp.Profile.Name != "suggested" {
		t.Fatalf("name = %q", resp.Profile.Name)
	}
}

func TestUpdate(t *testing.T) {
	e := newEnv(t)
	a := e.importProfile("a", ovpnProfile)
	e.importProfile("b", ovpnProfile)
	w := e.watch()

	name := "renamed"
	got, err := e.m.Update(&pb.UpdateProfileRequest{Id: a.Id, Name: &name, Settings: &pb.ProfileSettings{AutoConnect: true, TunnelMode: pb.TunnelMode_TUNNEL_MODE_FULL}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "renamed" || !got.Settings.AutoConnect || got.Settings.TunnelMode != pb.TunnelMode_TUNNEL_MODE_FULL || got.Settings.Priority != 1 {
		t.Fatalf("updated profile = %v", got)
	}
	eventually(t, "the change event", func() bool { return len(w.all()) == 1 })
	if ev := w.all()[0].GetChanged(); ev == nil || ev.Name != "renamed" {
		t.Fatalf("event = %v", w.all()[0])
	}

	_, err = e.m.Update(&pb.UpdateProfileRequest{Id: "nope", Name: &name})
	if !errors.Is(err, profile.ErrNotFound) {
		t.Fatalf("unknown id: %v, want ErrNotFound", err)
	}
	other := "B"
	var invalid *profile.InvalidError
	if _, err := e.m.Update(&pb.UpdateProfileRequest{Id: a.Id, Name: &other}); !errors.As(err, &invalid) {
		t.Fatalf("duplicate name: %v, want an InvalidError", err)
	}
}

func TestCredentialsFlow(t *testing.T) {
	e := newEnv(t)
	p := e.importProfile("asus", ovpnProfile+"auth-user-pass\n")
	if !p.Summary.RequiresCredentials {
		t.Fatalf("summary does not require credentials: %v", p.Summary)
	}
	if _, err := e.m.ProvideCredentials(&pb.ProvideCredentialsRequest{ProfileId: p.Id, Kind: pb.CredentialKind_CREDENTIAL_KIND_USER_PASSWORD, Username: "u", Password: "p"}); !errors.Is(err, manager.ErrNotAwaitingCredentials) {
		t.Fatalf("credentials for an idle profile: %v, want ErrNotAwaitingCredentials", err)
	}

	e.setEnabled(p.Id, true)
	asking := e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_AWAITING_CREDENTIALS)
	if asking.CredentialRequest.GetKind() != pb.CredentialKind_CREDENTIAL_KIND_USER_PASSWORD || asking.LastError != "" {
		t.Fatalf("request = %v", asking)
	}

	var invalid *profile.InvalidError
	if _, err := e.m.ProvideCredentials(&pb.ProvideCredentialsRequest{ProfileId: p.Id, Kind: pb.CredentialKind_CREDENTIAL_KIND_KEY_PASSPHRASE, Password: "x"}); !errors.As(err, &invalid) {
		t.Fatalf("wrong kind: %v, want an InvalidError", err)
	}
	if _, err := e.m.ProvideCredentials(&pb.ProvideCredentialsRequest{ProfileId: p.Id, Kind: pb.CredentialKind_CREDENTIAL_KIND_USER_PASSWORD, Username: "", Password: "x"}); !errors.As(err, &invalid) {
		t.Fatalf("empty user name: %v, want an InvalidError from the engine", err)
	}

	// The engine rejects the password "wrong" once: back to the request, with the reason.
	secret := "wrong"
	if _, err := e.m.ProvideCredentials(&pb.ProvideCredentialsRequest{ProfileId: p.Id, Kind: pb.CredentialKind_CREDENTIAL_KIND_USER_PASSWORD, Username: "alice", Password: secret}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the second request", func() bool {
		cur := e.get(p.Id)
		return cur.State == pb.ProfileState_PROFILE_STATE_AWAITING_CREDENTIALS && cur.LastError == "authentication failed"
	})

	good := "hunter2-correct"
	if _, err := e.m.ProvideCredentials(&pb.ProvideCredentialsRequest{ProfileId: p.Id, Kind: pb.CredentialKind_CREDENTIAL_KIND_USER_PASSWORD, Username: "alice", Password: good}); err != nil {
		t.Fatal(err)
	}
	up := e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
	if up.LastError != "" || up.CredentialRequest != nil {
		t.Fatalf("connected profile keeps stale fields: %v", up)
	}

	// Credentials are held in memory only and never logged, by anyone.
	lines, _, cancel, err := e.m.Logs(p.Id, 1000)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	for _, l := range lines {
		if strings.Contains(l.Text, good) || strings.Contains(l.Text, "alice") || strings.Contains(l.Text, secret) {
			t.Errorf("credentials in the profile log: %q", l.Text)
		}
	}
	assertNoSecretOnDisk(t, e.dir, good, "alice")
}

func TestProvidedCredentialsShowAsConnectingUntilTheEngineSaysOtherwise(t *testing.T) {
	var engine *stubEngine
	stub := &stubBackend{kind: tunnel.KindOpenVPN, onEngine: func(s *stubEngine) { engine = s }}
	e := newEnv(t, withBackends(stub.backend()))
	p := e.importProfile("a", ovpnProfile)
	e.setEnabled(p.Id, true)
	req := &pb.ProvideCredentialsRequest{ProfileId: p.Id, Kind: pb.CredentialKind_CREDENTIAL_KIND_USER_PASSWORD, Username: "u", Password: "p"}

	engine.send(tunnel.Status{State: tunnel.StateAwaitingCredentials, NeedsCredentials: tunnel.CredentialUserPassword})
	e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_AWAITING_CREDENTIALS)
	got, err := e.m.ProvideCredentials(req)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != pb.ProfileState_PROFILE_STATE_CONNECTING || got.CredentialRequest != nil {
		t.Fatalf("right after providing credentials: %v", got)
	}

	// The engine rejects them: the request comes back with the reason.
	engine.send(tunnel.Status{State: tunnel.StateAwaitingCredentials, NeedsCredentials: tunnel.CredentialUserPassword, Err: "authentication failed"})
	eventually(t, "the request to return", func() bool {
		cur := e.get(p.Id)
		return cur.State == pb.ProfileState_PROFILE_STATE_AWAITING_CREDENTIALS && cur.LastError == "authentication failed"
	})
}

func TestFailedEngineKeepsDesiredAndCanBeRetried(t *testing.T) {
	e := newEnv(t)
	p := e.importProfile("broken", ovpnProfile+"# fake: fail\n")

	e.setEnabled(p.Id, true)
	failed := e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_FAILED)
	if failed.LastError == "" || !failed.DesiredEnabled {
		t.Fatalf("failed profile: error %q desired %v", failed.LastError, failed.DesiredEnabled)
	}
	// The daemon does not retry behind the user's back.
	time.Sleep(100 * time.Millisecond)
	if cur := e.get(p.Id); cur.State != pb.ProfileState_PROFILE_STATE_FAILED {
		t.Fatalf("a failed profile moved to %v by itself", cur.State)
	}

	retry := e.setEnabled(p.Id, true)
	if retry.State != pb.ProfileState_PROFILE_STATE_CONNECTING || retry.LastError != "" {
		t.Fatalf("retry: %v", retry)
	}
	e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_FAILED)

	off := e.setEnabled(p.Id, false)
	if off.State != pb.ProfileState_PROFILE_STATE_DISCONNECTED || off.LastError != "" || off.DesiredEnabled {
		t.Fatalf("after disabling a failed profile: %v", off)
	}
}

func TestEngineThatCannotStartFailsTheProfile(t *testing.T) {
	for name, stub := range map[string]*stubBackend{
		"New fails":   {kind: tunnel.KindOpenVPN, newErr: errors.New("openvpn binary not found")},
		"Start fails": {kind: tunnel.KindOpenVPN, startErr: errors.New("cannot create the interface")},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, withBackends(stub.backend()))
			p := e.importProfile("a", ovpnProfile)

			got := e.setEnabled(p.Id, true)
			if got.State != pb.ProfileState_PROFILE_STATE_FAILED || !got.DesiredEnabled ||
				!strings.Contains(got.LastError, "openvpn binary not found") && !strings.Contains(got.LastError, "cannot create the interface") {
				t.Fatalf("profile = %v", got)
			}
		})
	}
}

func TestUnavailableEngineFailsTheProfileWithItsReason(t *testing.T) {
	stub := &stubBackend{kind: tunnel.KindOpenVPN, unavailable: "openvpn was not found at /nowhere/openvpn"}
	e := newEnv(t, withBackends(stub.backend()))
	p := e.importProfile("a", ovpnProfile)

	got := e.setEnabled(p.Id, true)
	if got.State != pb.ProfileState_PROFILE_STATE_FAILED || !strings.Contains(got.LastError, "openvpn was not found at /nowhere/openvpn") {
		t.Fatalf("profile = %v", got)
	}
	if n := stub.created.Load(); n != 0 {
		t.Fatalf("%d engines were created for an unavailable backend", n)
	}
	info := e.m.Info().Engines[0]
	if info.Available || info.Detail != "openvpn was not found at /nowhere/openvpn" {
		t.Fatalf("DaemonInfo says %v", info)
	}
}

// Engines are asked to withdraw, but a stuck or buggy one must not leave
// routes behind.
func TestStoppingAnEngineWithdrawsItFromTheReconcilerWhateverItDid(t *testing.T) {
	rec := fake.NewReconciler()
	stub := &stubBackend{kind: tunnel.KindOpenVPN, onEngine: func(s *stubEngine) {
		// The engine announces routes and, being buggy, never withdraws.
		_ = rec.Announce(tunnel.Intent{Owner: s.spec.Owner, State: tunnel.StateUp, Iface: "utun7", Routes: []netip.Prefix{netip.MustParsePrefix("10.5.0.0/16")}})
	}}
	e := newEnv(t, withBackends(stub.backend()), withReconciler(rec))
	p := e.importProfile("a", ovpnProfile)
	e.setEnabled(p.Id, true)
	if len(rec.Report().Routes) != 1 {
		t.Fatalf("routes = %+v, want the announced one", rec.Report().Routes)
	}

	e.setEnabled(p.Id, false)
	if routes := rec.Report().Routes; len(routes) != 0 {
		t.Fatalf("routes of a stopped engine remain: %+v", routes)
	}
}

func TestStartedEngineThatFailedToStartIsStopped(t *testing.T) {
	var engines []*stubEngine
	stub := &stubBackend{kind: tunnel.KindOpenVPN, startErr: errors.New("boom"), onEngine: func(s *stubEngine) { engines = append(engines, s) }}
	e := newEnv(t, withBackends(stub.backend()))
	p := e.importProfile("a", ovpnProfile)
	e.setEnabled(p.Id, true)

	if len(engines) != 1 || !engines[0].stopped.Load() {
		t.Fatal("an engine whose Start failed was not stopped, it may hold an interface or a process")
	}
}

func TestDeleteDisconnectsFirst(t *testing.T) {
	e := newEnv(t)
	p := e.importProfile("a", ovpnProfile)
	e.setEnabled(p.Id, true)
	e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
	w := e.watch()

	if err := e.m.Delete(p.Id); err != nil {
		t.Fatal(err)
	}
	if e.get(p.Id) != nil {
		t.Fatal("profile still listed")
	}
	if rep := e.rec.Report(); len(rep.Routes) != 0 {
		t.Fatalf("routes of the deleted profile remain: %+v", rep.Routes)
	}
	eventually(t, "the removed event", func() bool {
		for _, ev := range w.all() {
			if ev.GetRemoved() == p.Id {
				return true
			}
		}
		return false
	})
	if got := w.states(p.Id); !slices.Equal(got, []pb.ProfileState{pb.ProfileState_PROFILE_STATE_DISCONNECTING, pb.ProfileState_PROFILE_STATE_DISCONNECTED}) {
		t.Fatalf("states before removal = %v, want it to disconnect first", got)
	}
	if err := e.m.Delete(p.Id); !errors.Is(err, profile.ErrNotFound) {
		t.Fatalf("second Delete: %v, want ErrNotFound", err)
	}
	if _, err := e.m.SetEnabled(p.Id, true); !errors.Is(err, profile.ErrNotFound) {
		t.Fatalf("SetEnabled of a deleted profile: %v, want ErrNotFound", err)
	}
}

func TestProfilesAndAutoConnectSurviveARestart(t *testing.T) {
	dir := t.TempDir()
	first := startEnv(t, dir)
	auto := first.importWith(&pb.ImportProfileRequest{Name: "auto", Content: []byte(ovpnProfile), Settings: &pb.ProfileSettings{AutoConnect: true}}).Profile
	manual := first.importProfile("manual", wgProfile)
	first.setEnabled(manual.Id, true) // desired state is not persisted, only auto_connect
	first.shutdown()

	second := startEnv(t, dir)
	if got := ids(second.m.List()); !slices.Equal(got, []string{"auto", "manual"}) {
		t.Fatalf("profiles after restart = %v", got)
	}
	right := second.get(auto.Id)
	if !right.DesiredEnabled || right.State == pb.ProfileState_PROFILE_STATE_DISCONNECTED {
		t.Fatalf("the auto_connect profile was not started: %v", right)
	}
	second.waitState(auto.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
	if m := second.get(manual.Id); m.DesiredEnabled || m.State != pb.ProfileState_PROFILE_STATE_DISCONNECTED {
		t.Fatalf("a profile without auto_connect was started: %v", m)
	}
	if a := second.get(auto.Id); a.Summary.Endpoints[0].Host != "vpn.example.com" || a.Kind != pb.ProfileKind_PROFILE_KIND_OPENVPN {
		t.Fatalf("summary or kind lost: %v", a)
	}
}

func TestNetworkChangeRebindsEveryEngine(t *testing.T) {
	e := newEnv(t)
	a := e.importProfile("a", ovpnProfile)
	b := e.importProfile("b", wgProfile)
	idle := e.importProfile("idle", ovpnProfile)
	e.setEnabled(a.Id, true)
	e.setEnabled(b.Id, true)
	e.waitState(a.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
	e.waitState(b.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
	w := e.watch()

	e.rec.NetworkChanged()

	for _, id := range []string{a.Id, b.Id} {
		eventually(t, id+" to reconnect", func() bool {
			s := w.states(id)
			return slices.Contains(s, pb.ProfileState_PROFILE_STATE_RECONNECTING) && s[len(s)-1] == pb.ProfileState_PROFILE_STATE_CONNECTED
		})
	}
	if s := w.states(idle.Id); len(s) != 0 {
		t.Fatalf("an idle profile changed: %v", s)
	}
}

func TestRebindIsCalledOnEveryEngine(t *testing.T) {
	var mu sync.Mutex
	var engines []*stubEngine
	stub := &stubBackend{kind: tunnel.KindOpenVPN, onEngine: func(s *stubEngine) { mu.Lock(); engines = append(engines, s); mu.Unlock() }}
	e := newEnv(t, withBackends(stub.backend()))
	for _, name := range []string{"a", "b", "c"} {
		e.setEnabled(e.importProfile(name, ovpnProfile).Id, true)
	}

	e.rec.NetworkChanged()
	eventually(t, "every engine to be rebound", func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, s := range engines {
			if s.rebinds.Load() == 0 {
				return false
			}
		}
		return len(engines) == 3
	})
}

func TestConflictStatusesReachTheProfile(t *testing.T) {
	e := newEnv(t)
	p := e.importProfile("clash", ovpnProfile+"# fake: conflict\n")
	e.setEnabled(p.Id, true)
	up := e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)

	states := map[pb.RouteState]*pb.RouteStatus{}
	for _, r := range up.Status.Routes {
		states[r.State] = r
	}
	for _, want := range []pb.RouteState{pb.RouteState_ROUTE_STATE_INSTALLED, pb.RouteState_ROUTE_STATE_SHADOWED, pb.RouteState_ROUTE_STATE_BLOCKED} {
		if states[want] == nil {
			t.Fatalf("no %v route among %v", want, up.Status.Routes)
		}
	}
	if s := states[pb.RouteState_ROUTE_STATE_SHADOWED]; s.ShadowedBy == "" || s.Detail == "" {
		t.Errorf("shadowed route lacks its explanation: %v", s)
	}
	if s := states[pb.RouteState_ROUTE_STATE_BLOCKED]; s.Detail == "" {
		t.Errorf("blocked route lacks its explanation: %v", s)
	}
	if len(up.Status.Dns) != 1 || up.Status.Dns[0].State != pb.RouteState_ROUTE_STATE_INSTALLED || len(up.Status.Dns[0].Servers) == 0 {
		t.Errorf("dns = %v", up.Status.Dns)
	}
}

// A real Reconciler reports every owner's routes; each profile must show only
// its own, and the engine's warnings must come along.
func TestEngineWarningsAndRouteStatusesAreAggregated(t *testing.T) {
	var engine *stubEngine
	stub := &stubBackend{kind: tunnel.KindOpenVPN, onEngine: func(s *stubEngine) { engine = s }}
	rec := fake.NewReconciler()
	e := newEnv(t, withBackends(stub.backend()), withReconciler(rec))
	p := e.importProfile("a", ovpnProfile)
	other := e.importProfile("other", ovpnProfile)
	e.setEnabled(p.Id, true)

	engine.send(tunnel.Status{State: tunnel.StateUp, Iface: "utun9", Remote: "r:1", Warnings: []string{"compression is enabled"}})
	announce := func(owner string, routes ...string) {
		in := tunnel.Intent{Owner: tunnel.OwnerID(owner), State: tunnel.StateUp, Iface: "utun9",
			DNS: []tunnel.DNSIntent{{Servers: []netip.Addr{netip.MustParseAddr("10.1.0.1")}, MatchDomains: []string{"corp.example"}}}}
		for _, r := range routes {
			in.Routes = append(in.Routes, netip.MustParsePrefix(r))
		}
		if err := rec.Announce(in); err != nil {
			t.Fatal(err)
		}
	}
	announce(p.Id, "10.1.0.0/16", "192.168.0.0/24")
	announce(other.Id, "172.16.0.0/12")

	eventually(t, "the Reconciler's report to reach the profile", func() bool { return len(e.get(p.Id).GetStatus().GetRoutes()) == 2 })
	got := e.get(p.Id)
	if !slices.Equal(got.Status.Warnings, []string{"compression is enabled"}) {
		t.Fatalf("warnings = %v", got.Status.Warnings)
	}
	if r := got.Status.Routes; r[0].Prefix != "10.1.0.0/16" || r[0].State != pb.RouteState_ROUTE_STATE_INSTALLED ||
		r[1].Prefix != "192.168.0.0/24" || r[1].State != pb.RouteState_ROUTE_STATE_BLOCKED {
		t.Fatalf("routes = %v", r)
	}
	if d := got.Status.Dns; len(d) != 1 || d[0].Servers[0] != "10.1.0.1" || d[0].MatchDomains[0] != "corp.example" {
		t.Fatalf("dns = %v", d)
	}
	if r := e.get(other.Id).Status; r != nil {
		t.Fatalf("an idle profile got status %v", r)
	}
}

func TestSlowWatcherIsDroppedAndOthersKeepWorking(t *testing.T) {
	e := newEnv(t)
	p := e.importProfile("a", ovpnProfile)
	slow, events, cancel := e.m.Watch() // never read from
	_ = slow
	defer cancel()
	healthy := e.watch()

	for i := range 200 {
		name := fmt.Sprintf("name %d", i)
		if _, err := e.m.Update(&pb.UpdateProfileRequest{Id: p.Id, Name: &name}); err != nil {
			t.Fatal(err)
		}
	}

	drained := 0
	for range events { // the channel is closed once the watcher was dropped
		drained++
	}
	if drained == 0 || drained > 70 {
		t.Fatalf("slow watcher received %d events before being dropped", drained)
	}
	eventually(t, "the healthy watcher to see everything", func() bool { return len(healthy.all()) == 200 })
	if healthy.wasClosed() {
		t.Fatal("a healthy watcher was dropped")
	}
}

func TestCancelledWatchReleasesSubscriber(t *testing.T) {
	e := newEnv(t)
	w := e.watch()
	if n := e.m.Watchers(); n != 1 {
		t.Fatalf("watchers = %d, want 1", n)
	}
	w.cancel()
	if n := e.m.Watchers(); n != 0 {
		t.Fatalf("watchers after cancel = %d, want 0", n)
	}
	w.cancel() // idempotent
}

func TestWatchSnapshotThenChangesIncludingRemoved(t *testing.T) {
	e := newEnv(t)
	a := e.importProfile("a", ovpnProfile)
	w := e.watch()
	if len(w.snapshot) != 1 || w.snapshot[0].Id != a.Id {
		t.Fatalf("snapshot = %v", w.snapshot)
	}

	b := e.importProfile("b", wgProfile)
	if err := e.m.Delete(a.Id); err != nil {
		t.Fatal(err)
	}
	eventually(t, "events", func() bool { return len(w.all()) == 2 })
	evs := w.all()
	if evs[0].GetChanged().GetId() != b.Id || evs[1].GetRemoved() != a.Id {
		t.Fatalf("events = %v", evs)
	}
}

func TestDiagnostics(t *testing.T) {
	e := newEnv(t)
	p := e.importProfile("wg", wgProfile)
	e.setEnabled(p.Id, true)
	e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)

	d := e.m.Diagnostics()
	if d.Daemon.Version != "test" || !d.Daemon.Privileged || d.Daemon.ProtocolVersion != manager.ProtocolVersion {
		t.Fatalf("daemon = %v", d.Daemon)
	}
	var kinds []pb.ProfileKind
	for _, en := range d.Daemon.Engines {
		kinds = append(kinds, en.Kind)
		if !en.Available || en.Version != "fake" {
			t.Errorf("engine %v = %v", en.Kind, en)
		}
	}
	if !slices.Equal(kinds, []pb.ProfileKind{pb.ProfileKind_PROFILE_KIND_OPENVPN, pb.ProfileKind_PROFILE_KIND_WIREGUARD}) {
		t.Fatalf("engines = %v", kinds)
	}
	if d.Network.DefaultGatewayV4 == "" || d.Network.DefaultInterfaceV4 == "" || len(d.Network.Interfaces) == 0 || d.Network.LastChange == nil || d.Network.LastChangeReason == "" {
		t.Fatalf("network = %v", d.Network)
	}
	kindsSeen := map[pb.RouteKind]bool{}
	for _, r := range d.OwnedRoutes {
		if r.Owner != p.Id {
			t.Errorf("route %v owned by %q", r, r.Owner)
		}
		kindsSeen[r.Kind] = true
	}
	if !kindsSeen[pb.RouteKind_ROUTE_KIND_DEFAULT] || !kindsSeen[pb.RouteKind_ROUTE_KIND_BYPASS] {
		t.Fatalf("owned routes = %v, want default halves and a bypass route for the full tunnel", d.OwnedRoutes)
	}
	if len(d.StaleRoutes) != 1 || d.StaleRoutes[0].Key == "" || d.StaleRoutes[0].Gateway == "" || d.StaleRoutes[0].Reason == "" {
		t.Fatalf("stale routes = %v", d.StaleRoutes)
	}
	if len(d.ResolverEntries) != 1 || len(d.RecentJournal) == 0 || d.RecentJournal[0].Time == nil {
		t.Fatalf("resolver entries %v, journal %v", d.ResolverEntries, d.RecentJournal)
	}
}

func TestResyncAndRemoveStale(t *testing.T) {
	e := newEnv(t)
	p := e.importProfile("a", ovpnProfile)
	e.setEnabled(p.Id, true)
	e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)

	key := e.m.Diagnostics().StaleRoutes[0].Key
	if err := e.m.RemoveStale("no-such-key"); !errors.Is(err, profile.ErrNotFound) {
		t.Fatalf("unknown key: %v, want ErrNotFound", err)
	}
	if err := e.m.RemoveStale(key); err != nil {
		t.Fatal(err)
	}
	if n := len(e.m.Diagnostics().StaleRoutes); n != 0 {
		t.Fatalf("%d stale routes after removing it", n)
	}
	if err := e.m.RemoveStale(key); !errors.Is(err, profile.ErrNotFound) {
		t.Fatalf("removing it twice: %v, want ErrNotFound", err)
	}

	before := e.m.Diagnostics().Network.LastChange.AsTime()
	if err := e.m.Resync(); err != nil {
		t.Fatal(err)
	}
	d := e.m.Diagnostics()
	if d.Network.LastChangeReason != "manual" || !d.Network.LastChange.AsTime().After(before) {
		t.Fatalf("Resync did not reach the Reconciler: %v", d.Network)
	}
	if len(d.StaleRoutes) != 1 {
		t.Fatalf("the Reconciler's Resync result is not reflected: %v", d.StaleRoutes)
	}
}

func TestNewRejectsBadBackends(t *testing.T) {
	good := fake.Backends(fastFake)
	incomplete := good[0]
	incomplete.Probe = nil
	for name, backends := range map[string][]tunnel.Backend{
		"incomplete": {incomplete},
		"duplicate":  {good[0], good[0]},
		"no kind":    {{Parse: good[0].Parse, New: good[0].New, Probe: good[0].Probe}},
	} {
		_, err := manager.New(manager.Config{Log: discardLog(), StateDir: t.TempDir(), Backends: backends, Reconciler: fake.NewReconciler()})
		if err == nil {
			t.Errorf("%s: New succeeded", name)
		}
	}
}

func TestConcurrentUse(t *testing.T) {
	e := newEnv(t)
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := e.importProfile(fmt.Sprintf("p%d", i), ovpnProfile)
			for range 3 {
				if _, err := e.m.SetEnabled(p.Id, true); err != nil {
					t.Error(err)
				}
				e.m.List()
				e.m.Diagnostics()
				if _, err := e.m.SetEnabled(p.Id, false); err != nil {
					t.Error(err)
				}
			}
			if i%2 == 0 {
				if err := e.m.Delete(p.Id); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, events, cancel := e.m.Watch()
		defer cancel()
		deadline := time.After(300 * time.Millisecond)
		for {
			select {
			case <-events:
			case <-deadline:
				return
			}
		}
	}()
	wg.Wait()
	if n := len(e.m.List()); n != 3 {
		t.Fatalf("%d profiles left, want 3", n)
	}
}

func TestShutdownStopsEnginesInParallelThenTheReconciler(t *testing.T) {
	const engines, stopTime = 5, 200 * time.Millisecond
	var order orderRecorder
	stub := &stubBackend{kind: tunnel.KindOpenVPN, stopDelay: stopTime, onStop: func() { order.add("engine stopped") }}
	rec := &recordingReconciler{Reconciler: fake.NewReconciler(), order: &order}
	e := newEnv(t, withBackends(stub.backend()), withReconciler(rec))
	for i := range engines {
		e.setEnabled(e.importProfile(fmt.Sprintf("p%d", i), ovpnProfile).Id, true)
	}

	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.m.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(started); took > 3*stopTime {
		t.Fatalf("Shutdown took %v for %d engines that stop in %v each: they were not stopped in parallel", took, engines, stopTime)
	}
	if got := order.stoppedBeforeReconcilerEnded(); got != engines {
		t.Fatalf("%d engines were stopped before the Reconciler ended, want %d", got, engines)
	}
	if !rec.ended() {
		t.Fatal("the Reconciler's Run did not end")
	}
}

func TestStopTimeoutFailsTheProfileWithoutHanging(t *testing.T) {
	stub := &stubBackend{kind: tunnel.KindOpenVPN, blockStop: true}
	e := newEnv(t, withBackends(stub.backend()), withStopTimeout(100*time.Millisecond))
	p := e.importProfile("stuck", ovpnProfile)
	e.setEnabled(p.Id, true)

	started := time.Now()
	got := e.setEnabled(p.Id, false)
	if took := time.Since(started); took > 2*time.Second {
		t.Fatalf("disabling took %v with a 100ms stop timeout", took)
	}
	if got.State != pb.ProfileState_PROFILE_STATE_FAILED || !strings.Contains(got.LastError, "could not stop") || got.DesiredEnabled {
		t.Fatalf("profile = %v", got)
	}
}

func TestEngineThatClosesItsStatusWithoutBeingStoppedFailsTheProfile(t *testing.T) {
	var engine *stubEngine
	stub := &stubBackend{kind: tunnel.KindOpenVPN, onEngine: func(s *stubEngine) { engine = s }}
	e := newEnv(t, withBackends(stub.backend()))
	p := e.importProfile("a", ovpnProfile)
	e.setEnabled(p.Id, true)
	engine.send(tunnel.Status{State: tunnel.StateUp, Iface: "utun1"})
	e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)

	engine.closeStatus()
	failed := e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_FAILED)
	if failed.LastError == "" || !failed.DesiredEnabled {
		t.Fatalf("profile = %v", failed)
	}
}

func TestEngineThatFailsAndClosesKeepsItsOwnReason(t *testing.T) {
	var engine *stubEngine
	stub := &stubBackend{kind: tunnel.KindOpenVPN, onEngine: func(s *stubEngine) { engine = s }}
	e := newEnv(t, withBackends(stub.backend()))
	p := e.importProfile("a", ovpnProfile)
	e.setEnabled(p.Id, true)

	engine.send(tunnel.Status{State: tunnel.StateFailed, Err: "Cannot load inline certificate file"})
	engine.closeStatus()
	failed := e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_FAILED)
	// Give the consumer time to see the closed channel before reading.
	time.Sleep(50 * time.Millisecond)
	if got := e.get(p.Id).LastError; got != "Cannot load inline certificate file" {
		t.Fatalf("last_error = %q (profile %v), want the engine's own reason", got, failed)
	}
}

func TestReconcilerThatStopsByItselfIsFatal(t *testing.T) {
	rec := &exitingReconciler{Reconciler: fake.NewReconciler(), err: errors.New("lost the routing socket")}
	e := newEnv(t, withReconciler(rec))
	select {
	case err := <-e.m.Fatal():
		if !strings.Contains(err.Error(), "lost the routing socket") {
			t.Fatalf("fatal error = %v", err)
		}
	case <-time.After(waitTimeout):
		t.Fatal("no fatal error")
	}
}

func TestSetEnabledAfterShutdownIsRefused(t *testing.T) {
	e := newEnv(t)
	p := e.importProfile("a", ovpnProfile)
	e.shutdown()
	if _, err := e.m.SetEnabled(p.Id, true); !errors.Is(err, manager.ErrShuttingDown) {
		t.Fatalf("SetEnabled after Shutdown: %v, want ErrShuttingDown", err)
	}
	if _, err := e.m.Import(&pb.ImportProfileRequest{Content: []byte(ovpnProfile)}); !errors.Is(err, manager.ErrShuttingDown) {
		t.Fatalf("Import after Shutdown: %v, want ErrShuttingDown", err)
	}
}
