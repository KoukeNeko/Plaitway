package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/manager"
	"github.com/KoukeNeko/Plaitway/internal/transport"
)

// quickTiming reports often enough to be seen within a test.
var quickTiming = serviceTiming{
	startTick:    5 * time.Millisecond,
	startTimeout: 400 * time.Millisecond,
	stopTick:     5 * time.Millisecond,
	stopTimeout:  400 * time.Millisecond,
}

// fakeDaemon is the daemon as the handler sees it: a function that serves until
// its context ends.
type fakeDaemon struct {
	// announce logs that the daemon serves, after this long; negative never does.
	announce time.Duration
	// failBeforeServing makes it return this error instead of serving.
	failBeforeServing error
	// endsOnItsOwn makes it return this error once it serves, without being asked.
	endsOnItsOwn chan error
	// shutdownTakes is how long it needs after its context ends.
	shutdownTakes time.Duration
	// returnsError is what it returns after being asked to stop.
	returnsError error
	// ignoresStop keeps it from returning until release is closed.
	ignoresStop bool
	release     chan struct{}

	cancelledAt, returnedAt atomic.Int64
}

func (d *fakeDaemon) run(log *slog.Logger) func(context.Context) error {
	return func(ctx context.Context) error {
		defer func() { d.returnedAt.Store(time.Now().UnixNano()) }()
		if d.failBeforeServing != nil {
			return d.failBeforeServing
		}
		if d.announce >= 0 {
			time.Sleep(d.announce)
			log.Info(listeningMessage, "socket", "fake")
		}
		select {
		case <-ctx.Done():
			d.cancelledAt.Store(time.Now().UnixNano())
		case err := <-d.endsOnItsOwn:
			return err
		}
		if d.ignoresStop {
			<-d.release
		}
		time.Sleep(d.shutdownTakes)
		return d.returnsError
	}
}

// handlerRun is one Execute under way, with the channels svc would give it.
type handlerRun struct {
	requests chan svc.ChangeRequest
	statuses chan svc.Status
	done     chan [2]uint32
	// signals stand for the console control events of the process.
	signals chan os.Signal
	log     *syncBuffer
	daemon  *fakeDaemon
}

func startHandler(t *testing.T, daemon *fakeDaemon, timing serviceTiming) *handlerRun {
	t.Helper()
	if daemon.announce == 0 {
		daemon.announce = 10 * time.Millisecond
	}
	logs := &syncBuffer{}
	log, ready := withReadySignal(slog.New(slog.NewTextHandler(logs, nil)))
	signals := make(chan os.Signal, 1)
	handler := &serviceHandler{log: log, run: daemon.run(log), ready: ready, timing: timing, stopSignals: signals}
	r := &handlerRun{
		requests: make(chan svc.ChangeRequest),
		statuses: make(chan svc.Status, 1024),
		done:     make(chan [2]uint32, 1),
		signals:  signals,
		log:      logs,
		daemon:   daemon,
	}
	go func() {
		specific, code := handler.Execute(nil, r.requests, r.statuses)
		var flag uint32
		if specific {
			flag = 1
		}
		r.done <- [2]uint32{flag, code}
	}()
	t.Cleanup(func() {
		if daemon.release != nil {
			select {
			case <-daemon.release:
			default:
				close(daemon.release)
			}
		}
	})
	return r
}

func (r *handlerRun) send(t *testing.T, cmd svc.Cmd) {
	t.Helper()
	r.sendRequest(t, svc.ChangeRequest{Cmd: cmd})
}

func (r *handlerRun) sendRequest(t *testing.T, request svc.ChangeRequest) {
	t.Helper()
	select {
	case r.requests <- request:
	case <-time.After(waitTimeout):
		t.Fatalf("the handler did not take %v", request.Cmd)
	}
}

// waitForState returns every status up to and including the first in state.
func (r *handlerRun) waitForState(t *testing.T, state svc.State) []svc.Status {
	t.Helper()
	var seen []svc.Status
	deadline := time.After(waitTimeout)
	for {
		select {
		case status := <-r.statuses:
			seen = append(seen, status)
			if status.State == state {
				return seen
			}
		case <-deadline:
			t.Fatalf("no %v status within %s; saw %+v\n%s", state, waitTimeout, seen, r.log.String())
		}
	}
}

// finish returns what Execute returned.
func (r *handlerRun) finish(t *testing.T) (specific bool, code uint32) {
	t.Helper()
	select {
	case result := <-r.done:
		return result[0] == 1, result[1]
	case <-time.After(waitTimeout):
		t.Fatalf("Execute did not return\n%s", r.log.String())
		return false, 0
	}
}

func (r *handlerRun) allStatuses() []svc.Status {
	var all []svc.Status
	for {
		select {
		case status := <-r.statuses:
			all = append(all, status)
		default:
			return all
		}
	}
}

func TestHandlerReportsStartPendingThenRunningOnceTheDaemonServes(t *testing.T) {
	daemon := &fakeDaemon{announce: 60 * time.Millisecond}
	r := startHandler(t, daemon, quickTiming)

	seen := r.waitForState(t, svc.Running)

	running := seen[len(seen)-1]
	if running.Accepts != acceptedControls {
		t.Errorf("accepts %#x, want %#x", running.Accepts, acceptedControls)
	}
	var checkpoint uint32
	for _, status := range seen[:len(seen)-1] {
		if status.State != svc.StartPending || status.Accepts != 0 || status.CheckPoint <= checkpoint || status.WaitHint == 0 {
			t.Errorf("before Running: %+v after check point %d; want StartPending, no controls, a check point that grows and a wait hint", status, checkpoint)
		}
		checkpoint = status.CheckPoint
	}
	if len(seen) < 3 {
		t.Errorf("only %d status reports before Running, want a StartPending for every tick of the 60 ms start", len(seen))
	}

	r.send(t, svc.Stop)
	if specific, code := r.finish(t); specific || code != 0 {
		t.Errorf("Execute returned (%v, %d), want a clean exit", specific, code)
	}
}

func TestHandlerStopsOnStopShutdownAndPreShutdown(t *testing.T) {
	for _, cmd := range []svc.Cmd{svc.Stop, svc.Shutdown, svc.PreShutdown} {
		t.Run(controlName(cmd), func(t *testing.T) {
			daemon := &fakeDaemon{shutdownTakes: 80 * time.Millisecond}
			r := startHandler(t, daemon, quickTiming)
			r.waitForState(t, svc.Running)

			r.send(t, cmd)
			stopping := r.waitForState(t, svc.StopPending)
			specific, code := r.finish(t)

			if specific || code != 0 {
				t.Errorf("Execute returned (%v, %d), want a clean exit", specific, code)
			}
			if len(stopping) != 1 || stopping[0].Accepts != 0 || stopping[0].CheckPoint == 0 {
				t.Errorf("first status after the request %+v, want StopPending with a check point", stopping)
			}
			// The routes and DNS entries are removed before the daemon returns, and
			// Stopped is reported by svc only after Execute does.
			if daemon.returnedAt.Load() == 0 || daemon.cancelledAt.Load() == 0 || daemon.returnedAt.Load() < daemon.cancelledAt.Load()+int64(70*time.Millisecond) {
				t.Errorf("Execute returned before the daemon finished its shutdown (cancelled %d, returned %d)", daemon.cancelledAt.Load(), daemon.returnedAt.Load())
			}
		})
	}
}

// Windows tells a service of a shutdown with a console control event too, which
// the Go runtime turns into SIGTERM; it must end the daemon in order and not
// the process.
func TestHandlerStopsInOrderOnAShutdownSignal(t *testing.T) {
	for _, received := range []os.Signal{syscall.SIGTERM, os.Interrupt} {
		t.Run(received.String(), func(t *testing.T) {
			daemon := &fakeDaemon{shutdownTakes: 80 * time.Millisecond}
			r := startHandler(t, daemon, quickTiming)
			r.waitForState(t, svc.Running)

			r.signals <- received
			r.waitForState(t, svc.StopPending)
			specific, code := r.finish(t)

			if specific || code != 0 {
				t.Errorf("Execute returned (%v, %d), want a clean exit", specific, code)
			}
			if daemon.returnedAt.Load() < daemon.cancelledAt.Load()+int64(70*time.Millisecond) || daemon.cancelledAt.Load() == 0 {
				t.Errorf("Execute returned before the daemon finished its shutdown (cancelled %d, returned %d)", daemon.cancelledAt.Load(), daemon.returnedAt.Load())
			}
		})
	}
}

func TestHandlerKeepsReportingProgressWhileTheDaemonShutsDown(t *testing.T) {
	daemon := &fakeDaemon{shutdownTakes: 150 * time.Millisecond}
	r := startHandler(t, daemon, serviceTiming{startTick: 5 * time.Millisecond, startTimeout: time.Second, stopTick: 10 * time.Millisecond, stopTimeout: time.Second})
	r.waitForState(t, svc.Running)

	r.send(t, svc.Stop)
	r.finish(t)

	var checkpoint, hint uint32
	var reports int
	for _, status := range r.allStatuses() {
		if status.State != svc.StopPending {
			continue
		}
		reports++
		if status.CheckPoint <= checkpoint || status.WaitHint < hint {
			t.Errorf("report %+v after check point %d and wait hint %d: both must grow", status, checkpoint, hint)
		}
		checkpoint, hint = status.CheckPoint, status.WaitHint
	}
	if reports < 5 {
		t.Errorf("%d StopPending reports in a 150 ms shutdown with a 10 ms tick, want at least 5", reports)
	}
}

func TestStopWaitHintGrowsWithTheTimeSpentAndStaysWithinTheBudget(t *testing.T) {
	if first := stopWaitHint(0); first != stopHintFloor {
		t.Errorf("first estimate %s, want %s", first, stopHintFloor)
	}
	if stopWaitHint(2*time.Second) <= stopWaitHint(time.Second) {
		t.Error("the wait hint does not grow with the time spent")
	}
	if got := stopWaitHint(time.Hour); got != productionTiming.stopTimeout {
		t.Errorf("wait hint after an hour %s, want the budget %s", got, productionTiming.stopTimeout)
	}
}

func TestHandlerAnswersInterrogateInEveryState(t *testing.T) {
	daemon := &fakeDaemon{announce: 80 * time.Millisecond, shutdownTakes: 80 * time.Millisecond}
	r := startHandler(t, daemon, quickTiming)

	starting := svc.Status{State: svc.StartPending, CheckPoint: 99}
	r.sendRequest(t, svc.ChangeRequest{Cmd: svc.Interrogate, CurrentStatus: starting})
	seen := r.waitForState(t, svc.Running)
	running := svc.Status{State: svc.Running, Accepts: acceptedControls, ProcessId: 4242}
	r.sendRequest(t, svc.ChangeRequest{Cmd: svc.Interrogate, CurrentStatus: running})
	r.send(t, svc.Stop)
	stopping := svc.Status{State: svc.StopPending, CheckPoint: 98}
	r.sendRequest(t, svc.ChangeRequest{Cmd: svc.Interrogate, CurrentStatus: stopping})
	r.finish(t)

	answers := map[svc.Status]bool{}
	for _, status := range append(seen, r.allStatuses()...) {
		answers[status] = true
	}
	for name, want := range map[string]svc.Status{"starting": starting, "running": running, "stopping": stopping} {
		if !answers[want] {
			t.Errorf("no answer with %s status %+v among %v", name, want, answers)
		}
	}
}

func TestHandlerFailsWhenTheDaemonCannotStartAndNeverReportsRunning(t *testing.T) {
	daemon := &fakeDaemon{failBeforeServing: errors.New("state directory is not usable")}
	r := startHandler(t, daemon, quickTiming)

	specific, code := r.finish(t)

	if !specific || code != serviceExitDaemonFailed {
		t.Errorf("Execute returned (%v, %d), want service-specific code %d", specific, code, serviceExitDaemonFailed)
	}
	for _, status := range r.allStatuses() {
		if status.State == svc.Running {
			t.Errorf("Running was reported for a daemon that failed to start: %+v", status)
		}
	}
	if !strings.Contains(r.log.String(), "state directory is not usable") {
		t.Errorf("the log lacks the reason:\n%s", r.log.String())
	}
}

func TestHandlerGivesUpOnADaemonThatNeverServes(t *testing.T) {
	daemon := &fakeDaemon{announce: -1}
	r := startHandler(t, daemon, quickTiming)

	specific, code := r.finish(t)

	if !specific || code != serviceExitStartTimeout {
		t.Errorf("Execute returned (%v, %d), want service-specific code %d", specific, code, serviceExitStartTimeout)
	}
	if daemon.cancelledAt.Load() == 0 || daemon.returnedAt.Load() == 0 {
		t.Error("the daemon that never served was left running")
	}
}

func TestHandlerExitsWithAFailureWhenTheDaemonEndsOnItsOwn(t *testing.T) {
	daemon := &fakeDaemon{endsOnItsOwn: make(chan error, 1)}
	r := startHandler(t, daemon, quickTiming)
	r.waitForState(t, svc.Running)

	daemon.endsOnItsOwn <- errors.New("the Reconciler stopped")
	specific, code := r.finish(t)

	if !specific || code != serviceExitDaemonFailed {
		t.Errorf("Execute returned (%v, %d), want service-specific code %d so that the failure actions run", specific, code, serviceExitDaemonFailed)
	}
}

// A stop that somebody asked for is a clean exit however the daemon ended: a
// failure code would make the service manager start the service again.
func TestHandlerExitsCleanlyOnARequestedStopEvenIfTheDaemonReportsAnError(t *testing.T) {
	daemon := &fakeDaemon{returnsError: errors.New("shut down engines: context deadline exceeded")}
	r := startHandler(t, daemon, quickTiming)
	r.waitForState(t, svc.Running)

	r.send(t, svc.Stop)

	if specific, code := r.finish(t); specific || code != 0 {
		t.Errorf("Execute returned (%v, %d), want (false, 0)", specific, code)
	}
	if !strings.Contains(r.log.String(), "shut down engines") {
		t.Errorf("the error of the shutdown was not logged:\n%s", r.log.String())
	}
}

func TestHandlerGivesUpOnADaemonThatDoesNotStop(t *testing.T) {
	daemon := &fakeDaemon{ignoresStop: true, release: make(chan struct{})}
	r := startHandler(t, daemon, quickTiming)
	r.waitForState(t, svc.Running)

	r.send(t, svc.Stop)
	specific, code := r.finish(t)

	if !specific || code != serviceExitStopTimeout {
		t.Errorf("Execute returned (%v, %d), want service-specific code %d", specific, code, serviceExitStopTimeout)
	}
}

func TestHandlerLogsPowerEventsAndSessionChangesAndKeepsRunning(t *testing.T) {
	daemon := &fakeDaemon{}
	r := startHandler(t, daemon, quickTiming)
	r.waitForState(t, svc.Running)

	r.sendRequest(t, svc.ChangeRequest{Cmd: svc.PowerEvent, EventType: 0x4})
	r.sendRequest(t, svc.ChangeRequest{Cmd: svc.PowerEvent, EventType: 0x12})
	r.sendRequest(t, svc.ChangeRequest{Cmd: svc.SessionChange, EventType: windows.WTS_SESSION_LOGON})
	r.sendRequest(t, svc.ChangeRequest{Cmd: svc.SessionChange, EventType: 0x63})
	r.send(t, svc.Stop)
	r.finish(t)

	logged := r.log.String()
	for _, want := range []string{"event=suspend", "event=\"resume automatic\"", "event=logon", "event=\"event 99\""} {
		if !strings.Contains(logged, want) {
			t.Errorf("log lacks %s:\n%s", want, logged)
		}
	}
	for _, status := range r.allStatuses() {
		if status.State == svc.Running && status.Accepts != acceptedControls {
			t.Errorf("a status with other controls than the accepted ones: %+v", status)
		}
	}
}

func TestAcceptedControlsAreExactlyTheOnesTheHandlerHandles(t *testing.T) {
	want := svc.AcceptStop | svc.AcceptShutdown | svc.AcceptPreShutdown | svc.AcceptPowerEvent | svc.AcceptSessionChange
	if acceptedControls != want {
		t.Fatalf("accepted controls %#x, want %#x", acceptedControls, want)
	}
}

func TestReadyHandlerSignalsOnTheListeningRecordOnlyAndStillLogsIt(t *testing.T) {
	logs := &syncBuffer{}
	log, ready := withReadySignal(slog.New(slog.NewTextHandler(logs, nil)))
	derived := log.With("component", "daemon").WithGroup("group")
	closed := func() bool {
		select {
		case <-ready:
			return true
		default:
			return false
		}
	}

	derived.Info("starting")
	if closed() {
		t.Fatal("ready after a record that is not the listening one")
	}
	derived.Info(listeningMessage, "socket", "x")
	if !closed() {
		t.Fatal("not ready after the listening record, through a derived logger")
	}
	derived.Info(listeningMessage, "socket", "again") // must not panic on a second close
	if !strings.Contains(logs.String(), "msg=listening") || !strings.Contains(logs.String(), "component=daemon") {
		t.Errorf("the record did not reach the log:\n%s", logs.String())
	}
}

// run is the daemon of main.go: the service waits for the record that it logs,
// so it has to be logged. This is the test that fails when the text changes.
func TestServiceReachesRunningOnTheRealDaemon(t *testing.T) {
	socket := newSocketPath(t)
	cfg := config{socket: socket, stateDir: filepath.Join(shortDir(t), "state"), fake: &fastFake}
	logs := &syncBuffer{}
	handler := newServiceHandlerWithPolicy(slog.New(slog.NewTextHandler(logs, nil)), manager.NewLogBuffer(""), cfg, everyone().pol)
	handler.timing = serviceTiming{startTick: 20 * time.Millisecond, startTimeout: waitTimeout, stopTick: 20 * time.Millisecond, stopTimeout: waitTimeout}
	r := &handlerRun{requests: make(chan svc.ChangeRequest), statuses: make(chan svc.Status, 1024), done: make(chan [2]uint32, 1), log: logs}
	go func() {
		specific, code := handler.Execute(nil, r.requests, r.statuses)
		var flag uint32
		if specific {
			flag = 1
		}
		r.done <- [2]uint32{flag, code}
	}()

	r.waitForState(t, svc.Running)
	conn, err := grpc.NewClient(transport.Target(socket), append(transport.DialOptions(), grpc.WithTransportCredentials(insecure.NewCredentials()))...)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	info, err := pb.NewDaemonServiceClient(conn).GetDaemonInfo(context.Background(), &pb.GetDaemonInfoRequest{})
	if err != nil || info.Version != version {
		t.Fatalf("GetDaemonInfo right after Running: %v, %v; want the daemon to answer", info, err)
	}
	conn.Close()

	r.send(t, svc.Shutdown)
	if specific, code := r.finish(t); specific || code != 0 {
		t.Errorf("Execute returned (%v, %d), want a clean exit\n%s", specific, code, logs.String())
	}
	socketGone(t, socket)
	mustBePrivate(t, cfg.stateDir)
	if !strings.Contains(logs.String(), "stopping") {
		t.Errorf("the daemon did not log that it stopped:\n%s", logs.String())
	}
}

func TestEnterServiceModeLeavesAConsoleProcessAlone(t *testing.T) {
	if enterServiceMode() != nil {
		t.Fatal("a process that no service manager started was taken for a service")
	}
}

func TestServiceNamesAreTheOnesTheAppAndTheInstallerUse(t *testing.T) {
	if serviceName != "PlaitwayHelper" || serviceDisplayName != "Plaitway Helper" {
		t.Fatalf("service %q, display name %q", serviceName, serviceDisplayName)
	}
}

// ---- the hardening of the process, in a process of its own ----

// Mitigations and the DLL search path cannot be undone, so the tests run them in
// a copy of the test binary.
const (
	hardeningModeEnv   = "PLAITWAYD_TEST_HARDENING"
	hardeningDirEnv    = "PLAITWAYD_TEST_HARDENING_DIR"
	hardeningSocketEnv = "PLAITWAYD_TEST_HARDENING_SOCKET"
	hardeningState     = "PLAITWAYD_TEST_HARDENING_STATE"

	modeControl   = "control"
	modeHardened  = "hardened"
	modeServe     = "serve"
	plantedDLL    = "plaitway-planted-test.dll"
	copiedDLL     = "plaitway-copied-test.dll"
	versionDLL    = "version.dll"
	helperReady   = "ready"
	helperStopped = "stopped"
)

// HELPER: not a test.
func TestHardeningHelperProcess(t *testing.T) {
	mode := os.Getenv(hardeningModeEnv)
	if mode == "" {
		t.Skip("only runs as the helper process of another test")
	}
	dir := os.Getenv(hardeningDirEnv)
	switch mode {
	case modeControl, modeHardened:
		os.Exit(hardeningProbe(mode == modeHardened, dir))
	case modeServe:
		os.Exit(hardenedServe())
	}
	os.Exit(2)
}

// hardeningProbe plants a library where a careless search would find it, in
// the current directory and on PATH, optionally hardens the process, and tries
// to load it by name. It prints what it saw, one fact per line.
func hardeningProbe(harden bool, dir string) int {
	if err := os.Chdir(dir); err != nil {
		fmt.Println("chdir failed:", err)
		return 2
	}
	os.Setenv("PATH", dir+";"+os.Getenv("PATH"))
	if harden {
		if err := hardenServiceProcess(); err != nil {
			fmt.Println("harden failed:", err)
			return 2
		}
	}
	cwd, _ := os.Getwd()
	system, _ := windows.GetSystemDirectory()
	fmt.Println("cwd-is-system:", strings.EqualFold(cwd, system))

	_, byNameErr := windows.LoadLibrary(plantedDLL)
	fmt.Println("planted-by-name-loads:", byNameErr == nil)
	_, absoluteErr := windows.LoadLibrary(filepath.Join(dir, copiedDLL))
	fmt.Println("copy-by-path-loads:", absoluteErr == nil)
	_, systemErr := windows.LoadLibrary("kernel32.dll")
	fmt.Println("system-by-name-loads:", systemErr == nil)

	flags, err := imageLoadPolicyInForce()
	fmt.Printf("image-load-flags: %d %v\n", flags, err)
	return 0
}

// imageLoadPolicyInForce reads the image load mitigation of this process.
func imageLoadPolicyInForce() (uint32, error) {
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessMitigationPolicy")
	var flags uint32
	result, _, err := proc.Call(uintptr(windows.CurrentProcess()), processImageLoadPolicy, uintptr(unsafe.Pointer(&flags)), unsafe.Sizeof(flags))
	if result == 0 {
		return 0, err
	}
	return flags, nil
}

// hardenedServe hardens the process like a service and then serves the fake
// daemon on a pipe until its stdin ends: the daemon has to work under the
// hardening.
func hardenedServe() int {
	if err := hardenServiceProcess(); err != nil {
		fmt.Println("harden failed:", err)
		return 2
	}
	cfg := config{socket: os.Getenv(hardeningSocketEnv), stateDir: os.Getenv(hardeningState), fake: &fastFake}
	logs := &syncBuffer{}
	handler := newServiceHandlerWithPolicy(slog.New(slog.NewTextHandler(logs, nil)), manager.NewLogBuffer(""), cfg, everyone().pol)
	requests := make(chan svc.ChangeRequest)
	statuses := make(chan svc.Status, 1024)
	done := make(chan struct{})
	go func() {
		handler.Execute(nil, requests, statuses)
		close(done)
	}()
	for status := range statuses {
		if status.State == svc.Running {
			break
		}
	}
	fmt.Println(helperReady)
	io.Copy(io.Discard, os.Stdin)
	requests <- svc.ChangeRequest{Cmd: svc.Stop}
	<-done
	fmt.Println(helperStopped)
	return 0
}

func runHardeningHelper(t *testing.T, mode string, env ...string) (output string, err error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHardeningHelperProcess$")
	cmd.Env = append(os.Environ(), append([]string{hardeningModeEnv + "=" + mode}, env...)...)
	var captured bytes.Buffer
	cmd.Stdout, cmd.Stderr = &captured, &captured
	err = cmd.Run()
	return captured.String(), err
}

// plantLibraries makes a directory with a library under two names: one the
// probe asks for by name only, and one it loads by path.
func plantLibraries(t *testing.T) string {
	t.Helper()
	system, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(system, versionDLL))
	if err != nil {
		t.Skipf("no %s to copy: %v", versionDLL, err)
	}
	dir := t.TempDir()
	for _, name := range []string{plantedDLL, copiedDLL} {
		if err := os.WriteFile(filepath.Join(dir, name), original, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func probeFacts(output string) map[string]string {
	facts := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		if name, value, ok := strings.Cut(strings.TrimSpace(line), ": "); ok {
			facts[name] = value
		}
	}
	return facts
}

// Without the hardening the planted library is found through PATH, which shows
// that the probe would see the attack.
func TestHardeningProbeFindsThePlantedLibraryWhenTheProcessIsNotHardened(t *testing.T) {
	output, err := runHardeningHelper(t, modeControl, hardeningDirEnv+"="+plantLibraries(t))
	facts := probeFacts(output)
	if err != nil || facts["planted-by-name-loads"] != "true" {
		t.Fatalf("control run: %v, facts %v\n%s", err, facts, output)
	}
}

func TestHardenServiceProcessKeepsPlantedLibrariesOutAndSystemOnesIn(t *testing.T) {
	output, err := runHardeningHelper(t, modeHardened, hardeningDirEnv+"="+plantLibraries(t))
	facts := probeFacts(output)
	if err != nil {
		t.Fatalf("hardened run: %v\n%s", err, output)
	}
	want := map[string]string{
		"cwd-is-system":         "true",
		"planted-by-name-loads": "false",
		"copy-by-path-loads":    "true",
		"system-by-name-loads":  "true",
	}
	for name, value := range want {
		if facts[name] != value {
			t.Errorf("%s = %q, want %q\n%s", name, facts[name], value, output)
		}
	}
	// No remote images (1), no low label images (2), System32 first (4).
	if wantFlags := "7 <nil>"; facts["image-load-flags"] != wantFlags {
		t.Errorf("image load mitigation %q, want %q", facts["image-load-flags"], wantFlags)
	}
}

// The daemon needs files, pipes, gRPC and child processes; none may break.
func TestDaemonServesUnderTheHardeningOfAService(t *testing.T) {
	socket := newSocketPath(t)
	stateDir := filepath.Join(shortDir(t), "state")
	cmd := exec.Command(os.Args[0], "-test.run=^TestHardeningHelperProcess$")
	cmd.Env = append(os.Environ(), hardeningModeEnv+"="+modeServe, hardeningSocketEnv+"="+socket, hardeningState+"="+stateDir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	finished := false
	defer func() {
		if !finished {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()

	lines := bufio.NewScanner(stdout)
	waitForLine(t, lines, helperReady, &stderr)

	conn, err := grpc.NewClient(transport.Target(socket), append(transport.DialOptions(), grpc.WithTransportCredentials(insecure.NewCredentials()))...)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	if _, err := pb.NewDaemonServiceClient(conn).GetDaemonInfo(ctx, &pb.GetDaemonInfoRequest{}); err != nil {
		t.Fatalf("GetDaemonInfo against the hardened daemon: %v\n%s", err, stderr.String())
	}
	if out, err := exec.Command("cmd", "/c", "ver").CombinedOutput(); err != nil {
		t.Fatalf("the test process cannot start a program: %v %s", err, out)
	}

	stdin.Close()
	waitForLine(t, lines, helperStopped, &stderr)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("hardened daemon exited with %v\n%s", err, stderr.String())
	}
	finished = true
	socketGone(t, socket)
}

func waitForLine(t *testing.T, lines *bufio.Scanner, want string, stderr *bytes.Buffer) {
	t.Helper()
	found := make(chan bool, 1)
	go func() {
		for lines.Scan() {
			if strings.TrimSpace(lines.Text()) == want {
				found <- true
				return
			}
			if strings.Contains(lines.Text(), "failed") {
				break
			}
		}
		found <- false
	}()
	select {
	case ok := <-found:
		if !ok {
			t.Fatalf("the helper never printed %q\n%s", want, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the helper did not print %q in time\n%s", want, stderr.String())
	}
}
