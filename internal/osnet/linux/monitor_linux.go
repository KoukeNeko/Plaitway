package linux

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"golang.org/x/sys/unix"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

const (
	// netlinkGroups are the notifications that can change what Snapshot returns.
	netlinkGroups = rtmgrpLink | rtmgrpIPv4Addr | rtmgrpIPv4Route | rtmgrpIPv6Addr | rtmgrpIPv6Route

	sysClassNet = "/sys/class/net"
)

// NewNetMonitor returns the network monitor of this machine.
func NewNetMonitor(opts NetMonitorOptions) osnet.NetMonitor {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &netMonitor{
		log:          log,
		clk:          newRealClock(),
		routes:       NewRouteTable(),
		links:        listLinks,
		kinds:        newLinkKinds(os.DirFS(sysClassNet)),
		watchChanges: watchNetlink,
	}
}

// watchNetlink reads the multicast groups until ctx ends.
func watchNetlink(ctx context.Context, log *slog.Logger, changed func()) error {
	conn, err := openNetlink(netlinkGroups)
	if err != nil {
		return err
	}
	return watchConn(ctx, conn, log, changed)
}

// watchConn calls changed for every datagram of conn that can matter, until ctx
// ends. It closes conn.
func watchConn(ctx context.Context, conn *netlinkConn, log *slog.Logger, changed func()) error {
	defer conn.close()
	defer context.AfterFunc(ctx, func() { conn.close() })()
	links := &linkCache{load: listLinkIdentities}
	for {
		b, err := conn.recv()
		switch {
		case errors.Is(err, unix.ENOBUFS):
			// The receive buffer overflowed: messages were lost, and there is no
			// telling which. The socket keeps working.
			log.Warn("netlink messages were lost; treating it as a change")
			changed()
		case err != nil:
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read netlink socket: %w", err)
		case messagesRelevant(b, links, log):
			changed()
		}
	}
}
