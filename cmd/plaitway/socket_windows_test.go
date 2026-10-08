package main

import (
	"strings"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/transport"
)

// wantDefaultSocket is the pipe the daemon listens on by default.
const wantDefaultSocket = `\\.\pipe\plaitway`

// The client and the daemon agree on the pipe because both take it from the
// transport package.
func TestDefaultSocketIsTheDaemonsPipe(t *testing.T) {
	if defaultSocket != transport.DefaultPath() {
		t.Fatalf("client default %q, daemon default %q", defaultSocket, transport.DefaultPath())
	}
}

func TestUsageNamesTheDefaultPipe(t *testing.T) {
	var out strings.Builder
	(&app{}).usage(&out)
	if !strings.Contains(out.String(), "else "+wantDefaultSocket+".") {
		t.Fatalf("usage does not name the default pipe:\n%s", out.String())
	}
}

func TestCheckSocketPathAcceptsLocalNamedPipesOnly(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]bool{
		`\\.\pipe\plaitway`:        true,
		`\\.\PIPE\Plaitway-Dev`:    true,
		`\\.\pipe\a\b`:             true,
		`\\.\pipe\`:                false,
		`\\.\pipe`:                 false,
		`\\server\pipe\plaitway`:   false,
		`\\?\pipe\plaitway`:        false,
		`C:\plaitway`:              false,
		`C:\Windows\plaitway.sock`: false,
		`plaitway`:                 false,
	} {
		if got := checkSocketPath(path) == nil; got != want {
			t.Errorf("checkSocketPath(%q) accepts = %v, want %v", path, got, want)
		}
	}
}
