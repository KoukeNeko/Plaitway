package ovpn

import (
	"bufio"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestPeerIsProcessRecognisesTheOwnerOfTheOtherEnd(t *testing.T) {
	_, client := loopbackPair(t)
	if err := peerIsProcess(client, os.Getpid()); err != nil {
		t.Errorf("peerIsProcess(self) = %v, the listener is this process", err)
	}
}

func TestPeerIsProcessRefusesAnotherProcess(t *testing.T) {
	_, client := loopbackPair(t)
	other := os.Getpid() + 4
	err := peerIsProcess(client, other)
	if err == nil {
		t.Fatal("a connection to this process passed for another one")
	}
	if want := fmt.Sprintf("is owned by process %d, not by openvpn (process %d)", os.Getpid(), other); !strings.Contains(err.Error(), want) {
		t.Errorf("error = %v, want it to say %q", err, want)
	}
}

// The case the check exists for: a port held by another process. The stub
// listens, the test connects, and the table must name the stub and not the test.
func TestPeerIsProcessNamesTheListeningProcess(t *testing.T) {
	stub := writeStub(t, t.TempDir(), "listener", stubBehavior{Listen: true})
	cmd := exec.Command(stub)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var port int
	if _, err := fmt.Sscanf(line, "PORT %d", &port); err != nil {
		t.Fatalf("stub said %q", line)
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if err := peerIsProcess(conn, cmd.Process.Pid); err != nil {
		t.Errorf("the listener is process %d: %v", cmd.Process.Pid, err)
	}
	if err := peerIsProcess(conn, os.Getpid()); err == nil {
		t.Error("a connection to another process passed for this one")
	}
}

func TestPeerIsProcessRefusesWhatIsNotALoopbackConnection(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if err := peerIsProcess(a, os.Getpid()); err == nil {
		t.Error("a pipe passed")
	}
}

func TestPortOfReadsNetworkByteOrder(t *testing.T) {
	// 0xC0E8 = 49384 sent as the bytes E8 C0 in a word read little-endian.
	if got := portOf(0x0000e8c0); got != 0xc0e8 {
		t.Errorf("portOf = %#x", got)
	}
	if got := ipv4Word(netipAddr("127.0.0.1")); got != 0x0100007f {
		t.Errorf("ipv4Word = %#x, want the bytes 7f 00 00 01 in memory order", got)
	}
}

func netipAddr(s string) netip.Addr { return netip.MustParseAddr(s) }
