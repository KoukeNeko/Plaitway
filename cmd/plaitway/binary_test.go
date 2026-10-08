package main

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// These run the program as a script or a shell would.

func TestBinaryTakesItsVersionFromTheLinker(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"version"}, {"--version"}, {"-version"}} {
		if stdout, stderr, code := execClient(t, nil, args...); code != 0 || stdout != "plaitway "+testVersion+"\n" || stderr != "" {
			t.Errorf("plaitway %v: exit %d, stdout %q, stderr %q", args, code, stdout, stderr)
		}
	}
}

func TestBinaryExitCodes(t *testing.T) {
	t.Parallel()
	nobody := []string{"PLAITWAY_SOCKET=" + newSocketPath(t)}
	for name, c := range map[string]struct {
		args        []string
		code        int
		stdout      string // a part of it
		stderr      string // a part of it
		mustBeEmpty string
	}{
		"help":             {[]string{"help"}, 0, "Usage: plaitway", "", "stderr"},
		"-h":               {[]string{"-h"}, 0, "Commands:", "", "stderr"},
		"command help":     {[]string{"connect", "-h"}, 0, "-username", "", "stderr"},
		"no command":       {nil, exitUsage, "", "missing command", "stdout"},
		"unknown command":  {[]string{"frobnicate"}, exitUsage, "", `unknown command "frobnicate"`, "stdout"},
		"unknown flag":     {[]string{"list", "-nope"}, exitUsage, "", "flag provided but not defined: -nope", "stdout"},
		"missing argument": {[]string{"connect"}, exitUsage, "", "usage: plaitway connect [flags] profile", "stdout"},
		"no daemon":        {[]string{"list"}, exitFailure, "", "the daemon is not running", "stdout"},
	} {
		stdout, stderr, code := execClient(t, nobody, c.args...)
		if code != c.code || !strings.Contains(stdout, c.stdout) || !strings.Contains(stderr, c.stderr) ||
			(c.mustBeEmpty == "stdout" && stdout != "") || (c.mustBeEmpty == "stderr" && stderr != "") {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", name, code, stdout, stderr)
		}
	}
}

func TestBinaryAgainstTheDaemon(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	d.importText("home", wgProfile)
	d.mustRun("", "connect", "home")
	nobody := "PLAITWAY_SOCKET=" + newSocketPath(t)

	// The environment names the socket, and -socket takes precedence over it.
	for name, c := range map[string]struct {
		env  []string
		args []string
	}{
		"environment": {[]string{"PLAITWAY_SOCKET=" + d.socket}, []string{"status"}},
		"flag":        {[]string{nobody}, []string{"-socket", d.socket, "status"}},
		"flag last":   {[]string{nobody}, []string{"status", "-socket", d.socket}},
	} {
		// TERM says the terminal has colors, but this output is a pipe.
		stdout, stderr, code := execClient(t, append(c.env, "TERM=xterm-256color"), c.args...)
		if code != 0 || !strings.Contains(stdout, "connected") || strings.Contains(stdout, "\x1b") || stderr != "" {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", name, code, stdout, stderr)
		}
	}
	if _, stderr, code := execClient(t, []string{nobody}, "status"); code != exitFailure || !strings.Contains(stderr, "the daemon is not running") {
		t.Errorf("a socket nobody listens on: exit %d, stderr %q", code, stderr)
	}
}

// Ctrl-C is how logs -f and watch are meant to end.
func TestBinaryEndsWithSuccessOnInterrupt(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	d.importText("home", wgProfile)
	d.mustRun("", "connect", "home") // a log to follow

	for _, args := range [][]string{{"watch"}, {"logs", "-f", "home"}, {"logs", "-f"}} {
		cmd := exec.Command(clientBinary, args...)
		cmd.Env = append(scriptEnvironment(), "PLAITWAY_SOCKET="+d.socket)
		prepareToBeInterrupted(cmd)
		var stdout, stderr syncBuffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(waitTimeout)
		for stdout.String() == "" && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		interruptProcess(t, cmd.Process)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("plaitway %v after SIGINT: %v, stderr %q", args, err, stderr.String())
			}
		case <-time.After(waitTimeout):
			cmd.Process.Kill()
			t.Errorf("plaitway %v did not end after SIGINT", args)
		}
	}
}
