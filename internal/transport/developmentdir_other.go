//go:build unix && !linux

package transport

import "os"

// developmentDir is the temporary directory, which is private to the user on
// macOS.
func developmentDir() string { return os.TempDir() }
