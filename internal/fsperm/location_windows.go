//go:build windows

package fsperm

import (
	"path/filepath"
	"strings"
)

const (
	verbatimPrefix    = `\\?\`
	verbatimUNCPrefix = `\\?\UNC\`
	devicePrefix      = `\\.\`
	uncPrefix         = `\\`
)

// location is a place in a tree, spelled twice. Win32 changes a name before it
// reaches the file system (it drops a trailing dot or space), so an entry that
// the directory lists as "dotted." is opened as "dotted" and is not found. The
// verbatim spelling is taken literally; the other one is what a message shows.
type location struct {
	shown, exact string
}

// plainLocation is a path that is opened the way the caller spelled it.
func plainLocation(path string) location {
	return location{shown: path, exact: path}
}

// exactLocation is the root of a walk: the caller's spelling is kept for
// messages, and everything below it is opened literally.
func exactLocation(path string) (location, error) {
	exact, err := verbatimPath(path)
	if err != nil {
		return location{}, err
	}
	return location{shown: path, exact: exact}, nil
}

func (at location) child(name string) location {
	return location{shown: filepath.Join(at.shown, name), exact: filepath.Join(at.exact, name)}
}

// verbatimPath spells path so that Windows reads every name in it literally.
func verbatimPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	switch {
	case strings.HasPrefix(absolute, verbatimPrefix), strings.HasPrefix(absolute, devicePrefix):
		return absolute, nil
	case strings.HasPrefix(absolute, uncPrefix):
		return verbatimUNCPrefix + strings.TrimPrefix(absolute, uncPrefix), nil
	default:
		return verbatimPrefix + absolute, nil
	}
}
