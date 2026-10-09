//go:build !darwin && !windows && !linux

package main

import (
	"errors"
	"log/slog"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

type realConfig struct {
	log      *slog.Logger
	openvpn  string
	runDir   string
	stateDir string
}

// wireReal: only the macOS, Linux and Windows adapters exist; run with -fake
// elsewhere.
func wireReal(realConfig) ([]tunnel.Backend, tunnel.Reconciler, osnet.NetMonitor, error) {
	return nil, nil, nil, errors.New("real engines are only available on macOS, Linux and Windows")
}
