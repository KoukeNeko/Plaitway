//go:build !windows

package main

import (
	"log/slog"

	"github.com/KoukeNeko/Plaitway/internal/manager"
)

// enterServiceMode: only Windows has a service host. launchd and systemd run
// the daemon as the console program it is.
func enterServiceMode() func(*slog.Logger, *manager.LogBuffer, config) int { return nil }
