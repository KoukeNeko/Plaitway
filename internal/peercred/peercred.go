// Package peercred reads the identity of the process on the other end of a
// local socket and exposes it to gRPC handlers through peer.FromContext.
//
// It is a grpc-go TransportCredentials: ServerHandshake runs once per accepted
// connection, before any HTTP/2 traffic, receives the raw net.Conn, and returns
// an AuthInfo that grpc-go stores in the connection's peer.Peer.
package peercred

import (
	"context"
	"errors"
	"fmt"
	"net"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// Info identifies the connecting process. PID is 0 when the OS cannot say.
type Info struct {
	UID uint32
	// GID is the primary group.
	GID uint32
	// Groups is every group the kernel reports for the caller, GID included:
	// up to 16 on macOS (the xucred limit, a caller in more groups is
	// truncated), only GID on Linux (SO_PEERCRED has no supplementary groups).
	Groups []uint32
	PID    int32
}

// ErrUnsupported is returned by platforms without a peer-credential lookup.
var ErrUnsupported = errors.New("peercred: not supported on this OS")

// AuthInfo is what handlers find in peer.Peer.AuthInfo.
type AuthInfo struct {
	Info
	// Known is false only on OSes where lookup is unsupported (ErrUnsupported).
	Known bool
}

func (AuthInfo) AuthType() string { return "plaitway-peercred" }

// FromContext returns the caller identity for the RPC in ctx.
func FromContext(ctx context.Context) (AuthInfo, bool) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return AuthInfo{}, false
	}
	ai, ok := p.AuthInfo.(AuthInfo)
	return ai, ok
}

type serverCreds struct{}

// NewServerCredentials returns credentials for grpc.Creds on the server. It
// does no encryption: the Unix socket permissions are the access control and
// the uid is an identity input for per-RPC policy.
func NewServerCredentials() credentials.TransportCredentials { return serverCreds{} }

func (serverCreds) ServerHandshake(conn net.Conn) (net.Conn, credentials.AuthInfo, error) {
	info, err := lookup(conn)
	switch {
	case errors.Is(err, ErrUnsupported):
		return conn, AuthInfo{}, nil
	case err != nil:
		conn.Close()
		return nil, nil, fmt.Errorf("peercred: %w", err)
	}
	return conn, AuthInfo{Info: info, Known: true}, nil
}

func (serverCreds) ClientHandshake(context.Context, string, net.Conn) (net.Conn, credentials.AuthInfo, error) {
	return nil, nil, errors.New("peercred: server-side credentials only")
}

func (serverCreds) Info() credentials.ProtocolInfo {
	return credentials.ProtocolInfo{SecurityProtocol: "plaitway-peercred"}
}

func (c serverCreds) Clone() credentials.TransportCredentials { return c }

func (serverCreds) OverrideServerName(string) error { return nil }
