package reconciler

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testJournal(t *testing.T, path string) *journal {
	t.Helper()
	j, err := openJournal(path, func() time.Time { return t0 }, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.close() })
	return j
}

func routeRecord(key, state string) record {
	return record{Owner: "wg", Kind: kindRoute, Key: key, State: state, Gateway: "192.168.51.1", Iface: "en0"}
}

func TestJournalKeepsWhatIsNotFinished(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "journal")
	j := testJournal(t, path)
	for _, rec := range []record{
		routeRecord("1.1.1.1/32", statePending),
		routeRecord("1.1.1.1/32", stateApplied),
		routeRecord("2.2.2.2/32", statePending),
		routeRecord("2.2.2.2/32", stateApplied),
		routeRecord("2.2.2.2/32", stateRemoved),
		routeRecord("3.3.3.3/32", statePending),
		{Owner: "wg", Kind: kindResolver, Key: "wg", State: stateApplied},
	} {
		if err := j.append(rec); err != nil {
			t.Fatal(err)
		}
	}
	j.close()

	again := testJournal(t, path)
	var got []string
	for _, rec := range again.unresolved() {
		got = append(got, rec.Kind+" "+rec.Key+" "+rec.State)
	}
	want := "route 1.1.1.1/32 applied|route 3.3.3.3/32 pending|resolver wg applied"
	if strings.Join(got, "|") != want {
		t.Errorf("unresolved:\n got %v\nwant %v", got, want)
	}
	// Opening drops the finished records from the file.
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "2.2.2.2") {
		t.Errorf("a removed route is still in the file:\n%s", data)
	}
	// Sequence numbers keep growing across runs.
	if err := again.append(routeRecord("4.4.4.4/32", statePending)); err != nil {
		t.Fatal(err)
	}
	last := again.unresolved()[len(again.unresolved())-1]
	if last.Seq <= 7 {
		t.Errorf("seq restarted: %d", last.Seq)
	}
}

func TestJournalSkipsLinesItCannotRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal")
	content := `not json at all
{"seq":1,"owner":"wg","kind":"route","key":"1.1.1.1/32","state":"applied"}

{"seq":2,"owner":"wg","kind":"route","key":"2.2.2.2/32","sta`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	j := testJournal(t, path)
	if got := j.unresolved(); len(got) != 1 || got[0].Key != "1.1.1.1/32" {
		t.Errorf("unresolved: %+v", got)
	}
}

// The file stays small: finished records are dropped when enough have been
// appended.
func TestJournalCompactsWhileRunning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal")
	j := testJournal(t, path)
	j.compactAfter = 10
	for i := range 25 {
		key := fmt.Sprintf("10.0.0.%d/32", i)
		for _, state := range []string{statePending, stateApplied, stateRemoved} {
			if err := j.append(routeRecord(key, state)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := j.append(routeRecord("9.9.9.9/32", stateApplied)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(data), "\n"); lines > 12 {
		t.Errorf("the file has %d lines after 76 appends", lines)
	}
	if !strings.Contains(string(data), "9.9.9.9/32") {
		t.Error("a live record was lost in compaction")
	}
	if got := j.unresolved(); len(got) != 1 {
		t.Errorf("unresolved: %+v", got)
	}
}

func TestJournalViewIsBounded(t *testing.T) {
	j := testJournal(t, filepath.Join(t.TempDir(), "journal"))
	for i := range journalView + 50 {
		if err := j.append(routeRecord(fmt.Sprintf("10.0.%d.%d/32", i/256, i%256), statePending)); err != nil {
			t.Fatal(err)
		}
	}
	view := j.view()
	if len(view) != journalView {
		t.Fatalf("view has %d records, want %d", len(view), journalView)
	}
	if view[len(view)-1].Key != fmt.Sprintf("10.0.%d.%d/32", (journalView+49)/256, (journalView+49)%256) {
		t.Errorf("the newest record must be last: %+v", view[len(view)-1])
	}
	view[0].Key = "changed"
	if j.view()[0].Key == "changed" {
		t.Error("view must be a copy")
	}
}
