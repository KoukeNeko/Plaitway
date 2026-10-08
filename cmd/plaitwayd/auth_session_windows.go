package main

import (
	"errors"

	"golang.org/x/sys/windows"
)

// noActiveSession is what WTSGetActiveConsoleSessionId returns while nobody is
// attached to the console, for example after the session moved to Remote Desktop.
const noActiveSession = 0xFFFFFFFF

func activeConsoleSession() (uint32, error) {
	session := windows.WTSGetActiveConsoleSessionId()
	if session == noActiveSession {
		return 0, errors.New("no session is attached to the console")
	}
	return session, nil
}
