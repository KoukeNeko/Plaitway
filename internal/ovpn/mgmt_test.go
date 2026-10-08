package ovpn

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// shortTempDir returns a temporary directory whose path is short enough for a
// Unix socket (macOS allows 104 bytes; t.TempDir paths can exceed that).
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ov")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestParseNotification(t *testing.T) {
	tests := []struct {
		line string
		want mgmtEvent
	}{
		{">STATE:1760000000,CONNECTING,,,,,,", stateEvent{Name: "CONNECTING"}},
		{">STATE:1760000001,TCP_CONNECT,,,,,,", stateEvent{Name: "TCP_CONNECT"}},
		{">STATE:1760000002,ASSIGN_IP,,10.8.0.2,,,,", stateEvent{Name: "ASSIGN_IP", LocalIP: "10.8.0.2"}},
		{
			">STATE:1760000003,CONNECTED,SUCCESS,10.8.0.2,192.0.2.10,1194,192.168.51.185,54321,fd00::2",
			stateEvent{Name: "CONNECTED", Desc: "SUCCESS", LocalIP: "10.8.0.2", RemoteIP: "192.0.2.10", RemotePort: "1194"},
		},
		{">STATE:1760000004,RECONNECTING,ping-restart,,,,,", stateEvent{Name: "RECONNECTING", Desc: "ping-restart"}},
		{">STATE:1760000005,EXITING,exit-with-notification,,,,,", stateEvent{Name: "EXITING", Desc: "exit-with-notification"}},
		{">STATE:garbage", otherEvent{Line: ">STATE:garbage"}},
		{">BYTECOUNT:1234,5678", byteCountEvent{In: 1234, Out: 5678}},
		{">BYTECOUNT:12", otherEvent{Line: ">BYTECOUNT:12"}},
		{">BYTECOUNT:-1,2", otherEvent{Line: ">BYTECOUNT:-1,2"}},
		{">LOG:1760000000,I,TCP connection established with [AF_INET]192.0.2.10:1194", logEvent{Level: tunnel.LogInfo, Msg: "TCP connection established with [AF_INET]192.0.2.10:1194"}},
		{">LOG:1760000000,D,MANAGEMENT: CMD 'state on'", logEvent{Level: tunnel.LogDebug, Msg: "MANAGEMENT: CMD 'state on'"}},
		{">LOG:1760000000,W,WARNING: this, has, commas", logEvent{Level: tunnel.LogWarn, Msg: "WARNING: this, has, commas"}},
		{">LOG:1760000000,N,Options error: bad", logEvent{Level: tunnel.LogError, Msg: "Options error: bad"}},
		{">LOG:1760000000,F,Exiting due to fatal error", logEvent{Level: tunnel.LogError, Msg: "Exiting due to fatal error"}},
		{">LOG:1760000000,,Control Channel MTU parms [ mss_fix:0 ]", logEvent{Level: tunnel.LogDebug, Msg: "Control Channel MTU parms [ mss_fix:0 ]"}},
		{">LOG:1760000000,I", otherEvent{Line: ">LOG:1760000000,I"}},
		{">HOLD:Waiting for hold release:0", holdEvent{}},
		{">HOLD:Waiting for hold release:2", holdEvent{Wait: 2 * time.Second}},
		{">HOLD:Waiting for hold release:300", holdEvent{Wait: 300 * time.Second}},
		{">HOLD:Waiting for hold release", holdEvent{}},
		{">HOLD:Waiting for hold release:-3", holdEvent{}},
		{">HOLD:Waiting for hold release:soon", holdEvent{}},
		{">PASSWORD:Need 'Auth' username/password", passwordPrompt{Type: "Auth", UserPass: true}},
		{">PASSWORD:Need 'Private Key' password", passwordPrompt{Type: "Private Key"}},
		{">PASSWORD:Need 'Auth' username/password SC:1,Enter the code", passwordPrompt{Type: "Auth", UserPass: true, Challenge: true}},
		{">PASSWORD:Need 'HTTP Proxy' username/password", passwordPrompt{Type: "HTTP Proxy", UserPass: true}},
		{">PASSWORD:Verification Failed: 'Auth'", passwordFailed{Type: "Auth"}},
		{">PASSWORD:Verification Failed: 'Private Key'", passwordFailed{Type: "Private Key"}},
		{">PASSWORD:Verification Failed: 'Auth' ['CRV1:R,E:state:dXNlcg==:Enter code']", passwordFailed{Type: "Auth", Reason: "CRV1:R,E:state:dXNlcg==:Enter code"}},
		{">PASSWORD:Verification Failed: 'Auth' ['TEMP[backoff 5,advance no]:busy']", passwordFailed{Type: "Auth", Reason: "TEMP[backoff 5,advance no]:busy"}},
		{">PASSWORD:Auth-Token:SESS_ID_AT_s3cr3t", authTokenEvent{}},
		{">PASSWORD:something else", otherEvent{Line: ">PASSWORD:something else"}},
		{">FATAL:Cannot open TUN/TAP dev", fatalEvent{Msg: "Cannot open TUN/TAP dev"}},
		{">INFO:OpenVPN Management Interface Version 6 -- type 'help' for more info", infoEvent{Msg: "OpenVPN Management Interface Version 6 -- type 'help' for more info"}},
		{">INFOMSG:WEB_AUTH::https://example.com/auth", infoEvent{Msg: "WEB_AUTH::https://example.com/auth"}},
		{">ECHO:1760000000,something", otherEvent{Line: ">ECHO:1760000000,something"}},
		{">nonsense", otherEvent{Line: ">nonsense"}},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			if got := parseNotification(tt.line); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseNotification(%q) = %#v, want %#v", tt.line, got, tt.want)
			}
		})
	}
}

func TestMgmtQuote(t *testing.T) {
	tests := []struct {
		in   string
		want string
		err  bool
	}{
		{in: "alice", want: `"alice"`},
		{in: "", want: `""`},
		{in: "pass word", want: `"pass word"`},
		{in: `a"b`, want: `"a\"b"`},
		{in: `a\b`, want: `"a\\b"`},
		{in: `\"`, want: `"\\\""`},
		{in: `end\`, want: `"end\\"`},
		{in: "p#ss;word'", want: `"p#ss;word'"`},
		{in: "密碼", want: `"密碼"`},
		{in: "line\nbreak", err: true},
		{in: "carriage\rreturn", err: true},
		{in: "nul\x00byte", err: true},
		{in: "tab\there", err: true},
		{in: "del\x7f", err: true},
		{in: "injected\"\nsignal SIGTERM", err: true},
	}
	for _, tt := range tests {
		got, err := mgmtQuote(tt.in)
		if tt.err {
			if err == nil {
				t.Errorf("mgmtQuote(%q) = %q, want an error", tt.in, got)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("mgmtQuote(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}

// What mgmtQuote writes must read back as the same value through OpenVPN's own
// parameter rules, which splitFields implements.
func TestMgmtQuoteRoundTrip(t *testing.T) {
	for _, secret := range []string{"simple", "with space", `quo"te`, `back\slash`, `\\"\"`, "#hash", ";semi", "'single'", "日本語"} {
		cmd, err := credentialCommand("password", "Auth", secret)
		if err != nil {
			t.Fatal(err)
		}
		got, err := splitFields(cmd)
		if err != nil {
			t.Fatalf("%q does not parse: %v", cmd, err)
		}
		if want := []string{"password", "Auth", secret}; !reflect.DeepEqual(got, want) {
			t.Errorf("credentialCommand(%q) = %q, parses as %q", secret, cmd, got)
		}
	}
}

// realTranscript is a management session the way OpenVPN 2.7 writes it, with
// CRLF line ends, including a credential prompt and the UPDOWN dump of a
// split tunnel with pushed routes and DNS.
const realTranscript = ">INFO:OpenVPN Management Interface Version 6 -- type 'help' for more info\r\n" +
	">HOLD:Waiting for hold release:0\r\n" +
	"SUCCESS: real-time state notification set to ON\r\n" +
	"SUCCESS: bytecount interval changed\r\n" +
	"SUCCESS: real-time log notification set to ON\r\n" +
	">LOG:1760000000,D,MANAGEMENT: CMD 'hold release'\r\n" +
	"SUCCESS: hold release succeeded\r\n" +
	">PASSWORD:Need 'Auth' username/password\r\n" +
	">STATE:1760000001,CONNECTING,,,,,,\r\n" +
	">STATE:1760000001,TCP_CONNECT,,,,,,\r\n" +
	">LOG:1760000001,I,TCP connection established with [AF_INET]192.0.2.10:1194\r\n" +
	"SUCCESS: 'Auth' username entered, but not yet verified\r\n" +
	"ERROR: password too long\r\n" +
	">STATE:1760000002,GET_CONFIG,,,,,,\r\n" +
	">LOG:1760000002,I,PUSH: Received control message: 'PUSH_REPLY,route 10.20.0.0 255.255.0.0,dhcp-option DNS 10.20.0.1'\r\n" +
	">STATE:1760000002,ADD_ROUTES,,10.8.0.2,,,,\r\n" +
	">UPDOWN:UP\r\n" +
	">UPDOWN:ENV,dev=utun5\r\n" +
	">UPDOWN:ENV,ifconfig_local=10.8.0.2\r\n" +
	">UPDOWN:ENV,ifconfig_netmask=255.255.255.0\r\n" +
	">UPDOWN:ENV,foreign_option_1=dhcp-option DNS 10.20.0.1\r\n" +
	">UPDOWN:ENV,note=a=b=c\r\n" +
	">UPDOWN:ENV,END\r\n" +
	">STATE:1760000003,CONNECTED,SUCCESS,10.8.0.2,192.0.2.10,1194,192.168.51.185,54321\r\n" +
	">BYTECOUNT:1024,2048\r\n"

func collectEvents(t *testing.T, stream string) ([]mgmtEvent, *mgmtConn) {
	t.Helper()
	server, client := net.Pipe()
	go func() {
		server.Write([]byte(stream))
		server.Close()
	}()
	c := &mgmtConn{conn: client, pending: []string{"state", "bytecount", "log", "hold", "username", "password"}}
	var events []mgmtEvent
	if err := c.readLoop(func(ev mgmtEvent) bool { events = append(events, ev); return true }); err != nil {
		t.Fatalf("readLoop: %v", err)
	}
	return events, c
}

func TestReadLoopTranscript(t *testing.T) {
	events, c := collectEvents(t, realTranscript)

	want := []mgmtEvent{
		infoEvent{Msg: "OpenVPN Management Interface Version 6 -- type 'help' for more info"},
		holdEvent{},
		logEvent{Level: tunnel.LogDebug, Msg: "MANAGEMENT: CMD 'hold release'"},
		passwordPrompt{Type: "Auth", UserPass: true},
		stateEvent{Name: "CONNECTING"},
		stateEvent{Name: "TCP_CONNECT"},
		logEvent{Level: tunnel.LogInfo, Msg: "TCP connection established with [AF_INET]192.0.2.10:1194"},
		// Replies match commands in order: the five SUCCESS lines answer state,
		// bytecount, log, hold and username; the ERROR answers password.
		commandError{Cmd: "password", Msg: "password too long"},
		stateEvent{Name: "GET_CONFIG"},
		logEvent{Level: tunnel.LogInfo, Msg: "PUSH: Received control message: 'PUSH_REPLY,route 10.20.0.0 255.255.0.0,dhcp-option DNS 10.20.0.1'"},
		stateEvent{Name: "ADD_ROUTES", LocalIP: "10.8.0.2"},
		upDownEvent{Kind: "UP", Env: map[string]string{
			"dev":              "utun5",
			"ifconfig_local":   "10.8.0.2",
			"ifconfig_netmask": "255.255.255.0",
			"foreign_option_1": "dhcp-option DNS 10.20.0.1",
			"note":             "a=b=c",
		}},
		stateEvent{Name: "CONNECTED", Desc: "SUCCESS", LocalIP: "10.8.0.2", RemoteIP: "192.0.2.10", RemotePort: "1194"},
		byteCountEvent{In: 1024, Out: 2048},
	}
	if !reflect.DeepEqual(events, want) {
		for i := range events {
			if i >= len(want) || !reflect.DeepEqual(events[i], want[i]) {
				t.Errorf("event %d = %#v, want %#v", i, events[i], safeIndex(want, i))
			}
		}
		if len(events) != len(want) {
			t.Errorf("got %d events, want %d", len(events), len(want))
		}
	}
	if len(c.pending) != 0 {
		t.Errorf("pending commands = %v, want none", c.pending)
	}
}

func safeIndex(events []mgmtEvent, i int) mgmtEvent {
	if i < len(events) {
		return events[i]
	}
	return nil
}

func TestReadLoopErrors(t *testing.T) {
	long := ">LOG:1,I," + strings.Repeat("x", maxMgmtLineBytes) + "\n"
	tests := []struct {
		name   string
		stream string
	}{
		{"line too long", long},
		{"UPDOWN env without a start", ">UPDOWN:ENV,dev=utun5\n"},
		{"UPDOWN end without a start", ">UPDOWN:ENV,END\n"},
		{"UPDOWN unknown word", ">UPDOWN:SIDEWAYS\n"},
		{"UPDOWN environment too large", hugeUpDown()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, client := net.Pipe()
			go func() {
				server.Write([]byte(tt.stream))
				server.Close()
			}()
			c := &mgmtConn{conn: client}
			if err := c.readLoop(func(mgmtEvent) bool { return true }); err == nil {
				t.Fatal("readLoop returned nil, want an error")
			}
		})
	}
}

func hugeUpDown() string {
	var b strings.Builder
	b.WriteString(">UPDOWN:UP\n")
	for i := 0; i < maxUpDownEnvLines+2; i++ {
		fmt.Fprintf(&b, ">UPDOWN:ENV,k%d=1\n", i)
	}
	return b.String()
}

func TestReadLoopStopsWhenEmitSaysSo(t *testing.T) {
	server, client := net.Pipe()
	go func() {
		server.Write([]byte(">HOLD:x\n>HOLD:y\n"))
		server.Close()
	}()
	c := &mgmtConn{conn: client}
	n := 0
	if err := c.readLoop(func(mgmtEvent) bool { n++; return false }); err != nil || n != 1 {
		t.Fatalf("readLoop = %v after %d events, want nil after 1", err, n)
	}
}

// loopbackPair connects two ends over TCP on the loopback interface. Unlike
// net.Pipe it buffers, as a socket to openvpn does: Send holds the connection
// while it writes, and the reader needs the same lock to match a reply.
func loopbackPair(t *testing.T) (server, client net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- conn
	}()
	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	server, ok := <-accepted
	if !ok {
		t.Fatal("the loopback listener accepted nothing")
	}
	t.Cleanup(func() { server.Close() })
	return server, client
}

func TestSendAndReplyAttribution(t *testing.T) {
	server, client := loopbackPair(t)
	received := make(chan string, 8)
	go func() {
		defer server.Close()
		sc := bufio.NewScanner(server)
		for sc.Scan() {
			received <- sc.Text()
			switch {
			case strings.HasPrefix(sc.Text(), "username"):
				server.Write([]byte("ERROR: bad username\r\n"))
			default:
				server.Write([]byte("SUCCESS: ok\r\n"))
			}
		}
	}()

	c := &mgmtConn{conn: client}
	defer c.Close()

	var mu sync.Mutex
	var events []mgmtEvent
	done := make(chan error, 1)
	go func() {
		done <- c.readLoop(func(ev mgmtEvent) bool { mu.Lock(); events = append(events, ev); mu.Unlock(); return true })
	}()

	usernameCmd, _ := credentialCommand("username", "Auth", "al\"ice")
	for _, cmd := range [][2]string{{"state", "state on"}, {"username", usernameCmd}, {"hold", "hold release"}} {
		if err := c.Send(cmd[0], cmd[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"state on", usernameCmd, "hold release"} {
		select {
		case got := <-received:
			if got != want {
				t.Errorf("server received %q, want %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("server never received %q", want)
		}
	}

	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		n := len(events)
		mu.Unlock()
		if n == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("no ERROR event arrived")
		case <-time.After(10 * time.Millisecond):
		}
	}
	c.Close()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if want := (commandError{Cmd: "username", Msg: "bad username"}); !reflect.DeepEqual(events[0], want) {
		t.Errorf("event = %#v, want %#v", events[0], want)
	}
}

func TestSendRefusesLineBreaks(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	c := &mgmtConn{conn: client}
	for _, line := range []string{"state on\nsignal SIGTERM", "state on\r"} {
		if err := c.Send("state", line); err == nil {
			t.Errorf("Send(%q) succeeded, want an error", line)
		}
	}
	if len(c.pending) != 0 {
		t.Errorf("a refused command was queued: %v", c.pending)
	}
}
