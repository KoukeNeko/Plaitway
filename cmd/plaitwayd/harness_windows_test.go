package main

import (
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"

	"github.com/KoukeNeko/Plaitway/internal/peercred"
)

// The three roles are made from the real token of the test process. Whoever
// runs go test (an elevated administrator, a filtered one, a plain user) sits
// in the session the policy is told is the console, or in the one next to it;
// whether the caller is an administrator is the one fact a test has to decide
// for itself, so roleCredentials rewrites exactly that before the interceptor
// looks.

func ownSession() (uint32, error) {
	var session uint32
	if err := windows.ProcessIdToSessionId(uint32(os.Getpid()), &session); err != nil {
		return 0, fmt.Errorf("session of the test process: %w", err)
	}
	return session, nil
}

func consoleIsAnotherSession() (uint32, error) {
	session, err := ownSession()
	return session + 1, err
}

func asAdministrator(identity *peercred.WindowsIdentity)   { identity.Administrator = true }
func asNoAdministrator(identity *peercred.WindowsIdentity) { identity.Administrator = false }

// everyone may do everything: an administrator.
func everyone() caller {
	return caller{pol: &policy{consoleSession: ownSession}, rewrite: asAdministrator}
}

// consoleUserOnly lets the test process read and connect but not modify: it is
// no administrator and its session is the console.
func consoleUserOnly() caller {
	return caller{pol: &policy{consoleSession: ownSession}, rewrite: asNoAdministrator}
}

// stranger lets the test process do nothing: no administrator, and another
// session is the console.
func stranger() caller {
	return caller{pol: &policy{consoleSession: consoleIsAnotherSession}, rewrite: asNoAdministrator}
}

// skipIfEveryoneIsAuthorized never skips: the roles do not depend on who runs
// the tests, so there is always somebody to deny.
func skipIfEveryoneIsAuthorized(*testing.T) {}

var pipeCounter atomic.Uint64

// newSocketPath is a pipe name no other test, nor another run of the tests,
// uses: a named pipe lives in one namespace of the whole machine.
func newSocketPath(*testing.T) string { return uniquePipeName() }

// nestedSocketPath: a pipe has no directory for run to create.
func nestedSocketPath(string) string { return uniquePipeName() }

func uniquePipeName() string {
	return fmt.Sprintf(`\\.\pipe\plaitway-test-%d-%d`, os.Getpid(), pipeCounter.Add(1))
}

// assertRunFootprint checks the access list of what a running daemon created:
// the state directory. The pipe has its own access list, which the transport
// tests assert, and the mode of the socket means nothing here.
func assertRunFootprint(t *testing.T, cfg config) {
	t.Helper()
	mustBePrivate(t, cfg.stateDir)
}

// socketGone checks that nobody listens on the pipe of a daemon that stopped.
func socketGone(t *testing.T, socket string) {
	t.Helper()
	timeout := time.Second
	conn, err := winio.DialPipe(socket, &timeout)
	if err == nil {
		conn.Close()
		t.Fatalf("the pipe %s still accepts connections", socket)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dialing the pipe %s: %v, want it to be gone", socket, err)
	}
}
