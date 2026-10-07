package manager

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

const (
	// LogCapacity is how many lines a LogBuffer keeps.
	LogCapacity  = 1000
	logSubBuffer = 256
	// maxLogText bounds one line: engines forward text from child processes.
	maxLogText = 4096
)

// LogBuffer keeps the most recent log lines of one source (a profile, or the
// daemon itself) and streams new ones to watchers. It is safe for concurrent
// use and never blocks the writer.
type LogBuffer struct {
	profileID string

	mu     sync.Mutex
	lines  [LogCapacity]*pb.LogLine
	next   int // index the next line goes to
	count  int
	subs   map[chan *pb.LogLine]struct{}
	closed bool
}

// NewLogBuffer returns a buffer whose lines carry profileID; the empty id is
// the daemon's own log.
func NewLogBuffer(profileID string) *LogBuffer {
	return &LogBuffer{profileID: profileID, subs: map[chan *pb.LogLine]struct{}{}}
}

func (b *LogBuffer) Add(level pb.LogLevel, text string) {
	text = sanitizeText(text, maxLogText)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	line := &pb.LogLine{Time: timestamppb.Now(), Level: level, ProfileId: b.profileID, Text: text}
	b.lines[b.next] = line
	b.next = (b.next + 1) % LogCapacity
	b.count = min(b.count+1, LogCapacity)
	for ch := range b.subs {
		select {
		case ch <- line:
		default: // too slow: dropped, its channel closes
			delete(b.subs, ch)
			close(ch)
		}
	}
}

// Subscribe returns the last tail lines and a channel with every line added
// after them. The channel is closed when the watcher falls too far behind or
// the buffer is closed; cancel releases the subscription.
func (b *LogBuffer) Subscribe(tail int) (lines []*pb.LogLine, live <-chan *pb.LogLine, cancel func()) {
	ch := make(chan *pb.LogLine, logSubBuffer)
	b.mu.Lock()
	defer b.mu.Unlock()
	tail = max(0, min(tail, b.count))
	for i := b.count - tail; i < b.count; i++ {
		lines = append(lines, b.lines[(b.next-b.count+i+LogCapacity)%LogCapacity])
	}
	if b.closed {
		close(ch)
	} else {
		b.subs[ch] = struct{}{}
	}
	return lines, ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.subs, ch)
	}
}

// Close ends every subscription; later lines are discarded.
func (b *LogBuffer) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for ch := range b.subs {
		close(ch)
	}
	clear(b.subs)
}

func (b *LogBuffer) subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// Handler returns a slog.Handler that records every line it handles in the
// buffer, formatted as "message key=value ...", and then passes it to next.
func (b *LogBuffer) Handler(next slog.Handler) slog.Handler {
	return &bufferHandler{next: next, buffer: b}
}

type bufferHandler struct {
	next   slog.Handler
	buffer *LogBuffer
	group  string   // prefix of keys added from here on, "a.b."
	attrs  []string // "key=value" of attributes attached with WithAttrs
}

func (h *bufferHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *bufferHandler) Handle(ctx context.Context, r slog.Record) error {
	var text strings.Builder
	text.WriteString(r.Message)
	for _, a := range h.attrs {
		text.WriteString(" " + a)
	}
	r.Attrs(func(a slog.Attr) bool {
		text.WriteString(" " + h.format(a))
		return true
	})
	h.buffer.Add(slogLevelToProto(r.Level), text.String())
	return h.next.Handle(ctx, r)
}

func (h *bufferHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.next = h.next.WithAttrs(attrs)
	clone.attrs = append([]string(nil), h.attrs...)
	for _, a := range attrs {
		clone.attrs = append(clone.attrs, h.format(a))
	}
	return &clone
}

func (h *bufferHandler) WithGroup(name string) slog.Handler {
	clone := *h
	clone.next = h.next.WithGroup(name)
	clone.group = h.group + name + "."
	return &clone
}

func (h *bufferHandler) format(a slog.Attr) string {
	return fmt.Sprintf("%s%s=%v", h.group, a.Key, a.Value.Resolve())
}

func slogLevelToProto(l slog.Level) pb.LogLevel {
	switch {
	case l >= slog.LevelError:
		return pb.LogLevel_LOG_LEVEL_ERROR
	case l >= slog.LevelWarn:
		return pb.LogLevel_LOG_LEVEL_WARN
	case l >= slog.LevelInfo:
		return pb.LogLevel_LOG_LEVEL_INFO
	}
	return pb.LogLevel_LOG_LEVEL_DEBUG
}

func tunnelLevelToProto(l tunnel.LogLevel) pb.LogLevel {
	switch l {
	case tunnel.LogDebug:
		return pb.LogLevel_LOG_LEVEL_DEBUG
	case tunnel.LogInfo:
		return pb.LogLevel_LOG_LEVEL_INFO
	case tunnel.LogWarn:
		return pb.LogLevel_LOG_LEVEL_WARN
	case tunnel.LogError:
		return pb.LogLevel_LOG_LEVEL_ERROR
	}
	return pb.LogLevel_LOG_LEVEL_UNSPECIFIED
}

// sanitizeText makes engine-supplied text safe for a protobuf string field,
// which must be valid UTF-8, and bounds its length.
func sanitizeText(text string, limit int) string {
	if len(text) > limit {
		text = text[:limit]
	}
	return strings.ToValidUTF8(text, "�")
}
