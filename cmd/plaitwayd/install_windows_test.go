package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/KoukeNeko/Plaitway/internal/fsperm"
	"github.com/KoukeNeko/Plaitway/internal/fsperm/fspermtest"
	winnet "github.com/KoukeNeko/Plaitway/internal/osnet/windows"
)

const testExecutable = `C:\Program Files\Plaitway\plaitwayd.exe`

// ---- argument parsing and dispatch ----

func TestParseInstallArgs(t *testing.T) {
	tests := []struct {
		args    []string
		want    installOptions
		wantErr bool
	}{
		{args: nil, want: installOptions{}},
		{args: []string{"-start"}, want: installOptions{start: true}},
		{args: []string{"-update"}, want: installOptions{update: true}},
		{args: []string{"-start", "-update"}, want: installOptions{start: true, update: true}},
		{args: []string{"-update=false", "-start=true"}, want: installOptions{start: true}},
		{args: []string{"-purge"}, wantErr: true},
		{args: []string{"-nope"}, wantErr: true},
		{args: []string{"now"}, wantErr: true},
		{args: []string{"-start", "now"}, wantErr: true},
	}
	for _, test := range tests {
		got, err := parseInstallArgs(test.args)
		if (err != nil) != test.wantErr || (err == nil && got != test.want) {
			t.Errorf("parseInstallArgs(%q) = %+v, %v; want %+v, error %v", test.args, got, err, test.want, test.wantErr)
		}
	}
}

func TestParseUninstallArgs(t *testing.T) {
	tests := []struct {
		args    []string
		want    uninstallOptions
		wantErr bool
	}{
		{args: nil, want: uninstallOptions{}},
		{args: []string{"-purge"}, want: uninstallOptions{purge: true}},
		{args: []string{"-start"}, wantErr: true},
		{args: []string{"-purge", "everything"}, wantErr: true},
	}
	for _, test := range tests {
		got, err := parseUninstallArgs(test.args)
		if (err != nil) != test.wantErr || (err == nil && got != test.want) {
			t.Errorf("parseUninstallArgs(%q) = %+v, %v; want %+v, error %v", test.args, got, err, test.want, test.wantErr)
		}
	}
}

// The daemon's own flags are never taken for a subcommand: the console daemon
// is started with them exactly as before.
func TestRunSubcommandLeavesTheDaemonFlagsAlone(t *testing.T) {
	for _, args := range [][]string{nil, {}, {"-fake"}, {"-socket", `\\.\pipe\x`}, {"extra"}, {"Install"}, {"-install"}} {
		if code, handled := runSubcommand(args); handled {
			t.Errorf("runSubcommand(%q) handled it with code %d, want the daemon to take it", args, code)
		}
	}
}

func TestUsageMistakesExitWithTwoAndOneLine(t *testing.T) {
	for _, args := range [][]string{
		{commandInstall, "-nope"}, {commandInstall, "now"}, {commandUninstall, "-start"},
		{commandStart, "now"}, {commandStop, "-x"}, {commandStatus, "all"},
	} {
		env := newTestEnv(t)
		code := env.commands.run(args)
		message := env.stderr.String()
		if code != exitUsage || strings.Count(strings.TrimSpace(message), "\n") != 0 || !strings.HasPrefix(message, "plaitwayd "+args[0]+": ") {
			t.Errorf("%q: exit %d, stderr %q; want %d and one line that names the subcommand", args, code, message, exitUsage)
		}
		if len(env.scm.calls) != 0 {
			t.Errorf("%q reached the Service Control Manager: %v", args, env.scm.calls)
		}
	}
}

// ---- the configuration builder ----

func TestDesiredSettingsRegisterTheDaemonAsTheSpecificationSays(t *testing.T) {
	got := desiredSettings(testExecutable, nil)

	if got.config.ServiceStartName != localSystem || got.config.ServiceType != windows.SERVICE_WIN32_OWN_PROCESS {
		t.Errorf("account %q, type %d; want LocalSystem and an own process", got.config.ServiceStartName, got.config.ServiceType)
	}
	if got.config.StartType != mgr.StartAutomatic || got.config.DelayedAutoStart {
		t.Errorf("start type %d, delayed %v; want automatic and not delayed", got.config.StartType, got.config.DelayedAutoStart)
	}
	if got.config.SidType != windows.SERVICE_SID_TYPE_UNRESTRICTED {
		t.Errorf("service SID type %d, want unrestricted", got.config.SidType)
	}
	if got.config.DisplayName != "Plaitway Helper" || serviceName != "PlaitwayHelper" || got.config.Description == "" {
		t.Errorf("name %q, display name %q, description %q", serviceName, got.config.DisplayName, got.config.Description)
	}
	if want := []string{"Nsi", "Tcpip"}; !slices.Equal(got.config.Dependencies, want) {
		t.Errorf("dependencies %q, want %q", got.config.Dependencies, want)
	}
	if got.config.BinaryPathName != `"`+testExecutable+`"` {
		t.Errorf("command line %s, want the quoted executable and no flags", got.config.BinaryPathName)
	}
}

func TestDesiredSettingsRestartAfterFiveTenAndThirtySecondsOnEveryFailure(t *testing.T) {
	got := desiredSettings(testExecutable, nil)

	want := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}
	if !reflect.DeepEqual(got.recovery, want) {
		t.Errorf("failure actions %+v, want %+v", got.recovery, want)
	}
	if got.resetPeriod != 24*time.Hour {
		t.Errorf("failure count reset after %s, want a day", got.resetPeriod)
	}
	if !got.restartOnNonCrash {
		t.Error("the failure actions do not apply to a daemon that exits with an error")
	}
}

func TestBinaryPathQuotesTheExecutableAndEscapesTheArguments(t *testing.T) {
	got := binaryPath(testExecutable, []string{"-fake", "-state-dir", `C:\Program Files\scratch dir`})

	want := `"C:\Program Files\Plaitway\plaitwayd.exe" -fake -state-dir "C:\Program Files\scratch dir"`
	if got != want {
		t.Errorf("binaryPath = %s, want %s", got, want)
	}
	if plain := binaryPath(`C:\plaitwayd.exe`, nil); plain != `"C:\plaitwayd.exe"` {
		t.Errorf("a path without a space is quoted as well: %s", plain)
	}
}

// The time the service manager waits at shutdown has to be longer than the
// daemon needs to remove routes and DNS entries, and the service's own stop
// timeout sits between the two. packaging/lib_test.sh checks the same for
// launchd.
func TestShutdownBudgetsAreNestedInOrder(t *testing.T) {
	daemonNeeds := serverStopTimeout + shutdownTimeout
	if !(daemonNeeds < productionTiming.stopTimeout && productionTiming.stopTimeout < preshutdownTimeout) {
		t.Fatalf("daemon %s, service stop timeout %s, preshutdown timeout %s: want them in increasing order",
			daemonNeeds, productionTiming.stopTimeout, preshutdownTimeout)
	}
	if productionWaits.stop <= productionTiming.stopTimeout {
		t.Errorf("the stop command gives up after %s, before the service's %s", productionWaits.stop, productionTiming.stopTimeout)
	}
}

// ---- the access list of the service ----

const (
	serviceQueryStatus = 0x4
	serviceInterrogate = 0x80
	readControl        = 0x20000
)

type accessEntry struct {
	sid  string
	mask uint32
}

func parseServiceAccessList(t *testing.T) []accessEntry {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(serviceAccessList)
	if err != nil {
		t.Fatalf("the access list is not valid SDDL: %v", err)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		t.Fatalf("DACL: %v, %v", acl, err)
	}
	var entries []accessEntry
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Fatalf("entry %d is not an allow entry", i)
		}
		entries = append(entries, accessEntry{sid: (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String(), mask: uint32(ace.Mask)})
	}
	return entries
}

func TestServiceAccessListGivesInteractiveUsersTheStateAndNothingElse(t *testing.T) {
	entries := parseServiceAccessList(t)
	bySID := map[string]uint32{}
	for _, entry := range entries {
		bySID[entry.sid] = entry.mask
	}

	if len(entries) != 3 || len(bySID) != 3 {
		t.Fatalf("access list %+v, want SYSTEM, Administrators and interactive users once each", entries)
	}
	interactive := bySID["S-1-5-4"]
	if interactive != serviceQueryStatus|serviceInterrogate|readControl {
		t.Errorf("interactive users hold %#x, want exactly query status, interrogate and read control (%#x)",
			interactive, serviceQueryStatus|serviceInterrogate|readControl)
	}
	system, administrators := bySID["S-1-5-18"], bySID["S-1-5-32-544"]
	for _, right := range []uint32{windows.SERVICE_CHANGE_CONFIG, windows.DELETE, windows.WRITE_DAC, windows.WRITE_OWNER} {
		if system&right != 0 {
			t.Errorf("SYSTEM holds right %#x, which only Administrators may", right)
		}
	}
	for _, right := range []uint32{windows.SERVICE_START, windows.SERVICE_STOP, windows.SERVICE_CHANGE_CONFIG, windows.DELETE, windows.WRITE_DAC} {
		if administrators&right != right {
			t.Errorf("Administrators lack right %#x, which control needs", right)
		}
		if interactive&right != 0 {
			t.Errorf("interactive users hold right %#x, which only SYSTEM and Administrators may", right)
		}
	}
}

// accessCheck asks Windows whether a token that is the caller's, made an
// impersonation token, would be granted rights by the list, the way the
// Service Control Manager asks it.
func accessCheck(t *testing.T, sddl string, rights uint32) (granted bool) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString("O:SYG:SY" + sddl)
	if err != nil {
		t.Fatal(err)
	}
	var primary windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &primary); err != nil {
		t.Fatal(err)
	}
	defer primary.Close()
	var impersonation windows.Token
	if err := windows.DuplicateTokenEx(primary, windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE|windows.TOKEN_IMPERSONATE,
		nil, windows.SecurityImpersonation, windows.TokenImpersonation, &impersonation); err != nil {
		t.Fatal(err)
	}
	defer impersonation.Close()

	var mapping [4]uint32
	var privileges [64]byte
	privilegesLength := uint32(len(privileges))
	var grantedAccess, status uint32
	proc := windows.NewLazySystemDLL("advapi32.dll").NewProc("AccessCheck")
	result, _, callErr := proc.Call(uintptr(unsafe.Pointer(sd)), uintptr(impersonation), uintptr(rights), uintptr(unsafe.Pointer(&mapping)),
		uintptr(unsafe.Pointer(&privileges[0])), uintptr(unsafe.Pointer(&privilegesLength)), uintptr(unsafe.Pointer(&grantedAccess)), uintptr(unsafe.Pointer(&status)))
	if result == 0 {
		t.Fatalf("AccessCheck: %v", callErr)
	}
	return status != 0 && grantedAccess&rights == rights
}

// Windows itself decides, for the token of whoever runs the test: an
// interactive caller may read the state, and may control the service only as
// an elevated administrator.
func TestServiceAccessListAsWindowsEvaluatesItForTheCaller(t *testing.T) {
	interactive, err := windows.StringToSid("S-1-5-4")
	if err != nil {
		t.Fatal(err)
	}
	if isMember, err := windows.Token(0).IsMember(interactive); err != nil || !isMember {
		t.Skipf("this session is not an interactive logon (member %v, %v)", isMember, err)
	}

	for name, right := range map[string]uint32{"query status": serviceQueryStatus, "interrogate": serviceInterrogate} {
		if !accessCheck(t, serviceAccessList, right) {
			t.Errorf("an interactive caller may not %s", name)
		}
	}
	if isPrivileged() {
		t.Log("elevated: the caller is an administrator, so the control rights are not checked for denial")
		return
	}
	for name, right := range map[string]uint32{"start": windows.SERVICE_START, "stop": windows.SERVICE_STOP,
		"change the configuration": windows.SERVICE_CHANGE_CONFIG, "delete": windows.DELETE, "change the access list": windows.WRITE_DAC} {
		if accessCheck(t, serviceAccessList, right) {
			t.Errorf("an unelevated caller may %s", name)
		}
	}
}

// ---- the commands, against a fake Service Control Manager ----

type testEnv struct {
	commands       *commands
	scm            *fakeSCM
	stdout, stderr *bytes.Buffer
	purged         []string
	// sweepResult and sweepErr are what the fake sweep of the DNS rules returns.
	sweepResult winnet.SweepResult
	sweepErr    error
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	env := &testEnv{scm: &fakeSCM{}, stdout: new(bytes.Buffer), stderr: new(bytes.Buffer)}
	env.commands = &commands{
		out:              env.stdout,
		errOut:           env.stderr,
		name:             serviceName,
		isElevated:       func() bool { return true },
		executable:       func() (string, error) { return testExecutable, nil },
		checkInstallPath: func(string) error { return nil },
		connect:          env.scm.connect,
		query:            env.scm.query,
		dataRoot:         func() (string, error) { return `C:\ProgramData\Plaitway`, nil },
		logFile:          `C:\ProgramData\Plaitway\Logs\plaitwayd.log`,
		purge: func(root string) error {
			env.purged = append(env.purged, root)
			env.scm.record("purge")
			return nil
		},
		sweepDNS: func() (winnet.SweepResult, error) {
			env.scm.record("sweep")
			return env.sweepResult, env.sweepErr
		},
		waits: waitTimings{poll: time.Millisecond, start: 300 * time.Millisecond, stop: 300 * time.Millisecond},
	}
	return env
}

func (e *testEnv) run(args ...string) int { return e.commands.run(args) }

// installed puts a service in the fake, in the state and with the settings given.
func (e *testEnv) installed(state svc.State, binary string) *fakeService {
	settings := desiredSettings(binary, nil)
	e.scm.service = &fakeService{scm: e.scm, settings: settings, state: state}
	return e.scm.service
}

// calls without the bookkeeping of connecting and opening.
func (e *testEnv) operations() []string {
	var operations []string
	for _, call := range e.scm.calls {
		if call != "connect" && call != "open" && !strings.HasPrefix(call, "query") {
			operations = append(operations, call)
		}
	}
	return operations
}

type fakeSCM struct {
	calls      []string
	service    *fakeService
	connectErr error
	createErr  error
}

func (s *fakeSCM) record(call string) { s.calls = append(s.calls, call) }

func (s *fakeSCM) connect() (serviceControl, error) {
	s.record("connect")
	return s, s.connectErr
}

func (s *fakeSCM) query(string) (svc.Status, error) {
	s.record("query")
	if s.service == nil {
		return svc.Status{}, errNotInstalled
	}
	return s.service.Status()
}

func (s *fakeSCM) Open(string) (registeredService, error) {
	s.record("open")
	if s.service == nil {
		return nil, errNotInstalled
	}
	return s.service, nil
}

func (s *fakeSCM) Create(_, executable string) (registeredService, error) {
	s.record("create")
	if s.createErr != nil {
		return nil, s.createErr
	}
	s.service = &fakeService{scm: s, settings: serviceSettings{config: mgr.Config{BinaryPathName: executable}}, state: svc.Stopped}
	return s.service, nil
}

func (s *fakeSCM) Close() error { return nil }

type fakeService struct {
	scm      *fakeSCM
	settings serviceSettings
	state    svc.State
	exit     svc.Status
	// polls until a stop takes effect, to see the commands wait for it.
	stopTakes int
	stopping  int
	// failsToStart decides, from the settings in force, whether a start ends in
	// an exit code.
	failsToStart func(serviceSettings) bool
	// failsToApply makes Apply fail for the settings it returns true for.
	failsToApply func(serviceSettings) bool
	// refusesToStop leaves the service running whatever it is told.
	refusesToStop bool
	applied       []string
}

func (f *fakeService) Status() (svc.Status, error) {
	if f.state == svc.StopPending {
		f.stopping++
		if f.stopping > f.stopTakes {
			f.state = svc.Stopped
		}
	}
	status := f.exit
	status.State = f.state
	return status, nil
}

func (f *fakeService) Settings() (serviceSettings, error) { return f.settings, nil }

func (f *fakeService) Apply(settings serviceSettings) error {
	f.scm.record("apply " + settings.config.BinaryPathName)
	if f.state != svc.Stopped {
		f.scm.record("apply while " + fmt.Sprint(f.state))
	}
	if f.failsToApply != nil && f.failsToApply(settings) {
		return errors.New("the Service Control Manager refused the settings")
	}
	f.settings = settings
	return nil
}

func (f *fakeService) Start() error {
	f.scm.record("start")
	if f.failsToStart != nil && f.failsToStart(f.settings) {
		f.state = svc.Stopped
		f.exit = svc.Status{Win32ExitCode: uint32(windows.ERROR_SERVICE_SPECIFIC_ERROR), ServiceSpecificExitCode: 7}
		return nil
	}
	f.state, f.exit = svc.Running, svc.Status{}
	return nil
}

func (f *fakeService) Stop() error {
	f.scm.record("stop")
	if f.refusesToStop {
		return nil
	}
	f.state, f.stopping = svc.StopPending, 0
	return nil
}

func (f *fakeService) Delete() error {
	f.scm.record("delete")
	f.scm.service = nil
	return nil
}

func (f *fakeService) Close() error { return nil }

func brokenBinary(settings serviceSettings) bool {
	return strings.Contains(settings.config.BinaryPathName, "broken")
}

// ---- install ----

func TestInstallRefusesWithoutElevationBeforeTouchingAnything(t *testing.T) {
	env := newTestEnv(t)
	env.commands.isElevated = func() bool { return false }

	code := env.run("install")

	if code != exitFailed || !strings.Contains(env.stderr.String(), "run from an elevated shell") {
		t.Fatalf("exit %d, stderr %q; want a failure that says to run from an elevated shell", code, env.stderr.String())
	}
	if len(env.scm.calls) != 0 {
		t.Errorf("the Service Control Manager was reached without elevation: %v", env.scm.calls)
	}
}

func TestInstallRefusesAPathAStandardUserCanWrite(t *testing.T) {
	env := newTestEnv(t)
	env.commands.checkInstallPath = func(path string) error {
		return fmt.Errorf("%s grants it to BUILTIN\\Users", path)
	}

	code := env.run("install", "-start")

	message := env.stderr.String()
	if code != exitFailed || !strings.Contains(message, testExecutable) || !strings.Contains(message, "Program Files") {
		t.Fatalf("exit %d, stderr %q; want a refusal that names the file and the kind of location to use", code, message)
	}
	if len(env.scm.calls) != 0 {
		t.Errorf("a refused path reached the Service Control Manager: %v", env.scm.calls)
	}
}

func TestInstallRegistersTheServiceWithTheDesiredSettings(t *testing.T) {
	env := newTestEnv(t)

	if code := env.run("install"); code != 0 {
		t.Fatalf("exit %d: %s", code, env.stderr.String())
	}

	if want := []string{"create", "apply " + binaryPath(testExecutable, nil)}; !slices.Equal(env.operations(), want) {
		t.Errorf("operations %q, want %q", env.operations(), want)
	}
	if !reflect.DeepEqual(env.scm.service.settings, desiredSettings(testExecutable, nil)) {
		t.Errorf("settings %+v, want the desired ones", env.scm.service.settings)
	}
	if env.scm.service.state != svc.Stopped || env.stdout.String() != "PlaitwayHelper installed\n" {
		t.Errorf("state %v, output %q; want a stopped service and one line", env.scm.service.state, env.stdout.String())
	}
}

func TestInstallWithStartRunsTheServiceAndStaysSilentUntilItDoes(t *testing.T) {
	env := newTestEnv(t)

	if code := env.run("install", "-start"); code != 0 {
		t.Fatalf("exit %d: %s", code, env.stderr.String())
	}

	if env.scm.service.state != svc.Running || env.stdout.String() != "PlaitwayHelper installed, running\n" {
		t.Errorf("state %v, output %q", env.scm.service.state, env.stdout.String())
	}
}

func TestInstallRefusesAnInstalledServiceWithoutUpdate(t *testing.T) {
	env := newTestEnv(t)
	env.installed(svc.Running, `C:\old\plaitwayd.exe`)

	code := env.run("install")

	if code != exitFailed || !strings.Contains(env.stderr.String(), "-update") {
		t.Fatalf("exit %d, stderr %q; want a refusal that names -update", code, env.stderr.String())
	}
	if len(env.operations()) != 0 {
		t.Errorf("an installed service was touched: %v", env.operations())
	}
}

func TestInstallUpdateRefusesAServiceThatIsNotInstalled(t *testing.T) {
	env := newTestEnv(t)

	code := env.run("install", "-update")

	if code != exitFailed || !strings.Contains(env.stderr.String(), "is not installed") {
		t.Fatalf("exit %d, stderr %q", code, env.stderr.String())
	}
	if len(env.operations()) != 0 {
		t.Errorf("operations %v, want none", env.operations())
	}
}

// Idempotence: a failed install leaves nothing behind, so the same command can
// be run again.
func TestInstallDeletesTheServiceItCouldNotConfigure(t *testing.T) {
	env := newTestEnv(t)
	env.commands.connect = func() (serviceControl, error) {
		env.scm.record("connect")
		return failingApplySCM{env.scm}, nil
	}

	code := env.run("install")

	if code != exitFailed || !strings.Contains(env.stderr.String(), "configure PlaitwayHelper") {
		t.Fatalf("exit %d, stderr %q", code, env.stderr.String())
	}
	if env.scm.service != nil || !slices.Contains(env.operations(), "delete") {
		t.Errorf("the half-installed service is still there: %v, operations %v", env.scm.service, env.operations())
	}
	if lines := strings.Count(strings.TrimSpace(env.stderr.String()), "\n"); lines != 0 {
		t.Errorf("the error takes %d lines, want one", lines+1)
	}
}

// failingApplySCM creates services whose configuration cannot be set.
type failingApplySCM struct{ *fakeSCM }

func (s failingApplySCM) Create(name, executable string) (registeredService, error) {
	created, err := s.fakeSCM.Create(name, executable)
	if err == nil {
		s.fakeSCM.service.failsToApply = func(serviceSettings) bool { return true }
	}
	return created, err
}

func TestInstallKeepsTheServiceWhenOnlyTheStartFails(t *testing.T) {
	env := newTestEnv(t)
	env.commands.executable = func() (string, error) { return `C:\Program Files\broken\plaitwayd.exe`, nil }
	env.commands.connect = func() (serviceControl, error) {
		env.scm.record("connect")
		return brokenStartSCM{env.scm}, nil
	}

	code := env.run("install", "-start")

	message := env.stderr.String()
	if code != exitFailed || !strings.Contains(message, "exit code 7") || !strings.Contains(message, env.commands.logFile) {
		t.Fatalf("exit %d, stderr %q; want the exit code of the daemon and the log to read", code, message)
	}
	if env.scm.service == nil || slices.Contains(env.operations(), "delete") {
		t.Errorf("the service was removed because it did not start: %v", env.operations())
	}
}

type brokenStartSCM struct{ *fakeSCM }

func (s brokenStartSCM) Create(name, executable string) (registeredService, error) {
	created, err := s.fakeSCM.Create(name, executable)
	if err == nil {
		s.fakeSCM.service.failsToStart = brokenBinary
	}
	return created, err
}

func TestAccessDeniedFromTheServiceControlManagerSaysToElevate(t *testing.T) {
	env := newTestEnv(t)
	env.scm.connectErr = fmt.Errorf("connect to the Service Control Manager: %w", windows.ERROR_ACCESS_DENIED)

	for _, command := range []string{"install", "uninstall", "start", "stop"} {
		env.stderr.Reset()
		if code := env.run(command); code != exitFailed || !strings.Contains(env.stderr.String(), "run from an elevated shell") {
			t.Errorf("%s: exit %d, stderr %q", command, code, env.stderr.String())
		}
	}
}

// ---- install -update ----

func TestUpdateStopsReplacesAndStartsInThatOrder(t *testing.T) {
	env := newTestEnv(t)
	service := env.installed(svc.Running, `C:\old\plaitwayd.exe`)
	service.stopTakes = 3

	if code := env.run("install", "-update"); code != 0 {
		t.Fatalf("exit %d: %s", code, env.stderr.String())
	}

	want := []string{"stop", "apply " + binaryPath(testExecutable, nil), "start"}
	if !slices.Equal(env.operations(), want) {
		t.Errorf("operations %q, want %q (no apply while the service ran: %v)", env.operations(), want, env.scm.calls)
	}
	if service.state != svc.Running || env.stdout.String() != "PlaitwayHelper updated, running\n" {
		t.Errorf("state %v, output %q", service.state, env.stdout.String())
	}
}

func TestUpdateOfAStoppedServiceDoesNotStartIt(t *testing.T) {
	env := newTestEnv(t)
	service := env.installed(svc.Stopped, `C:\old\plaitwayd.exe`)

	if code := env.run("install", "-update"); code != 0 {
		t.Fatalf("exit %d: %s", code, env.stderr.String())
	}

	if want := []string{"apply " + binaryPath(testExecutable, nil)}; !slices.Equal(env.operations(), want) {
		t.Errorf("operations %q, want %q", env.operations(), want)
	}
	if service.state != svc.Stopped || env.stdout.String() != "PlaitwayHelper updated\n" {
		t.Errorf("state %v, output %q", service.state, env.stdout.String())
	}
}

func TestUpdateWithStartStartsAStoppedService(t *testing.T) {
	env := newTestEnv(t)
	service := env.installed(svc.Stopped, `C:\old\plaitwayd.exe`)

	if code := env.run("install", "-update", "-start"); code != 0 {
		t.Fatalf("exit %d: %s", code, env.stderr.String())
	}

	if service.state != svc.Running {
		t.Errorf("state %v, want running", service.state)
	}
}

func TestUpdateRestoresThePreviousRegistrationWhenTheNewServiceDoesNotStart(t *testing.T) {
	env := newTestEnv(t)
	env.commands.executable = func() (string, error) { return `C:\Program Files\broken\plaitwayd.exe`, nil }
	service := env.installed(svc.Running, `C:\old\plaitwayd.exe`)
	service.failsToStart = brokenBinary
	previous := service.settings

	code := env.run("install", "-update")

	want := []string{
		"stop",
		"apply " + binaryPath(`C:\Program Files\broken\plaitwayd.exe`, nil),
		"start",
		"apply " + binaryPath(`C:\old\plaitwayd.exe`, nil),
		"start",
	}
	if !slices.Equal(env.operations(), want) {
		t.Errorf("operations %q, want %q", env.operations(), want)
	}
	message := env.stderr.String()
	if code != exitFailed || !strings.Contains(message, "exit code 7") || !strings.Contains(message, "previous registration was restored") {
		t.Errorf("exit %d, stderr %q", code, message)
	}
	if !reflect.DeepEqual(service.settings, previous) || service.state != svc.Running {
		t.Errorf("settings %+v, state %v; want the previous settings and a running service", service.settings, service.state)
	}
	if strings.Count(strings.TrimSpace(message), "\n") != 0 {
		t.Errorf("the error takes several lines: %q", message)
	}
}

func TestUpdateRestoresThePreviousRegistrationWhenTheNewOneCannotBeApplied(t *testing.T) {
	env := newTestEnv(t)
	service := env.installed(svc.Running, `C:\old\plaitwayd.exe`)
	service.failsToApply = func(settings serviceSettings) bool {
		return strings.Contains(settings.config.BinaryPathName, "Program Files")
	}
	previous := service.settings

	code := env.run("install", "-update")

	if code != exitFailed || !strings.Contains(env.stderr.String(), "previous registration was restored") {
		t.Errorf("exit %d, stderr %q", code, env.stderr.String())
	}
	if !reflect.DeepEqual(service.settings, previous) || service.state != svc.Running {
		t.Errorf("settings %+v, state %v; want the previous ones and a running service", service.settings, service.state)
	}
}

func TestUpdateOfAStoppedServiceStaysStoppedWhenItRollsBack(t *testing.T) {
	env := newTestEnv(t)
	env.commands.executable = func() (string, error) { return `C:\Program Files\broken\plaitwayd.exe`, nil }
	service := env.installed(svc.Stopped, `C:\old\plaitwayd.exe`)
	service.failsToStart = brokenBinary

	code := env.run("install", "-update", "-start")

	if code != exitFailed || service.state != svc.Stopped {
		t.Errorf("exit %d, state %v; want a failure and a stopped service as it was found", code, service.state)
	}
	if last := env.operations()[len(env.operations())-1]; last != "apply "+binaryPath(`C:\old\plaitwayd.exe`, nil) {
		t.Errorf("the last operation was %q, want the restoring apply", last)
	}
}

// ---- uninstall ----

func TestUninstallStopsDeletesAndKeepsTheData(t *testing.T) {
	env := newTestEnv(t)
	env.installed(svc.Running, testExecutable).stopTakes = 2

	if code := env.run("uninstall"); code != 0 {
		t.Fatalf("exit %d: %s", code, env.stderr.String())
	}

	if want := []string{"stop", "delete", "sweep"}; !slices.Equal(env.operations(), want) {
		t.Errorf("operations %q, want %q", env.operations(), want)
	}
	if env.scm.service != nil || len(env.purged) != 0 || env.stdout.String() != "PlaitwayHelper uninstalled\n" {
		t.Errorf("service %v, purged %v, output %q", env.scm.service, env.purged, env.stdout.String())
	}
}

func TestUninstallPurgeDeletesTheDataRootAfterTheService(t *testing.T) {
	env := newTestEnv(t)
	env.installed(svc.Stopped, testExecutable)

	if code := env.run("uninstall", "-purge"); code != 0 {
		t.Fatalf("exit %d: %s", code, env.stderr.String())
	}

	if want := []string{"delete", "sweep", "purge"}; !slices.Equal(env.operations(), want) {
		t.Errorf("operations %q, want %q", env.operations(), want)
	}
	if want := []string{`C:\ProgramData\Plaitway`}; !slices.Equal(env.purged, want) {
		t.Errorf("purged %q, want %q", env.purged, want)
	}
}

func TestUninstallOfAServiceThatIsNotInstalledSucceeds(t *testing.T) {
	env := newTestEnv(t)

	if code := env.run("uninstall"); code != 0 || !strings.Contains(env.stdout.String(), "is not installed") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, env.stdout.String(), env.stderr.String())
	}
	// Nothing to stop or delete, but the rules of an earlier run may be there.
	if want := []string{"sweep"}; !slices.Equal(env.operations(), want) {
		t.Errorf("operations %v, want %v", env.operations(), want)
	}

	if code := env.run("uninstall", "-purge"); code != 0 || !slices.Equal(env.purged, []string{`C:\ProgramData\Plaitway`}) {
		t.Errorf("-purge of a missing service: exit %d, purged %q; want the data to go anyway", code, env.purged)
	}
}

func TestUninstallRemovesNothingWhenTheServiceDoesNotStop(t *testing.T) {
	env := newTestEnv(t)
	env.installed(svc.Running, testExecutable).refusesToStop = true

	code := env.run("uninstall", "-purge")

	if code != exitFailed || !strings.Contains(env.stderr.String(), "nothing was removed") {
		t.Fatalf("exit %d, stderr %q", code, env.stderr.String())
	}
	if env.scm.service == nil || len(env.purged) != 0 || slices.Contains(env.operations(), "delete") || slices.Contains(env.operations(), "sweep") {
		t.Errorf("something was removed: service %v, purged %v, operations %v", env.scm.service, env.purged, env.operations())
	}
}

// The rules are the one thing a dead daemon leaves that outlives a reboot, so
// uninstall takes them out after the service is gone, whatever state the
// service was in.
func TestUninstallRemovesTheDNSRulesTheDaemonLeft(t *testing.T) {
	env := newTestEnv(t)
	env.installed(svc.Stopped, testExecutable)
	env.sweepResult = winnet.SweepResult{Removed: []string{"Plaitway-0123456789abcdef-0-0", "Plaitway-0123456789abcdef-0-1"}}

	if code := env.run("uninstall"); code != 0 {
		t.Fatalf("exit %d: %s", code, env.stderr.String())
	}

	if want := []string{"delete", "sweep"}; !slices.Equal(env.operations(), want) {
		t.Errorf("operations %q, want the rules swept after the delete", env.operations())
	}
	if out := env.stdout.String(); !strings.Contains(out, "removed 2 DNS rules") {
		t.Errorf("output %q does not say what was removed", out)
	}
}

func TestUninstallSaysNothingAboutRulesWhenThereWereNone(t *testing.T) {
	env := newTestEnv(t)
	env.installed(svc.Stopped, testExecutable)

	if code := env.run("uninstall"); code != 0 || strings.Contains(env.stdout.String(), "DNS") {
		t.Errorf("exit %d, stdout %q", code, env.stdout.String())
	}
}

// A rule that stays keeps the DNS of the PC on a dead server across reboots: it
// is an error, with the command that takes it out by hand.
func TestUninstallWarnsWhenRulesRemain(t *testing.T) {
	env := newTestEnv(t)
	env.installed(svc.Stopped, testExecutable)
	stuck := "Plaitway-0123456789abcdef-0-0"
	env.sweepResult = winnet.SweepResult{Remaining: []string{stuck}}
	env.sweepErr = errors.New("remove the DNS rule " + stuck + ": access denied")

	code := env.run("uninstall")

	if code != exitFailed {
		t.Errorf("exit %d, want %d: the PC is not clean", code, exitFailed)
	}
	for _, want := range []string{"1 DNS rules of Plaitway remain", stuck, "access denied", winnet.ManualSweepCommand()} {
		if !strings.Contains(env.stderr.String(), want) {
			t.Errorf("stderr %q lacks %q", env.stderr.String(), want)
		}
	}
	if env.scm.service != nil {
		t.Error("the service must be gone although the rules are not")
	}
}

func TestUninstallReportsASweepThatFailedWithoutKnowingWhatRemains(t *testing.T) {
	env := newTestEnv(t)
	env.sweepErr = errors.New("list the DNS rules: registry unavailable")

	code := env.run("uninstall")

	if code != exitFailed || !strings.Contains(env.stderr.String(), "registry unavailable") {
		t.Errorf("exit %d, stderr %q", code, env.stderr.String())
	}
}

// The data is purged although a rule stays: the rules are found by their marker
// and not by the journal, and a failed sweep must not leave the data behind
// after the owner asked for -purge.
func TestUninstallPurgesTheDataEvenWhenRulesRemain(t *testing.T) {
	env := newTestEnv(t)
	env.installed(svc.Stopped, testExecutable)
	env.sweepResult = winnet.SweepResult{Remaining: []string{"Plaitway-0123456789abcdef-0-0"}}
	env.sweepErr = errors.New("access denied")
	env.commands.purge = func(string) error { return errors.New("a junction is in the way") }

	code := env.run("uninstall", "-purge")

	if code != exitFailed {
		t.Errorf("exit %d", code)
	}
	for _, want := range []string{"remain", "a junction is in the way"} {
		if !strings.Contains(env.stderr.String(), want) {
			t.Errorf("stderr %q lacks %q: both failures must be reported", env.stderr.String(), want)
		}
	}
}

func TestUninstallRefusesWithoutElevation(t *testing.T) {
	env := newTestEnv(t)
	env.commands.isElevated = func() bool { return false }

	if code := env.run("uninstall", "-purge"); code != exitFailed || len(env.scm.calls) != 0 || len(env.purged) != 0 {
		t.Errorf("exit %d, calls %v, purged %v; want a refusal that touches nothing", code, env.scm.calls, env.purged)
	}
}

func TestUninstallReportsAFailedPurge(t *testing.T) {
	env := newTestEnv(t)
	env.commands.purge = func(string) error { return errors.New("a junction is in the way") }

	code := env.run("uninstall", "-purge")

	if code != exitFailed || !strings.Contains(env.stderr.String(), "a junction is in the way") {
		t.Errorf("exit %d, stderr %q", code, env.stderr.String())
	}
}

// ---- start, stop, status ----

func TestStartOfAServiceThatIsNotInstalled(t *testing.T) {
	env := newTestEnv(t)

	if code := env.run("start"); code != exitFailed || !strings.Contains(env.stderr.String(), "PlaitwayHelper is not installed") {
		t.Fatalf("exit %d, stderr %q", code, env.stderr.String())
	}
}

func TestStartLeavesARunningServiceAlone(t *testing.T) {
	env := newTestEnv(t)
	env.installed(svc.Running, testExecutable)

	if code := env.run("start"); code != 0 || len(env.operations()) != 0 || env.stdout.String() != "PlaitwayHelper: running\n" {
		t.Fatalf("exit %d, operations %v, stdout %q", code, env.operations(), env.stdout.String())
	}
}

func TestStartStartsAStoppedServiceAndReportsAFailureWithItsExitCode(t *testing.T) {
	env := newTestEnv(t)
	service := env.installed(svc.Stopped, testExecutable)

	if code := env.run("start"); code != 0 || service.state != svc.Running {
		t.Fatalf("exit %d, state %v, stderr %q", code, service.state, env.stderr.String())
	}

	service.state = svc.Stopped
	service.failsToStart = func(serviceSettings) bool { return true }
	env.stderr.Reset()
	if code := env.run("start"); code != exitFailed || !strings.Contains(env.stderr.String(), "exit code 7") {
		t.Errorf("exit %d, stderr %q; want the exit code of the daemon", code, env.stderr.String())
	}
}

func TestStopWaitsUntilTheServiceHasStopped(t *testing.T) {
	env := newTestEnv(t)
	service := env.installed(svc.Running, testExecutable)
	service.stopTakes = 4

	if code := env.run("stop"); code != 0 || service.state != svc.Stopped || env.stdout.String() != "PlaitwayHelper: stopped\n" {
		t.Fatalf("exit %d, state %v, stdout %q, stderr %q", code, service.state, env.stdout.String(), env.stderr.String())
	}

	env.scm.calls = nil
	if code := env.run("stop"); code != 0 || slices.Contains(env.operations(), "stop") {
		t.Errorf("stopping a stopped service: exit %d, operations %v", code, env.operations())
	}
}

func TestStopGivesUpOnAServiceThatDoesNotStop(t *testing.T) {
	env := newTestEnv(t)
	env.installed(svc.Running, testExecutable).refusesToStop = true

	if code := env.run("stop"); code != exitFailed || !strings.Contains(env.stderr.String(), "running after") {
		t.Fatalf("exit %d, stderr %q", code, env.stderr.String())
	}
}

func TestStatusExitCodesTellInstalledAndRunning(t *testing.T) {
	tests := []struct {
		name     string
		state    svc.State
		missing  bool
		wantCode int
		wantLine string
	}{
		{name: "not installed", missing: true, wantCode: exitNotInstalled, wantLine: "PlaitwayHelper: not installed\n"},
		{name: "stopped", state: svc.Stopped, wantCode: exitNotRunning, wantLine: "PlaitwayHelper: stopped\n"},
		{name: "starting", state: svc.StartPending, wantCode: exitNotRunning, wantLine: "PlaitwayHelper: start pending\n"},
		{name: "stopping", state: svc.StopPending, wantCode: exitNotRunning, wantLine: "PlaitwayHelper: stop pending\n"},
		{name: "running", state: svc.Running, wantCode: 0, wantLine: "PlaitwayHelper: running\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newTestEnv(t)
			if !test.missing {
				env.installed(test.state, testExecutable).stopTakes = 100
			}

			code := env.run("status")

			if code != test.wantCode || env.stdout.String() != test.wantLine || env.stderr.Len() != 0 {
				t.Errorf("exit %d, stdout %q, stderr %q; want %d and %q", code, env.stdout.String(), env.stderr.String(), test.wantCode, test.wantLine)
			}
			if slices.Contains(env.scm.calls, "connect") {
				t.Error("status needs no more access than reading the state, but it opened the Service Control Manager for control")
			}
		})
	}
}

// ---- the real Service Control Manager, read only ----

func TestQueryServiceStatusReportsAServiceThatDoesNotExistAsNotInstalled(t *testing.T) {
	_, err := queryServiceStatus("PlaitwayHelperThatNobodyInstalled")

	if !errors.Is(err, errNotInstalled) {
		t.Fatalf("queryServiceStatus = %v, want errNotInstalled", err)
	}
}

// RpcSs runs on every Windows machine, and anyone may read its state: this is
// the access an unelevated app has to the daemon's service.
func TestQueryServiceStatusReadsARunningServiceWithoutElevation(t *testing.T) {
	status, err := queryServiceStatus("RpcSs")

	if err != nil || status.State != svc.Running {
		t.Fatalf("queryServiceStatus(RpcSs) = %+v, %v; want a running service", status, err)
	}
}

func TestStatusCommandAnswersOnTheRealMachineWithoutElevation(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := newCommands(&stdout, &stderr).run([]string{commandStatus})

	if (code != 0 && code != exitNotRunning && code != exitNotInstalled) || !strings.HasPrefix(stdout.String(), "PlaitwayHelper: ") || stderr.Len() != 0 {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}

// A filtered administrator holds no right to create services, so install, and
// the commands that control the service, are refused by Windows itself, and the
// refusal reads as an instruction.
func TestCommandsThatNeedAdministratorRightsSayToElevate(t *testing.T) {
	if isPrivileged() {
		t.Skip("elevated: the commands would be allowed to proceed, which this test must not ask of the real service")
	}
	for _, command := range []string{commandInstall, commandUninstall, commandStart, commandStop} {
		var stdout, stderr bytes.Buffer
		code := newCommands(&stdout, &stderr).run([]string{command})
		if code != exitFailed || !strings.Contains(stderr.String(), "run from an elevated shell") || stdout.Len() != 0 {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", command, code, stdout.String(), stderr.String())
		}
	}
}

func TestNewCommandsIsWiredForTheProductionService(t *testing.T) {
	c := newCommands(io.Discard, io.Discard)

	if c.name != serviceName || len(c.daemonArgs) != 0 || c.waits != productionWaits || c.startOnDemand || c.sweepDNS == nil {
		t.Fatalf("name %q, daemon arguments %q, waits %+v", c.name, c.daemonArgs, c.waits)
	}
	if !strings.HasSuffix(c.logFile, `Plaitway\Logs\plaitwayd.log`) {
		t.Errorf("log file %q, want the one of the production daemon", c.logFile)
	}
}

// ---- purge ----

func TestPurgeDataRootDeletesTheWholeTree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Plaitway")
	if err := os.MkdirAll(filepath.Join(root, "run"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "profiles.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := purgeDataRoot(root); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the data root is still there: %v", err)
	}
}

func TestPurgeDataRootOfAMissingDirectoryIsNotAnError(t *testing.T) {
	if err := purgeDataRoot(filepath.Join(t.TempDir(), "never-created")); err != nil {
		t.Fatal(err)
	}
}

// A standard user can plant a junction in a directory they can write to, and a
// deletion by an administrator must not follow it.
func TestPurgeDataRootRefusesAJunctionAndLeavesWhatItPointsAtAlone(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside")
	precious := filepath.Join(outside, "precious.txt")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(precious, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "Plaitway")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	fspermtest.MakeJunction(t, filepath.Join(root, "run"), outside)

	err := purgeDataRoot(root)

	if !errors.Is(err, fsperm.ErrReparsePoint) {
		t.Fatalf("purgeDataRoot = %v, want ErrReparsePoint", err)
	}
	if content, readErr := os.ReadFile(precious); readErr != nil || string(content) != "keep" {
		t.Fatalf("the file behind the junction: %q, %v", content, readErr)
	}
}

// A test service starts on demand, so that one that outlives a killed test does
// not come back at the next boot; the real one starts at boot.
func TestOnlyTheTestsRegisterTheServiceForStartOnDemand(t *testing.T) {
	production := newCommands(io.Discard, io.Discard)
	if got := production.desiredSettings(testExecutable).config.StartType; got != mgr.StartAutomatic {
		t.Errorf("the production service starts with %v, want automatic", got)
	}
	testCommands := newCommands(io.Discard, io.Discard)
	testCommands.startOnDemand = true
	settings := testCommands.desiredSettings(testExecutable)
	if settings.config.StartType != mgr.StartManual {
		t.Errorf("the test service starts with %v, want on demand", settings.config.StartType)
	}
	wantElse := desiredSettings(testExecutable, nil)
	wantElse.config.StartType = mgr.StartManual
	if !reflect.DeepEqual(settings, wantElse) {
		t.Errorf("another setting changed with the start type:\n%+v\n%+v", settings, wantElse)
	}
}
