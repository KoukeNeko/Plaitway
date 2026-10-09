//go:build !darwin && !linux && !windows

package main

import (
	"errors"
	"os"
)

// Only macOS, Linux and Windows run the daemon; elsewhere the client is for
// development and takes passwords from stdin only.
func isTerminal(*os.File) bool { return false }

func disableEcho(*os.File) (func(), error) {
	return nil, errors.New("no-echo input is not supported on this OS")
}
