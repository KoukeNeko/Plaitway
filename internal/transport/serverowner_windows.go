package transport

import (
	"context"
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// RefusedServerPrefix starts the text of every error that refuses a pipe
// server. gRPC reports a failed dial as a string, so a caller that wants to
// tell a refused server from a pipe that is missing can only look at the text.
const RefusedServerPrefix = "pipe server refused: "

// dialVerified opens the pipe and returns the connection only if its server is
// one of the identities documented in the package comment. Nothing is written
// to a server that fails the check.
func dialVerified(ctx context.Context, address string, caller *windows.SID) (net.Conn, error) {
	conn, err := winio.DialPipeAccessImpLevel(ctx, address, interactiveUserPipeAccess, winio.PipeImpLevelIdentification)
	if err != nil {
		return nil, err
	}
	if err := verifyServer(conn, caller); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func verifyServer(conn net.Conn, caller *windows.SID) error {
	handleHolder, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return fmt.Errorf("%scannot read the owner of a %T", RefusedServerPrefix, conn)
	}
	owner, err := pipeOwner(windows.Handle(handleHolder.Fd()))
	if err != nil {
		return fmt.Errorf("%scannot read its owner: %w", RefusedServerPrefix, err)
	}
	return checkServerOwner(owner, caller)
}

// pipeOwner reads the owner of the pipe instance behind a client handle, which
// needs READ_CONTROL on that handle (interactiveUserPipeAccess has it).
func pipeOwner(pipe windows.Handle) (*windows.SID, error) {
	descriptor, err := windows.GetSecurityInfo(pipe, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return nil, fmt.Errorf("GetSecurityInfo: %w", err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return nil, fmt.Errorf("security descriptor owner: %w", err)
	}
	return owner, nil
}

// checkServerOwner is the rule itself. SYSTEM and Administrators own what a
// service or an elevated daemon creates; the caller's own user owns what an
// unelevated development daemon creates. Anyone else, or an owner that is
// missing, is refused.
func checkServerOwner(owner, caller *windows.SID) error {
	if owner == nil || !owner.IsValid() {
		return fmt.Errorf("%sit has no valid owner", RefusedServerPrefix)
	}
	if owner.IsWellKnown(windows.WinLocalSystemSid) || owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
		return nil
	}
	if caller != nil && caller.IsValid() && owner.Equals(caller) {
		return nil
	}
	return fmt.Errorf("%sit is owned by %s, not by SYSTEM, Administrators or %s",
		RefusedServerPrefix, describeSID(owner), describeCaller(caller))
}

func describeCaller(caller *windows.SID) string {
	if caller == nil || !caller.IsValid() {
		return "the calling user"
	}
	return describeSID(caller)
}

// describeSID names the account when Windows can resolve it, always with the
// SID, which is the part that stays unambiguous.
func describeSID(sid *windows.SID) string {
	account, domain, _, err := sid.LookupAccount("")
	if err != nil {
		return sid.String()
	}
	return fmt.Sprintf(`%s\%s (%s)`, domain, account, sid)
}

// currentUserSID is the user the calling process runs as.
func currentUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("%scannot read the calling user: %w", RefusedServerPrefix, err)
	}
	return user.User.Sid, nil
}
