//go:build rootintegration

package main

// WARNING: these tests CREATE AND REMOVE A REAL WINDOWS SERVICE named
// PlaitwayHelperTest and a directory named PlaitwayHelperTest-* below Program
// Files, and they start a SYSTEM process. They never touch the service
// PlaitwayHelper or %ProgramData%\Plaitway: the daemon runs on the fake backend,
// with its state, run directory, log and pipe in the scratch directory.
//
// Run them by hand from an elevated shell, one at a time:
//
//	go test -count=1 -tags rootintegration -run TestRealServiceLifecycle ./cmd/plaitwayd
//	go test -count=1 -tags rootintegration -run TestRealServiceRestartsAfterACrash ./cmd/plaitwayd
//
// The test service is registered to start on demand, never at boot, so that one
// that outlives a killed run does not come back with the PC. A guard process
// (the test binary started again, internal/winiface/rootguard) is armed before
// the first change and removes the service and its directory when this process
// ends without releasing it, or after ten minutes; it prints the command that
// does the same by hand. If both the run and the guard are killed:
// sc.exe stop PlaitwayHelperTest, then sc.exe delete PlaitwayHelperTest, then
// delete "%ProgramFiles%\PlaitwayHelperTest-*".

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/transport"
	"github.com/KoukeNeko/Plaitway/internal/winiface/rootguard"
)

const (
	// testServiceName can never be the real one: the commands refuse it below.
	testServiceName    = "PlaitwayHelperTest"
	scratchDirPrefix   = "PlaitwayHelperTest-"
	buildTimeout       = 5 * time.Minute
	restartWait        = 40 * time.Second
	noRestartAfterStop = 8 * time.Second // longer than the first restart delay
	localSystemSID     = "S-1-5-18"

	recoveryTestService = "test-service"
	// serviceGuardWindow covers the build of the daemon and the longest test; the
	// guard removes the service and its directory after it if the test is hung.
	serviceGuardWindow = buildTimeout + 5*time.Minute
)

func init() { rootguard.Register(recoveryTestService, removeTestServiceAndDirectory) }

// TestRootGuardHelper is the entry of the guards that these tests arm: the test
// binary started again (see internal/winiface/rootguard).
func TestRootGuardHelper(t *testing.T) { rootguard.RunHelper(t) }

// removeTestServiceAndDirectory is the recovery of the guard: the service goes
// first, and the directory it ran from after it.
func removeTestServiceAndDirectory(scratch string) error {
	if err := removeTestService(); err != nil {
		return err
	}
	return os.RemoveAll(scratch)
}

// manualRecoveryCommand does by hand what the guard does.
func manualRecoveryCommand(scratch string) string {
	return fmt.Sprintf("sc.exe stop %s; sc.exe delete %s; Remove-Item -Recurse -Force '%s'", testServiceName, testServiceName, scratch)
}

// testService is one run of the test service in its scratch directory.
type testService struct {
	t        *testing.T
	scratch  string
	pipe     string
	logFile  string
	commands *commands
}

// requireElevated skips unless the shell is elevated, and refuses to go on if
// anything named like the test service is there already.
func requireElevated(t *testing.T) {
	t.Helper()
	if !isPrivileged() {
		t.Skip("run from an elevated shell: the test installs a real service")
	}
	if testServiceName == serviceName {
		t.Fatal("the test service must not be the real one")
	}
	if _, err := queryServiceStatus(testServiceName); !errors.Is(err, errNotInstalled) {
		t.Fatalf("%s exists already (%v); remove it with sc.exe delete %s first", testServiceName, err, testServiceName)
	}
}

// newTestService builds the daemon into a scratch directory below Program
// Files, where the install accepts it, and prepares the commands. Everything it
// makes is removed when the test ends, also when the test panics.
func newTestService(t *testing.T) *testService {
	t.Helper()
	requireElevated(t)

	programFiles, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, windows.KF_FLAG_DEFAULT)
	if err != nil {
		t.Fatal(err)
	}
	scratch, err := os.MkdirTemp(programFiles, scratchDirPrefix)
	if err != nil {
		t.Fatalf("create the scratch directory below %s: %v", programFiles, err)
	}
	s := &testService{
		t:       t,
		scratch: scratch,
		pipe:    fmt.Sprintf(`\\.\pipe\plaitway-test-service-%d`, os.Getpid()),
		logFile: filepath.Join(scratch, "Logs", "plaitwayd.log"),
	}
	// The guard goes first, so that it is released last: the clean-ups below are
	// the ones that do the work when this process lives to run them.
	rootguard.Arm(t, rootguard.Plan{Action: recoveryTestService, Argument: scratch, Within: serviceGuardWindow, Manual: manualRecoveryCommand(scratch)})
	// Last in, first out: the service goes before the directory it runs from.
	t.Cleanup(func() { os.RemoveAll(scratch) })
	t.Cleanup(s.removeService)

	executable := filepath.Join(scratch, "plaitwayd.exe")
	s.buildDaemon(executable)

	stdout, stderr := testWriter{t, "stdout"}, testWriter{t, "stderr"}
	s.commands = newCommands(stdout, stderr)
	s.commands.name = testServiceName
	s.commands.executable = func() (string, error) { return executable, nil }
	s.commands.logFile = s.logFile
	s.commands.startOnDemand = true
	s.commands.daemonArgs = []string{
		"-fake", "-socket", s.pipe,
		"-state-dir", filepath.Join(scratch, "state"),
		"-run-dir", filepath.Join(scratch, "run"),
		"-log-file", s.logFile,
	}
	return s
}

type testWriter struct {
	t    *testing.T
	name string
}

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Logf("%s: %s", w.name, strings.TrimRight(string(p), "\r\n"))
	return len(p), nil
}

// buildDaemon builds this package into dst. The test runs in the package
// directory.
func (s *testService) buildDaemon(dst string) {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "go", "build", "-o", dst, ".").CombinedOutput()
	if err != nil {
		s.t.Fatalf("go build: %v\n%s", err, out)
	}
}

// removeService stops and deletes the test service if it exists. It runs after
// every test, also after a failed or a panicking one, and does nothing when
// there is nothing to remove.
func (s *testService) removeService() {
	if err := removeTestService(); err != nil {
		s.t.Logf("cleanup: %v (sc.exe delete %s)", err, testServiceName)
	}
}

// removeTestService is removeService without a test: the guard runs it too. A
// service that is not installed is not an error.
func removeTestService() error {
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to the Service Control Manager: %w", err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(testServiceName)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", testServiceName, err)
	}
	defer service.Close()
	if status, err := service.Query(); err == nil && status.State != svc.Stopped {
		service.Control(svc.Stop)
		for deadline := time.Now().Add(restartWait); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
			if status, err := service.Query(); err != nil || status.State == svc.Stopped {
				break
			}
		}
	}
	if err := service.Delete(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
		return fmt.Errorf("delete %s: %w", testServiceName, err)
	}
	return nil
}

// guard runs a test body and cleans up before a panic goes on.
func guard(s *testService, body func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			s.removeService()
			os.RemoveAll(s.scratch)
			panic(recovered)
		}
	}()
	body()
}

func (s *testService) run(args ...string) {
	s.t.Helper()
	if code := s.commands.run(args); code != 0 {
		s.t.Fatalf("plaitwayd %s exited with %d", strings.Join(args, " "), code)
	}
}

func (s *testService) status() svc.Status {
	s.t.Helper()
	status, err := queryServiceStatus(testServiceName)
	if err != nil {
		s.t.Fatalf("query %s: %v", testServiceName, err)
	}
	return status
}

func TestRealServiceLifecycle(t *testing.T) {
	s := newTestService(t)
	guard(s, func() {
		s.run(commandInstall)
		s.checkRegistration()
		s.checkUnelevatedUserCanOnlyRead()
		if status := s.status(); status.State != svc.Stopped {
			t.Fatalf("a new service is %v, want stopped", status.State)
		}

		s.run(commandStart)
		status := s.status()
		if status.State != svc.Running || status.ProcessId == 0 {
			t.Fatalf("after start: %+v, want a running service with a process", status)
		}
		s.checkPipeServedByTheService(status.ProcessId)
		s.checkCallsAreAuthorized()
		s.checkTheDaemonRunsAsSystem()

		s.run(commandStop)
		if state := s.status().State; state != svc.Stopped {
			t.Fatalf("after stop: %v", state)
		}
		s.checkNothingListensOnThePipe()
		// A requested stop is a clean exit: the failure actions, which also run
		// for a non-zero exit code, must not bring it back.
		time.Sleep(noRestartAfterStop)
		if state := s.status().State; state != svc.Stopped {
			t.Fatalf("the service came back %s after a stop: %v, restarted by its failure actions", noRestartAfterStop, state)
		}

		s.run(commandUninstall)
		if _, err := queryServiceStatus(testServiceName); !errors.Is(err, errNotInstalled) {
			t.Fatalf("after uninstall: %v, want the service gone", err)
		}
	})
}

// checkRegistration reads the registration back from the Service Control
// Manager and compares it with what install is meant to set.
func (s *testService) checkRegistration() {
	s.t.Helper()
	manager, err := mgr.Connect()
	if err != nil {
		s.t.Fatal(err)
	}
	defer manager.Disconnect()
	opened, err := manager.OpenService(testServiceName)
	if err != nil {
		s.t.Fatal(err)
	}
	service := systemService{service: opened}
	defer service.Close()

	got, err := service.Settings()
	if err != nil {
		s.t.Fatal(err)
	}
	want := s.commands.desiredSettings(s.commands.mustExecutable())
	// Not derived from the commands: a test service that started at boot would
	// outlive a killed run, and come back with the PC.
	if got.config.StartType != mgr.StartManual {
		s.t.Errorf("the test service starts with %v, want on demand", got.config.StartType)
	}
	checks := map[string][2]any{
		"binary path":      {got.config.BinaryPathName, want.config.BinaryPathName},
		"account":          {got.config.ServiceStartName, want.config.ServiceStartName},
		"start type":       {got.config.StartType, want.config.StartType},
		"delayed start":    {got.config.DelayedAutoStart, want.config.DelayedAutoStart},
		"service SID type": {got.config.SidType, want.config.SidType},
		"display name":     {got.config.DisplayName, want.config.DisplayName},
		"description":      {got.config.Description, want.config.Description},
		"reset period":     {got.resetPeriod, want.resetPeriod},
		"non-crash flag":   {got.restartOnNonCrash, want.restartOnNonCrash},
		"preshutdown":      {got.preshutdownTimeout, want.preshutdownTimeout},
		"failure actions":  {fmt.Sprint(got.recovery), fmt.Sprint(want.recovery)},
		"dependencies":     {fmt.Sprint(got.config.Dependencies), fmt.Sprint(want.config.Dependencies)},
		"service type":     {got.config.ServiceType, want.config.ServiceType},
		"error control":    {got.config.ErrorControl, want.config.ErrorControl},
	}
	for name, pair := range checks {
		if fmt.Sprint(pair[0]) != fmt.Sprint(pair[1]) {
			s.t.Errorf("%s is %v, want %v", name, pair[0], pair[1])
		}
	}
	for _, ace := range []string{"(A;;LCLORC;;;IU)", ";;;SY)", ";;;BA)"} {
		if !strings.Contains(got.accessList, ace) {
			s.t.Errorf("access list %s lacks %s", got.accessList, ace)
		}
	}
}

func (c *commands) mustExecutable() string {
	executable, err := c.executable()
	if err != nil {
		panic(err)
	}
	return executable
}

// checkUnelevatedUserCanOnlyRead takes the elevated token of the test and makes
// it what an administrator's ordinary shell has: Administrators deny-only, no
// privileges. That token reads the state of the service, and is refused
// everything else.
func (s *testService) checkUnelevatedUserCanOnlyRead() {
	s.t.Helper()
	runAsUnelevatedUser(s.t, func() {
		status, err := queryServiceStatus(testServiceName)
		if err != nil || status.State != svc.Stopped {
			s.t.Errorf("an unelevated user reads %+v, %v; want a stopped service", status, err)
		}
		manager, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
		if err != nil {
			s.t.Fatalf("an unelevated user cannot connect to the Service Control Manager: %v", err)
		}
		defer windows.CloseServiceHandle(manager)
		name, _ := windows.UTF16PtrFromString(testServiceName)
		for what, right := range map[string]uint32{
			"start": windows.SERVICE_START, "stop": windows.SERVICE_STOP,
			"change the configuration": windows.SERVICE_CHANGE_CONFIG, "delete": windows.DELETE,
		} {
			handle, err := windows.OpenService(manager, name, right)
			if err == nil {
				windows.CloseServiceHandle(handle)
				s.t.Errorf("an unelevated user may %s the service", what)
			} else if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
				s.t.Errorf("an unelevated user trying to %s: %v, want access denied", what, err)
			}
		}
	})
}

// runAsUnelevatedUser runs body on this thread with an impersonation token that
// is the caller's with Administrators deny-only and every privilege removed.
func runAsUnelevatedUser(t *testing.T, body func()) {
	t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	var own windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_ASSIGN_PRIMARY|windows.TOKEN_QUERY, &own); err != nil {
		t.Fatal(err)
	}
	defer own.Close()
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	disable := windows.SIDAndAttributes{Sid: administrators}
	const disableMaxPrivilege = 0x1
	var restricted windows.Token
	create := windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken")
	result, _, callErr := create.Call(uintptr(own), disableMaxPrivilege, 1, uintptr(unsafe.Pointer(&disable)), 0, 0, 0, 0, uintptr(unsafe.Pointer(&restricted)))
	if result == 0 {
		t.Fatalf("CreateRestrictedToken: %v", callErr)
	}
	defer restricted.Close()
	var impersonation windows.Token
	if err := windows.DuplicateTokenEx(restricted, windows.TOKEN_QUERY|windows.TOKEN_IMPERSONATE, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &impersonation); err != nil {
		t.Fatal(err)
	}
	defer impersonation.Close()
	if err := windows.SetThreadToken(nil, impersonation); err != nil {
		t.Fatal(err)
	}
	defer windows.RevertToSelf()

	if member, err := windows.Token(0).IsMember(administrators); err != nil || member {
		t.Fatalf("the impersonated token is still an administrator (member %v, %v)", member, err)
	}
	body()
}

// checkPipeServedByTheService connects to the pipe and checks that the process
// that serves it is the one the service manager started, and that the pipe is
// owned by SYSTEM or Administrators, which is all a service creates.
func (s *testService) checkPipeServedByTheService(servicePID uint32) {
	s.t.Helper()
	timeout := 5 * time.Second
	conn, err := winio.DialPipe(s.pipe, &timeout)
	if err != nil {
		s.t.Fatalf("dial %s: %v", s.pipe, err)
	}
	defer conn.Close()
	handle := windows.Handle(conn.(interface{ Fd() uintptr }).Fd())
	var serverPID uint32
	if err := windows.GetNamedPipeServerProcessId(handle, &serverPID); err != nil || serverPID != servicePID {
		s.t.Errorf("the pipe is served by process %d (%v), the service runs as %d", serverPID, err, servicePID)
	}
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		s.t.Fatalf("read the owner of the pipe: %v", err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil || !(owner.IsWellKnown(windows.WinLocalSystemSid) || owner.IsWellKnown(windows.WinBuiltinAdministratorsSid)) {
		s.t.Errorf("the pipe is owned by %v (%v), want SYSTEM or Administrators", owner, err)
	}
}

// checkCallsAreAuthorized calls the daemon the way the app does, through the
// verified dial: a read, and a call that only administrators may make.
func (s *testService) checkCallsAreAuthorized() {
	s.t.Helper()
	conn, err := grpc.NewClient(transport.Target(s.pipe), append(transport.DialOptions(), grpc.WithTransportCredentials(insecure.NewCredentials()))...)
	if err != nil {
		s.t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := pb.NewDaemonServiceClient(conn)

	info, err := client.GetDaemonInfo(ctx, &pb.GetDaemonInfoRequest{}, grpc.WaitForReady(true))
	if err != nil || info.Version != version {
		s.t.Fatalf("GetDaemonInfo: %v, %v", info, err)
	}
	imported, err := client.ImportProfile(ctx, &pb.ImportProfileRequest{Name: "integration", Content: []byte(ovpnProfile)})
	if err != nil {
		s.t.Fatalf("ImportProfile as an administrator: %v", err)
	}
	if _, err := client.DeleteProfile(ctx, &pb.DeleteProfileRequest{Id: imported.Profile.Id}); err != nil {
		s.t.Errorf("DeleteProfile: %v", err)
	}
}

// checkTheDaemonRunsAsSystem reads what the daemon logged about itself.
func (s *testService) checkTheDaemonRunsAsSystem() {
	s.t.Helper()
	logged, err := os.ReadFile(s.logFile)
	if err != nil {
		s.t.Fatalf("read the log of the service: %v", err)
	}
	for _, want := range []string{"msg=listening", "user=" + localSystemSID, "privileged=true", "fake=true", "plaitway-test-service-" + strconv.Itoa(os.Getpid())} {
		if !strings.Contains(string(logged), want) {
			s.t.Errorf("the log of the service lacks %q:\n%s", want, logged)
		}
	}
}

func (s *testService) checkNothingListensOnThePipe() {
	s.t.Helper()
	timeout := time.Second
	conn, err := winio.DialPipe(s.pipe, &timeout)
	if err == nil {
		conn.Close()
		s.t.Errorf("%s still accepts connections after the service stopped", s.pipe)
	}
	logged, readErr := os.ReadFile(s.logFile)
	if readErr != nil || !strings.Contains(string(logged), "msg=stopping") {
		s.t.Errorf("the log lacks the stop (%v):\n%s", readErr, logged)
	}
}

// Killing the process is a crash to the service manager: it must start the
// service again after the first restart delay.
func TestRealServiceRestartsAfterACrash(t *testing.T) {
	s := newTestService(t)
	guard(s, func() {
		s.run(commandInstall, "-start")
		first := s.status()
		if first.State != svc.Running {
			t.Fatalf("after install -start: %+v", first)
		}

		process, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, first.ProcessId)
		if err != nil {
			t.Skipf("this machine does not let an administrator end a SYSTEM service process (%v); end it some other way and look at the state", err)
		}
		defer windows.CloseHandle(process)
		if err := windows.TerminateProcess(process, 1); err != nil {
			t.Fatal(err)
		}

		deadline := time.Now().Add(restartWait)
		for time.Now().Before(deadline) {
			if status := s.status(); status.State == svc.Running && status.ProcessId != first.ProcessId {
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Fatalf("the service was not restarted within %s of a crash: %+v", restartWait, s.status())
	})
}
