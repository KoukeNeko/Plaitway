package transport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultPathIsInTheRuntimeDirectoryOfTheUser(t *testing.T) {
	dir := shortSocketDir(t)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	if got, want := DefaultPath(), filepath.Join(dir, "plaitway.sock"); got != want {
		t.Fatalf("DefaultPath() = %q, want %q", got, want)
	}
}

func TestDefaultPathFallsBackToTheTemporaryDirectory(t *testing.T) {
	notADirectory := filepath.Join(shortSocketDir(t), "file")
	if err := os.WriteFile(notADirectory, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	openToOthers := shortSocketDir(t)
	if err := os.Chmod(openToOthers, 0o777); err != nil {
		t.Fatal(err)
	}
	groupWritable := shortSocketDir(t)
	if err := os.Chmod(groupWritable, 0o770); err != nil {
		t.Fatal(err)
	}
	// 108 bytes are sun_path; the socket's name makes this directory one too long.
	tooLong := filepath.Join(shortSocketDir(t), strings.Repeat("d", 100))
	if err := os.Mkdir(tooLong, 0o700); err != nil {
		t.Fatal(err)
	}

	for name, value := range map[string]string{
		"unset":                   "",
		"relative":                "run/user/1000",
		"missing":                 filepath.Join(shortSocketDir(t), "missing"),
		"not a directory":         notADirectory,
		"writable by others":      openToOthers,
		"writable by its group":   groupWritable,
		"too long for a sun_path": tooLong,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_RUNTIME_DIR", value)
			if got, want := DefaultPath(), filepath.Join(os.TempDir(), "plaitway.sock"); got != want {
				t.Fatalf("DefaultPath() = %q, want %q", got, want)
			}
		})
	}
}

// A directory of somebody else is not the user's own, however private it is.
func TestDefaultPathIgnoresARuntimeDirectoryOfAnotherUser(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs a directory owned by another user, which only root can make")
	}
	dir := shortSocketDir(t)
	if err := os.Chown(dir, 1, 1); err != nil {
		t.Skipf("cannot give the directory to another user: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", dir)
	if got, want := DefaultPath(), filepath.Join(os.TempDir(), "plaitway.sock"); got != want {
		t.Fatalf("DefaultPath() = %q, want %q", got, want)
	}
}

func shortSocketDir(t *testing.T) string {
	t.Helper()
	return filepath.Dir(shortSocketPath(t))
}
