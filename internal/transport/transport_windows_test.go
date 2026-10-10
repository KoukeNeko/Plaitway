//go:build windows

package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/KoukeNeko/Plaitway/internal/peercred"
)

const (
	testTimeout           = 10 * time.Second
	pipeBusyRetryInterval = 10 * time.Millisecond
)

var pipeCounter atomic.Uint64

// uniquePipePath gives every test its own pipe: names are machine-global, so
// parallel packages and repeated runs would otherwise collide.
func uniquePipePath() string {
	return fmt.Sprintf(`%splaitway-transport-test-%d-%d`, pipeNamespace, os.Getpid(), pipeCounter.Add(1))
}

func listenForTest(t *testing.T, path string) net.Listener {
	t.Helper()
	listener, err := Listen(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	return listener
}

func dialRaw(t *testing.T, path string, level winio.PipeImpLevel) net.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	conn, err := winio.DialPipeAccessImpLevel(ctx, path, interactiveUserPipeAccess, level)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

type acceptResult struct {
	conn net.Conn
	err  error
}

func acceptAsync(listener net.Listener) <-chan acceptResult {
	accepted := make(chan acceptResult, 1)
	go func() {
		conn, err := listener.Accept()
		accepted <- acceptResult{conn, err}
	}()
	return accepted
}

func awaitAccepted(t *testing.T, accepted <-chan acceptResult) net.Conn {
	t.Helper()
	select {
	case result := <-accepted:
		if result.err != nil {
			t.Fatal(result.err)
		}
		t.Cleanup(func() { result.conn.Close() })
		return result.conn
	case <-time.After(testTimeout):
		t.Fatal("Accept did not return")
		return nil
	}
}

// connectPair returns the server end and the client end of one connection.
func connectPair(t *testing.T, listener net.Listener, level winio.PipeImpLevel) (server, client net.Conn) {
	t.Helper()
	accepted := acceptAsync(listener)
	client = dialRaw(t, listener.Addr().String(), level)
	return awaitAccepted(t, accepted), client
}

// healthServer serves the standard health service behind the daemon's
// transport credentials and records who the handler saw calling.
func healthServer(t *testing.T, listener net.Listener) <-chan peercred.AuthInfo {
	t.Helper()
	callers := make(chan peercred.AuthInfo, 1)
	server := grpc.NewServer(
		grpc.Creds(peercred.NewServerCredentials()),
		grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
			if caller, ok := peercred.FromContext(ctx); ok {
				callers <- caller
			}
			return next(ctx, req)
		}),
	)
	healthpb.RegisterHealthServer(server, health.NewServer())
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	return callers
}

func newClient(t *testing.T, path string, extra ...grpc.DialOption) healthpb.HealthClient {
	t.Helper()
	options := append(DialOptions(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	conn, err := grpc.NewClient(Target(path), append(options, extra...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return healthpb.NewHealthClient(conn)
}

func TestDefaultPathIsAcceptedByListen(t *testing.T) {
	if err := checkPipePath(DefaultPath()); err != nil {
		t.Fatal(err)
	}
	if got, want := Target(`\\.\pipe\plaitway`), `passthrough:///\\.\pipe\plaitway`; got != want {
		t.Fatalf("Target = %q, want %q", got, want)
	}
}

func TestGRPCRoundTripReportsTheCaller(t *testing.T) {
	path := uniquePipePath()
	callers := healthServer(t, listenForTest(t, path))

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if _, err := newClient(t, path).Check(ctx, &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatal(err)
	}

	caller := <-callers
	own, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if !caller.Known || caller.Windows == nil || caller.Windows.SID != own.User.Sid.String() || caller.PID != int32(os.Getpid()) {
		t.Fatalf("handler saw caller %+v, want this process and user %s", caller, own.User.Sid)
	}
}

// transfer sends message from one end and reads it on the other. The pipe has
// no buffer (go-winio creates it with size 0), so a write only completes when
// the peer reads: the write cannot run on the reading goroutine.
func transfer(t *testing.T, from, to net.Conn, message string) {
	t.Helper()
	written := make(chan error, 1)
	go func() {
		_, err := io.WriteString(from, message)
		written <- err
	}()
	received := make([]byte, len(message))
	to.SetReadDeadline(time.Now().Add(testTimeout))
	if _, err := io.ReadFull(to, received); err != nil {
		t.Fatal(err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	if string(received) != message {
		t.Fatalf("received %q, want %q", received, message)
	}
}

func TestRawConnectionRoundTrip(t *testing.T) {
	server, client := connectPair(t, listenForTest(t, uniquePipePath()), winio.PipeImpLevelIdentification)

	transfer(t, client, server, "ping")
	transfer(t, server, client, "pong")
}
func TestListenIsAnErrorWhenThePipeAlreadyExists(t *testing.T) {
	path := uniquePipePath()
	first := listenForTest(t, path)

	second, err := Listen(path, 0)
	if err == nil {
		second.Close()
		t.Fatal("second Listen on a live pipe succeeded")
	}
	// Windows says "access denied" here, which would read as a permission problem.
	if !strings.Contains(err.Error(), "another process already serves "+path) {
		t.Errorf("second Listen: %v, want it to say that another process serves the pipe", err)
	}

	// The refused attempt must not have disturbed the running listener.
	server, client := connectPair(t, first, winio.PipeImpLevelIdentification)
	transfer(t, client, server, "x")
}

func TestListenSucceedsAgainAfterClose(t *testing.T) {
	path := uniquePipePath()
	first, err := Listen(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	first.Close()

	second, err := Listen(path, 0)
	if err != nil {
		t.Fatalf("Listen after Close: %v", err)
	}
	second.Close()
}

func TestListenRejectsPathsThatAreNotPipeNames(t *testing.T) {
	scratchFile := t.TempDir() + `\plaitway.sock`
	for _, path := range []string{
		"",
		"plaitway",
		scratchFile,
		"/tmp/plaitway.sock",
		pipeNamespace,
		`\\.\pipe`,
		`\\server\pipe\plaitway`,
		`\\.\mailslot\plaitway`,
	} {
		listener, err := Listen(path, 0)
		if err == nil {
			listener.Close()
			t.Errorf("Listen(%q) succeeded", path)
			continue
		}
		if path != "" && !strings.Contains(err.Error(), "named pipe path") {
			t.Errorf("Listen(%q) error %q does not say the path is not a pipe path", path, err)
		}
	}
	if _, err := os.Stat(scratchFile); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Listen created or touched %s (stat error: %v)", scratchFile, err)
	}
}

func TestPipeNameIsMatchedCaseInsensitively(t *testing.T) {
	path := uniquePipePath()
	listener, err := Listen(`\\.\PIPE\`+path[len(pipeNamespace):], 0)
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
}

// accessControlEntry is one ACE of the pipe's DACL.
type accessControlEntry struct {
	aceType byte
	mask    uint32
	sid     string
}

// readPipeDACL reads the descriptor back from the server end of an accepted
// connection, which is the object clients are checked against.
func readPipeDACL(t *testing.T, server net.Conn) (entries []accessControlEntry, protected bool) {
	t.Helper()
	handleHolder, ok := server.(interface{ Fd() uintptr })
	if !ok {
		t.Fatalf("server end %T has no handle", server)
	}
	descriptor, err := windows.GetSecurityInfo(windows.Handle(handleHolder.Fd()), windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := descriptor.DACL()
	if err != nil || acl == nil {
		t.Fatalf("pipe has no DACL (err %v)", err)
	}
	for index := uint32(0); index < uint32(acl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, index, &ace); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, accessControlEntry{
			aceType: ace.Header.AceType,
			mask:    uint32(ace.Mask),
			sid:     (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String(),
		})
	}
	return entries, control&windows.SE_DACL_PROTECTED != 0
}

// pipeFullControl is GENERIC_ALL after the file system mapped it for the pipe
// (FILE_ALL_ACCESS), which is how it reads back from the object.
const pipeFullControl = 0x1F01FF

const (
	sidNetwork        = "S-1-5-2"
	sidSystem         = "S-1-5-18"
	sidAdministrators = "S-1-5-32-544"
	sidInteractive    = "S-1-5-4"
	sidEveryone       = "S-1-1-0"
	sidAnonymous      = "S-1-5-7"
	sidAuthenticated  = "S-1-5-11"
	sidUsers          = "S-1-5-32-545"
)

func ownUserSID(t *testing.T) string {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return user.User.Sid.String()
}

func TestPipeDACLIsExactlyAsSpecified(t *testing.T) {
	server, _ := connectPair(t, listenForTest(t, uniquePipePath()), winio.PipeImpLevelIdentification)

	got, protected := readPipeDACL(t, server)

	if !protected {
		t.Error("DACL is not protected: it would inherit from a parent")
	}
	want := []accessControlEntry{
		{windows.ACCESS_DENIED_ACE_TYPE, pipeFullControl, sidNetwork},
		{windows.ACCESS_ALLOWED_ACE_TYPE, pipeFullControl, sidSystem},
		{windows.ACCESS_ALLOWED_ACE_TYPE, pipeFullControl, sidAdministrators},
		{windows.ACCESS_ALLOWED_ACE_TYPE, pipeFullControl, ownUserSID(t)},
		{windows.ACCESS_ALLOWED_ACE_TYPE, interactiveUserPipeAccess, sidInteractive},
	}
	if len(got) != len(want) {
		t.Fatalf("DACL has %d entries, want %d: %+v", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("DACL entry %d = %+v, want %+v", index, got[index], want[index])
		}
	}
}

func TestPipeDACLGrantsNothingToEveryoneAnonymousNetworkOrOrdinaryGroups(t *testing.T) {
	server, _ := connectPair(t, listenForTest(t, uniquePipePath()), winio.PipeImpLevelIdentification)

	entries, _ := readPipeDACL(t, server)

	unwanted := map[string]bool{sidEveryone: true, sidAnonymous: true, sidNetwork: true, sidAuthenticated: true, sidUsers: true}
	for _, entry := range entries {
		if unwanted[entry.sid] && entry.aceType != windows.ACCESS_DENIED_ACE_TYPE {
			t.Errorf("DACL allows %s: %+v", entry.sid, entry)
		}
		if entry.sid == sidInteractive && entry.mask&windows.FILE_APPEND_DATA != 0 {
			t.Errorf("interactive users hold FILE_APPEND_DATA, which creates pipe instances: %+v", entry)
		}
	}
}

func TestListenIgnoresTheModeParameter(t *testing.T) {
	var reference []accessControlEntry
	for _, mode := range []os.FileMode{0, 0o600, 0o666, 0o777} {
		listener, err := Listen(uniquePipePath(), mode)
		if err != nil {
			t.Fatalf("mode %v: %v", mode, err)
		}
		t.Cleanup(func() { listener.Close() })

		// Mode 0 would mean "no access" on Unix; here every user the DACL names
		// must still get in.
		server, _ := connectPair(t, listener, winio.PipeImpLevelIdentification)
		entries, _ := readPipeDACL(t, server)
		if reference == nil {
			reference = entries
			continue
		}
		if fmt.Sprint(entries) != fmt.Sprint(reference) {
			t.Errorf("mode %v changed the DACL: %+v, want %+v", mode, entries, reference)
		}
	}
}

// impersonateOrdinaryInteractiveUser runs action on a thread whose token is
// this user's, but with the user SID reduced to deny-only. The access check then
// matches no ACE for the user itself and sees what any other interactive user
// would: the Interactive group and nothing else.
func impersonateOrdinaryInteractiveUser(t *testing.T, action func()) {
	t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	token := restrictedImpersonationToken(t)
	defer token.Close()
	if err := windows.SetThreadToken(nil, token); err != nil {
		t.Fatal(err)
	}
	defer windows.RevertToSelf()
	action()
}

// administratorsGroupOf returns the Administrators group when the token has it,
// elevated or not: an ordinary user is not one, and an elevated process would
// otherwise be let in by the pipe's entry for administrators.
func administratorsGroupOf(t *testing.T, token windows.Token) []windows.SIDAndAttributes {
	t.Helper()
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := token.GetTokenGroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range groups.AllGroups() {
		if group.Sid.Equals(administrators) {
			return []windows.SIDAndAttributes{{Sid: group.Sid}}
		}
	}
	return nil
}

var procCreateRestrictedToken = windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken")

func restrictedImpersonationToken(t *testing.T) windows.Token {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	disable := []windows.SIDAndAttributes{{Sid: user.User.Sid}}
	var own windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY, &own); err != nil {
		t.Fatal(err)
	}
	defer own.Close()
	disable = append(disable, administratorsGroupOf(t, own)...)
	var restricted windows.Token
	result, _, callErr := procCreateRestrictedToken.Call(
		uintptr(own), 0,
		uintptr(len(disable)), uintptr(unsafe.Pointer(&disable[0])),
		0, 0, 0, 0,
		uintptr(unsafe.Pointer(&restricted)))
	if result == 0 {
		t.Fatalf("CreateRestrictedToken: %v", callErr)
	}
	defer restricted.Close()

	var impersonation windows.Token
	if err := windows.DuplicateTokenEx(restricted, windows.TOKEN_ALL_ACCESS, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &impersonation); err != nil {
		t.Fatal(err)
	}
	return impersonation
}

func createExtraPipeInstance(path string) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := windows.CreateNamedPipe(name, windows.PIPE_ACCESS_DUPLEX, windows.PIPE_TYPE_BYTE|windows.PIPE_REJECT_REMOTE_CLIENTS, 255, 4096, 4096, 0, nil)
	if err != nil {
		return err
	}
	return windows.CloseHandle(handle)
}

// openPipe opens the pipe once, waiting while the server has no instance in the
// listening state yet (the same retry go-winio's Dial makes).
func openPipe(path string, access uint32) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	deadline := time.Now().Add(testTimeout)
	for {
		handle, err := windows.CreateFile(name, access, 0, nil, windows.OPEN_EXISTING, 0, 0)
		if err == nil {
			return handle, nil
		}
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) || time.Now().After(deadline) {
			return 0, err
		}
		time.Sleep(pipeBusyRetryInterval)
	}
}

// openPipeExpectingDenial fails the test unless the open is refused with
// access denied.
func openPipeExpectingDenial(t *testing.T, description, path string, access uint32) {
	t.Helper()
	handle, err := openPipe(path, access)
	if err == nil {
		windows.CloseHandle(handle)
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Errorf("%s got %v, want access denied", description, err)
	}
}
func TestInteractiveUserCanConnectButCannotAddPipeInstances(t *testing.T) {
	path := uniquePipePath()
	listener := listenForTest(t, path)

	// The daemon's own user is a legitimate server: this is what Accept does.
	if err := createExtraPipeInstance(path); err != nil {
		t.Fatalf("the daemon's own user cannot add an instance: %v", err)
	}

	accepted := acceptAsync(listener)
	impersonateOrdinaryInteractiveUser(t, func() {
		if err := createExtraPipeInstance(path); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			t.Errorf("an interactive user adding a pipe instance got %v, want access denied", err)
		}
		openPipeExpectingDenial(t, "an interactive user opening with generic read/write", path, windows.GENERIC_READ|windows.GENERIC_WRITE)
		handle, err := openPipe(path, interactiveUserPipeAccess)
		if err != nil {
			t.Errorf("an interactive user cannot connect with the client access mask: %v", err)
			return
		}
		// Stay connected until the server has accepted: a client that hangs up
		// first makes go-winio drop the connection and wait for the next one.
		defer windows.CloseHandle(handle)
		awaitAccepted(t, accepted)
	})
}

// Over SMB a pipe is reachable by name from other machines, and loopback to
// 127.0.0.1 takes the same network-logon path.
func TestPipeIsNotReachableOverSMB(t *testing.T) {
	path := uniquePipePath()
	listener := listenForTest(t, path)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	// A pipe without the reject flag proves that loopback SMB works here at all.
	controlName := path + "-control"
	control, err := windows.UTF16PtrFromString(controlName)
	if err != nil {
		t.Fatal(err)
	}
	controlHandle, err := windows.CreateNamedPipe(control, windows.PIPE_ACCESS_DUPLEX, windows.PIPE_TYPE_BYTE, 255, 4096, 4096, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(controlHandle)

	loopback := func(local string) string { return `\\127.0.0.1\pipe\` + local[len(pipeNamespace):] }
	controlClient, err := openPipe(loopback(controlName), windows.GENERIC_READ|windows.GENERIC_WRITE)
	if err != nil {
		t.Skipf("loopback SMB to a plain pipe does not work on this machine: %v", err)
	}
	windows.CloseHandle(controlClient)

	openPipeExpectingDenial(t, "opening the pipe through SMB", loopback(path), interactiveUserPipeAccess)
}

func TestAnonymousClientConnectsButPeerLookupRefusesIt(t *testing.T) {
	server, _ := connectPair(t, listenForTest(t, uniquePipePath()), winio.PipeImpLevelAnonymous)

	if _, _, err := peercred.NewServerCredentials().ServerHandshake(server); err == nil {
		t.Fatal("a client that cannot be identified was accepted")
	}
}

func TestGRPCCallFromAnonymousClientFails(t *testing.T) {
	path := uniquePipePath()
	callers := healthServer(t, listenForTest(t, path))
	anonymousDialer := grpc.WithContextDialer(func(ctx context.Context, address string) (net.Conn, error) {
		return winio.DialPipeAccessImpLevel(ctx, address, interactiveUserPipeAccess, winio.PipeImpLevelAnonymous)
	})

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	_, err := newClient(t, path, anonymousDialer).Check(ctx, &healthpb.HealthCheckRequest{})

	if status.Code(err) != codes.Unavailable {
		t.Fatalf("call from an anonymous client got %v, want Unavailable", err)
	}
	select {
	case caller := <-callers:
		t.Fatalf("handler ran for an anonymous client: %+v", caller)
	default:
	}
}
