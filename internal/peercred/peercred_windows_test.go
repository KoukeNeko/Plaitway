package peercred

import (
	"context"
	"fmt"
	"net"
	"os"
	"runtime"
	"testing"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// acceptedPipe connects a client of this process to a new pipe and returns the
// accepted end.
func acceptedPipe(t *testing.T, level winio.PipeImpLevel) net.Conn {
	t.Helper()
	path := fmt.Sprintf(`\\.\pipe\plaitway-peercred-test-%d-%s`, os.Getpid(), t.Name())
	listener, err := winio.ListenPipe(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	accepted := make(chan net.Conn, 1)
	go func() {
		if conn, err := listener.Accept(); err == nil {
			accepted <- conn
		}
	}()
	client, err := winio.DialPipeAccessImpLevel(context.Background(), path, windows.GENERIC_READ|windows.GENERIC_WRITE, level)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	server := <-accepted
	t.Cleanup(func() { server.Close() })
	return server
}

func TestServerHandshakeReportsConnectingProcess(t *testing.T) {
	server := acceptedPipe(t, winio.PipeImpLevelIdentification)

	_, authInfo, err := NewServerCredentials().ServerHandshake(server)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := authInfo.(AuthInfo)
	if !ok || !got.Known || got.Windows == nil {
		t.Fatalf("auth info = %#v, want a known AuthInfo with a Windows identity", authInfo)
	}
	// The client end lives in this process.
	if got.PID != int32(os.Getpid()) {
		t.Fatalf("peer pid = %d, want %d", got.PID, os.Getpid())
	}
	own, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if got.Windows.SID != own.User.Sid.String() {
		t.Fatalf("peer SID = %s, want %s", got.Windows.SID, own.User.Sid)
	}
	var ownSession uint32
	if err := windows.ProcessIdToSessionId(uint32(os.Getpid()), &ownSession); err != nil {
		t.Fatal(err)
	}
	if got.Windows.SessionID != ownSession {
		t.Fatalf("peer session = %d, want %d", got.Windows.SessionID, ownSession)
	}
	want := administratorByMembership(t, windows.GetCurrentProcessToken())
	if got.Windows.Administrator != want {
		t.Fatalf("peer Administrator = %v, but the system's membership check says %v", got.Windows.Administrator, want)
	}
}

// tokenElevationTypeLimited is TokenElevationTypeLimited: the filtered half of
// an administrator's split token, whose linked token is the elevated half.
const tokenElevationTypeLimited = 3

// administratorByMembership answers the question isAdministrator answers
// without reading the group list. CheckTokenMembership counts enabled groups
// only, so the filtered half of a split token is found through the elevated
// token it is linked to.
func administratorByMembership(t *testing.T, token windows.Token) bool {
	t.Helper()
	administrators := wellKnownSID(t, windows.WinBuiltinAdministratorsSid)
	enabled, err := token.IsMember(administrators)
	if err != nil {
		t.Fatalf("IsMember: %v", err)
	}
	if enabled {
		return true
	}
	var elevationType, written uint32
	if err := windows.GetTokenInformation(token, windows.TokenElevationType, (*byte)(unsafe.Pointer(&elevationType)), uint32(unsafe.Sizeof(elevationType)), &written); err != nil {
		t.Fatalf("token elevation type: %v", err)
	}
	if elevationType != tokenElevationTypeLimited {
		return false
	}
	linked, err := token.GetLinkedToken()
	if err != nil {
		t.Fatalf("linked token: %v", err)
	}
	defer linked.Close()
	elevated, err := linked.IsMember(administrators)
	if err != nil {
		t.Fatalf("IsMember on the linked token: %v", err)
	}
	return elevated
}

func wellKnownSID(t *testing.T, kind windows.WELL_KNOWN_SID_TYPE) *windows.SID {
	t.Helper()
	sid, err := windows.CreateWellKnownSid(kind)
	if err != nil {
		t.Fatalf("CreateWellKnownSid(%d): %v", kind, err)
	}
	return sid
}

// tokenGroups lays the entries out the way GetTokenInformation does: a count
// followed by the entries in the same buffer, which is what Tokengroups'
// variable length stands for.
func tokenGroups(t *testing.T, entries ...windows.SIDAndAttributes) *windows.Tokengroups {
	t.Helper()
	entrySize := unsafe.Sizeof(windows.SIDAndAttributes{})
	headerSize := unsafe.Offsetof(windows.Tokengroups{}.Groups)
	// Not fewer than one entry: Tokengroups declares Groups as [1].
	size := headerSize + max(uintptr(len(entries)), 1)*entrySize
	// uint64 words keep the buffer aligned for the pointers it holds.
	buffer := make([]uint64, (size+7)/8)
	groups := (*windows.Tokengroups)(unsafe.Pointer(&buffer[0]))
	groups.GroupCount = uint32(len(entries))
	for i, entry := range entries {
		*(*windows.SIDAndAttributes)(unsafe.Add(unsafe.Pointer(&groups.Groups[0]), uintptr(i)*entrySize)) = entry
	}
	// The SIDs are referenced from memory the collector does not scan.
	t.Cleanup(func() { runtime.KeepAlive(entries) })
	return groups
}

func TestAdministratorIsDecidedByTheGroupAndItsAttributes(t *testing.T) {
	const (
		enabled  = windows.SE_GROUP_MANDATORY | windows.SE_GROUP_ENABLED_BY_DEFAULT | windows.SE_GROUP_ENABLED
		denyOnly = windows.SE_GROUP_MANDATORY | windows.SE_GROUP_ENABLED_BY_DEFAULT | windows.SE_GROUP_USE_FOR_DENY_ONLY
		// Enabled by default but switched off: the group is present and does
		// not count.
		disabled = windows.SE_GROUP_MANDATORY | windows.SE_GROUP_ENABLED_BY_DEFAULT
	)
	administrators := wellKnownSID(t, windows.WinBuiltinAdministratorsSid)
	users := wellKnownSID(t, windows.WinBuiltinUsersSid)
	everyone := wellKnownSID(t, windows.WinWorldSid)

	tests := []struct {
		name   string
		groups []windows.SIDAndAttributes
		want   bool
	}{
		{"Administrators enabled", []windows.SIDAndAttributes{{Sid: administrators, Attributes: enabled}}, true},
		{"Administrators deny-only (filtered administrator)", []windows.SIDAndAttributes{{Sid: administrators, Attributes: denyOnly}}, true},
		{"Administrators disabled", []windows.SIDAndAttributes{{Sid: administrators, Attributes: disabled}}, false},
		{"Administrators without attributes", []windows.SIDAndAttributes{{Sid: administrators}}, false},
		{"Administrators absent", []windows.SIDAndAttributes{{Sid: users, Attributes: enabled}, {Sid: everyone, Attributes: enabled}}, false},
		{"no groups", nil, false},
		{"another group enabled", []windows.SIDAndAttributes{{Sid: users, Attributes: enabled}}, false},
		{"another group deny-only", []windows.SIDAndAttributes{{Sid: users, Attributes: denyOnly}}, false},
		{"Administrators disabled beside enabled groups", []windows.SIDAndAttributes{{Sid: users, Attributes: enabled}, {Sid: administrators, Attributes: disabled}, {Sid: everyone, Attributes: enabled}}, false},
		{"Administrators enabled after other groups", []windows.SIDAndAttributes{{Sid: users, Attributes: enabled}, {Sid: everyone, Attributes: enabled}, {Sid: administrators, Attributes: enabled}}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := isAdministrator(tokenGroups(t, test.groups...))
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("isAdministrator = %v, want %v", got, test.want)
			}
		})
	}
}

func TestAnonymousClientIsRefused(t *testing.T) {
	server := acceptedPipe(t, winio.PipeImpLevelAnonymous)

	if _, _, err := NewServerCredentials().ServerHandshake(server); err == nil {
		t.Fatal("a client that cannot be identified was accepted")
	}
}
