//go:build !windows

package profile

import (
	"os"
	"path/filepath"
)

// replaceFile renames from over to and syncs the directory, so that a crash
// after it returns leaves the new file in place.
func replaceFile(from, to string) error {
	if err := os.Rename(from, to); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(to))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
