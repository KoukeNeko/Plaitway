package manager_test

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/manager"
	"github.com/KoukeNeko/Plaitway/internal/profile"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

const (
	wgEdited   = "[Interface]\nPrivateKey = edited-key\nAddress = 10.6.0.9/32\n[Peer]\nEndpoint = 198.51.100.4:51821\nAllowedIPs = 10.9.0.0/16\n"
	ovpnEdited = "client\nremote edited.example.com 443 tcp\nroute 10.2.0.0 255.255.0.0\n"
)

var base64Key = regexp.MustCompile(`^[A-Za-z0-9+/]{43}=$`)

func (e *env) content(id string) string {
	e.t.Helper()
	content, err := e.m.GetContent(id)
	if err != nil {
		e.t.Fatalf("GetContent(%s): %v", id, err)
	}
	return string(content)
}

func (e *env) updateContent(id, content string, reconnect bool) *pb.ImportProfileResponse {
	e.t.Helper()
	resp, err := e.m.UpdateContent(&pb.UpdateProfileContentRequest{Id: id, Content: []byte(content), Reconnect: reconnect})
	if err != nil {
		e.t.Fatalf("UpdateContent(%s): %v", id, err)
	}
	return resp
}

func wantInvalidArgument(t *testing.T, err error, contains string) {
	t.Helper()
	var invalid *profile.InvalidError
	if !errors.As(err, &invalid) {
		t.Fatalf("error = %v, want an InvalidError", err)
	}
	if !strings.Contains(invalid.Error(), contains) {
		t.Fatalf("message = %q, want it to contain %q", invalid.Error(), contains)
	}
}

func TestGetContentReturnsTheStoredText(t *testing.T) {
	e := newEnv(t)
	p := e.importProfile("home", wgProfile+"PostUp = /bin/evil\n")

	// What is stored is what Parse kept, not what was uploaded.
	if got := e.content(p.Id); got != wgProfile {
		t.Fatalf("content = %q, want %q", got, wgProfile)
	}
	if _, err := e.m.GetContent("nope"); !errors.Is(err, profile.ErrNotFound) {
		t.Fatalf("unknown id: %v, want ErrNotFound", err)
	}
}

func TestUpdateContentStoresTheTextRefreshesTheSummaryAndTellsWatchers(t *testing.T) {
	e := newEnv(t)
	p := e.importProfile("home", wgProfile)
	if !base64Key.MatchString(p.Summary.PublicKey) {
		t.Fatalf("public key after import = %q, want 44 characters of base64", p.Summary.PublicKey)
	}
	w := e.watch()

	resp := e.updateContent(p.Id, wgEdited+"PostUp = /bin/evil\n", false)

	if got := e.content(p.Id); got != wgEdited {
		t.Fatalf("stored content = %q, want %q", got, wgEdited)
	}
	sum := resp.Profile.Summary
	if len(sum.Endpoints) != 1 || sum.Endpoints[0].Host != "198.51.100.4" || sum.Endpoints[0].Port != 51821 {
		t.Fatalf("summary endpoints = %v, want the edited one", sum.Endpoints)
	}
	if !base64Key.MatchString(sum.PublicKey) || sum.PublicKey == p.Summary.PublicKey {
		t.Fatalf("public key = %q, want another 44 characters of base64 than %q", sum.PublicKey, p.Summary.PublicKey)
	}
	if len(resp.Warnings) != 1 || resp.Warnings[0].Directive != "PostUp" || resp.Warnings[0].Line != 7 {
		t.Fatalf("warnings = %v, want the stripped PostUp on line 7", resp.Warnings)
	}
	if got := e.get(p.Id).Summary.PublicKey; got != sum.PublicKey {
		t.Fatalf("List shows public key %q, the response said %q", got, sum.PublicKey)
	}
	if resp.Profile.Name != "home" || resp.Profile.State != pb.ProfileState_PROFILE_STATE_DISCONNECTED {
		t.Fatalf("profile in the response = %v", resp.Profile)
	}
	eventually(t, "the change event", func() bool { return len(w.all()) == 1 })
	if ev := w.all()[0].GetChanged(); ev == nil || ev.Summary.PublicKey != sum.PublicKey {
		t.Fatalf("event = %v, want the changed profile with the new summary", w.all()[0])
	}
}

func TestSamePrivateKeyGivesTheSamePublicKey(t *testing.T) {
	e := newEnv(t)
	p := e.importProfile("home", wgProfile)
	resp := e.updateContent(p.Id, strings.Replace(wgProfile, "203.0.113.5:51820", "203.0.113.6:51820", 1), false)
	if resp.Profile.Summary.PublicKey != p.Summary.PublicKey {
		t.Fatalf("public key = %q after editing only the endpoint, was %q", resp.Profile.Summary.PublicKey, p.Summary.PublicKey)
	}
}

func TestUpdateContentRefusesTextOfTheOtherKind(t *testing.T) {
	e := newEnv(t)
	ovpn := e.importProfile("office", ovpnProfile)
	wg := e.importProfile("home", wgProfile)
	w := e.watch()

	_, err := e.m.UpdateContent(&pb.UpdateProfileContentRequest{Id: ovpn.Id, Content: []byte(wgProfile)})
	wantInvalidArgument(t, err, "this profile is OpenVPN, but the text is WireGuard")
	_, err = e.m.UpdateContent(&pb.UpdateProfileContentRequest{Id: wg.Id, Content: []byte(ovpnProfile)})
	wantInvalidArgument(t, err, "this profile is WireGuard, but the text is OpenVPN")

	if e.content(ovpn.Id) != ovpnProfile || e.content(wg.Id) != wgProfile {
		t.Fatal("a refused update changed a stored text")
	}
	time.Sleep(20 * time.Millisecond)
	if n := len(w.all()); n != 0 {
		t.Fatalf("%d events after refused updates", n)
	}
}

func TestUpdateContentRejectedByParseChangesNothing(t *testing.T) {
	e := newEnv(t)
	p := e.importProfile("office", ovpnProfile)
	w := e.watch()

	_, err := e.m.UpdateContent(&pb.UpdateProfileContentRequest{Id: p.Id, Content: []byte("client\n# fake: reject\n")})
	wantInvalidArgument(t, err, `line 2: rejected by "# fake: reject"`)
	if e.content(p.Id) != ovpnProfile {
		t.Fatal("the stored text changed")
	}
	if got := e.get(p.Id).Summary.Endpoints[0].Host; got != "vpn.example.com" {
		t.Fatalf("summary changed: %q", got)
	}
	if n := len(w.all()); n != 0 {
		t.Fatalf("%d events after a refused update", n)
	}
}

func TestUpdateContentOfAnUnknownProfile(t *testing.T) {
	e := newEnv(t)
	_, err := e.m.UpdateContent(&pb.UpdateProfileContentRequest{Id: "nope", Content: []byte(ovpnProfile)})
	if !errors.Is(err, profile.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestUpdateContentAfterShutdownIsRefused(t *testing.T) {
	e := newEnv(t)
	p := e.importProfile("a", ovpnProfile)
	e.shutdown()
	_, err := e.m.UpdateContent(&pb.UpdateProfileContentRequest{Id: p.Id, Content: []byte(ovpnEdited)})
	if !errors.Is(err, manager.ErrShuttingDown) {
		t.Fatalf("error = %v, want ErrShuttingDown", err)
	}
}

// Without reconnect the new text waits for the next connection: the running
// engine is left alone, and the next one is built from the new text.
func TestUpdateContentWithoutReconnectLeavesARunningEngineAlone(t *testing.T) {
	stub := &stubBackend{kind: tunnel.KindOpenVPN}
	e := newEnv(t, withBackends(stub.backend()))
	p := e.importProfile("office", ovpnProfile)
	e.setEnabled(p.Id, true)
	first := stub.engine(0)

	e.updateContent(p.Id, ovpnEdited, false)

	if n := stub.created.Load(); n != 1 || first.stopped.Load() {
		t.Fatalf("%d engines created, first stopped %v; the running engine must not be touched", n, first.stopped.Load())
	}
	if got := e.get(p.Id); !got.DesiredEnabled || got.State == pb.ProfileState_PROFILE_STATE_DISCONNECTED {
		t.Fatalf("the profile was disturbed: %v", got)
	}

	e.setEnabled(p.Id, false)
	e.setEnabled(p.Id, true)
	if got := string(stub.engine(1).spec.Content); got != ovpnEdited {
		t.Fatalf("the next connection runs %q, want the new text", got)
	}
}

func TestUpdateContentWithReconnectRestartsAnEnabledProfile(t *testing.T) {
	stub := &stubBackend{kind: tunnel.KindOpenVPN}
	e := newEnv(t, withBackends(stub.backend()))
	p := e.importProfile("office", ovpnProfile)
	e.setEnabled(p.Id, true)
	first := stub.engine(0)
	w := e.watch()

	resp := e.updateContent(p.Id, ovpnEdited, true)

	if n := stub.created.Load(); n != 2 {
		t.Fatalf("%d engines created, want 2", n)
	}
	if !first.stopped.Load() {
		t.Fatal("the old engine was not stopped")
	}
	if got := string(stub.engine(1).spec.Content); got != ovpnEdited {
		t.Fatalf("the new engine runs %q, want the new text", got)
	}
	if !resp.Profile.DesiredEnabled || resp.Profile.State == pb.ProfileState_PROFILE_STATE_DISCONNECTED {
		t.Fatalf("the response says the profile is not running: %v", resp.Profile)
	}
	want := []pb.ProfileState{pb.ProfileState_PROFILE_STATE_DISCONNECTING, pb.ProfileState_PROFILE_STATE_DISCONNECTED, pb.ProfileState_PROFILE_STATE_CONNECTING}
	eventually(t, "the restart to show", func() bool {
		got := w.states(p.Id)
		return len(got) >= len(want) && got[len(got)-3] == want[0] && got[len(got)-2] == want[1] && got[len(got)-1] == want[2]
	})
}

func TestUpdateContentWithReconnectLeavesADisabledProfileAlone(t *testing.T) {
	stub := &stubBackend{kind: tunnel.KindOpenVPN}
	e := newEnv(t, withBackends(stub.backend()))
	p := e.importProfile("office", ovpnProfile)

	resp := e.updateContent(p.Id, ovpnEdited, true)

	if n := stub.created.Load(); n != 0 {
		t.Fatalf("%d engines created for a profile that is not enabled", n)
	}
	if resp.Profile.DesiredEnabled || resp.Profile.State != pb.ProfileState_PROFILE_STATE_DISCONNECTED {
		t.Fatalf("the profile was started: %v", resp.Profile)
	}
}

// A profile that is enabled but FAILED because of its text restarts as well.
func TestUpdateContentWithReconnectRestartsAProfileThatFailed(t *testing.T) {
	e := newEnv(t)
	p := e.importProfile("office", ovpnProfile+"# fake: fail\n")
	e.setEnabled(p.Id, true)
	e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_FAILED)

	e.updateContent(p.Id, ovpnProfile, false)
	if got := e.get(p.Id); got.State != pb.ProfileState_PROFILE_STATE_FAILED {
		t.Fatalf("state = %v without reconnect, want the failure left as it is", got.State)
	}

	e.updateContent(p.Id, ovpnProfile, true)
	if got := e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED); got.LastError != "" || !got.DesiredEnabled {
		t.Fatalf("after the restart: %v", got)
	}
}

func TestUpdateContentWithReconnectRestartsAProfileThatIsStillConnecting(t *testing.T) {
	stub := &stubBackend{kind: tunnel.KindOpenVPN, startGate: make(chan struct{})}
	e := newEnv(t, withBackends(stub.backend()), withStartWait(20*time.Millisecond))
	p := e.importProfile("office", ovpnProfile)
	e.setEnabled(p.Id, true) // its engine is held inside Start

	e.updateContent(p.Id, ovpnEdited, true)

	eventually(t, "a second engine, built from the new text", func() bool {
		second := stub.engine(1)
		return second != nil && string(second.spec.Content) == ovpnEdited
	})
	close(stub.startGate)
	eventually(t, "the second engine to start", func() bool { return stub.engine(1).started.Load() })
	if got := e.get(p.Id); !got.DesiredEnabled {
		t.Fatalf("the profile is no longer enabled: %v", got)
	}
}

func TestTextAndSummarySurviveARestart(t *testing.T) {
	dir := t.TempDir()
	first := startEnv(t, dir)
	p := first.importProfile("home", wgProfile)
	updated := first.updateContent(p.Id, wgEdited, false).Profile
	first.shutdown()

	second := startEnv(t, dir)
	if got := second.content(p.Id); got != wgEdited {
		t.Fatalf("content after the restart = %q", got)
	}
	if got := second.get(p.Id).Summary; got.PublicKey != updated.Summary.PublicKey || got.Endpoints[0].Host != "198.51.100.4" {
		t.Fatalf("summary after the restart = %v", got)
	}
}
