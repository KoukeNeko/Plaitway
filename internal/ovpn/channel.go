package ovpn

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// mgmtChannel is how one engine reaches the management interface of its
// openvpn: the options that open the interface, and the connection to it. The
// interface is a Unix socket where the OS has a good one, and a password
// protected TCP port on loopback where openvpn has no other (Windows).
type mgmtChannel interface {
	// prepare creates, in the engine's private directory, what options refer to.
	prepare() error
	// options are the openvpn options that open the interface.
	options() []string
	// secrets are what must never reach a log: openvpn echoes a line it does not
	// understand, and the password has been one.
	secrets() []string
	// output sees every line the child prints; openvpn announces there where it
	// listens when the system chose.
	output(line string)
	// dial connects once openvpn listens. pid is the child's process id; abort is
	// closed when the child has exited.
	dial(ctx context.Context, abort <-chan struct{}, pid int) (*mgmtConn, error)
}

// --- Unix socket ---

// socketFileName is the management socket in the engine's directory. The
// longest path openvpn can listen on is maxSocketPath (platform_*.go).
const socketFileName = "m.sock"

// unixSocketPath names the management socket in the engine's directory.
func unixSocketPath(dir string) (string, error) {
	socket := filepath.Join(dir, socketFileName)
	if len(socket) > maxSocketPath {
		return "", fmt.Errorf("management socket path %q is too long (%d bytes, at most %d)", socket, len(socket), maxSocketPath)
	}
	return socket, nil
}

// unixManagementOptions is the command line that makes openvpn listen on a
// Unix socket.
func unixManagementOptions(socket string) []string {
	return []string{"--management", socket, "unix"}
}

// unixChannel is the management interface on a Unix socket. openvpn creates
// the socket world-accessible, so the engine's directory (mode 0700) is what
// keeps other users away from it.
type unixChannel struct{ socket string }

func newUnixChannel(dir string) (*unixChannel, error) {
	socket, err := unixSocketPath(dir)
	if err != nil {
		return nil, err
	}
	return &unixChannel{socket: socket}, nil
}

func (c *unixChannel) prepare() error { return nil }

func (c *unixChannel) options() []string { return unixManagementOptions(c.socket) }

func (c *unixChannel) secrets() []string { return nil }

func (c *unixChannel) output(string) {}

func (c *unixChannel) dial(ctx context.Context, abort <-chan struct{}, _ int) (*mgmtConn, error) {
	return dialMgmt(ctx, c.socket, abort)
}

// --- loopback TCP ---

const (
	passwordFileName = "mgmt.pw"
	// passwordBytes of randomness make the password; it is the only access
	// control of a port every local user can connect to.
	passwordBytes = 32
	loopbackHost  = "127.0.0.1"
	// systemChosenPort asks openvpn for any free port, which it then reports.
	systemChosenPort = "0"

	managementPrompt  = "ENTER PASSWORD:"
	managementAccept  = "SUCCESS: password is correct"
	maxHandshakeBytes = 256
)

// handshakeTimeout bounds the exchange of the password. It is a variable for the
// tests.
var handshakeTimeout = 5 * time.Second

// listeningLine is how openvpn says where its management interface listens.
var listeningLine = regexp.MustCompile(`MANAGEMENT: TCP Socket listening on \[AF_INET\]` + regexp.QuoteMeta(loopbackHost) + `:(\d+)\s*$`)

// peerCheck confirms that the process behind the connection is the child.
type peerCheck func(conn net.Conn, pid int) error

// tcpChannel is the management interface on a loopback TCP port. Every local
// user can connect to the port, so three things protect it:
//
//   - the password, 256 random bits in a file of the engine's private
//     directory, which openvpn demands before it says anything;
//   - the port is chosen by the system and learned from openvpn's output, so no
//     other process can be given the number beforehand;
//   - before the password is sent, verify (when set) confirms that the process
//     that owns the other end of the connection is the child. Without it a
//     process that grabbed the port first would receive the password and could
//     feed the engine routes and DNS servers as if openvpn had pushed them.
//
// What it cannot stop is another local user connecting first, which keeps the
// engine out of the interface until openvpn is stopped: the engine then fails
// with the error of the missing password prompt, and nothing is leaked.
type tcpChannel struct {
	dir      string
	verify   peerCheck
	password string

	once  sync.Once
	ready chan struct{} // closed when the port is known
	port  string
}

func newTCPChannel(dir string, verify peerCheck) *tcpChannel {
	return &tcpChannel{dir: dir, verify: verify, ready: make(chan struct{})}
}

func (c *tcpChannel) passwordPath() string { return filepath.Join(c.dir, passwordFileName) }

func (c *tcpChannel) prepare() error {
	secret := make([]byte, passwordBytes)
	if _, err := rand.Read(secret); err != nil {
		return fmt.Errorf("make the management password: %w", err)
	}
	c.password = hex.EncodeToString(secret)
	f, err := os.OpenFile(c.passwordPath(), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create the management password file: %w", err)
	}
	if _, err := f.WriteString(c.password); err != nil {
		f.Close()
		return fmt.Errorf("write the management password file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write the management password file: %w", err)
	}
	return nil
}

func (c *tcpChannel) options() []string {
	return []string{"--management", loopbackHost, systemChosenPort, c.passwordPath()}
}

func (c *tcpChannel) secrets() []string { return []string{c.password} }

func (c *tcpChannel) output(line string) {
	m := listeningLine.FindStringSubmatch(line)
	if m == nil {
		return
	}
	c.once.Do(func() {
		c.port = m[1]
		close(c.ready)
	})
}

func (c *tcpChannel) dial(ctx context.Context, abort <-chan struct{}, pid int) (*mgmtConn, error) {
	select {
	case <-c.ready:
	case <-ctx.Done():
		return nil, fmt.Errorf("openvpn did not report its management port: %w", ctx.Err())
	case <-abort:
		return nil, errors.New("openvpn exited before its management port was ready")
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(loopbackHost, c.port))
	if err != nil {
		return nil, fmt.Errorf("connect to the management port: %w", err)
	}
	if err := c.authenticate(conn, pid); err != nil {
		conn.Close()
		return nil, err
	}
	// The reply to the password is a reply like any other: the first command waits
	// the gap too.
	return &mgmtConn{conn: conn, gap: mgmtCommandGap, replyTimeout: mgmtReplyTimeout, repliedAt: time.Now()}, nil
}

// authenticate checks who answers, then answers the password prompt. It reads
// the connection byte by byte: the prompt has no line break, and a buffer
// would swallow the notifications that follow the reply.
func (c *tcpChannel) authenticate(conn net.Conn, pid int) error {
	if c.verify != nil {
		if err := c.verify(conn, pid); err != nil {
			return fmt.Errorf("the management port is not openvpn's: %w", err)
		}
	}
	if err := conn.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return fmt.Errorf("management password: %w", err)
	}
	defer conn.SetDeadline(time.Time{})

	if _, err := readUntil(conn, func(read string) bool { return strings.HasSuffix(read, managementPrompt) }); err != nil {
		return fmt.Errorf("wait for the management password prompt: %w", err)
	}
	if _, err := conn.Write([]byte(c.password + "\n")); err != nil {
		return fmt.Errorf("send the management password: %w", err)
	}
	reply, err := readUntil(conn, func(read string) bool { return strings.HasSuffix(read, "\n") })
	if err != nil {
		return fmt.Errorf("wait for the answer to the management password: %w", err)
	}
	if strings.TrimSpace(reply) != managementAccept {
		return fmt.Errorf("openvpn refused the management password: %s", truncate(strings.TrimSpace(reply), 80))
	}
	return nil
}

// readUntil reads one byte at a time until done accepts what was read.
func readUntil(conn net.Conn, done func(read string) bool) (string, error) {
	var read []byte
	one := make([]byte, 1)
	for !done(string(read)) {
		if len(read) >= maxHandshakeBytes {
			return "", fmt.Errorf("unexpected data %q", truncate(string(read), 40))
		}
		if _, err := conn.Read(one); err != nil {
			return "", err
		}
		read = append(read, one[0])
	}
	return string(read), nil
}
