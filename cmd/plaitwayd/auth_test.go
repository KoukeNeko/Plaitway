package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/peercred"
)

const (
	adminGID     = 80
	consoleOwner = 501
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testPolicy() *policy {
	return &policy{adminGID: adminGID, consoleUID: func() (uint32, error) { return consoleOwner, nil }}
}

func identity(uid uint32, groups ...uint32) peercred.AuthInfo {
	info := peercred.Info{UID: uid, PID: 4242, Groups: groups}
	if len(groups) > 0 {
		info.GID = groups[0]
	}
	return peercred.AuthInfo{Info: info, Known: true}
}

var (
	connectMethods = []string{
		pb.DaemonService_GetDaemonInfo_FullMethodName,
		pb.DaemonService_GetDiagnostics_FullMethodName,
		pb.DaemonService_ListProfiles_FullMethodName,
		pb.DaemonService_WatchProfiles_FullMethodName,
		pb.DaemonService_WatchLogs_FullMethodName,
		pb.DaemonService_SetProfileEnabled_FullMethodName,
		pb.DaemonService_ProvideCredentials_FullMethodName,
		pb.DaemonService_Resync_FullMethodName,
	}
	modifyMethods = []string{
		pb.DaemonService_ImportProfile_FullMethodName,
		pb.DaemonService_UpdateProfile_FullMethodName,
		pb.DaemonService_UpdateProfileContent_FullMethodName,
		pb.DaemonService_DeleteProfile_FullMethodName,
		pb.DaemonService_ReorderProfiles_FullMethodName,
		pb.DaemonService_RemoveStaleRoute_FullMethodName,
		// It changes nothing, but it returns the private keys.
		pb.DaemonService_GetProfileContent_FullMethodName,
	}
)

func TestPolicyDecisions(t *testing.T) {
	tests := []struct {
		name    string
		who     peercred.AuthInfo
		connect bool
		modify  bool
	}{
		{"root", identity(0, 0), true, true},
		{"root without groups", identity(0), true, true},
		{"administrator", identity(502, 20, adminGID), true, true},
		{"administrator by a later group", identity(502, 20, 12, 61, adminGID), true, true},
		{"console user", identity(consoleOwner, 20), true, false},
		{"console user who is also an administrator", identity(consoleOwner, 20, adminGID), true, true},
		{"another user", identity(503, 20), false, false},
		{"another user in many groups", identity(503, 20, 12, 61, 79, 81, 701), false, false},
		{"group with the admin number as its uid is not an administrator", identity(adminGID, 20), false, false},
		{"unknown identity", peercred.AuthInfo{}, false, false},
		{"unknown identity that claims root", peercred.AuthInfo{Info: peercred.Info{UID: 0, Groups: []uint32{adminGID}}}, false, false},
	}
	p := testPolicy()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, method := range connectMethods {
				if allowed := p.check(tt.who, method) == nil; allowed != tt.connect {
					t.Errorf("%s allowed = %v, want %v", method, allowed, tt.connect)
				}
			}
			for _, method := range modifyMethods {
				if allowed := p.check(tt.who, method) == nil; allowed != tt.modify {
					t.Errorf("%s allowed = %v, want %v", method, allowed, tt.modify)
				}
			}
		})
	}
}

func TestPolicyDeniesWithPermissionDenied(t *testing.T) {
	err := testPolicy().check(identity(503, 20), pb.DaemonService_ListProfiles_FullMethodName)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code = %v (%v)", status.Code(err), err)
	}
}

func TestPolicyFailsClosedWhenTheConsoleUserIsUnknown(t *testing.T) {
	p := &policy{adminGID: adminGID, consoleUID: func() (uint32, error) { return 0, errors.New("no console") }}
	if err := p.check(identity(consoleOwner, 20), pb.DaemonService_ListProfiles_FullMethodName); err == nil {
		t.Fatal("a non-administrator was allowed although the console user could not be read")
	}
	// Administrators do not depend on the console.
	if err := p.check(identity(502, 20, adminGID), pb.DaemonService_ListProfiles_FullMethodName); err != nil {
		t.Fatalf("administrator denied: %v", err)
	}
}

func TestPolicyRefusesMethodsWithoutAnEntry(t *testing.T) {
	if err := testPolicy().check(identity(0, 0), "/plaitway.v1.DaemonService/Brand_New"); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("unlisted method: %v, want PermissionDenied even for root", err)
	}
}

// A new RPC must get an access level before it is reachable.
func TestEveryRPCHasAnAccessLevel(t *testing.T) {
	var all []string
	for _, m := range pb.DaemonService_ServiceDesc.Methods {
		all = append(all, "/"+pb.DaemonService_ServiceDesc.ServiceName+"/"+m.MethodName)
	}
	for _, s := range pb.DaemonService_ServiceDesc.Streams {
		all = append(all, "/"+pb.DaemonService_ServiceDesc.ServiceName+"/"+s.StreamName)
	}
	if len(all) != len(connectMethods)+len(modifyMethods) {
		t.Errorf("the API has %d methods, the tests list %d", len(all), len(connectMethods)+len(modifyMethods))
	}
	for _, method := range all {
		if methodLevels[method] == 0 {
			t.Errorf("%s has no access level", method)
		}
	}
	for method, level := range methodLevels {
		want := levelConnect
		for _, m := range modifyMethods {
			if m == method {
				want = levelModify
			}
		}
		if level != want {
			t.Errorf("%s has level %d, want %d", method, level, want)
		}
	}
}

func TestAuthorizeFailsClosedWithoutPeerIdentity(t *testing.T) {
	_, err := testPolicy().authorize(context.Background(), pb.DaemonService_GetDaemonInfo_FullMethodName)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied", status.Code(err))
	}
	// A peer with some other kind of AuthInfo is no identity either.
	ctx := peer.NewContext(context.Background(), &peer.Peer{})
	if _, err := testPolicy().authorize(ctx, pb.DaemonService_GetDaemonInfo_FullMethodName); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied", status.Code(err))
	}
}

func peerContext(ai peercred.AuthInfo) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{AuthInfo: ai})
}

func TestUnaryInterceptorDoesNotRunDeniedHandlers(t *testing.T) {
	var ran atomic.Bool
	handler := func(context.Context, any) (any, error) { ran.Store(true); return "done", nil }
	intercept := testPolicy().unaryInterceptor(discardLog())

	for _, tt := range []struct {
		name    string
		ctx     context.Context
		method  string
		allowed bool
	}{
		{"stranger modifies", peerContext(identity(503, 20)), pb.DaemonService_DeleteProfile_FullMethodName, false},
		{"console user modifies", peerContext(identity(consoleOwner, 20)), pb.DaemonService_ImportProfile_FullMethodName, false},
		{"stranger reads", peerContext(identity(503, 20)), pb.DaemonService_ListProfiles_FullMethodName, false},
		{"no identity", context.Background(), pb.DaemonService_ListProfiles_FullMethodName, false},
		{"console user reads", peerContext(identity(consoleOwner, 20)), pb.DaemonService_ListProfiles_FullMethodName, true},
		{"administrator modifies", peerContext(identity(502, 20, adminGID)), pb.DaemonService_DeleteProfile_FullMethodName, true},
	} {
		ran.Store(false)
		_, err := intercept(tt.ctx, nil, &grpc.UnaryServerInfo{FullMethod: tt.method}, handler)
		if ran.Load() != tt.allowed || (err == nil) != tt.allowed {
			t.Errorf("%s: handler ran %v, error %v; allowed should be %v", tt.name, ran.Load(), err, tt.allowed)
		}
	}
}

type fakeStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s fakeStream) Context() context.Context { return s.ctx }

func TestStreamInterceptorDoesNotRunDeniedHandlers(t *testing.T) {
	var ran atomic.Bool
	handler := func(any, grpc.ServerStream) error { ran.Store(true); return nil }
	intercept := testPolicy().streamInterceptor(discardLog())

	err := intercept(nil, fakeStream{ctx: peerContext(identity(503, 20))}, &grpc.StreamServerInfo{FullMethod: pb.DaemonService_WatchLogs_FullMethodName}, handler)
	if err == nil || ran.Load() {
		t.Fatalf("stranger watched logs: error %v, handler ran %v", err, ran.Load())
	}
	err = intercept(nil, fakeStream{ctx: peerContext(identity(consoleOwner, 20))}, &grpc.StreamServerInfo{FullMethod: pb.DaemonService_WatchLogs_FullMethodName}, handler)
	if err != nil || !ran.Load() {
		t.Fatalf("console user was refused: %v", err)
	}
}

func TestConsoleUserIsReadAtMostOncePerSecond(t *testing.T) {
	now := time.Unix(1000, 0)
	reads := 0
	owner := uint32(501)
	c := &consoleUser{
		stat: func() (uint32, error) { reads++; return owner, nil },
		now:  func() time.Time { return now },
	}

	for range 5 {
		if uid, err := c.uid(); uid != 501 || err != nil {
			t.Fatalf("uid = %d, %v", uid, err)
		}
	}
	if reads != 1 {
		t.Fatalf("%d reads within one second, want 1", reads)
	}

	owner = 502 // somebody else logged in
	now = now.Add(consoleRefresh - time.Millisecond)
	if uid, _ := c.uid(); uid != 501 {
		t.Fatalf("uid = %d before the interval ended, want the cached 501", uid)
	}
	now = now.Add(time.Millisecond)
	if uid, _ := c.uid(); uid != 502 || reads != 2 {
		t.Fatalf("uid = %d after %d reads, want 502 after 2", uid, reads)
	}
}

func TestConsoleUserCachesFailuresToo(t *testing.T) {
	now := time.Unix(1000, 0)
	reads := 0
	c := &consoleUser{
		stat: func() (uint32, error) { reads++; return 0, errors.New("no such device") },
		now:  func() time.Time { return now },
	}
	for range 3 {
		if _, err := c.uid(); err == nil {
			t.Fatal("error lost")
		}
	}
	if reads != 1 {
		t.Fatalf("%d reads, want 1", reads)
	}
}

func TestRealConsoleOwnerIsReadable(t *testing.T) {
	if _, err := fileOwner(consoleDevice); err != nil {
		t.Skipf("no console device here: %v", err)
	}
	if _, err := newConsoleUser().uid(); err != nil {
		t.Fatal(err)
	}
}

// The menu bar app polls the read-only calls all the time (the Diagnostics
// page every two seconds), which would fill the log file, and the log the
// Helper log tab shows, with lines that say nothing.
func TestSuccessfulReadsAreLoggedOnlyAtDebug(t *testing.T) {
	reads := []string{
		pb.DaemonService_GetDaemonInfo_FullMethodName,
		pb.DaemonService_GetDiagnostics_FullMethodName,
		pb.DaemonService_ListProfiles_FullMethodName,
		pb.DaemonService_WatchProfiles_FullMethodName,
		pb.DaemonService_WatchLogs_FullMethodName,
	}
	for _, method := range reads {
		for _, err := range []error{nil, status.Error(codes.Canceled, "client went away")} {
			var out bytes.Buffer
			info := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelInfo}))
			logCall(info, method, identity(consoleOwner, 20), time.Now(), err)
			if out.Len() != 0 {
				t.Errorf("%s (%v) is logged at info: %s", method, err, out.String())
			}

			out.Reset()
			debug := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))
			logCall(debug, method, identity(consoleOwner, 20), time.Now(), err)
			if out.Len() == 0 {
				t.Errorf("%s (%v) is not logged at debug", method, err)
			}
		}
	}
}

// What is worth finding in the log stays: every call that changes something,
// every read of the private keys, and every call that fails.
func TestCallsThatChangeSomethingOrFailAreLogged(t *testing.T) {
	changes := []string{
		pb.DaemonService_SetProfileEnabled_FullMethodName,
		pb.DaemonService_ProvideCredentials_FullMethodName,
		pb.DaemonService_Resync_FullMethodName,
		pb.DaemonService_ImportProfile_FullMethodName,
		pb.DaemonService_UpdateProfile_FullMethodName,
		pb.DaemonService_UpdateProfileContent_FullMethodName,
		pb.DaemonService_GetProfileContent_FullMethodName,
		pb.DaemonService_DeleteProfile_FullMethodName,
		pb.DaemonService_ReorderProfiles_FullMethodName,
		pb.DaemonService_RemoveStaleRoute_FullMethodName,
	}
	for _, method := range changes {
		var out bytes.Buffer
		log := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelInfo}))
		logCall(log, method, identity(consoleOwner, 20), time.Now(), nil)
		if out.Len() == 0 {
			t.Errorf("%s is not logged", method)
		}
	}

	var out bytes.Buffer
	log := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logCall(log, pb.DaemonService_ListProfiles_FullMethodName, identity(503, 20), time.Now(), status.Error(codes.PermissionDenied, "no"))
	if out.Len() == 0 {
		t.Error("a refused read is not logged")
	}
}
