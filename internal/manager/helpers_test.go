package manager_test

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/manager"
	"github.com/KoukeNeko/Plaitway/internal/manager/fake"
	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

const (
	waitTimeout = 5 * time.Second
	// onDemandSettle is long enough for a test to emit a burst of changes
	// inside one window and short enough not to slow the tests down.
	onDemandSettle = 150 * time.Millisecond
)

var fastFake = fake.Config{ConnectDelay: 20 * time.Millisecond, StatsInterval: 10 * time.Millisecond}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// env is a started manager on the fake reconciler.
type env struct {
	t   *testing.T
	m   *manager.Manager
	rec *fake.Reconciler
	dir string
}

type envOption func(*envConfig)

type envConfig struct {
	backends    []tunnel.Backend
	rec         tunnel.Reconciler
	net         osnet.NetMonitor
	log         *slog.Logger
	stopTimeout time.Duration
	startWait   time.Duration
	daemonLog   *manager.LogBuffer
}

func withBackends(b ...tunnel.Backend) envOption { return func(c *envConfig) { c.backends = b } }
func withReconciler(r tunnel.Reconciler) envOption {
	return func(c *envConfig) { c.rec = r }
}
func withNet(n osnet.NetMonitor) envOption      { return func(c *envConfig) { c.net = n } }
func withLogger(l *slog.Logger) envOption       { return func(c *envConfig) { c.log = l } }
func withStopTimeout(d time.Duration) envOption { return func(c *envConfig) { c.stopTimeout = d } }
func withStartWait(d time.Duration) envOption   { return func(c *envConfig) { c.startWait = d } }
func withDaemonLog(b *manager.LogBuffer) envOption {
	return func(c *envConfig) { c.daemonLog = b }
}

func newEnv(t *testing.T, opts ...envOption) *env {
	t.Helper()
	return startEnv(t, t.TempDir(), opts...)
}

// startEnv starts a manager on dir, which a previous env may have used.
func startEnv(t *testing.T, dir string, opts ...envOption) *env {
	t.Helper()
	rec := fake.NewReconciler()
	cfg := envConfig{backends: fake.Backends(fastFake), rec: rec, net: rec, log: discardLog()}
	for _, o := range opts {
		o(&cfg)
	}
	m, err := manager.New(manager.Config{
		Log:            cfg.log,
		StateDir:       dir,
		Backends:       cfg.backends,
		Reconciler:     cfg.rec,
		Net:            cfg.net,
		DaemonLog:      cfg.daemonLog,
		Version:        "test",
		Privileged:     true,
		StopTimeout:    cfg.stopTimeout,
		StartWait:      cfg.startWait,
		OnDemandSettle: onDemandSettle,
	})
	if err != nil {
		t.Fatal(err)
	}
	m.Start()
	e := &env{t: t, m: m, rec: rec, dir: dir}
	t.Cleanup(func() { e.shutdown() })
	return e
}

func (e *env) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.m.Shutdown(ctx); err != nil {
		e.t.Errorf("Shutdown: %v", err)
	}
}

func (e *env) importProfile(name, content string) *pb.Profile {
	e.t.Helper()
	return e.importWith(&pb.ImportProfileRequest{Name: name, Content: []byte(content)}).Profile
}

func (e *env) importWith(req *pb.ImportProfileRequest) *pb.ImportProfileResponse {
	e.t.Helper()
	resp, err := e.m.Import(req)
	if err != nil {
		e.t.Fatalf("Import(%q): %v", req.Name, err)
	}
	return resp
}

func (e *env) get(id string) *pb.Profile {
	for _, p := range e.m.List() {
		if p.Id == id {
			return p
		}
	}
	return nil
}

func (e *env) setEnabled(id string, enabled bool) *pb.Profile {
	e.t.Helper()
	p, err := e.m.SetEnabled(id, enabled)
	if err != nil {
		e.t.Fatalf("SetEnabled(%s, %v): %v", id, enabled, err)
	}
	return p
}

func (e *env) waitState(id string, want pb.ProfileState) *pb.Profile {
	e.t.Helper()
	var last *pb.Profile
	eventually(e.t, id+" to be "+want.String(), func() bool {
		last = e.get(id)
		return last != nil && last.State == want
	})
	return last
}

// waitStarting enables the profile in the background and returns once the
// engine it asked for has been created. A stub with a startGate is then inside
// Start, which is what the caller wants to act on.
func (e *env) waitStarting(id string, stub *stubBackend) *stubEngine {
	e.t.Helper()
	go e.m.SetEnabled(id, true)
	var engine *stubEngine
	eventually(e.t, "the engine to be created", func() bool {
		engine = stub.engine(int(stub.created.Load()) - 1)
		return engine != nil
	})
	return engine
}

const (
	ovpnProfile = "client\nremote vpn.example.com 1194\nroute 10.1.0.0 255.255.0.0\n"
	wgProfile   = "[Interface]\nPrivateKey = k\nAddress = 10.6.0.2/32\nDNS = 10.6.0.1\n[Peer]\nEndpoint = 203.0.113.5:51820\nAllowedIPs = 0.0.0.0/0\n"
)

// recorder collects the events of one Watch.
type recorder struct {
	snapshot []*pb.Profile
	cancel   func()

	mu     sync.Mutex
	events []*pb.ProfileEvent
	closed bool
}

func (e *env) watch() *recorder {
	snapshot, events, cancel := e.m.Watch()
	r := &recorder{snapshot: snapshot, cancel: cancel}
	e.t.Cleanup(cancel)
	go func() {
		for ev := range events {
			r.mu.Lock()
			r.events = append(r.events, ev)
			r.mu.Unlock()
		}
		r.mu.Lock()
		r.closed = true
		r.mu.Unlock()
	}()
	return r
}

func (r *recorder) all() []*pb.ProfileEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

func (r *recorder) wasClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// states is the sequence of distinct states a profile went through.
func (r *recorder) states(id string) []pb.ProfileState {
	var out []pb.ProfileState
	for _, ev := range r.all() {
		if p := ev.GetChanged(); p != nil && p.Id == id && (len(out) == 0 || out[len(out)-1] != p.State) {
			out = append(out, p.State)
		}
	}
	return out
}

// stubBackend is a backend whose engines do what the test says. It accepts any
// content.
type stubBackend struct {
	kind tunnel.Kind
	// newErr, when set, makes New fail.
	newErr error
	// startErr, when set, makes Start fail.
	startErr error
	// startGate, when set, holds every Start until it is closed or the
	// engine's context ends.
	startGate chan struct{}
	// stopDelay is how long Stop takes; blockStop makes it wait for its context.
	stopDelay time.Duration
	blockStop bool
	// onEngine sees every engine that was created.
	onEngine func(*stubEngine)
	// onStop is called when an engine has stopped.
	onStop func()
	// unavailable, when set, is why Probe reports the backend as unavailable.
	unavailable string

	created atomic.Int32

	mu      sync.Mutex
	engines []*stubEngine
}

// engine returns the i-th engine that was created, or nil.
func (b *stubBackend) engine(i int) *stubEngine {
	b.mu.Lock()
	defer b.mu.Unlock()
	if i < 0 || i >= len(b.engines) {
		return nil
	}
	return b.engines[i]
}

func (b *stubBackend) backend() tunnel.Backend {
	return tunnel.Backend{
		Kind: b.kind,
		Parse: func(content []byte) (tunnel.Parsed, error) {
			return tunnel.Parsed{Content: content}, nil
		},
		New: func(spec tunnel.Spec, deps tunnel.Deps) (tunnel.Engine, error) {
			if b.newErr != nil {
				return nil, b.newErr
			}
			b.created.Add(1)
			eng := &stubEngine{b: b, spec: spec, deps: deps, status: make(chan tunnel.Status, 8)}
			if b.onEngine != nil {
				b.onEngine(eng)
			}
			b.mu.Lock()
			b.engines = append(b.engines, eng)
			b.mu.Unlock()
			return eng, nil
		},
		Probe: func() tunnel.EngineInfo {
			if b.unavailable != "" {
				return tunnel.EngineInfo{Detail: b.unavailable}
			}
			return tunnel.EngineInfo{Available: true, Version: "stub"}
		},
	}
}

type stubEngine struct {
	b      *stubBackend
	spec   tunnel.Spec
	deps   tunnel.Deps
	status chan tunnel.Status

	mu       sync.Mutex
	closed   bool
	started  atomic.Bool
	stopped  atomic.Bool
	rebinds  atomic.Int32
	provided chan credentialCall
}

type credentialCall struct {
	kind           tunnel.CredentialKind
	user, password string
}

func (s *stubEngine) Start(ctx context.Context) error {
	if gate := s.b.startGate; gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if s.b.startErr != nil {
		return s.b.startErr
	}
	s.started.Store(true)
	return nil
}

func (s *stubEngine) Stop(ctx context.Context) error {
	if s.b.blockStop {
		<-ctx.Done()
		return ctx.Err()
	}
	time.Sleep(s.b.stopDelay)
	s.stopped.Store(true)
	s.closeStatus()
	if s.b.onStop != nil {
		s.b.onStop()
	}
	return nil
}

func (s *stubEngine) Status() <-chan tunnel.Status { return s.status }
func (s *stubEngine) Rebind()                      { s.rebinds.Add(1) }

func (s *stubEngine) ProvideCredentials(kind tunnel.CredentialKind, user, pass string) error {
	if s.provided != nil {
		s.provided <- credentialCall{kind, user, pass}
	}
	return nil
}

func (s *stubEngine) send(st tunnel.Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.status <- st
	}
}

func (s *stubEngine) closeStatus() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.status)
	}
}

// orderRecorder notes in which order things happened.
type orderRecorder struct {
	mu     sync.Mutex
	events []string
}

func (o *orderRecorder) add(event string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, event)
}

// stoppedBeforeReconcilerEnded counts the engine stops that precede the end of
// the Reconciler.
func (o *orderRecorder) stoppedBeforeReconcilerEnded() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := 0
	for _, ev := range o.events {
		if ev == "reconciler ended" {
			break
		}
		if ev == "engine stopped" {
			n++
		}
	}
	return n
}

// recordingReconciler notes when its Run ended.
type recordingReconciler struct {
	tunnel.Reconciler
	order *orderRecorder
}

func (r *recordingReconciler) Run(ctx context.Context) error {
	err := r.Reconciler.Run(ctx)
	r.order.add("reconciler ended")
	return err
}

func (r *recordingReconciler) ended() bool {
	r.order.mu.Lock()
	defer r.order.mu.Unlock()
	return slices.Contains(r.order.events, "reconciler ended")
}

// exitingReconciler's Run returns without being asked to.
type exitingReconciler struct {
	tunnel.Reconciler
	err error
}

func (r *exitingReconciler) Run(context.Context) error { return r.err }

// assertNoSecretOnDisk fails if any file under dir contains one of the secrets.
func assertNoSecretOnDisk(t *testing.T, dir string, secrets ...string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, secret := range secrets {
			if strings.Contains(string(data), secret) {
				t.Errorf("%s contains %q", path, secret)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
