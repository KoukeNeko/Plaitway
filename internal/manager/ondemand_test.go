package manager_test

import (
	"bytes"
	"errors"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/osnet"
	osnetfake "github.com/KoukeNeko/Plaitway/internal/osnet/fake"
)

var (
	wifiOnly     = &pb.OnDemandRules{Wifi: true}
	ethernetOnly = &pb.OnDemandRules{Ethernet: true}
	bothKinds    = &pb.OnDemandRules{Ethernet: true, Wifi: true}
)

func networkOf(kind osnet.LinkKind) osnet.NetState {
	return osnet.NetState{DefaultV4: &osnet.Nexthop{Gateway: netip.MustParseAddr("192.168.1.1"), Iface: "en0", Kind: kind}}
}

var noNetwork = osnet.NetState{}

// moveTo makes the pretend Mac join another network and tells the manager.
func moveTo(mon *osnetfake.NetMonitor, state osnet.NetState) {
	mon.Set(state)
	mon.Emit(osnet.Change{Reason: osnet.ChangeRoute})
}

// onDemandEnv is a manager that sees the network the test scripts. On-demand
// is not started: each test starts it when the network is as it wants it.
func onDemandEnv(t *testing.T, state osnet.NetState, opts ...envOption) (*env, *osnetfake.NetMonitor) {
	t.Helper()
	mon := osnetfake.NewNetMonitor()
	mon.Set(state)
	return newEnv(t, append([]envOption{withNet(mon)}, opts...)...), mon
}

func (e *env) importWithRules(name, content string, rules *pb.OnDemandRules) *pb.Profile {
	e.t.Helper()
	return e.importWith(&pb.ImportProfileRequest{Name: name, Content: []byte(content), Settings: &pb.ProfileSettings{OnDemand: rules}}).Profile
}

func (e *env) waitDesired(id string, want bool) {
	e.t.Helper()
	eventually(e.t, id+" to be wanted up="+boolText(want), func() bool { return e.get(id).DesiredEnabled == want })
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// settle gives the manager time to act on what it was last told: more than
// the window in which it waits for a burst of changes to end.
func settle() { time.Sleep(2 * onDemandSettle) }

// syncBuffer is a log destination that the test can read while the manager writes.
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

// stays asserts that nothing changes for a few settle windows, long enough for
// the manager to have acted on an event it was sent.
func (e *env) stays(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(3 * onDemandSettle)
	for time.Now().Before(deadline) {
		if !cond() {
			e.t.Fatalf("%s did not hold", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestOnDemandMatchesTheNetworkWhenItStarts(t *testing.T) {
	e, _ := onDemandEnv(t, networkOf(osnet.LinkWiFi))
	onWiFi := e.importWithRules("wifi", ovpnProfile, wifiOnly)
	onEthernet := e.importWithRules("ethernet", wgProfile, ethernetOnly)
	plain := e.importProfile("plain", ovpnProfile)
	e.setEnabled(onEthernet.Id, true) // wanted up now, but the rules say Ethernet
	e.setEnabled(plain.Id, true)

	e.m.StartOnDemand()

	e.waitState(onWiFi.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
	e.waitState(onEthernet.Id, pb.ProfileState_PROFILE_STATE_DISCONNECTED)
	if got := e.get(onEthernet.Id); got.DesiredEnabled {
		t.Fatalf("the Ethernet profile is still wanted up: %v", got)
	}
	if got := e.get(plain.Id); !got.DesiredEnabled {
		t.Fatalf("a profile without rules was touched: %v", got)
	}
}

func TestOnDemandFollowsTheKindOfThePrimaryNetwork(t *testing.T) {
	e, mon := onDemandEnv(t, networkOf(osnet.LinkWiFi))
	wifi := e.importWithRules("wifi", ovpnProfile, wifiOnly)
	ethernet := e.importWithRules("ethernet", wgProfile, ethernetOnly)
	both := e.importWithRules("both", ovpnProfile, bothKinds)
	e.m.StartOnDemand()

	check := func(network string, up ...*pb.Profile) {
		t.Helper()
		for _, p := range []*pb.Profile{wifi, ethernet, both} {
			want := slices.ContainsFunc(up, func(u *pb.Profile) bool { return u.Id == p.Id })
			e.waitDesired(p.Id, want)
			if want {
				e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
			} else {
				e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_DISCONNECTED)
			}
		}
		t.Logf("on %s: ok", network)
	}

	check("Wi-Fi", wifi, both)
	moveTo(mon, networkOf(osnet.LinkEthernet))
	check("Ethernet", ethernet, both)
	moveTo(mon, networkOf(osnet.LinkOther)) // a phone tethered over USB, or something unknown
	check("another kind")
	moveTo(mon, networkOf(osnet.LinkWiFi))
	check("Wi-Fi again", wifi, both)
}

// The user's connect or disconnect stands until the kind of the primary
// network changes, however many other changes the network reports meanwhile.
func TestAManualChoiceHoldsUntilTheKindChanges(t *testing.T) {
	e, mon := onDemandEnv(t, networkOf(osnet.LinkWiFi))
	p := e.importWithRules("wifi", ovpnProfile, wifiOnly)
	e.m.StartOnDemand()
	e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)

	e.setEnabled(p.Id, false)
	for range 3 {
		moveTo(mon, networkOf(osnet.LinkWiFi)) // same kind: a new gateway, a wake, a heartbeat
	}
	e.stays("the manual disconnect", func() bool { return !e.get(p.Id).DesiredEnabled })

	moveTo(mon, networkOf(osnet.LinkEthernet))
	e.stays("the profile staying down on a kind it does not check", func() bool { return !e.get(p.Id).DesiredEnabled })
	moveTo(mon, networkOf(osnet.LinkWiFi))
	e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)

	// And the other way round: connected by hand on a kind the rules do not check.
	moveTo(mon, networkOf(osnet.LinkEthernet))
	e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_DISCONNECTED)
	e.setEnabled(p.Id, true)
	for range 3 {
		moveTo(mon, networkOf(osnet.LinkEthernet))
	}
	e.stays("the manual connect", func() bool { return e.get(p.Id).DesiredEnabled })
	moveTo(mon, networkOf(osnet.LinkOther))
	e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_DISCONNECTED)
}

func TestNoNetworkChangesNothing(t *testing.T) {
	e, mon := onDemandEnv(t, networkOf(osnet.LinkWiFi))
	p := e.importWithRules("wifi", ovpnProfile, wifiOnly)
	e.m.StartOnDemand()
	e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)

	// Offline is not a kind of network: the tunnel is not taken down.
	moveTo(mon, noNetwork)
	e.stays("the connection while offline", func() bool { return e.get(p.Id).DesiredEnabled })

	// Back as the same kind is no change either, so a manual choice made
	// meanwhile is kept.
	e.setEnabled(p.Id, false)
	moveTo(mon, networkOf(osnet.LinkWiFi))
	e.stays("the manual disconnect after the network came back", func() bool { return !e.get(p.Id).DesiredEnabled })

	// Back as another kind is one, and the kind it came back as is the one
	// that counts, however long the network was away.
	moveTo(mon, noNetwork)
	moveTo(mon, networkOf(osnet.LinkEthernet))
	settle()
	moveTo(mon, noNetwork)
	moveTo(mon, networkOf(osnet.LinkWiFi))
	e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
}

func TestOnDemandStartedOffline(t *testing.T) {
	e, mon := onDemandEnv(t, noNetwork)
	p := e.importWithRules("wifi", ovpnProfile, wifiOnly)
	e.m.StartOnDemand()
	e.stays("the profile staying down without a network", func() bool { return !e.get(p.Id).DesiredEnabled })

	moveTo(mon, networkOf(osnet.LinkWiFi)) // the first network is a change
	e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
}

// A flapping network is judged by where it ends, not by each step.
func TestABurstOfChangesIsJudgedByWhereItEnded(t *testing.T) {
	e, mon := onDemandEnv(t, networkOf(osnet.LinkWiFi))
	p := e.importWithRules("wifi", ovpnProfile, wifiOnly)
	e.m.StartOnDemand()
	e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
	w := e.watch()

	// Ethernet comes and goes inside one window: no disconnect, no reconnect.
	moveTo(mon, networkOf(osnet.LinkEthernet))
	time.Sleep(10 * time.Millisecond)
	moveTo(mon, networkOf(osnet.LinkWiFi))
	e.stays("the connection through the burst", func() bool {
		return e.get(p.Id).State == pb.ProfileState_PROFILE_STATE_CONNECTED
	})
	if got := w.states(p.Id); !slices.Equal(got, []pb.ProfileState{pb.ProfileState_PROFILE_STATE_CONNECTED}) {
		t.Fatalf("the profile went through %v during a burst that ended where it began", got)
	}

	// A burst that ends on another kind acts once, on that kind.
	moveTo(mon, networkOf(osnet.LinkEthernet))
	time.Sleep(10 * time.Millisecond)
	moveTo(mon, networkOf(osnet.LinkWiFi))
	time.Sleep(10 * time.Millisecond)
	moveTo(mon, networkOf(osnet.LinkOther))
	e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_DISCONNECTED)
	want := []pb.ProfileState{pb.ProfileState_PROFILE_STATE_CONNECTED, pb.ProfileState_PROFILE_STATE_DISCONNECTING, pb.ProfileState_PROFILE_STATE_DISCONNECTED}
	// waitState reads the manager; the recorder is fed through the event stream, a goroutine later.
	var got []pb.ProfileState
	eventually(t, "the watcher to see the disconnect", func() bool {
		got = w.states(p.Id)
		return slices.Contains(got, pb.ProfileState_PROFILE_STATE_DISCONNECTED)
	})
	if !slices.Equal(got, want) {
		t.Fatalf("states = %v, want %v", got, want)
	}
}

func TestIPv6DefaultRouteDecidesOnlyWithoutAnIPv4One(t *testing.T) {
	v6 := func(kind osnet.LinkKind) *osnet.Nexthop {
		return &osnet.Nexthop{Gateway: netip.MustParseAddr("fe80::1"), Iface: "en5", Kind: kind}
	}
	e, mon := onDemandEnv(t, osnet.NetState{DefaultV6: v6(osnet.LinkEthernet)})
	wifi := e.importWithRules("wifi", ovpnProfile, wifiOnly)
	ethernet := e.importWithRules("ethernet", wgProfile, ethernetOnly)
	e.m.StartOnDemand()
	e.waitState(ethernet.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
	if e.get(wifi.Id).DesiredEnabled {
		t.Fatal("the Wi-Fi profile is up on an Ethernet IPv6 network")
	}

	// With an IPv4 default route it is that one that counts.
	both := networkOf(osnet.LinkWiFi)
	both.DefaultV6 = v6(osnet.LinkEthernet)
	moveTo(mon, both)
	e.waitState(wifi.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
	e.waitState(ethernet.Id, pb.ProfileState_PROFILE_STATE_DISCONNECTED)
}

func TestOnDemandSurvivesANetworkThatCannotBeRead(t *testing.T) {
	e, mon := onDemandEnv(t, networkOf(osnet.LinkEthernet))
	p := e.importWithRules("wifi", ovpnProfile, wifiOnly)
	e.m.StartOnDemand()

	mon.FailSnapshot(errors.New("sysctl failed"))
	moveTo(mon, networkOf(osnet.LinkWiFi))
	e.stays("the profile staying down while the network cannot be read", func() bool { return !e.get(p.Id).DesiredEnabled })

	mon.FailSnapshot(nil)
	moveTo(mon, networkOf(osnet.LinkWiFi))
	e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
}

// A profile that connects at startup is still matched to the network: the rules
// are the more specific statement.
func TestOnDemandOverridesAutoConnectAtStartup(t *testing.T) {
	dir := t.TempDir()
	first := startEnv(t, dir)
	auto := first.importWith(&pb.ImportProfileRequest{
		Name: "auto", Content: []byte(ovpnProfile),
		Settings: &pb.ProfileSettings{AutoConnect: true, OnDemand: ethernetOnly},
	}).Profile
	first.shutdown()

	mon := osnetfake.NewNetMonitor()
	mon.Set(networkOf(osnet.LinkWiFi))
	second := startEnv(t, dir, withNet(mon))
	if !second.get(auto.Id).DesiredEnabled {
		t.Fatal("auto_connect did not start the profile")
	}
	second.m.StartOnDemand()
	second.waitState(auto.Id, pb.ProfileState_PROFILE_STATE_DISCONNECTED)
	second.waitDesired(auto.Id, false)
}

func TestSavingRulesAppliesThemToTheCurrentNetworkAtOnce(t *testing.T) {
	// StartOnDemand is not needed for this: saving looks at the network itself.
	e, _ := onDemandEnv(t, networkOf(osnet.LinkWiFi))
	p := e.importProfile("office", ovpnProfile)
	bystander := e.importWithRules("bystander", wgProfile, wifiOnly) // set by hand to stay down

	got, err := e.updateSettings(p.Id, &pb.ProfileSettings{OnDemand: wifiOnly})
	if err != nil {
		t.Fatal(err)
	}
	if !got.DesiredEnabled || got.State == pb.ProfileState_PROFILE_STATE_DISCONNECTED {
		t.Fatalf("the response to saving Wi-Fi on Wi-Fi: %v", got)
	}
	e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)

	got, err = e.updateSettings(p.Id, &pb.ProfileSettings{OnDemand: ethernetOnly})
	if err != nil {
		t.Fatal(err)
	}
	if got.DesiredEnabled || got.State != pb.ProfileState_PROFILE_STATE_DISCONNECTED {
		t.Fatalf("the response to saving Ethernet on Wi-Fi: %v", got)
	}

	// Saving one profile's rules does not make the others match: their manual
	// state holds until the network changes.
	if e.get(bystander.Id).DesiredEnabled {
		t.Fatalf("a profile whose rules were not saved was connected: %v", e.get(bystander.Id))
	}
}

func TestSavingOtherSettingsKeepsAManualChoice(t *testing.T) {
	e, _ := onDemandEnv(t, networkOf(osnet.LinkWiFi))
	p := e.importProfile("office", ovpnProfile)
	if _, err := e.updateSettings(p.Id, &pb.ProfileSettings{OnDemand: wifiOnly}); err != nil {
		t.Fatal(err)
	}
	e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
	e.setEnabled(p.Id, false)

	// The same rules again, with another setting, and a new name.
	name := "renamed"
	got, err := e.m.Update(&pb.UpdateProfileRequest{Id: p.Id, Name: &name, Settings: &pb.ProfileSettings{AutoConnect: true, OnDemand: wifiOnly}})
	if err != nil {
		t.Fatal(err)
	}
	if got.DesiredEnabled {
		t.Fatalf("saving unchanged rules reconnected the profile: %v", got)
	}
	// Turning the rules off leaves the profile as it is.
	if got, err = e.updateSettings(p.Id, &pb.ProfileSettings{}); err != nil || got.DesiredEnabled {
		t.Fatalf("after turning the rules off: %v, %v", got, err)
	}
}

func TestSavingRulesWithoutANetworkChangesNothing(t *testing.T) {
	e, _ := onDemandEnv(t, noNetwork)
	p := e.importProfile("office", ovpnProfile)
	e.setEnabled(p.Id, true)
	got, err := e.updateSettings(p.Id, &pb.ProfileSettings{OnDemand: ethernetOnly})
	if err != nil {
		t.Fatal(err)
	}
	if !got.DesiredEnabled {
		t.Fatalf("saving rules while offline disconnected the profile: %v", got)
	}
}

func TestOnDemandActionsAreLoggedAtInfo(t *testing.T) {
	var out syncBuffer
	log := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelInfo}))
	e, mon := onDemandEnv(t, networkOf(osnet.LinkWiFi), withLogger(log))
	p := e.importWithRules("wifi", ovpnProfile, wifiOnly)
	e.m.StartOnDemand()
	e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
	moveTo(mon, networkOf(osnet.LinkEthernet))
	e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_DISCONNECTED)

	var actions []string
	for line := range strings.Lines(out.String()) {
		if strings.Contains(line, `msg="on demand"`) {
			actions = append(actions, line)
		}
	}
	if len(actions) != 2 ||
		!strings.Contains(actions[0], "level=INFO") || !strings.Contains(actions[0], "profile="+p.Id) || !strings.Contains(actions[0], "network=Wi-Fi") || !strings.Contains(actions[0], "connect=true") ||
		!strings.Contains(actions[1], "network=Ethernet") || !strings.Contains(actions[1], "connect=false") {
		t.Fatalf("on-demand log lines = %q, want one for connecting on Wi-Fi and one for disconnecting on Ethernet", actions)
	}

	// The profile's own log says why it started and stopped.
	lines, _, cancel, err := e.m.Logs(p.Id, 100)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	var texts []string
	for _, l := range lines {
		texts = append(texts, l.Text)
	}
	if joined := strings.Join(texts, "\n"); !strings.Contains(joined, "on demand: connecting, the network is Wi-Fi") || !strings.Contains(joined, "on demand: disconnecting, the network is Ethernet") {
		t.Fatalf("profile log = %q", joined)
	}
}

// A profile that already is as the rules want it is not touched, so a kind
// change from one checked network to another does not restart its tunnel.
func TestOnDemandLeavesAProfileAloneThatIsAlreadyAsWanted(t *testing.T) {
	var out syncBuffer
	log := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelInfo}))
	e, mon := onDemandEnv(t, networkOf(osnet.LinkWiFi), withLogger(log))
	p := e.importWithRules("both", ovpnProfile, bothKinds)
	e.m.StartOnDemand()
	e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)
	w := e.watch()

	moveTo(mon, networkOf(osnet.LinkEthernet))
	settle()

	if n := strings.Count(out.String(), `msg="on demand"`); n != 1 {
		t.Fatalf("%d on-demand actions logged, want only the first connect", n)
	}
	for _, state := range w.states(p.Id) {
		if state != pb.ProfileState_PROFILE_STATE_CONNECTED {
			t.Fatalf("the profile went through %v when the network changed from one checked kind to another", w.states(p.Id))
		}
	}
}

func TestOnDemandLeavesProfilesWithoutRulesAlone(t *testing.T) {
	e, mon := onDemandEnv(t, networkOf(osnet.LinkWiFi))
	plain := e.importProfile("plain", ovpnProfile)
	e.setEnabled(plain.Id, true)
	other := e.importProfile("other", wgProfile)
	e.m.StartOnDemand()

	moveTo(mon, networkOf(osnet.LinkEthernet))
	moveTo(mon, networkOf(osnet.LinkOther))
	e.stays("the profiles without rules", func() bool {
		return e.get(plain.Id).DesiredEnabled && !e.get(other.Id).DesiredEnabled
	})
}

func TestOnDemandDoesNothingOnceTheDaemonIsShuttingDown(t *testing.T) {
	e, mon := onDemandEnv(t, networkOf(osnet.LinkWiFi))
	p := e.importWithRules("wifi", wgProfile, wifiOnly)
	e.m.StartOnDemand()
	e.waitState(p.Id, pb.ProfileState_PROFILE_STATE_CONNECTED)

	e.shutdown()
	moveTo(mon, networkOf(osnet.LinkEthernet))
	e.stays("the profile as shutdown left it", func() bool { return e.get(p.Id).DesiredEnabled })
}

// Editing, connecting, saving rules and a changing network at the same time:
// run under -race, and nothing may deadlock or fail.
func TestConcurrentEditsConnectsAndNetworkChanges(t *testing.T) {
	e, mon := onDemandEnv(t, networkOf(osnet.LinkWiFi))
	e.m.StartOnDemand()

	stop := make(chan struct{})
	var flipper sync.WaitGroup
	flipper.Add(1)
	go func() {
		defer flipper.Done()
		kinds := []osnet.LinkKind{osnet.LinkEthernet, osnet.LinkWiFi, osnet.LinkOther}
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				moveTo(mon, networkOf(kinds[i%len(kinds)]))
			}
		}
	}()

	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := e.importWithRules("p"+string(rune('a'+i)), ovpnProfile, wifiOnly)
			for j := range 4 {
				if _, err := e.m.UpdateContent(&pb.UpdateProfileContentRequest{Id: p.Id, Content: []byte(ovpnEdited), Reconnect: j%2 == 0}); err != nil {
					t.Error(err)
				}
				if _, err := e.m.SetEnabled(p.Id, j%2 == 1); err != nil {
					t.Error(err)
				}
				rules := &pb.OnDemandRules{Ethernet: j%2 == 0, Wifi: j%2 == 1}
				if _, err := e.updateSettings(p.Id, &pb.ProfileSettings{OnDemand: rules}); err != nil {
					t.Error(err)
				}
				if _, err := e.m.GetContent(p.Id); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
	flipper.Wait()
	if n := len(e.m.List()); n != 4 {
		t.Fatalf("%d profiles, want 4", n)
	}
}
