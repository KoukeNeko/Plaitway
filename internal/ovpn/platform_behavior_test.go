package ovpn

import (
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

func TestParseRecordsTheDeviceOfTheProfile(t *testing.T) {
	tests := []struct{ profile, want string }{
		{"client\ndev tun\nremote 192.0.2.1\n", "tun"},
		{"client\ndev tun0\nremote 192.0.2.1\n", "tun0"},
		{"client\ndev null\nremote 192.0.2.1\n", "null"},
		{"client\ndev tap\nremote 192.0.2.1\n", "tap"},
		{"client\ndev tap\ndev tun\nremote 192.0.2.1\n", "tun"}, // the last one wins, as in openvpn
		{"client\nremote 192.0.2.1\n", ""},
	}
	for _, tt := range tests {
		p, err := parseProfile([]byte(tt.profile))
		if err != nil {
			t.Fatalf("%q: %v", tt.profile, err)
		}
		if p.device != tt.want {
			t.Errorf("device of %q = %q, want %q", tt.profile, p.device, tt.want)
		}
	}
}

func TestAddDHCPOption(t *testing.T) {
	tests := []struct {
		option  string
		dns     []netip.Addr
		domains []string
		note    string
	}{
		{"dhcp-option DNS 10.8.0.1", addrs("10.8.0.1"), nil, ""},
		{"dhcp-option DNS6 fd00::1", addrs("fd00::1"), nil, ""},
		{"dhcp-option DOMAIN corp.example", nil, []string{"corp.example"}, ""},
		{"dhcp-option DOMAIN-SEARCH Lan.Example", nil, []string{"lan.example"}, ""},
		{"dhcp-option DNS 127.0.0.1", nil, nil, `DNS server "127.0.0.1" ignored`},
		{"dhcp-option DNS 0.0.0.0", nil, nil, `DNS server "0.0.0.0" ignored`},
		{"dhcp-option DNS not-an-address", nil, nil, `DNS server "not-an-address" ignored`},
		{"dhcp-option DOMAIN bad domain!", nil, nil, ""}, // not three words
		{"dhcp-option DOMAIN a_b/c", nil, nil, `domain "a_b/c" ignored`},
		{"dhcp-option WINS 10.8.0.2", nil, nil, ""},
		{"dhcp-option DISABLE-NBT", nil, nil, ""},
		{"redirect-gateway def1", nil, nil, ""},
		{"", nil, nil, ""},
	}
	for _, tt := range tests {
		var info upInfo
		note := info.addDHCPOption(tt.option)
		if note != tt.note || !reflect.DeepEqual(info.DNS, tt.dns) || !reflect.DeepEqual(info.Domains, tt.domains) {
			t.Errorf("%q: DNS %v, domains %v, note %q; want %v, %v, %q", tt.option, info.DNS, info.Domains, note, tt.dns, tt.domains, tt.note)
		}
	}
	var info upInfo
	info.addDHCPOption("dhcp-option DNS 10.8.0.1")
	info.addDHCPOption("dhcp-option DNS 10.8.0.1")
	if len(info.DNS) != 1 {
		t.Errorf("the same server twice: %v", info.DNS)
	}
}

// Where the engine reads the dhcp-options a server pushed from the log (readsDHCPOptionsFromLog), it
// keeps the ones the profile does not filter, and forgets them with the reply.
func TestPushedDHCPOptionsAreKeptWhereTheEngineReadsThemFromTheLog(t *testing.T) {
	e := newTestEngine(t, shortTempDir(t), minimalProfile+"pull-filter ignore \"dhcp-option DOMAIN\"\n")
	e.onPushReply(pushReply{options: []string{
		"route-gateway 10.8.0.1", "dhcp-option DNS 10.8.0.1", "dhcp-option DOMAIN corp.example", "dhcp-option DNS 10.8.0.2",
	}, more: true})
	e.onPushReply(pushReply{options: []string{"dhcp-option DNS 10.8.0.3"}})

	var want []string
	if readsDHCPOptionsFromLog {
		want = []string{"dhcp-option DNS 10.8.0.1", "dhcp-option DNS 10.8.0.2", "dhcp-option DNS 10.8.0.3"}
	}
	if !reflect.DeepEqual(e.s.pushedDHCP, want) {
		t.Errorf("pushedDHCP = %q, want %q", e.s.pushedDHCP, want)
	}

	e.onPushReply(pushReply{options: []string{"dhcp-option DNS 10.9.0.1"}}) // the next connection
	if readsDHCPOptionsFromLog && !reflect.DeepEqual(e.s.pushedDHCP, []string{"dhcp-option DNS 10.9.0.1"}) {
		t.Errorf("pushedDHCP = %q, the reply of an earlier connection was kept", e.s.pushedDHCP)
	}
}

// openvpn for Windows loses a command that follows a reply too closely, and a
// credential is two commands. The engine therefore sends one release at the
// start, not the two that a pipeline can afford.
func TestPacedConnectionGetsOneReleaseAtTheStart(t *testing.T) {
	h := newHarness(t, harnessOpts{loopback: true})
	h.start()
	h.waitFor("Up", h.stateIs(tunnel.StateUp))
	time.Sleep(500 * time.Millisecond) // a second release would be queued behind the first commands
	if n := strings.Count(h.readRecording("commands"), "hold release\n"); n != 1 {
		t.Errorf("openvpn was told to release the hold %d times, want once:\n%s", n, h.readRecording("commands"))
	}
	h.stop()
}

// The same engine over the management port on loopback, on every OS: it is the
// only channel on Windows, and the others should not find out about a break in
// it a platform late.
func TestEngineOverTheLoopbackChannel(t *testing.T) {
	t.Run("comes up and stops", func(t *testing.T) {
		h := newHarness(t, harnessOpts{loopback: true})
		h.start()
		up := h.waitFor("Up", h.stateIs(tunnel.StateUp))
		if up.Iface != "utun11" || up.Remote != "192.0.2.1:1194" {
			t.Errorf("Up status = %+v", up)
		}
		h.waitFor("byte counts", func(s tunnel.Status) bool { return s.Stats == tunnel.Stats{RxBytes: 1024, TxBytes: 2048} })
		cmds := h.readRecording("commands")
		for _, want := range []string{"state on", "bytecount 2", "log on", "hold release"} {
			if !strings.Contains(cmds, want+"\n") {
				t.Errorf("openvpn never received %q; got:\n%s", want, cmds)
			}
		}
		h.stop()
		if !strings.Contains(h.readRecording("commands"), "signal SIGTERM") {
			t.Error("Stop did not ask openvpn to exit with signal SIGTERM")
		}
		h.requireProcessGone()
		h.requireWorkspaceGone()
		requireNoEngineGoroutines(t)
	})
	t.Run("credentials, a wrong password, then the right one", func(t *testing.T) {
		h := newHarness(t, harnessOpts{loopback: true, env: map[string]string{"OVPN_FAKE_PASSWORD": "right"}})
		h.start()
		h.waitFor("AwaitingCredentials", h.stateIs(tunnel.StateAwaitingCredentials))
		if err := h.eng.ProvideCredentials(tunnel.CredentialUserPassword, "carol", "wrong"); err != nil {
			t.Fatal(err)
		}
		h.waitFor("a rejected password", func(s tunnel.Status) bool {
			return s.State == tunnel.StateAwaitingCredentials && s.Err == "authentication failed"
		})
		if err := h.eng.ProvideCredentials(tunnel.CredentialUserPassword, "carol", "right"); err != nil {
			t.Fatal(err)
		}
		h.waitFor("Up", h.stateIs(tunnel.StateUp))
		cmds := h.readRecording("commands")
		if !strings.Contains(cmds, `username "Auth" "carol"`+"\n") || !strings.Contains(cmds, `password "Auth" "right"`+"\n") {
			t.Errorf("commands:\n%s", cmds)
		}
		h.stop()
		requireNoEngineGoroutines(t)
	})
	t.Run("a network change restarts the connection", func(t *testing.T) {
		h := newHarness(t, harnessOpts{loopback: true})
		h.start()
		h.waitFor("Up", h.stateIs(tunnel.StateUp))
		h.eng.Rebind()
		h.waitForNew("Reconnecting", h.stateIs(tunnel.StateReconnecting))
		h.waitForNew("Up again", h.stateIs(tunnel.StateUp))
		if !strings.Contains(h.readRecording("commands"), "signal SIGUSR1\n") {
			t.Error("Rebind did not send signal SIGUSR1")
		}
		h.stop()
		requireNoEngineGoroutines(t)
	})
	t.Run("an openvpn that does not say where it listens", func(t *testing.T) {
		h := newHarness(t, harnessOpts{
			loopback: true,
			env:      map[string]string{"OVPN_FAKE_NOMGMT": "1"},
			cfgMod:   func(e *engine) { e.dialTimeout = 300e6 },
		})
		h.start()
		failed := h.waitFor("Failed", h.stateIs(tunnel.StateFailed))
		if !strings.Contains(failed.Err, "management port") {
			t.Errorf("Err = %q", failed.Err)
		}
		<-h.collect
		h.requireProcessGone()
		h.requireWorkspaceGone()
		requireNoEngineGoroutines(t)
	})
}

// openvpn for Windows once took the management password for the command that
// followed it, answered it with an error that quotes it, and dropped the
// command. Without state notices the engine is blind, and without the hold
// released openvpn does nothing: it must say so instead of waiting for ever.
// The error quotes the password, which must not reach a log or a status.
func TestEngineFailsWhenOpenVPNRefusesACommandItCannotDoWithout(t *testing.T) {
	for _, command := range []string{"state", "log", "hold"} {
		t.Run(command, func(t *testing.T) {
			h := newHarness(t, harnessOpts{loopback: true, env: map[string]string{"OVPN_FAKE_REFUSE": command}})
			h.start()
			password := h.eng.channel.(*tcpChannel).password // made at the start
			failed := h.waitFor("Failed", h.stateIs(tunnel.StateFailed))
			if want := fmt.Sprintf("openvpn refused the command %q", command); !strings.Contains(failed.Err, want) {
				t.Errorf("Err = %q, want it to say %q", failed.Err, want)
			}
			<-h.collect
			h.requireProcessGone()
			h.requireWorkspaceGone()
			if password == "" {
				t.Fatal("the channel has no password; the test would prove nothing")
			}
			secrets := h.logText() + h.dumpStatuses() + failed.Err
			if strings.Contains(secrets, password) {
				t.Errorf("the management password reached the log or a status:\n%s", secrets)
			}
			requireNoEngineGoroutines(t)
		})
	}
}

func TestEngineGoesOnWithoutByteCounts(t *testing.T) {
	h := newHarness(t, harnessOpts{loopback: true, env: map[string]string{"OVPN_FAKE_REFUSE": "bytecount"}})
	h.start()
	password := h.eng.channel.(*tcpChannel).password // made at the start
	h.waitFor("Up", h.stateIs(tunnel.StateUp))
	if !strings.Contains(h.logText(), `management command "bytecount" failed`) {
		t.Errorf("the refusal was not reported:\n%s", h.logText())
	}
	if strings.Contains(h.logText(), password) {
		t.Errorf("the management password reached the log:\n%s", h.logText())
	}
	h.stop()
}
