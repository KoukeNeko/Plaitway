package main

import (
	"context"
	"errors"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/transport"
)

// failureOfDial is what the client says when the dialer of gRPC fails with err:
// the error goes through gRPC, which words the description the way a real dial
// failure is worded.
func failureOfDial(t *testing.T, socket string, dialErr error) error {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///"+socket,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return nil, dialErr }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	c := &client{api: pb.NewDaemonServiceClient(conn), conn: conn, socket: socket}

	ctx, cancel := callContext(context.Background())
	defer cancel()
	_, err = c.profiles(ctx)
	if err == nil {
		t.Fatal("the dial failed but the call succeeded")
	}
	return err
}

func TestRefusedPipeServerIsWordedWithItsOwner(t *testing.T) {
	t.Parallel()
	const owner = `NT AUTHORITY\NETWORK SERVICE (S-1-5-20), not by SYSTEM, Administrators or HOST\user (S-1-5-21-1-2-3-1001)`
	for name, c := range map[string]struct{ dialErr, want string }{
		"another owner": {
			transport.RefusedServerPrefix + "it is owned by " + owner,
			`refusing to use \\.\pipe\plaitway: it is owned by ` + owner,
		},
		"owner not readable": {
			transport.RefusedServerPrefix + "cannot read its owner: GetSecurityInfo: Access is denied.",
			`refusing to use \\.\pipe\plaitway: cannot read its owner: GetSecurityInfo: Access is denied.`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := failureOfDial(t, `\\.\pipe\plaitway`, errors.New(c.dialErr))
			if err.Error() != c.want {
				t.Errorf("got %q\nwant %q", err, c.want)
			}
		})
	}
}

func TestDialFailureOfAPipeIsClassifiedByItsReason(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		dialErr string
		want    string
	}{
		"missing":       {`open \\.\pipe\plaitway: The system cannot find the file specified.`, `the daemon is not running: \\.\pipe\plaitway does not exist`},
		"access denied": {`open \\.\pipe\plaitway: Access is denied.`, `permission denied to open \\.\pipe\plaitway`},
	} {
		t.Run(name, func(t *testing.T) {
			if err := failureOfDial(t, `\\.\pipe\plaitway`, errors.New(c.dialErr)); err.Error() != c.want {
				t.Errorf("got %q, want %q", err, c.want)
			}
		})
	}
}
