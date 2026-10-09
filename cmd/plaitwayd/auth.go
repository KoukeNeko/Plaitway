package main

import (
	"context"
	"fmt"
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

// consoleRefresh is how long the answer about who is at the console is kept.
const consoleRefresh = time.Second

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

// policy decides per call from the peer's identity, which the OS reports for the
// process that connected: uid and groups on Unix, the token on Windows. Each OS
// builds it in newPolicy.
type policy struct {
	// administrator says whether a Unix caller other than root is an
	// administrator: a member of the administrators' group of this OS. The error
	// is for a caller whose groups could not be read, who then counts as none.
	administrator func(peercred.AuthInfo) (bool, error)
	// consoleUIDs returns the users at the console.
	consoleUIDs func() ([]uint32, error)
	// consoleSession returns the logon session at the console, which is what
	// stands for "the user at the console" on Windows.
	consoleSession func() (uint32, error)
}

// inGroup is the administrator rule of a system with one administrators' group:
// the groups the kernel reports for the caller include gid.
func inGroup(gid uint32) func(peercred.AuthInfo) (bool, error) {
	return func(ai peercred.AuthInfo) (bool, error) { return slices.Contains(ai.Groups, gid), nil }
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
	administrator, adminErr := p.isAdministrator(ai)
	if administrator {
		return nil
	}
	caller := describeCaller(ai)
	if adminErr != nil {
		caller += " (" + adminErr.Error() + ")"
	}
	if level == levelModify {
		return status.Errorf(codes.PermissionDenied, "%s is not an administrator, which %s requires", caller, fullMethod)
	}
	atConsole, err := p.isConsoleUser(ai)
	if err != nil {
		return status.Errorf(codes.PermissionDenied, "%s is not an administrator and the console user is unknown: %v", caller, err)
	}
	if !atConsole {
		return status.Errorf(codes.PermissionDenied, "%s is neither the console user nor an administrator", caller)
	}
	return nil
}

// isAdministrator looks at the Windows identity first: its UID is 0, which
// would be root anywhere else.
func (p *policy) isAdministrator(ai peercred.AuthInfo) (bool, error) {
	if windows := ai.Windows; windows != nil {
		return windows.Administrator, nil
	}
	if ai.UID == 0 {
		return true, nil
	}
	return p.administrator(ai)
}

func (p *policy) isConsoleUser(ai peercred.AuthInfo) (bool, error) {
	if windows := ai.Windows; windows != nil {
		session, err := p.consoleSession()
		return err == nil && windows.SessionID == session, err
	}
	console, err := p.consoleUIDs()
	return err == nil && slices.Contains(console, ai.UID), err
}

func describeCaller(ai peercred.AuthInfo) string {
	if ai.Windows != nil {
		return "sid " + ai.Windows.SID
	}
	return fmt.Sprintf("uid %d", ai.UID)
}

// callerAttr is the key and value that name the caller in a log line.
func callerAttr(ai peercred.AuthInfo) []any {
	if ai.Windows != nil {
		return []any{"sid", ai.Windows.SID}
	}
	return []any{"uid", ai.UID}
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
	attrs := append([]any{"method", method}, callerAttr(ai)...)
	attrs = append(attrs, "pid", ai.PID, "code", code.String(), "dur", time.Since(started).Round(time.Millisecond))
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
