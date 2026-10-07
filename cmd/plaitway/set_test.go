package main

import (
	"maps"
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
)

func TestSet(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	d.importText("home", wgProfile)
	workID := d.importText("work", ovpnProfile)
	home := func() jsonProfile { return d.profile("home") }
	priorities := func() [2]int { return [2]int{home().Settings.Priority, d.profile(workID).Settings.Priority} }
	before := priorities()
	if before[0] == 0 || before[1] == 0 || before[0] == before[1] {
		t.Fatalf("the profiles have the priorities %v", before)
	}

	t.Run("a flag changes its own setting only", func(t *testing.T) {
		if r := d.run("", "set", "-auto-connect", "home"); r.code != 0 || r.stdout != "updated home\n" || r.stderr != "" {
			t.Errorf("set -auto-connect: %+v", r)
		}
		if s := home().Settings; !s.AutoConnect || s.TunnelMode != "TUNNEL_MODE_AUTO" || s.ExcludePrivateIps || s.OnDemand.Ethernet || s.OnDemand.Wifi {
			t.Errorf("settings after -auto-connect: %+v", s)
		}

		// A flag after the profile works, and the setting made before stays.
		d.mustRun("", "set", "home", "-tunnel-mode=split")
		if s := home().Settings; !s.AutoConnect || s.TunnelMode != "TUNNEL_MODE_SPLIT" {
			t.Errorf("settings after -tunnel-mode: %+v", s)
		}

		d.mustRun("", "set", "-exclude-private-ips", "-on-demand", "wifi,Ethernet", "home")
		if s := home().Settings; !s.AutoConnect || s.TunnelMode != "TUNNEL_MODE_SPLIT" || !s.ExcludePrivateIps || !s.OnDemand.Ethernet || !s.OnDemand.Wifi {
			t.Errorf("settings after -exclude-private-ips and -on-demand: %+v", s)
		}

		d.mustRun("", "set", "-on-demand=wifi", "home")
		if s := home().Settings; s.OnDemand.Ethernet || !s.OnDemand.Wifi || !s.ExcludePrivateIps {
			t.Errorf("settings after -on-demand=wifi: %+v", s)
		}

		d.mustRun("", "set", "-on-demand=off", "-exclude-private-ips=false", "-auto-connect=false", "-tunnel-mode=full", "home")
		if s := home().Settings; s.OnDemand.Ethernet || s.OnDemand.Wifi || s.ExcludePrivateIps || s.AutoConnect || s.TunnelMode != "TUNNEL_MODE_FULL" {
			t.Errorf("settings after turning them off: %+v", s)
		}
	})

	t.Run("the priority stays", func(t *testing.T) {
		d.mustRun("", "set", "-auto-connect", "work")
		d.mustRun("", "set", "-name", "Work VPN", "work")
		if got := priorities(); got != before {
			t.Errorf("priorities %v after set, were %v", got, before)
		}
	})

	t.Run("the name", func(t *testing.T) {
		if r := d.run("", "set", "-name", "Home 2", "home"); r.code != 0 || r.stdout != "updated Home 2\n" {
			t.Errorf("set -name: %+v", r)
		}
		if p := d.profile("Home 2"); p.Settings.TunnelMode != "TUNNEL_MODE_FULL" {
			t.Errorf("a new name changed the settings: %+v", p.Settings)
		}
		d.mustRun("", "set", "-name", "home", "Home 2")

		r := d.run("", "set", "-name", "work vpn", "home")
		if r.code != exitFailure || r.stdout != "" || !strings.Contains(r.stderr, "already used") {
			t.Errorf("set -name to a name in use: %+v", r)
		}
		if r := d.run("", "set", "-name", " ", "home"); r.code != exitFailure || !strings.Contains(r.stderr, "empty") {
			t.Errorf("set -name with an empty name: %+v", r)
		}
	})

	t.Run("excluding private IPs is for WireGuard", func(t *testing.T) {
		r := d.run("", "set", "-exclude-private-ips", workID)
		if r.code != exitFailure || r.stdout != "" || !strings.Contains(r.stderr, "WireGuard") {
			t.Errorf("set -exclude-private-ips on OpenVPN: %+v", r)
		}
		if d.profile(workID).Settings.ExcludePrivateIps {
			t.Error("the daemon refused the option and stored it")
		}
		// Turning it off is no change to refuse.
		if r := d.run("", "set", "-exclude-private-ips=false", workID); r.code != 0 {
			t.Errorf("set -exclude-private-ips=false on OpenVPN: %+v", r)
		}
	})

	t.Run("a command line that cannot be run changes nothing", func(t *testing.T) {
		settings := d.profile("home").Settings
		for name, args := range map[string][]string{
			"no flag":              {"set", "home"},
			"no profile":           {"set", "-auto-connect"},
			"unknown tunnel mode":  {"set", "-tunnel-mode", "everything", "home"},
			"empty tunnel mode":    {"set", "-tunnel-mode=", "home"},
			"unknown network":      {"set", "-on-demand", "wifi,tethering", "home"},
			"off with a network":   {"set", "-on-demand", "off,wifi", "home"},
			"empty on-demand":      {"set", "-on-demand=", "home"},
			"not a boolean":        {"set", "-auto-connect=maybe", "home"},
			"another flag is none": {"set", "-socket", d.socket, "home"},
		} {
			if r := d.run("", args...); r.code != exitUsage || r.stdout != "" {
				t.Errorf("%s: %+v", name, r)
			}
		}
		if got := d.profile("home").Settings; got != settings {
			t.Errorf("settings %+v after refused command lines, were %+v", got, settings)
		}
	})

	t.Run("a tunnel that is up keeps its settings until it connects again", func(t *testing.T) {
		d.mustRun("", "connect", "home")
		for _, c := range []struct {
			flags []string
			want  string
		}{
			{[]string{"-tunnel-mode=split"}, "updated home (applies on the next connection)\n"},
			{[]string{"-exclude-private-ips"}, "updated home (applies on the next connection)\n"},
			{[]string{"-tunnel-mode=split", "-exclude-private-ips"}, "updated home\n"}, // no change
			{[]string{"-auto-connect"}, "updated home\n"},
			{[]string{"-on-demand=off"}, "updated home\n"},
		} {
			args := append(append([]string{"set"}, c.flags...), "home")
			if r := d.run("", args...); r.code != 0 || r.stdout != c.want {
				t.Errorf("%v: %+v, want stdout %q", args, r, c.want)
			}
		}
	})
}

func TestStatusOfOneProfileShowsItsSettingsAndPublicKey(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	d.importText("home", wgProfile)
	d.importText("work", ovpnProfile)
	d.mustRun("", "set", "-tunnel-mode=split", "-exclude-private-ips", "-on-demand=ethernet,wifi", "home")
	key := d.profile("home").Summary.PublicKey
	if key == "" {
		t.Fatal("the WireGuard profile has no public key")
	}

	out := d.mustRun("", "status", "home").stdout
	table, details, found := strings.Cut(out, "\n\n")
	if !found || !strings.Contains(table, "NAME") || !strings.Contains(table, "home") || strings.Contains(table, key) {
		t.Fatalf("status home has a table and then the details:\n%s", out)
	}
	want := map[string]string{
		"auto-connect": "off", "tunnel mode": "split", "exclude private IPs": "on",
		"on demand": "ethernet,wifi", "public key": key,
	}
	if got := detailsOf(details); !maps.Equal(got, want) {
		t.Errorf("details of home %v, want %v", got, want)
	}

	// OpenVPN has no public key and no private-range option.
	work := detailsOf(strings.SplitN(d.mustRun("", "status", "work").stdout, "\n\n", 2)[1])
	if want := map[string]string{"auto-connect": "off", "tunnel mode": "auto", "on demand": "off"}; !maps.Equal(work, want) {
		t.Errorf("details of work %v, want %v", work, want)
	}

	// The table of every profile is not widened by them.
	if all := d.mustRun("", "status").stdout; strings.Contains(all, key) || strings.Contains(all, "tunnel mode") {
		t.Errorf("status shows the details of every profile:\n%s", all)
	}

	// A script gets them for every profile, in the API's own words.
	if p := d.profile("home"); p.Summary.PublicKey != key || p.Settings.TunnelMode != "TUNNEL_MODE_SPLIT" || !p.Settings.ExcludePrivateIps ||
		!p.Settings.OnDemand.Ethernet || !p.Settings.OnDemand.Wifi {
		t.Errorf("status -json home: %+v", p)
	}
}

// The daemon takes the settings as one message, so the command has to send what
// it has read back with the change merged in.
func TestSetSendsTheMergedSettings(t *testing.T) {
	t.Parallel()
	current := &pb.ProfileSettings{AutoConnect: true, TunnelMode: pb.TunnelMode_TUNNEL_MODE_FULL, Priority: 3,
		ExcludePrivateIps: true, OnDemand: &pb.OnDemandRules{Wifi: true}}
	var (
		mu       sync.Mutex
		requests []*pb.UpdateProfileRequest
	)
	profileWith := func(settings *pb.ProfileSettings) *pb.Profile {
		p := profileOf("ID", "home", pb.ProfileState_PROFILE_STATE_DISCONNECTED)
		p.Settings = settings
		return p
	}
	socket := (&scripted{
		list: func() ([]*pb.Profile, error) { return []*pb.Profile{profileWith(current)}, nil },
		update: func(req *pb.UpdateProfileRequest) (*pb.Profile, error) {
			mu.Lock()
			defer mu.Unlock()
			requests = append(requests, req)
			return profileWith(req.Settings), nil
		},
	}).serve(t)

	for name, c := range map[string]struct {
		args     []string
		wantName *string
		want     *pb.ProfileSettings // nil: no settings are sent
	}{
		"on-demand":     {[]string{"-on-demand=ethernet", "home"}, nil, &pb.ProfileSettings{AutoConnect: true, TunnelMode: pb.TunnelMode_TUNNEL_MODE_FULL, Priority: 3, ExcludePrivateIps: true, OnDemand: &pb.OnDemandRules{Ethernet: true}}},
		"auto-connect":  {[]string{"-auto-connect=false", "home"}, nil, &pb.ProfileSettings{TunnelMode: pb.TunnelMode_TUNNEL_MODE_FULL, Priority: 3, ExcludePrivateIps: true, OnDemand: &pb.OnDemandRules{Wifi: true}}},
		"name only":     {[]string{"-name", "office", "home"}, proto.String("office"), nil},
		"name and mode": {[]string{"-name=office", "-tunnel-mode=auto", "home"}, proto.String("office"), &pb.ProfileSettings{AutoConnect: true, TunnelMode: pb.TunnelMode_TUNNEL_MODE_AUTO, Priority: 3, ExcludePrivateIps: true, OnDemand: &pb.OnDemandRules{Wifi: true}}},
	} {
		mu.Lock()
		requests = nil
		mu.Unlock()
		if r := runAt(t, socket, "", nil, append([]string{"set"}, c.args...)...); r.code != 0 {
			t.Errorf("%s: %+v", name, r)
			continue
		}
		mu.Lock()
		if len(requests) != 1 || requests[0].Id != "ID" || !proto.Equal(requests[0].Settings, c.want) ||
			(c.wantName == nil) != (requests[0].Name == nil) || (c.wantName != nil && *c.wantName != *requests[0].Name) {
			t.Errorf("%s: sent %v", name, requests)
		}
		mu.Unlock()
	}
}

// A profile without a settings message is read as one with the defaults.
func TestSetReadsAProfileWithoutSettingsAsTheDefaults(t *testing.T) {
	t.Parallel()
	var (
		mu  sync.Mutex
		got *pb.UpdateProfileRequest
	)
	socket := (&scripted{
		list: func() ([]*pb.Profile, error) {
			return []*pb.Profile{profileOf("ID", "home", pb.ProfileState_PROFILE_STATE_DISCONNECTED)}, nil
		},
		update: func(req *pb.UpdateProfileRequest) (*pb.Profile, error) {
			mu.Lock()
			defer mu.Unlock()
			got = req
			return profileOf("ID", "home", pb.ProfileState_PROFILE_STATE_DISCONNECTED), nil
		},
	}).serve(t)
	if r := runAt(t, socket, "", nil, "set", "-auto-connect", "home"); r.code != 0 || r.stdout != "updated home\n" {
		t.Fatalf("set: %+v", r)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := (&pb.ProfileSettings{AutoConnect: true}); got == nil || !proto.Equal(got.Settings, want) {
		t.Errorf("sent %v, want the defaults with auto-connect", got)
	}
}

// detailsOf reads the lines of keyValues back: the name, two or more spaces,
// the value.
func detailsOf(text string) map[string]string {
	details := map[string]string{}
	for line := range strings.Lines(text) {
		name, value, _ := strings.Cut(strings.TrimRight(line, "\n"), "  ")
		details[name] = strings.TrimSpace(value)
	}
	return details
}

// A tunnel that is up keeps the settings it started with; one that the change
// itself makes on-demand start does not.
func TestSetSaysWhenARunningTunnelKeepsItsOldSettings(t *testing.T) {
	t.Parallel()
	const note = "updated home (applies on the next connection)\n"
	for name, c := range map[string]struct {
		runningBefore, runningAfter bool
		flag                        string
		want                        string
	}{
		"tunnel mode of a running tunnel": {true, true, "-tunnel-mode=full", note},
		"private IPs of a running tunnel": {true, true, "-exclude-private-ips", note},
		"setting that applies at once":    {true, true, "-auto-connect", "updated home\n"},
		"tunnel that is not running":      {false, false, "-tunnel-mode=full", "updated home\n"},
		"started by on-demand":            {false, true, "-tunnel-mode=full", "updated home\n"},
	} {
		profileWith := func(running bool, settings *pb.ProfileSettings) *pb.Profile {
			p := profileOf("ID", "home", pb.ProfileState_PROFILE_STATE_DISCONNECTED)
			p.DesiredEnabled, p.Settings = running, settings
			return p
		}
		socket := (&scripted{
			list: func() ([]*pb.Profile, error) {
				return []*pb.Profile{profileWith(c.runningBefore, &pb.ProfileSettings{TunnelMode: pb.TunnelMode_TUNNEL_MODE_AUTO})}, nil
			},
			update: func(req *pb.UpdateProfileRequest) (*pb.Profile, error) {
				return profileWith(c.runningAfter, req.Settings), nil
			},
		}).serve(t)
		if r := runAt(t, socket, "", nil, "set", c.flag, "home"); r.code != 0 || r.stdout != c.want {
			t.Errorf("%s: %+v, want stdout %q", name, r, c.want)
		}
	}
}
