//go:build !windows

package reconciler

import (
	"os"
	"path/filepath"
)

// replaceFile moves tmp over the journal atomically and syncs the directory so
// that the rename itself survives a power cut. The append handle stays open: if
// the replacement fails the old journal is still the one being written.
func (j *journal) replaceFile(tmp string) error {
	if err := os.Rename(tmp, j.path); err != nil {
		return err
	}
	if dir, err := os.Open(filepath.Dir(j.path)); err == nil {
		dir.Sync() // the rename itself; best effort, not every filesystem allows it
		dir.Close()
	}
	return nil
}
