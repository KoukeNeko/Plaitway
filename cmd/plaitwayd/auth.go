package main

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/peercred"
)

const (
	// adminGroupGID is the macOS "admin" group.
	adminGroupGID = 80

	consoleDevice  = "/dev/console"
	consoleRefresh = time.Second
)

// accessLevel is what a caller has to be to make a call. It follows the policy
// in the header of proto/plaitway/v1/plaitway.proto.
type accessLevel uint8

const (
	// levelConnect: read state and logs, connect and disconnect, answer
	// credential requests. The console user, administrators and root.
	levelConnect accessLevel = iota + 1
	// levelModify: change which profiles exist and how they are set up.
	// Administrators and root.
	levelModify
)

// methodLevels has an entry for every RPC. A method missing here is refused,
// so a new RPC cannot be reachable before someone decided who may call it.
var methodLevels = map[string]accessLevel{
	pb.DaemonService_GetDaemonInfo_FullMethodName:      levelConnect,
	pb.DaemonService_GetDiagnostics_FullMethodName:     levelConnect,
	pb.DaemonService_ListProfiles_FullMethodName:       levelConnect,
	pb.DaemonService_WatchProfiles_FullMethodName:      levelConnect,
	pb.DaemonService_WatchLogs_FullMethodName:          levelConnect,
	pb.DaemonService_SetProfileEnabled_FullMethodName:  levelConnect,
	pb.DaemonService_ProvideCredentials_FullMethodName: levelConnect,
	pb.DaemonService_Resync_FullMethodName:             levelConnect,

	pb.DaemonService_ImportProfile_FullMethodName:        levelModify,
	pb.DaemonService_UpdateProfile_FullMethodName:        levelModify,
	pb.DaemonService_UpdateProfileContent_FullMethodName: levelModify,
	pb.DaemonService_DeleteProfile_FullMethodName:        levelModify,
	pb.DaemonService_ReorderProfiles_FullMethodName:      levelModify,
	pb.DaemonService_RemoveStaleRoute_FullMethodName:     levelModify,
	// The stored text holds private keys, so reading it is a modify call too.
	pb.DaemonService_GetProfileContent_FullMethodName: levelModify,
}

// policy decides per call from the peer's uid and groups, which the kernel
// reports for the process that connected.
type policy struct {
	adminGID uint32
	// consoleUID returns the user at the console.
	consoleUID func() (uint32, error)
}

func newPolicy() *policy {
	return &policy{adminGID: adminGroupGID, consoleUID: newConsoleUser().uid}
}

// check fails closed: an identity the OS could not tell is refused.
func (p *policy) check(ai peercred.AuthInfo, fullMethod string) error {
	level, ok := methodLevels[fullMethod]
	if !ok {
		return status.Errorf(codes.PermissionDenied, "no access policy for %s", fullMethod)
	}
	if !ai.Known {
		return status.Error(codes.PermissionDenied, "peer identity unavailable")
	}
	if ai.UID == 0 || slices.Contains(ai.Groups, p.adminGID) {
		return nil
	}
	if level == levelModify {
		return status.Errorf(codes.PermissionDenied, "uid %d is not an administrator, which %s requires", ai.UID, fullMethod)
	}
	console, err := p.consoleUID()
	if err != nil {
		return status.Errorf(codes.PermissionDenied, "uid %d is not an administrator and the console user is unknown: %v", ai.UID, err)
	}
	if ai.UID != console {
		return status.Errorf(codes.PermissionDenied, "uid %d is neither the console user nor an administrator", ai.UID)
	}
	return nil
}

// authorize is the only place where the identity in a request's context is
// turned into a decision.
func (p *policy) authorize(ctx context.Context, fullMethod string) (peercred.AuthInfo, error) {
	ai, ok := peercred.FromContext(ctx)
	if !ok {
		return ai, status.Error(codes.PermissionDenied, "peer identity unavailable")
	}
	return ai, p.check(ai, fullMethod)
}

// consoleUser reads the owner of /dev/console, which is the user at the
// keyboard, at most once per refresh interval: the answer is needed on every
// call but changes only at login and logout.
type consoleUser struct {
	stat func() (uint32, error)
	now  func() time.Time

	mu      sync.Mutex
	readAt  time.Time
	cached  uint32
	cachedE error
}

func newConsoleUser() *consoleUser {
	return &consoleUser{stat: func() (uint32, error) { return fileOwner(consoleDevice) }, now: time.Now}
}

func (c *consoleUser) uid() (uint32, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if now := c.now(); c.readAt.IsZero() || now.Sub(c.readAt) >= consoleRefresh {
		c.cached, c.cachedE = c.stat()
		c.readAt = now
	}
	return c.cached, c.cachedE
}

// readOnlyMethods are the calls that change nothing. The menu bar app makes
// them all the time (the Diagnostics page polls every two seconds), so their
// successes are logged at debug: at info they would fill the log file, which is
// only rotated when the daemon starts, and the Helper log tab with lines that
// say nothing. It is not an access policy, which methodLevels is.
var readOnlyMethods = map[string]bool{
	pb.DaemonService_GetDaemonInfo_FullMethodName:  true,
	pb.DaemonService_GetDiagnostics_FullMethodName: true,
	pb.DaemonService_ListProfiles_FullMethodName:   true,
	pb.DaemonService_WatchProfiles_FullMethodName:  true,
	pb.DaemonService_WatchLogs_FullMethodName:      true,
}

// logCall records who called what and how it ended; it never sees a request
// body. The message of an error is logged only for failures of the daemon
// itself: for the others it may quote the caller's input.
func logCall(log *slog.Logger, method string, ai peercred.AuthInfo, started time.Time, err error) {
	code := status.Code(err)
	attrs := []any{"method", method, "uid", ai.UID, "pid", ai.PID, "code", code.String(), "dur", time.Since(started).Round(time.Millisecond)}
	switch code {
	case codes.OK, codes.Canceled:
		if readOnlyMethods[method] {
			log.Debug("rpc", attrs...)
		} else {
			log.Info("rpc", attrs...)
		}
	case codes.Internal, codes.Unknown:
		log.Error("rpc", append(attrs, "err", status.Convert(err).Message())...)
	default:
		log.Warn("rpc", attrs...)
	}
}

func (p *policy) unaryInterceptor(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		started := time.Now()
		ai, err := p.authorize(ctx, info.FullMethod)
		if err != nil {
			logCall(log, info.FullMethod, ai, started, err)
			return nil, err
		}
		resp, err := h(ctx, req)
		logCall(log, info.FullMethod, ai, started, err)
		return resp, err
	}
}

func (p *policy) streamInterceptor(log *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) error {
		started := time.Now()
		ai, err := p.authorize(ss.Context(), info.FullMethod)
		if err != nil {
			logCall(log, info.FullMethod, ai, started, err)
			return err
		}
		err = h(srv, ss)
		logCall(log, info.FullMethod, ai, started, err)
		return err
	}
}
