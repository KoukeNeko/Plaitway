//go:build windows

package profile

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

const (
	// A scanner, an indexer or a backup that has the old file open for a moment
	// is gone within tens of milliseconds, so the wait is short.
	replaceAttempts   = 10
	replaceRetryDelay = 20 * time.Millisecond
)

// replaceFile renames from over to. Windows cannot sync a directory, so it asks
// MoveFileEx to return only once the move is on disk. A target that another
// process holds open without sharing delete cannot be replaced, and Windows
// reports that as access denied, the same as a real refusal: the move is
// tried a few times before it is given up.
func replaceFile(from, to string) error {
	fromName, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	toName, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	for attempt := 1; ; attempt++ {
		err = windows.MoveFileEx(fromName, toName, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
		if err == nil {
			return nil
		}
		if attempt == replaceAttempts || !isHeldOpen(err) {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
		}
		time.Sleep(replaceRetryDelay)
	}
}

func isHeldOpen(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
