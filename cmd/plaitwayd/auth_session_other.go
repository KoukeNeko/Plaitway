//go:build !windows

package main

import "errors"

// Only Windows identifies the console by session; elsewhere consoleUID does it.
func activeConsoleSession() (uint32, error) {
	return 0, errors.New("console sessions exist only on Windows")
}
