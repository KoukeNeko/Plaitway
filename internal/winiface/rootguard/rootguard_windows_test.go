package rootguard

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const (
	envKilledChildMarker = "PLAITWAY_ROOTGUARD_TEST_KILLED_CHILD_MARKER"
	killedChildExitCode  = 7
)

// TestKilledParent runs only as the child of
// TestAGuardActsWhenItsParentIsKilled: it arms a guard and is ended the hard way
// without releasing it, as a killed test process is.
func TestKilledParent(t *testing.T) {
	path := os.Getenv(envKilledChildMarker)
	if path == "" {
		t.Skip("runs only as the child of TestAGuardActsWhenItsParentIsKilled")
	}
	if _, err := start(Plan{Action: "touch", Argument: path, Within: 10 * time.Minute, Manual: "by hand"}); err != nil {
		t.Fatal(err)
	}
	if err := windows.TerminateProcess(windows.CurrentProcess(), killedChildExitCode); err != nil {
		t.Fatal(err)
	}
}

// The case the guard exists for: the process that armed it is gone without a
// release, no cleanup has run, and the deadline is ten minutes away. The guard
// notices at once, because the pipe it reads from closes.
func TestAGuardActsWhenItsParentIsKilled(t *testing.T) {
	path := marker(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestKilledParent$", "-test.count=1")
	cmd.Env = append(os.Environ(), envKilledChildMarker+"="+path)
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != killedChildExitCode {
		t.Fatalf("child: %v, want exit code %d", err, killedChildExitCode)
	}
	t.Cleanup(func() { os.Remove(logFileOf("touch", cmd.Process.Pid)) })
	started := time.Now()
	eventually(t, "the recovery after the parent was killed", 20*time.Second, func() bool { return exists(path) })
	t.Logf("the guard acted %v after the parent had ended", time.Since(started))
	if content, _ := os.ReadFile(path); !strings.Contains(string(content), "recovered") {
		t.Errorf("marker = %q", content)
	}
}

// Ctrl+C goes to every process attached to the console. The guard must not be
// one: no console, and a process group of its own.
func TestTheGuardStartsWithoutAConsoleAndInItsOwnProcessGroup(t *testing.T) {
	cmd := exec.Command("unused")
	detach(cmd)
	flags := cmd.SysProcAttr.CreationFlags
	for name, flag := range map[string]uint32{"CREATE_NEW_PROCESS_GROUP": windows.CREATE_NEW_PROCESS_GROUP, "DETACHED_PROCESS": windows.DETACHED_PROCESS} {
		if flags&flag == 0 {
			t.Errorf("creation flags %#x lack %s", flags, name)
		}
	}
}
