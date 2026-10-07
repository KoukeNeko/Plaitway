package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	"google.golang.org/grpc"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/manager"
	"github.com/KoukeNeko/Plaitway/internal/manager/fake"
	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/peercred"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

const (
	// serverStopTimeout is how long open RPCs get to finish when stopping.
	serverStopTimeout = 2 * time.Second
	// shutdownTimeout bounds stopping the engines and the Reconciler. launchd
	// kills a daemon that needs more than 20 seconds.
	shutdownTimeout = 15 * time.Second
)

type config struct {
	socket     string
	socketMode os.FileMode
	stateDir   string
	runDir     string
	logFile    string
	openvpn    string
	// fake, when set, runs the daemon on the in-memory backend and Reconciler
	// instead of the real engines.
	fake *fake.Config
}

// daemon is the daemon without its listener and signal handling, so that the
// tests run the same wiring as main.
type daemon struct {
	log *slog.Logger
	mgr *manager.Manager
	svc *service
	srv *grpc.Server
}

// newDaemon opens the state directory and builds the server. It does not
// listen and starts no engine.
func newDaemon(log *slog.Logger, daemonLog *manager.LogBuffer, cfg config, pol *policy) (*daemon, error) {
	backends, rec, netMonitor, err := selectEngines(log, cfg)
	if err != nil {
		return nil, err
	}
	mgr, err := manager.New(manager.Config{
		Log:        log,
		StateDir:   cfg.stateDir,
		Backends:   backends,
		Reconciler: rec,
		Net:        netMonitor,
		DaemonLog:  daemonLog,
		Version:    version,
		// The fake engines need no privileges, so the fake daemon does not
		// claim to lack them.
		Privileged: cfg.fake != nil || os.Geteuid() == 0,
	})
	if err != nil {
		return nil, fmt.Errorf("start profile manager: %w", err)
	}
	svc := newService(log, mgr)
	return &daemon{log: log, mgr: mgr, svc: svc, srv: newServer(log, svc, pol)}, nil
}

func selectEngines(log *slog.Logger, cfg config) ([]tunnel.Backend, tunnel.Reconciler, osnet.NetMonitor, error) {
	if cfg.fake != nil {
		rec := fake.NewReconciler()
		return fake.Backends(*cfg.fake), rec, rec, nil
	}
	openvpn, openvpnErr := trustedOpenVPN(cfg.openvpn, cfg.runDir)
	if openvpnErr != nil {
		log.Error("openvpn will not be used", "err", openvpnErr)
	}
	backends, rec, netMonitor, err := wireReal(realConfig{log: log, openvpn: openvpn, runDir: cfg.runDir, stateDir: cfg.stateDir})
	if err != nil {
		return nil, nil, nil, err
	}
	if openvpnErr != nil {
		backends = openvpnUnavailable(backends, openvpnErr)
	}
	return backends, rec, netMonitor, nil
}

// newServer wires the peer-identity credentials and the per-call authorization
// into a gRPC server; main and the tests build the daemon the same way.
func newServer(log *slog.Logger, svc *service, pol *policy) *grpc.Server {
	srv := grpc.NewServer(
		grpc.Creds(peercred.NewServerCredentials()),
		grpc.UnaryInterceptor(pol.unaryInterceptor(log)),
		grpc.StreamInterceptor(pol.streamInterceptor(log)),
	)
	pb.RegisterDaemonServiceServer(srv, svc)
	return srv
}

// serve runs the daemon on lis until ctx ends, the listener fails or the
// Reconciler stops, and then shuts down in order: end the watch streams, stop
// the server, stop every engine, end the Reconciler.
func (d *daemon) serve(ctx context.Context, lis net.Listener) error {
	// Start returns at once: the auto_connect profiles are CONNECTING when the
	// first call is answered, and their engines start in the background.
	d.mgr.Start()
	served := make(chan error, 1)
	go func() { served <- d.srv.Serve(lis) }()
	// The on-demand profiles follow the network from the moment clients can ask.
	d.mgr.StartOnDemand()

	var cause error
	serverEnded := false
	select {
	case <-ctx.Done():
		d.log.Info("stopping")
	case cause = <-d.mgr.Fatal():
		d.log.Error("stopping because the Reconciler stopped", "err", cause)
	case err := <-served:
		serverEnded = true
		cause = errors.New("the server stopped on its own")
		if err != nil {
			cause = fmt.Errorf("the server failed: %w", err)
		}
		d.log.Error("stopping", "err", cause)
	}

	d.svc.beginShutdown()
	d.stopServer()
	if !serverEnded {
		if err := <-served; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			cause = errors.Join(cause, err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := d.mgr.Shutdown(shutdownCtx); err != nil {
		cause = errors.Join(cause, fmt.Errorf("shut down engines: %w", err))
	}
	return cause
}

func (d *daemon) stopServer() {
	done := make(chan struct{})
	go func() {
		d.srv.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(serverStopTimeout):
		d.srv.Stop()
	}
}
