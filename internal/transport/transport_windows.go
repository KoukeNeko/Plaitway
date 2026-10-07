//go:build windows

package transport

import (
	"context"
	"net"
	"os"

	"github.com/Microsoft/go-winio"
	"google.golang.org/grpc"
)

// DefaultPath is a named pipe, not a file.
func DefaultPath() string { return `\\.\pipe\plaitway` }

// Listen creates the pipe with an explicit DACL at creation time: protected
// (no inheritance), full control for SYSTEM and Administrators, read/write for
// interactively logged-on users. The DACL replaces the Unix socket mode, which
// is ignored. Compile-checked only; not yet run on Windows.
func Listen(path string, _ os.FileMode) (net.Listener, error) {
	return winio.ListenPipe(path, &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;IU)",
	})
}

// Target uses grpc-go's passthrough resolver so the pipe name reaches the
// custom dialer untouched.
func Target(path string) string { return "passthrough:///" + path }

func DialOptions() []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			return winio.DialPipeContext(ctx, addr)
		}),
	}
}
