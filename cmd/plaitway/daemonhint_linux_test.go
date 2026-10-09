package main

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// dialError is what gRPC says of a socket it could not connect to.
func dialError(socket, reason string) error {
	return status.Error(codes.Unavailable, `connection error: desc = "transport: Error while dialing: dial unix `+socket+`: connect: `+reason+`"`)
}

func TestFailureNamesTheUnitWhenTheProductionDaemonIsNotRunning(t *testing.T) {
	const hint = `; run "systemctl status plaitwayd"`
	for _, tt := range []struct {
		name, socket, reason, want string
	}{
		{"no socket", defaultSocket, "no such file or directory", "the daemon is not running: " + defaultSocket + " does not exist" + hint},
		{"nobody listens", defaultSocket, "connection refused", "the daemon is not running: nothing listens on " + defaultSocket + hint},
		// A daemon at another socket was started by hand.
		{"another socket", "/tmp/dev.sock", "no such file or directory", "the daemon is not running: /tmp/dev.sock does not exist"},
		// The unit is not what stops a user from opening the socket.
		{"permission", defaultSocket, "permission denied", "permission denied to open " + defaultSocket},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := (&client{socket: tt.socket}).failure(dialError(tt.socket, tt.reason))
			if got == nil || got.Error() != tt.want {
				t.Fatalf("failure = %v, want %q", got, tt.want)
			}
		})
	}
}
