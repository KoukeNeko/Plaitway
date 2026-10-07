package wg

import (
	"net/netip"
	"runtime/debug"
	"slices"
	"strings"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

func TestProbe(t *testing.T) {
	info := Backend(Config{}).Probe()
	if !info.Available || info.Detail != "" {
		t.Errorf("Probe() = %+v, want an available engine without detail", info)
	}
	build, ok := debug.ReadBuildInfo()
	if !ok {
		t.Skip("no build info in this binary")
	}
	for _, dep := range build.Deps {
		if dep.Path == modulePath {
			if info.Version != dep.Version {
				t.Errorf("Version = %q, want the module version %q", info.Version, dep.Version)
			}
			return
		}
	}
	t.Skip("wireguard-go is not in the build info of this test binary")
}

func TestBackendIsWireGuard(t *testing.T) {
	b := Backend(Config{})
	if b.Kind != tunnel.KindWireGuard || b.Parse == nil || b.New == nil || b.Probe == nil {
		t.Fatalf("Backend = %+v", b)
	}
}

func TestBackendNewRejectsABadProfile(t *testing.T) {
	b := Backend(Config{})
	spec := tunnel.Spec{Owner: "x", Name: "home", Content: []byte("[Interface]\nPostUp = true\n")}
	_, err := b.New(spec, tunnel.Deps{})
	if err == nil || !strings.Contains(err.Error(), "PostUp is not allowed") {
		t.Fatalf("New() error = %v, want the profile rejected", err)
	}
}

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
