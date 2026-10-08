package reconciler

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

const (
	// replaceRetryWindow is how long a rename may keep failing because another
	// program, typically a virus scanner or the search indexer, has the file
	// open without allowing it to be replaced. Their hold is brief.
	replaceRetryWindow   = 2 * time.Second
	replaceRetryInterval = 10 * time.Millisecond
)

// replaceFile moves tmp over the journal atomically. Windows cannot replace a
// file that any handle has open, and every append was already synced, so the
// append handle is let go first. A directory cannot be synced on Windows and
// does not need to be: whichever of the two files a power cut leaves behind is
// a complete journal, and the old one only has more finished records in it.
//
// When the replacement fails the old journal is still whole, so it is opened
// again to keep the write-ahead guarantee. The count towards the next
// compaction starts over, so that a file that stays locked costs the retry
// window once per compactAfter records instead of once per append.
func (j *journal) replaceFile(tmp string) error {
	if j.file != nil {
		j.file.Close()
		j.file = nil
	}
	err := renameWithRetry(tmp, j.path)
	if err == nil {
		return nil
	}
	j.appended = 0
	if reopenErr := j.openForAppend(); reopenErr != nil {
		return errors.Join(err, fmt.Errorf("reopening journal: %w", reopenErr))
	}
	return err
}

func renameWithRetry(from, to string) error {
	deadline := time.Now().Add(replaceRetryWindow)
	for {
		err := os.Rename(from, to)
		if err == nil || !isFileInUse(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(replaceRetryInterval)
	}
}

// isFileInUse reports whether err is what Windows says when another handle
// stops the rename. An access denied from a handle that lacks the delete share
// mode looks the same as a real permission problem, so the retry is bounded.
func isFileInUse(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
