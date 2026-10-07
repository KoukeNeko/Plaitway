// Command smoketest calls GetDaemonInfo on a daemon socket and prints the
// answer as key=value lines. scripts/verify-bundle.sh runs it against the
// packaged plaitwayd.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/transport"
)

func main() {
	socket := flag.String("socket", transport.DefaultPath(), "daemon socket")
	timeout := flag.Duration("timeout", 10*time.Second, "how long to wait for the daemon to answer")
	flag.Parse()

	if err := run(*socket, *timeout); err != nil {
		fmt.Fprintln(os.Stderr, "smoketest:", err)
		os.Exit(1)
	}
}

func run(socket string, timeout time.Duration) error {
	opts := append([]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}, transport.DialOptions()...)
	conn, err := grpc.NewClient(transport.Target(socket), opts...)
	if err != nil {
		return err
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	// WaitForReady rides out the daemon still starting up.
	info, err := pb.NewDaemonServiceClient(conn).GetDaemonInfo(ctx, &pb.GetDaemonInfoRequest{}, grpc.WaitForReady(true))
	if err != nil {
		return fmt.Errorf("GetDaemonInfo on %s: %w", socket, err)
	}
	fmt.Printf("version=%s\nprotocol_version=%d\nprivileged=%t\n", info.GetVersion(), info.GetProtocolVersion(), info.GetPrivileged())
	for _, e := range info.GetEngines() {
		fmt.Printf("engine=%s available=%t version=%s detail=%s\n", e.GetKind(), e.GetAvailable(), e.GetVersion(), e.GetDetail())
	}
	return nil
}
