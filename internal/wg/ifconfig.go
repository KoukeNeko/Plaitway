//go:build !windows

package wg

import (
	"context"
	"fmt"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
)

// ifconfig is the default Interfaces: /sbin/ifconfig, which needs root.
type ifconfig struct{}

func (ifconfig) Configure(ctx context.Context, name string, addrs []netip.Prefix, mtu int) error {
	for _, args := range ifconfigCommands(name, addrs, mtu) {
		out, err := exec.CommandContext(ctx, "/sbin/ifconfig", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("ifconfig %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// ifconfigCommands is the argument list of each ifconfig run, as wg-quick does
// it on macOS: a tunnel address is an alias that is its own point-to-point peer.
func ifconfigCommands(name string, addrs []netip.Prefix, mtu int) [][]string {
	var commands [][]string
	for _, addr := range addrs {
		if addr.Addr().Is4() {
			commands = append(commands, []string{name, "inet", addr.String(), addr.Addr().String(), "alias"})
		} else {
			commands = append(commands, []string{name, "inet6", addr.String(), "alias"})
		}
	}
	return append(commands, []string{name, "mtu", strconv.Itoa(mtu), "up"})
}
