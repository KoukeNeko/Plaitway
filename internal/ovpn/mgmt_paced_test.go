package ovpn

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// lossyServer plays openvpn for Windows as far as the pipeline goes: it answers
// every command with one line, and drops, without a word, a command that
// arrives less than tooSoon after its last reply. That is what the real binary
// does (see mgmtConn), and the tests of the paced connection need it to be able
// to fail.
type lossyServer struct {
	ln      net.Listener
	tooSoon time.Duration

	mu       sync.Mutex
	arrived  []string
	answered []string
	dropped  []string
	inFlight int // commands received that no reply has followed yet
	overlap  bool
	lastSent time.Time
	hangOn   string // a command that is never answered
	errorOn  string // a command that gets ERROR instead of SUCCESS
}

func newLossyServer(t *testing.T, tooSoon time.Duration) (*lossyServer, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &lossyServer{ln: ln, tooSoon: tooSoon}
	t.Cleanup(func() { ln.Close() })
	go s.serve()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return s, conn
}

func (s *lossyServer) serve() {
	conn, err := s.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		command := sc.Text()
		s.mu.Lock()
		s.arrived = append(s.arrived, command)
		s.inFlight++
		if s.inFlight > 1 {
			s.overlap = true
		}
		lost := !s.lastSent.IsZero() && time.Since(s.lastSent) < s.tooSoon
		hang := command == s.hangOn
		reply := "SUCCESS: " + command
		if command == s.errorOn {
			reply = "ERROR: refused " + command
		}
		switch {
		case lost:
			s.dropped = append(s.dropped, command)
			s.inFlight--
		case hang:
			s.inFlight--
		default:
			s.answered = append(s.answered, command)
		}
		s.mu.Unlock()
		if lost || hang {
			continue
		}
		time.Sleep(2 * time.Millisecond) // openvpn takes a moment to write its reply
		s.mu.Lock()
		s.inFlight--
		s.lastSent = time.Now()
		s.mu.Unlock()
		fmt.Fprintf(conn, "%s\r\n", reply)
	}
}

func (s *lossyServer) snapshot() (answered, dropped []string, overlap bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.answered...), append([]string(nil), s.dropped...), s.overlap
}

// pump reads the connection as the engine does, and collects the errors that
// answer commands.
func pump(c *mgmtConn) (events chan mgmtEvent, done chan error) {
	events = make(chan mgmtEvent, 64)
	done = make(chan error, 1)
	go func() {
		done <- c.readLoop(func(ev mgmtEvent) bool { events <- ev; return true })
		close(events)
	}()
	return events, done
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

var pacedCommands = []string{"state on", "bytecount 2", "log on", "hold release"}

func TestPipelineLosesCommandsOnAServerThatDropsTheOnesThatFollowTooClosely(t *testing.T) {
	// The control of the next test: without a gap the same server loses commands.
	server, conn := newLossyServer(t, 20*time.Millisecond)
	c := &mgmtConn{conn: conn}
	defer c.Close()
	pump(c)
	for _, command := range pacedCommands {
		if err := c.Send(command, command); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	waitFor(t, "the server to see everything", func() bool {
		answered, dropped, _ := server.snapshot()
		return len(answered)+len(dropped) == len(pacedCommands)
	})
	if _, dropped, _ := server.snapshot(); len(dropped) == 0 {
		t.Fatal("the server dropped nothing; the paced test below would pass whatever it does")
	}
}

func TestPacedConnectionKeepsOneCommandInFlightAndLosesNone(t *testing.T) {
	server, conn := newLossyServer(t, 20*time.Millisecond)
	c := &mgmtConn{conn: conn, gap: 30 * time.Millisecond, replyTimeout: 5 * time.Second}
	defer c.Close()
	pump(c)

	start := time.Now()
	for _, command := range pacedCommands {
		if err := c.Send(command, command); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d > 20*time.Millisecond {
		t.Errorf("Send waited %v; it must queue and return", d)
	}
	waitFor(t, "every command to be answered", func() bool {
		answered, _, _ := server.snapshot()
		return len(answered) == len(pacedCommands)
	})
	answered, dropped, overlap := server.snapshot()
	if len(dropped) != 0 || overlap {
		t.Errorf("dropped %q, overlap %v: commands followed a reply too closely, or each other", dropped, overlap)
	}
	for i, command := range pacedCommands {
		if answered[i] != command {
			t.Errorf("answered[%d] = %q, want %q: the order changed", i, answered[i], command)
		}
	}
	if d := time.Since(start); d < time.Duration(len(pacedCommands)-1)*30*time.Millisecond {
		t.Errorf("four commands took %v; the gap was not kept", d)
	}
}

func TestPacedConnectionMatchesAnErrorToItsCommand(t *testing.T) {
	server, conn := newLossyServer(t, 0)
	server.errorOn = "bytecount 2"
	c := &mgmtConn{conn: conn, gap: 5 * time.Millisecond, replyTimeout: 5 * time.Second}
	defer c.Close()
	events, _ := pump(c)
	for _, command := range pacedCommands {
		if err := c.Send(strings.Fields(command)[0], command); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case ev := <-events:
		if got, ok := ev.(commandError); !ok || got.Cmd != "bytecount" || !strings.Contains(got.Msg, "refused") {
			t.Fatalf("event = %#v, want the error of bytecount", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no error event")
	}
	waitFor(t, "the commands after the refused one", func() bool {
		answered, _, _ := server.snapshot()
		return len(answered) == len(pacedCommands)
	})
}

func TestPacedConnectionGivesUpOnACommandNobodyAnswers(t *testing.T) {
	server, conn := newLossyServer(t, 0)
	server.hangOn = "log on"
	c := &mgmtConn{conn: conn, gap: 5 * time.Millisecond, replyTimeout: 150 * time.Millisecond}
	defer c.Close()
	_, done := pump(c)
	for _, command := range pacedCommands {
		if err := c.Send(command, command); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), `did not answer "log on"`) {
			t.Fatalf("readLoop = %v, want the reason the connection was closed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the connection stayed open although a command was never answered")
	}
	if why := c.whyClosed(); why == nil {
		t.Error("whyClosed is empty")
	}
	if err := c.Send("late", "late"); !errors.Is(err, errMgmtWrite) {
		t.Errorf("Send after the failure = %v, want a closed connection", err)
	}
	_, dropped, _ := server.snapshot()
	if len(dropped) != 0 {
		t.Errorf("dropped %q", dropped)
	}
}

func TestPacedConnectionWritesNothingAfterClose(t *testing.T) {
	server, conn := newLossyServer(t, 0)
	c := &mgmtConn{conn: conn, gap: 100 * time.Millisecond, replyTimeout: 5 * time.Second}
	pump(c)
	for _, command := range pacedCommands {
		if err := c.Send(command, command); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "the first command to be answered", func() bool {
		answered, _, _ := server.snapshot()
		return len(answered) >= 1
	})
	c.Close()
	time.Sleep(400 * time.Millisecond)
	if answered, _, _ := server.snapshot(); len(answered) > 2 {
		t.Errorf("%q were sent after Close", answered)
	}
}
