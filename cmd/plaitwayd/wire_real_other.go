//go:build !darwin

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

// wireReal: only the macOS adapters exist so far; run with -fake elsewhere.
func wireReal(realConfig) ([]tunnel.Backend, tunnel.Reconciler, osnet.NetMonitor, error) {
	return nil, nil, nil, errors.New("real engines are only available on macOS")
}
