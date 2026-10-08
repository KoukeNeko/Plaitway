package profile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomicReplacesTheFileAndLeavesNothingElse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.json")
	for _, content := range []string{"first", "second, and longer"} {
		if err := writeFileAtomic(path, []byte(content)); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != content {
			t.Fatalf("file = %q, %v; want %q", got, err, content)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("directory holds %v, want only the file", entries)
	}
}

func TestWriteFileAtomicFailsAndCleansUpWhenThePathIsADirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.json")
	if err := os.MkdirAll(filepath.Join(path, "inside"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := writeFileAtomic(path, []byte("data")); err == nil {
		t.Fatal("writeFileAtomic replaced a directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("directory holds %v, want the temporary file removed", entries)
	}
}
