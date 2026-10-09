//go:build !linux

package main

import "time"

const (
	// adminGroupGID is the macOS "admin" group.
	adminGroupGID = 80

	consoleDevice = "/dev/console"
)

func newPolicy() *policy {
	console := newConsoleUser()
	return &policy{
		administrator: inGroup(adminGroupGID),
		consoleUIDs: func() ([]uint32, error) {
			uid, err := console.uid()
			return []uint32{uid}, err
		},
		consoleSession: activeConsoleSession,
	}
}

func newConsoleUser() *consoleUser {
	return &consoleUser{stat: func() (uint32, error) { return fileOwner(consoleDevice) }, now: time.Now}
}
