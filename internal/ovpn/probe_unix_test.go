//go:build unix

package ovpn

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Whether a file may be run is a mode bit on Unix. Windows decides by the
// extension and reports another error.
func TestProbeBinaryNotExecutable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := probeBinary(path, 5*time.Second)
	if want := "openvpn at " + path + " cannot be executed"; got.available || got.detail != want {
		t.Errorf("probeBinary = %+v, want detail %q", got, want)
	}
}
