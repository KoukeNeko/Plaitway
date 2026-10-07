package macos

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sys/unix"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// NewNetMonitor returns the network monitor of this Mac.
func NewNetMonitor(opts NetMonitorOptions) osnet.NetMonitor {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	run := opts.Run
	if run == nil {
		run = runCommand
	}
	return &netMonitor{
		log:         log,
		clk:         newRealClock(),
		routes:      NewRouteTable(),
		interfaces:  listInterfaces,
		kinds:       newLinkKinds(run, log),
		sleepTime:   kernSleepTime,
		watchRoutes: watchRouteSocket,
	}
}

func kernSleepTime() (time.Time, error) {
	tv, err := unix.SysctlTimeval("kern.sleeptime")
	if err != nil {
		return time.Time{}, fmt.Errorf("sysctl kern.sleeptime: %w", err)
	}
	return time.Unix(tv.Unix()), nil
}

// watchRouteSocket reads the routing socket until ctx ends.
func watchRouteSocket(ctx context.Context, log *slog.Logger, changed func()) error {
	sock, err := openRouteSocket()
	if err != nil {
		return err
	}
	defer sock.Close()
	defer context.AfterFunc(ctx, func() { sock.Close() })()

	names := nameCache{load: interfaceNames}
	buf := make([]byte, 1<<16)
	for {
		n, err := sock.Read(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read routing socket: %w", err)
		}
		if routeMessagesRelevant(buf[:n], &names, log) {
			changed()
		}
	}
}
