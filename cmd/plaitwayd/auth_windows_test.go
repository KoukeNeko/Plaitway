package main

import (
	"context"
	"log/slog"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/transport"
)

// administratorsSID is the group of administrators; whoami lists it in the
// token of an administrator whether it is enabled or only deny-only.
const administratorsSID = "S-1-5-32-544"

// pipeAccess is what transport.DialOptions asks for. The test dials by itself
// to choose the impersonation level.
const pipeAccess = windows.FILE_READ_DATA | windows.FILE_WRITE_DATA | windows.FILE_READ_ATTRIBUTES | windows.READ_CONTROL | windows.SYNCHRONIZE

func TestRealConsoleSessionIsReadable(t *testing.T) {
	session, err := activeConsoleSession()
	if err != nil {
		t.Skipf("no session is attached to the console of this machine: %v", err)
	}
	if got, err := newPolicy().consoleSession(); err != nil || got != session {
		t.Fatalf("the policy of the daemon reads session %d, %v; want %d", got, err, session)
	}
}

func processTokenSID(t *testing.T) string {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return user.User.Sid.String()
}

// The decision is made on the token the daemon read from the pipe, with nothing
// rewritten: a plain user is refused, an administrator (elevated or not) is let
// in even though the session is not the console's. Which of the two this test
// is depends on who runs it, so whoami, which reads the token on its own, says
// what to expect.
func TestTheRealTokenOfTheTestProcessDecidesWhenNothingIsRewritten(t *testing.T) {
	// By absolute path: a shell with Git's tools first on PATH (Git Bash, GoLand
	// started from it) resolves "whoami" to the coreutils one, which has no /groups.
	system, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(filepath.Join(system, "whoami.exe"), "/groups").Output()
	if err != nil {
		t.Fatalf("whoami /groups: %v", err)
	}
	wantAllowed := strings.Contains(string(out), administratorsSID)
	h := startDaemon(t, t.TempDir(), caller{pol: &policy{consoleSession: consoleIsAnotherSession}})

	_, err = h.client.ListProfiles(context.Background(), &pb.ListProfilesRequest{})

	switch {
	case wantAllowed && err != nil:
		t.Fatalf("the token is an administrator's, but ListProfiles failed: %v", err)
	case !wantAllowed:
		wantCode(t, err, codes.PermissionDenied)
	}
}

// What the interceptor logs is the SID of the real token: the roles of the
// tests rewrite the administrator flag only.
func TestCallersAreNamedByTheSIDOfTheirRealToken(t *testing.T) {
	var logOut syncBuffer
	h := startLoggingDaemon(t, t.TempDir(), stranger(), &logOut, slog.LevelInfo)

	_, err := h.client.ListProfiles(context.Background(), &pb.ListProfilesRequest{})
	wantCode(t, err, codes.PermissionDenied)

	if want := "sid=" + processTokenSID(t); !strings.Contains(logOut.String(), want) {
		t.Fatalf("the refusal is not logged with %s:\n%s", want, logOut.String())
	}
}

// A client that connects anonymously cannot be told from any other: the
// handshake refuses it, whoever the rewritten role would make it.
func TestAnonymousClientIsRefusedByTheRunningDaemon(t *testing.T) {
	h := newDaemonHarness(t)
	conn, err := grpc.NewClient(transport.Target(h.socket),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, address string) (net.Conn, error) {
			return winio.DialPipeAccessImpLevel(ctx, address, pipeAccess, winio.PipeImpLevelAnonymous)
		}))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()

	_, err = pb.NewDaemonServiceClient(conn).GetDaemonInfo(ctx, &pb.GetDaemonInfoRequest{})

	wantCode(t, err, codes.Unavailable)
	if _, err := h.client.GetDaemonInfo(context.Background(), &pb.GetDaemonInfoRequest{}); err != nil {
		t.Fatalf("the refusal of one client stopped the daemon from serving the next: %v", err)
	}
}
