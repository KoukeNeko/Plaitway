package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

// startRun runs the daemon the way main does, with the real peer credentials,
// and returns a stop that waits for run to return. The first answer of the
// daemon is the proof that it is ready: the socket appears before that. The
// test process is whoever runs the tests, which pol must let connect.
func startRun(t *testing.T, cfg config, pol *policy) (client pb.DaemonServiceClient, stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, discardLog(), manager.NewLogBuffer(""), cfg, pol) }()
	stopped := false
	stop = func() error {
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

	conn, err := grpc.NewClient(transport.Target(cfg.socket),
		append(transport.DialOptions(), grpc.WithTransportCredentials(insecure.NewCredentials()))...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	client = pb.NewDaemonServiceClient(conn)
	info, err := client.GetDaemonInfo(ctx, &pb.GetDaemonInfoRequest{}, grpc.WaitForReady(true))
	if err != nil || info.Version != version {
		t.Fatalf("GetDaemonInfo: %v, %v", info, err)
	}
	return client, stop
}

// run is main without the flags and the signals: it must listen with the
// requested mode, create the socket directory, serve and clean up.
func TestRunServesAndStopsCleanly(t *testing.T) {
	dir := shortDir(t)
	cfg := config{socket: nestedSocketPath(dir), socketMode: 0o666, stateDir: filepath.Join(dir, "state"), fake: &fastFake}

	_, stop := startRun(t, cfg, everyone().pol)
	assertRunFootprint(t, cfg)

	if err := stop(); err != nil {
		t.Fatalf("run returned %v", err)
	}
	socketGone(t, cfg.socket)
}

// A second daemon must not take over the address of a running one, and the
// first must keep serving.
func TestRunRefusesALiveSocket(t *testing.T) {
	dir := shortDir(t)
	cfg := config{socket: newSocketPath(t), socketMode: 0o600, stateDir: filepath.Join(dir, "state"), fake: &fastFake}
	client, _ := startRun(t, cfg, everyone().pol)

	// A second daemon that did start would serve until the context ends, so the
	// timeout turns that into a failure instead of a hang.
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	second := cfg
	second.stateDir = filepath.Join(dir, "state2")
	if err := run(ctx, discardLog(), manager.NewLogBuffer(""), second, everyone().pol); err == nil {
		t.Fatal("a second daemon started on a live socket")
	}

	if _, err := client.GetDaemonInfo(context.Background(), &pb.GetDaemonInfoRequest{}); err != nil {
		t.Fatalf("the first daemon stopped answering: %v", err)
	}
}
