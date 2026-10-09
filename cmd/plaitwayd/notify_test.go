//go:build unix

package main

import (
	"context"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/transport"
)

// fakeSystemd is the notification socket of a service manager: it collects the
// datagrams the daemon sends.
type fakeSystemd struct {
	messages chan string
}

// listenNotify listens on a datagram socket at name, a path or an abstract name
// that starts with "@".
func listenNotify(t *testing.T, name string) *fakeSystemd {
	t.Helper()
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: name, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	systemd := &fakeSystemd{messages: make(chan string, 16)}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, _, err := conn.ReadFromUnix(buf)
			if err != nil {
				return
			}
			systemd.messages <- string(buf[:n])
		}
	}()
	return systemd
}

func (s *fakeSystemd) next(t *testing.T) string {
	t.Helper()
	select {
	case m := <-s.messages:
		return m
	case <-time.After(waitTimeout):
		t.Fatal("systemd was told nothing")
		return ""
	}
}

func (s *fakeSystemd) expectNothing(t *testing.T) {
	t.Helper()
	select {
	case m := <-s.messages:
		t.Fatalf("systemd was told %q", m)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSendNotificationReachesTheSocketAtAPath(t *testing.T) {
	path := filepath.Join(shortDir(t), "notify")
	systemd := listenNotify(t, path)
	if err := sendNotification(path, notifyReady); err != nil {
		t.Fatal(err)
	}
	if got := systemd.next(t); got != "READY=1" {
		t.Fatalf("systemd received %q, want READY=1", got)
	}
	if err := sendNotification(path, notifyStopping); err != nil {
		t.Fatal(err)
	}
	if got := systemd.next(t); got != "STOPPING=1" {
		t.Fatalf("systemd received %q, want STOPPING=1", got)
	}
}

func TestSendNotificationDoesNothingWithoutASocket(t *testing.T) {
	if err := sendNotification("", notifyReady); err != nil {
		t.Fatalf("sendNotification(\"\") = %v, want nothing to happen", err)
	}
}

func TestSendNotificationFailsForAnAddressThatIsNotASocket(t *testing.T) {
	for name, socket := range map[string]string{
		"relative path":   "notify",
		"vsock":           "vsock:2:1234",
		"nobody listens":  filepath.Join(shortDir(t), "gone"),
		"abstract absent": "@plaitway-test-nobody-listens",
	} {
		if err := sendNotification(socket, notifyReady); err == nil {
			t.Errorf("%s: no error for %q", name, socket)
		}
	}
}

// startNotifying runs the daemon on fake engines with notifySocket set, the way
// run does, and returns the client of its socket and what stops it.
func startNotifying(t *testing.T, notifySocket string, logOut *syncBuffer) (pb.DaemonServiceClient, func() error) {
	t.Helper()
	socket := newSocketPath(t)
	lis, err := transport.Listen(socket, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	who := everyone()
	log, daemonLog := newLogger(logOut, slog.LevelDebug)
	d, err := newDaemonWithCredentials(log, daemonLog, config{socket: socket, stateDir: t.TempDir(), fake: &fastFake}, who.pol, who.credentials())
	if err != nil {
		lis.Close()
		t.Fatal(err)
	}
	d.notifySocket = notifySocket
	conn, err := grpc.NewClient(transport.Target(socket),
		append(transport.DialOptions(), grpc.WithTransportCredentials(insecure.NewCredentials()))...)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- d.serve(ctx, lis) }()
	var (
		once    sync.Once
		stopErr error
	)
	stop := func() error {
		once.Do(func() {
			conn.Close()
			cancel()
			select {
			case stopErr = <-served:
			case <-time.After(waitTimeout):
				t.Error("the daemon did not stop in time")
			}
		})
		return stopErr
	}
	t.Cleanup(func() { stop() })
	return pb.NewDaemonServiceClient(conn), stop
}

// systemd starts what depends on the daemon at READY=1, so a client that comes
// then must be answered, and STOPPING=1 comes when the stop begins, before the
// daemon has finished.
func TestServeTellsSystemdWhenItIsReadyAndWhenItStops(t *testing.T) {
	path := filepath.Join(shortDir(t), "notify")
	systemd := listenNotify(t, path)
	var logOut syncBuffer
	client, stop := startNotifying(t, path, &logOut)

	if got := systemd.next(t); got != "READY=1" {
		t.Fatalf("first message %q, want READY=1", got)
	}
	if _, err := client.GetDaemonInfo(context.Background(), &pb.GetDaemonInfoRequest{}, grpc.WaitForReady(true)); err != nil {
		t.Fatalf("GetDaemonInfo after READY=1: %v", err)
	}
	systemd.expectNothing(t)

	if err := stop(); err != nil {
		t.Fatalf("serve returned %v", err)
	}
	if got := systemd.next(t); got != "STOPPING=1" {
		t.Fatalf("message at the stop %q, want STOPPING=1", got)
	}
	systemd.expectNothing(t)
	if strings.Contains(logOut.String(), "cannot notify") {
		t.Errorf("the daemon logged a failure:\n%s", logOut.String())
	}
}

func TestServeSaysNothingWithoutANotificationSocket(t *testing.T) {
	var logOut syncBuffer
	client, stop := startNotifying(t, "", &logOut)
	if _, err := client.GetDaemonInfo(context.Background(), &pb.GetDaemonInfoRequest{}, grpc.WaitForReady(true)); err != nil {
		t.Fatal(err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logOut.String(), "systemd") {
		t.Errorf("the daemon mentioned systemd:\n%s", logOut.String())
	}
}

// Nobody has to listen for the daemon to work.
func TestAnUnreachableNotificationSocketOnlyCostsAWarning(t *testing.T) {
	var logOut syncBuffer
	client, stop := startNotifying(t, filepath.Join(shortDir(t), "gone"), &logOut)
	if _, err := client.GetDaemonInfo(context.Background(), &pb.GetDaemonInfoRequest{}, grpc.WaitForReady(true)); err != nil {
		t.Fatal(err)
	}
	if err := stop(); err != nil {
		t.Fatalf("serve returned %v", err)
	}
	for _, want := range []string{"level=WARN", "cannot notify systemd", "READY=1", "STOPPING=1"} {
		if !strings.Contains(logOut.String(), want) {
			t.Errorf("the log lacks %q:\n%s", want, logOut.String())
		}
	}
}

// run takes the socket from the environment, as systemd sets it for Type=notify.
func TestRunNotifiesTheSocketOfTheEnvironment(t *testing.T) {
	path := filepath.Join(shortDir(t), "notify")
	systemd := listenNotify(t, path)
	t.Setenv("NOTIFY_SOCKET", path)
	dir := shortDir(t)
	cfg := config{socket: newSocketPath(t), socketMode: 0o600, stateDir: filepath.Join(dir, "state"), fake: &fastFake}

	_, stop := startRun(t, cfg, everyone().pol)
	if got := systemd.next(t); got != "READY=1" {
		t.Fatalf("first message %q, want READY=1", got)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if got := systemd.next(t); got != "STOPPING=1" {
		t.Fatalf("message at the stop %q, want STOPPING=1", got)
	}
}
