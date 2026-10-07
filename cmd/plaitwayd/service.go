package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/manager"
	"github.com/KoukeNeko/Plaitway/internal/profile"
)

// defaultLogTail is how many lines WatchLogs sends first when the request
// leaves tail_lines at 0.
const defaultLogTail = 200

// service implements the gRPC API on top of the manager. Who may call what is
// decided before a method runs, by the interceptors in auth.go.
type service struct {
	pb.UnimplementedDaemonServiceServer

	log          *slog.Logger
	mgr          *manager.Manager
	shutdown     chan struct{} // closed when the daemon is stopping
	shutdownOnce sync.Once
}

func newService(log *slog.Logger, mgr *manager.Manager) *service {
	return &service{log: log, mgr: mgr, shutdown: make(chan struct{})}
}

// beginShutdown makes the open streams end with Unavailable, so that stopping
// the server does not wait for clients that watch forever.
func (s *service) beginShutdown() { s.shutdownOnce.Do(func() { close(s.shutdown) }) }

// statusError turns what the manager and the store return into the status
// code the API documents.
func statusError(err error) error {
	var invalid *profile.InvalidError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &invalid):
		return status.Error(codes.InvalidArgument, invalid.Error())
	case errors.Is(err, profile.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, manager.ErrNotAwaitingCredentials):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, manager.ErrShuttingDown):
		return status.Error(codes.Unavailable, err.Error())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return status.FromContextError(err).Err()
	}
	return status.Error(codes.Internal, err.Error())
}

func (s *service) GetDaemonInfo(context.Context, *pb.GetDaemonInfoRequest) (*pb.DaemonInfo, error) {
	return s.mgr.Info(), nil
}

func (s *service) GetDiagnostics(context.Context, *pb.GetDiagnosticsRequest) (*pb.Diagnostics, error) {
	return s.mgr.Diagnostics(), nil
}

func (s *service) ListProfiles(context.Context, *pb.ListProfilesRequest) (*pb.ListProfilesResponse, error) {
	return &pb.ListProfilesResponse{Profiles: s.mgr.List()}, nil
}

func (s *service) ImportProfile(_ context.Context, req *pb.ImportProfileRequest) (*pb.ImportProfileResponse, error) {
	resp, err := s.mgr.Import(req)
	return resp, statusError(err)
}

func (s *service) UpdateProfile(_ context.Context, req *pb.UpdateProfileRequest) (*pb.Profile, error) {
	p, err := s.mgr.Update(req)
	return p, statusError(err)
}

func (s *service) GetProfileContent(_ context.Context, req *pb.GetProfileContentRequest) (*pb.GetProfileContentResponse, error) {
	content, err := s.mgr.GetContent(req.Id)
	if err != nil {
		return nil, statusError(err)
	}
	return &pb.GetProfileContentResponse{Content: content}, nil
}

func (s *service) UpdateProfileContent(_ context.Context, req *pb.UpdateProfileContentRequest) (*pb.ImportProfileResponse, error) {
	resp, err := s.mgr.UpdateContent(req)
	return resp, statusError(err)
}

func (s *service) DeleteProfile(_ context.Context, req *pb.DeleteProfileRequest) (*pb.DeleteProfileResponse, error) {
	if err := s.mgr.Delete(req.Id); err != nil {
		return nil, statusError(err)
	}
	return &pb.DeleteProfileResponse{}, nil
}

func (s *service) ReorderProfiles(_ context.Context, req *pb.ReorderProfilesRequest) (*pb.ListProfilesResponse, error) {
	profiles, err := s.mgr.Reorder(req.Ids)
	if err != nil {
		return nil, statusError(err)
	}
	return &pb.ListProfilesResponse{Profiles: profiles}, nil
}

func (s *service) SetProfileEnabled(_ context.Context, req *pb.SetProfileEnabledRequest) (*pb.Profile, error) {
	p, err := s.mgr.SetEnabled(req.Id, req.Enabled)
	return p, statusError(err)
}

func (s *service) ProvideCredentials(_ context.Context, req *pb.ProvideCredentialsRequest) (*pb.Profile, error) {
	p, err := s.mgr.ProvideCredentials(req)
	return p, statusError(err)
}

func (s *service) Resync(context.Context, *pb.ResyncRequest) (*pb.ResyncResponse, error) {
	if err := s.mgr.Resync(); err != nil {
		return nil, statusError(err)
	}
	return &pb.ResyncResponse{}, nil
}

func (s *service) RemoveStaleRoute(_ context.Context, req *pb.RemoveStaleRouteRequest) (*pb.RemoveStaleRouteResponse, error) {
	if err := s.mgr.RemoveStale(req.Key); err != nil {
		return nil, statusError(err)
	}
	return &pb.RemoveStaleRouteResponse{}, nil
}

// WatchProfiles sends a snapshot, then one event per change, until the client
// goes away (cancel, SIGKILL and dead connection all cancel stream.Context())
// or the daemon shuts down.
func (s *service) WatchProfiles(_ *pb.WatchProfilesRequest, stream grpc.ServerStreamingServer[pb.ProfileEvent]) error {
	snapshot, events, cancel := s.mgr.Watch()
	s.log.Debug("profile watch started", "watchers", s.mgr.Watchers())
	defer func() {
		cancel()
		s.log.Debug("profile watch ended", "watchers", s.mgr.Watchers())
	}()

	if err := stream.Send(&pb.ProfileEvent{Event: &pb.ProfileEvent_Snapshot{Snapshot: &pb.ProfileSnapshot{Profiles: snapshot}}}); err != nil {
		return err
	}
	for {
		select {
		case <-stream.Context().Done():
			return status.FromContextError(stream.Context().Err()).Err()
		case <-s.shutdown:
			return status.Error(codes.Unavailable, "daemon shutting down")
		case ev, ok := <-events:
			if !ok {
				return status.Error(codes.ResourceExhausted, "watcher too slow, dropped")
			}
			if err := stream.Send(ev); err != nil {
				return err
			}
		}
	}
}

// WatchLogs sends the buffered tail of a profile's log (or the daemon's own),
// then the lines that follow, until the client goes away.
func (s *service) WatchLogs(req *pb.WatchLogsRequest, stream grpc.ServerStreamingServer[pb.LogLine]) error {
	tail := int(req.TailLines)
	switch {
	case tail < 0:
		return status.Error(codes.InvalidArgument, "tail_lines must not be negative")
	case tail == 0:
		tail = defaultLogTail
	}
	lines, live, cancel, err := s.mgr.Logs(req.ProfileId, tail)
	if err != nil {
		return statusError(err)
	}
	defer cancel()

	for _, line := range lines {
		if err := stream.Send(line); err != nil {
			return err
		}
	}
	for {
		select {
		case <-stream.Context().Done():
			return status.FromContextError(stream.Context().Err()).Err()
		case <-s.shutdown:
			return status.Error(codes.Unavailable, "daemon shutting down")
		case line, ok := <-live:
			if !ok {
				return status.Error(codes.Aborted, "log stream ended")
			}
			if err := stream.Send(line); err != nil {
				return err
			}
		}
	}
}
