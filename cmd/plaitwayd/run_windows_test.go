package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/manager"
)

// Only the fake backend exists on Windows until the real engines land: a daemon
// asked for the real ones must say so and give the pipe back instead of
// answering calls it cannot honour.
func TestRunWithoutTheFakeBackendFailsWithTheReasonAndReleasesThePipe(t *testing.T) {
	cfg := config{socket: newSocketPath(t), stateDir: filepath.Join(shortDir(t), "state")}

	err := run(context.Background(), discardLog(), manager.NewLogBuffer(""), cfg, everyone().pol)

	if err == nil || !strings.Contains(err.Error(), "real engines are only available on macOS") {
		t.Fatalf("run returned %v, want the reason that the real engines are not there", err)
	}
	socketGone(t, cfg.socket)
}
