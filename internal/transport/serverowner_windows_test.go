package transport

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/KoukeNeko/Plaitway/internal/peercred"
)

const (
	sidCaller         = "S-1-5-21-1111111111-2222222222-3333333333-1001"
	sidOtherUser      = "S-1-5-21-1111111111-2222222222-3333333333-1002"
	sidBuiltinAdmin   = "S-1-5-21-1111111111-2222222222-3333333333-500"
	sidLocalService   = "S-1-5-19"
	sidNetworkService = "S-1-5-20"
	sidServiceAccount = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"
)

func parseSID(t *testing.T, text string) *windows.SID {
	t.Helper()
	sid, err := windows.StringToSid(text)
	if err != nil {
		t.Fatal(err)
	}
	return sid
}

func TestCheckServerOwner(t *testing.T) {
	cases := []struct {
		name   string
		owner  string
		caller string
		trust  bool
	}{
		{"SYSTEM, the service", sidSystem, sidCaller, true},
		{"Administrators, an elevated daemon", sidAdministrators, sidCaller, true},
		{"the calling user, an unelevated daemon", sidCaller, sidCaller, true},
		{"SYSTEM when the caller is unknown", sidSystem, "", true},
		{"another user", sidOtherUser, sidCaller, false},
		{"the built-in Administrator account is not the Administrators group", sidBuiltinAdmin, sidCaller, false},
		{"Everyone", sidEveryone, sidCaller, false},
		{"Users", sidUsers, sidCaller, false},
		{"Authenticated Users", sidAuthenticated, sidCaller, false},
		{"Interactive", sidInteractive, sidCaller, false},
		{"Local Service", sidLocalService, sidCaller, false},
		{"Network Service", sidNetworkService, sidCaller, false},
		{"a per-service account", sidServiceAccount, sidCaller, false},
		{"another user when the caller is unknown", sidOtherUser, "", false},
		{"a caller that is SYSTEM trusts itself", sidSystem, sidSystem, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var caller *windows.SID
			if tc.caller != "" {
				caller = parseSID(t, tc.caller)
			}
			err := checkServerOwner(parseSID(t, tc.owner), caller)
			if tc.trust {
				if err != nil {
					t.Fatalf("owner %s refused: %v", tc.owner, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("owner %s accepted for caller %q", tc.owner, tc.caller)
			}
			if !strings.HasPrefix(err.Error(), RefusedServerPrefix) || !strings.Contains(err.Error(), tc.owner) {
				t.Errorf("error %q must start with the refusal prefix and name the owner %s", err, tc.owner)
			}
		})
	}
}

func TestCheckServerOwnerRefusesAMissingOwner(t *testing.T) {
	if err := checkServerOwner(nil, parseSID(t, sidCaller)); err == nil || !strings.HasPrefix(err.Error(), RefusedServerPrefix) {
		t.Fatalf("a missing owner got %v, want a refusal", err)
	}
	if err := checkServerOwner(&windows.SID{}, parseSID(t, sidCaller)); err == nil {
		t.Fatal("an invalid owner was accepted")
	}
}

func TestRefusalNamesTheOwnerAccountWhenItResolves(t *testing.T) {
	err := checkServerOwner(parseSID(t, sidLocalService), parseSID(t, sidCaller))
	if err == nil || !strings.Contains(err.Error(), "LOCAL SERVICE") {
		t.Fatalf("got %v, want the account name of S-1-5-19", err)
	}
}

func currentUser(t *testing.T) *windows.SID {
	t.Helper()
	sid, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	return sid
}

// listenOwnedByCaller creates the pipe the way Listen does but names the
// calling user as its owner. Without it the owner would be Administrators in an
// elevated test run, which every caller trusts, and the refusal tests would
// pass or fail for the wrong reason.
func listenOwnedByCaller(t *testing.T) net.Listener {
	t.Helper()
	descriptor, err := pipeSecurityDescriptor()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := winio.ListenPipe(uniquePipePath(), &winio.PipeConfig{SecurityDescriptor: "O:" + currentUser(t).String() + descriptor})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	return listener
}

// serveHealthOwnedByCaller is listenOwnedByCaller with a gRPC server behind it,
// and returns the callers its handlers saw.
func serveHealthOwnedByCaller(t *testing.T) (path string, callers <-chan peercred.AuthInfo) {
	t.Helper()
	listener := listenOwnedByCaller(t)
	return listener.Addr().String(), healthServer(t, listener)
}

func checkWithOptions(t *testing.T, path string, lookupCaller func() (*windows.SID, error)) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	_, err := newClient(t, path, dialOptionsAs(lookupCaller)...).Check(ctx, &healthpb.HealthCheckRequest{})
	return err
}

func callerIs(sid *windows.SID) func() (*windows.SID, error) {
	return func() (*windows.SID, error) { return sid, nil }
}

func skipWhenCallerIsSystem(t *testing.T) {
	t.Helper()
	if currentUser(t).IsWellKnown(windows.WinLocalSystemSid) {
		t.Skip("a SYSTEM caller owns its pipes as SYSTEM, which every caller trusts, so there is no foreign owner to pose against")
	}
}

func TestPipeCreatedByListenIsAcceptedForItsCreator(t *testing.T) {
	path := uniquePipePath()
	listener := listenForTest(t, path)
	accepted := acceptAsync(listener)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	conn, err := dialVerified(ctx, path, currentUser(t))
	if err != nil {
		t.Fatalf("the pipe made by Listen was refused for the user who made it: %v", err)
	}
	conn.Close()
	awaitAccepted(t, accepted)
}

func TestListenedPipeIsOwnedByASystemIdentityOrTheCaller(t *testing.T) {
	server, client := connectPair(t, listenForTest(t, uniquePipePath()), winio.PipeImpLevelIdentification)

	ends := []struct {
		name string
		conn net.Conn
	}{{"server end", server}, {"client end", client}}
	for _, end := range ends {
		handleHolder, ok := end.conn.(interface{ Fd() uintptr })
		if !ok {
			t.Fatalf("%s %T has no handle", end.name, end.conn)
		}
		owner, err := pipeOwner(windows.Handle(handleHolder.Fd()))
		if err != nil {
			t.Fatalf("%s: %v", end.name, err)
		}
		isCreator := owner.Equals(currentUser(t))
		isSystemIdentity := owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) || owner.IsWellKnown(windows.WinLocalSystemSid)
		if !isCreator && !isSystemIdentity {
			t.Errorf("%s: owner %s is neither the creator, SYSTEM nor Administrators", end.name, owner)
		}
		if err := checkServerOwner(owner, currentUser(t)); err != nil {
			t.Errorf("%s: %v", end.name, err)
		}
	}
}

func TestDialRefusesAServerOwnedBySomeoneElseAndSendsNothing(t *testing.T) {
	skipWhenCallerIsSystem(t)
	listener := listenOwnedByCaller(t)
	accepted := acceptAsync(listener)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	_, err := dialVerified(ctx, listener.Addr().String(), parseSID(t, sidOtherUser))
	if err == nil || !strings.HasPrefix(err.Error(), RefusedServerPrefix) || !strings.Contains(err.Error(), currentUser(t).String()) {
		t.Fatalf("dial got %v, want a refusal naming the owner %s", err, currentUser(t))
	}

	// The refused client hung up without a byte: the server sees end of file.
	server := awaitAccepted(t, accepted)
	server.SetReadDeadline(time.Now().Add(testTimeout))
	if received, readErr := io.ReadAll(server); readErr != nil || len(received) != 0 {
		t.Fatalf("server received %q (error %v) from a client that refused it", received, readErr)
	}
}

func TestGRPCCallRefusesAServerOwnedBySomeoneElse(t *testing.T) {
	skipWhenCallerIsSystem(t)
	path, callers := serveHealthOwnedByCaller(t)

	err := checkWithOptions(t, path, callerIs(parseSID(t, sidOtherUser)))

	if status.Code(err) != codes.Unavailable || !strings.Contains(err.Error(), RefusedServerPrefix) {
		t.Fatalf("call got %v, want Unavailable with %q", err, RefusedServerPrefix)
	}
	assertNoHandlerRan(t, callers)
}

func TestGRPCCallSucceedsForTheOwnerOfThePipe(t *testing.T) {
	path, callers := serveHealthOwnedByCaller(t)

	if err := checkWithOptions(t, path, callerIs(currentUser(t))); err != nil {
		t.Fatal(err)
	}
	<-callers
}

func TestDialFailsClosedWhenTheCallerCannotBeIdentified(t *testing.T) {
	path, callers := serveHealthOwnedByCaller(t)
	unreadable := errors.New("token unreadable")

	err := checkWithOptions(t, path, func() (*windows.SID, error) { return nil, unreadable })

	if status.Code(err) != codes.Unavailable || !strings.Contains(err.Error(), unreadable.Error()) {
		t.Fatalf("call got %v, want Unavailable naming %q", err, unreadable)
	}
	assertNoHandlerRan(t, callers)
}

func assertNoHandlerRan(t *testing.T, callers <-chan peercred.AuthInfo) {
	t.Helper()
	select {
	case caller := <-callers:
		t.Fatalf("handler ran for a client that refused the server: %+v", caller)
	default:
	}
}

func TestDialFailsClosedWhenTheOwnerCannotBeRead(t *testing.T) {
	listener := listenForTest(t, uniquePipePath())
	// A handle opened without READ_CONTROL cannot be asked for its owner.
	const accessWithoutReadControl = interactiveUserPipeAccess &^ windows.READ_CONTROL
	accepted := acceptAsync(listener)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	conn, err := winio.DialPipeAccessImpLevel(ctx, listener.Addr().String(), accessWithoutReadControl, winio.PipeImpLevelIdentification)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	awaitAccepted(t, accepted)

	err = verifyServer(conn, currentUser(t))
	if err == nil || !strings.HasPrefix(err.Error(), RefusedServerPrefix) || !strings.Contains(err.Error(), "cannot read its owner") {
		t.Fatalf("verifyServer got %v, want a refusal because the owner is unreadable", err)
	}
}

func TestOrdinaryInteractiveUserCanReadTheOwnerOfTheServicePipe(t *testing.T) {
	path := uniquePipePath()
	accepted := acceptAsync(listenForTest(t, path))
	owner := currentUser(t)

	// The DACL names this user, so the check has to reach the owner through the
	// Interactive ACE, as every other user's client does.
	impersonateOrdinaryInteractiveUser(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()
		conn, err := dialVerified(ctx, path, owner)
		if err != nil {
			t.Errorf("an ordinary interactive user cannot verify the pipe: %v", err)
			return
		}
		defer conn.Close()
		awaitAccepted(t, accepted)
	})
}
