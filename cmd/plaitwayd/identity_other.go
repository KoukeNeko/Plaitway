//go:build !windows

package main

import (
	"fmt"
	"os"
)

// processIdentityAttrs are the log attributes that say who the daemon runs as.
func processIdentityAttrs() []any { return []any{"uid", os.Getuid()} }

// socketModeAttrs name the permissions of the socket, which are its access control.
func socketModeAttrs(mode os.FileMode) []any { return []any{"mode", fmt.Sprintf("%04o", mode)} }
