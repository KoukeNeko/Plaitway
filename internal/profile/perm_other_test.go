//go:build !windows

package profile

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// The line a daemon on Unix logs when it tightens the state directory is the
// one it always logged, text and attributes alike: operators grep for it.
func TestOpenLogsTheUnixWarningOfTheStateDirectoryUnchanged(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	openStore(t, dir)
	loosen(t, dir)

	var logged bytes.Buffer
	if _, err := Open(dir, stubBackends(), slog.New(slog.NewTextHandler(&logged, withoutTime()))); err != nil {
		t.Fatal(err)
	}

	var want bytes.Buffer
	slog.New(slog.NewTextHandler(&want, withoutTime())).Warn(
		"state directory was accessible to others, restricted to 0700", "dir", dir, "was", os.FileMode(0o755))
	if logged.String() != want.String() {
		t.Fatalf("logged %q, want %q", logged.String(), want.String())
	}
	literal := `level=WARN msg="state directory was accessible to others, restricted to 0700" dir=` + dir + " was=-rwxr-xr-x\n"
	if logged.String() != literal {
		t.Fatalf("logged %q, want %q", logged.String(), literal)
	}
}

// withoutTime leaves the timestamp out of the lines, which is the only part of
// a line that differs between runs.
func withoutTime() *slog.HandlerOptions {
	return &slog.HandlerOptions{ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
		if attr.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return attr
	}}
}

// assertPrivate checks that only the owner reaches path, with exactly mode.
func assertPrivate(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != mode {
		t.Errorf("%s mode = %v, want %v", path, fi.Mode().Perm(), mode)
	}
}

// loosen lets other users into dir.
func loosen(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

// makeIndexUnwritable makes the replacement of the index in dir fail, while the
// files below dir can still be written.
func makeIndexUnwritable(t *testing.T, dir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("needs a non-root POSIX user to make the directory read-only")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
}
