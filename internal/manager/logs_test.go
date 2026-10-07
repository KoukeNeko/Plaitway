package manager_test

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/manager"
	"github.com/KoukeNeko/Plaitway/internal/profile"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

func texts(lines []*pb.LogLine) []string {
	var out []string
	for _, l := range lines {
		out = append(out, l.Text)
	}
	return out
}

func TestLogBufferKeepsTheNewestThousandLines(t *testing.T) {
	b := manager.NewLogBuffer("p1")
	for i := range 1500 {
		b.Add(pb.LogLevel_LOG_LEVEL_INFO, fmt.Sprintf("line %d", i))
	}

	all, _, cancel := b.Subscribe(5000)
	defer cancel()
	if len(all) != manager.LogCapacity || all[0].Text != "line 500" || all[len(all)-1].Text != "line 1499" {
		t.Fatalf("tail of everything: %d lines from %q to %q", len(all), all[0].Text, all[len(all)-1].Text)
	}
	tail, _, cancel2 := b.Subscribe(3)
	defer cancel2()
	if got := texts(tail); strings.Join(got, ",") != "line 1497,line 1498,line 1499" {
		t.Fatalf("tail of 3 = %v", got)
	}
	if tail[0].ProfileId != "p1" || tail[0].Time == nil || tail[0].Level != pb.LogLevel_LOG_LEVEL_INFO {
		t.Fatalf("line = %v", tail[0])
	}
	none, _, cancel3 := b.Subscribe(0)
	defer cancel3()
	if len(none) != 0 {
		t.Fatalf("tail of 0 returned %d lines", len(none))
	}
}

func TestLogBufferTailThenLiveWithoutGapOrRepeat(t *testing.T) {
	b := manager.NewLogBuffer("")
	b.Add(pb.LogLevel_LOG_LEVEL_INFO, "old 1")
	b.Add(pb.LogLevel_LOG_LEVEL_INFO, "old 2")
	tail, live, cancel := b.Subscribe(10)
	defer cancel()
	b.Add(pb.LogLevel_LOG_LEVEL_WARN, "new 1")

	if got := texts(tail); strings.Join(got, ",") != "old 1,old 2" {
		t.Fatalf("tail = %v", got)
	}
	if l := <-live; l.Text != "new 1" || l.Level != pb.LogLevel_LOG_LEVEL_WARN {
		t.Fatalf("live line = %v", l)
	}
}

func TestLogBufferDropsASlowSubscriberOnly(t *testing.T) {
	b := manager.NewLogBuffer("")
	_, slow, _ := b.Subscribe(0)
	_, healthy, cancel := b.Subscribe(0)
	defer cancel()

	received := 0
	for i := range 1000 {
		b.Add(pb.LogLevel_LOG_LEVEL_INFO, fmt.Sprint(i))
		<-healthy
		received++
	}
	n := 0
	for range slow {
		n++
	}
	if n == 0 || n >= 1000 {
		t.Fatalf("the slow subscriber got %d lines before it was dropped", n)
	}
	if received != 1000 {
		t.Fatalf("the healthy subscriber got %d lines", received)
	}
}

func TestLogBufferCancelAndClose(t *testing.T) {
	b := manager.NewLogBuffer("")
	_, live, cancel := b.Subscribe(0)
	cancel()
	b.Add(pb.LogLevel_LOG_LEVEL_INFO, "after cancel")
	select {
	case l := <-live:
		t.Fatalf("a cancelled subscriber received %v", l)
	default:
	}

	_, live2, cancel2 := b.Subscribe(0)
	defer cancel2()
	b.Close()
	if _, ok := <-live2; ok {
		t.Fatal("the channel of a closed buffer delivered a line")
	}
	_, live3, _ := b.Subscribe(0)
	if _, ok := <-live3; ok {
		t.Fatal("subscribing to a closed buffer returned an open channel")
	}
	b.Add(pb.LogLevel_LOG_LEVEL_INFO, "ignored") // must not panic
}

func TestLogTextIsMadeSafeForProtobuf(t *testing.T) {
	b := manager.NewLogBuffer("")
	b.Add(pb.LogLevel_LOG_LEVEL_INFO, "bad \xff\xfe bytes")
	b.Add(pb.LogLevel_LOG_LEVEL_INFO, strings.Repeat("x", 100_000))
	lines, _, cancel := b.Subscribe(2)
	defer cancel()
	if !utf8.ValidString(lines[0].Text) {
		t.Errorf("invalid UTF-8 kept: %q", lines[0].Text)
	}
	if len(lines[1].Text) > 8192 {
		t.Errorf("a line of %d bytes was kept", len(lines[1].Text))
	}
}

func TestHandlerRecordsLinesAndPassesThem(t *testing.T) {
	var out bytes.Buffer
	buf := manager.NewLogBuffer("")
	log := slog.New(buf.Handler(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelInfo})))

	log.Debug("not enabled")
	log.Info("listening", "socket", "/tmp/s", "pid", 42)
	log.With("profile", "p1").WithGroup("g").Warn("careful", "n", 1)
	log.Error("broken", "err", errors.New("boom"))

	lines, _, cancel := buf.Subscribe(10)
	defer cancel()
	want := []string{
		"listening socket=/tmp/s pid=42",
		"careful profile=p1 g.n=1",
		"broken err=boom",
	}
	if got := texts(lines); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("recorded %q, want %q", got, want)
	}
	levels := []pb.LogLevel{lines[0].Level, lines[1].Level, lines[2].Level}
	if levels[0] != pb.LogLevel_LOG_LEVEL_INFO || levels[1] != pb.LogLevel_LOG_LEVEL_WARN || levels[2] != pb.LogLevel_LOG_LEVEL_ERROR {
		t.Fatalf("levels = %v", levels)
	}
	if !strings.Contains(out.String(), "msg=listening") || strings.Contains(out.String(), "not enabled") {
		t.Fatalf("the wrapped handler did not get the lines: %q", out.String())
	}
}

func TestProfileLogTailAndLive(t *testing.T) {
	var engine *stubEngine
	stub := &stubBackend{kind: tunnel.KindOpenVPN, onEngine: func(s *stubEngine) { engine = s }}
	e := newEnv(t, withBackends(stub.backend()))
	p := e.importProfile("a", ovpnProfile)
	e.setEnabled(p.Id, true)

	engine.deps.Log(tunnel.LogInfo, "TLS handshake")
	engine.deps.Log(tunnel.LogError, "AUTH_FAILED")
	lines, live, cancel, err := e.m.Logs(p.Id, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	got := texts(lines)
	if len(got) < 3 || got[len(got)-2] != "TLS handshake" || got[len(got)-1] != "AUTH_FAILED" {
		t.Fatalf("tail = %v, want the engine's lines at the end", got)
	}
	if last := lines[len(lines)-1]; last.Level != pb.LogLevel_LOG_LEVEL_ERROR || last.ProfileId != p.Id {
		t.Fatalf("line = %v", last)
	}

	engine.deps.Log(tunnel.LogDebug, "live line")
	if l := <-live; l.Text != "live line" || l.Level != pb.LogLevel_LOG_LEVEL_DEBUG {
		t.Fatalf("live = %v", l)
	}
}

func TestDaemonLogIsServedWithAnEmptyProfileID(t *testing.T) {
	daemonLog := manager.NewLogBuffer("")
	daemonLog.Add(pb.LogLevel_LOG_LEVEL_INFO, "daemon started")
	e := newEnv(t, withDaemonLog(daemonLog))

	lines, _, cancel, err := e.m.Logs("", 10)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if got := texts(lines); len(got) != 1 || got[0] != "daemon started" {
		t.Fatalf("daemon log = %v", got)
	}
}

func TestLogsOfAnUnknownProfileAreNotFound(t *testing.T) {
	e := newEnv(t)
	if _, _, _, err := e.m.Logs("nope", 10); !errors.Is(err, profile.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestDeletingAProfileEndsItsLogStream(t *testing.T) {
	e := newEnv(t)
	p := e.importProfile("a", ovpnProfile)
	_, live, cancel, err := e.m.Logs(p.Id, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if err := e.m.Delete(p.Id); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the log stream to end", func() bool {
		select {
		case _, ok := <-live:
			return !ok
		default:
			return false
		}
	})
}
