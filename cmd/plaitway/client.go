package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/transport"
)

// rpcTimeout bounds the calls that return one answer, which the daemon gives
// within moments; stopping an engine is the slowest (internal/manager waits at
// most ten seconds for it).
const rpcTimeout = 30 * time.Second

type client struct {
	api    pb.DaemonServiceClient
	conn   *grpc.ClientConn
	socket string
}

// dial prepares the connection to the daemon. gRPC connects with the first
// call, so a daemon that is not running shows up there; see failure.
func (a *app) dial() (*client, error) {
	// The path is checked as the person gave it: after filepath.Abs a Unix-style
	// value on Windows would be reported with a drive letter nobody typed.
	requested := a.socketPath()
	if err := checkSocketPath(requested); err != nil {
		return nil, err
	}
	// "unix://" plus a relative path would name a host, not a file.
	socket, err := filepath.Abs(requested)
	if err != nil {
		return nil, err
	}
	conn, err := grpc.NewClient(transport.Target(socket),
		append(transport.DialOptions(), grpc.WithTransportCredentials(insecure.NewCredentials()))...)
	if err != nil {
		return nil, fmt.Errorf("socket %s: %w", socket, err)
	}
	return &client{api: pb.NewDaemonServiceClient(conn), conn: conn, socket: socket}, nil
}

func (c *client) close() { _ = c.conn.Close() }

// failure words a gRPC error for the person at the terminal. The daemon's own
// text is kept where it says why: a refusal or a rejected input.
func (c *client) failure(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	msg := st.Message()
	switch st.Code() {
	case codes.Unavailable:
		if reason, ok := dialFailure(msg); ok {
			if detail, refused := refusal(reason); refused {
				return fmt.Errorf("refusing to use %s: %s", c.socket, detail)
			}
			switch classifyDialFailure(reason) {
			case socketMissing:
				return fmt.Errorf("the daemon is not running: %s does not exist%s", c.socket, notRunningHint(c.socket))
			case nobodyListens:
				return fmt.Errorf("the daemon is not running: nothing listens on %s%s", c.socket, notRunningHint(c.socket))
			case accessDenied:
				return fmt.Errorf("permission denied to open %s", c.socket)
			}
			return fmt.Errorf("cannot connect to %s: %s", c.socket, reason)
		}
		return fmt.Errorf("the daemon is unavailable: %s", msg)
	case codes.PermissionDenied:
		return fmt.Errorf("permission denied: %s", msg)
	case codes.NotFound:
		return fmt.Errorf("not found: %s", msg)
	case codes.InvalidArgument, codes.FailedPrecondition:
		return errors.New(msg)
	case codes.DeadlineExceeded:
		return errors.New("the daemon did not answer in time")
	}
	return fmt.Errorf("%s (%s)", msg, st.Code())
}

// dialProblem is why the daemon cannot be reached, as far as a person can do
// something about it.
type dialProblem int

const (
	otherProblem dialProblem = iota
	// socketMissing: the daemon's socket, or the directory it is in, is not there.
	socketMissing
	// nobodyListens: the socket is there, but no daemon is behind it.
	nobodyListens
	// accessDenied: the daemon does not let this user connect.
	accessDenied
)

func callContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, rpcTimeout)
}

// profiles asks the daemon for every profile.
func (c *client) profiles(ctx context.Context) ([]*pb.Profile, error) {
	ctx, cancel := callContext(ctx)
	defer cancel()
	resp, err := c.api.ListProfiles(ctx, &pb.ListProfilesRequest{})
	if err != nil {
		return nil, c.failure(err)
	}
	return resp.Profiles, nil
}

// find is the profile a command line names.
func (c *client) find(ctx context.Context, query string) (*pb.Profile, error) {
	profiles, err := c.profiles(ctx)
	if err != nil {
		return nil, err
	}
	return matchProfile(profiles, query)
}

// matchProfile takes an id, else a name without regard to case. Names are not
// unique, so a name that fits several profiles is an error that lists them.
func matchProfile(profiles []*pb.Profile, query string) (*pb.Profile, error) {
	for _, p := range profiles {
		if p.Id == query {
			return p, nil
		}
	}
	var named []*pb.Profile
	for _, p := range profiles {
		if strings.EqualFold(p.Name, query) {
			named = append(named, p)
		}
	}
	switch len(named) {
	case 0:
		return nil, fmt.Errorf("no profile named %q; \"plaitway list\" shows them", query)
	case 1:
		return named[0], nil
	}
	choices := make([]string, len(named))
	for i, p := range named {
		choices[i] = fmt.Sprintf("%s (%s)", p.Id, p.Name)
	}
	return nil, fmt.Errorf("%q fits %d profiles, use an id: %s", query, len(named), strings.Join(choices, ", "))
}
