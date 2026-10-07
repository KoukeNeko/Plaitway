//go:build unix

package main

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
)

const (
	wgEdited    = "[Interface]\nPrivateKey = edited-key\nAddress = 10.6.0.9/32\n[Peer]\nEndpoint = 198.51.100.4:51821\nAllowedIPs = 10.9.0.0/16\n"
	ovpnEdited  = "client\nremote edited.example.com 443 tcp\nroute 10.2.0.0 255.255.0.0\n"
	secretKey   = "TOP-SECRET-KEY-MATERIAL"
	wgWithKey   = "[Interface]\nPrivateKey = " + secretKey + "\nAddress = 10.6.0.2/32\n[Peer]\nEndpoint = 203.0.113.5:51820\nAllowedIPs = 0.0.0.0/0\n"
	ovpnWithKey = "client\nremote vpn.example.com 1194\n<key>\n" + secretKey + "\n</key>\n"
)

var publicKeyShape = regexp.MustCompile(`^[A-Za-z0-9+/]{43}=$`)

func (h *harness) content(id string) string {
	h.t.Helper()
	resp, err := h.client.GetProfileContent(context.Background(), &pb.GetProfileContentRequest{Id: id})
	if err != nil {
		h.t.Fatalf("GetProfileContent(%s): %v", id, err)
	}
	return string(resp.Content)
}

func (h *harness) updateContent(id, content string, reconnect bool) (*pb.ImportProfileResponse, error) {
	return h.client.UpdateProfileContent(context.Background(), &pb.UpdateProfileContentRequest{Id: id, Content: []byte(content), Reconnect: reconnect})
}

func TestGetProfileContent(t *testing.T) {
	h := newDaemonHarness(t)
	ovpn := h.importProfile("office", ovpnProfile)
	wg := h.importProfile("home", wgProfile+"PostUp = /bin/evil\n")

	if got := h.content(ovpn.Id); got != ovpnProfile {
		t.Fatalf("OpenVPN content = %q", got)
	}
	// What is stored is what Parse kept, not what was uploaded.
	if got := h.content(wg.Id); got != wgProfile {
		t.Fatalf("WireGuard content = %q, want it without the hook", got)
	}
	_, err := h.client.GetProfileContent(context.Background(), &pb.GetProfileContentRequest{Id: "nope"})
	wantCode(t, err, codes.NotFound)
}

func TestUpdateProfileContent(t *testing.T) {
	h := newDaemonHarness(t)
	ovpn := h.importProfile("office", ovpnProfile)
	wg := h.importProfile("home", wgProfile)
	if !publicKeyShape.MatchString(wg.Summary.PublicKey) || ovpn.Summary.PublicKey != "" {
		t.Fatalf("public keys: WireGuard %q, OpenVPN %q", wg.Summary.PublicKey, ovpn.Summary.PublicKey)
	}
	w := h.watch()

	resp, err := h.updateContent(wg.Id, wgEdited+"PostUp = /bin/evil\n", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.content(wg.Id); got != wgEdited {
		t.Fatalf("stored content = %q, want the edited text without the hook", got)
	}
	sum := resp.Profile.Summary
	if sum.Endpoints[0].Host != "198.51.100.4" || !publicKeyShape.MatchString(sum.PublicKey) || sum.PublicKey == wg.Summary.PublicKey {
		t.Fatalf("summary = %v, want the new endpoint and another public key than %q", sum, wg.Summary.PublicKey)
	}
	if len(resp.Warnings) != 1 || resp.Warnings[0].Directive != "PostUp" || resp.Warnings[0].Line != 7 {
		t.Fatalf("warnings = %v", resp.Warnings)
	}
	if got := h.get(wg.Id).Summary.PublicKey; got != sum.PublicKey {
		t.Fatalf("ListProfiles shows public key %q, the response said %q", got, sum.PublicKey)
	}
	eventually(t, "the watcher to see the new summary", func() bool {
		for _, ev := range w.allEvents() {
			if c := ev.GetChanged(); c != nil && c.Id == wg.Id && c.Summary.PublicKey == sum.PublicKey {
				return true
			}
		}
		return false
	})

	if _, err := h.updateContent(ovpn.Id, ovpnEdited, false); err != nil {
		t.Fatal(err)
	}
	if got := h.get(ovpn.Id).Summary.Endpoints[0]; got.Host != "edited.example.com" || got.Port != 443 || got.Protocol != "tcp" {
		t.Fatalf("OpenVPN summary endpoint = %v", got)
	}
}

func TestUpdateProfileContentErrors(t *testing.T) {
	h := newDaemonHarness(t)
	ovpn := h.importProfile("office", ovpnProfile)
	wg := h.importProfile("home", wgProfile)
	w := h.watch()

	tests := []struct {
		name    string
		id      string
		content string
		code    codes.Code
		message string
	}{
		{"unknown profile", "nope", ovpnProfile, codes.NotFound, "nope"},
		{"rejected by Parse", ovpn.Id, "client\n# fake: reject\n", codes.InvalidArgument, `line 2: rejected by "# fake: reject"`},
		{"empty", ovpn.Id, "", codes.InvalidArgument, "profile is empty"},
		{"oversized", ovpn.Id, strings.Repeat("a", 1<<20+1), codes.InvalidArgument, "limit"},
		{"WireGuard text for an OpenVPN profile", ovpn.Id, wgProfile, codes.InvalidArgument, "this profile is OpenVPN, but the text is WireGuard"},
		{"OpenVPN text for a WireGuard profile", wg.Id, ovpnProfile, codes.InvalidArgument, "this profile is WireGuard, but the text is OpenVPN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := h.updateContent(tt.id, tt.content, true)
			wantCode(t, err, tt.code)
			if !strings.Contains(status.Convert(err).Message(), tt.message) {
				t.Fatalf("message = %q, want it to contain %q", status.Convert(err).Message(), tt.message)
			}
		})
	}
	if h.content(ovpn.Id) != ovpnProfile || h.content(wg.Id) != wgProfile {
		t.Fatal("a refused update changed a stored text")
	}
	for _, ev := range w.allEvents() {
		t.Errorf("a refused update published %v", ev)
	}
}

// Without reconnect a running tunnel keeps the text it was started with; with
// it, an enabled profile restarts at once with the new text.
func TestUpdateProfileContentReconnect(t *testing.T) {
	h := newDaemonHarness(t)
	p := h.importProfile("office", ovpnProfile)
	h.setEnabled(p.Id, true)
	h.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
	w := h.watch()

	// The call has returned, so a restart would have happened by now.
	resp, err := h.updateContent(p.Id, ovpnEdited, false)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Profile.State != pb.ProfileState_PROFILE_STATE_CONNECTED || resp.Profile.Status.Remote != "vpn.example.com:1194" {
		t.Fatalf("without reconnect the running tunnel changed: %v", resp.Profile)
	}
	for _, state := range w.states(p.Id) {
		if state != pb.ProfileState_PROFILE_STATE_CONNECTED {
			t.Fatalf("without reconnect the tunnel went through %v", w.states(p.Id))
		}
	}

	resp, err = h.updateContent(p.Id, ovpnEdited+"# edited again\n", true)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Profile.DesiredEnabled || resp.Profile.State == pb.ProfileState_PROFILE_STATE_DISCONNECTED {
		t.Fatalf("the response after a reconnecting update: %v", resp.Profile)
	}
	restarted := h.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
	if restarted.Status.Remote != "edited.example.com:443" {
		t.Fatalf("the restarted tunnel connects to %q, want the edited server", restarted.Status.Remote)
	}
	eventually(t, "the watcher to see the tunnel go down and come back", func() bool {
		return slices.Contains(w.states(p.Id), pb.ProfileState_PROFILE_STATE_DISCONNECTED)
	})

	// A profile that is not enabled is left alone.
	idle := h.importProfile("idle", ovpnProfile)
	resp, err = h.updateContent(idle.Id, ovpnEdited, true)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Profile.DesiredEnabled || resp.Profile.State != pb.ProfileState_PROFILE_STATE_DISCONNECTED {
		t.Fatalf("an idle profile was started: %v", resp.Profile)
	}
}

func TestUpdateProfileSettingsExcludePrivateIPsAndOnDemand(t *testing.T) {
	h := newDaemonHarness(t)
	ovpn := h.importProfile("office", ovpnProfile)
	wg := h.importProfile("home", wgProfile)
	ctx := context.Background()

	want := &pb.ProfileSettings{ExcludePrivateIps: true, OnDemand: &pb.OnDemandRules{Ethernet: true}, Priority: 2, TunnelMode: pb.TunnelMode_TUNNEL_MODE_AUTO}
	p, err := h.client.UpdateProfile(ctx, &pb.UpdateProfileRequest{Id: wg.Id, Settings: &pb.ProfileSettings{ExcludePrivateIps: true, OnDemand: &pb.OnDemandRules{Ethernet: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(p.Settings, want) {
		t.Fatalf("settings = %v, want %v", p.Settings, want)
	}

	// On demand is for both kinds; excluding private IPs is for WireGuard.
	p, err = h.client.UpdateProfile(ctx, &pb.UpdateProfileRequest{Id: ovpn.Id, Settings: &pb.ProfileSettings{OnDemand: &pb.OnDemandRules{Ethernet: true, Wifi: true}}})
	if err != nil || !p.Settings.OnDemand.Ethernet || !p.Settings.OnDemand.Wifi {
		t.Fatalf("on demand for OpenVPN: %v, %v", p, err)
	}
	_, err = h.client.UpdateProfile(ctx, &pb.UpdateProfileRequest{Id: ovpn.Id, Settings: &pb.ProfileSettings{ExcludePrivateIps: true}})
	wantCode(t, err, codes.InvalidArgument)
	if msg := status.Convert(err).Message(); !strings.Contains(msg, "WireGuard") || !strings.Contains(msg, "OpenVPN") {
		t.Fatalf("message = %q, want it to say why", msg)
	}
	if h.get(ovpn.Id).Settings.ExcludePrivateIps {
		t.Fatal("the refused setting was stored")
	}
}

// Both settings and the text are what the daemon came back with: a restart
// loses none of them, and the on-demand rules are applied to the network the
// daemon starts on.
func TestTextAndNewSettingsSurviveARestart(t *testing.T) {
	stateDir := t.TempDir()
	first := startDaemon(t, stateDir, everyone())
	wg := first.importProfile("home", wgProfile)
	ovpn := first.importProfile("office", ovpnProfile)
	updated, err := first.updateContent(wg.Id, wgEdited, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	wgSettings := &pb.ProfileSettings{ExcludePrivateIps: true, OnDemand: &pb.OnDemandRules{Wifi: true}}
	if _, err := first.client.UpdateProfile(ctx, &pb.UpdateProfileRequest{Id: wg.Id, Settings: wgSettings}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.client.UpdateProfile(ctx, &pb.UpdateProfileRequest{Id: ovpn.Id, Settings: &pb.ProfileSettings{OnDemand: &pb.OnDemandRules{Ethernet: true}}}); err != nil {
		t.Fatal(err)
	}
	first.waitState(wg.Id, pb.ProfileState_PROFILE_STATE_CONNECTED) // the fake host is on Wi-Fi
	if err := first.stop(); err != nil {
		t.Fatal(err)
	}

	second := startDaemon(t, stateDir, everyone())
	if got := second.content(wg.Id); got != wgEdited {
		t.Fatalf("text after the restart = %q", got)
	}
	got := second.get(wg.Id)
	if got.Summary.PublicKey != updated.Profile.Summary.PublicKey {
		t.Fatalf("public key after the restart = %q, want %q", got.Summary.PublicKey, updated.Profile.Summary.PublicKey)
	}
	if !got.Settings.ExcludePrivateIps || !got.Settings.OnDemand.Wifi || got.Settings.OnDemand.Ethernet {
		t.Fatalf("WireGuard settings after the restart = %v", got.Settings)
	}
	if o := second.get(ovpn.Id).Settings.OnDemand; !o.Ethernet || o.Wifi {
		t.Fatalf("OpenVPN rules after the restart = %v", o)
	}
	// The daemon starts on the fake host's Wi-Fi network: the Wi-Fi profile
	// connects by itself, the Ethernet one does not.
	second.waitState(wg.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
	if o := second.get(ovpn.Id); o.DesiredEnabled {
		t.Fatalf("the Ethernet profile was connected on Wi-Fi: %v", o)
	}
}

// The fake host is on Wi-Fi. Saving rules applies them to it before the call
// returns, and a manual choice then stands.
func TestSavingOnDemandRulesConnectsAndDisconnectsAtOnce(t *testing.T) {
	h := newDaemonHarness(t)
	p := h.importProfile("office", ovpnProfile)
	save := func(rules *pb.OnDemandRules) *pb.Profile {
		t.Helper()
		got, err := h.client.UpdateProfile(context.Background(), &pb.UpdateProfileRequest{Id: p.Id, Settings: &pb.ProfileSettings{OnDemand: rules}})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	if got := save(&pb.OnDemandRules{Wifi: true}); !got.DesiredEnabled {
		t.Fatalf("saving Wi-Fi on Wi-Fi did not connect: %v", got)
	}
	h.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)

	if got := save(&pb.OnDemandRules{Ethernet: true}); got.DesiredEnabled || got.State != pb.ProfileState_PROFILE_STATE_DISCONNECTED {
		t.Fatalf("saving Ethernet on Wi-Fi did not disconnect: %v", got)
	}

	// Connected by hand on a network the rules do not check: it stays.
	h.setEnabled(p.Id, true)
	h.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
	name := "renamed"
	got, err := h.client.UpdateProfile(context.Background(), &pb.UpdateProfileRequest{Id: p.Id, Name: &name, Settings: &pb.ProfileSettings{OnDemand: &pb.OnDemandRules{Ethernet: true}}})
	if err != nil || !got.DesiredEnabled {
		t.Fatalf("saving the same rules again: %v, %v", got, err)
	}
}

// The calls are on the record, but the daemon's log at any level, and the
// profiles' logs, never hold what a profile says.
func TestProfileTextIsNeverLogged(t *testing.T) {
	var logOut syncBuffer
	h := startLoggingDaemon(t, t.TempDir(), everyone(), &logOut, slog.LevelDebug)
	ctx := context.Background()

	wg := h.importProfile("home", wgWithKey)
	ovpn := h.importProfile("office", ovpnWithKey)
	h.content(wg.Id)
	h.content(ovpn.Id)
	h.setEnabled(wg.Id, true)
	if _, err := h.updateContent(wg.Id, wgWithKey+"# edited "+secretKey+"\n", true); err != nil {
		t.Fatal(err)
	}
	h.waitState(wg.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
	// The failures: a text that is rejected, and one of the wrong kind, both with the key in them.
	if _, err := h.updateContent(ovpn.Id, ovpnWithKey+"# fake: reject\n", false); err == nil {
		t.Fatal("a rejected text was accepted")
	}
	if _, err := h.updateContent(ovpn.Id, wgWithKey, false); err == nil {
		t.Fatal("text of the wrong kind was accepted")
	}
	if _, err := h.client.ImportProfile(ctx, &pb.ImportProfileRequest{Name: "bad", Content: []byte(wgWithKey + "# fake: reject\n")}); err == nil {
		t.Fatal("a rejected import was accepted")
	}
	h.setEnabled(wg.Id, false)

	log := logOut.String()
	if !strings.Contains(log, "GetProfileContent") || !strings.Contains(log, "UpdateProfileContent") {
		t.Fatalf("the calls are not in the log at all:\n%s", log)
	}
	if strings.Contains(log, secretKey) {
		t.Fatalf("the daemon log holds the key material:\n%s", log)
	}
	for _, id := range []string{"", wg.Id, ovpn.Id} {
		for _, line := range readLogs(t, h, id, 1000) {
			if strings.Contains(line.Text, secretKey) {
				t.Errorf("the log of %q holds the key material: %q", id, line.Text)
			}
		}
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
