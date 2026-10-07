// Command plaitwayd is the daemon: it runs several OpenVPN and WireGuard
// profiles at the same time and serves the control API over a Unix domain
// socket only, never TCP. With -fake it runs on an in-memory backend, which is
// a complete stand-in for UI development without root.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/KoukeNeko/Plaitway/internal/manager"
	"github.com/KoukeNeko/Plaitway/internal/manager/fake"
	"github.com/KoukeNeko/Plaitway/internal/transport"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "0.0.0-dev"

// openvpnSHA256 is the SHA-256, in hex, of the openvpn binary that was built
// for this daemon: -ldflags "-X main.openvpnSHA256=<hex>". The daemon runs as
// root, and the app bundle that holds openvpn can be changed by the user who
// installed it, so a build that sets it runs only a copy of openvpn that has
// this hash, made in the run directory. Without it (development builds)
// openvpn runs from the configured path as it is.
var openvpnSHA256 string

const (
	// Where the LaunchDaemon keeps its state, sockets and logs.
	productionStateDir = "/Library/Application Support/Plaitway"
	productionRunDir   = "/var/run/plaitway"
	productionLogFile  = "/Library/Logs/Plaitway/plaitwayd.log"

	// maxLogFileSize is when the log file is moved aside at start-up.
	maxLogFileSize = 10 << 20
)

func main() {
	cfg, level, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "plaitwayd:", err)
		os.Exit(2)
	}
	out, closeLog, err := logOutput(cfg.logFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plaitwayd:", err)
		os.Exit(2)
	}
	defer closeLog()
	log, daemonLog := newLogger(out, level)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop() // a second signal ends the process at once
	}()

	if err := run(ctx, log, daemonLog, cfg, newPolicy()); err != nil {
		log.Error("daemon failed", "err", err)
		os.Exit(1)
	}
}

// newLogger logs to w and keeps the lines for WatchLogs with an empty profile id.
func newLogger(w io.Writer, level slog.Level) (*slog.Logger, *manager.LogBuffer) {
	daemonLog := manager.NewLogBuffer("")
	return slog.New(daemonLog.Handler(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))), daemonLog
}

func parseFlags(args []string) (config, slog.Level, error) {
	stateDir, runDir := defaultDirs()
	var (
		cfg      config
		mode     string
		level    slog.Level
		levelArg string
		useFake  bool
	)
	fs := flag.NewFlagSet("plaitwayd", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.socket, "socket", transport.DefaultPath(), "Unix socket path (named pipe name on Windows)")
	fs.StringVar(&mode, "socket-mode", "0600", "permissions of the socket, octal; the production daemon passes 0666 and authorizes each call itself")
	fs.StringVar(&cfg.stateDir, "state-dir", stateDir, "directory for the stored profiles (mode 0700)")
	fs.StringVar(&cfg.runDir, "run-dir", runDir, "directory for management sockets and generated configs")
	fs.StringVar(&cfg.openvpn, "openvpn", bundledOpenVPN(), "path of the openvpn binary")
	fs.StringVar(&cfg.logFile, "log-file", defaultLogFile(), "log file, in addition to stderr; empty for stderr only")
	fs.StringVar(&levelArg, "log-level", "info", "debug, info, warn or error")
	fs.BoolVar(&useFake, "fake", false, "use the in-memory backend instead of real tunnels")
	if err := fs.Parse(args); err != nil {
		return config{}, 0, err
	}
	if fs.NArg() > 0 {
		return config{}, 0, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	perm, err := strconv.ParseUint(mode, 8, 32)
	if err != nil || perm > 0o777 {
		return config{}, 0, fmt.Errorf("-socket-mode %q is not an octal permission like 0600", mode)
	}
	cfg.socketMode = os.FileMode(perm)
	if err := level.UnmarshalText([]byte(levelArg)); err != nil {
		return config{}, 0, fmt.Errorf("-log-level %q: %w", levelArg, err)
	}
	if useFake {
		cfg.fake = &fake.Config{}
	}
	return cfg, level, nil
}

// defaultDirs are the production directories when running as root and
// directories under the temporary directory otherwise, so that a development
// daemon needs no flags and touches nothing outside its user's own space.
func defaultDirs() (stateDir, runDir string) {
	if os.Geteuid() == 0 {
		return productionStateDir, productionRunDir
	}
	return filepath.Join(os.TempDir(), "plaitway-state"), filepath.Join(os.TempDir(), "plaitway-run")
}

// defaultLogFile is the production log when running as root. launchd opens
// StandardErrorPath before the daemon runs and fails the job when its
// directory is missing, so the daemon makes its own log directory instead.
func defaultLogFile() string {
	if os.Geteuid() == 0 {
		return productionLogFile
	}
	return ""
}

// logOutput returns stderr, plus the log file when there is one. A file above
// maxLogFileSize is moved to <path>.1 first, replacing the previous one. The
// file is readable by root only: it names endpoints and users.
func logOutput(path string) (w io.Writer, closeLog func(), err error) {
	if path == "" {
		return os.Stderr, func() {}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, fmt.Errorf("create log directory: %w", err)
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxLogFileSize {
		if err := os.Rename(path, path+".1"); err != nil {
			return nil, nil, fmt.Errorf("rotate log file: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open log file: %w", err)
	}
	// Under launchd stderr goes nowhere, so the log file takes its place: what
	// the Go runtime prints on a panic is the most valuable line of a crash.
	if !stderrIsTerminal() {
		if err := redirectStderr(f); err != nil {
			return nil, nil, fmt.Errorf("redirect stderr to the log file: %w", err)
		}
		return os.Stderr, func() { f.Close() }, nil
	}
	return io.MultiWriter(os.Stderr, f), func() { f.Close() }, nil
}

// bundledOpenVPN is where the app bundle keeps openvpn:
// Plaitway.app/Contents/Resources/bin/openvpn next to Contents/MacOS/plaitwayd.
func bundledOpenVPN() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), "..", "Resources", "bin", "openvpn")
}

// run listens, builds the daemon and serves until ctx ends.
func run(ctx context.Context, log *slog.Logger, daemonLog *manager.LogBuffer, cfg config, pol *policy) error {
	// Listen first, while the process is still single-threaded in practice (see
	// transport.Listen on the umask).
	lis, err := transport.Listen(cfg.socket, cfg.socketMode)
	if err != nil {
		return err
	}
	d, err := newDaemon(log, daemonLog, cfg, pol)
	if err != nil {
		lis.Close()
		return err
	}
	if cfg.fake == nil {
		if err := os.MkdirAll(cfg.runDir, 0o755); err != nil {
			lis.Close()
			return fmt.Errorf("create run directory: %w", err)
		}
	}
	log.Info("listening", "socket", cfg.socket, "mode", fmt.Sprintf("%04o", cfg.socketMode), "state", cfg.stateDir,
		"version", version, "fake", cfg.fake != nil, "pid", os.Getpid(), "uid", os.Getuid())
	return d.serve(ctx, lis)
}
