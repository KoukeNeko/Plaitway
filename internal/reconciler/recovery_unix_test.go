//go:build unix

package reconciler

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// File mode bits are the journal's access control on Unix. Windows ignores
// them; there the directory's ACL decides, which the service sets up.
func TestJournalFileIsPrivate(t *testing.T) { eachKeying(t, testJournalFileIsPrivate) }

func testJournalFileIsPrivate(t *testing.T, k keyingCase) {
	e := newEnv(t, k)
	e.addTunnel("utun1", "10.1.0.2/24")
	e.announce(up("a", 1, "utun1", tunnel.RoleSplit, "10.1.0.0/16"))
	info, err := os.Stat(e.journal)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("journal mode %v", perm)
	}
	dir, err := os.Stat(filepath.Dir(e.journal))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dir.Mode().Perm(); perm != 0o700 {
		t.Errorf("journal directory mode %v", perm)
	}
}
