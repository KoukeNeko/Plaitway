//go:build unix

package ovpn

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recordWorkspaceAccess lets the fake say what the engine made of its
// directory and the profile: what the modes were when openvpn started.
func recordWorkspaceAccess(record func(name, text string), config string) {
	if info, err := os.Stat(config); err == nil {
		record("config-mode", fmt.Sprintf("%o", info.Mode().Perm()))
	}
	if info, err := os.Stat(filepath.Dir(config)); err == nil {
		record("dir-mode", fmt.Sprintf("%o", info.Mode().Perm()))
	}
}

// requirePrivateWorkspace fails unless openvpn found the profile and its
// directory private to the daemon.
func requirePrivateWorkspace(t *testing.T, h *harness) {
	t.Helper()
	if got := h.readRecording("config-mode"); strings.TrimSpace(got) != "600" {
		t.Errorf("config mode = %q, want 600", got)
	}
	if got := h.readRecording("dir-mode"); strings.TrimSpace(got) != "700" {
		t.Errorf("workspace mode = %q, want 700", got)
	}
}

// closedPortOutcome is what the status says when the server's port is closed
// and openvpn has had its time, and whether openvpn tries again at once and so
// can be counted. A closed port is refused at once here.
func closedPortOutcome(port int) (reason string, retries bool) {
	return fmt.Sprintf("cannot reach 127.0.0.1:%d: connection refused", port), true
}
