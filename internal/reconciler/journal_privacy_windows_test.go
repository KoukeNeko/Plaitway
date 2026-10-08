package reconciler

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/fsperm"
)

func requirePrivate(t *testing.T, step, path string) {
	t.Helper()
	private, err := fsperm.IsPrivate(path)
	if err != nil {
		t.Fatalf("%s: %v", step, err)
	}
	if !private {
		t.Errorf("%s: %s is readable by accounts other than SYSTEM, Administrators and the owner", step, path)
	}
}

// The journal lists every route the daemon installed, so it is private the way
// the Unix mode bits make it: the file is created inside a directory with a
// protected list and takes that list, and stays that way through compaction,
// the temporary file and a restart.
func TestJournalStaysPrivateThroughCompactionAndRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := fsperm.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "journal")
	tmp := path + ".tmp"
	j := testJournal(t, path)
	j.compactAfter = 3
	requirePrivate(t, "after open", path)

	for _, key := range []string{"1.1.1.1/32", "2.2.2.2/32", "3.3.3.3/32", "4.4.4.4/32"} {
		for _, state := range []string{statePending, stateApplied} {
			if err := j.append(routeRecord(key, state)); err != nil {
				t.Fatal(err)
			}
		}
	}
	requirePrivate(t, "after compaction", path)
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("the temporary file outlived the compaction: %v", err)
	}

	// A compaction that cannot finish leaves journal.tmp behind, which is the
	// only time it can be looked at.
	release := holdOpen(t, path)
	if err := j.rewrite(); err == nil {
		t.Fatal("rewrite succeeded under a held file")
	}
	requirePrivate(t, "journal.tmp of a failed compaction", tmp)
	requirePrivate(t, "journal after a failed compaction", path)
	release()
	if err := j.close(); err != nil {
		t.Fatal(err)
	}

	restarted := testJournal(t, path)
	requirePrivate(t, "after restart", path)
	if err := restarted.append(routeRecord("5.5.5.5/32", statePending)); err != nil {
		t.Fatal(err)
	}
	requirePrivate(t, "after restart and append", path)
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("the restart left the temporary file behind: %v", err)
	}
}
