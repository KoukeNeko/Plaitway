package linux

import (
	"context"
	"os/exec"
	"strings"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// NewDNS returns the DNS configurator of this host, which drives
// systemd-resolved through resolvectl. Writing needs root.
func NewDNS(opts DNSOptions) osnet.DNSConfigurator {
	if opts.Run == nil {
		opts.Run = runDNSCommand
	}
	return newDNSConfigurator(opts)
}

// runDNSCommand runs a system tool with a fixed, minimal environment: the
// daemon is root and the tool needs nothing from it, and resolvectl would
// follow DBUS_SYSTEM_BUS_ADDRESS to wherever it points. LC_ALL=C keeps the
// error texts the adapter looks for in English.
func runDNSCommand(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"LC_ALL=C", "PATH=/usr/bin:/bin"}
	cmd.Stdin = strings.NewReader(stdin)
	return cmd.CombinedOutput()
}
