package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/manager/fake"
	"github.com/KoukeNeko/Plaitway/internal/peercred"
	"github.com/KoukeNeko/Plaitway/internal/transport"
)

const waitTimeout = 5 * time.Second

var fastFake = fake.Config{ConnectDelay: 20 * time.Millisecond, StatsInterval: 10 * time.Millisecond}

const (
	ovpnProfile = "client\nremote vpn.example.com 1194\nroute 10.1.0.0 255.255.0.0\n"
	wgProfile   = "[Interface]\nPrivateKey = k\nAddress = 10.6.0.2/32\nDNS = 10.6.0.1\n[Peer]\nEndpoint = 203.0.113.5:51820\nAllowedIPs = 0.0.0.0/0\n"
)

// caller is who the test process is to the daemon under test: one of the three
// roles of the authorization matrix (everyone, consoleUserOnly, stranger),
// which each OS builds in its own way in harness_unix_test.go and
// harness_windows_test.go.
type caller struct {
	pol *policy
	// rewrite changes the Windows identity the real handshake read, before the
	// interceptor sees it. It is nil where the policy alone makes the role.
	rewrite func(*peercred.WindowsIdentity)
}

// roleCredentials are the real peer credentials, handshake included, that then
// hand the interceptor the identity a test role asks for. On Windows the
// identity is the token of whoever runs the tests (an elevated administrator, a
// filtered one or a plain user), so a policy parameter cannot make the test
// process a stranger; the SID, the session and the pid stay the real ones.
type roleCredentials struct {
	credentials.TransportCredentials
	rewrite func(*peercred.WindowsIdentity)
}

func (c roleCredentials) ServerHandshake(conn net.Conn) (net.Conn, credentials.AuthInfo, error) {
	conn, info, err := c.TransportCredentials.ServerHandshake(conn)
	if err != nil {
		return conn, info, err
	}
	if ai, ok := info.(peercred.AuthInfo); ok && ai.Windows != nil {
		identity := *ai.Windows
		c.rewrite(&identity)
		ai.Windows = &identity
		return conn, ai, nil
	}
	return conn, info, nil
}

// Clone must keep the rewrite: the embedded Clone would hand back the plain
// credentials.
func (c roleCredentials) Clone() credentials.TransportCredentials {
	return roleCredentials{TransportCredentials: c.TransportCredentials.Clone(), rewrite: c.rewrite}
}

// credentials are the real ones unless the role rewrites the identity.
func (c caller) credentials() credentials.TransportCredentials {
	peerCredentials := peercred.NewServerCredentials()
	if c.rewrite == nil {
		return peerCredentials
	}
	return roleCredentials{TransportCredentials: peerCredentials, rewrite: c.rewrite}
}

// harness runs the real daemon wiring (peer credentials, interceptors, store,
// manager, fake backend) on a Unix socket, or a named pipe on Windows, and gives
// tests a generated client.
type harness struct {
	t        *testing.T
	stateDir string
	socket   string
	d        *daemon
	client   pb.DaemonServiceClient
	conn     *grpc.ClientConn

	cancel context.CancelFunc
	served chan error
	once   sync.Once
	err    error
}

// startDaemon starts a daemon on stateDir, which may hold what an earlier
// daemon left.
func startDaemon(t *testing.T, stateDir string, who caller) *harness {
	t.Helper()
	return startLoggingDaemon(t, stateDir, who, io.Discard, slog.LevelInfo)
}

// startLoggingDaemon is startDaemon with the daemon's log going to logOut at level.
func startLoggingDaemon(t *testing.T, stateDir string, who caller, logOut io.Writer, level slog.Level) *harness {
	t.Helper()
	socket := newSocketPath(t)

	lis, err := transport.Listen(socket, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	log, daemonLog := newLogger(logOut, level)
	d, err := newDaemonWithCredentials(log, daemonLog, config{socket: socket, stateDir: stateDir, fake: &fastFake}, who.pol, who.credentials())
	if err != nil {
		lis.Close()
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{t: t, stateDir: stateDir, socket: socket, d: d, cancel: cancel, served: make(chan error, 1)}
	go func() { h.served <- d.serve(ctx, lis) }()

	conn, err := grpc.NewClient(transport.Target(socket),
		append(transport.DialOptions(), grpc.WithTransportCredentials(insecure.NewCredentials()))...)
	if err != nil {
		t.Fatal(err)
	}
	h.conn = conn
	h.client = pb.NewDaemonServiceClient(conn)
	t.Cleanup(func() { h.stop() })
	return h
}

func newDaemonHarness(t *testing.T) *harness { return startDaemon(t, t.TempDir(), everyone()) }

// stop shuts the daemon down the way a SIGTERM does and returns what serve returned.
func (h *harness) stop() error {
	h.once.Do(func() {
		h.conn.Close()
		h.cancel()
		select {
		case h.err = <-h.served:
		case <-time.After(waitTimeout):
			h.t.Error("the daemon did not stop in time")
		}
	})
	return h.err
}

func (h *harness) importProfile(name, content string) *pb.Profile {
	h.t.Helper()
	resp, err := h.client.ImportProfile(context.Background(), &pb.ImportProfileRequest{Name: name, Content: []byte(content)})
	if err != nil {
		h.t.Fatalf("ImportProfile(%s): %v", name, err)
	}
	return resp.Profile
}

func (h *harness) setEnabled(id string, enabled bool) *pb.Profile {
	h.t.Helper()
	p, err := h.client.SetProfileEnabled(context.Background(), &pb.SetProfileEnabledRequest{Id: id, Enabled: enabled})
	if err != nil {
		h.t.Fatalf("SetProfileEnabled(%s, %v): %v", id, enabled, err)
	}
	return p
}

func (h *harness) list() []*pb.Profile {
	h.t.Helper()
	resp, err := h.client.ListProfiles(context.Background(), &pb.ListProfilesRequest{})
	if err != nil {
		h.t.Fatalf("ListProfiles: %v", err)
	}
	return resp.Profiles
}

func (h *harness) get(id string) *pb.Profile {
	for _, p := range h.list() {
		if p.Id == id {
			return p
		}
	}
	return nil
}

func (h *harness) waitState(id string, want pb.ProfileState) *pb.Profile {
	h.t.Helper()
	var last *pb.Profile
	eventually(h.t, id+" to be "+want.String(), func() bool {
		last = h.get(id)
		return last != nil && last.State == want
	})
	return last
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func names(profiles []*pb.Profile) []string {
	var out []string
	for _, p := range profiles {
		out = append(out, p.Name)
	}
	return out
}

// watcher follows one WatchProfiles stream. Events of different profiles
// arrive in no fixed order, so it keeps what each profile went through.
type watcher struct {
	snapshot []*pb.Profile
	cancel   context.CancelFunc

	mu      sync.Mutex
	events  []*pb.ProfileEvent
	history map[string][]pb.ProfileState
	err     error
}

// watch returns once the snapshot has arrived, which is also the point where
// the daemon has registered the subscriber.
func (h *harness) watch() *watcher {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h.t.Cleanup(cancel)
	stream, err := h.client.WatchProfiles(ctx, &pb.WatchProfilesRequest{})
	if err != nil {
		h.t.Fatal(err)
	}
	first, err := stream.Recv()
	if err != nil {
		h.t.Fatal(err)
	}
	if first.GetSnapshot() == nil {
		h.t.Fatalf("first watch message is not a snapshot: %v", first)
	}
	w := &watcher{snapshot: first.GetSnapshot().Profiles, cancel: cancel, history: map[string][]pb.ProfileState{}}
	go func() {
		for {
			ev, err := stream.Recv()
			w.mu.Lock()
			if err != nil {
				w.err = err
				w.mu.Unlock()
				return
			}
			w.events = append(w.events, ev)
			if p := ev.GetChanged(); p != nil {
				if hist := w.history[p.Id]; len(hist) == 0 || hist[len(hist)-1] != p.State {
					w.history[p.Id] = append(hist, p.State)
				}
			}
			w.mu.Unlock()
		}
	}()
	return w
}

func (w *watcher) allEvents() []*pb.ProfileEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.events)
}

func (w *watcher) states(id string) []pb.ProfileState {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.history[id])
}

func (w *watcher) streamErr() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}
