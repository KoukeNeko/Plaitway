//go:build !windows

package wg

import (
	"runtime/debug"
	"testing"
)

func TestProbe(t *testing.T) {
	if err := checkPlatform(); err != nil {
		t.Skipf("the engine is unavailable on this machine: %v", err)
	}
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
