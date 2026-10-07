//go:build unix

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/manager"
	"github.com/KoukeNeko/Plaitway/internal/transport"
)

func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pw") // t.TempDir() is too long for sun_path on macOS
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// run is main without the flags and the signals: it must listen with the
// requested mode, create the socket directory, serve and clean up.
func TestRunServesAndStopsCleanly(t *testing.T) {
	dir := shortDir(t)
	socketDir := filepath.Join(dir, "run", "plaitway") // does not exist yet
	socket := filepath.Join(socketDir, "plaitwayd.sock")
	cfg := config{socket: socket, socketMode: 0o666, stateDir: filepath.Join(dir, "state"), fake: &fastFake}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, discardLog(), manager.NewLogBuffer(""), cfg, everyone()) }()
	stopped := false
	stop := func() error {
		if stopped {
			return nil
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(waitTimeout):
			return errors.New("run did not return")
		}
	}
	t.Cleanup(func() { stop() })

	// The socket appears before the daemon is ready; the first answer is the
	// proof that it is.
	conn, err := grpc.NewClient(transport.Target(socket), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	info, err := pb.NewDaemonServiceClient(conn).GetDaemonInfo(ctx, &pb.GetDaemonInfoRequest{}, grpc.WaitForReady(true))
	if err != nil || info.Version != version {
		t.Fatalf("GetDaemonInfo: %v, %v", info, err)
	}

	if fi, err := os.Stat(socketDir); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("socket directory: %v, %v; want mode 0755", fi, err)
	}
	if fi, err := os.Lstat(socket); err != nil || fi.Mode().Perm() != 0o666 {
		t.Errorf("socket: %v, %v; want mode 0666", fi, err)
	}
	if fi, err := os.Stat(cfg.stateDir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("state directory: %v, %v; want mode 0700", fi, err)
	}

	if err := stop(); err != nil {
		t.Fatalf("run returned %v", err)
	}
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatalf("the socket was left behind: %v", err)
	}
}

// The real wiring (macOS adapters, Reconciler, both engines) starts without
// root. It only reads the routing table and the resolver keys until a tunnel is
// brought up, so this touches nothing on the host.
func TestRunWithTheRealEnginesStartsWithoutRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the real wiring is not exercised as root")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("the real engines are only linked on macOS")
	}
	dir := shortDir(t)
	socket := filepath.Join(dir, "d.sock")
	missing := filepath.Join(dir, "no-such-openvpn")
	cfg := config{socket: socket, socketMode: 0o600, stateDir: filepath.Join(dir, "state"), runDir: filepath.Join(dir, "run"), openvpn: missing}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, discardLog(), manager.NewLogBuffer(""), cfg, everyone()) }()
	t.Cleanup(func() { cancel(); <-done })

	conn, err := grpc.NewClient(transport.Target(socket), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := pb.NewDaemonServiceClient(conn)
	info, err := client.GetDaemonInfo(ctx, &pb.GetDaemonInfoRequest{}, grpc.WaitForReady(true))
	if err != nil {
		t.Fatalf("GetDaemonInfo: %v", err)
	}
	if info.Privileged {
		t.Error("an unprivileged daemon claims to be privileged")
	}
	engines := map[pb.ProfileKind]*pb.EngineInfo{}
	for _, e := range info.Engines {
		engines[e.Kind] = e
	}
	if e := engines[pb.ProfileKind_PROFILE_KIND_WIREGUARD]; e == nil || !e.Available {
		t.Errorf("WireGuard engine: %v, want available (it is embedded)", e)
	}
	if e := engines[pb.ProfileKind_PROFILE_KIND_OPENVPN]; e == nil || e.Available || e.Detail == "" {
		t.Errorf("OpenVPN engine: %v, want unavailable with a reason for a missing binary", e)
	}

	// The Reconciler read this Mac's network through the real adapters.
	diag, err := client.GetDiagnostics(ctx, &pb.GetDiagnosticsRequest{})
	if err != nil {
		t.Fatalf("GetDiagnostics: %v", err)
	}
	if len(diag.Network.Interfaces) == 0 {
		t.Errorf("diagnostics lists no network interfaces: %v", diag.Network)
	}
	// Resolver entries are not checked: the list is every entry on the host that
	// carries Plaitway's marker, so a helper running on the machine that runs the
	// tests shows up in it.
	if len(diag.OwnedRoutes) != 0 {
		t.Errorf("a daemon with no tunnels owns routes %v", diag.OwnedRoutes)
	}
}

// A build that knows the hash of its openvpn does not run any other binary: the
// OpenVPN engine says why it is unavailable, and nothing is copied.
func TestRunWithTheRealEnginesRefusesAnOpenVPNThatIsNotTheBuiltOne(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the real wiring is not exercised as root")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("the real engines are only linked on macOS")
	}
	defer func(old string) { openvpnSHA256 = old }(openvpnSHA256)
	openvpnSHA256 = sha256Hex("the openvpn that was built")

	dir := shortDir(t)
	other := filepath.Join(dir, "openvpn")
	if err := os.WriteFile(other, []byte(fakeOpenVPN), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config{socket: filepath.Join(dir, "d.sock"), socketMode: 0o600, stateDir: filepath.Join(dir, "state"), runDir: filepath.Join(dir, "run"), openvpn: other}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, discardLog(), manager.NewLogBuffer(""), cfg, everyone()) }()
	t.Cleanup(func() { cancel(); <-done })

	conn, err := grpc.NewClient(transport.Target(cfg.socket), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	info, err := pb.NewDaemonServiceClient(conn).GetDaemonInfo(ctx, &pb.GetDaemonInfoRequest{}, grpc.WaitForReady(true))
	if err != nil {
		t.Fatalf("GetDaemonInfo: %v", err)
	}
	for _, e := range info.Engines {
		switch e.Kind {
		case pb.ProfileKind_PROFILE_KIND_OPENVPN:
			if e.Available || !strings.Contains(e.Detail, "not the binary this daemon was built for") {
				t.Errorf("OpenVPN engine: %v, want it unavailable because the binary is not the built one", e)
			}
		case pb.ProfileKind_PROFILE_KIND_WIREGUARD:
			if !e.Available {
				t.Errorf("WireGuard engine: %v, want it available", e)
			}
		}
	}
	if _, err := os.Lstat(filepath.Join(cfg.runDir, "openvpn")); !os.IsNotExist(err) {
		t.Errorf("the rejected binary was installed: %v", err)
	}
}

func TestRunRefusesALiveSocket(t *testing.T) {
	dir := shortDir(t)
	socket := filepath.Join(dir, "d.sock")
	cfg := config{socket: socket, socketMode: 0o600, stateDir: filepath.Join(dir, "state"), fake: &fastFake}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, discardLog(), manager.NewLogBuffer(""), cfg, everyone()) }()
	t.Cleanup(func() { cancel(); <-done })
	eventually(t, "the first daemon", func() bool { _, err := os.Lstat(socket); return err == nil })

	second := cfg
	second.stateDir = filepath.Join(dir, "state2")
	if err := run(ctx, discardLog(), manager.NewLogBuffer(""), second, everyone()); err == nil {
		t.Fatal("a second daemon started on a live socket")
	}
}
