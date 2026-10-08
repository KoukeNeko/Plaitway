package main

import (
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/KoukeNeko/Plaitway/internal/fsperm"
)

const (
	// Below the data root (%ProgramData%\Plaitway): the state directory is the
	// root itself, and the run directory and the log directory are in it.
	runDirName  = "run"
	logDirName  = "Logs"
	logFileName = "plaitwayd.log"
)

// productionLocations puts everything below the data root, which fsperm finds
// through the shell instead of the environment: a service's environment is not
// something the daemon should trust to say where it keeps private keys. fsperm
// also checks everything below that root (owner, links, access lists) whenever
// the log is opened or the state and run directories are.
func productionLocations() (locations, error) {
	root, err := fsperm.DataRoot()
	if err != nil {
		return locations{}, err
	}
	return locations{
		stateDir: root,
		runDir:   filepath.Join(root, runDirName),
		logFile:  filepath.Join(root, logDirName, logFileName),
	}, nil
}

// ensureRunDir creates the run directory and keeps it private. The control
// socket is a named pipe, so nothing in the directory has to be reachable by
// other users: it holds the generated configurations.
func ensureRunDir(log *slog.Logger, dir string) error {
	if err := fsperm.MkdirAll(dir, runDirMode); err != nil {
		return err
	}
	if err := fsperm.RestrictAndWarn(log, "run directory", dir); err != nil {
		return fmt.Errorf("restrict run directory: %w", err)
	}
	return nil
}
