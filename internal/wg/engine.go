package wg

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

const (
	// resolveTimeout bounds one endpoint lookup. With a full tunnel the system
	// resolver may be behind the tunnel itself, which is not carrying traffic
	// while it re-establishes.
	resolveTimeout = 10 * time.Second

	noHandshakeReason = "no handshake with the peer yet"
)

type phase uint8

const (
	phaseNew     phase = iota
	phaseStarted       // the engine's goroutine runs, or has ended on its own
	phaseStopped
)

// engine runs one WireGuard profile. Start launches one goroutine, which finds
// the endpoints, brings the device up and then watches it until Stop, so
// everything that waits for the network or the system happens there.
type engine struct {
	cfg  Config
	spec tunnel.Spec
	deps tunnel.Deps
	prof *profile
	plan plan
	// initiating is true when some peer has an endpoint to send handshakes to.
	initiating bool

	feed    *statusFeed
	rebindc chan struct{} // Rebind's request to the engine's goroutine; room for one

	mu     sync.Mutex // serializes Start and Stop
	phase  phase
	cancel context.CancelFunc // ends the engine's goroutine
	done   chan struct{}      // closed when it has released everything and returned
	// announced is true while the Reconciler may hold state for this owner.
	announced atomic.Bool

	// Owned by the engine's goroutine.
	dev            *device.Device
	iface          string
	endpoints      []netip.AddrPort // resolved endpoint of each peer
	announcedState tunnel.State     // StateConnecting or StateUp
	upSince        time.Time
	// While awaiting, the engine waits for a handshake not older than awaitSince.
	awaiting    bool
	awaitSince  time.Time
	noHandshake bool
}

func newEngine(cfg Config, spec tunnel.Spec, deps tunnel.Deps) (*engine, error) {
	prof, err := parseProfile(spec.Content)
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", spec.Name, err)
	}
	prof.excludePrivate = spec.ExcludePrivateIPs
	return &engine{
		cfg:        cfg,
		spec:       spec,
		deps:       deps,
		prof:       prof,
		plan:       prof.plan(spec.Mode),
		initiating: slices.ContainsFunc(prof.peers, func(p peerConfig) bool { return p.endpoint != nil }),
		feed:       newStatusFeed(),
		rebindc:    make(chan struct{}, 1),
	}, nil
}

func (e *engine) Status() <-chan tunnel.Status { return e.feed.ch }

func (e *engine) ProvideCredentials(tunnel.CredentialKind, string, string) error {
	return errors.New("WireGuard profiles take no credentials")
}

// Rebind asks the engine's goroutine to re-establish the underlay connection.
// It never blocks: the Reconciler calls it while it may hold locks Start waits
// for.
func (e *engine) Rebind() {
	select {
	case e.rebindc <- struct{}{}:
	default:
	}
}

// Start returns at once: the engine resolves its endpoints, creates the device
// and shakes hands in the background, in StateConnecting, and reports what
// goes wrong in its status. ctx is looked at only here; Stop ends the engine.
func (e *engine) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.phase != phaseNew {
		return errors.New("engine can be started only once")
	}
	e.phase = phaseStarted
	// A change of the network from before Start is already behind the setup.
	select {
	case <-e.rebindc:
	default:
	}
	e.feed.update(func(s *tunnel.Status) {
		s.State = tunnel.StateConnecting
		s.Addresses = e.prof.addresses
		s.Remote = e.remote()
		s.Warnings = slices.Clone(e.plan.warnings)
	})

	runCtx, cancel := context.WithCancel(context.Background())
	e.cancel, e.done = cancel, make(chan struct{})
	go e.run(runCtx)
	return nil
}

// run is the engine's goroutine. A setup failure is final (the status says
// Failed, nothing is kept) but a network that is not ready is not: the
// endpoint lookup and the first handshake are waited for until ctx ends.
func (e *engine) run(ctx context.Context) {
	defer close(e.done)
	defer e.release()

	endpoints, err := e.resolveEndpoints(ctx)
	if err != nil {
		return // Stop
	}
	e.endpoints = endpoints
	e.setStall("")

	if err := e.bringUp(ctx); err != nil {
		if ctx.Err() == nil {
			e.fail(err)
		}
		return
	}
	e.monitor(ctx)
}

// createTun makes the tunnel device: the configured factory, else the
// platform's own.
func (e *engine) createTun(mtu int) (tun.Device, error) {
	if e.cfg.TunFactory != nil {
		return e.cfg.TunFactory(mtu)
	}
	return createPlatformTun(e.spec.Owner, e.cfg.adapterPrefix, mtu, e.log)
}

// interfaces is the configured Interfaces, else the platform's own for dev.
func (e *engine) interfaces(dev tun.Device) Interfaces {
	if e.cfg.Interfaces != nil {
		return e.cfg.Interfaces
	}
	return platformInterfaces(dev)
}

func (e *engine) bringUp(ctx context.Context) error {
	mtu := cmp.Or(e.prof.mtu, device.DefaultMTU)
	tunDev, err := e.createTun(mtu)
	if err != nil {
		return fmt.Errorf("create tunnel device: %w", err)
	}
	e.iface, err = tunDev.Name()
	if err != nil {
		tunDev.Close()
		return fmt.Errorf("read tunnel device name: %w", err)
	}
	// From here on the device owns tunDev: closing the device closes it.
	e.dev = device.NewDevice(tunDev, newBind(), e.deviceLogger())
	e.feed.update(func(s *tunnel.Status) { s.Iface = e.iface })

	if err := e.interfaces(tunDev).Configure(ctx, e.iface, e.prof.addresses, mtu); err != nil {
		return fmt.Errorf("configure %s: %w", e.iface, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// The key and the peers go in only after the Reconciler has the endpoints:
	// a peer with a persistent keepalive starts its handshake as soon as the
	// device is up, and the tunnel interface can raise that by itself.
	e.announce(tunnel.StateConnecting)
	e.awaiting, e.awaitSince = true, time.Now()
	if err := e.dev.IpcSet(e.prof.uapiConfig(e.endpoints)); err != nil {
		return fmt.Errorf("configure device: %w", err)
	}
	if err := e.dev.Up(); err != nil {
		return fmt.Errorf("bring device up: %w", err)
	}
	if err := e.checkListenPort(); err != nil {
		return err
	}
	e.initiate()
	return nil
}

// Stop ends the engine's goroutine, which withdraws the intent and closes the
// device (which destroys the tunnel interface), and closes the status channel.
// It works in every phase of the setup: the goroutine ends whatever lookup or
// interface command it is in. When ctx ends first, the goroutine may still be
// inside a call to the Network and finishes the release when that returns.
func (e *engine) Stop(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	was := e.phase
	if was == phaseStopped {
		return nil
	}
	e.phase = phaseStopped

	var err error
	if was == phaseStarted {
		e.setState(tunnel.StateDisconnecting)
		e.cancel()
		select {
		case <-e.done:
		case <-ctx.Done():
			err = ctx.Err()
		}
		e.feed.update(func(s *tunnel.Status) {
			if s.State != tunnel.StateFailed {
				s.State, s.Err = tunnel.StateDisconnected, ""
			}
			s.Iface, s.Since = "", time.Time{}
		})
	}
	e.feed.close()
	return err
}

// setState changes the state unless the engine already failed, which a
// shutdown must not hide.
func (e *engine) setState(state tunnel.State) {
	e.feed.update(func(s *tunnel.Status) {
		if s.State != tunnel.StateFailed {
			s.State = state
		}
	})
}

// setStall says why the tunnel is not coming up, or takes the reason back with
// "". The state stays Connecting: what is waited for may still happen.
func (e *engine) setStall(reason string) {
	e.feed.update(func(s *tunnel.Status) { s.Err = reason })
}

// release withdraws the intent and closes the device. Both are safe to repeat.
func (e *engine) release() {
	if e.announced.Swap(false) {
		e.deps.Network.Withdraw(e.spec.Owner)
	}
	if e.dev != nil {
		e.dev.Close()
		// Stop promises that the interface is gone.
		awaitInterfaceRemoval(e.iface, e.log)
	}
}

func (e *engine) monitor(ctx context.Context) {
	ticker := time.NewTicker(e.cfg.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.dev.Wait():
			e.fail(errors.New("tunnel device closed"))
			return
		case <-ticker.C:
			e.poll()
		case <-e.rebindc:
			e.rebind(ctx)
		}
	}
}

// fail ends the tunnel's life: the device closed by itself or the setup could
// not finish. The routes are bound to an interface that is gone or never
// worked, so the intent is withdrawn at once; Stop does the rest.
func (e *engine) fail(err error) {
	e.log(tunnel.LogError, err.Error())
	if e.announced.Swap(false) {
		e.deps.Network.Withdraw(e.spec.Owner)
	}
	e.feed.update(func(s *tunnel.Status) {
		s.State = tunnel.StateFailed
		s.Err = err.Error()
	})
}

func (e *engine) readDevice() (deviceStats, error) {
	dump, err := e.dev.IpcGet()
	if err != nil {
		return deviceStats{}, fmt.Errorf("read device state: %w", err)
	}
	return parseDeviceStats(dump)
}

// checkListenPort fails when the device is not bound to the profile's
// ListenPort. The interface coming up raises an Up of its own, which runs
// before the port is set and, when the port is taken, leaves wireguard-go on a
// random one; the Up that follows then succeeds silently.
func (e *engine) checkListenPort() error {
	if e.prof.listenPort == 0 {
		return nil
	}
	stats, err := e.readDevice()
	if err != nil {
		return err
	}
	if stats.listenPort != e.prof.listenPort {
		return fmt.Errorf("cannot listen on port %d", e.prof.listenPort)
	}
	return nil
}

// poll reads the device's counters and handshake, and moves the status along.
func (e *engine) poll() {
	stats, err := e.readDevice()
	if err != nil {
		e.log(tunnel.LogWarn, err.Error())
		return
	}

	switch {
	case e.awaiting && !stats.lastHandshake.Before(e.awaitSince):
		// Not After: the Windows wall clock ticks about every millisecond, so a
		// handshake answered over a fast link can carry awaitSince's timestamp.
		e.handshakeDone()
	case e.awaiting:
		// Without routes nothing else would make wireguard-go try again once
		// its own retries have run out.
		e.initiate()
		if !e.noHandshake && time.Since(e.awaitSince) >= e.cfg.stallGrace {
			e.noHandshake = true
			e.log(tunnel.LogWarn, noHandshakeReason)
			e.setStall(noHandshakeReason)
		}
	}
	e.feed.update(func(s *tunnel.Status) {
		s.Stats = tunnel.Stats{RxBytes: stats.rxBytes, TxBytes: stats.txBytes}
	})
}

// handshakeDone: the peer answers. The first time, the routes and DNS are
// announced, so that nothing is sent into the tunnel before it works.
func (e *engine) handshakeDone() {
	e.awaiting, e.noHandshake = false, false
	if e.upSince.IsZero() {
		e.upSince = time.Now()
		e.announce(tunnel.StateUp)
	}
	e.feed.update(func(s *tunnel.Status) {
		s.State = tunnel.StateUp
		s.Err = ""
		s.Since = e.upSince
	})
}

// rebind follows a change of the underlay network: new sockets and a new
// handshake with the address in use, then a fresh look at the endpoint names.
// The lookup comes last because it may take until its timeout and the name
// usually still means the same server. The Reconciler has fixed the routes by
// now.
func (e *engine) rebind(ctx context.Context) {
	e.log(tunnel.LogInfo, "network changed, rebinding")
	if err := e.dev.BindUpdate(); err != nil {
		e.log(tunnel.LogWarn, "rebind sockets: "+err.Error())
	}
	if !e.initiating {
		return
	}
	e.restartHandshakes()
	e.feed.update(func(s *tunnel.Status) {
		if s.State == tunnel.StateUp {
			s.State = tunnel.StateReconnecting
		}
		s.Err = ""
	})
	e.refreshEndpoints(ctx)
}

// restartHandshakes drops the session of every peer that has an endpoint and
// sends it a new handshake initiation. Expiring the keypairs also lifts
// wireguard-go's limit of one initiation per RekeyTimeout, so it goes out at
// once.
func (e *engine) restartHandshakes() {
	e.eachInitiatingPeer(func(peer *device.Peer) { peer.ExpireCurrentKeypairs() })
	e.awaiting, e.awaitSince, e.noHandshake = true, time.Now(), false
	e.initiate()
}

// refreshEndpoints resolves the endpoint names again and, when an address
// changed, tells the Reconciler before wireguard-go uses it and shakes hands
// with the new address.
func (e *engine) refreshEndpoints(ctx context.Context) {
	endpoints, err := e.resolve(ctx)
	if err != nil {
		e.log(tunnel.LogWarn, "resolve endpoint, keeping the previous address: "+err.Error())
		return
	}
	if slices.Equal(endpoints, e.endpoints) {
		return
	}
	previous := e.endpoints
	e.endpoints = endpoints
	e.announce(e.announcedState)
	if err := e.dev.IpcSet(e.prof.endpointUpdate(previous, endpoints)); err != nil {
		e.endpoints = previous
		e.log(tunnel.LogError, "set endpoint: "+err.Error())
		return
	}
	e.restartHandshakes()
}

// initiate sends a handshake initiation to every peer that has an endpoint.
// wireguard-go sends at most one per RekeyTimeout.
func (e *engine) initiate() {
	e.eachInitiatingPeer(func(peer *device.Peer) {
		if err := peer.SendHandshakeInitiation(false); err != nil {
			// wireguard-go has logged the failure; this only marks the retry.
			e.log(tunnel.LogDebug, "handshake initiation: "+err.Error())
		}
	})
}

func (e *engine) eachInitiatingPeer(fn func(*device.Peer)) {
	for _, p := range e.prof.peers {
		if p.endpoint == nil {
			continue
		}
		if peer := e.dev.LookupPeer(device.NoisePublicKey(p.publicKey)); peer != nil {
			fn(peer)
		}
	}
}

// announce replaces the engine's intent. A failure is logged and left to the
// next announcement: the Reconciler also reports what it could not apply.
func (e *engine) announce(state tunnel.State) {
	intent := tunnel.Intent{
		Owner:     e.spec.Owner,
		State:     state,
		Iface:     e.iface,
		Role:      e.plan.role,
		Priority:  e.spec.Priority,
		Endpoints: e.endpointAddrs(),
	}
	name := "connecting"
	if state == tunnel.StateUp {
		name = "up"
		intent.UpSince = e.upSince
		intent.Routes = e.plan.routes
		intent.DNS = e.plan.dns
	}
	e.announced.Store(true)
	e.announcedState = state
	if err := e.deps.Network.Announce(intent); err != nil {
		e.log(tunnel.LogWarn, fmt.Sprintf("announce %s: %v", name, err))
	}
}

func (e *engine) endpointAddrs() []netip.Addr {
	var addrs []netip.Addr
	for _, ep := range e.endpoints {
		if ep.IsValid() && !slices.Contains(addrs, ep.Addr()) {
			addrs = append(addrs, ep.Addr())
		}
	}
	return addrs
}

// resolveEndpoints resolves the endpoint names until it succeeds. A name that
// does not resolve usually means the network is not up yet, as when the daemon
// starts at boot, so the engine stays Connecting and tries again after a pause
// that doubles up to retryMax; a change of the network ends the pause at once.
// Once stallGrace has passed without an answer, the reason is put in the
// status. It returns an error only when ctx ends.
func (e *engine) resolveEndpoints(ctx context.Context) ([]netip.AddrPort, error) {
	retry := time.NewTimer(0)
	stall := time.NewTimer(e.cfg.stallGrace)
	defer retry.Stop()
	defer stall.Stop()
	pause := e.cfg.retryMin
	stalled := false
	var reason string
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-e.rebindc:
			pause = e.cfg.retryMin
			retry.Reset(0)
		case <-stall.C:
			stalled = true
			e.setStall(reason)
		case <-retry.C:
			endpoints, err := e.resolve(ctx)
			if err == nil {
				return endpoints, nil
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if failure := "resolve endpoint: " + err.Error(); failure != reason {
				reason = failure
				e.log(tunnel.LogWarn, reason+", retrying")
				if stalled {
					e.setStall(reason)
				}
			}
			retry.Reset(pause)
			pause = min(2*pause, e.cfg.retryMax)
		}
	}
}

// resolve returns the address of each peer's endpoint, taking IPv4 over IPv6
// when a name has both.
func (e *engine) resolve(ctx context.Context) ([]netip.AddrPort, error) {
	endpoints := make([]netip.AddrPort, len(e.prof.peers))
	for i, peer := range e.prof.peers {
		ep := peer.endpoint
		if ep == nil {
			continue
		}
		addr := ep.addr
		if !addr.IsValid() {
			var err error
			if addr, err = e.lookup(ctx, ep.host); err != nil {
				return nil, fmt.Errorf("%s: %w", ep.host, err)
			}
		}
		endpoints[i] = netip.AddrPortFrom(addr, ep.port)
	}
	return endpoints, nil
}

func (e *engine) lookup(ctx context.Context, host string) (netip.Addr, error) {
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	addrs, err := e.cfg.lookup(ctx, host)
	if err != nil {
		return netip.Addr{}, err
	}
	var first netip.Addr
	for _, addr := range addrs {
		if addr = addr.Unmap(); addr.Is4() {
			return addr, nil
		} else if !first.IsValid() {
			first = addr
		}
	}
	if !first.IsValid() {
		return netip.Addr{}, errors.New("no addresses")
	}
	return first, nil
}

// remote is the first endpoint as the profile writes it.
func (e *engine) remote() string {
	for _, peer := range e.prof.peers {
		if peer.endpoint != nil {
			return net.JoinHostPort(peer.endpoint.host, strconv.Itoa(int(peer.endpoint.port)))
		}
	}
	return ""
}

// deviceLogger passes wireguard-go's log lines on to the daemon.
func (e *engine) deviceLogger() *device.Logger {
	return &device.Logger{
		Verbosef: func(format string, args ...any) { e.log(tunnel.LogDebug, fmt.Sprintf(format, args...)) },
		Errorf:   func(format string, args ...any) { e.log(tunnel.LogError, fmt.Sprintf(format, args...)) },
	}
}

func (e *engine) log(level tunnel.LogLevel, text string) {
	if e.deps.Log != nil {
		e.deps.Log(level, text)
	}
	e.cfg.Log.Log(context.Background(), slogLevel(level), text, "owner", e.spec.Owner)
}

func slogLevel(level tunnel.LogLevel) slog.Level {
	switch level {
	case tunnel.LogDebug:
		return slog.LevelDebug
	case tunnel.LogWarn:
		return slog.LevelWarn
	case tunnel.LogError:
		return slog.LevelError
	}
	return slog.LevelInfo
}
