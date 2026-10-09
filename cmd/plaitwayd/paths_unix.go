//go:build !windows

package main

import (
	"log/slog"

	"github.com/KoukeNeko/Plaitway/internal/fsperm"
)

// ensureRunDir creates the run directory. It stays open to every user: the
// control socket is created in it, and the clients have to reach that.
func ensureRunDir(_ *slog.Logger, dir string) error {
	return fsperm.MkdirAll(dir, runDirMode)
}
