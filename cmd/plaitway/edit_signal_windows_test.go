package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// Whatever ends the command, the text of the profile is not left in a file. A
// console sends Ctrl-C and Ctrl-Break alike as an interrupt, and closing the
// window as a termination; only the interrupt can be sent from a test, and it
// is the one that main and edit wait for through os.Interrupt.
func TestEditRemovesItsFileWhenTheCommandIsInterrupted(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	d.importText("home", wgSecrets)

	started := filepath.Join(shortDir(t), "started")
	cmd := exec.Command(clientBinary, "edit", "home")
	cmd.Env = append(scriptEnvironment(), "PLAITWAY_SOCKET="+d.socket, "EDITOR="+fakeEditorCommand(hangingEditor, started))
	var stderr syncBuffer
	cmd.Stderr = &stderr
	prepareToBeInterrupted(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	path, editorPID := waitForHangingEditor(t, cmd, started, &stderr)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the file is not there while the editor runs: %v", err)
	}

	interruptProcess(t, cmd.Process)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != exitFailure {
			t.Errorf("the command ended with %v, want exit status %d", err, exitFailure)
		}
	case <-time.After(waitTimeout):
		cmd.Process.Kill()
		t.Fatal("the command did not end")
	}

	for _, p := range []string{path, filepath.Dir(path)} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still there (stat: %v)", p, err)
		}
	}
	if processIsRunning(t, editorPID) {
		killProcess(editorPID)
		t.Error("the editor is still running")
	}
	if !strings.Contains(stderr.String(), "interrupted") {
		t.Errorf("stderr %q", stderr.String())
	}
	if got := d.storedText("home"); got != wgSecrets {
		t.Errorf("stored text %q", got)
	}
}

// waitForHangingEditor waits until the editor says it runs and returns the file
// it was given and its process id.
func waitForHangingEditor(t *testing.T, cmd *exec.Cmd, started string, stderr *syncBuffer) (path string, pid int) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		data, err := os.ReadFile(started)
		if err == nil {
			fields := strings.Fields(string(data))
			if len(fields) != 2 {
				t.Fatalf("the editor wrote %q", data)
			}
			pid, err = strconv.Atoi(fields[1])
			if err != nil {
				t.Fatal(err)
			}
			return fields[0], pid
		}
		if time.Now().After(deadline) {
			cmd.Process.Kill()
			t.Fatalf("the editor did not start; stderr %q", stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// processIsRunning tells whether the process with the id has not ended. A
// process that has ended may stay openable while somebody holds a handle to
// it, so it is asked whether it is still waiting to end.
func processIsRunning(t *testing.T, pid int) bool {
	t.Helper()
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return false // no process has this id
	}
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	event, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		t.Fatal(err)
	}
	return event == uint32(windows.WAIT_TIMEOUT)
}

func killProcess(pid int) {
	if process, err := os.FindProcess(pid); err == nil {
		process.Kill()
	}
}
