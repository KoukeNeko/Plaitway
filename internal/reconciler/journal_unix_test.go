//go:build unix

package reconciler

import (
	"os"
	"path/filepath"
	"testing"
)

// When the compaction cannot replace the journal, the handle that is still
// recording stays open and the next append tries the compaction again, so the
// failure keeps showing up in the log.
func TestJournalRetriesCompactionAfterAFailedReplace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal")
	j := testJournal(t, path)
	j.compactAfter = 2
	recording := j.file
	// A directory in the journal's place makes the rename fail while the open
	// handle keeps pointing at the unlinked file.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "occupied"), 0o700); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{"1.1.1.1/32", "2.2.2.2/32", "3.3.3.3/32"} {
		if err := j.append(routeRecord(key, statePending)); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if j.file != recording {
		t.Error("the append handle was replaced although the replacement failed")
	}
	if j.appended != 3 {
		t.Errorf("appended is %d, want 3: the failed compaction must be retried on every append", j.appended)
	}
}
