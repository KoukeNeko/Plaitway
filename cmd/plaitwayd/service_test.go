//go:build unix

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
)

func wantCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if got := status.Code(err); got != want {
		t.Fatalf("code = %v (%v), want %v", got, err, want)
	}
}

func TestGetDaemonInfo(t *testing.T) {
	h := newDaemonHarness(t)
	info, err := h.client.GetDaemonInfo(context.Background(), &pb.GetDaemonInfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != version || info.ProtocolVersion != 1 || !info.Privileged || len(info.Engines) != 2 {
		t.Fatalf("info = %v", info)
	}
	for _, e := range info.Engines {
		if !e.Available || e.Detail != "" {
			t.Errorf("engine %v = %v", e.Kind, e)
		}
	}
}

func TestImportValidProfiles(t *testing.T) {
	h := newDaemonHarness(t)

	resp, err := h.client.ImportProfile(context.Background(), &pb.ImportProfileRequest{
		Content:        []byte(ovpnProfile),
		SourceFilename: "/Users/me/Downloads/office.ovpn",
		Settings:       &pb.ProfileSettings{AutoConnect: true, TunnelMode: pb.TunnelMode_TUNNEL_MODE_SPLIT},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := resp.Profile
	if p.Name != "office" || p.Kind != pb.ProfileKind_PROFILE_KIND_OPENVPN || p.Id == "" || len(resp.Warnings) != 0 {
		t.Fatalf("openvpn import = %v", resp)
	}
	if p.State != pb.ProfileState_PROFILE_STATE_DISCONNECTED || p.DesiredEnabled || !p.Settings.AutoConnect ||
		p.Settings.TunnelMode != pb.TunnelMode_TUNNEL_MODE_SPLIT || p.Settings.Priority != 1 {
		t.Fatalf("a new profile must be idle, keep its settings and go first: %v", p)
	}
	if got := p.Summary.Endpoints[0]; got.Host != "vpn.example.com" || got.Port != 1194 || got.Protocol != "udp" {
		t.Fatalf("summary endpoint = %v", got)
	}

	resp, err = h.client.ImportProfile(context.Background(), &pb.ImportProfileRequest{Name: "Home", Content: []byte(wgProfile)})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Profile.Kind != pb.ProfileKind_PROFILE_KIND_WIREGUARD || resp.Profile.Settings.Priority != 2 || !resp.Profile.Summary.RedirectsDefaultRoute {
		t.Fatalf("wireguard import = %v", resp.Profile)
	}

	// An explicit kind is honoured for text that cannot be detected.
	resp, err = h.client.ImportProfile(context.Background(), &pb.ImportProfileRequest{Name: "Raw", Content: []byte("anything at all"), Kind: pb.ProfileKind_PROFILE_KIND_WIREGUARD})
	if err != nil || resp.Profile.Kind != pb.ProfileKind_PROFILE_KIND_WIREGUARD {
		t.Fatalf("explicit kind: %v, %v", resp, err)
	}
	if got := names(h.list()); !slices.Equal(got, []string{"office", "Home", "Raw"}) {
		t.Fatalf("list = %v", got)
	}
}

func TestImportRejectedProfilesAreInvalidArgument(t *testing.T) {
	h := newDaemonHarness(t)
	tests := []struct {
		name    string
		req     *pb.ImportProfileRequest
		message string
	}{
		{"rejected by Parse", &pb.ImportProfileRequest{Content: []byte("client\n# fake: reject\n")}, `line 2: rejected by "# fake: reject"`},
		{"empty", &pb.ImportProfileRequest{Kind: pb.ProfileKind_PROFILE_KIND_OPENVPN}, "profile is empty"},
		{"undetectable", &pb.ImportProfileRequest{Content: []byte("hello\n")}, "cannot tell"},
		{"oversized", &pb.ImportProfileRequest{Content: bytes.Repeat([]byte("a"), 1<<20+1), Kind: pb.ProfileKind_PROFILE_KIND_OPENVPN}, "limit"},
		{"unknown kind", &pb.ImportProfileRequest{Content: []byte(ovpnProfile), Kind: 99}, "unknown profile kind"},
		{"unknown tunnel mode", &pb.ImportProfileRequest{Content: []byte(ovpnProfile), Settings: &pb.ProfileSettings{TunnelMode: 99}}, "unknown tunnel mode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := h.client.ImportProfile(context.Background(), tt.req)
			wantCode(t, err, codes.InvalidArgument)
			if !strings.Contains(status.Convert(err).Message(), tt.message) {
				t.Fatalf("message = %q, want it to contain %q", status.Convert(err).Message(), tt.message)
			}
		})
	}
	if n := len(h.list()); n != 0 {
		t.Fatalf("%d profiles stored by rejected imports", n)
	}
}

func TestImportReturnsWarningsForStrippedDirectives(t *testing.T) {
	h := newDaemonHarness(t)
	resp, err := h.client.ImportProfile(context.Background(), &pb.ImportProfileRequest{
		Name:    "scripted",
		Content: []byte("client\nremote h 1194\nup /usr/local/bin/hook\nscript-security 2\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, w := range resp.Warnings {
		got = append(got, fmt.Sprintf("%d %s: %s", w.Line, w.Directive, w.Message))
	}
	if len(got) != 2 || !strings.HasPrefix(got[0], "3 up: ") || !strings.HasPrefix(got[1], "4 script-security: ") {
		t.Fatalf("warnings = %q", got)
	}
}

func TestUpdateProfile(t *testing.T) {
	h := newDaemonHarness(t)
	a := h.importProfile("a", ovpnProfile)
	h.importProfile("b", wgProfile)

	name := "Renamed"
	p, err := h.client.UpdateProfile(context.Background(), &pb.UpdateProfileRequest{
		Id: a.Id, Name: &name,
		Settings: &pb.ProfileSettings{AutoConnect: true, TunnelMode: pb.TunnelMode_TUNNEL_MODE_FULL},
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Renamed" || !p.Settings.AutoConnect || p.Settings.TunnelMode != pb.TunnelMode_TUNNEL_MODE_FULL || p.Settings.Priority != 1 {
		t.Fatalf("updated = %v", p)
	}

	// Only the settings: the name stays.
	p, err = h.client.UpdateProfile(context.Background(), &pb.UpdateProfileRequest{Id: a.Id, Settings: &pb.ProfileSettings{TunnelMode: pb.TunnelMode_TUNNEL_MODE_AUTO}})
	if err != nil || p.Name != "Renamed" || p.Settings.AutoConnect {
		t.Fatalf("settings-only update: %v, %v", p, err)
	}

	_, err = h.client.UpdateProfile(context.Background(), &pb.UpdateProfileRequest{Id: "nope", Name: &name})
	wantCode(t, err, codes.NotFound)
	taken := "b"
	_, err = h.client.UpdateProfile(context.Background(), &pb.UpdateProfileRequest{Id: a.Id, Name: &taken})
	wantCode(t, err, codes.InvalidArgument)
	empty := ""
	_, err = h.client.UpdateProfile(context.Background(), &pb.UpdateProfileRequest{Id: a.Id, Name: &empty})
	wantCode(t, err, codes.InvalidArgument)
}

func TestDeleteProfile(t *testing.T) {
	h := newDaemonHarness(t)
	a := h.importProfile("a", ovpnProfile)
	b := h.importProfile("b", wgProfile)
	h.setEnabled(a.Id, true)
	h.waitState(a.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
	w := h.watch()

	if _, err := h.client.DeleteProfile(context.Background(), &pb.DeleteProfileRequest{Id: a.Id}); err != nil {
		t.Fatal(err)
	}
	if got := names(h.list()); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("list = %v", got)
	}
	eventually(t, "the removed event", func() bool {
		for _, ev := range w.allEvents() {
			if ev.GetRemoved() == a.Id {
				return true
			}
		}
		return false
	})
	// A connected profile is disconnected before it goes.
	want := []pb.ProfileState{pb.ProfileState_PROFILE_STATE_DISCONNECTING, pb.ProfileState_PROFILE_STATE_DISCONNECTED}
	if got := w.states(a.Id); !slices.Equal(got, want) {
		t.Fatalf("states before removal = %v, want %v", got, want)
	}

	_, err := h.client.DeleteProfile(context.Background(), &pb.DeleteProfileRequest{Id: a.Id})
	wantCode(t, err, codes.NotFound)
	if b2 := h.get(b.Id); b2 == nil {
		t.Fatal("the other profile went with it")
	}
}

func TestReorderProfiles(t *testing.T) {
	h := newDaemonHarness(t)
	a := h.importProfile("a", ovpnProfile)
	b := h.importProfile("b", wgProfile)
	c := h.importProfile("c", ovpnProfile)
	w := h.watch()

	resp, err := h.client.ReorderProfiles(context.Background(), &pb.ReorderProfilesRequest{Ids: []string{c.Id, a.Id, b.Id}})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(resp.Profiles); !slices.Equal(got, []string{"c", "a", "b"}) {
		t.Fatalf("response order = %v", got)
	}
	for i, p := range resp.Profiles {
		if int(p.Settings.Priority) != i+1 {
			t.Errorf("%s priority = %d, want %d", p.Name, p.Settings.Priority, i+1)
		}
	}
	if got := names(h.list()); !slices.Equal(got, []string{"c", "a", "b"}) {
		t.Fatalf("list order = %v", got)
	}
	// Watchers learn the new priorities from one event per changed profile.
	eventually(t, "events for the reordered profiles", func() bool { return len(w.allEvents()) >= 3 })

	_, err = h.client.ReorderProfiles(context.Background(), &pb.ReorderProfilesRequest{Ids: []string{a.Id}})
	wantCode(t, err, codes.InvalidArgument)
	_, err = h.client.ReorderProfiles(context.Background(), &pb.ReorderProfilesRequest{Ids: []string{a.Id, b.Id, "nope"}})
	wantCode(t, err, codes.NotFound)
	if got := names(h.list()); !slices.Equal(got, []string{"c", "a", "b"}) {
		t.Fatalf("a rejected reorder changed the order to %v", got)
	}
}

func TestEnableAndDisable(t *testing.T) {
	h := newDaemonHarness(t)
	p := h.importProfile("office", ovpnProfile)
	w := h.watch()

	started := h.setEnabled(p.Id, true)
	if started.State != pb.ProfileState_PROFILE_STATE_CONNECTING || !started.DesiredEnabled {
		t.Fatalf("after enabling: %v", started)
	}
	up := h.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
	if up.Status.InterfaceName == "" || up.Status.Remote != "vpn.example.com:1194" || up.Status.ConnectedSince == nil || len(up.Status.Routes) != 1 || len(up.Status.Dns) != 1 {
		t.Fatalf("connected status = %v", up.Status)
	}
	eventually(t, "the counters to move", func() bool { return h.get(p.Id).Status.RxBytes > up.Status.RxBytes })

	stopped := h.setEnabled(p.Id, false)
	if stopped.State != pb.ProfileState_PROFILE_STATE_DISCONNECTED || stopped.DesiredEnabled || stopped.Status != nil {
		t.Fatalf("after disabling: %v", stopped)
	}
	want := []pb.ProfileState{
		pb.ProfileState_PROFILE_STATE_CONNECTING,
		pb.ProfileState_PROFILE_STATE_CONNECTED,
		pb.ProfileState_PROFILE_STATE_DISCONNECTING,
		pb.ProfileState_PROFILE_STATE_DISCONNECTED,
	}
	eventually(t, "the watcher to see every state", func() bool { return slices.Equal(w.states(p.Id), want) })

	_, err := h.client.SetProfileEnabled(context.Background(), &pb.SetProfileEnabledRequest{Id: "nope", Enabled: true})
	wantCode(t, err, codes.NotFound)
}

func TestSeveralProfilesConnectAtOnceAndFailuresStayApart(t *testing.T) {
	h := newDaemonHarness(t)
	a := h.importProfile("a", ovpnProfile)
	b := h.importProfile("b", wgProfile)
	broken := h.importProfile("broken", ovpnProfile+"# fake: fail\n")
	for _, p := range []*pb.Profile{a, b, broken} {
		h.setEnabled(p.Id, true)
	}
	h.waitState(a.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
	h.waitState(b.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
	failed := h.waitState(broken.Id, pb.ProfileState_PROFILE_STATE_FAILED)
	if failed.LastError == "" || !failed.DesiredEnabled {
		t.Fatalf("failed profile = %v", failed)
	}
}

func TestCredentialsFlowIncludingAWrongPassword(t *testing.T) {
	h := newDaemonHarness(t)
	p := h.importProfile("asus", ovpnProfile+"auth-user-pass\n")
	provide := func(kind pb.CredentialKind, user, password string) (*pb.Profile, error) {
		return h.client.ProvideCredentials(context.Background(), &pb.ProvideCredentialsRequest{ProfileId: p.Id, Kind: kind, Username: user, Password: password})
	}

	_, err := provide(pb.CredentialKind_CREDENTIAL_KIND_USER_PASSWORD, "u", "p")
	wantCode(t, err, codes.FailedPrecondition)
	_, err = h.client.ProvideCredentials(context.Background(), &pb.ProvideCredentialsRequest{ProfileId: "nope"})
	wantCode(t, err, codes.NotFound)

	h.setEnabled(p.Id, true)
	asking := h.waitState(p.Id, pb.ProfileState_PROFILE_STATE_AWAITING_CREDENTIALS)
	if asking.CredentialRequest.GetKind() != pb.CredentialKind_CREDENTIAL_KIND_USER_PASSWORD {
		t.Fatalf("credential request = %v", asking.CredentialRequest)
	}

	_, err = provide(pb.CredentialKind_CREDENTIAL_KIND_KEY_PASSPHRASE, "", "x")
	wantCode(t, err, codes.InvalidArgument)
	_, err = provide(pb.CredentialKind_CREDENTIAL_KIND_USER_PASSWORD, "", "x")
	wantCode(t, err, codes.InvalidArgument)

	if _, err := provide(pb.CredentialKind_CREDENTIAL_KIND_USER_PASSWORD, "alice", "wrong"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the prompt to come back with the reason", func() bool {
		cur := h.get(p.Id)
		return cur.State == pb.ProfileState_PROFILE_STATE_AWAITING_CREDENTIALS && cur.LastError == "authentication failed"
	})

	if _, err := provide(pb.CredentialKind_CREDENTIAL_KIND_USER_PASSWORD, "alice", "s3cret-passphrase"); err != nil {
		t.Fatal(err)
	}
	up := h.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
	if up.LastError != "" || up.CredentialRequest != nil {
		t.Fatalf("connected profile keeps stale fields: %v", up)
	}

	// The daemon never logs credentials and never writes them down.
	for _, id := range []string{p.Id, ""} {
		for _, line := range readLogs(t, h, id, 1000) {
			if strings.Contains(line.Text, "s3cret-passphrase") || strings.Contains(line.Text, "alice") {
				t.Errorf("credentials in the log of %q: %q", id, line.Text)
			}
		}
	}
	filepath.WalkDir(h.stateDir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if data, _ := os.ReadFile(path); bytes.Contains(data, []byte("s3cret-passphrase")) {
				t.Errorf("%s holds the password", path)
			}
		}
		return nil
	})
}

// readLogs returns the buffered tail of a log, without waiting for more.
func readLogs(t *testing.T, h *harness, profileID string, tail int32) []*pb.LogLine {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := h.client.WatchLogs(ctx, &pb.WatchLogsRequest{ProfileId: profileID, TailLines: tail})
	if err != nil {
		t.Fatal(err)
	}
	lines := make(chan *pb.LogLine)
	go func() {
		defer close(lines)
		for {
			l, err := stream.Recv()
			if err != nil {
				return
			}
			select {
			case lines <- l:
			case <-ctx.Done():
				return
			}
		}
	}()
	var out []*pb.LogLine
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				return out
			}
			out = append(out, l)
		case <-time.After(150 * time.Millisecond): // the tail has been sent, only live lines would follow
			return out
		}
	}
}

func TestWatchLogsTailThenLive(t *testing.T) {
	h := newDaemonHarness(t)
	p := h.importProfile("a", ovpnProfile)
	h.setEnabled(p.Id, true)
	h.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := h.client.WatchLogs(ctx, &pb.WatchLogsRequest{ProfileId: p.Id, TailLines: 2})
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan *pb.LogLine, 100)
	go func() {
		for {
			l, err := stream.Recv()
			if err != nil {
				return
			}
			received <- l
		}
	}()
	next := func() *pb.LogLine {
		t.Helper()
		select {
		case l := <-received:
			return l
		case <-time.After(waitTimeout):
			t.Fatal("no log line")
			return nil
		}
	}

	// The tail: the last two lines the engine wrote while connecting.
	first, second := next(), next()
	if first.ProfileId != p.Id || first.Time == nil || first.Text == "" || first.Level == pb.LogLevel_LOG_LEVEL_UNSPECIFIED {
		t.Fatalf("log line = %v", first)
	}
	if !strings.Contains(second.Text, "connected on utun") {
		t.Fatalf("the newest buffered line = %q", second.Text)
	}

	// Then live lines.
	h.setEnabled(p.Id, false)
	var live []string
	for len(live) < 1 || !strings.Contains(live[len(live)-1], "stopping") {
		live = append(live, next().Text)
	}
}

func TestWatchLogsOfTheDaemonItself(t *testing.T) {
	h := newDaemonHarness(t)
	h.importProfile("a", ovpnProfile) // logs "profile imported" at Info

	var found bool
	eventually(t, "the daemon's own log to mention the import", func() bool {
		for _, l := range readLogs(t, h, "", 0) { // 0 means the default tail
			if strings.HasPrefix(l.Text, "profile imported") && l.ProfileId == "" {
				found = true
			}
		}
		return found
	})
}

func TestWatchLogsErrors(t *testing.T) {
	h := newDaemonHarness(t)
	recvErr := func(req *pb.WatchLogsRequest) error {
		stream, err := h.client.WatchLogs(context.Background(), req)
		if err != nil {
			return err
		}
		_, err = stream.Recv()
		return err
	}
	wantCode(t, recvErr(&pb.WatchLogsRequest{ProfileId: "nope"}), codes.NotFound)
	wantCode(t, recvErr(&pb.WatchLogsRequest{TailLines: -1}), codes.InvalidArgument)
}

func TestWatchLogsDefaultsToTwoHundredLines(t *testing.T) {
	h := newDaemonHarness(t)
	p := h.importProfile("a", ovpnProfile)
	for range 120 { // each connect and disconnect writes a few lines
		h.setEnabled(p.Id, true)
		h.setEnabled(p.Id, false)
	}
	if n := len(readLogs(t, h, p.Id, 0)); n != 200 {
		t.Fatalf("default tail = %d lines, want 200", n)
	}
	if n := len(readLogs(t, h, p.Id, 5)); n != 5 {
		t.Fatalf("tail of 5 = %d lines", n)
	}
}

func TestGetDiagnostics(t *testing.T) {
	h := newDaemonHarness(t)
	p := h.importProfile("clash", wgProfile+"# fake: conflict\n")
	h.setEnabled(p.Id, true)
	h.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)

	d, err := h.client.GetDiagnostics(context.Background(), &pb.GetDiagnosticsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Daemon == nil || d.Network.DefaultGatewayV4 == "" || len(d.OwnedRoutes) == 0 || len(d.StaleRoutes) != 1 || len(d.ResolverEntries) != 1 || len(d.RecentJournal) == 0 {
		t.Fatalf("diagnostics = %v", d)
	}
	states := map[pb.RouteState]bool{}
	for _, r := range d.OwnedRoutes {
		states[r.State] = true
	}
	if !states[pb.RouteState_ROUTE_STATE_SHADOWED] || !states[pb.RouteState_ROUTE_STATE_BLOCKED] || !states[pb.RouteState_ROUTE_STATE_INSTALLED] {
		t.Fatalf("route states = %v, want installed, shadowed and blocked", states)
	}
}

func TestResyncAndRemoveStaleRoute(t *testing.T) {
	h := newDaemonHarness(t)
	diagnostics := func() *pb.Diagnostics {
		d, err := h.client.GetDiagnostics(context.Background(), &pb.GetDiagnosticsRequest{})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}

	_, err := h.client.RemoveStaleRoute(context.Background(), &pb.RemoveStaleRouteRequest{Key: "nope"})
	wantCode(t, err, codes.NotFound)
	key := diagnostics().StaleRoutes[0].Key
	if _, err := h.client.RemoveStaleRoute(context.Background(), &pb.RemoveStaleRouteRequest{Key: key}); err != nil {
		t.Fatal(err)
	}
	if n := len(diagnostics().StaleRoutes); n != 0 {
		t.Fatalf("%d stale routes after removing it", n)
	}

	if _, err := h.client.Resync(context.Background(), &pb.ResyncRequest{}); err != nil {
		t.Fatal(err)
	}
	if got := diagnostics().Network.LastChangeReason; got != "manual" {
		t.Fatalf("last change reason after Resync = %q", got)
	}
}

func TestWatchSnapshotThenChangesIncludingRemoved(t *testing.T) {
	h := newDaemonHarness(t)
	a := h.importProfile("a", ovpnProfile)
	w := h.watch()
	if len(w.snapshot) != 1 || w.snapshot[0].Id != a.Id {
		t.Fatalf("snapshot = %v", w.snapshot)
	}

	b := h.importProfile("b", wgProfile)
	name := "a2"
	if _, err := h.client.UpdateProfile(context.Background(), &pb.UpdateProfileRequest{Id: a.Id, Name: &name}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.DeleteProfile(context.Background(), &pb.DeleteProfileRequest{Id: b.Id}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "three events", func() bool { return len(w.allEvents()) == 3 })
	evs := w.allEvents()
	if evs[0].GetChanged().GetId() != b.Id || evs[1].GetChanged().GetName() != "a2" || evs[2].GetRemoved() != b.Id {
		t.Fatalf("events = %v", evs)
	}
}

func TestCancelledWatchesReleaseSubscribers(t *testing.T) {
	h := newDaemonHarness(t)
	p := h.importProfile("a", ovpnProfile)

	profileWatch := h.watch()
	if n := h.d.mgr.Watchers(); n != 1 {
		t.Fatalf("watchers = %d, want 1", n)
	}
	profileWatch.cancel()
	eventually(t, "the profile watcher to be released", func() bool { return h.d.mgr.Watchers() == 0 })

	// A log watcher is a subscriber on the profile's log until it ends.
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := h.client.WatchLogs(ctx, &pb.WatchLogsRequest{ProfileId: p.Id})
	if err != nil {
		t.Fatal(err)
	}
	h.setEnabled(p.Id, true) // makes the stream deliver a line, so it is surely registered
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}
	if n := h.d.mgr.LogWatchers(p.Id); n != 1 {
		t.Fatalf("log watchers = %d, want 1", n)
	}
	cancel()
	eventually(t, "the log watcher to be released", func() bool { return h.d.mgr.LogWatchers(p.Id) == 0 })
}

func TestRestartKeepsProfilesAndStartsAutoConnect(t *testing.T) {
	stateDir := t.TempDir()
	first := startDaemon(t, stateDir, everyone())
	auto, err := first.client.ImportProfile(context.Background(), &pb.ImportProfileRequest{
		Name: "auto", Content: []byte(ovpnProfile), Settings: &pb.ProfileSettings{AutoConnect: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	manual := first.importProfile("manual", wgProfile)
	first.setEnabled(manual.Id, true)
	renamed := "kept name"
	if _, err := first.client.UpdateProfile(context.Background(), &pb.UpdateProfileRequest{Id: manual.Id, Name: &renamed}); err != nil {
		t.Fatal(err)
	}
	if err := first.stop(); err != nil {
		t.Fatalf("clean shutdown returned %v", err)
	}

	second := startDaemon(t, stateDir, everyone())
	if got := names(second.list()); !slices.Equal(got, []string{"auto", "kept name"}) {
		t.Fatalf("profiles after the restart = %v", got)
	}
	right := second.get(auto.Profile.Id)
	if !right.DesiredEnabled {
		t.Fatalf("the auto_connect profile is not wanted up after the restart: %v", right)
	}
	second.waitState(auto.Profile.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
	if m := second.get(manual.Id); m.DesiredEnabled || m.State != pb.ProfileState_PROFILE_STATE_DISCONNECTED {
		t.Fatalf("a profile without auto_connect came back %v", m)
	}
	if m := second.get(manual.Id); m.Summary.Endpoints[0].Host != "203.0.113.5" || m.Settings.Priority != 2 {
		t.Fatalf("the stored summary or priority was lost: %v", m)
	}
}

func TestShutdownEndsWatchesStopsEnginesAndReturnsCleanly(t *testing.T) {
	h := newDaemonHarness(t)
	p := h.importProfile("a", ovpnProfile)
	h.setEnabled(p.Id, true)
	h.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
	w := h.watch()

	started := time.Now()
	h.cancel()
	eventually(t, "the watch to end", func() bool { return w.streamErr() != nil })
	if got := status.Code(w.streamErr()); got != codes.Unavailable {
		t.Fatalf("watch ended with %v, want Unavailable", w.streamErr())
	}
	if err := h.stop(); err != nil {
		t.Fatalf("serve returned %v", err)
	}
	if took := time.Since(started); took > 3*time.Second {
		t.Fatalf("shutdown took %v", took)
	}
	if _, err := os.Stat(h.socket); !os.IsNotExist(err) {
		t.Fatalf("the socket file was left behind: %v", err)
	}
}

// What an unauthorized caller asks for must not happen, whichever call it is.
func TestDeniedCallsHaveNoSideEffects(t *testing.T) {
	skipIfRoot(t)
	stateDir := t.TempDir()

	setup := startDaemon(t, stateDir, everyone())
	keep := setup.importProfile("keep", ovpnProfile)
	other := setup.importProfile("other", wgProfile)
	before := setup.list()
	beforeContent := map[string]string{keep.Id: setup.content(keep.Id), other.Id: setup.content(other.Id)}
	if err := setup.stop(); err != nil {
		t.Fatal(err)
	}

	for name, pol := range map[string]*policy{"stranger": stranger(), "console user": consoleUserOnly()} {
		t.Run(name, func(t *testing.T) {
			h := startDaemon(t, stateDir, pol)
			ctx := context.Background()
			newName := "hacked"
			modifying := map[string]func() error{
				"ImportProfile": func() error {
					_, err := h.client.ImportProfile(ctx, &pb.ImportProfileRequest{Name: "evil", Content: []byte(ovpnProfile)})
					return err
				},
				"UpdateProfile": func() error {
					_, err := h.client.UpdateProfile(ctx, &pb.UpdateProfileRequest{Id: keep.Id, Name: &newName, Settings: &pb.ProfileSettings{AutoConnect: true, OnDemand: &pb.OnDemandRules{Wifi: true}}})
					return err
				},
				"UpdateProfileContent": func() error {
					_, err := h.client.UpdateProfileContent(ctx, &pb.UpdateProfileContentRequest{Id: keep.Id, Content: []byte(ovpnEdited), Reconnect: true})
					return err
				},
				// It changes nothing, but the answer would be the private keys.
				"GetProfileContent": func() error {
					_, err := h.client.GetProfileContent(ctx, &pb.GetProfileContentRequest{Id: keep.Id})
					return err
				},
				"DeleteProfile": func() error {
					_, err := h.client.DeleteProfile(ctx, &pb.DeleteProfileRequest{Id: keep.Id})
					return err
				},
				"ReorderProfiles": func() error {
					_, err := h.client.ReorderProfiles(ctx, &pb.ReorderProfilesRequest{Ids: []string{other.Id, keep.Id}})
					return err
				},
				"RemoveStaleRoute": func() error {
					_, err := h.client.RemoveStaleRoute(ctx, &pb.RemoveStaleRouteRequest{Key: "fake-stale-1"})
					return err
				},
			}
			for method, call := range modifying {
				if err := call(); status.Code(err) != codes.PermissionDenied {
					t.Errorf("%s: %v, want PermissionDenied", method, err)
				}
			}

			if name == "stranger" {
				connecting := map[string]func() error{
					"GetDaemonInfo":  func() error { _, err := h.client.GetDaemonInfo(ctx, &pb.GetDaemonInfoRequest{}); return err },
					"GetDiagnostics": func() error { _, err := h.client.GetDiagnostics(ctx, &pb.GetDiagnosticsRequest{}); return err },
					"ListProfiles":   func() error { _, err := h.client.ListProfiles(ctx, &pb.ListProfilesRequest{}); return err },
					"SetProfileEnabled": func() error {
						_, err := h.client.SetProfileEnabled(ctx, &pb.SetProfileEnabledRequest{Id: keep.Id, Enabled: true})
						return err
					},
					"ProvideCredentials": func() error {
						_, err := h.client.ProvideCredentials(ctx, &pb.ProvideCredentialsRequest{ProfileId: keep.Id, Username: "u", Password: "p"})
						return err
					},
					"Resync": func() error { _, err := h.client.Resync(ctx, &pb.ResyncRequest{}); return err },
					"WatchProfiles": func() error {
						s, err := h.client.WatchProfiles(ctx, &pb.WatchProfilesRequest{})
						if err != nil {
							return err
						}
						_, err = s.Recv()
						return err
					},
					"WatchLogs": func() error {
						s, err := h.client.WatchLogs(ctx, &pb.WatchLogsRequest{})
						if err != nil {
							return err
						}
						_, err = s.Recv()
						return err
					},
				}
				for method, call := range connecting {
					if err := call(); status.Code(err) != codes.PermissionDenied {
						t.Errorf("%s by a stranger: %v, want PermissionDenied", method, err)
					}
				}
				if n := h.d.mgr.Watchers(); n != 0 {
					t.Errorf("a refused WatchProfiles left %d watchers", n)
				}
			} else {
				// The console user may read and connect.
				if _, err := h.client.ListProfiles(ctx, &pb.ListProfilesRequest{}); err != nil {
					t.Fatalf("console user cannot list: %v", err)
				}
				if p := h.setEnabled(keep.Id, true); p.State == pb.ProfileState_PROFILE_STATE_DISCONNECTED {
					t.Fatalf("console user cannot connect: %v", p)
				}
				h.setEnabled(keep.Id, false)
			}
			if err := h.stop(); err != nil {
				t.Fatal(err)
			}

			// Look at what is on disk with a daemon that allows it.
			check := startDaemon(t, stateDir, everyone())
			after := check.list()
			if len(after) != len(before) {
				t.Fatalf("%d profiles after the denied calls, %d before", len(after), len(before))
			}
			for i, p := range after {
				b := before[i]
				if p.Id != b.Id || p.Name != b.Name || !proto.Equal(p.Settings, b.Settings) || !proto.Equal(p.Summary, b.Summary) || p.DesiredEnabled {
					t.Errorf("profile changed by denied calls:\nbefore %v\nafter  %v", b, p)
				}
				if got := check.content(p.Id); got != beforeContent[p.Id] {
					t.Errorf("text of %s changed by denied calls: %q, was %q", p.Name, got, beforeContent[p.Id])
				}
			}
			if err := check.stop(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEveryRPCIsImplemented(t *testing.T) {
	h := newDaemonHarness(t)
	p := h.importProfile("a", ovpnProfile)
	ctx := context.Background()
	name := "x"

	calls := map[string]func() error{
		"GetDaemonInfo":  func() error { _, err := h.client.GetDaemonInfo(ctx, &pb.GetDaemonInfoRequest{}); return err },
		"GetDiagnostics": func() error { _, err := h.client.GetDiagnostics(ctx, &pb.GetDiagnosticsRequest{}); return err },
		"ListProfiles":   func() error { _, err := h.client.ListProfiles(ctx, &pb.ListProfilesRequest{}); return err },
		"ImportProfile":  func() error { _, err := h.client.ImportProfile(ctx, &pb.ImportProfileRequest{}); return err },
		"UpdateProfile": func() error {
			_, err := h.client.UpdateProfile(ctx, &pb.UpdateProfileRequest{Id: p.Id, Name: &name})
			return err
		},
		"GetProfileContent": func() error {
			_, err := h.client.GetProfileContent(ctx, &pb.GetProfileContentRequest{Id: p.Id})
			return err
		},
		"UpdateProfileContent": func() error {
			_, err := h.client.UpdateProfileContent(ctx, &pb.UpdateProfileContentRequest{Id: p.Id, Content: []byte(ovpnProfile)})
			return err
		},
		"DeleteProfile":   func() error { _, err := h.client.DeleteProfile(ctx, &pb.DeleteProfileRequest{Id: "nope"}); return err },
		"ReorderProfiles": func() error { _, err := h.client.ReorderProfiles(ctx, &pb.ReorderProfilesRequest{}); return err },
		"SetProfileEnabled": func() error {
			_, err := h.client.SetProfileEnabled(ctx, &pb.SetProfileEnabledRequest{Id: p.Id})
			return err
		},
		"ProvideCredentials": func() error {
			_, err := h.client.ProvideCredentials(ctx, &pb.ProvideCredentialsRequest{ProfileId: p.Id})
			return err
		},
		"Resync": func() error { _, err := h.client.Resync(ctx, &pb.ResyncRequest{}); return err },
		"RemoveStaleRoute": func() error {
			_, err := h.client.RemoveStaleRoute(ctx, &pb.RemoveStaleRouteRequest{Key: "x"})
			return err
		},
		"WatchProfiles": func() error {
			c, cancel := context.WithCancel(ctx)
			defer cancel()
			s, err := h.client.WatchProfiles(c, &pb.WatchProfilesRequest{})
			if err != nil {
				return err
			}
			_, err = s.Recv()
			return err
		},
		"WatchLogs": func() error {
			// An empty log sends nothing until a line arrives; that the call is
			// served is shown by it still running when the deadline hits.
			c, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
			defer cancel()
			s, err := h.client.WatchLogs(c, &pb.WatchLogsRequest{})
			if err != nil {
				return err
			}
			_, err = s.Recv()
			if status.Code(err) == codes.DeadlineExceeded {
				return nil
			}
			return err
		},
	}
	if want := len(pb.DaemonService_ServiceDesc.Methods) + len(pb.DaemonService_ServiceDesc.Streams); len(calls) != want {
		t.Fatalf("this test covers %d RPCs, the API has %d", len(calls), want)
	}
	for method, call := range calls {
		if got := status.Code(call()); got == codes.Unimplemented || got == codes.Internal || got == codes.Unknown {
			t.Errorf("%s: %v", method, got)
		}
	}
}
