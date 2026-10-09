package wg

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestTunOpenError(t *testing.T) {
	const path = "/dev/net/tun"
	tests := []struct {
		name  string
		errno syscall.Errno
		want  string
		// keepsCause is set where the message adds to the error instead of replacing it.
		keepsCause bool
	}{
		{"missing node", syscall.ENOENT, "does not exist: load the tun kernel module", false},
		{"no driver", syscall.ENODEV, "no tun driver", false},
		{"EACCES", syscall.EACCES, "no permission to open /dev/net/tun", true},
		// A container whose device cgroup does not list the device answers EPERM.
		{"EPERM", syscall.EPERM, "no permission to open /dev/net/tun", true},
		{"anything else", syscall.EMFILE, "too many open files", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cause := &fs.PathError{Op: "open", Path: path, Err: tt.errno}
			err := tunOpenError(path, cause)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("tunOpenError = %v, want it to say %q", err, tt.want)
			}
			if tt.keepsCause && !errors.Is(err, tt.errno) {
				t.Errorf("tunOpenError = %v, want the cause kept", err)
			}
		})
	}
}

func TestCheckTunDevice(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "tun")
	if err := os.WriteFile(present, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkTunDevice(present); err != nil {
		t.Errorf("checkTunDevice(existing file) = %v", err)
	}
	err := checkTunDevice(filepath.Join(dir, "missing"))
	if err == nil || !strings.Contains(err.Error(), "modprobe tun") {
		t.Errorf("checkTunDevice(missing) = %v, want advice to load the tun module", err)
	}
}

func TestExplainTunError(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "tun")
	if err := os.WriteFile(present, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func(old string) func() { return func() { tunDevicePath = old } }(tunDevicePath))

	// wireguard-go reports a missing device without the error behind it.
	tunDevicePath = filepath.Join(dir, "missing")
	cause := errors.New("CreateTUN failed; the device does not exist")
	if err := explainTunError(cause); err == nil || !strings.Contains(err.Error(), "modprobe tun") {
		t.Errorf("explainTunError(device missing) = %v, want advice to load the tun module", err)
	}

	tunDevicePath = present
	if err := explainTunError(syscall.EPERM); !errors.Is(err, syscall.EPERM) || !strings.Contains(err.Error(), "CAP_NET_ADMIN") {
		t.Errorf("explainTunError(EPERM) = %v, want a message naming CAP_NET_ADMIN around the cause", err)
	}
	if err := explainTunError(syscall.EBUSY); err != syscall.EBUSY {
		t.Errorf("explainTunError(EBUSY) = %v, want the error as it was", err)
	}
}

func TestProbeSaysWhyTheTunDeviceIsUnusable(t *testing.T) {
	t.Cleanup(func(old string) func() { return func() { tunDevicePath = old } }(tunDevicePath))
	tunDevicePath = filepath.Join(t.TempDir(), "missing")
	info := Backend(Config{}).Probe()
	if info.Available || !strings.Contains(info.Detail, tunDevicePath) {
		t.Errorf("Probe() = %+v, want an unavailable engine whose detail names %s", info, tunDevicePath)
	}
}

// fakeIPv6Sysctl makes the sysctl directory of an interface and returns the
// path of its disable_ipv6.
func fakeIPv6Sysctl(t *testing.T, iface, content string) string {
	t.Helper()
	root := t.TempDir()
	t.Cleanup(func(old string) func() { return func() { procSysNet = old } }(procSysNet))
	procSysNet = root
	dir := filepath.Join(root, "ipv6", "conf", iface)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "disable_ipv6")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEnableIPv6TurnsItOnWhereTheHostDisablesItByDefault(t *testing.T) {
	path := fakeIPv6Sysctl(t, "plaitway0", "1\n")
	var l logSink
	enableIPv6("plaitway0", l.log)
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "0" {
		t.Errorf("disable_ipv6 = %q, %v; want 0", got, err)
	}
	if !strings.Contains(strings.Join(l.lines, "\n"), "plaitway0") {
		t.Errorf("log = %q, want a line about plaitway0", strings.Join(l.lines, "\n"))
	}
}

func TestEnableIPv6LeavesWhatIsAlreadyOn(t *testing.T) {
	path := fakeIPv6Sysctl(t, "plaitway0", "0\n")
	var l logSink
	enableIPv6("plaitway0", l.log)
	if got, _ := os.ReadFile(path); string(got) != "0\n" {
		t.Errorf("disable_ipv6 = %q, want it untouched", got)
	}
	if len(l.lines) != 0 {
		t.Errorf("log = %q, want nothing", strings.Join(l.lines, "\n"))
	}
}

func TestEnableIPv6HasNothingToDoWithoutAnIPv6Stack(t *testing.T) {
	fakeIPv6Sysctl(t, "other0", "1\n")
	var l logSink
	enableIPv6("plaitway0", l.log) // the interface has no sysctl directory
	if len(l.lines) != 0 {
		t.Errorf("log = %q, want nothing", strings.Join(l.lines, "\n"))
	}
}

func TestEnableIPv6WarnsWhenItCannotRead(t *testing.T) {
	path := fakeIPv6Sysctl(t, "plaitway0", "")
	// A directory in place of the file: reading it fails whoever runs the test.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	var l logSink
	enableIPv6("plaitway0", l.log)
	if want := "3 read whether IPv6 is disabled on plaitway0"; !strings.Contains(strings.Join(l.lines, "\n"), want) {
		t.Errorf("log = %q, want a warning starting %q", strings.Join(l.lines, "\n"), want)
	}
}

func TestEnableIPv6WarnsWhenItCannotWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to a read-only file")
	}
	path := fakeIPv6Sysctl(t, "plaitway0", "1\n")
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	var l logSink
	enableIPv6("plaitway0", l.log)
	if want := "3 enable IPv6 on plaitway0"; !strings.Contains(strings.Join(l.lines, "\n"), want) {
		t.Errorf("log = %q, want a warning starting %q", strings.Join(l.lines, "\n"), want)
	}
}

func TestAwaitInterfaceRemovalReturnsAtOnceForAnInterfaceThatIsGone(t *testing.T) {
	var l logSink
	start := time.Now()
	awaitInterfaceRemoval("plaitway-none", l.log)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %v for an interface that does not exist", elapsed)
	}
	if len(l.lines) != 0 {
		t.Errorf("log = %q, want nothing", strings.Join(l.lines, "\n"))
	}
}

func TestAwaitInterfaceRemovalWarnsWhenTheInterfaceStays(t *testing.T) {
	t.Cleanup(func(wait time.Duration) func() { return func() { removalWait = wait } }(removalWait))
	removalWait = 50 * time.Millisecond
	var l logSink
	start := time.Now()
	awaitInterfaceRemoval("lo", l.log) // loopback exists in every network namespace
	if elapsed := time.Since(start); elapsed < removalWait {
		t.Errorf("returned after %v, before the wait of %v was over", elapsed, removalWait)
	}
	if want := "3 lo is still there"; !strings.Contains(strings.Join(l.lines, "\n"), want) {
		t.Errorf("log = %q, want a warning starting %q", strings.Join(l.lines, "\n"), want)
	}
}
