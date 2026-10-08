package reconciler

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// holdOpen opens path the way a virus scanner does, without letting anyone
// replace it. The returned function lets go.
func holdOpen(t *testing.T, path string) (release func()) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release = func() { once.Do(func() { f.Close() }) }
	t.Cleanup(release)
	return release
}

// A rename onto a file that is open fails right away, so the premise of the
// tests below is checked first.
func requireReplaceBlockedBy(t *testing.T, path string) {
	t.Helper()
	probe := path + ".probe"
	if err := os.WriteFile(probe, []byte("probe"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := os.Rename(probe, path)
	os.Remove(probe)
	if err == nil || !isFileInUse(err) {
		t.Fatalf("an open file did not block the rename: %v", err)
	}
}

// Another program that has the journal open for a moment must not fail the
// compaction, nor a daemon start.
func TestJournalReplaceWaitsForAnotherProgramToLetGo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal")
	j := testJournal(t, path)
	if err := j.append(routeRecord("1.1.1.1/32", statePending)); err != nil {
		t.Fatal(err)
	}
	// The journal lets go of its own handle in rewrite, so this is the only one.
	release := holdOpen(t, path)
	requireReplaceBlockedBy(t, path)
	time.AfterFunc(200*time.Millisecond, release)

	if err := j.rewrite(); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := j.append(routeRecord("2.2.2.2/32", statePending)); err != nil {
		t.Fatalf("append after rewrite: %v", err)
	}
	j.close()
	if got := testJournal(t, path).unresolved(); len(got) != 2 {
		t.Errorf("unresolved after restart: %+v", got)
	}
}

// If the file stays held, the compaction fails but the journal goes on
// recording: the write-ahead guarantee does not depend on the compaction.
func TestJournalKeepsRecordingWhenItCannotBeReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal")
	j := testJournal(t, path)
	if err := j.append(routeRecord("1.1.1.1/32", statePending)); err != nil {
		t.Fatal(err)
	}
	release := holdOpen(t, path)

	if err := j.rewrite(); err == nil {
		t.Fatal("rewrite succeeded under a held file")
	}
	if err := j.append(routeRecord("2.2.2.2/32", statePending)); err != nil {
		t.Fatalf("append after the failed rewrite: %v", err)
	}
	j.close()
	release()
	if got := testJournal(t, path).unresolved(); len(got) != 2 {
		t.Errorf("unresolved after restart: %+v", got)
	}
}
