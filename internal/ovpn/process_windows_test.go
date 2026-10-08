package ovpn

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// stillActive is the exit code GetExitCodeProcess gives a process that runs.
const stillActive = 259

// processExists reports whether pid is a process that has not ended.
func processExists(pid int) bool {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		// Windows answers an id that no process has with an invalid parameter.
		return !errors.Is(err, windows.ERROR_INVALID_PARAMETER)
	}
	defer windows.CloseHandle(process)
	var code uint32
	if err := windows.GetExitCodeProcess(process, &code); err != nil {
		return true
	}
	return code == stillActive
}

func startStub(t *testing.T, behavior stubBehavior) (*exec.Cmd, string) {
	t.Helper()
	path := writeStub(t, t.TempDir(), "child", behavior)
	cmd := exec.Command(path)
	prepareCommand(cmd, filepath.Dir(path))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return cmd, path
}

func waitGone(t *testing.T, pid int, what string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); processExists(pid); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%s: process %d is still running", what, pid)
		}
	}
}

// The child is created suspended and must not run a single instruction before
// it is in the job: a process that is in no job when it starts could have
// started others that are in none either.
func TestChildRunsOnlyOnceItIsUnderTheGuard(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "runs")
	cmd, _ := startStub(t, stubBehavior{Counter: counter, Hang: true})
	time.Sleep(400 * time.Millisecond)
	if _, err := os.Stat(counter); err == nil {
		t.Fatal("the child ran before it was put in the job")
	}
	guard, err := guardProcess(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.close()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(counter); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the child never ran after it was let go")
		}
	}
}

func TestGuardEndsTheChildWhenClosed(t *testing.T) {
	cmd, _ := startStub(t, stubBehavior{Hang: true})
	guard, err := guardProcess(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !processExists(cmd.Process.Pid) {
		t.Fatal("the child is not running")
	}
	guard.close()
	waitGone(t, cmd.Process.Pid, "after the guard was closed")
	guard.close() // again: harmless
	if err := guard.kill(cmd); err != nil {
		t.Errorf("kill after close = %v", err)
	}
}

func TestGuardKillEndsTheChild(t *testing.T) {
	cmd, _ := startStub(t, stubBehavior{Hang: true})
	guard, err := guardProcess(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.close()
	if err := guard.kill(cmd); err != nil {
		t.Fatal(err)
	}
	waitGone(t, cmd.Process.Pid, "after kill")
}

// What the guard is for: the daemon is killed without a chance to clean up, and
// openvpn must not stay behind.
func TestGuardedChildDoesNotOutliveACrashedDaemon(t *testing.T) {
	dir := t.TempDir()
	child := writeStub(t, dir, "openvpn", stubBehavior{Hang: true})
	daemonPath := writeStub(t, dir, "daemon", stubBehavior{GuardedChild: child})
	daemon := exec.Command(daemonPath)
	stdout, err := daemon.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { daemon.Process.Kill(); daemon.Wait() })

	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var childPID int
	if _, err := fmt.Sscanf(line, "CHILD %d", &childPID); err != nil {
		t.Fatalf("the daemon said %q", line)
	}
	if !processExists(childPID) {
		t.Fatal("the child is not running")
	}
	t.Cleanup(func() {
		if p, err := os.FindProcess(childPID); err == nil {
			p.Kill()
		}
	})

	if err := daemon.Process.Kill(); err != nil { // TerminateProcess: nothing runs after it
		t.Fatal(err)
	}
	daemon.Wait()
	waitGone(t, childPID, "after the daemon was killed")
}

func TestChildEnvironmentIsTheSystemDirectoriesOnly(t *testing.T) {
	t.Setenv("PATH", `C:\Users\Public\bin;`+os.Getenv("PATH"))
	t.Setenv("SECRET_TOKEN", "x")
	env := strings.Join(childEnv(), "\n")
	if strings.Contains(env, "Public") || strings.Contains(env, "SECRET_TOKEN") {
		t.Errorf("the daemon's environment leaked: %s", env)
	}
	system, _ := windows.GetSystemWindowsDirectory()
	for _, want := range []string{"SystemRoot=" + system, "windir=" + system, `PATH=` + system + `\System32;` + system} {
		if !strings.Contains(env, want) {
			t.Errorf("environment lacks %q: %s", want, env)
		}
	}
}

func TestWindowsHasNoDirectSignalForOpenVPN(t *testing.T) {
	cmd, _ := startStub(t, stubBehavior{Hang: true})
	if err := signalExit(cmd); !errors.Is(err, errNoExitSignal) {
		t.Errorf("signalExit = %v", err)
	}
}
