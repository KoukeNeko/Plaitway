package macos

import (
	"context"
	"os/exec"
	"strings"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// NewDNS returns the DNS configurator of this Mac. Writing needs root.
func NewDNS(opts DNSOptions) osnet.DNSConfigurator {
	if opts.Run == nil {
		opts.Run = runCommand
	}
	return newDNSConfigurator(opts)
}

// runCommand runs a system tool with an empty environment: the daemon is root
// and the tools need nothing from it.
func runCommand(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"LC_ALL=C"}
	cmd.Stdin = strings.NewReader(stdin)
	return cmd.CombinedOutput()
}
