package manager_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

func (e *env) updateSettings(id string, settings *pb.ProfileSettings) (*pb.Profile, error) {
	return e.m.Update(&pb.UpdateProfileRequest{Id: id, Settings: settings})
}

func TestExcludePrivateIPsAndOnDemandAreSettingsOfAWireGuardProfile(t *testing.T) {
	e := newEnv(t)
	p := e.importProfile("home", wgProfile)
	if s := p.Settings; s.ExcludePrivateIps || s.OnDemand == nil || s.OnDemand.Ethernet || s.OnDemand.Wifi {
		t.Fatalf("a new profile starts with both off: %v", s)
	}
	w := e.watch()

	want := &pb.ProfileSettings{ExcludePrivateIps: true, OnDemand: &pb.OnDemandRules{Wifi: true}, Priority: 1, TunnelMode: pb.TunnelMode_TUNNEL_MODE_AUTO}
	got, err := e.updateSettings(p.Id, &pb.ProfileSettings{ExcludePrivateIps: true, OnDemand: &pb.OnDemandRules{Wifi: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(got.Settings, want) {
		t.Fatalf("settings = %v, want %v", got.Settings, want)
	}
	eventually(t, "the change event", func() bool { return len(w.all()) >= 1 })
	if ev := w.all()[0].GetChanged(); ev == nil || !proto.Equal(ev.Settings, want) {
		t.Fatalf("event = %v", w.all()[0])
	}

	// Settings replace each other: a client has to send them all.
	got, err = e.updateSettings(p.Id, &pb.ProfileSettings{AutoConnect: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Settings.ExcludePrivateIps || got.Settings.OnDemand.Wifi || !got.Settings.AutoConnect {
		t.Fatalf("settings after sending only auto_connect = %v", got.Settings)
	}
}

func TestOnDemandIsASettingOfAnOpenVPNProfileToo(t *testing.T) {
	e := newEnv(t)
	p := e.importProfile("office", ovpnProfile)
	got, err := e.updateSettings(p.Id, &pb.ProfileSettings{OnDemand: &pb.OnDemandRules{Ethernet: true, Wifi: true}})
	if err != nil {
		t.Fatal(err)
	}
	if rules := got.Settings.OnDemand; !rules.Ethernet || !rules.Wifi {
		t.Fatalf("on_demand = %v", rules)
	}
}

func TestExcludePrivateIPsIsRefusedForAnOpenVPNProfile(t *testing.T) {
	e := newEnv(t)
	p := e.importProfile("office", ovpnProfile)

	_, err := e.updateSettings(p.Id, &pb.ProfileSettings{ExcludePrivateIps: true})
	wantInvalidArgument(t, err, "this profile is OpenVPN")
	if e.get(p.Id).Settings.ExcludePrivateIps {
		t.Fatal("the refused setting was stored")
	}

	_, err = e.m.Import(&pb.ImportProfileRequest{Name: "other", Content: []byte(ovpnProfile), Settings: &pb.ProfileSettings{ExcludePrivateIps: true}})
	wantInvalidArgument(t, err, "this profile is OpenVPN")
	if n := len(e.m.List()); n != 1 {
		t.Fatalf("%d profiles after a refused import, want 1", n)
	}
}

func TestImportKeepsExcludePrivateIPsAndOnDemand(t *testing.T) {
	e := newEnv(t)
	p := e.importWith(&pb.ImportProfileRequest{
		Name: "home", Content: []byte(wgProfile),
		Settings: &pb.ProfileSettings{ExcludePrivateIps: true, OnDemand: &pb.OnDemandRules{Ethernet: true}},
	}).Profile
	if !p.Settings.ExcludePrivateIps || !p.Settings.OnDemand.Ethernet || p.Settings.OnDemand.Wifi {
		t.Fatalf("settings = %v", p.Settings)
	}
}

func TestExcludePrivateIPsReachesTheEngineOnTheNextConnection(t *testing.T) {
	stub := &stubBackend{kind: tunnel.KindWireGuard}
	e := newEnv(t, withBackends(stub.backend()))
	p := e.importProfile("home", wgProfile)

	e.setEnabled(p.Id, true)
	if stub.engine(0).spec.ExcludePrivateIPs {
		t.Fatal("the option is on by default")
	}
	if _, err := e.updateSettings(p.Id, &pb.ProfileSettings{ExcludePrivateIps: true}); err != nil {
		t.Fatal(err)
	}
	if stub.created.Load() != 1 || stub.engine(0).stopped.Load() {
		t.Fatal("changing the option touched the running engine")
	}

	e.setEnabled(p.Id, false)
	e.setEnabled(p.Id, true)
	if !stub.engine(1).spec.ExcludePrivateIPs {
		t.Fatal("the next engine does not get the option")
	}
}

func TestNewSettingsSurviveARestart(t *testing.T) {
	dir := t.TempDir()
	first := startEnv(t, dir)
	wg := first.importProfile("home", wgProfile)
	ovpn := first.importProfile("office", ovpnProfile)
	wgSettings := &pb.ProfileSettings{ExcludePrivateIps: true, OnDemand: &pb.OnDemandRules{Wifi: true}}
	ovpnSettings := &pb.ProfileSettings{OnDemand: &pb.OnDemandRules{Ethernet: true}}
	if _, err := first.updateSettings(wg.Id, wgSettings); err != nil {
		t.Fatal(err)
	}
	if _, err := first.updateSettings(ovpn.Id, ovpnSettings); err != nil {
		t.Fatal(err)
	}
	first.shutdown()

	second := startEnv(t, dir)
	for id, want := range map[string]*pb.ProfileSettings{wg.Id: wgSettings, ovpn.Id: ovpnSettings} {
		got := second.get(id).Settings
		if got.ExcludePrivateIps != want.ExcludePrivateIps || !proto.Equal(got.OnDemand, want.OnDemand) {
			t.Errorf("settings of %s after the restart = %v, want %v", id, got, want)
		}
	}
}
