package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"

	"github.com/KoukeNeko/Plaitway/internal/manager"
)

const (
	serviceName        = "PlaitwayHelper"
	serviceDisplayName = "Plaitway Helper"
	// The wording of the macOS helper in the app ("manages routes and DNS with
	// administrator rights"), for a list in which the name is all there is.
	serviceDescription = "Runs the VPN tunnels of Plaitway and manages their routes and DNS entries with administrator rights."

	// listeningMessage is the record that run logs once the pipe is open, the
	// state is loaded and the server is about to serve. The service reports
	// Running on it, so that "running" means "answers calls", and a daemon that
	// fails while it starts is reported as that and not as a service that
	// stopped a moment after it started. TestServiceReachesRunningOnTheRealDaemon
	// fails if run stops logging it.
	listeningMessage = "listening"

	// What the service manager may send. Stop is for people and tools,
	// PreShutdown comes first at system shutdown, while the network still works
	// and the routes and DNS entries can be removed, and Shutdown is the
	// fallback for a stop that PreShutdown did not complete. Power events and
	// session changes are logged; the daemon watches the network for itself.
	acceptedControls = svc.AcceptStop | svc.AcceptShutdown | svc.AcceptPreShutdown | svc.AcceptPowerEvent | svc.AcceptSessionChange

	// Service-specific exit codes. A non-zero one makes the service manager run
	// the failure actions, which is wanted for each of them.
	serviceExitDaemonFailed uint32 = 1
	serviceExitStartTimeout uint32 = 2
	serviceExitStopTimeout  uint32 = 3
	serviceExitPrepare      uint32 = 4
	// exitDispatcherFailed is the exit code of the process when it was started
	// as a service but could not reach the service manager.
	exitDispatcherFailed = 1

	// stopGrace is what the service allows beyond the daemon's own shutdown
	// budget (daemon.go), which bounds every step of it.
	stopGrace = 5 * time.Second
	// stopHintFloor is the first estimate of a stop, short because most stops
	// are quick; every later report adds the time already spent, see stopWaitHint.
	stopHintFloor = 3 * time.Second
	// startWaitHint is how long the service manager waits for the next report
	// while the service starts.
	startWaitHint = 10 * time.Second
)

// serviceTiming is how often the service reports progress and when it gives up.
type serviceTiming struct {
	startTick, startTimeout time.Duration
	stopTick, stopTimeout   time.Duration
}

// productionTiming keeps the stop budget in step with daemon.go: the daemon
// needs up to serverStopTimeout and shutdownTimeout to remove the routes and
// DNS entries, and the service must outwait that.
var productionTiming = serviceTiming{
	startTick:    time.Second,
	startTimeout: 25 * time.Second,
	stopTick:     time.Second,
	stopTimeout:  serverStopTimeout + shutdownTimeout + stopGrace,
}

// stopWaitHint is what the service manager is told to expect of a stop that has
// lasted elapsed so far. It grows with the time spent, so that a stop that
// drags on is waited for as long as it makes progress (the check point also
// grows with each report), and it never promises more than the whole budget.
func stopWaitHint(elapsed time.Duration) time.Duration {
	return min(stopHintFloor+elapsed, productionTiming.stopTimeout)
}

// pendingStatus is a report that an operation is under way.
func pendingStatus(state svc.State, checkpoint uint32, waitHint time.Duration) svc.Status {
	return svc.Status{State: state, CheckPoint: checkpoint, WaitHint: uint32(waitHint.Milliseconds())}
}

// serviceHandler runs the daemon for the service manager.
type serviceHandler struct {
	log *slog.Logger
	// run serves until ctx ends and returns after the routes and DNS entries
	// are gone: it is run of main.go, whose shutdown order is the contract.
	run func(ctx context.Context) error
	// ready is closed when run reports that it serves.
	ready  <-chan struct{}
	timing serviceTiming
	// stopSignals end the daemon like a stop request does. Nil means the
	// signals of the process, which the tests replace.
	stopSignals <-chan os.Signal
}

// execution is one run of the daemon under Execute.
type execution struct {
	cancel   context.CancelFunc
	finished <-chan error
	requests <-chan svc.ChangeRequest
	status   chan<- svc.Status
	signals  <-chan os.Signal
}

// Execute implements svc.Handler.
func (h *serviceHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (specificCode bool, exitCode uint32) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- h.run(ctx) }()

	signals := h.stopSignals
	if signals == nil {
		// Windows tells a service of a shutdown with a console control event as
		// well, and the Go runtime ends a process that nobody listens for it in.
		// Listening makes it a stop request, so that the routes and DNS entries
		// are removed first, and the runtime holds the process until then.
		processSignals := make(chan os.Signal, 1)
		signal.Notify(processSignals, syscall.SIGTERM, os.Interrupt)
		defer signal.Stop(processSignals)
		signals = processSignals
	}
	run := &execution{cancel: cancel, finished: finished, requests: requests, status: status, signals: signals}

	if code := h.awaitStart(run); code != 0 {
		return true, code
	}
	status <- svc.Status{State: svc.Running, Accepts: acceptedControls}
	h.log.Info("service running", "name", serviceName)
	return h.serveUntilStopped(run)
}

// awaitStart reports StartPending until the daemon serves, and returns 0 when
// it does. Controls are not accepted yet, so a stop request cannot arrive.
func (h *serviceHandler) awaitStart(run *execution) uint32 {
	ticker := time.NewTicker(h.timing.startTick)
	defer ticker.Stop()
	timeout := time.After(h.timing.startTimeout)
	var checkpoint uint32
	report := func() {
		checkpoint++
		run.status <- pendingStatus(svc.StartPending, checkpoint, startWaitHint)
	}
	report()
	for {
		select {
		case <-h.ready:
			return 0
		case err := <-run.finished:
			h.log.Error("the daemon ended before it served", "err", err)
			return serviceExitDaemonFailed
		case <-ticker.C:
			report()
		case <-timeout:
			h.log.Error("the daemon did not serve in time", "timeout", h.timing.startTimeout)
			run.cancel()
			h.awaitEnd(run.finished)
			return serviceExitStartTimeout
		case request := <-run.requests:
			answerInterrogate(request, run.status)
		}
	}
}

// awaitEnd waits for the daemon to finish after cancel, within the stop budget.
func (h *serviceHandler) awaitEnd(finished <-chan error) {
	select {
	case <-finished:
	case <-time.After(h.timing.stopTimeout):
		h.log.Error("the daemon did not stop in time", "timeout", h.timing.stopTimeout)
	}
}

func (h *serviceHandler) serveUntilStopped(run *execution) (bool, uint32) {
	for {
		select {
		case request := <-run.requests:
			switch request.Cmd {
			case svc.Interrogate:
				run.status <- request.CurrentStatus
			case svc.Stop, svc.Shutdown, svc.PreShutdown:
				h.log.Info("stopping", "control", controlName(request.Cmd))
				return h.stop(run)
			case svc.PowerEvent:
				h.log.Info("power event", "event", powerEventName(request.EventType))
			case svc.SessionChange:
				// The session id is behind EventData, which the service manager
				// owns only until the control handler returns, and svc passes it
				// on after that. Reading it would be reading freed memory.
				h.log.Info("session change", "event", sessionEventName(request.EventType))
			}
		case received := <-run.signals:
			h.log.Info("stopping", "signal", received.String())
			return h.stop(run)
		case err := <-run.finished:
			h.log.Error("the daemon stopped on its own", "err", err)
			return true, serviceExitDaemonFailed
		}
	}
}

// stop ends the daemon and reports StopPending with a growing check point and
// wait hint until it has finished. Whatever the daemon returns, a stop that was
// asked for is a clean exit: a non-zero code would make the service manager
// restart the service that somebody has just stopped.
func (h *serviceHandler) stop(run *execution) (bool, uint32) {
	run.cancel()
	started := time.Now()
	ticker := time.NewTicker(h.timing.stopTick)
	defer ticker.Stop()
	timeout := time.After(h.timing.stopTimeout)
	var checkpoint uint32
	report := func() {
		checkpoint++
		run.status <- pendingStatus(svc.StopPending, checkpoint, stopWaitHint(time.Since(started)))
	}
	report()
	for {
		select {
		case err := <-run.finished:
			if err != nil {
				h.log.Error("the daemon stopped with an error", "err", err)
			}
			return false, 0
		case <-ticker.C:
			report()
		case <-timeout:
			h.log.Error("the daemon did not stop in time; routes or DNS entries may be left for the next start to repair", "timeout", h.timing.stopTimeout)
			return true, serviceExitStopTimeout
		case request := <-run.requests:
			answerInterrogate(request, run.status)
		}
	}
}

func answerInterrogate(request svc.ChangeRequest, status chan<- svc.Status) {
	if request.Cmd == svc.Interrogate {
		status <- request.CurrentStatus
	}
}

func controlName(cmd svc.Cmd) string {
	switch cmd {
	case svc.Stop:
		return "stop"
	case svc.Shutdown:
		return "shutdown"
	case svc.PreShutdown:
		return "preshutdown"
	}
	return fmt.Sprintf("control %d", cmd)
}

// Power events and session changes the service manager reports, as named by
// the PBT_ and WTS_ constants.
var (
	powerEventNames = map[uint32]string{
		0x4:    "suspend",
		0x7:    "resume from suspend",
		0xa:    "power status change",
		0x12:   "resume automatic",
		0x8013: "power setting change",
	}
	sessionEventNames = map[uint32]string{
		windows.WTS_CONSOLE_CONNECT:        "console connect",
		windows.WTS_CONSOLE_DISCONNECT:     "console disconnect",
		0x3:                                "remote connect",
		0x4:                                "remote disconnect",
		windows.WTS_SESSION_LOGON:          "logon",
		windows.WTS_SESSION_LOGOFF:         "logoff",
		windows.WTS_SESSION_LOCK:           "lock",
		windows.WTS_SESSION_UNLOCK:         "unlock",
		windows.WTS_SESSION_REMOTE_CONTROL: "remote control",
		windows.WTS_SESSION_CREATE:         "create",
		windows.WTS_SESSION_TERMINATE:      "terminate",
	}
)

func powerEventName(eventType uint32) string   { return eventName(powerEventNames, eventType) }
func sessionEventName(eventType uint32) string { return eventName(sessionEventNames, eventType) }

func eventName(names map[uint32]string, eventType uint32) string {
	if name, ok := names[eventType]; ok {
		return name
	}
	return fmt.Sprintf("event %d", eventType)
}

// readyHandler tells when the daemon says it serves.
type readyHandler struct {
	slog.Handler
	once  *sync.Once
	ready chan struct{}
}

func (h readyHandler) Handle(ctx context.Context, record slog.Record) error {
	err := h.Handler.Handle(ctx, record)
	if record.Message == listeningMessage {
		h.once.Do(func() { close(h.ready) })
	}
	return err
}

func (h readyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.Handler = h.Handler.WithAttrs(attrs)
	return h
}

func (h readyHandler) WithGroup(name string) slog.Handler {
	h.Handler = h.Handler.WithGroup(name)
	return h
}

// withReadySignal wraps log so that the returned channel closes when the daemon
// logs that it serves.
func withReadySignal(log *slog.Logger) (*slog.Logger, <-chan struct{}) {
	ready := make(chan struct{})
	return slog.New(readyHandler{Handler: log.Handler(), once: new(sync.Once), ready: ready}), ready
}

// newServiceHandler runs the daemon the way main does, with the real policy.
func newServiceHandler(log *slog.Logger, daemonLog *manager.LogBuffer, cfg config) *serviceHandler {
	return newServiceHandlerWithPolicy(log, daemonLog, cfg, newPolicy())
}

// newServiceHandlerWithPolicy exists for the tests, which have to say who may
// call the daemon.
func newServiceHandlerWithPolicy(log *slog.Logger, daemonLog *manager.LogBuffer, cfg config, pol *policy) *serviceHandler {
	signalling, ready := withReadySignal(log)
	return &serviceHandler{
		log: log,
		run: func(ctx context.Context) error {
			return run(ctx, signalling, daemonLog, cfg, pol)
		},
		ready:  ready,
		timing: productionTiming,
	}
}

// enterServiceMode is called first thing by main. When the process was started
// by the service manager it hardens the process, before anything loads a
// library or reads a path, and returns what runs the service; otherwise it
// returns nil and the process is the console daemon it has always been. A
// failure is kept for the returned function to log, because there is no log
// yet.
func enterServiceMode() func(*slog.Logger, *manager.LogBuffer, config) int {
	inService, err := svc.IsWindowsService()
	if err == nil && !inService {
		return nil
	}
	if err == nil {
		err = hardenServiceProcess()
	}
	return func(log *slog.Logger, daemonLog *manager.LogBuffer, cfg config) int {
		return serveAsService(err, log, daemonLog, cfg)
	}
}

func serveAsService(prepareErr error, log *slog.Logger, daemonLog *manager.LogBuffer, cfg config) int {
	if prepareErr != nil {
		log.Error("cannot start as a service", "err", prepareErr)
		return int(serviceExitPrepare)
	}
	if err := svc.Run(serviceName, newServiceHandler(log, daemonLog, cfg)); err != nil {
		log.Error("the service dispatcher failed", "err", err)
		return exitDispatcherFailed
	}
	return 0
}

const (
	// A service loads libraries from its own directory, which the installation
	// shows to be writable by administrators only, and from System32. Not from
	// the current directory or from PATH, whatever the order of the default
	// search says.
	dllSearchApplicationAndSystem32 = windows.LOAD_LIBRARY_SEARCH_APPLICATION_DIR | windows.LOAD_LIBRARY_SEARCH_SYSTEM32

	// processImageLoadPolicy is the PROCESS_MITIGATION_POLICY of
	// SetProcessMitigationPolicy for where images may be loaded from.
	processImageLoadPolicy = 10
	// The flags of PROCESS_MITIGATION_IMAGE_LOAD_POLICY: no image from a share,
	// none from a file with a low integrity label, and System32 first for a name
	// that exists there as well.
	imageLoadNoRemoteImages       = 1 << 0
	imageLoadNoLowLabelImages     = 1 << 1
	imageLoadPreferSystem32Images = 1 << 2
	imageLoadPolicyFlags          = uint32(imageLoadNoRemoteImages | imageLoadNoLowLabelImages | imageLoadPreferSystem32Images)
)

var setProcessMitigationPolicy = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetProcessMitigationPolicy")

// hardenServiceProcess limits what the privileged process can be made to load
// or open by a path somebody else controls. The mitigations that would break
// the daemon are left out on purpose: no ban on child processes (openvpn), on
// non-Microsoft images (wintun, openvpn) or on dynamic code (security software
// that hooks the process needs it).
func hardenServiceProcess() error {
	if err := windows.SetDefaultDllDirectories(dllSearchApplicationAndSystem32); err != nil {
		return fmt.Errorf("restrict the DLL search path: %w", err)
	}
	// A service starts in System32 already; saying so keeps it true whatever
	// started the process, and keeps every relative path out of the user's hands.
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return fmt.Errorf("find the system directory: %w", err)
	}
	if err := os.Chdir(system); err != nil {
		return fmt.Errorf("change to the system directory: %w", err)
	}
	if err := restrictImageLoading(); err != nil {
		return fmt.Errorf("restrict where images are loaded from: %w", err)
	}
	return nil
}

func restrictImageLoading() error {
	if err := setProcessMitigationPolicy.Find(); err != nil {
		return err
	}
	flags := imageLoadPolicyFlags
	result, _, callErr := setProcessMitigationPolicy.Call(processImageLoadPolicy, uintptr(unsafe.Pointer(&flags)), unsafe.Sizeof(flags))
	if result == 0 {
		return errors.Join(errors.New("SetProcessMitigationPolicy failed"), callErr)
	}
	return nil
}
