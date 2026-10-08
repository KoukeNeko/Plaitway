package ovpn

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// managementServer stands in for the management interface of openvpn: it
// listens on a port the system chose, asks for the password on connect, and
// records what it was sent.
type managementServer struct {
	ln       net.Listener
	password string

	mu             sync.Mutex
	received       []string
	acceptedAt     time.Time // when the reply to the password was sent
	firstCommandAt time.Time // when the first line after it arrived
}

func newManagementServer(t *testing.T, password string) *managementServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &managementServer{ln: ln, password: password}
	t.Cleanup(func() { ln.Close() })
	go s.serve()
	return s
}

func (s *managementServer) announcement() string {
	return fmt.Sprintf("2026-10-07 12:00:00 MANAGEMENT: TCP Socket listening on [AF_INET]%s", s.ln.Addr())
}

func (s *managementServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.talk(conn)
	}
}

func (s *managementServer) talk(conn net.Conn) {
	defer conn.Close()
	fmt.Fprint(conn, "ENTER PASSWORD:")
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return
	}
	s.mu.Lock()
	s.received = append(s.received, strings.TrimSpace(line))
	s.mu.Unlock()
	if strings.TrimSpace(line) != s.password {
		fmt.Fprint(conn, "ERROR: bad password\r\n")
		return
	}
	fmt.Fprint(conn, "SUCCESS: password is correct\r\n>INFO:OpenVPN Management Interface Version 5\r\n")
	s.mu.Lock()
	s.acceptedAt = time.Now()
	s.mu.Unlock()
	conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond)) // the connection stays up while the test reads
	if _, err := bufio.NewReader(conn).ReadString('\n'); err == nil {
		s.mu.Lock()
		s.firstCommandAt = time.Now()
		s.mu.Unlock()
	}
}

func (s *managementServer) passwordsSeen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.received...)
}

func readyChannel(t *testing.T, verify peerCheck) *tcpChannel {
	t.Helper()
	c := newTCPChannel(shortTempDir(t), verify)
	if err := c.prepare(); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTCPChannelOptions(t *testing.T) {
	c := readyChannel(t, nil)
	want := []string{"--management", "127.0.0.1", "0", filepath.Join(c.dir, passwordFileName)}
	got := c.options()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("options = %q, want %q", got, want)
	}
}

func TestTCPChannelPasswordFile(t *testing.T) {
	c := readyChannel(t, nil)
	stored, err := os.ReadFile(c.passwordPath())
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).Match(stored) {
		t.Errorf("password file = %q, want 256 bits in hex and nothing else", stored)
	}
	other := readyChannel(t, nil)
	otherStored, _ := os.ReadFile(other.passwordPath())
	if string(stored) == string(otherStored) {
		t.Error("two channels got the same password")
	}
	if err := c.prepare(); err == nil {
		t.Error("the password file was written over; it must be created, never replaced")
	}
}

func TestTCPChannelConnects(t *testing.T) {
	c := readyChannel(t, nil)
	server := newManagementServer(t, c.password)
	c.output("unrelated line")
	c.output(server.announcement())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := c.dial(ctx, nil, 1234)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// What openvpn sends after the password reaches the reader intact.
	var first mgmtEvent
	conn.readLoop(func(ev mgmtEvent) bool { first = ev; return false })
	if info, ok := first.(infoEvent); !ok || !strings.Contains(info.Msg, "Management Interface") {
		t.Errorf("first event = %#v", first)
	}
	if seen := server.passwordsSeen(); len(seen) != 1 || seen[0] != c.password {
		t.Errorf("the server saw %q", seen)
	}
	if conn.gap == 0 {
		t.Error("the connection to openvpn for Windows sends commands in a pipeline; they get lost")
	}
}

func TestTCPChannelLearnsOnlyTheFirstAnnouncedPort(t *testing.T) {
	c := readyChannel(t, nil)
	c.output("2026-10-07 12:00:00 MANAGEMENT: TCP Socket listening on [AF_INET]127.0.0.1:41000")
	c.output("2026-10-07 12:00:01 MANAGEMENT: TCP Socket listening on [AF_INET]127.0.0.1:42000")
	if c.port != "41000" {
		t.Errorf("port = %q, want the first announcement", c.port)
	}
	for _, line := range []string{
		"MANAGEMENT: TCP Socket listening on [AF_INET6]::1:43000",
		"MANAGEMENT: TCP Socket listening on [AF_INET]10.0.0.1:43000",
		"MANAGEMENT: TCP Socket listening on [AF_INET]127.0.0.1:",
	} {
		other := readyChannel(t, nil)
		other.output(line)
		select {
		case <-other.ready:
			t.Errorf("%q was taken for the announcement of the interface", line)
		default:
		}
	}
}

func TestTCPChannelRefusesAPeerThatIsNotOpenVPN(t *testing.T) {
	var asked struct {
		sync.Mutex
		pid int
	}
	verify := func(conn net.Conn, pid int) error {
		asked.Lock()
		asked.pid = pid
		asked.Unlock()
		return errors.New("owned by someone else")
	}
	c := readyChannel(t, verify)
	server := newManagementServer(t, c.password)
	c.output(server.announcement())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.dial(ctx, nil, 4321)
	if err == nil || !strings.Contains(err.Error(), "owned by someone else") {
		t.Fatalf("dial = %v, want the refusal of the peer check", err)
	}
	asked.Lock()
	defer asked.Unlock()
	if asked.pid != 4321 {
		t.Errorf("the peer check was asked about process %d, want the child's, 4321", asked.pid)
	}
	time.Sleep(100 * time.Millisecond)
	if seen := server.passwordsSeen(); len(seen) != 0 {
		t.Errorf("the password was sent to a peer that failed the check: %q", seen)
	}
}

func TestTCPChannelWrongPassword(t *testing.T) {
	c := readyChannel(t, nil)
	server := newManagementServer(t, "another password")
	c.output(server.announcement())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.dial(ctx, nil, 1); err == nil || !strings.Contains(err.Error(), "refused the management password") {
		t.Fatalf("dial = %v", err)
	}
}

func TestTCPChannelGivesUp(t *testing.T) {
	t.Run("openvpn never says where it listens", func(t *testing.T) {
		c := readyChannel(t, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_, err := c.dial(ctx, nil, 1)
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "management port") {
			t.Fatalf("dial = %v", err)
		}
	})
	t.Run("the child exited", func(t *testing.T) {
		c := readyChannel(t, nil)
		abort := make(chan struct{})
		close(abort)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		start := time.Now()
		if _, err := c.dial(ctx, abort, 1); err == nil || !strings.Contains(err.Error(), "exited") {
			t.Fatalf("dial = %v", err)
		}
		if time.Since(start) > 2*time.Second {
			t.Error("dial kept waiting after the child exited")
		}
	})
	t.Run("a port nobody listens on", func(t *testing.T) {
		c := readyChannel(t, nil)
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		c.output(fmt.Sprintf("MANAGEMENT: TCP Socket listening on [AF_INET]127.0.0.1:%d", port))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := c.dial(ctx, nil, 1); err == nil {
			t.Fatal("dial succeeded")
		}
	})
	t.Run("a peer that never asks for the password", func(t *testing.T) {
		c := readyChannel(t, nil)
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		go func() {
			conn, err := ln.Accept()
			if err == nil {
				defer conn.Close()
				time.Sleep(2 * time.Second)
			}
		}()
		c.output(fmt.Sprintf("MANAGEMENT: TCP Socket listening on [AF_INET]%s", ln.Addr()))
		defer func(old time.Duration) { handshakeTimeout = old }(handshakeTimeout)
		handshakeTimeout = 200 * time.Millisecond
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := c.dial(ctx, nil, 1); err == nil || !strings.Contains(err.Error(), "password prompt") {
			t.Fatalf("dial = %v", err)
		}
	})
}

func TestUnixChannelKeepsItsSocketPathAndOptions(t *testing.T) {
	dir := shortTempDir(t)
	c, err := newUnixChannel(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.prepare(); err != nil {
		t.Fatal(err)
	}
	want := []string{"--management", filepath.Join(dir, socketFileName), "unix"}
	if got := c.options(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("options = %q, want %q", got, want)
	}
	if _, err := newUnixChannel(filepath.Join(dir, strings.Repeat("d", 120))); err == nil {
		t.Error("a socket path longer than sun_path was accepted")
	}
}

// The reply to the password is a reply like any other for openvpn for Windows:
// a command that follows it at once may be lost, so the first one waits too.
func TestTCPChannelFirstCommandWaitsTheGapAfterThePassword(t *testing.T) {
	c := readyChannel(t, nil)
	server := newManagementServer(t, c.password)
	c.output(server.announcement())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := c.dial(ctx, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	pump(conn)
	if err := conn.Send("state", "state on"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		server.mu.Lock()
		arrived, accepted := server.firstCommandAt, server.acceptedAt
		server.mu.Unlock()
		if !arrived.IsZero() {
			if wait := arrived.Sub(accepted); wait < mgmtCommandGap-10*time.Millisecond {
				t.Errorf("the first command came %v after the reply to the password, want at least %v", wait, mgmtCommandGap)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the server never saw the command")
		}
	}
}
