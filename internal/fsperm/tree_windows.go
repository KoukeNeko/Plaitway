//go:build windows

package fsperm

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
)

// restrictTree makes path and everything below it private and returns how many
// objects it had to repair. Every object is opened without following a link and
// has to be owned by a trusted account; the first one that is not ends the walk
// with an error. Each child is looked at on its own: an access list that is
// protected from inheritance is not changed by repairing the directory above.
func restrictTree(path string, p principals) (repaired int, err error) {
	return walkTree(path, p, true)
}

// checkTree is restrictTree without the repair: it refuses a link or an
// untrusted owner anywhere below path, and leaves every access list as it is.
func checkTree(path string, p principals) error {
	_, err := walkTree(path, p, false)
	return err
}

func walkTree(path string, p principals, repair bool) (repaired int, err error) {
	root, err := exactLocation(path)
	if err != nil {
		return 0, err
	}
	return walkLocation(root, p, repair)
}

func walkLocation(at location, p principals, repair bool) (repaired int, err error) {
	changed, isDir, err := visitObject(at, p, repair)
	if err != nil {
		return 0, err
	}
	if changed {
		repaired++
	}
	if !isDir {
		return repaired, nil
	}
	entries, err := os.ReadDir(at.exact)
	if err != nil {
		return repaired, err
	}
	for _, entry := range entries {
		below, err := walkLocation(at.child(entry.Name()), p, repair)
		repaired += below
		if err := forgiveVanished(at, entry.Name(), err); err != nil {
			return repaired, err
		}
	}
	return repaired, nil
}

// forgiveVanished decides what a failure to open an entry of dir means. Only an
// entry that a fresh listing no longer shows has been removed since the first
// one, and needs no repair; one that is still listed but is not found is hiding
// from the walk, and passing it over would leave it with whatever access list
// it has.
func forgiveVanished(dir location, name string, err error) error {
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	listed, listErr := isListed(dir, name)
	switch {
	case listErr != nil:
		return fmt.Errorf("confirm that %s was removed: %w", dir.child(name).shown, listErr)
	case listed:
		return fmt.Errorf("%w: %s: %v", ErrUnreachable, dir.child(name).shown, err)
	}
	return nil
}

// isListed reports whether dir lists an entry called name. A directory that is
// gone lists nothing.
func isListed(dir location, name string) (bool, error) {
	entries, err := os.ReadDir(dir.exact)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(entries, func(entry fs.DirEntry) bool { return entry.Name() == name }), nil
}

// visitObject checks one file or directory through its handle and, when repair
// is set, replaces its access list if someone else can reach it.
func visitObject(at location, p principals, repair bool) (changed, isDir bool, err error) {
	access := uint32(inspectAccess)
	if repair {
		access = repairAccess
	}
	handle, err := openLocation(at, access)
	if err != nil {
		return false, false, explainDenial(at, err, p)
	}
	defer closeInto(handle, &err)

	found, err := inspect(handle, at.shown, p)
	if err != nil {
		return false, false, err
	}
	if found.private || !repair {
		return false, found.isDir, nil
	}
	if err := replaceList(handle, at.shown, p.grantees(), inheritanceFor(found.isDir)); err != nil {
		return false, found.isDir, err
	}
	return true, found.isDir, nil
}
