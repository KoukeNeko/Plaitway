package fake

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// A pretend endpoint for profiles whose server is a name or missing.
var stubEndpoint = netip.MustParseAddr("198.51.100.7")

var _ tunnel.Engine = (*engine)(nil)

type credentials struct{ username, password string }

type engine struct {
	cfg     Config
	spec    tunnel.Spec
	deps    tunnel.Deps
	summary tunnel.Summary
	number  int // distinguishes this engine's interface and address

	needsCredentials, failConnecting, conflict bool

	creds  chan credentials
	rebind chan struct{}
	done   chan struct{}
	// rejectedWrong is only touched by the run goroutine.
	rejectedWrong bool

	mu      sync.Mutex
	started bool
	closed  bool // the Status channel is closed
	cancel  context.CancelFunc
	current tunnel.Status
	out     chan tunnel.Status
}

func newEngine(f *factory, kind tunnel.Kind, spec tunnel.Spec, deps tunnel.Deps) (*engine, error) {
	parsed, err := parse(kind, spec.Content)
	if err != nil {
		return nil, err
	}
	text := string(spec.Content)
	return &engine{
		number:           int(f.nextNumber.Add(1)) - 1,
		cfg:              f.cfg,
		spec:             spec,
		deps:             deps,
		summary:          parsed.Summary,
		needsCredentials: parsed.Summary.RequiresCredentials,
		failConnecting:   hasMarker(text, markerFail),
		conflict:         hasMarker(text, markerConflict),
		creds:            make(chan credentials, 1),
		rebind:           make(chan struct{}, 1),
		done:             make(chan struct{}),
		out:              make(chan tunnel.Status, 1),
	}, nil
}

// iface is the pretend interface name.
func (e *engine) iface() string { return "utun" + strconv.Itoa(100+e.number) }

func (e *engine) Start(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started || e.closed {
		return errors.New("engine already started")
	}
	e.started = true
	ctx, e.cancel = context.WithCancel(ctx)
	go e.run(ctx)
	return nil
}

func (e *engine) Stop(ctx context.Context) error {
	e.mu.Lock()
	cancel := e.cancel
	e.mu.Unlock()
	if cancel != nil {
		cancel()
		select {
		case <-e.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	e.deps.Network.Withdraw(e.spec.Owner)

	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.closed {
		e.closed = true
		e.publishLocked(tunnel.Status{State: tunnel.StateDisconnected})
		close(e.out)
	}
	return nil
}

func (e *engine) Status() <-chan tunnel.Status { return e.out }

func (e *engine) ProvideCredentials(kind tunnel.CredentialKind, username, password string) error {
	e.mu.Lock()
	awaiting := e.current.State == tunnel.StateAwaitingCredentials
	e.mu.Unlock()
	switch {
	case !awaiting:
		return errors.New("not waiting for credentials")
	case kind != tunnel.CredentialUserPassword:
		return errors.New("a user name and password are expected")
	case username == "" || password == "":
		return errors.New("user name and password must not be empty")
	}
	select {
	case e.creds <- credentials{username, password}:
		return nil
	default:
		return errors.New("credentials were already provided")
	}
}

func (e *engine) Rebind() {
	select {
	case e.rebind <- struct{}{}:
	default:
	}
}

func (e *engine) run(ctx context.Context) {
	defer close(e.done)
	remote := e.remote()
	e.log(tunnel.LogInfo, "connecting to "+remote)
	e.set(tunnel.Status{State: tunnel.StateConnecting, Remote: remote})
	if err := e.deps.Network.Announce(tunnel.Intent{
		Owner:     e.spec.Owner,
		State:     tunnel.StateConnecting,
		Priority:  e.spec.Priority,
		Endpoints: e.endpoints(),
	}); err != nil {
		e.fail(remote, err)
		return
	}
	if !e.wait(ctx, e.cfg.ConnectDelay) {
		return
	}
	if e.needsCredentials && !e.authenticate(ctx, remote) {
		return
	}
	if e.failConnecting {
		e.fail(remote, errors.New("no response from the server (simulated)"))
		return
	}

	since := time.Now()
	if err := e.deps.Network.Announce(e.upIntent(since)); err != nil {
		e.fail(remote, err)
		return
	}
	up := tunnel.Status{
		State:     tunnel.StateUp,
		Iface:     e.iface(),
		Addresses: e.addresses(),
		Remote:    remote,
		Since:     since,
	}
	e.set(up)
	e.log(tunnel.LogInfo, "connected on "+e.iface())
	e.hold(ctx, up)
}

// authenticate asks for credentials until the server accepts them.
func (e *engine) authenticate(ctx context.Context, remote string) bool {
	var lastErr string
	for {
		e.set(tunnel.Status{State: tunnel.StateAwaitingCredentials, Remote: remote, NeedsCredentials: tunnel.CredentialUserPassword, Err: lastErr})
		e.log(tunnel.LogInfo, "waiting for credentials")
		var c credentials
		select {
		case c = <-e.creds:
		case <-ctx.Done():
			return false
		}
		e.set(tunnel.Status{State: tunnel.StateConnecting, Remote: remote})
		if !e.wait(ctx, e.cfg.ConnectDelay/2) {
			return false
		}
		if c.password == "wrong" && !e.rejectedWrong {
			e.rejectedWrong = true
			lastErr = "authentication failed"
			e.log(tunnel.LogWarn, lastErr)
			continue
		}
		return true
	}
}

// hold keeps the tunnel up: the counters move, and a network change makes it
// reconnect for a moment.
func (e *engine) hold(ctx context.Context, up tunnel.Status) {
	ticker := time.NewTicker(e.cfg.StatsInterval)
	defer ticker.Stop()
	for tick := uint64(1); ; tick++ {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			up.Stats.RxBytes += 64<<10 + tick*512
			up.Stats.TxBytes += 16<<10 + tick*128
			e.set(up)
		case <-e.rebind:
			e.log(tunnel.LogInfo, "network changed, reconnecting")
			reconnecting := up
			reconnecting.State = tunnel.StateReconnecting
			e.set(reconnecting)
			if !e.wait(ctx, e.cfg.ConnectDelay/2) {
				return
			}
			e.set(up)
		}
	}
}

func (e *engine) fail(remote string, err error) {
	e.log(tunnel.LogError, "connection failed: "+err.Error())
	e.deps.Network.Withdraw(e.spec.Owner)
	e.set(tunnel.Status{State: tunnel.StateFailed, Remote: remote, Err: "connection failed: " + err.Error()})
}

func (e *engine) wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// set records a status snapshot. The channel keeps only the newest one: a
// reader that is behind skips to the latest.
func (e *engine) set(st tunnel.Status) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	e.publishLocked(st)
}

func (e *engine) publishLocked(st tunnel.Status) {
	e.current = st
	select {
	case e.out <- st:
	default:
		select {
		case <-e.out:
		default:
		}
		e.out <- st // the mutex makes this the only sender, so there is room now
	}
}

func (e *engine) log(level tunnel.LogLevel, text string) { e.deps.Log(level, text) }

func (e *engine) remote() string {
	if len(e.summary.Endpoints) == 0 {
		return net.JoinHostPort("vpn.fake.example", "1194")
	}
	ep := e.summary.Endpoints[0]
	return net.JoinHostPort(ep.Host, strconv.Itoa(int(ep.Port)))
}

// endpoints are the addresses the tunnel itself talks to.
func (e *engine) endpoints() []netip.Addr {
	if len(e.summary.Endpoints) > 0 {
		if a, err := netip.ParseAddr(e.summary.Endpoints[0].Host); err == nil {
			return []netip.Addr{a}
		}
	}
	return []netip.Addr{stubEndpoint}
}

func (e *engine) addresses() []netip.Prefix {
	if len(e.summary.Addresses) > 0 {
		return slices.Clone(e.summary.Addresses)
	}
	return []netip.Prefix{netip.MustParsePrefix(fmt.Sprintf("10.8.%d.2/24", e.number%250))}
}

// upIntent is what the engine asks of the host network once it is up. The
// profile's tunnel mode decides, as for a real engine, whether it takes the
// default route.
func (e *engine) upIntent(since time.Time) tunnel.Intent {
	full := e.spec.Mode == tunnel.ModeFull || (e.spec.Mode == tunnel.ModeAuto && e.summary.RedirectsDefaultRoute)
	routes := slices.DeleteFunc(slices.Clone(e.summary.Routes), func(p netip.Prefix) bool { return p.Bits() == 0 })
	role := tunnel.RoleSplit
	match := []string{"corp.example"}
	if full {
		role = tunnel.RoleFull
		routes = append(routes, netip.MustParsePrefix("0.0.0.0/0"))
		match = []string{"."}
	}
	if e.conflict {
		routes = append(routes, shadowedRoute, localSubnet)
	}
	servers := slices.Clone(e.summary.DNSServers)
	if len(servers) == 0 {
		servers = []netip.Addr{netip.MustParseAddr("10.8.0.1")}
	}
	return tunnel.Intent{
		Owner:     e.spec.Owner,
		State:     tunnel.StateUp,
		Iface:     e.iface(),
		Role:      role,
		Priority:  e.spec.Priority,
		UpSince:   since,
		Endpoints: e.endpoints(),
		Routes:    routes,
		DNS:       []tunnel.DNSIntent{{Servers: servers, MatchDomains: match}},
	}
}
