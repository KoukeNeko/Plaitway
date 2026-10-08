//go:build windows

package profile

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A reader with the old file open makes the rename fail with "access denied"
// until it lets go; a short wait must get the write through.
func TestReplaceFileWaitsForAReaderToLetGo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		defer close(released)
		time.Sleep(3 * replaceRetryDelay)
		reader.Close()
	}()

	err = writeFileAtomic(path, []byte("new"))
	<-released
	if err != nil {
		t.Fatalf("writeFileAtomic while a reader had the file open: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Fatalf("file = %q, want the new content", got)
	}
}

// A reader that never lets go is reported, after the retries and no later.
func TestReplaceFileGivesUpOnAReaderThatStays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	start := time.Now()
	if err := writeFileAtomic(path, []byte("new")); err == nil {
		t.Fatal("writeFileAtomic replaced a file that is held open")
	}
	if elapsed := time.Since(start); elapsed > 10*replaceAttempts*replaceRetryDelay {
		t.Errorf("giving up took %v", elapsed)
	}
	if got, _ := os.ReadFile(path); string(got) != "old" {
		t.Fatalf("file = %q, want the old content kept", got)
	}
}
