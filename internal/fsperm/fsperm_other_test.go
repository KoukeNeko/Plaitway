//go:build !windows

package fsperm

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func mustMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != want {
		t.Errorf("%s mode = %v, want %v", path, fi.Mode().Perm(), want)
	}
}

func TestRestrictTightensALooseDirectoryToOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if private, err := IsPrivate(dir); err != nil || private {
		t.Fatalf("IsPrivate of 0755 = %v, %v", private, err)
	}

	changed, err := Restrict(dir)
	if err != nil || !changed {
		t.Fatalf("Restrict = %v, %v; want it to tighten the mode", changed, err)
	}
	mustMode(t, dir, 0o700)
	if changed, err := Restrict(dir); err != nil || changed {
		t.Fatalf("second Restrict = %v, %v; want nothing left to do", changed, err)
	}
}

func TestRestrictTightensALooseFileTo0600(t *testing.T) {
	file := filepath.Join(t.TempDir(), "plaitwayd.log")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0o644); err != nil { // the umask may have narrowed it
		t.Fatal(err)
	}

	if changed, err := Restrict(file); err != nil || !changed {
		t.Fatalf("Restrict = %v, %v", changed, err)
	}
	mustMode(t, file, 0o600)
}

func TestRestrictLeavesAnOwnerOnlyModeAlone(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)

	if changed, err := Restrict(dir); err != nil || changed {
		t.Fatalf("Restrict = %v, %v; want it to leave 0500 as it is", changed, err)
	}
	mustMode(t, dir, 0o500)
}

func TestMkdirAllUsesTheModeItIsGiven(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run", "plaitway")
	if err := MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil { // the umask may have narrowed it
		t.Fatal(err)
	}
	if err := MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	mustMode(t, dir, 0o755) // an existing directory is left as it is
}

func TestOpenAppendCreatesAnOwnerOnlyFileAndAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plaitwayd.log")
	for _, line := range []string{"first\n", "second\n"} {
		f, err := OpenAppend(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(line); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "first\nsecond\n" {
		t.Fatalf("file = %q, %v", got, err)
	}
	mustMode(t, path, 0o600)
}

// The warning of a tightened directory keeps the text and the attributes that
// operators of the daemon already know: the mode that was found is logged.
func TestRestrictAndWarnLogsTheModeThatWasFound(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var logged bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
		if attr.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return attr
	}}))

	if err := RestrictAndWarn(log, "state directory", dir); err != nil {
		t.Fatal(err)
	}
	want := `level=WARN msg="state directory was accessible to others, restricted to 0700" dir=` + dir + " was=-rwxr-xr-x\n"
	if logged.String() != want {
		t.Fatalf("logged %q, want %q", logged.String(), want)
	}
	mustMode(t, dir, 0o700)

	logged.Reset()
	if err := RestrictAndWarn(log, "state directory", dir); err != nil || logged.Len() != 0 {
		t.Fatalf("second call = %v, logged %q; want nothing", err, logged.String())
	}
}

func TestIsPrivateFailsForAMissingPath(t *testing.T) {
	if _, err := IsPrivate(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("IsPrivate of a missing path succeeded")
	}
}
