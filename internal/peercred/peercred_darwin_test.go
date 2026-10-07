package peercred

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The administrator check in the daemon depends on the complete group list,
// not only the primary group.
func TestLookupReportsSupplementaryGroupsOnDarwin(t *testing.T) {
	dir, err := os.MkdirTemp("", "pw") // t.TempDir() is too long for sun_path on macOS
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "g.sock")

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

	info, err := lookup(server)
	if err != nil {
		t.Fatal(err)
	}
	own, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, gid := range own {
		if !slices.Contains(info.Groups, uint32(gid)) {
			t.Errorf("group %d of this process is missing from %v", gid, info.Groups)
		}
	}
}
