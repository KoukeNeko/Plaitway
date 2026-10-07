package ovpn

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// The management interface is a line protocol: openvpn sends asynchronous
// notifications that start with ">", and one reply line ("SUCCESS: ..." or
// "ERROR: ...") per command. Only commands with single-line replies are used
// here, so replies are matched to commands in order.

const (
	mgmtDialInterval  = 20 * time.Millisecond
	mgmtWriteTimeout  = 5 * time.Second
	maxMgmtLineBytes  = 1 << 20
	maxUpDownEnvLines = 8192
)

// mgmtEvent is a parsed notification or reply from openvpn.
type mgmtEvent interface{ isMgmtEvent() }

type (
	// stateEvent: >STATE:time,name,description,tun-ip,remote-ip,remote-port,...
	stateEvent struct {
		Name, Desc                    string
		LocalIP, RemoteIP, RemotePort string
	}
	// byteCountEvent: >BYTECOUNT:in,out
	byteCountEvent struct{ In, Out uint64 }
	// logEvent: >LOG:time,flags,message
	logEvent struct {
		Level tunnel.LogLevel
		Msg   string
	}
	// holdEvent: >HOLD:Waiting for hold release:N. N is the pause in seconds
	// openvpn would make before it connects (again) without a management
	// client; the client has to wait that long, or openvpn retries as fast as
	// it can fail.
	holdEvent struct{ Wait time.Duration }
	// passwordPrompt: >PASSWORD:Need 'Auth' username/password
	passwordPrompt struct {
		Type     string // "Auth", "Private Key", "HTTP Proxy", ...
		UserPass bool   // username and password, not just a password
		// Challenge is set for a static challenge, which needs a response
		// field this engine has no way to ask for.
		Challenge bool
	}
	// passwordFailed: >PASSWORD:Verification Failed: 'Auth' ['reason']
	passwordFailed struct{ Type, Reason string }
	// authTokenEvent: >PASSWORD:Auth-Token:token. The token is a credential
	// and is dropped here: it must not reach a log.
	authTokenEvent struct{}
	// upDownEvent: >UPDOWN:UP followed by >UPDOWN:ENV,name=value lines and a
	// final >UPDOWN:ENV,END.
	upDownEvent struct {
		Kind string // "UP" or "DOWN"
		Env  map[string]string
	}
	// fatalEvent: >FATAL:message
	fatalEvent struct{ Msg string }
	// infoEvent: >INFO: or >INFOMSG: lines
	infoEvent struct{ Msg string }
	// commandError: "ERROR: ..." in reply to command Cmd.
	commandError struct{ Cmd, Msg string }
	// otherEvent is any line the engine does not act on.
	otherEvent struct{ Line string }
)

func (stateEvent) isMgmtEvent()     {}
func (byteCountEvent) isMgmtEvent() {}
func (logEvent) isMgmtEvent()       {}
func (holdEvent) isMgmtEvent()      {}
func (passwordPrompt) isMgmtEvent() {}
func (passwordFailed) isMgmtEvent() {}
func (authTokenEvent) isMgmtEvent() {}
func (upDownEvent) isMgmtEvent()    {}
func (fatalEvent) isMgmtEvent()     {}
func (infoEvent) isMgmtEvent()      {}
func (commandError) isMgmtEvent()   {}
func (otherEvent) isMgmtEvent()     {}

var (
	needPrompt   = regexp.MustCompile(`^Need '([^']*)' (username/password|password)( SC:.*)?$`)
	failedPrompt = regexp.MustCompile(`^Verification Failed: '([^']*)'(?: \['(.*)'\])?`)
)

// parseNotification parses one line that starts with ">". Everything but
// UPDOWN is a single line; UPDOWN is assembled by the reader.
func parseNotification(line string) mgmtEvent {
	kind, payload, ok := strings.Cut(line[1:], ":")
	if !ok {
		return otherEvent{Line: line}
	}
	switch kind {
	case "STATE":
		f := strings.SplitN(payload, ",", 9)
		if len(f) < 2 {
			return otherEvent{Line: line}
		}
		ev := stateEvent{Name: f[1]}
		if len(f) > 2 {
			ev.Desc = f[2]
		}
		if len(f) > 3 {
			ev.LocalIP = f[3]
		}
		if len(f) > 4 {
			ev.RemoteIP = f[4]
		}
		if len(f) > 5 {
			ev.RemotePort = f[5]
		}
		return ev
	case "BYTECOUNT":
		in, out, ok := strings.Cut(payload, ",")
		inN, err1 := strconv.ParseUint(in, 10, 64)
		outN, err2 := strconv.ParseUint(out, 10, 64)
		if !ok || err1 != nil || err2 != nil {
			return otherEvent{Line: line}
		}
		return byteCountEvent{In: inN, Out: outN}
	case "LOG":
		f := strings.SplitN(payload, ",", 3)
		if len(f) < 3 {
			return otherEvent{Line: line}
		}
		return logEvent{Level: logLevel(f[1]), Msg: f[2]}
	case "HOLD":
		return holdEvent{Wait: holdWait(payload)}
	case "PASSWORD":
		if m := needPrompt.FindStringSubmatch(payload); m != nil {
			return passwordPrompt{Type: m[1], UserPass: m[2] == "username/password", Challenge: m[3] != ""}
		}
		if m := failedPrompt.FindStringSubmatch(payload); m != nil {
			return passwordFailed{Type: m[1], Reason: m[2]}
		}
		if strings.HasPrefix(payload, "Auth-Token:") {
			return authTokenEvent{}
		}
	case "FATAL":
		return fatalEvent{Msg: payload}
	case "INFO", "INFOMSG":
		return infoEvent{Msg: payload}
	}
	return otherEvent{Line: line}
}

// holdWait reads the pause of "Waiting for hold release:N". openvpn versions
// that do not announce one are treated as announcing none.
func holdWait(payload string) time.Duration {
	_, seconds, ok := strings.Cut(payload, "release:")
	n, err := strconv.ParseUint(seconds, 10, 32)
	if !ok || err != nil {
		return 0
	}
	return time.Duration(n) * time.Second
}

// logLevel maps openvpn's log flags (I info, W warning, N non-fatal error, F
// fatal, D debug) to a level; the most severe flag wins. openvpn leaves the
// flags empty for the verbose messages it prints above verb 1.
func logLevel(flags string) tunnel.LogLevel {
	switch {
	case strings.ContainsAny(flags, "FN"):
		return tunnel.LogError
	case strings.Contains(flags, "W"):
		return tunnel.LogWarn
	case strings.Contains(flags, "I"):
		return tunnel.LogInfo
	}
	return tunnel.LogDebug
}

// mgmtQuote quotes one command parameter the way OpenVPN's parser reads it:
// in double quotes, with backslash and double quote escaped. A control
// character would end the command line and start another one, so it is
// refused rather than escaped.
func mgmtQuote(s string) (string, error) {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f:
			return "", errors.New("contains a control character")
		case r == '\\' || r == '"':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String(), nil
}

// credentialCommand builds `username "Auth" "value"` or `password ...`.
func credentialCommand(verb, typ, value string) (string, error) {
	t, err := mgmtQuote(typ)
	if err != nil {
		return "", err
	}
	v, err := mgmtQuote(value)
	if err != nil {
		return "", err
	}
	return verb + " " + t + " " + v, nil
}

// errMgmtWrite marks a command that could not be written: openvpn closed the
// connection, which means it is exiting.
var errMgmtWrite = errors.New("management connection closed")

// mgmtConn is the client side of one management connection.
type mgmtConn struct {
	conn net.Conn

	mu      sync.Mutex
	pending []string // names of commands still waiting for their reply line
}

// dialMgmt connects to the management socket openvpn creates, retrying until
// it exists. abort closes when the child has exited, so a child that died
// before it listened ends the wait.
func dialMgmt(ctx context.Context, socket string, abort <-chan struct{}) (*mgmtConn, error) {
	var d net.Dialer
	for {
		conn, err := d.DialContext(ctx, "unix", socket)
		if err == nil {
			return &mgmtConn{conn: conn}, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("connect to management socket: %w", ctx.Err())
		case <-abort:
			return nil, errors.New("openvpn exited before its management socket was ready")
		case <-time.After(mgmtDialInterval):
		}
	}
}

// Send writes one command. name labels it for error attribution and must not
// contain anything secret; line is the complete command.
func (c *mgmtConn) Send(name, line string) error {
	if strings.ContainsAny(line, "\r\n") {
		return errors.New("management command contains a line break")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.conn.SetWriteDeadline(time.Now().Add(mgmtWriteTimeout)); err != nil {
		return fmt.Errorf("send %s: %w: %v", name, errMgmtWrite, err)
	}
	c.pending = append(c.pending, name)
	if _, err := c.conn.Write([]byte(line + "\n")); err != nil {
		return fmt.Errorf("send %s: %w: %v", name, errMgmtWrite, err)
	}
	return nil
}

func (c *mgmtConn) popPending() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) == 0 {
		return ""
	}
	name := c.pending[0]
	c.pending = c.pending[1:]
	return name
}

func (c *mgmtConn) Close() error { return c.conn.Close() }

// readLoop reads until the connection ends, passing every event to emit, which
// returns false to stop reading. A clean end of the stream returns nil.
func (c *mgmtConn) readLoop(emit func(mgmtEvent) bool) error {
	sc := bufio.NewScanner(c.conn)
	sc.Buffer(make([]byte, 0, 64<<10), maxMgmtLineBytes)
	var up *upDownEvent
	for sc.Scan() {
		line := sc.Text()
		var ev mgmtEvent
		switch {
		case strings.HasPrefix(line, ">UPDOWN:"):
			done, err := collectUpDown(&up, strings.TrimPrefix(line, ">UPDOWN:"))
			if err != nil {
				return err
			}
			if !done {
				continue
			}
			ev, up = *up, nil
		case strings.HasPrefix(line, ">"):
			ev = parseNotification(line)
		case strings.HasPrefix(line, "SUCCESS:"):
			c.popPending()
			continue
		case strings.HasPrefix(line, "ERROR:"):
			ev = commandError{Cmd: c.popPending(), Msg: strings.TrimSpace(strings.TrimPrefix(line, "ERROR:"))}
		default:
			ev = otherEvent{Line: line}
		}
		if !emit(ev) {
			return nil
		}
	}
	return sc.Err()
}

// collectUpDown folds the lines of one UPDOWN dump into acc. It reports true
// when the dump is complete.
func collectUpDown(acc **upDownEvent, payload string) (bool, error) {
	switch payload {
	case "UP", "DOWN":
		*acc = &upDownEvent{Kind: payload, Env: map[string]string{}}
		return false, nil
	}
	env, ok := strings.CutPrefix(payload, "ENV,")
	if !ok || *acc == nil {
		return false, fmt.Errorf("unexpected UPDOWN line %q", truncate(payload, 80))
	}
	if env == "END" {
		return true, nil
	}
	if len((*acc).Env) >= maxUpDownEnvLines {
		return false, errors.New("UPDOWN environment is too large")
	}
	if name, value, ok := strings.Cut(env, "="); ok {
		(*acc).Env[name] = value
	}
	return false, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
