package ovpn

import (
	"bytes"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

func TestLineSink(t *testing.T) {
	var got []string
	s := &lineSink{emit: func(l tunnel.LogLevel, text string) { got = append(got, text) }}
	for _, chunk := range []string{"first li", "ne\nsecond line\r\n\n  \nthi", "rd\n", "partial"} {
		if n, err := s.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("Write(%q) = %d, %v", chunk, n, err)
		}
	}
	if want := []string{"first line", "second line", "third"}; !reflect.DeepEqual(got, want) {
		t.Errorf("lines before flush = %q, want %q", got, want)
	}
	s.flush()
	if want := []string{"first line", "second line", "third", "partial"}; !reflect.DeepEqual(got, want) {
		t.Errorf("lines after flush = %q, want %q", got, want)
	}
}

func TestLineSinkBoundsAnUnterminatedLine(t *testing.T) {
	var got []string
	s := &lineSink{emit: func(l tunnel.LogLevel, text string) { got = append(got, text) }}
	s.Write([]byte(strings.Repeat("x", maxOutputLine+10)))
	if len(got) != 1 || len(got[0]) != maxOutputLine+10 {
		t.Fatalf("got %d lines", len(got))
	}
	if len(s.buf) != 0 {
		t.Errorf("%d bytes are still buffered", len(s.buf))
	}
}

func TestOutputLevel(t *testing.T) {
	tests := []struct {
		line string
		want tunnel.LogLevel
	}{
		{"Options error: Unrecognized option or missing or extra parameter(s) in config:1: frobnicate (2.7.7)", tunnel.LogError},
		{"ERROR: could not read Auth username/password/ok/string from management interface", tunnel.LogError},
		{"Exiting due to fatal error", tunnel.LogError},
		{"Cannot open TUN/TAP dev /dev/tun0: Permission denied (errno=13)", tunnel.LogError},
		{"WARNING: Compression for receiving enabled.", tunnel.LogWarn},
		{"DEPRECATED OPTION: --persist-key option ignored.", tunnel.LogWarn},
		{"2026-10-07 17:24:38 us=287682   config = '[UNDEF]'", tunnel.LogDebug},
		{"OpenVPN 2.7.7 aarch64-apple-darwin27.0.0", tunnel.LogDebug},
	}
	for _, tt := range tests {
		if got := outputLevel(tt.line); got != tt.want {
			t.Errorf("outputLevel(%q) = %v, want %v", tt.line, got, tt.want)
		}
	}
}

func TestScrub(t *testing.T) {
	e := newTestEngine(t, shortTempDir(t), minimalProfile)
	if err := e.ProvideCredentials(tunnel.CredentialUserPassword, "alice", "hunter2hunter2"); err != nil {
		t.Fatal(err)
	}
	if err := e.ProvideCredentials(tunnel.CredentialKeyPassphrase, "", "open sesame"); err != nil {
		t.Fatal(err)
	}
	tests := []struct{ in, want string }{
		{"nothing secret here", "nothing secret here"},
		{"auth with hunter2hunter2 failed", "auth with [redacted] failed"},
		{"hunter2hunter2 and open sesame and hunter2hunter2", "[redacted] and [redacted] and [redacted]"},
		{"MANAGEMENT: CMD 'password [...]'", "MANAGEMENT: credential command [redacted]"},
		{`MANAGEMENT: CMD 'username "Auth" "alice"'`, "MANAGEMENT: credential command [redacted]"},
		{"MANAGEMENT: CMD 'state on'", "MANAGEMENT: CMD 'state on'"},
	}
	for _, tt := range tests {
		if got := e.scrub(tt.in); got != tt.want {
			t.Errorf("scrub(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestScrubRemembersRejectedPasswords(t *testing.T) {
	e := newTestEngine(t, shortTempDir(t), minimalProfile)
	if err := e.ProvideCredentials(tunnel.CredentialUserPassword, "alice", "oldpassword"); err != nil {
		t.Fatal(err)
	}
	e.handle(passwordFailed{Type: "Auth"}, nil) // the credentials are dropped...
	if e.userPass.set {
		t.Fatal("the rejected credentials were kept")
	}
	if got := e.scrub("late line: oldpassword"); got != "late line: [redacted]" { // ...but still never logged
		t.Errorf("scrub = %q", got)
	}
}

func TestPrivateKeyPassphraseRejected(t *testing.T) {
	e := newTestEngine(t, shortTempDir(t), minimalProfile)
	if err := e.ProvideCredentials(tunnel.CredentialKeyPassphrase, "", "bad pass"); err != nil {
		t.Fatal(err)
	}
	if err := e.ProvideCredentials(tunnel.CredentialUserPassword, "alice", "keep me"); err != nil {
		t.Fatal(err)
	}
	if err := e.handle(passwordFailed{Type: "Private Key"}, nil); err != nil {
		t.Fatal(err)
	}
	if e.keyPass.set || !e.userPass.set {
		t.Errorf("only the key passphrase should be dropped: key %v, user %v", e.keyPass.set, e.userPass.set)
	}
	st := <-e.Status()
	if st.State != tunnel.StateAwaitingCredentials || st.NeedsCredentials != tunnel.CredentialKeyPassphrase || st.Err != "wrong key passphrase" {
		t.Errorf("status = %+v", st)
	}
}

func TestRefreshStateDerivation(t *testing.T) {
	tests := []struct {
		name  string
		s     session
		state tunnel.State
	}{
		{"fresh", session{}, tunnel.StateConnecting},
		{"tunnel reported before CONNECTED on a first connect", session{upKnown: true}, tunnel.StateConnecting},
		{"CONNECTED without a tunnel report", session{connected: true}, tunnel.StateConnecting},
		{"CONNECTED and reported", session{connected: true, upKnown: true}, tunnel.StateUp},
		{"after Up, any other state is a reconnect", session{upKnown: true, everUp: true}, tunnel.StateReconnecting},
		{"credentials win over everything but a stop", session{needCreds: tunnel.CredentialUserPassword, connected: true, upKnown: true}, tunnel.StateAwaitingCredentials},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEngine(t, shortTempDir(t), minimalProfile)
			e.s = tt.s
			e.refreshState()
			if got := (<-e.Status()).State; got != tt.state {
				t.Errorf("state = %v, want %v", got, tt.state)
			}
			if tt.state == tunnel.StateUp && !e.s.everUp {
				t.Error("Up must be remembered, so that later states read as reconnects")
			}
		})
	}
}

func TestSoftAuthFailureKeepsCredentials(t *testing.T) {
	provide := func(e *engine) {
		if err := e.ProvideCredentials(tunnel.CredentialUserPassword, "alice", "hunter2hunter2"); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("session token refused once", func(t *testing.T) {
		e := newTestEngine(t, shortTempDir(t), minimalProfile)
		provide(e)
		e.handle(authTokenEvent{}, nil)
		e.handle(passwordFailed{Type: "Auth"}, nil)
		if !e.userPass.set || e.s.needCreds != tunnel.CredentialNone || e.s.credErr != "" {
			t.Errorf("a refused session token dropped the credentials: set %v, needCreds %v, err %q", e.userPass.set, e.s.needCreds, e.s.credErr)
		}
		// openvpn used the token up. The next failure is the server refusing
		// the password itself.
		e.handle(passwordFailed{Type: "Auth"}, nil)
		if e.userPass.set || e.s.needCreds != tunnel.CredentialUserPassword || e.s.credErr != "authentication failed" {
			t.Errorf("a second failure must reject the password: set %v, needCreds %v, err %q", e.userPass.set, e.s.needCreds, e.s.credErr)
		}
	})
	t.Run("temporary rejection", func(t *testing.T) {
		e := newTestEngine(t, shortTempDir(t), minimalProfile)
		provide(e)
		e.handle(passwordFailed{Type: "Auth", Reason: "TEMP[backoff 5]:busy"}, nil)
		e.handle(passwordFailed{Type: "Auth", Reason: "TEMP[backoff 5]:busy"}, nil)
		if !e.userPass.set || e.s.needCreds != tunnel.CredentialNone {
			t.Errorf("a temporary rejection dropped the credentials")
		}
	})
	t.Run("token does not save a wrong key passphrase", func(t *testing.T) {
		e := newTestEngine(t, shortTempDir(t), minimalProfile)
		e.handle(authTokenEvent{}, nil)
		e.handle(passwordFailed{Type: "Private Key"}, nil)
		if e.s.needCreds != tunnel.CredentialKeyPassphrase {
			t.Errorf("needCreds = %v", e.s.needCreds)
		}
	})
}

// The token is a credential: it reaches neither the logs nor a status.
func TestAuthTokenIsNeverLogged(t *testing.T) {
	var daemonLog bytes.Buffer
	var engineLog []string
	b := Backend(Config{Binary: "/nonexistent/openvpn", RunDir: shortTempDir(t),
		Log: slog.New(slog.NewTextHandler(&daemonLog, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	eng, err := b.New(tunnel.Spec{Owner: "o", Content: []byte(minimalProfile)}, tunnel.Deps{
		Network: &fakeNetwork{},
		Log:     func(_ tunnel.LogLevel, text string) { engineLog = append(engineLog, text) },
	})
	if err != nil {
		t.Fatal(err)
	}
	e := eng.(*engine)
	if err := e.handle(parseNotification(">PASSWORD:Auth-Token:SESS_ID_AT_s3cr3ttoken"), nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(daemonLog.String(), "s3cr3ttoken") || strings.Contains(strings.Join(engineLog, "\n"), "s3cr3ttoken") {
		t.Errorf("the session token was logged:\n%s\n%v", daemonLog.String(), engineLog)
	}
	if !e.s.tokenHeld {
		t.Error("the engine did not note that openvpn holds a token")
	}
}

func TestRefreshStateSaysWhyOnlyWhenTheConnectionTakesTooLong(t *testing.T) {
	e := newTestEngine(t, shortTempDir(t), minimalProfile)
	e.stuckGrace = time.Hour
	status := func() tunnel.Status {
		t.Helper()
		select {
		case st := <-e.Status():
			return st
		default:
			t.Fatal("no status was published")
			return tunnel.Status{}
		}
	}

	e.refreshState()
	e.setCause("cannot reach 192.0.2.1:1194: connection refused", true)
	if st := status(); st.Err != "" {
		t.Errorf("Err = %q before the connection has taken too long", st.Err)
	}
	e.onStuck()
	if st := status(); st.State != tunnel.StateConnecting || st.Err != "cannot reach 192.0.2.1:1194: connection refused" {
		t.Errorf("status = %+v", st)
	}
	e.setCause("cannot reach 192.0.2.1:1194: network is unreachable", true)
	if st := status(); st.Err != "cannot reach 192.0.2.1:1194: network is unreachable" {
		t.Errorf("the newer cause is not shown: %+v", st)
	}

	// A wait for the user is not a connection that does not come up: the
	// countdown and the cause start over, and the first request for
	// credentials carries no error, which the app would read as a rejection.
	e.s.needCreds = tunnel.CredentialUserPassword
	e.refreshState()
	if st := status(); st.State != tunnel.StateAwaitingCredentials || st.Err != "" {
		t.Errorf("status = %+v, want a request for credentials without an error", st)
	}
	e.s.needCreds = tunnel.CredentialNone
	e.refreshState()
	status()
	e.onStuck()
	status()
	e.s.needCreds = tunnel.CredentialKeyPassphrase
	e.s.credErr = "wrong key passphrase"
	e.refreshState()
	if st := status(); st.State != tunnel.StateAwaitingCredentials || st.Err != "wrong key passphrase" {
		t.Errorf("status = %+v", st)
	}
	e.s.needCreds, e.s.credErr = tunnel.CredentialNone, ""
	e.refreshState()
	if st := status(); st.State != tunnel.StateConnecting || st.Err != "" {
		t.Errorf("a stale error stayed after the credentials were answered: %+v", st)
	}

	// Silence from the server is a reason of its own, in the states that wait for it.
	e.s.vpnState = "WAIT"
	e.onStuck()
	if st := status(); st.Err != "no response from the server" {
		t.Errorf("status = %+v", st)
	}
	e.s.vpnState = "AUTH"
	e.refreshState()
	if st := status(); st.Err != "" {
		t.Errorf("Err = %q for a server that answers", st.Err)
	}

	// Up clears it all.
	e.s.connected, e.s.upKnown = true, true
	e.setCause("cannot reach 192.0.2.1:1194: connection refused", true)
	e.refreshState()
	if st := status(); st.State != tunnel.StateUp || st.Err != "" || e.s.stuck || e.s.cause != "" {
		t.Errorf("status = %+v, stuck %v, cause %q", st, e.s.stuck, e.s.cause)
	}
}

func TestConnectFailure(t *testing.T) {
	tests := []struct {
		line, want string
		reach      bool
	}{
		// Lines as openvpn 2.7.7 writes them.
		{"TCP: connect to [AF_INET]127.0.0.1:1 failed: Connection refused", "cannot reach 127.0.0.1:1: connection refused", true},
		{"TCP: connect to [AF_INET]203.0.113.88:443 failed: Can't assign requested address", "cannot reach 203.0.113.88:443: can't assign requested address", true},
		{"TCP: connect to [AF_INET6]2001:db8::1:1194 failed: No route to host", "cannot reach 2001:db8::1:1194: no route to host", true},
		{"RESOLVE: Cannot resolve host address: no-such-host.invalid:1194 (nodename nor servname provided, or not known)", "cannot resolve no-such-host.invalid: nodename nor servname provided, or not known", true},
		{"VERIFY ERROR: depth=0, error=certificate has expired: C=TW, CN=server", "server certificate rejected: certificate has expired", false},
		{"TLS Error: TLS key negotiation failed to occur within 60 seconds (check your network connectivity)", "no response from the server: TLS handshake timed out", false},
		{"Attempting to establish TCP connection with [AF_INET]127.0.0.1:1", "", false},
		{"SIGUSR1[connection failed(soft),connection-failed] received, process restarting", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		if got, reach := connectFailure(tt.line); got != tt.want || reach != tt.reach {
			t.Errorf("connectFailure(%q) = %q, %v; want %q, %v", tt.line, got, reach, tt.want, tt.reach)
		}
	}
}

// A refused connection explains a stall until openvpn has reached the server;
// after that it would say something false about a handshake that hangs. What
// goes wrong after the connection stays shown.
func TestCauseOfAnUnreachableServerEndsWhenItIsReached(t *testing.T) {
	e := newTestEngine(t, shortTempDir(t), minimalProfile)
	e.refreshState()
	e.onStuck()

	e.setCause("cannot reach 192.0.2.1:1194: connection refused", true)
	e.onState(stateEvent{Name: "TCP_CONNECT"})
	if e.s.cause == "" {
		t.Error("the cause went while openvpn was still connecting")
	}
	e.onState(stateEvent{Name: "WAIT"})
	if e.s.cause != "" {
		t.Errorf("cause = %q after the server was reached", e.s.cause)
	}

	e.setCause("server certificate rejected: certificate has expired", false)
	e.onState(stateEvent{Name: "AUTH"})
	e.onState(stateEvent{Name: "RECONNECTING"})
	e.onState(stateEvent{Name: "WAIT"})
	if e.s.cause == "" {
		t.Error("the cause of a failed handshake went with the next attempt")
	}
}

func TestReadableTail(t *testing.T) {
	got := readableTail([]string{
		"2026-10-07 20:20:12 us=996428 Opening utun254 failed (connect(AF_SYS_CONTROL)): Operation not permitted (errno=1)",
		"2026-10-07 20:20:12 MANAGEMENT: Client disconnected",
		"2026-10-07 20:20:13 us=1 MANAGEMENT: Client disconnected",
		"Exiting due to fatal error",
	})
	want := []string{
		"Opening utun254 failed (connect(AF_SYS_CONTROL)): Operation not permitted (errno=1)",
		"MANAGEMENT: Client disconnected",
		"Exiting due to fatal error",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("readableTail = %q, want %q", got, want)
	}
}
