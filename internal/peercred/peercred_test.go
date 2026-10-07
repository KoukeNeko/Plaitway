//go:build darwin || linux

package peercred

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestServerHandshakeReportsConnectingProcess(t *testing.T) {
	dir, err := os.MkdirTemp("", "pw") // t.TempDir() is too long for sun_path on macOS
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "p.sock")

	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		if c, err := l.Accept(); err == nil {
			accepted <- c
		}
	}()
	client, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server := <-accepted
	defer server.Close()

	_, authInfo, err := NewServerCredentials().ServerHandshake(server)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := authInfo.(AuthInfo)
	if !ok || !got.Known {
		t.Fatalf("auth info = %#v, want a known AuthInfo", authInfo)
	}
	// The client end lives in this process.
	if got.UID != uint32(os.Getuid()) || got.PID != int32(os.Getpid()) {
		t.Fatalf("peer = uid %d pid %d, want uid %d pid %d", got.UID, got.PID, os.Getuid(), os.Getpid())
	}
	egid := uint32(os.Getegid())
	if got.GID != egid || !slices.Contains(got.Groups, egid) {
		t.Fatalf("peer gid = %d groups = %v, want gid %d among the groups", got.GID, got.Groups, egid)
	}
}
