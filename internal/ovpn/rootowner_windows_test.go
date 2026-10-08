package ovpn

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The root tests make TAP adapters and run openvpn next to the OpenVPN of the
// person whose PC this is, if there is one. They never name an adapter that is
// not theirs, but two OpenVPNs on one machine are not something to leave to
// luck: the TAP driver is shared, a connection that is up has routes and DNS
// of its own, and the tests start and stop processes by name. So they refuse to
// run while an openvpn.exe that is not theirs is running, unless the person
// says that it is fine.

const (
	// scratchPrefix starts the name of every adapter the root tests make. It is
	// not a name that the engine, the OpenVPN GUI or its services give.
	scratchPrefix = "Plaitway-test-ovpn-"

	// allowOwnerOpenVPNEnv, set to 1, lets the root tests run while another
	// OpenVPN is connected.
	allowOwnerOpenVPNEnv = "PLAITWAY_ROOT_ALLOW_OWNER_OPENVPN"

	openvpnImageName = "openvpn.exe"

	// processCommandLineInformation is the class of NtQueryInformationProcess
	// that returns the command line as a UNICODE_STRING (Windows 8.1 and later),
	// which needs no read of the memory of the process.
	processCommandLineInformation = 60
	// commandLineBufferSize holds the longest command line (32767 characters)
	// and the UNICODE_STRING in front of it.
	commandLineBufferSize = 2*32768 + 64
)

// runningProcess is a process of the machine, with the command line it was
// started with when that could be read.
type runningProcess struct {
	PID         uint32
	CommandLine string
	// CommandLineErr is why CommandLine is empty: a process of another user or of
	// the system may not be opened.
	CommandLineErr error
}

// The command lines of the processes of the tests. The engine starts openvpn
// with --dev-node naming a scratch adapter, and the loopback server of the
// tests is an openvpn that has no device and listens on 127.0.0.1.
var testServerArguments = []string{"--mode server", "--dev null", "--local 127.0.0.1"}

// belongsToTheTests reports whether an openvpn command line is one of the
// tests': it names a scratch adapter, or it is their loopback server.
func belongsToTheTests(commandLine string) bool {
	if strings.Contains(strings.ToLower(commandLine), strings.ToLower(scratchPrefix)) {
		return true
	}
	for _, argument := range testServerArguments {
		if !strings.Contains(commandLine, argument) {
			return false
		}
	}
	return true
}

// ownersOpenVPNs picks the processes that are not the tests'. A process whose
// command line could not be read has an empty one, which belongs to nobody, so it
// counts as the owner's: not knowing is no reason to start.
func ownersOpenVPNs(processes []runningProcess) []runningProcess {
	var owners []runningProcess
	for _, process := range processes {
		if !belongsToTheTests(process.CommandLine) {
			owners = append(owners, process)
		}
	}
	return owners
}

// runningOpenVPNs lists the openvpn.exe processes of the machine.
func runningOpenVPNs() ([]runningProcess, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("list the processes: %w", err)
	}
	defer windows.CloseHandle(snapshot)

	var found []runningProcess
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if !strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), openvpnImageName) {
			continue
		}
		commandLine, lineErr := commandLineOf(entry.ProcessID)
		found = append(found, runningProcess{PID: entry.ProcessID, CommandLine: commandLine, CommandLineErr: lineErr})
	}
	if !errors.Is(err, syscall.ERROR_NO_MORE_FILES) {
		return nil, fmt.Errorf("list the processes: %w", err)
	}
	return found, nil
}

func commandLineOf(pid uint32) (string, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", fmt.Errorf("open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(process)
	buffer := make([]uint64, commandLineBufferSize/8)
	var written uint32
	status := windows.NtQueryInformationProcess(process, processCommandLineInformation, unsafe.Pointer(&buffer[0]), uint32(len(buffer)*8), &written)
	if status != nil {
		return "", fmt.Errorf("read the command line of process %d: %w", pid, status)
	}
	return (*windows.NTUnicodeString)(unsafe.Pointer(&buffer[0])).String(), nil
}

// requireNoOwnerOpenVPN fails the test while an OpenVPN that is not the tests'
// runs on the machine, and says which and how to go on anyway.
func requireNoOwnerOpenVPN(t *testing.T) {
	t.Helper()
	if os.Getenv(allowOwnerOpenVPNEnv) == "1" {
		t.Logf("%s=1: running next to an OpenVPN that is not ours", allowOwnerOpenVPNEnv)
		return
	}
	processes, err := runningOpenVPNs()
	if err != nil {
		t.Fatalf("cannot tell whether an OpenVPN of yours is connected, so the test does not run: %v", err)
	}
	if refusal := refusalFor(processes); refusal != "" {
		t.Fatal(refusal)
	}
}

// refusalFor is the reason not to run next to these openvpn processes, or empty
// when none of them is the owner's.
func refusalFor(processes []runningProcess) string {
	owners := ownersOpenVPNs(processes)
	if len(owners) == 0 {
		return ""
	}
	described := make([]string, len(owners))
	for i, process := range owners {
		described[i] = fmt.Sprintf("pid %d: %s", process.PID, describeProcess(process))
	}
	return fmt.Sprintf("refusing to run: an OpenVPN that is not the tests' is running (%s). Disconnect it first, or set %s=1 to run next to it.",
		strings.Join(described, "; "), allowOwnerOpenVPNEnv)
}

// describeProcess says which process it is by its command line, or by why there
// is none.
func describeProcess(process runningProcess) string {
	if process.CommandLine != "" {
		return process.CommandLine
	}
	return fmt.Sprintf("command line unknown: %v", process.CommandLineErr)
}

func TestOnlyTheCommandLinesOfTheTestsBelongToThem(t *testing.T) {
	tests := []struct {
		name string
		line string
		want bool
	}{
		{"the service of the owner's OpenVPN", `"C:\Program Files\OpenVPN\bin\openvpn.exe" --config "C:\Users\me\OpenVPN\config\asus\asus.ovpn" --service "Global\exit" 0`, false},
		{"the GUI's connection of the owner", `openvpn.exe --config asus.ovpn --management 127.0.0.1 25340 stdin --auth-retry interact`, false},
		{"an owner's server that listens on loopback", `openvpn.exe --mode server --dev tun --local 127.0.0.1 --port 1194`, false},
		{"the engine's client on a scratch adapter", `openvpn.exe --config c.ovpn --dev-node Plaitway-test-ovpn-1a2b3c4d --dev tun`, true},
		{"the same in other capitals", `openvpn.exe --dev-node plaitway-TEST-ovpn-1a2b3c4d`, true},
		{"the loopback server of the tests", `openvpn.exe --mode server --tls-server --dev null --server 198.51.100.0 255.255.255.0 --local 127.0.0.1 --port 5000`, true},
		{"an adapter of the daemon, not of the tests", `openvpn.exe --dev-node Plaitway-1a2b3c4d`, false},
		{"nothing known", ``, false},
	}
	for _, tt := range tests {
		if got := belongsToTheTests(tt.line); got != tt.want {
			t.Errorf("%s: belongsToTheTests = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestAProcessThatCannotBeReadCountsAsTheOwners(t *testing.T) {
	processes := []runningProcess{
		{PID: 1, CommandLine: `openvpn.exe --dev-node Plaitway-test-ovpn-1a2b3c4d`},
		{PID: 2, CommandLineErr: errors.New("access denied")},
		{PID: 3, CommandLine: `openvpn.exe --config asus.ovpn`},
	}
	owners := ownersOpenVPNs(processes)
	if len(owners) != 2 || owners[0].PID != 2 || owners[1].PID != 3 {
		t.Errorf("owners = %+v, want processes 2 and 3", owners)
	}
	refusal := refusalFor(processes)
	for _, want := range []string{"pid 2", "command line unknown: access denied", "pid 3", "asus.ovpn", allowOwnerOpenVPNEnv} {
		if !strings.Contains(refusal, want) {
			t.Errorf("refusal %q lacks %q", refusal, want)
		}
	}
	if strings.Contains(refusal, "pid 1") {
		t.Errorf("refusal %q names the tests' own process", refusal)
	}
	if got := refusalFor(processes[:1]); got != "" {
		t.Errorf("only the tests' own process: refusal %q", got)
	}
	if got := refusalFor(nil); got != "" {
		t.Errorf("no process: refusal %q", got)
	}
}

// The real listing, against a process the test starts: a copy of ping.exe under
// the name openvpn.exe stands for an openvpn that is connected. It must be
// found with its command line, and the process of the same image that is not
// named so must not.
func TestRunningOpenVPNsFindsAProcessByItsImageNameAndReadsItsCommandLine(t *testing.T) {
	ping := filepath.Join(os.Getenv("SystemRoot"), "System32", "ping.exe")
	stand := filepath.Join(t.TempDir(), openvpnImageName)
	if err := copyExecutable(ping, stand); err != nil {
		t.Skipf("no ping.exe to stand in: %v", err)
	}
	cmd := exec.Command(stand, "-n", "30", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})

	processes, err := runningOpenVPNs()
	if err != nil {
		t.Fatal(err)
	}
	var found *runningProcess
	for i := range processes {
		if processes[i].PID == uint32(cmd.Process.Pid) {
			found = &processes[i]
		}
	}
	if found == nil {
		t.Fatalf("process %d is not among %+v", cmd.Process.Pid, processes)
	}
	if found.CommandLineErr != nil || !strings.HasSuffix(found.CommandLine, `openvpn.exe -n 30 127.0.0.1`) {
		t.Errorf("command line %q (%v), want the one the process was started with", found.CommandLine, found.CommandLineErr)
	}
	if !slices.ContainsFunc(ownersOpenVPNs(processes), func(p runningProcess) bool { return p.PID == found.PID }) {
		t.Error("an openvpn that is not the tests' was not counted as the owner's")
	}
}

// scratchAdapterNames picks the names that start with scratchPrefix. Every
// adapter the tests make, the one a test makes by hand included, is named so, and
// no adapter of the OpenVPN GUI or the services is.
func scratchAdapterNames(names []string) []string {
	return slices.DeleteFunc(slices.Clone(names), func(name string) bool {
		return !strings.HasPrefix(strings.ToLower(name), strings.ToLower(scratchPrefix))
	})
}

func TestOnlyTheScratchAdaptersAreSwept(t *testing.T) {
	names := []string{
		"Ethernet", "OpenVPN TAP-Windows6", "OpenVPN Data Channel Offload", "OpenVPN TAP-Windows6 #2",
		"Plaitway-1a2b3c4d",             // the daemon's, with its own prefix
		"Plaitway-test-ovpn-1a2b3c4d",   // an engine's adapter in a root test
		"plaitway-TEST-ovpn-not-ours",   // the adapter that TestRootStaleAdaptersAreRemoved keeps by hand
		"Plaitway-test-wg-1a2b3c4d",     // the WireGuard tests' own prefix
		"OpenVPN Plaitway-test-ovpn-xx", // the prefix inside a name is not the start of one
	}
	want := []string{"Plaitway-test-ovpn-1a2b3c4d", "plaitway-TEST-ovpn-not-ours"}
	if got := scratchAdapterNames(names); !slices.Equal(got, want) {
		t.Errorf("scratchAdapterNames = %q, want %q", got, want)
	}
}
