package ovpn

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// socketPathOfLength is a path for a management socket that is n bytes long, in
// a directory that exists.
func socketPathOfLength(t *testing.T, n int) string {
	t.Helper()
	base := shortTempDir(t)
	pad := n - len(base) - 1 - len(socketFileName) - 1
	if pad < 1 {
		t.Fatalf("%s is too long to make a path of %d bytes", base, n)
	}
	dir := filepath.Join(base, strings.Repeat("d", pad))
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, socketFileName)
}

// maxSocketPath is what the kernel takes: sun_path has 108 bytes, the last one
// for the terminating NUL.
func TestMaxSocketPathIsTheKernels(t *testing.T) {
	longest := socketPathOfLength(t, maxSocketPath)
	if got, err := unixSocketPath(filepath.Dir(longest)); err != nil || got != longest {
		t.Fatalf("unixSocketPath(%d bytes) = %q, %v; want it accepted", len(longest), got, err)
	}
	ln, err := net.Listen("unix", longest)
	if err != nil {
		t.Fatalf("the kernel refused a socket path of %d bytes: %v", len(longest), err)
	}
	ln.Close()

	tooLong := socketPathOfLength(t, maxSocketPath+1)
	if _, err := unixSocketPath(filepath.Dir(tooLong)); err == nil {
		t.Errorf("unixSocketPath accepted %d bytes", len(tooLong))
	}
	if ln, err := net.Listen("unix", tooLong); err == nil {
		ln.Close()
		t.Errorf("the kernel took a socket path of %d bytes", len(tooLong))
	}
}

// openvpn must listen on the longest path the engine accepts; one more byte is
// refused before anything starts.
func TestRealBinaryListensOnTheLongestSocketPath(t *testing.T) {
	bin := realBinary(t)
	p := newPKI(t)
	profile := asusLoopbackProfile(p, freeTCPPort(t), "null")

	// The socket is <runDir>/ovpn-<12 hex digits>/m.sock.
	const engineDirBytes = len("ovpn-") + 12
	runDir := func(socketBytes int) string {
		base := shortTempDir(t)
		pad := socketBytes - len(base) - 1 - 1 - engineDirBytes - 1 - len(socketFileName)
		return filepath.Join(base, strings.Repeat("r", pad))
	}

	h := newHarness(t, harnessOpts{binary: bin, profile: profile, runDir: runDir(maxSocketPath)})
	if got := len(h.eng.channel.(*unixChannel).socket); got != maxSocketPath {
		t.Fatalf("the test's socket path is %d bytes, want %d", got, maxSocketPath)
	}
	h.start()
	h.waitFor("a request for credentials", h.stateIs(tunnel.StateAwaitingCredentials))
	h.stop()

	b := newBackend(Config{Binary: bin, RunDir: runDir(maxSocketPath + 1)})
	if _, err := b.newEngine(tunnel.Spec{Owner: "long", Content: []byte(profile)}, tunnel.Deps{Network: &fakeNetwork{}}); err == nil {
		t.Error("an engine was made for a management socket path one byte too long")
	}
}

// The engine does not start a profile for a tap device, however it asks for
// one, and says why. It would come up and carry nothing: see tapRefusal.
func TestEngineRefusesATapProfile(t *testing.T) {
	for _, tt := range []struct {
		name, lines string
		refused     bool
	}{
		{"dev tap", "dev tap\n", true},
		{"dev tap0", "dev tap0\n", true},
		{"a tap by its type", "dev vpn0\ndev-type tap\n", true},
		{"the type decides over the name", "dev tap0\ndev-type tun\n", false},
		{"dev tun", "dev tun\n", false},
		{"the last dev counts", "dev tap\ndev tun\n", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, harnessOpts{profile: "client\nremote 192.0.2.1 1194\n" + tt.lines + "<ca>\nPEM\n</ca>\n"})
			ctx, cancel := contextWithTimeout(10 * time.Second)
			defer cancel()
			err := h.eng.Start(ctx)
			if tt.refused {
				if err == nil || err.Error() != tapRefusal {
					t.Errorf("Start = %v, want %q", err, tapRefusal)
				}
				if h.readRecording("pid") != "" {
					t.Error("openvpn was started")
				}
				return
			}
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			h.waitFor("Up", h.stateIs(tunnel.StateUp))
			h.stop()
		})
	}
}

// The child of the engine is in a group of its own, and ends with the process
// that started it however that dies: otherwise a crash of the daemon would
// leave an openvpn behind with its tunnel, for the next start to find.
func TestChildDiesWithTheDaemon(t *testing.T) {
	dir := shortTempDir(t)
	child := writeStub(t, dir, "child", stubBehavior{Hang: true})
	parent := writeStub(t, dir, "parent", stubBehavior{GuardedChild: child})
	cmd := exec.Command(parent)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()

	var pid int
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read the child's pid: %v", err)
	}
	if _, err := fmt.Sscanf(line, "CHILD %d", &pid); err != nil {
		t.Fatalf("unexpected output %q", line)
	}
	if group, err := syscall.Getpgid(pid); err != nil || group != pid {
		t.Errorf("process group of the child = %d, %v; want its own, %d", group, err, pid)
	}

	if err := cmd.Process.Kill(); err != nil { // no chance to clean up
		t.Fatal(err)
	}
	cmd.Wait()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if !processRuns(pid) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the child %d outlived the process that started it", pid)
		}
	}
}

// processRuns reports whether pid is a process that has not exited: a zombie
// waiting for its parent has.
func processRuns(pid int) bool {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	// "pid (comm) S ...": the state follows the last parenthesis.
	i := strings.LastIndexByte(string(stat), ')')
	return i >= 0 && i+2 < len(stat) && stat[i+2] != 'Z'
}
