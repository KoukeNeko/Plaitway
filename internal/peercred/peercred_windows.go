package peercred

import (
	"fmt"
	"net"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procImpersonateNamedPipeClient = windows.NewLazySystemDLL("advapi32.dll").NewProc("ImpersonateNamedPipeClient")

// lookup reads the identity from the token of the process that opened the pipe.
// The token comes from impersonating the client, not from its pid: the pid can
// be reused by another process between connect and lookup, the pipe's security
// context cannot. The client has to dial with at least identification level
// (transport.DialOptions does); an anonymous client has no identity and is
// refused.
func lookup(conn net.Conn) (Info, error) {
	file, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return Info{}, fmt.Errorf("not a named pipe connection: %T", conn)
	}
	pipe := windows.Handle(file.Fd())

	var pid uint32
	if err := windows.GetNamedPipeClientProcessId(pipe, &pid); err != nil {
		return Info{}, fmt.Errorf("GetNamedPipeClientProcessId: %w", err)
	}
	token, err := openClientToken(pipe)
	if err != nil {
		return Info{}, err
	}
	defer token.Close()

	identity, err := identityFromToken(token)
	if err != nil {
		return Info{}, err
	}
	return Info{PID: int32(pid), Windows: identity}, nil
}

// openClientToken impersonates the client on this thread just long enough to
// copy the thread token, and always reverts.
func openClientToken(pipe windows.Handle) (windows.Token, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if result, _, err := procImpersonateNamedPipeClient.Call(uintptr(pipe)); result == 0 {
		return 0, fmt.Errorf("ImpersonateNamedPipeClient: %w", err)
	}
	defer windows.RevertToSelf()

	thread, err := windows.GetCurrentThread()
	if err != nil {
		return 0, fmt.Errorf("GetCurrentThread: %w", err)
	}
	var token windows.Token
	// OpenAsSelf: the access check is made against this process, not against the
	// identity that is being impersonated.
	if err := windows.OpenThreadToken(thread, windows.TOKEN_QUERY, true, &token); err != nil {
		return 0, fmt.Errorf("OpenThreadToken: %w", err)
	}
	return token, nil
}

func identityFromToken(token windows.Token) (*WindowsIdentity, error) {
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("token user: %w", err)
	}
	groups, err := token.GetTokenGroups()
	if err != nil {
		return nil, fmt.Errorf("token groups: %w", err)
	}
	administrator, err := isAdministrator(groups)
	if err != nil {
		return nil, err
	}
	session, err := tokenSessionID(token)
	if err != nil {
		return nil, err
	}
	return &WindowsIdentity{SID: user.User.Sid.String(), Administrator: administrator, SessionID: session}, nil
}

// isAdministrator is true when the token has the Administrators group, enabled
// or deny-only. A user account that is an administrator runs ordinary programs
// with a filtered token, where the group is deny-only: such a caller is the
// counterpart of a macOS user in the admin group, who needs no sudo to be one.
func isAdministrator(groups *windows.Tokengroups) (bool, error) {
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false, fmt.Errorf("Administrators SID: %w", err)
	}
	const memberOf = windows.SE_GROUP_ENABLED | windows.SE_GROUP_USE_FOR_DENY_ONLY
	for _, group := range groups.AllGroups() {
		if group.Attributes&memberOf != 0 && group.Sid.Equals(administrators) {
			return true, nil
		}
	}
	return false, nil
}

func tokenSessionID(token windows.Token) (uint32, error) {
	var session, written uint32
	if err := windows.GetTokenInformation(token, windows.TokenSessionId, (*byte)(unsafe.Pointer(&session)), uint32(unsafe.Sizeof(session)), &written); err != nil {
		return 0, fmt.Errorf("token session: %w", err)
	}
	return session, nil
}
