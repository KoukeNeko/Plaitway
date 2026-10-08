package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/KoukeNeko/Plaitway/internal/fsperm"
	winnet "github.com/KoukeNeko/Plaitway/internal/osnet/windows"
)

// Exit codes of the subcommands. status uses 3 and 4 the way service scripts
// read them: installed but not running, and not installed.
const (
	exitFailed       = 1
	exitUsage        = 2
	exitNotRunning   = 3
	exitNotInstalled = 4
)

const (
	commandInstall   = "install"
	commandUninstall = "uninstall"
	commandStart     = "start"
	commandStop      = "stop"
	commandStatus    = "status"
)

var subcommandNames = []string{commandInstall, commandUninstall, commandStart, commandStop, commandStatus}

const (
	// localSystem is the account of the service, which the SCM spells so.
	localSystem = "LocalSystem"
	// The network stack is up before the first tunnel; WireGuard's own service
	// declares the same two.
	dependencyNetworkStore = "Nsi"
	dependencyTCPIP        = "Tcpip"

	// serviceAccessList (SDDL) lets every interactive user read the state of the
	// service, which is what the app shows as running, stopped or not installed
	// without elevation, and lets only SYSTEM and Administrators control it.
	// LC reads the status, LO interrogates, RC reads this list.
	serviceAccessList = "D:" +
		"(A;;CCLCSWRPWPDTLOCRRC;;;SY)" +
		"(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;BA)" +
		"(A;;LCLORC;;;IU)"

	// Restart after 5 s, 10 s and 30 s; a fourth failure and the ones after it
	// get the last action again. The count starts over after a day without one.
	restartDelayFirst   = 5 * time.Second
	restartDelaySecond  = 10 * time.Second
	restartDelayLater   = 30 * time.Second
	failureCountResetAt = 24 * time.Hour

	// preshutdownTimeout is how long the service manager waits for a service
	// that has been told about the shutdown of the machine. It has to exceed the
	// time the daemon takes to remove routes and DNS entries; the default of three
	// minutes would hold up a machine whose daemon hangs.
	preshutdownTimeout = 30 * time.Second
)

var (
	errNeedsElevation   = errors.New("this needs administrator rights: run from an elevated shell")
	errNotInstalled     = errors.New("is not installed")
	errAlreadyInstalled = errors.New("is already installed: use -update to replace its registration")
)

// serviceSettings is everything that install sets on the service.
type serviceSettings struct {
	config             mgr.Config
	recovery           []mgr.RecoveryAction
	resetPeriod        time.Duration
	restartOnNonCrash  bool
	accessList         string
	preshutdownTimeout time.Duration
}

// desiredSettings is the registration of the daemon at executable. The daemon
// needs no flags: its defaults are the production ones. args exist for the
// tests, which run it on the fake backend in a scratch directory.
func desiredSettings(executable string, args []string) serviceSettings {
	return serviceSettings{
		config: mgr.Config{
			ServiceType: windows.SERVICE_WIN32_OWN_PROCESS,
			// A VPN has to be up before anybody logs on, and its auto-connect
			// profiles with it. A delayed start waits about two minutes after boot
			// for the other services.
			StartType:        mgr.StartAutomatic,
			DelayedAutoStart: false,
			ErrorControl:     mgr.ErrorNormal,
			BinaryPathName:   binaryPath(executable, args),
			Dependencies:     []string{dependencyNetworkStore, dependencyTCPIP},
			ServiceStartName: localSystem,
			DisplayName:      serviceDisplayName,
			Description:      serviceDescription,
			// The service gets its own SID in its token, so that files can be
			// granted to this service alone, and its token is not restricted: a
			// restricted token would stop it from adding adapters and routes.
			SidType: windows.SERVICE_SID_TYPE_UNRESTRICTED,
		},
		recovery: []mgr.RecoveryAction{
			{Type: mgr.ServiceRestart, Delay: restartDelayFirst},
			{Type: mgr.ServiceRestart, Delay: restartDelaySecond},
			{Type: mgr.ServiceRestart, Delay: restartDelayLater},
		},
		resetPeriod: failureCountResetAt,
		// The daemon exits non-zero when it fails on its own, which is a failure
		// that did not crash the process.
		restartOnNonCrash:  true,
		accessList:         serviceAccessList,
		preshutdownTimeout: preshutdownTimeout,
	}
}

// desiredSettings is the registration these commands make: that of the daemon,
// with the start type of a test service when the commands are the tests'.
func (c *commands) desiredSettings(executable string) serviceSettings {
	settings := desiredSettings(executable, c.daemonArgs)
	if c.startOnDemand {
		settings.config.StartType = mgr.StartManual
	}
	return settings
}

// binaryPath is the command line of the service. The executable is always
// quoted, so that a path with spaces cannot be read as a shorter one.
func binaryPath(executable string, args []string) string {
	words := []string{`"` + executable + `"`}
	for _, arg := range args {
		words = append(words, syscall.EscapeArg(arg))
	}
	return strings.Join(words, " ")
}

// serviceControl is the Service Control Manager as the commands use it.
type serviceControl interface {
	// Open returns the registered service, or an error that is errNotInstalled.
	Open(name string) (registeredService, error)
	// Create registers a service for executable; the caller applies the settings.
	Create(name, executable string) (registeredService, error)
	Close() error
}

type registeredService interface {
	Status() (svc.Status, error)
	Settings() (serviceSettings, error)
	Apply(serviceSettings) error
	Start() error
	// Stop asks the service to stop; it does not wait.
	Stop() error
	Delete() error
	Close() error
}

// waitTimings are how long the commands wait for a state and how often they ask.
type waitTimings struct {
	poll, start, stop time.Duration
	// settle is how long a service that is running has to stay so to count as
	// started: it is reported running as soon as it serves, and a daemon that
	// fails in its first moments must not count.
	settle time.Duration
}

var productionWaits = waitTimings{
	poll:   200 * time.Millisecond,
	start:  productionTiming.startTimeout + 10*time.Second,
	stop:   productionTiming.stopTimeout + 10*time.Second,
	settle: time.Second,
}

// commands is the subcommands with everything that touches the machine passed
// in, so that the tests run them against a fake Service Control Manager.
type commands struct {
	out, errOut io.Writer
	name        string
	// daemonArgs go to the service's command line; only the tests set them.
	daemonArgs       []string
	isElevated       func() bool
	executable       func() (string, error)
	checkInstallPath func(string) error
	connect          func() (serviceControl, error)
	// query reads the state with the least access there is, no elevation.
	query    func(name string) (svc.Status, error)
	dataRoot func() (string, error)
	logFile  string
	purge    func(root string) error
	// sweepDNS removes the DNS rules that Plaitway left in the registry, without
	// the daemon.
	sweepDNS func() (winnet.SweepResult, error)
	waits    waitTimings
	// startOnDemand registers the service for start on demand, not at boot. Only
	// the tests set it: a test service that outlives a killed test must not come
	// back with the PC.
	startOnDemand bool
}

func newCommands(out, errOut io.Writer) *commands {
	return &commands{
		out:              out,
		errOut:           errOut,
		name:             serviceName,
		isElevated:       isPrivileged,
		executable:       os.Executable,
		checkInstallPath: fsperm.CheckAdminOnlyPath,
		connect:          connectSCM,
		query:            queryServiceStatus,
		dataRoot:         fsperm.DataRoot,
		logFile:          productionLogFile(),
		purge:            purgeDataRoot,
		sweepDNS:         func() (winnet.SweepResult, error) { return winnet.SweepDNS(warningLog(errOut)) },
		waits:            productionWaits,
	}
}

// warningLog logs to w what needs a person's attention.
func warningLog(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// runSubcommand runs the subcommand named by args[0], if there is one. The
// daemon's own flags all start with a dash, so a first word that names a
// subcommand cannot be taken for one.
func runSubcommand(args []string) (exitCode int, handled bool) {
	if len(args) == 0 || !slices.Contains(subcommandNames, args[0]) {
		return 0, false
	}
	return newCommands(os.Stdout, os.Stderr).run(args), true
}

// codedError carries an exit code, and a message when there is one to print.
type codedError struct {
	code    int
	message string
}

func (e codedError) Error() string { return e.message }

func usageError(err error) error { return codedError{code: exitUsage, message: err.Error()} }

func (c *commands) run(args []string) int {
	command, rest := args[0], args[1:]
	err := c.dispatch(command, rest)
	if err == nil {
		return 0
	}
	var coded codedError
	if errors.As(err, &coded) {
		if coded.message != "" {
			fmt.Fprintf(c.errOut, "plaitwayd %s: %s\n", command, coded.message)
		}
		return coded.code
	}
	fmt.Fprintf(c.errOut, "plaitwayd %s: %v\n", command, err)
	return exitFailed
}

func (c *commands) dispatch(command string, args []string) error {
	switch command {
	case commandInstall:
		options, err := parseInstallArgs(args)
		if err != nil {
			return usageError(err)
		}
		return c.install(options)
	case commandUninstall:
		options, err := parseUninstallArgs(args)
		if err != nil {
			return usageError(err)
		}
		return c.uninstall(options)
	case commandStart:
		return withoutOperands(args, c.start)
	case commandStop:
		return withoutOperands(args, c.stop)
	}
	return withoutOperands(args, c.status)
}

func withoutOperands(args []string, run func() error) error {
	if len(args) > 0 {
		return usageError(fmt.Errorf("unexpected argument %q", args[0]))
	}
	return run()
}

type installOptions struct {
	start, update bool
}

type uninstallOptions struct {
	purge bool
}

func newFlagSet(command string) *flag.FlagSet {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func parseInstallArgs(args []string) (installOptions, error) {
	var options installOptions
	fs := newFlagSet(commandInstall)
	fs.BoolVar(&options.start, "start", false, "start the service after installing it")
	fs.BoolVar(&options.update, "update", false, "replace the registration of an installed service, restarting it if it runs")
	return options, parseWithoutOperands(fs, args)
}

func parseUninstallArgs(args []string) (uninstallOptions, error) {
	var options uninstallOptions
	fs := newFlagSet(commandUninstall)
	fs.BoolVar(&options.purge, "purge", false, "also delete the profiles, the route journal and the logs")
	return options, parseWithoutOperands(fs, args)
}

func parseWithoutOperands(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return nil
}

// install registers the daemon with the Service Control Manager, or with
// -update replaces the registration of an installed one.
func (c *commands) install(options installOptions) error {
	if !c.isElevated() {
		return errNeedsElevation
	}
	executable, err := c.executable()
	if err != nil {
		return fmt.Errorf("find this program: %w", err)
	}
	if err := c.checkInstallPath(executable); err != nil {
		return fmt.Errorf("%s cannot be a service: %w; use an administrator-only location such as Program Files", executable, err)
	}
	scm, err := c.connect()
	if err != nil {
		return c.describe(err)
	}
	defer scm.Close()

	existing, err := scm.Open(c.name)
	switch {
	case err == nil:
		defer existing.Close()
		if !options.update {
			return fmt.Errorf("%s %w", c.name, errAlreadyInstalled)
		}
		return c.update(existing, executable, options)
	case !errors.Is(err, errNotInstalled):
		return c.describe(err)
	case options.update:
		return fmt.Errorf("%s %w: install it without -update", c.name, errNotInstalled)
	}
	return c.create(scm, executable, options)
}

// create registers a new service. A service that was created and could not be
// configured is deleted again, so that a failed install leaves nothing and can
// be run again.
func (c *commands) create(scm serviceControl, executable string, options installOptions) error {
	created, err := scm.Create(c.name, executable)
	if err != nil {
		return c.describe(err)
	}
	defer created.Close()
	if err := created.Apply(c.desiredSettings(executable)); err != nil {
		return alsoFailed(fmt.Errorf("configure %s: %w", c.name, c.describe(err)), c.removeHalfInstalled(created))
	}
	if !options.start {
		fmt.Fprintf(c.out, "%s installed\n", c.name)
		return nil
	}
	if err := c.startAndWait(created); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "%s installed, running\n", c.name)
	return nil
}

func (c *commands) removeHalfInstalled(created registeredService) error {
	if err := created.Delete(); err != nil {
		return fmt.Errorf("remove the half-installed %s: %w", c.name, err)
	}
	return nil
}

// update stops the service if it runs, replaces its registration and starts it
// again, in that order. If the new registration does not apply or does not
// start, the previous one is put back and the service is restarted if it was
// running. Only the registration is restored: the files are the caller's.
func (c *commands) update(existing registeredService, executable string, options installOptions) error {
	previous, err := existing.Settings()
	if err != nil {
		return fmt.Errorf("read the registration of %s: %w", c.name, c.describe(err))
	}
	before, err := existing.Status()
	if err != nil {
		return fmt.Errorf("read the state of %s: %w", c.name, c.describe(err))
	}
	wasRunning := before.State != svc.Stopped
	if err := c.stopAndWait(existing); err != nil {
		return err
	}
	if err := existing.Apply(c.desiredSettings(executable)); err != nil {
		return c.rollback(existing, previous, wasRunning, fmt.Errorf("replace the registration of %s: %w", c.name, c.describe(err)))
	}
	if !wasRunning && !options.start {
		fmt.Fprintf(c.out, "%s updated\n", c.name)
		return nil
	}
	if err := c.startAndWait(existing); err != nil {
		return c.rollback(existing, previous, wasRunning, err)
	}
	fmt.Fprintf(c.out, "%s updated, running\n", c.name)
	return nil
}

// rollback puts the previous registration back, restarts the service if it was
// running, and returns cause together with whatever went wrong on the way.
func (c *commands) rollback(existing registeredService, previous serviceSettings, wasRunning bool, cause error) error {
	if err := existing.Apply(previous); err != nil {
		return alsoFailed(cause, fmt.Errorf("restore the previous registration of %s: %w", c.name, c.describe(err)))
	}
	if wasRunning {
		if err := c.startAndWait(existing); err != nil {
			return alsoFailed(cause, fmt.Errorf("start the previous version again: %w", err))
		}
	}
	return fmt.Errorf("%w; the previous registration was restored", cause)
}

// alsoFailed keeps a message to one line where errors.Join would make several.
func alsoFailed(cause, other error) error {
	if other == nil {
		return cause
	}
	return fmt.Errorf("%w; %v", cause, other)
}

// uninstall stops the service, waits for it, deletes it, removes the DNS rules
// the daemon left in the registry and, with -purge, deletes the data the
// daemon keeps. The data stays otherwise: the profiles belong to the people who
// made them, and an uninstall cannot tell from an upgrade.
//
// The rules are removed here, and not left to the daemon, because they are the
// one thing a dead daemon leaves that outlives a reboot: a service that cannot
// start (a failed check of wintun or openvpn) after a power loss with a full
// tunnel up would otherwise keep all DNS of the PC on a server that is gone.
// Routes and adapters are not swept: they live in the active store and with
// the process, and go with the reboot.
func (c *commands) uninstall(options uninstallOptions) error {
	if !c.isElevated() {
		return errNeedsElevation
	}
	scm, err := c.connect()
	if err != nil {
		return c.describe(err)
	}
	defer scm.Close()

	existing, err := scm.Open(c.name)
	switch {
	case err == nil:
		defer existing.Close()
		if err := c.removeService(existing); err != nil {
			return err
		}
	case errors.Is(err, errNotInstalled):
		fmt.Fprintf(c.out, "%s is not installed\n", c.name)
	default:
		return c.describe(err)
	}
	sweepErr := c.sweepDNSRules()
	if !options.purge {
		return sweepErr
	}
	// The journal and the profiles are not needed to find the rules: they are
	// found by their marker. So a rule that stays does not keep the data.
	purgeErr := c.purgeData()
	if sweepErr == nil {
		return purgeErr
	}
	return alsoFailed(sweepErr, purgeErr)
}

// sweepDNSRules removes the DNS rules of Plaitway and tells what it did. Rules
// that remain are an error with the command that removes them by hand.
func (c *commands) sweepDNSRules() error {
	result, err := c.sweepDNS()
	if len(result.Removed) > 0 {
		fmt.Fprintf(c.out, "removed %d DNS rules left in the registry\n", len(result.Removed))
	}
	switch {
	case len(result.Remaining) > 0:
		return fmt.Errorf("%d DNS rules of Plaitway remain in the registry and keep sending DNS to their servers (%s); remove them with: %s%s",
			len(result.Remaining), strings.Join(result.Remaining, ", "), winnet.ManualSweepCommand(), detailOf(err))
	case err != nil:
		return fmt.Errorf("remove the DNS rules left in the registry: %w", err)
	}
	return nil
}

// detailOf appends the reason to a sentence, or nothing.
func detailOf(err error) string {
	if err == nil {
		return ""
	}
	return " (" + err.Error() + ")"
}

func (c *commands) removeService(existing registeredService) error {
	if err := c.stopAndWait(existing); err != nil {
		return fmt.Errorf("%w; nothing was removed", err)
	}
	if err := existing.Delete(); err != nil {
		return fmt.Errorf("delete %s: %w", c.name, c.describe(err))
	}
	existing.Close()
	if c.waitUntilGone() {
		fmt.Fprintf(c.out, "%s uninstalled\n", c.name)
	} else {
		fmt.Fprintf(c.out, "%s is marked for deletion and goes when the last program that has it open closes it, for example the Services window\n", c.name)
	}
	return nil
}

// waitUntilGone reports whether the service has left the Service Control
// Manager within the wait for a stop.
func (c *commands) waitUntilGone() bool {
	deadline := time.Now().Add(c.waits.stop)
	for {
		if _, err := c.query(c.name); errors.Is(err, errNotInstalled) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(c.waits.poll)
	}
}

func (c *commands) purgeData() error {
	root, err := c.dataRoot()
	if err != nil {
		return err
	}
	if err := c.purge(root); err != nil {
		return fmt.Errorf("delete %s: %w", root, err)
	}
	fmt.Fprintf(c.out, "deleted %s\n", root)
	return nil
}

// purgeDataRoot deletes the data directory. fsperm looks at every object below
// it first: a junction or an object that a standard user owns is refused, so
// that the deletion cannot be sent somewhere else by a link that a standard
// user planted in a directory they can write to.
func purgeDataRoot(root string) error {
	if _, err := fsperm.Restrict(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return os.RemoveAll(root)
}

func (c *commands) start() error {
	existing, err := c.openForControl()
	if err != nil {
		return err
	}
	defer existing.Close()
	if status, err := existing.Status(); err == nil && status.State == svc.Running {
		fmt.Fprintf(c.out, "%s: running\n", c.name)
		return nil
	}
	if err := c.startAndWait(existing); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "%s: running\n", c.name)
	return nil
}

func (c *commands) stop() error {
	existing, err := c.openForControl()
	if err != nil {
		return err
	}
	defer existing.Close()
	if err := c.stopAndWait(existing); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "%s: stopped\n", c.name)
	return nil
}

// openForControl opens the installed service for start and stop. The access
// list lets only administrators do either.
func (c *commands) openForControl() (registeredService, error) {
	scm, err := c.connect()
	if err != nil {
		return nil, c.describe(err)
	}
	defer scm.Close()
	existing, err := scm.Open(c.name)
	if errors.Is(err, errNotInstalled) {
		return nil, fmt.Errorf("%s %w", c.name, errNotInstalled)
	}
	if err != nil {
		return nil, c.describe(err)
	}
	return existing, nil
}

func (c *commands) status() error {
	status, err := c.query(c.name)
	if errors.Is(err, errNotInstalled) {
		fmt.Fprintf(c.out, "%s: not installed\n", c.name)
		return codedError{code: exitNotInstalled}
	}
	if err != nil {
		return c.describe(err)
	}
	fmt.Fprintf(c.out, "%s: %s\n", c.name, stateName(status.State))
	if status.State != svc.Running {
		return codedError{code: exitNotRunning}
	}
	return nil
}

func stateName(state svc.State) string {
	switch state {
	case svc.Running:
		return "running"
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "start pending"
	case svc.StopPending:
		return "stop pending"
	case svc.Paused, svc.PausePending, svc.ContinuePending:
		return "paused"
	}
	return fmt.Sprintf("state %d", state)
}

// startAndWait starts the service and waits until it runs and stays so.
func (c *commands) startAndWait(target registeredService) error {
	if err := target.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return c.startFailed(target, err)
	}
	if err := c.waitForState(target, svc.Running, c.waits.start); err != nil {
		return err
	}
	time.Sleep(c.waits.settle)
	status, err := target.Status()
	if err != nil {
		return fmt.Errorf("read the state of %s: %w", c.name, c.describe(err))
	}
	if status.State != svc.Running {
		return c.stoppedUnexpectedly(status)
	}
	return nil
}

// stopAndWait asks a service that runs to stop and waits for it. A service that
// is stopped already is not an error.
func (c *commands) stopAndWait(target registeredService) error {
	status, err := target.Status()
	if err != nil {
		return fmt.Errorf("read the state of %s: %w", c.name, c.describe(err))
	}
	if status.State == svc.Stopped {
		return nil
	}
	if status.State != svc.StopPending {
		if err := target.Stop(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			return fmt.Errorf("stop %s: %w", c.name, c.describe(err))
		}
	}
	return c.waitForState(target, svc.Stopped, c.waits.stop)
}

// waitForState polls until target is in want. A service that stops while it is
// waited for to start has failed, and says why.
func (c *commands) waitForState(target registeredService, want svc.State, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		status, err := target.Status()
		if err != nil {
			return fmt.Errorf("read the state of %s: %w", c.name, c.describe(err))
		}
		switch {
		case status.State == want:
			return nil
		case status.State == svc.Stopped:
			return c.stoppedUnexpectedly(status)
		case time.Now().After(deadline):
			return fmt.Errorf("%s is %s after %s, not %s", c.name, stateName(status.State), timeout, stateName(want))
		}
		time.Sleep(c.waits.poll)
	}
}

// startFailed explains a start that the Service Control Manager refused, with
// the exit code of the daemon when it started and ended.
func (c *commands) startFailed(target registeredService, cause error) error {
	failure := fmt.Errorf("start %s: %w", c.name, c.describe(cause))
	if status, err := target.Status(); err == nil && status.State == svc.Stopped {
		return fmt.Errorf("%w (%s)%s", failure, describeExit(status), c.logHint())
	}
	return failure
}

func (c *commands) stoppedUnexpectedly(status svc.Status) error {
	return fmt.Errorf("%s stopped (%s)%s", c.name, describeExit(status), c.logHint())
}

func (c *commands) logHint() string {
	if c.logFile == "" {
		return ""
	}
	return "; see " + c.logFile
}

// describeExit says how a service ended. A failure before the log is open is
// the one case that the log does not explain.
func describeExit(status svc.Status) string {
	switch {
	case status.Win32ExitCode == uint32(windows.ERROR_SERVICE_SPECIFIC_ERROR):
		return fmt.Sprintf("exit code %d", status.ServiceSpecificExitCode)
	case status.Win32ExitCode != 0:
		return fmt.Sprintf("error %d: %v", status.Win32ExitCode, windows.Errno(status.Win32ExitCode))
	}
	return "exit code 0"
}

// describe turns the errors a caller meets most into the sentence that says
// what to do.
func (c *commands) describe(err error) error {
	switch {
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return errNeedsElevation
	case errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE):
		return fmt.Errorf("%s is marked for deletion: close the Services window, or restart, and try again", c.name)
	}
	return err
}

// productionLogFile is where the daemon logs, or empty when the shell cannot be
// asked for the data directory.
func productionLogFile() string {
	locations, err := productionLocations()
	if err != nil {
		return ""
	}
	return locations.logFile
}
