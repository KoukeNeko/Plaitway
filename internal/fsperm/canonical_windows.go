//go:build windows

package fsperm

import (
	"errors"
	"fmt"
	"path/filepath"

	"golang.org/x/sys/windows"
)

const (
	// fileNameNormalized has the file system spell every name in full: an 8.3
	// short name becomes the long one and the case is the one on the disk.
	fileNameNormalized = 0x0
	// volumeNameNT names the volume by its device, \Device\HarddiskVolume3,
	// which every volume has and which is the same whatever leads to it: a
	// drive letter, a subst drive, a mount point or a junction. Naming it with
	// a drive letter would fail for a volume that has none.
	volumeNameNT   = 0x2
	finalPathFlags = fileNameNormalized | volumeNameNT
	// Nearly every path fits; a longer buffer is asked for only when this one
	// is too short.
	finalPathStartLength = windows.MAX_PATH
)

// canonicalPath spells path the way the file system does: the one name that
// every spelling of the same place (verbatim prefix, 8.3 name, case, a link on
// the way) comes down to. Windows is asked about an open handle, which is the
// only answer that cannot disagree with where an access would end up. A part
// of the path that does not exist yet cannot be asked, so the deepest part that
// exists is resolved and the rest is appended as it is written.
func canonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var missing []string
	for existing := absolute; ; {
		final, err := finalPathOf(existing)
		if err == nil {
			return filepath.Join(append([]string{final}, missing...)...), nil
		}
		parent := filepath.Dir(existing)
		if !isMissing(err) || parent == existing {
			return "", fmt.Errorf("resolve %s: %w", absolute, err)
		}
		missing = append([]string{filepath.Base(existing)}, missing...)
		existing = parent
	}
}

func isMissing(err error) bool {
	return errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND)
}

// finalPathOf opens path, following any link, and asks Windows where it ended up.
func finalPathOf(path string) (final string, err error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES, shareEverything, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer closeInto(handle, &err)

	buffer := make([]uint16, finalPathStartLength)
	for {
		length, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), finalPathFlags)
		if err != nil {
			return "", err
		}
		if int(length) < len(buffer) {
			return windows.UTF16ToString(buffer[:length]), nil
		}
		// A result that does not fit reports the size it needs, with its end.
		buffer = make([]uint16, length+1)
	}
}
