//go:build !windows

package main

import "os"

// isPrivileged reports whether the daemon runs as root.
func isPrivileged() bool { return os.Geteuid() == 0 }
