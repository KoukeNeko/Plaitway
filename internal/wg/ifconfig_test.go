//go:build !windows

package wg

import (
	"net/netip"
	"slices"
	"testing"
)

func TestIfconfigCommands(t *testing.T) {
	got := ifconfigCommands("utun7", []netip.Prefix{
		netip.MustParsePrefix("10.6.0.2/24"),
		netip.MustParsePrefix("fd00::2/64"),
		netip.MustParsePrefix("192.0.2.5/32"),
	}, 1380)
	want := [][]string{
		{"utun7", "inet", "10.6.0.2/24", "10.6.0.2", "alias"},
		{"utun7", "inet6", "fd00::2/64", "alias"},
		{"utun7", "inet", "192.0.2.5/32", "192.0.2.5", "alias"},
		{"utun7", "mtu", "1380", "up"},
	}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("ifconfigCommands = %q, want %q", got, want)
	}
}

func TestIfconfigCommandsWithoutAddresses(t *testing.T) {
	got := ifconfigCommands("utun7", nil, 1420)
	if want := [][]string{{"utun7", "mtu", "1420", "up"}}; !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("ifconfigCommands = %q, want %q", got, want)
	}
}
