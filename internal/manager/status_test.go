package manager_test

import (
	"net/netip"
	"slices"
	"testing"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/manager/fake"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// A tunnel whose routes the Reconciler could not install is up, but it carries
// no traffic. It stays CONNECTED, which is what the engine says, and says why
// in its warnings.
func TestConnectedProfileWithNothingInstalledSaysSo(t *testing.T) {
	var engine *stubEngine
	stub := &stubBackend{kind: tunnel.KindOpenVPN, onEngine: func(s *stubEngine) { engine = s }}
	rec := fake.NewReconciler()
	e := newEnv(t, withBackends(stub.backend()), withReconciler(rec))
	p := e.importProfile("a", ovpnProfile)
	e.setEnabled(p.Id, true)

	announce := func(routes ...string) {
		in := tunnel.Intent{Owner: tunnel.OwnerID(p.Id), State: tunnel.StateUp, Iface: "utun9"}
		for _, r := range routes {
			in.Routes = append(in.Routes, netip.MustParsePrefix(r))
		}
		if err := engine.deps.Network.Announce(in); err != nil {
			t.Fatal(err)
		}
	}
	// The fake Reconciler blocks routes into the pretend local network 192.168.0.0/24.
	announce("192.168.0.0/24")
	engine.send(tunnel.Status{State: tunnel.StateUp, Iface: "utun9"})

	const want = "1 route not installed: overlaps the local network 192.168.0.0/24"
	eventually(t, "the warning", func() bool {
		cur := e.get(p.Id)
		return cur.State == pb.ProfileState_PROFILE_STATE_CONNECTED && slices.Contains(cur.Status.GetWarnings(), want)
	})
	if cur := e.get(p.Id); cur.LastError != "" {
		t.Fatalf("profile = %v: a tunnel that is up has no error", cur)
	}

	// Once something is installed the profile is not warned about any more.
	announce("192.168.0.0/24", "10.1.0.0/16")
	eventually(t, "the warning to go", func() bool { return len(e.get(p.Id).Status.GetWarnings()) == 0 })
}

// What an engine knows about why it cannot connect is shown while it keeps
// trying, and goes away with the next status that has no error.
func TestEngineErrorWhileConnectingIsShownAndCleared(t *testing.T) {
	var engine *stubEngine
	stub := &stubBackend{kind: tunnel.KindOpenVPN, onEngine: func(s *stubEngine) { engine = s }}
	e := newEnv(t, withBackends(stub.backend()))
	p := e.importProfile("a", ovpnProfile)
	e.setEnabled(p.Id, true)

	const reason = "cannot resolve vpn.example.com: no such host"
	engine.send(tunnel.Status{State: tunnel.StateConnecting, Err: reason})
	eventually(t, "the reason", func() bool {
		cur := e.get(p.Id)
		return cur.State == pb.ProfileState_PROFILE_STATE_CONNECTING && cur.LastError == reason
	})

	engine.send(tunnel.Status{State: tunnel.StateConnecting, Err: "no route to host"})
	eventually(t, "the new reason", func() bool { return e.get(p.Id).LastError == "no route to host" })

	engine.send(tunnel.Status{State: tunnel.StateUp, Iface: "utun9"})
	eventually(t, "the reason to be cleared", func() bool {
		cur := e.get(p.Id)
		return cur.State == pb.ProfileState_PROFILE_STATE_CONNECTED && cur.LastError == ""
	})
}
