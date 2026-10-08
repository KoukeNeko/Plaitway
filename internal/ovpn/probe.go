package ovpn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

const (
	probeTimeout = 10 * time.Second
	// reprobeAfter is how soon a binary that could not be used is looked at
	// again.
	reprobeAfter = 5 * time.Second
)

// binaryInfo is what "openvpn --version" says about the executable.
type binaryInfo struct {
	available bool
	detail    string // why it is unavailable
	version   string // "2.7.7"
	major     int
	minor     int
	lzo       bool
	// driver is the Windows driver the engine runs the tunnel on
	// (windowsDriverName); empty elsewhere.
	driver string
}

// supportsDisableDCO: --disable-dco was added with data channel offload in 2.6.
func (i binaryInfo) supportsDisableDCO() bool { return i.atLeast(2, 6) }

// persistKeyIsDeprecated: since 2.7 keys are always kept across restarts and
// --persist-key only prints a deprecation notice.
func (i binaryInfo) persistKeyIsDeprecated() bool { return i.atLeast(2, 7) }

// hasDNSUpdown: --dns-updown, with its built-in default script, came in 2.7.
func (i binaryInfo) hasDNSUpdown() bool { return i.atLeast(2, 7) }

func (i binaryInfo) atLeast(major, minor int) bool {
	return i.major > major || (i.major == major && i.minor >= minor)
}

type backend struct {
	cfg     Config
	reprobe time.Duration
	// inspect asks the binary what it is, and refuses one the daemon cannot
	// trust; trustBinary does the second again right before a start. Tests
	// replace them.
	inspect     func(cfg Config, timeout time.Duration) binaryInfo
	trustBinary func() (release func(), err error)

	mu       sync.Mutex
	probe    binaryInfo
	probedAt time.Time
}

func newBackend(cfg Config) *backend {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	b := &backend{cfg: cfg, reprobe: reprobeAfter, inspect: inspectBinary}
	b.trustBinary = func() (func(), error) { return trustBinary(b.cfg) }
	return b
}

// info runs the probe the first time it is needed and remembers a usable
// answer. One that says the binary is unusable is only kept for a few seconds:
// the binary may have been in the middle of an update, or slow on its first
// start, and the daemon has to notice when it is usable.
func (b *backend) info() binaryInfo {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.probe.available || (!b.probedAt.IsZero() && time.Since(b.probedAt) < b.reprobe) {
		return b.probe
	}
	b.probe = b.inspect(b.cfg, probeTimeout)
	b.probedAt = time.Now()
	return b.probe
}

func (b *backend) probeInfo() tunnel.EngineInfo {
	i := b.info()
	if !i.available {
		return tunnel.EngineInfo{Detail: i.detail}
	}
	version := i.version
	if i.driver != "" {
		version += " (" + i.driver + ")"
	}
	if !i.lzo {
		version += " (no LZO)"
	}
	return tunnel.EngineInfo{Available: true, Version: version}
}

var versionLine = regexp.MustCompile(`^OpenVPN (\d+)\.(\d+)\.(\d+)\S*`)

func probeBinary(path string, timeout time.Duration) binaryInfo {
	if path == "" {
		return binaryInfo{detail: "openvpn binary is not configured"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	prepareProbe(cmd)
	cmd.WaitDelay = time.Second // a wrapper script's children must not hold the pipe open
	// openvpn --version exits with status 1 after printing; the output decides.
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return binaryInfo{detail: fmt.Sprintf("%s --version did not finish within %s", path, timeout)}
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, exec.ErrNotFound):
		return binaryInfo{detail: fmt.Sprintf("openvpn not found at %s", path)}
	case errors.Is(err, fs.ErrPermission):
		return binaryInfo{detail: fmt.Sprintf("openvpn at %s cannot be executed", path)}
	case err != nil && !errors.As(err, &exitErr):
		return binaryInfo{detail: fmt.Sprintf("running %s --version: %v", path, err)}
	}
	first, _, _ := strings.Cut(string(out), "\n")
	m := versionLine.FindStringSubmatch(first)
	if m == nil {
		return binaryInfo{detail: fmt.Sprintf("%s --version did not print an OpenVPN version", path)}
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	return binaryInfo{
		available: true,
		version:   strings.Join(m[1:4], "."),
		major:     major,
		minor:     minor,
		lzo:       strings.Contains(first, "[LZO]"),
	}
}
