package rootguard

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The recoveries of these tests write files, so that the tests can see whether
// and when the guard acted. They need no rights.
func init() {
	Register("touch", func(path string) error {
		return os.WriteFile(path, []byte("recovered"), 0o600)
	})
	Register("fail-twice", func(counter string) error {
		return failFirstAttempts(counter, 2)
	})
	Register("always-fail", func(string) error {
		return errors.New("access denied")
	})
}

// failFirstAttempts fails until counter has recorded attempts failures.
func failFirstAttempts(counter string, attempts int) error {
	previous, _ := os.ReadFile(counter)
	failures, _ := strconv.Atoi(string(previous))
	if failures < attempts {
		if err := os.WriteFile(counter, []byte(strconv.Itoa(failures+1)), 0o600); err != nil {
			return err
		}
		return errors.New("the key is locked")
	}
	return nil
}

func TestRootGuardHelper(t *testing.T) { RunHelper(t) }

func marker(t *testing.T) string { return filepath.Join(t.TempDir(), "marker") }

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func eventually(t *testing.T, what string, within time.Duration, condition func() bool) {
	t.Helper()
	for deadline := time.Now().Add(within); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if condition() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func startGuard(t *testing.T, plan Plan) *Guard {
	t.Helper()
	if plan.Manual == "" {
		plan.Manual = "by hand"
	}
	guard, err := start(plan)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		guard.cmd.Process.Kill()
		os.Remove(guard.logFile)
	})
	return guard
}

// waitUntilItEnds waits for the guard to end on its own, without consuming the
// end that Release reports.
func waitUntilItEnds(t *testing.T, guard *Guard) {
	t.Helper()
	eventually(t, "the guard to end", 30*time.Second, func() bool {
		select {
		case err := <-guard.ended:
			guard.ended <- err
			return true
		default:
			return false
		}
	})
}

func logOf(t *testing.T, guard *Guard) string {
	t.Helper()
	text, err := os.ReadFile(guard.logFile)
	if err != nil {
		t.Fatal(err)
	}
	return string(text)
}

// The normal end of a test: it cleaned up, it releases the guard, the guard
// does nothing.
func TestAReleasedGuardDoesNothing(t *testing.T) {
	path := marker(t)
	guard := startGuard(t, Plan{Action: "touch", Argument: path, Within: time.Minute})

	acted, err := guard.Release()
	if acted || err != nil {
		t.Fatalf("Release = %v, %v; want the guard to have done nothing", acted, err)
	}
	if exists(path) {
		t.Error("the guard ran its recovery although it was released")
	}
	if log := logOf(t, guard); !strings.Contains(log, "released") || strings.Contains(log, "recovered") {
		t.Errorf("log:\n%s", log)
	}
	// Releasing twice is harmless.
	if acted, err := guard.Release(); acted || err != nil {
		t.Errorf("second Release = %v, %v", acted, err)
	}
}

// A test that hangs: the guard acts when its deadline passes, and a release
// afterwards says so.
func TestAGuardActsAtItsDeadline(t *testing.T) {
	path := marker(t)
	guard := startGuard(t, Plan{Action: "touch", Argument: path, Within: 300 * time.Millisecond})

	eventually(t, "the recovery at the deadline", 20*time.Second, func() bool { return exists(path) })
	acted, err := guard.Release()
	if !acted || err != nil {
		t.Errorf("Release = %v, %v; want the guard to have acted, without an error", acted, err)
	}
	if log := logOf(t, guard); !strings.Contains(log, "deadline") {
		t.Errorf("log:\n%s", log)
	}
}

// A recovery that fails is tried again, and the guard reports it when it never
// works.
func TestAGuardTriesAFailingRecoveryAgain(t *testing.T) {
	counter := marker(t)
	guard := startGuard(t, Plan{Action: "fail-twice", Argument: counter, Within: 100 * time.Millisecond})

	waitUntilItEnds(t, guard)
	acted, err := guard.Release()
	if !acted || err != nil {
		t.Errorf("Release = %v, %v; want a recovery that worked on the third attempt", acted, err)
	}
	if log := logOf(t, guard); !strings.Contains(log, "attempt 2 of fail-twice") || !strings.Contains(log, "recovered: fail-twice") {
		t.Errorf("log:\n%s", log)
	}
}

func TestAGuardThatCannotRecoverSaysSo(t *testing.T) {
	guard := startGuard(t, Plan{Action: "always-fail", Within: 100 * time.Millisecond})

	waitUntilItEnds(t, guard)
	acted, err := guard.Release()
	if !acted || err == nil {
		t.Errorf("Release = %v, %v; want the failure of the recovery", acted, err)
	}
	if log := logOf(t, guard); !strings.Contains(log, "RECOVERY FAILED: always-fail") || !strings.Contains(log, "access denied") {
		t.Errorf("log:\n%s", log)
	}
}

func TestAPlanThatCannotBeFollowedIsRefusedBeforeAnythingChanges(t *testing.T) {
	tests := map[string]Plan{
		"no action":              {Within: time.Minute, Manual: "x"},
		"an action nobody knows": {Action: "no-such-recovery", Within: time.Minute, Manual: "x"},
		"no deadline":            {Action: "touch", Manual: "x"},
		"no manual command":      {Action: "touch", Within: time.Minute},
	}
	for name, plan := range tests {
		if _, err := start(plan); err == nil {
			t.Errorf("%s: the guard was started", name)
		}
	}
}

// A package that forgot to define the helper test gets a binary that runs no
// test and ends: the test must not go on as if it were protected.
func TestAGuardThatDoesNotSayItIsArmedFailsTheStart(t *testing.T) {
	g := &Guard{logFile: "guard.log"}
	err := g.awaitArmed(strings.NewReader("PASS\n"))
	if err == nil || !strings.Contains(err.Error(), HelperTestName) {
		t.Errorf("awaitArmed = %v, want the missing helper named", err)
	}
	if err := g.awaitArmed(strings.NewReader(armedLine + "\n")); err != nil {
		t.Errorf("awaitArmed of an armed guard = %v", err)
	}
}

func TestTheGuardRefusesWhatItCannotRecover(t *testing.T) {
	var output strings.Builder
	if code := runGuard("no-such-recovery", "", "1m", filepath.Join(t.TempDir(), "log"), strings.NewReader(""), &output); code != exitUnknownRecovery {
		t.Errorf("exit %d, want %d", code, exitUnknownRecovery)
	}
	if strings.Contains(output.String(), armedLine+"\n") {
		t.Errorf("output %q says armed", output.String())
	}
	output.Reset()
	if code := runGuard("touch", "", "soon", filepath.Join(t.TempDir(), "log"), strings.NewReader(""), &output); code != exitBadSetup {
		t.Errorf("a bad deadline: exit %d, want %d", code, exitBadSetup)
	}
}
