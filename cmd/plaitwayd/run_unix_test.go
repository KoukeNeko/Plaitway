//go:build unix

package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/manager"
	"github.com/KoukeNeko/Plaitway/internal/transport"
)

// The real wiring (macOS or Linux adapters, Reconciler, both engines) starts
// without root. It only reads the routing table and the resolver keys until a
// tunnel is brought up, so this touches nothing on the host.
func TestRunWithTheRealEnginesStartsWithoutRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the real wiring is not exercised as root")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("the real engines are only linked on macOS and Linux")
	}
	dir := shortDir(t)
	socket := filepath.Join(dir, "d.sock")
	missing := filepath.Join(dir, "no-such-openvpn")
	cfg := config{socket: socket, socketMode: 0o600, stateDir: filepath.Join(dir, "state"), runDir: filepath.Join(dir, "run"), openvpn: missing}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, discardLog(), manager.NewLogBuffer(""), cfg, everyone().pol) }()
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

	// The Reconciler read this machine's network through the real adapters.
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
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("the real engines are only linked on macOS and Linux")
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
	go func() { done <- run(ctx, discardLog(), manager.NewLogBuffer(""), cfg, everyone().pol) }()
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
