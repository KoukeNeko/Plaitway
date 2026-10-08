package rootguard

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// HelperTestName is the test function that every package which arms a guard
// defines, as one line: func TestRootGuardHelper(t *testing.T) { rootguard.RunHelper(t) }.
// The guard is the test binary started with -test.run for exactly this name.
const HelperTestName = "TestRootGuardHelper"

const (
	envAction   = "PLAITWAY_ROOTGUARD_ACTION"
	envArgument = "PLAITWAY_ROOTGUARD_ARGUMENT"
	envDeadline = "PLAITWAY_ROOTGUARD_DEADLINE"
	envLogFile  = "PLAITWAY_ROOTGUARD_LOG"

	// Lines of the protocol between the test and its guard. The guard says that
	// it is armed on its standard output; the test says that it is done on the
	// standard input of the guard.
	armedLine   = "armed"
	releaseLine = "release"

	// startTimeout bounds the wait for the guard to say that it is armed: a
	// package that forgot HelperTestName gets a guard that ends at once, and the
	// test must not go on unprotected.
	startTimeout = 30 * time.Second
	// stopTimeout bounds the wait for a released guard to end.
	stopTimeout = 30 * time.Second

	// recoveryAttempts and recoveryPause: a recovery can meet a key that is
	// locked for a moment, or a service that is still stopping.
	recoveryAttempts = 3
	recoveryPause    = 2 * time.Second

	// Exit codes of the guard. A guard that was released ends with 0, so that a
	// test can tell it from one that acted, even when it is released while the
	// recovery is under way.
	exitReleased        = 0
	exitRecoveryFailed  = 1
	exitUnknownRecovery = 2
	exitBadSetup        = 3
	exitRecovered       = 10
)

// Recovery removes what a test changed. It must not depend on the state of the
// test that failed, only on its argument, and it must succeed when there is
// nothing left to remove. It runs in the guard, not in the test.
type Recovery func(argument string) error

var (
	recoveriesMu sync.Mutex
	recoveries   = map[string]Recovery{}
)

// Register makes a recovery known under name. Call it from an init function of
// the test package, so that the guard, which is the same binary, has it.
func Register(name string, recovery Recovery) {
	recoveriesMu.Lock()
	defer recoveriesMu.Unlock()
	if _, taken := recoveries[name]; taken {
		panic("rootguard: recovery " + name + " is registered twice")
	}
	recoveries[name] = recovery
}

func recoveryNamed(name string) (Recovery, bool) {
	recoveriesMu.Lock()
	defer recoveriesMu.Unlock()
	recovery, ok := recoveries[name]
	return recovery, ok
}

// Plan says what the guard does.
type Plan struct {
	// Action is the name of a registered recovery and Argument is what it gets.
	Action, Argument string
	// Within is how long after Arm the guard acts if the test has not released
	// it: longer than the test runs, short enough that a hung test does not
	// leave the machine changed for long.
	Within time.Duration
	// Manual is the exact command that does by hand what the recovery does. It
	// is printed when the guard is armed.
	Manual string
}

func (p Plan) validate() error {
	switch {
	case p.Action == "":
		return errors.New("the plan has no action")
	case p.Within <= 0:
		return errors.New("the plan has no deadline")
	case p.Manual == "":
		return errors.New("the plan has no manual recovery command")
	}
	if _, ok := recoveryNamed(p.Action); !ok {
		return fmt.Errorf("no recovery is registered as %q", p.Action)
	}
	return nil
}

// Guard is a running guard.
type Guard struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	ended   chan error
	logFile string

	once  sync.Once
	acted bool
	err   error
}

// Arm starts a guard for plan and waits until it is armed; the test fails if
// it cannot be. It returns after printing the manual recovery command. The
// guard is released when the test ends, after the cleanups that were
// registered after Arm; call Arm before the first change, so that the test's
// own cleanups run before the release. A guard that had to act fails the test.
func Arm(t testing.TB, plan Plan) *Guard {
	t.Helper()
	guard, err := start(plan)
	if err != nil {
		t.Fatalf("cannot arm the guard for this test, so it does not run: %v", err)
	}
	fmt.Fprintf(os.Stderr, "ROOT TEST GUARD armed (%s, pid %d, acts within %s, log %s).\n  If this run is killed and the machine is left changed, run: %s\n",
		plan.Action, guard.cmd.Process.Pid, plan.Within, guard.logFile, plan.Manual)
	t.Cleanup(func() {
		acted, err := guard.Release()
		switch {
		case err != nil:
			t.Errorf("the guard: %v; run by hand: %s", err, plan.Manual)
		case acted:
			t.Errorf("the guard had to clean up after this test (%s): the test did not remove what it changed within %s; see %s", plan.Action, plan.Within, guard.logFile)
		}
	})
	return guard
}

// logFileOf is where the guard of a process writes down what it did: it is for
// the person who reads it after a test was killed, so it stays.
func logFileOf(action string, pid int) string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("plaitway-rootguard-%s-%d.log", action, pid))
}

func start(plan Plan) (*Guard, error) {
	if err := plan.validate(); err != nil {
		return nil, err
	}
	logFile := logFileOf(plan.Action, os.Getpid())
	cmd := exec.Command(os.Args[0], "-test.run=^"+HelperTestName+"$", "-test.count=1", "-test.timeout=0")
	cmd.Env = append(os.Environ(),
		envAction+"="+plan.Action,
		envArgument+"="+plan.Argument,
		envDeadline+"="+plan.Within.String(),
		envLogFile+"="+logFile,
	)
	detach(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start the guard: %w", err)
	}
	guard := &Guard{cmd: cmd, stdin: stdin, ended: make(chan error, 1), logFile: logFile}
	go func() { guard.ended <- cmd.Wait() }()
	if err := guard.awaitArmed(stdout); err != nil {
		stdin.Close()
		cmd.Process.Kill()
		return nil, err
	}
	return guard, nil
}

// awaitArmed reads the first line of the guard.
func (g *Guard) awaitArmed(stdout io.Reader) error {
	line := make(chan string, 1)
	go func() {
		text, _ := bufio.NewReader(stdout).ReadString('\n')
		line <- strings.TrimSpace(text)
	}()
	select {
	case text := <-line:
		if text != armedLine {
			return fmt.Errorf("the guard did not say that it is armed (it said %q; does the package define %s?); see %s", text, HelperTestName, g.logFile)
		}
		return nil
	case <-time.After(startTimeout):
		return fmt.Errorf("the guard was not armed within %s; see %s", startTimeout, g.logFile)
	}
}

// Release tells the guard that the test has cleaned up and waits for it to end.
// It reports whether the guard acted before it was released (it ended on its
// own, having run the recovery), which means that the test did not clean up in
// time, and an error when the guard failed. It can be called again.
func (g *Guard) Release() (acted bool, err error) {
	g.once.Do(func() { g.acted, g.err = g.release() })
	return g.acted, g.err
}

func (g *Guard) release() (acted bool, err error) {
	select {
	case endErr := <-g.ended:
		return g.outcome(endErr)
	default:
	}
	fmt.Fprintln(g.stdin, releaseLine) // a guard that has ended does not read it
	g.stdin.Close()
	select {
	case endErr := <-g.ended:
		return g.outcome(endErr)
	case <-time.After(stopTimeout):
		g.cmd.Process.Kill()
		return false, fmt.Errorf("it did not end within %s after the release; see %s", stopTimeout, g.logFile)
	}
}

// outcome reads what the guard did from how it ended: 0 is a guard that was
// released, whenever that was, and exitRecovered one that ran its recovery. A
// guard that is released while its recovery is under way ends with the exit code
// of the recovery.
func (g *Guard) outcome(endErr error) (acted bool, err error) {
	var exit *exec.ExitError
	switch {
	case endErr == nil:
		return false, nil
	case errors.As(endErr, &exit) && exit.ExitCode() == exitRecovered:
		return true, nil
	}
	return true, fmt.Errorf("it ended with %v; see %s", endErr, g.logFile)
}

// RunHelper is the body of the test function HelperTestName. It does nothing
// in a normal run of the tests. In the guard it never returns.
func RunHelper(t *testing.T) {
	action := os.Getenv(envAction)
	if action == "" {
		t.Skip("runs only as the guard of a root test")
	}
	os.Exit(runGuard(action, os.Getenv(envArgument), os.Getenv(envDeadline), os.Getenv(envLogFile), os.Stdin, os.Stdout))
}

type guardEvent int

const (
	eventReleased guardEvent = iota
	eventParentGone
)

// runGuard is the guard: it says that it is armed, then waits for the release,
// for the end of its input, or for the deadline, whichever comes first, and
// runs the recovery in the last two cases.
func runGuard(action, argument, deadlineText, logPath string, input io.Reader, output io.Writer) int {
	logf := logTo(logPath)
	recovery, ok := recoveryNamed(action)
	if !ok {
		logf("no recovery is registered as %q", action)
		fmt.Fprintf(output, "error: unknown recovery %q\n", action)
		return exitUnknownRecovery
	}
	deadline, err := time.ParseDuration(deadlineText)
	if err != nil || deadline <= 0 {
		logf("bad deadline %q", deadlineText)
		fmt.Fprintf(output, "error: bad deadline %q\n", deadlineText)
		return exitBadSetup
	}
	events := watchInput(input)
	logf("armed: %s(%q) within %s", action, argument, deadline)
	fmt.Fprintln(output, armedLine)

	timer := time.NewTimer(deadline)
	defer timer.Stop()
	select {
	case event := <-events:
		if event == eventReleased {
			logf("released: the test cleaned up")
			return exitReleased
		}
		logf("the test process ended without releasing the guard")
	case <-timer.C:
		logf("the deadline of %s passed", deadline)
	}
	return runRecovery(recovery, action, argument, logf)
}

// watchInput reports the first line of input as a release and the end of the
// input without one as the end of the parent: the pipe closes when the process
// that holds the other end ends, however it ends.
func watchInput(input io.Reader) <-chan guardEvent {
	events := make(chan guardEvent, 1)
	go func() {
		scanner := bufio.NewScanner(input)
		for scanner.Scan() {
			if strings.TrimSpace(scanner.Text()) == releaseLine {
				events <- eventReleased
				return
			}
		}
		events <- eventParentGone
	}()
	return events
}

func runRecovery(recovery Recovery, action, argument string, logf func(string, ...any)) int {
	var err error
	for attempt := 1; attempt <= recoveryAttempts; attempt++ {
		if err = recovery(argument); err == nil {
			logf("recovered: %s(%q) on attempt %d", action, argument, attempt)
			return exitRecovered
		}
		logf("attempt %d of %s(%q) failed: %v", attempt, action, argument, err)
		if attempt < recoveryAttempts {
			time.Sleep(recoveryPause)
		}
	}
	logf("RECOVERY FAILED: %s(%q): %v", action, argument, err)
	return exitRecoveryFailed
}

// logTo appends to the file. A guard whose log cannot be opened still runs: the
// log is for the person who reads it afterwards, not part of the protocol.
func logTo(path string) func(string, ...any) {
	return func(format string, args ...any) {
		file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		defer file.Close()
		fmt.Fprintf(file, "%s pid %d: %s\n", time.Now().Format(time.RFC3339), os.Getpid(), fmt.Sprintf(format, args...))
	}
}
