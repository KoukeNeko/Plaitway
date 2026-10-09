// Package wg is the WireGuard engine: an embedded wireguard-go device behind
// tunnel.Backend. It owns the tunnel device and the handshake; routes and DNS
// are announced to the Reconciler, never written here.
package wg

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"runtime/debug"
	"time"

	"golang.zx2c4.com/wireguard/tun"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

const modulePath = "golang.zx2c4.com/wireguard"

// Config holds what the daemon, or a test, replaces. The zero value is the
// production behavior.
type Config struct {
	// Log receives the engines' diagnostics; nil discards them.
	Log *slog.Logger
	// TunFactory creates the tunnel device with the given MTU. The default
	// creates the platform's device, which needs root or, on Windows, an
	// elevated process: a utun on macOS, a tun device named plaitwayN on Linux,
	// a wintun adapter named after the profile on Windows.
	TunFactory func(mtu int) (tun.Device, error)
	// Interfaces applies addresses and MTU to the new tunnel interface. The
	// default runs /sbin/ifconfig on macOS, talks rtnetlink on Linux and calls
	// the IP Helper API on Windows, which all need privilege.
	Interfaces Interfaces

	// Seams for tests in this package.
	lookup       func(ctx context.Context, host string) ([]netip.Addr, error)
	pollInterval time.Duration
	// stallGrace is how long a tunnel may wait for its endpoint to resolve or
	// for the first handshake before the reason is put in the status.
	stallGrace time.Duration
	// The pause after a failed lookup starts at retryMin, doubles and stops at
	// retryMax.
	retryMin, retryMax time.Duration
	// adapterPrefix starts the name of the Windows adapters; the tests that
	// create real ones use a scratch prefix so that they never touch an adapter
	// of the daemon.
	adapterPrefix string
}

func (c Config) withDefaults() Config {
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	if c.adapterPrefix == "" {
		c.adapterPrefix = defaultAdapterPrefix
	}
	if c.lookup == nil {
		c.lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	if c.pollInterval == 0 {
		c.pollInterval = 2 * time.Second
	}
	if c.stallGrace == 0 {
		c.stallGrace = 20 * time.Second
	}
	if c.retryMin == 0 {
		c.retryMin = 2 * time.Second
	}
	if c.retryMax == 0 {
		c.retryMax = 30 * time.Second
	}
	return c
}

// Backend returns the WireGuard backend.
func Backend(cfg Config) tunnel.Backend {
	cfg = cfg.withDefaults()
	return tunnel.Backend{
		Kind:  tunnel.KindWireGuard,
		Parse: Parse,
		New: func(spec tunnel.Spec, deps tunnel.Deps) (tunnel.Engine, error) {
			return newEngine(cfg, spec, deps)
		},
		Probe: probe,
	}
}

// probe: wireguard-go is linked into the daemon, so it is available unless
// the platform lacks something it needs (on Windows, a trusted wintun.dll).
func probe() tunnel.EngineInfo {
	// The version comes second: it includes what the check just loaded.
	err := checkPlatform()
	info := tunnel.EngineInfo{Version: engineVersion()}
	if err != nil {
		info.Detail = err.Error()
		return info
	}
	info.Available = true
	return info
}

func engineVersion() string {
	version := moduleVersion()
	if extra := platformVersion(); extra != "" {
		return version + " (" + extra + ")"
	}
	return version
}

func moduleVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, dep := range info.Deps {
		if dep.Path == modulePath {
			if dep.Replace != nil {
				return dep.Replace.Version
			}
			return dep.Version
		}
	}
	return ""
}
