//go:build unix

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Whatever ends the command, the text of the profile is not left in a file.
func TestEditRemovesItsFileWhenTheCommandIsStopped(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	d.importText("home", wgSecrets)

	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		dir := shortDir(t)
		started := filepath.Join(dir, "started")
		script := filepath.Join(dir, "hang.sh")
		// exec makes the editor the process that $$ names, so that the test
		// can tell whether it was stopped.
		body := "#!/bin/sh\necho \"$1 $$\" > \"" + started + ".tmp\" && mv \"" + started + ".tmp\" \"" + started + "\"\nexec sleep 60\n"
		if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(clientBinary, "edit", "home")
		cmd.Env = []string{"PLAITWAY_SOCKET=" + d.socket, "EDITOR=" + script}
		var stderr syncBuffer
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		var path string
		var editorPID int
		deadline := time.Now().Add(waitTimeout)
		for path == "" {
			if data, err := os.ReadFile(started); err == nil {
				fields := strings.Fields(string(data))
				editorPID, _ = strconv.Atoi(fields[1])
				path = fields[0]
			} else if time.Now().After(deadline) {
				cmd.Process.Kill()
				t.Fatalf("%v: the editor did not start; stderr %q", sig, stderr.String())
			} else {
				time.Sleep(20 * time.Millisecond)
			}
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%v: the file is not there while the editor runs: %v", sig, err)
		}

		if err := cmd.Process.Signal(sig); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != exitFailure {
				t.Errorf("%v: the command ended with %v, want exit status %d", sig, err, exitFailure)
			}
		case <-time.After(waitTimeout):
			cmd.Process.Kill()
			t.Fatalf("%v: the command did not end", sig)
		}

		for _, p := range []string{path, filepath.Dir(path)} {
			if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%v: %s is still there (stat: %v)", sig, p, err)
			}
		}
		if err := syscall.Kill(editorPID, 0); !errors.Is(err, syscall.ESRCH) {
			syscall.Kill(editorPID, syscall.SIGKILL)
			t.Errorf("%v: the editor is still running (kill 0: %v)", sig, err)
		}
		if !strings.Contains(stderr.String(), "interrupted") {
			t.Errorf("%v: stderr %q", sig, stderr.String())
		}
		if got := d.storedText("home"); got != wgSecrets {
			t.Errorf("%v: stored text %q", sig, got)
		}
	}
}
