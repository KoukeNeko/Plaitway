//go:build windows

package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
	"google.golang.org/grpc"
)

const pipeNamespace = `\\.\pipe\`

// interactiveUserPipeAccess is what a client may do with the pipe: read, write
// and wait on it, read its owner and DACL (READ_CONTROL, which is how a client
// checks who serves the pipe, see the package comment), plus the attribute read
// that CreateFile adds to every open. It deliberately leaves out
// FILE_APPEND_DATA, which on a pipe is FILE_CREATE_PIPE_INSTANCE: GENERIC_WRITE
// maps to it, so granting generic read/write would let any interactive user
// create a second server instance of the pipe and answer the clients that land
// on it. Clients must therefore open the pipe with exactly this mask
// (DialOptions does), not with GENERIC_*.
const interactiveUserPipeAccess = windows.FILE_READ_DATA | windows.FILE_WRITE_DATA | windows.FILE_READ_ATTRIBUTES | windows.READ_CONTROL | windows.SYNCHRONIZE

// DefaultPath is a named pipe, not a file.
func DefaultPath() string { return pipeNamespace + "plaitway" }

// Listen creates the pipe with an explicit DACL at creation time. The DACL
// replaces the Unix socket mode, which is ignored here and must not be read as
// access control. go-winio creates the first instance with FILE_CREATE, so a
// pipe that already exists is an error, and with PIPE_REJECT_REMOTE_CLIENTS, so
// the pipe is not reachable over SMB.
func Listen(path string, _ os.FileMode) (net.Listener, error) {
	if err := checkPipePath(path); err != nil {
		return nil, err
	}
	descriptor, err := pipeSecurityDescriptor()
	if err != nil {
		return nil, err
	}
	listener, err := winio.ListenPipe(path, &winio.PipeConfig{SecurityDescriptor: descriptor})
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		// FILE_CREATE on a name that exists reports access denied, which reads like
		// a permission problem when another daemon already serves the pipe.
		return nil, fmt.Errorf("another process already serves %s: %w", path, err)
	}
	return listener, err
}

// checkPipePath refuses anything that is not a local named pipe, because
// go-winio would otherwise hand a file path to the pipe file system and report
// an error that says nothing about the path being wrong.
func checkPipePath(path string) error {
	hasNamespace := len(path) > len(pipeNamespace) && strings.EqualFold(path[:len(pipeNamespace)], pipeNamespace)
	if !hasNamespace {
		return fmt.Errorf("%q is not a local named pipe path, want %s<name>", path, pipeNamespace)
	}
	return nil
}

// pipeSecurityDescriptor is protected (no inheritance) and ordered so that a
// network logon is denied before any allow can match, even for an administrator
// and even if the reject-remote-clients flag were ever dropped. SYSTEM and
// Administrators have full control; the process's own user too, because without
// it an unelevated development daemon could create the pipe but not accept on
// it (its Administrators group is deny-only). Interactive users get
// interactiveUserPipeAccess. The owner is left to the creator's default, which
// is what clients check (serverowner_windows.go): an unprivileged process cannot
// make itself the owner as SYSTEM or Administrators.
func pipeSecurityDescriptor() (string, error) {
	ownSID, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("read the daemon's own user: %w", err)
	}
	return fmt.Sprintf("D:P(D;;GA;;;NU)(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;%s)(A;;0x%x;;;IU)",
		ownSID.User.Sid, interactiveUserPipeAccess), nil
}

// Target uses grpc-go's passthrough resolver so the pipe name reaches the
// custom dialer untouched.
func Target(path string) string { return "passthrough:///" + path }

// DialOptions connects with identification level: the daemon reads the caller's
// token to decide what it may do, which an anonymous client does not allow. The
// daemon can look at the token but cannot act as the caller. A connection is
// handed to gRPC only after the pipe's owner has been checked (dialVerified), so
// a squatted pipe never receives a request.
func DialOptions() []grpc.DialOption { return dialOptionsAs(currentUserSID) }

// dialOptionsAs takes the caller's identity as a function so that a test can
// pose as another user. An error from it refuses the connection.
func dialOptionsAs(lookupCaller func() (*windows.SID, error)) []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithContextDialer(func(ctx context.Context, address string) (net.Conn, error) {
			caller, err := lookupCaller()
			if err != nil {
				return nil, err
			}
			return dialVerified(ctx, address, caller)
		}),
	}
}
