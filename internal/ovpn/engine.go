package ovpn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

const (
	// stopGrace is how long openvpn gets to exit after SIGTERM before it is killed.
	stopGrace = 5 * time.Second
	// dialTimeout bounds the wait for openvpn to create its management socket.
	dialTimeout = 10 * time.Second
	// lookupTimeout bounds resolving the remote host names at start;
	// rebindLookupTimeout does so after a network change, where a slow answer
	// delays the restart of the connection.
	lookupTimeout       = 15 * time.Second
	rebindLookupTimeout = 5 * time.Second
	// upReportGrace is how long a CONNECTED tunnel may go without openvpn
	// reporting its interface and routes before the engine gives up.
	upReportGrace = 5 * time.Second
	// connectStuckGrace is how long a tunnel may be connecting before its
	// status says why it is not up yet.
	connectStuckGrace = 20 * time.Second
	// A failure that may pass, such as a server name that does not resolve yet,
	// is tried again after initialRetryWait, then after twice as long each time
	// up to maxRetryWait. openvpn paces its own attempts, and asks the engine to
	// wait out that pause (holdEvent); the engine waits at most maxRetryWait of
	// it, so that the tunnel comes back soon after the network does.
	initialRetryWait = 2 * time.Second
	maxRetryWait     = 30 * time.Second
	// outputTailLines is how many of the child's last output lines go into a
	// failure report.
	outputTailLines = 5
	outputRingLines = 64
	maxOutputLine   = 64 << 10
	redacted        = "[redacted]"
)

// credential is what the user supplied: user is empty for a key passphrase.
type credential struct {
	user, secret string
	set          bool
}

// session is the part of the engine's state that only the run goroutine
// touches.
type session struct {
	endpoints []netip.Addr
	up        *upInfo         // what openvpn reported for its tunnel; nil while there is none
	connected bool            // the last >STATE was CONNECTED
	upKnown   bool            // the interface and routes were reported and announced
	everUp    bool            // the tunnel was Up at least once: later non-Up states are reconnects
	prompt    *passwordPrompt // credential prompt not answered yet
	needCreds tunnel.CredentialKind
	credErr   string // why the credentials are asked for again, while they are
	upSince   time.Time
	lastFatal string

	startReleased bool // the release of the start-up hold was sent and its notification has not come

	holding  bool          // openvpn waits for a hold release between two connection attempts
	holdWait time.Duration // how long it asked the engine to wait before releasing

	tokenHeld bool // openvpn holds a session token the server pushed

	pushedDNS  dnsOptions // the dns options of the last PUSH_REPLY
	pushedDHCP []string   // the dhcp-options of the last PUSH_REPLY, where they are read from it
	pushMore   bool       // that reply is continued in the next message

	vpnState   string      // the name of the last >STATE
	cause      string      // the latest concrete reason the connection is not coming up
	causeConn  bool        // cause is a failure to reach the server, and ends when openvpn reaches it
	stuck      bool        // the connection has taken too long: cause is shown
	stuckArmed bool        // stuckTimer is running
	stuckTimer *time.Timer // fires connectStuckGrace after the engine started connecting
}

// child is the openvpn process.
type child struct {
	cmd   *exec.Cmd
	guard processGuard
	done  chan struct{} // closed after the process exited and its output was read
	err   error         // the result of Wait; valid once done is closed
}

// kill ends the process and everything it started, at once.
func (c *child) kill() { _ = c.guard.kill(c.cmd) }

type engine struct {
	cfg  Config
	log  *slog.Logger
	info func() binaryInfo
	// trust confirms that the binary is the one the daemon trusts and keeps it
	// so until release is called, around the start of the process.
	trust func() (release func(), err error)
	spec  tunnel.Spec
	deps  tunnel.Deps
	prof  *profile

	dir, configPath string
	channel         mgmtChannel
	device          deviceProvider

	// Tests change these before Start.
	stopGrace     time.Duration
	dialTimeout   time.Duration
	upReportGrace time.Duration
	stuckGrace    time.Duration
	retryWait     time.Duration
	lookup        func(ctx context.Context, host string) ([]netip.Addr, error)

	runCtx   context.Context
	cancel   context.CancelFunc
	stopReq  chan struct{}
	stopOnce sync.Once
	done     chan struct{} // closed when the run goroutine has finished everything
	started  atomic.Bool
	child    atomic.Pointer[child]
	mgmtLive atomic.Bool // the management connection is up; its >LOG lines are the log
	wake     chan struct{}
	ring     lineRing
	logMu    sync.Mutex

	mu              sync.Mutex // guards the fields below
	status          tunnel.Status
	statusCh        chan tunnel.Status
	closed          bool
	userPass        credential
	keyPass         credential
	secrets         []string // every password or passphrase ever supplied, for redaction
	rebindRequested bool

	s session
}

func (b *backend) newEngine(spec tunnel.Spec, deps tunnel.Deps) (tunnel.Engine, error) {
	switch {
	case spec.Owner == "":
		return nil, errors.New("openvpn: profile has no owner id")
	case deps.Network == nil:
		return nil, errors.New("openvpn: Deps.Network is required")
	case b.cfg.RunDir == "":
		return nil, errors.New("openvpn: Config.RunDir is required")
	}
	prof, err := parseProfile(spec.Content)
	if err != nil {
		return nil, fmt.Errorf("openvpn: stored profile is not acceptable: %w", err)
	}
	dir, config := workspaceFiles(b.cfg.RunDir, string(spec.Owner))
	channel, err := newMgmtChannel(dir)
	if err != nil {
		return nil, fmt.Errorf("openvpn: %w", err)
	}
	if deps.Log == nil {
		deps.Log = func(tunnel.LogLevel, string) {}
	}
	log := b.cfg.Log.With("owner", string(spec.Owner))
	ctx, cancel := context.WithCancel(context.Background())
	e := &engine{
		cfg:           b.cfg,
		log:           log,
		info:          b.info,
		trust:         b.trustBinary,
		spec:          spec,
		deps:          deps,
		prof:          prof,
		dir:           dir,
		configPath:    config,
		channel:       channel,
		device:        newDeviceProvider(b.cfg, spec.Owner, prof, log),
		stopGrace:     stopGrace,
		dialTimeout:   dialTimeout,
		upReportGrace: upReportGrace,
		stuckGrace:    connectStuckGrace,
		retryWait:     initialRetryWait,
		lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
		runCtx:   ctx,
		cancel:   cancel,
		stopReq:  make(chan struct{}),
		done:     make(chan struct{}),
		wake:     make(chan struct{}, 1),
		ring:     lineRing{max: outputRingLines},
		statusCh: make(chan tunnel.Status, 1),
	}
	e.status.Warnings = warningTexts(prof.Warnings)
	return e, nil
}

func warningTexts(ws []tunnel.Warning) []string {
	var out []string
	for _, w := range ws {
		out = append(out, fmt.Sprintf("line %d: %s: %s", w.Line, w.Directive, w.Message))
	}
	return out
}

// Start returns once openvpn is being launched; the engine reports progress
// through Status. It checks what it can without waiting: the binary, LZO
// support, the device the profile asks for and the workspace. ctx only bounds
// this call; the engine runs until Stop.
func (e *engine) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	bin := e.info()
	switch {
	case !bin.available:
		return fmt.Errorf("openvpn is not available: %s", bin.detail)
	case e.prof.needsLZO && !bin.lzo:
		return errors.New("this profile uses LZO compression, which the openvpn binary was built without")
	case refusesTap && e.prof.usesTap():
		return errors.New(tapRefusal)
	}
	if !e.started.CompareAndSwap(false, true) {
		return errors.New("openvpn: engine was already started or stopped")
	}
	if err := e.prepareWorkspace(); err != nil {
		e.removeWorkspace()
		e.closeStatus()
		close(e.done)
		return err
	}
	go e.run(bin)
	return nil
}

// Status delivers snapshots. A reader that falls behind misses intermediate
// ones and always finds the latest.
func (e *engine) Status() <-chan tunnel.Status { return e.statusCh }

// Stop ends openvpn, removes its files, withdraws the owner and closes Status.
// openvpn gets stopGrace to exit after SIGTERM and is killed after that, or at
// once when ctx ends first.
func (e *engine) Stop(ctx context.Context) error {
	e.stopOnce.Do(func() {
		close(e.stopReq)
		e.cancel()
		e.update(func(s *tunnel.Status) {
			if s.State != tunnel.StateFailed && s.State != tunnel.StateDisconnected {
				s.State = tunnel.StateDisconnecting
			}
		})
	})
	if e.started.CompareAndSwap(false, true) {
		e.closeStatus() // never started: there is nothing to stop
		close(e.done)
		return nil
	}
	select {
	case <-e.done:
		return nil
	case <-ctx.Done():
		if c := e.child.Load(); c != nil {
			c.kill()
		}
		<-e.done
		return ctx.Err()
	}
}

// ProvideCredentials stores credentials in memory and answers the pending
// prompt, if any. They are kept to answer the prompts that follow a reconnect
// and dropped when openvpn rejects them.
func (e *engine) ProvideCredentials(kind tunnel.CredentialKind, username, password string) error {
	switch kind {
	case tunnel.CredentialUserPassword:
		if username == "" {
			return errors.New("username is empty")
		}
		if _, err := mgmtQuote(username); err != nil {
			return fmt.Errorf("username %w", err)
		}
	case tunnel.CredentialKeyPassphrase:
	default:
		return fmt.Errorf("unsupported credential kind %d", kind)
	}
	if _, err := mgmtQuote(password); err != nil {
		return fmt.Errorf("password %w", err)
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return errors.New("engine has stopped")
	}
	if kind == tunnel.CredentialUserPassword {
		e.userPass = credential{user: username, secret: password, set: true}
	} else {
		e.keyPass = credential{secret: password, set: true}
	}
	if password != "" && !slices.Contains(e.secrets, password) {
		e.secrets = append(e.secrets, password)
	}
	e.mu.Unlock()
	e.signal()
	return nil
}

// Rebind sends SIGUSR1 to openvpn: it restarts its connection to the server
// and keeps the tunnel interface. It never blocks; the Reconciler calls it.
func (e *engine) Rebind() {
	e.mu.Lock()
	e.rebindRequested = true
	e.mu.Unlock()
	e.signal()
}

func (e *engine) signal() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *engine) stopRequested() bool {
	select {
	case <-e.stopReq:
		return true
	default:
		return false
	}
}

// --- status ---

func (e *engine) update(fn func(*tunnel.Status)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	fn(&e.status)
	e.publishLocked()
}

func (e *engine) publishLocked() {
	if e.closed {
		return
	}
	snap := e.status
	snap.Addresses = slices.Clone(snap.Addresses)
	snap.Warnings = slices.Clone(snap.Warnings)
	select {
	case e.statusCh <- snap:
		return
	default:
	}
	select {
	case <-e.statusCh: // drop the snapshot the reader did not take
	default:
	}
	select {
	case e.statusCh <- snap:
	default:
	}
}

func (e *engine) closeStatus() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.closed {
		e.closed = true
		close(e.statusCh)
	}
}

// refreshState derives the state from what has happened so far and publishes
// it in one snapshot.
func (e *engine) refreshState() {
	s := &e.s
	state := tunnel.StateConnecting
	switch {
	case e.stopRequested():
		state = tunnel.StateDisconnecting
	case s.needCreds != tunnel.CredentialNone:
		state = tunnel.StateAwaitingCredentials
	case s.connected && s.upKnown:
		state = tunnel.StateUp
		s.everUp = true
		s.credErr = ""
	case s.everUp:
		state = tunnel.StateReconnecting
	}
	s.trackStuck(state == tunnel.StateConnecting || state == tunnel.StateReconnecting, e.stuckGrace)
	e.update(func(st *tunnel.Status) {
		st.State = state
		st.NeedsCredentials = tunnel.CredentialNone
		switch state {
		case tunnel.StateAwaitingCredentials:
			st.NeedsCredentials = s.needCreds
			st.Err = s.credErr // the app takes an error here for a rejected password
		case tunnel.StateUp:
			st.Err = ""
			if st.Since.IsZero() {
				st.Since = s.upSince
			}
		case tunnel.StateConnecting, tunnel.StateReconnecting:
			st.Err = s.stuckReason()
		}
	})
}

// trackStuck runs the timer that ends the silence of a connection that does not
// come up: while connecting it counts down, and when it fires the status gets
// the cause. Any other state, an Up tunnel or a wait for credentials, starts
// over.
func (s *session) trackStuck(connecting bool, grace time.Duration) {
	switch {
	case !connecting:
		if s.stuckTimer != nil {
			s.stuckTimer.Stop()
		}
		s.stuckArmed, s.stuck, s.cause, s.causeConn = false, false, "", false
	case !s.stuckArmed && !s.stuck:
		if s.stuckTimer == nil {
			s.stuckTimer = time.NewTimer(grace)
		} else {
			s.stuckTimer.Reset(grace)
		}
		s.stuckArmed = true
	}
}

// stuckC is the channel of stuckTimer; it never delivers while there is none.
func (s *session) stuckC() <-chan time.Time {
	if s.stuckTimer == nil {
		return nil
	}
	return s.stuckTimer.C
}

// stuckReason is what the status says about a connection that has taken too
// long: the latest concrete failure, or that the server does not answer.
func (s *session) stuckReason() string {
	switch {
	case !s.stuck:
		return ""
	case s.cause != "":
		return s.cause
	case s.vpnState == "WAIT" || s.vpnState == "TCP_CONNECT":
		return "no response from the server"
	}
	return ""
}

// onStuck handles the connection taking too long.
func (e *engine) onStuck() {
	e.s.stuckArmed, e.s.stuck = false, true
	e.refreshState()
}

// setCause records why the connection is not coming up; it is shown once the
// connection has taken too long. reach says the cause is a failure to reach the
// server.
func (e *engine) setCause(cause string, reach bool) {
	if cause == e.s.cause && reach == e.s.causeConn {
		return
	}
	e.s.cause, e.s.causeConn = cause, reach
	if e.s.stuck {
		e.refreshState()
	}
}

// --- logging ---

func (e *engine) logLine(level tunnel.LogLevel, text string) {
	text = e.scrub(text)
	e.logMu.Lock()
	defer e.logMu.Unlock()
	e.deps.Log(level, text)
}

// scrub removes credentials from text before it is logged or reported. openvpn
// may echo management commands in its log, and a server or a bug could echo a
// password in other ways.
func (e *engine) scrub(text string) string {
	if strings.Contains(text, "MANAGEMENT: CMD") && (strings.Contains(text, "'password") || strings.Contains(text, "'username")) {
		return "MANAGEMENT: credential command " + redacted
	}
	e.mu.Lock()
	secrets := slices.Clone(e.secrets)
	e.mu.Unlock()
	for _, secret := range secrets {
		text = strings.ReplaceAll(text, secret, redacted)
	}
	return text
}

// onOutput takes a line the child wrote to stdout or stderr. They all go into
// the ring for failure reports. They are logged only while the management
// connection is not up: once it is, the same lines arrive as >LOG with their
// level.
func (e *engine) onOutput(level tunnel.LogLevel, line string) {
	e.channel.output(line)
	line = e.scrub(line)
	e.ring.add(line)
	if !e.mgmtLive.Load() {
		e.logLine(level, line)
	}
}

// outputLevel guesses the severity of a line the child printed outside the
// management interface, where no level is attached. openvpn writes its
// start-up option dump there, which is debug output, and its option errors,
// which are not.
func outputLevel(line string) tunnel.LogLevel {
	switch {
	case strings.Contains(line, "Options error") || strings.Contains(line, "ERROR:") ||
		strings.Contains(line, "fatal error") || strings.Contains(line, "Cannot "):
		return tunnel.LogError
	case strings.Contains(line, "WARNING:") || strings.Contains(line, "DEPRECATED"):
		return tunnel.LogWarn
	}
	return tunnel.LogDebug
}

// lineRing keeps the last lines of the child's output.
type lineRing struct {
	mu    sync.Mutex
	max   int
	lines []string
}

func (r *lineRing) add(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.lines) == r.max {
		r.lines = r.lines[1:]
	}
	r.lines = append(r.lines, line)
}

func (r *lineRing) tail(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.lines[max(0, len(r.lines)-n):])
}

// lineSink turns the child's output stream into lines.
type lineSink struct {
	emit func(tunnel.LogLevel, string)
	buf  []byte
}

func (s *lineSink) Write(p []byte) (int, error) {
	s.buf = append(s.buf, p...)
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			break
		}
		s.line(s.buf[:i])
		s.buf = s.buf[i+1:]
	}
	if len(s.buf) > maxOutputLine {
		s.line(s.buf)
		s.buf = nil
	}
	return len(p), nil
}

func (s *lineSink) flush() {
	s.line(s.buf)
	s.buf = nil
}

func (s *lineSink) line(b []byte) {
	if text := strings.TrimSpace(string(b)); text != "" {
		s.emit(outputLevel(text), text)
	}
}

// --- run ---

func (e *engine) run(bin binaryInfo) {
	defer close(e.done)
	failure := e.supervise(bin)
	e.finish(failure)
}

// finish reports the outcome, removes the workspace, the owner's routes and the
// interface, and closes the Status channel. failure is nil after a requested
// Stop. The interface goes after the routes: the Reconciler takes the routes
// that lead to it away first.
func (e *engine) finish(failure error) {
	if failure != nil && !e.stopRequested() {
		reason := e.scrub(failure.Error()) // before update: both take e.mu
		e.log.Error("openvpn failed", "err", reason)
		e.update(func(s *tunnel.Status) {
			s.State = tunnel.StateFailed
			s.Err = reason
			s.NeedsCredentials = tunnel.CredentialNone
		})
	}
	e.cancel()
	e.removeWorkspace()
	e.deps.Network.Withdraw(e.spec.Owner)
	e.device.release()
	if !e.statusIsFailed() {
		e.update(func(s *tunnel.Status) {
			s.State = tunnel.StateDisconnected
			s.NeedsCredentials = tunnel.CredentialNone
			s.Iface = ""
		})
	}
	e.closeStatus()
}

func (e *engine) statusIsFailed() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status.State == tunnel.StateFailed
}

func (e *engine) removeWorkspace() {
	if err := os.RemoveAll(e.dir); err != nil {
		e.log.Warn("remove workspace", "dir", e.dir, "err", err)
	}
}

func (e *engine) supervise(bin binaryInfo) error {
	e.refreshState()

	endpoints, ok := e.awaitEndpoints()
	if !ok {
		return nil
	}
	e.s.endpoints = endpoints
	if err := e.announce(); err != nil {
		return err
	}
	if e.stopRequested() {
		return nil
	}

	if err := e.device.acquire(e.runCtx); err != nil {
		if e.stopRequested() {
			return nil
		}
		return fmt.Errorf("prepare the tunnel interface: %w", err)
	}
	if e.stopRequested() {
		return nil
	}

	c, err := e.spawn(bin)
	if err != nil {
		return err
	}
	return e.monitor(c)
}

// awaitEndpoints resolves the server names, and tries again for as long as they
// do not resolve: at boot the network is often not up yet, and a name that does
// not resolve now says nothing about later. A network change (Rebind) ends a
// wait at once. It returns false when the engine was stopped.
func (e *engine) awaitEndpoints() ([]netip.Addr, bool) {
	wait := e.retryWait
	for {
		endpoints, err := e.resolveEndpoints(lookupTimeout)
		if e.stopRequested() {
			return nil, false
		}
		if err == nil {
			e.setCause("", false)
			return endpoints, true
		}
		e.setCause(err.Error(), true)
		e.logLine(tunnel.LogWarn, fmt.Sprintf("no server address yet; trying again in %s", wait))
		for retry := time.After(wait); retry != nil; {
			select {
			case <-retry:
				retry = nil
			case <-e.wake:
				e.takeRebind()
				retry = nil
			case <-e.s.stuckC():
				e.onStuck()
			case <-e.stopReq:
				return nil, false
			}
		}
		wait = min(wait*2, maxRetryWait)
	}
}

// resolveEndpoints looks up every remote and proxy host name. A name that does
// not resolve is skipped as long as another one does.
func (e *engine) resolveEndpoints(timeout time.Duration) ([]netip.Addr, error) {
	var hosts []string
	for _, ep := range e.prof.Summary.Endpoints {
		hosts = appendUnique(hosts, ep.Host)
	}
	for _, h := range e.prof.proxyHosts {
		hosts = appendUnique(hosts, h)
	}

	ctx, cancel := context.WithTimeout(e.runCtx, timeout)
	defer cancel()
	var (
		addrs    []netip.Addr
		firstErr error
	)
	for _, host := range hosts {
		if addr, err := netip.ParseAddr(host); err == nil {
			addrs = appendUnique(addrs, addr.Unmap())
			continue
		}
		found, err := e.lookup(ctx, host)
		if err != nil {
			e.logLine(tunnel.LogWarn, fmt.Sprintf("cannot resolve %s: %v", host, err))
			if firstErr == nil {
				firstErr = fmt.Errorf("cannot resolve %s: %w", host, err)
			}
			continue
		}
		for _, a := range found {
			a = a.Unmap()
			if !a.IsUnspecified() && !a.IsMulticast() {
				addrs = appendUnique(addrs, a)
			}
		}
	}
	if len(addrs) == 0 {
		if firstErr == nil {
			firstErr = errors.New("no server address")
		}
		return nil, firstErr
	}
	return addrs, nil
}

func (e *engine) spawn(bin binaryInfo) (*child, error) {
	release, err := e.trust()
	if err != nil {
		return nil, fmt.Errorf("openvpn is not trusted: %w", err)
	}
	defer release()

	cmd := exec.Command(e.cfg.Binary, buildArgs(bin, e.configPath, e.channel.options(), e.device.options())...)
	cmd.Env = childEnv()
	prepareCommand(cmd, e.dir)
	stdout := &lineSink{emit: e.onOutput}
	stderr := &lineSink{emit: e.onOutput}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start openvpn: %w", err)
	}
	guard, err := guardProcess(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("start openvpn: %w", err)
	}
	c := &child{cmd: cmd, guard: guard, done: make(chan struct{})}
	e.child.Store(c)
	e.log.Info("openvpn started", "pid", cmd.Process.Pid)
	go func() {
		c.err = cmd.Wait()
		stdout.flush()
		stderr.flush()
		c.guard.close()
		close(c.done)
	}()
	return c, nil
}

// terminate stops the child: SIGTERM through the management interface (or
// directly when there is none), then a kill after the grace period. Where the
// OS has no direct way to ask, and the management interface is out of reach,
// the kill comes at once.
func (e *engine) terminate(c *child, conn *mgmtConn) {
	if conn == nil || conn.Send("signal", "signal SIGTERM") != nil {
		if errors.Is(signalExit(c.cmd), errNoExitSignal) {
			e.log.Warn("openvpn cannot be asked to exit; killing it")
			c.kill()
			<-c.done
			return
		}
	}
	select {
	case <-c.done:
		return
	case <-time.After(e.stopGrace):
	}
	e.log.Warn("openvpn ignored SIGTERM; killing it")
	c.kill()
	<-c.done
}

// monitor drives the management session until the child is gone. It returns
// nil after a requested Stop and the reason otherwise.
func (e *engine) monitor(c *child) error {
	ctx, cancel := context.WithTimeout(e.runCtx, e.dialTimeout)
	conn, err := e.channel.dial(ctx, c.done, c.cmd.Process.Pid)
	cancel()
	if err != nil {
		if e.stopRequested() {
			e.terminate(c, nil)
			return nil
		}
		// A child that is still running cannot be controlled: stop it.
		select {
		case <-c.done:
		default:
			e.terminate(c, nil)
		}
		return e.exitFailure(c, err)
	}
	e.mgmtLive.Store(true)

	events := make(chan mgmtEvent)
	quit := make(chan struct{})
	readerDone := make(chan struct{})
	readResult := make(chan error, 1)
	go func() {
		defer close(readerDone)
		readResult <- conn.readLoop(func(ev mgmtEvent) bool {
			select {
			case events <- ev:
				return true
			case <-quit:
				return false
			}
		})
		close(events)
	}()
	defer func() {
		close(quit)
		conn.Close()
		<-readerDone
		e.mgmtLive.Store(false)
	}()

	var (
		eventsCh  = (<-chan mgmtEvent)(events)
		lostTimer <-chan time.Time
		upTimer   <-chan time.Time
		holdTimer <-chan time.Time
	)
	// syncHold arms the timer that ends openvpn's pause between two connection
	// attempts when it starts, and drops it when the pause is over. A repeated
	// notification of the same pause does not extend it.
	syncHold := func() {
		switch {
		case !e.s.holding:
			holdTimer = nil
		case holdTimer == nil:
			holdTimer = time.After(e.s.holdWait)
		}
	}
	// connLost is for a connection that ended or could not be written to: the
	// child is expected to be exiting. Waiting for it gives its exit status and
	// last output; if it does not exit, lostTimer ends it.
	connLost := func() {
		eventsCh = nil
		if lostTimer == nil {
			lostTimer = time.After(e.stopGrace)
		}
	}
	// failed ends the session because of err, unless err only says that openvpn
	// closed the connection. It returns the error to end the session with.
	failed := func(err error) error {
		switch {
		case err == nil:
			return nil
		case errors.Is(err, errMgmtWrite):
			connLost()
			return nil
		}
		e.terminate(c, conn)
		return err
	}

	for _, cmd := range [][2]string{
		{"state", "state on"},
		{"bytecount", "bytecount 2"},
		{"log", "log on"},
		{"hold", "hold release"},
	} {
		if err := conn.Send(cmd[0], cmd[1]); err != nil {
			if err := failed(err); err != nil {
				return err
			}
			break
		}
		e.s.startReleased = e.s.startReleased || cmd[0] == "hold"
	}
	e.takeRebind() // openvpn has just started; there is nothing to restart

	for {
		select {
		case ev, ok := <-eventsCh:
			if !ok {
				connLost()
				continue
			}
			if err := failed(e.handle(ev, conn)); err != nil {
				return err
			}
			syncHold()
			switch {
			case e.s.upKnown || !e.s.connected:
				upTimer = nil
			case upTimer == nil:
				upTimer = time.After(e.upReportGrace)
			}
		case <-c.done:
			e.drain(eventsCh)
			var readErr error
			select {
			case readErr = <-readResult:
			default:
			}
			return e.exitFailure(c, readErr)
		case <-e.wake:
			if err := failed(e.onWake(conn)); err != nil {
				return err
			}
			syncHold()
		case <-holdTimer:
			holdTimer = nil
			if err := failed(e.releaseHold(conn)); err != nil {
				return err
			}
		case <-e.s.stuckC():
			e.onStuck()
		case <-e.stopReq:
			e.refreshState()
			e.terminate(c, conn)
			return nil
		case <-lostTimer:
			e.terminate(c, nil)
			if why := conn.whyClosed(); why != nil {
				return fmt.Errorf("lost the management connection to openvpn: %w", why)
			}
			return errors.New("lost the management connection to openvpn")
		case <-upTimer:
			e.terminate(c, conn)
			return errors.New("openvpn connected but did not report its tunnel interface")
		}
	}
}

// drain handles what openvpn wrote just before it exited: its last log lines
// and fatal message.
func (e *engine) drain(events <-chan mgmtEvent) {
	if events == nil {
		return
	}
	timeout := time.After(300 * time.Millisecond)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return
			}
			switch ev := ev.(type) {
			case logEvent:
				e.logLine(ev.Level, ev.Msg)
			case fatalEvent:
				e.s.lastFatal = ev.Msg
			}
		case <-timeout:
			return
		}
	}
}

// exitFailure describes why the child is gone: its exit status and last lines.
func (e *engine) exitFailure(c *child, cause error) error {
	<-c.done
	msg := "openvpn exited"
	if c.err != nil {
		msg += ": " + c.err.Error()
	}
	if e.s.lastFatal != "" {
		msg += ": " + e.s.lastFatal
	} else if cause != nil {
		msg += ": " + cause.Error()
	}
	if tail := readableTail(e.ring.tail(outputTailLines)); len(tail) > 0 {
		msg += ": " + strings.Join(tail, " | ")
	}
	return errors.New(msg)
}

// outputTimestamp starts the lines openvpn writes to its output: "2026-10-07
// 20:20:12 us=996428 ".
var outputTimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}(?: us=\d+)? `)

// readableTail makes the last lines of openvpn's output fit in one error
// message: the timestamps go, and so do lines that repeat the one before.
func readableTail(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = outputTimestamp.ReplaceAllString(line, "")
	}
	return slices.Compact(out)
}

func (e *engine) takeRebind() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	r := e.rebindRequested
	e.rebindRequested = false
	return r
}

// onWake handles what other goroutines asked for: a credential that arrived
// and a rebind.
func (e *engine) onWake(conn *mgmtConn) error {
	if err := e.answerPrompt(conn); err != nil {
		return err
	}
	if e.takeRebind() && !e.stopRequested() {
		if err := e.refreshEndpoints(); err != nil {
			return err
		}
		if e.s.holding {
			// openvpn ignores SIGUSR1 during its pause, and the pause is what
			// the network change makes pointless.
			e.logLine(tunnel.LogInfo, "network changed; ending the pause before the next attempt")
			return e.releaseHold(conn)
		}
		e.logLine(tunnel.LogInfo, "network changed; restarting the connection")
		return conn.Send("signal", "signal SIGUSR1")
	}
	return nil
}

// refreshEndpoints looks the server names up again and, when the answer
// changed, tells the Reconciler before openvpn reconnects: a network change
// often brings a different answer (dynamic DNS, geo DNS), and the Reconciler
// keeps a route around the tunnel for exactly the addresses it was given. A
// failed lookup keeps the previous addresses.
func (e *engine) refreshEndpoints() error {
	addrs, err := e.resolveEndpoints(rebindLookupTimeout)
	if err != nil {
		e.logLine(tunnel.LogWarn, "keeping the previous server addresses: "+err.Error())
		return nil
	}
	if sameAddrs(addrs, e.s.endpoints) {
		return nil
	}
	e.s.endpoints = addrs
	return e.announce()
}

func sameAddrs(a, b []netip.Addr) bool {
	if len(a) != len(b) {
		return false
	}
	for _, addr := range a {
		if !slices.Contains(b, addr) {
			return false
		}
	}
	return true
}

// announce gives the Reconciler the current statement: the tunnel with its
// routes when it is up, otherwise only the endpoints.
func (e *engine) announce() error {
	if e.s.up != nil {
		if err := e.deps.Network.Announce(upIntent(e.spec, e.prof, *e.s.up, e.s.endpoints, e.s.upSince)); err != nil {
			return fmt.Errorf("announce routes: %w", err)
		}
		return nil
	}
	if err := e.deps.Network.Announce(connectingIntent(e.spec, e.s.endpoints)); err != nil {
		return fmt.Errorf("announce endpoints: %w", err)
	}
	return nil
}

// handle processes one management event. An error is fatal for the session.
func (e *engine) handle(ev mgmtEvent, conn *mgmtConn) error {
	switch ev := ev.(type) {
	case holdEvent:
		return e.onHold(ev, conn)
	case stateEvent:
		e.onState(ev)
	case byteCountEvent:
		e.update(func(s *tunnel.Status) { s.Stats = tunnel.Stats{RxBytes: ev.In, TxBytes: ev.Out} })
	case logEvent:
		e.logLine(ev.Level, ev.Msg)
		e.onLog(ev.Msg)
	case passwordPrompt:
		return e.onPrompt(ev, conn)
	case passwordFailed:
		e.onPasswordFailed(ev)
	case authTokenEvent:
		e.s.tokenHeld = true
	case upDownEvent:
		return e.onUpDown(ev)
	case fatalEvent:
		e.s.lastFatal = ev.Msg
		e.logLine(tunnel.LogError, ev.Msg)
	case infoEvent:
		e.logLine(tunnel.LogInfo, ev.Msg)
	case commandError:
		return e.onCommandError(ev)
	case otherEvent:
		e.log.Debug("management line ignored", "line", e.scrub(truncate(ev.Line, 200)))
	}
	return nil
}

// onHold answers openvpn's wait for a hold release. The first one, with no
// wait, is the start: the engine is ready. Later ones are the pause before
// another connection attempt: releasing at once would make openvpn retry as
// fast as it can fail, thousands of times a second against a server that
// refuses, so the monitor loop releases it after the wait.
func (e *engine) onHold(ev holdEvent, conn *mgmtConn) error {
	if ev.Wait <= 0 {
		if e.s.startReleased && conn.paced() {
			// The release sent at the start answers this notification. A second one
			// would wait for a reply that openvpn does not give while it asks for
			// credentials, and keep every command after it from being sent.
			e.s.startReleased = false
			return nil
		}
		return e.releaseHold(conn)
	}
	e.s.holding = true
	e.s.holdWait = min(ev.Wait, maxRetryWait)
	return nil
}

func (e *engine) releaseHold(conn *mgmtConn) error {
	e.s.holding = false
	return conn.Send("hold", "hold release")
}

// onLog reads what the engine needs from openvpn's log: the dns options the
// server pushed, and why a connection attempt failed.
func (e *engine) onLog(msg string) {
	if reply, ok := parsePushReply(msg); ok {
		e.onPushReply(reply)
		return
	}
	if cause, reach := connectFailure(msg); cause != "" {
		e.setCause(cause, reach)
	}
}

// onPushReply collects the dns options of a PUSH_REPLY. openvpn does not put
// them in the UPDOWN environment, and applies its own pull-filters before it
// acts on them, which the log line does not show.
func (e *engine) onPushReply(reply pushReply) {
	s := &e.s
	if !s.pushMore {
		s.pushedDNS = dnsOptions{} // a new reply; the one before is for an earlier connection
		s.pushedDHCP = nil
	}
	s.pushMore = reply.more
	for _, opt := range reply.options {
		args := strings.Fields(opt)
		if len(args) == 0 || e.prof.filtered(opt) {
			continue
		}
		if readsDHCPOptionsFromLog && args[0] == "dhcp-option" {
			s.pushedDHCP = append(s.pushedDHCP, opt)
			continue
		}
		if args[0] != "dns" {
			continue
		}
		if err := s.pushedDNS.add(args[1:]); err != nil {
			e.logLine(tunnel.LogWarn, fmt.Sprintf("pushed option %q not applied: %v", truncate(opt, 80), err))
		}
	}
}

var (
	tcpConnectFailed = regexp.MustCompile(`^TCP: connect to \[\w+\](\S+) failed: (.+)$`)
	resolveFailed    = regexp.MustCompile(`^RESOLVE: Cannot resolve host address: (\S+) \((.+)\)$`)
	verifyFailed     = regexp.MustCompile(`^VERIFY ERROR: depth=\d+, error=([^:]+)`)
)

// connectFailure describes the log lines of openvpn that say why a connection
// attempt failed, or returns "" for any other line. reach is set when the
// failure is in reaching the server, not in what follows.
func connectFailure(logMsg string) (cause string, reach bool) {
	if m := tcpConnectFailed.FindStringSubmatch(logMsg); m != nil {
		return "cannot reach " + m[1] + ": " + lowerFirst(m[2]), true
	}
	if m := resolveFailed.FindStringSubmatch(logMsg); m != nil {
		host := m[1]
		if i := strings.LastIndexByte(host, ':'); i > 0 {
			host = host[:i] // the port
		}
		return "cannot resolve " + host + ": " + m[2], true
	}
	if m := verifyFailed.FindStringSubmatch(logMsg); m != nil {
		return "server certificate rejected: " + m[1], false
	}
	if strings.HasPrefix(logMsg, "TLS Error: TLS key negotiation failed to occur within") {
		return "no response from the server: TLS handshake timed out", false
	}
	return "", false
}

// serverReached reports whether a >STATE name is one openvpn only gets to once
// it has a connection to the server.
func serverReached(state string) bool {
	switch state {
	case "WAIT", "AUTH", "GET_CONFIG", "ASSIGN_IP", "ADD_ROUTES", "CONNECTED":
		return true
	}
	return false
}

// lowerFirst lower-cases the first letter of an error text such as an errno
// message, so that it reads on after a colon.
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func (e *engine) onState(ev stateEvent) {
	e.s.vpnState = ev.Name
	if e.s.causeConn && serverReached(ev.Name) {
		e.setCause("", false) // a refused connection says nothing about a handshake that stalls
	}
	e.s.connected = ev.Name == "CONNECTED"
	if e.s.connected {
		if remote := ev.RemoteIP; remote != "" {
			if ev.RemotePort != "" {
				remote = net.JoinHostPort(remote, ev.RemotePort)
			}
			e.update(func(s *tunnel.Status) { s.Remote = remote })
		}
		if e.s.upSince.IsZero() {
			e.s.upSince = time.Now()
		}
	}
	e.refreshState()
}

// --- credentials ---

func (e *engine) onPrompt(p passwordPrompt, conn *mgmtConn) error {
	isAuth := p.Type == "Auth" && p.UserPass
	isKey := p.Type == "Private Key" && !p.UserPass
	if p.Challenge || !(isAuth || isKey) {
		return fmt.Errorf("openvpn asked for credentials this engine cannot supply (%s)", truncate(p.Type, 40))
	}
	e.s.prompt = &p
	return e.answerPrompt(conn)
}

// answerPrompt answers the pending prompt from the stored credentials, or
// reports that it is waiting for them.
func (e *engine) answerPrompt(conn *mgmtConn) error {
	p := e.s.prompt
	if p == nil {
		return nil
	}
	kind := tunnel.CredentialKeyPassphrase
	if p.UserPass {
		kind = tunnel.CredentialUserPassword
	}

	e.mu.Lock()
	cred := e.keyPass
	if p.UserPass {
		cred = e.userPass
	}
	e.mu.Unlock()

	if !cred.set {
		e.s.needCreds = kind
		e.refreshState()
		return nil
	}
	if p.UserPass {
		cmd, err := credentialCommand("username", p.Type, cred.user)
		if err != nil {
			return err
		}
		if err := conn.Send("username", cmd); err != nil {
			return err
		}
	}
	cmd, err := credentialCommand("password", p.Type, cred.secret)
	if err != nil {
		return err
	}
	if err := conn.Send("password", cmd); err != nil {
		return err
	}
	e.s.prompt = nil
	e.s.needCreds = tunnel.CredentialNone
	e.refreshState()
	return nil
}

// onPasswordFailed handles the server or the key file rejecting the
// credentials: they are dropped and the user is asked again.
func (e *engine) onPasswordFailed(ev passwordFailed) {
	if ev.Type == "Auth" && e.softAuthFailure(ev) {
		return
	}
	kind, reason := tunnel.CredentialUserPassword, "authentication failed"
	e.mu.Lock()
	if ev.Type == "Private Key" {
		kind, reason = tunnel.CredentialKeyPassphrase, "wrong key passphrase"
		e.keyPass = credential{}
	} else {
		e.userPass = credential{}
	}
	e.mu.Unlock()
	e.s.prompt = nil
	e.s.needCreds = kind
	e.s.credErr = reason
	e.refreshState()
}

// softAuthFailure reports whether openvpn's failed verification leaves the
// user's credentials alone, as it does itself: it reconnects with them after
// the server refused the session token it holds (the server restarted, or the
// token expired, and the password is as good as before) and after the server
// said the failure is temporary. Only a rejection after those is a rejected
// password.
func (e *engine) softAuthFailure(ev passwordFailed) bool {
	switch {
	case strings.HasPrefix(ev.Reason, "TEMP"):
		e.logLine(tunnel.LogInfo, "the server refused the login for now; openvpn tries again")
	case e.s.tokenHeld:
		e.s.tokenHeld = false
		e.logLine(tunnel.LogInfo, "the server did not accept its session token; openvpn reconnects with the credentials")
	default:
		return false
	}
	return true
}

// onCommandError handles openvpn refusing a command. The ones the engine cannot do
// without end the session: with no state notices it cannot tell what openvpn is
// doing, and with the hold not released openvpn does nothing.
func (e *engine) onCommandError(ev commandError) error {
	switch ev.Cmd {
	case "state", "log", "hold":
		return fmt.Errorf("openvpn refused the command %q: %s", ev.Cmd, ev.Msg)
	case "username", "password":
		// openvpn refused the credential itself, for example for its length.
		kind := tunnel.CredentialUserPassword
		if e.s.prompt != nil && !e.s.prompt.UserPass {
			kind = tunnel.CredentialKeyPassphrase
		}
		e.mu.Lock()
		e.userPass, e.keyPass = credential{}, credential{}
		e.mu.Unlock()
		e.s.needCreds = kind
		e.s.credErr = "openvpn rejected the credentials: " + ev.Msg
		e.refreshState()
	default:
		e.logLine(tunnel.LogWarn, fmt.Sprintf("management command %q failed: %s", ev.Cmd, ev.Msg))
	}
	return nil
}

// --- tunnel up and down ---

func (e *engine) onUpDown(ev upDownEvent) error {
	switch ev.Kind {
	case "UP":
		return e.onTunnelUp(ev.Env)
	case "DOWN":
		return e.onTunnelDown()
	}
	return nil
}

func (e *engine) onTunnelUp(env map[string]string) error {
	if name := e.device.name(); name != "" {
		// The interface is the engine's own; what openvpn calls it is not needed.
		env["dev"] = name
	}
	up, notes, err := parseUpEnv(env)
	if err != nil {
		return err
	}
	up.PulledDNS = e.s.pushedDNS
	for _, opt := range e.s.pushedDHCP {
		if note := up.addDHCPOption(opt); note != "" {
			notes = append(notes, note)
		}
	}
	for _, n := range notes {
		e.logLine(tunnel.LogWarn, n)
	}
	if err := e.device.configure(e.runCtx, up); err != nil {
		return fmt.Errorf("configure the tunnel interface: %w", err)
	}
	if e.s.upSince.IsZero() {
		e.s.upSince = time.Now()
	}
	e.s.up = &up
	if err := e.announce(); err != nil {
		e.s.up = nil
		return err
	}
	e.s.upKnown = true
	e.update(func(s *tunnel.Status) {
		s.Iface = up.Iface
		s.Addresses = up.Addresses
	})
	e.refreshState()
	return nil
}

// onTunnelDown handles the interface going away: its routes go with it, so
// only the endpoints stay announced.
func (e *engine) onTunnelDown() error {
	e.s.up, e.s.upKnown = nil, false
	e.s.upSince = time.Time{}
	e.update(func(s *tunnel.Status) {
		s.Iface = ""
		s.Addresses = nil
		s.Since = time.Time{}
	})
	e.refreshState()
	if e.stopRequested() {
		return nil
	}
	return e.announce()
}
