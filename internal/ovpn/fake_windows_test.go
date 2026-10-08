package ovpn

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/fsperm"
)

// recordWorkspaceAccess lets the fake say what the engine made of its
// directory and the profile: whether Windows calls them private, which is an
// access list of SYSTEM, Administrators and the daemon's user.
func recordWorkspaceAccess(record func(name, text string), config string) {
	for name, path := range map[string]string{"config-private": config, "dir-private": filepath.Dir(config)} {
		private, err := fsperm.IsPrivate(path)
		if err != nil {
			record(name, "error: "+err.Error())
			continue
		}
		record(name, boolWord(private))
	}
}

func boolWord(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// requirePrivateWorkspace fails unless openvpn found the profile and its
// directory private to the daemon.
func requirePrivateWorkspace(t *testing.T, h *harness) {
	t.Helper()
	for _, name := range []string{"config-private", "dir-private"} {
		if got := strings.TrimSpace(h.readRecording(name)); got != "yes" {
			t.Errorf("%s = %q, want yes", name, got)
		}
	}
}

// closedPortOutcome is what the status says when the server's port is closed
// and openvpn has had its time, and whether openvpn tries again at once and so
// can be counted. Windows sends no refusal to a connection to a closed port for
// about two seconds, and openvpn for Windows does not take even that for an
// answer: it stays in TCP_CONNECT for its connect-timeout, two minutes, without
// a word in its log. The status then says what it says of any silent server.
func closedPortOutcome(int) (reason string, retries bool) {
	return "no response from the server", false
}
