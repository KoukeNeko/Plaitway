//go:build windows

package fsperm

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// dataDirName is the directory below %ProgramData% that holds the state, the
// run files and the logs of the daemon.
const dataDirName = "Plaitway"

// dataRoot is replaced by the tests: a session that is not elevated cannot make
// a directory below the real %ProgramData% its own.
var dataRoot = func() (string, error) {
	programData, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return "", fmt.Errorf("find the ProgramData folder: %w", err)
	}
	return filepath.Join(programData, dataDirName), nil
}

// DataRoot is the directory of the production daemon below %ProgramData%. It
// asks the shell instead of reading the environment, which a service's caller
// can set.
func DataRoot() (string, error) {
	return dataRoot()
}

// checkDataRoot looks at the whole data root when path is inside it and says
// whether it is. %ProgramData% lets every user create directories, so whatever
// is found at the data root may have been put there by one of them. A link or
// an untrusted owner is refused; access lists are left to Restrict, which is
// the one that reports repairing them. A location outside the data root is the
// caller's own business.
func checkDataRoot(path string, p principals) (inside bool, err error) {
	root, err := DataRoot()
	if err != nil {
		return false, err
	}
	inside, err = isInside(root, path)
	if err != nil || !inside {
		return false, err
	}
	// A root that is missing is for the caller's own create or open to report.
	if err := checkTree(root, p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return true, err
	}
	return true, nil
}

// isInside reports whether path is root or below it. It is the way a path is
// written that must not decide: a verbatim prefix, an 8.3 short name, another
// case or a junction that leads into the root all name the same objects as the
// plain spelling, and escaping the checks that way would defeat them. So a path
// is inside when either its text or the place Windows resolves it to is. Whatever
// cannot be resolved counts as inside, because the cost of that is a check that
// was not needed, and the cost of the opposite is one that was skipped.
func isInside(root, path string) (bool, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	return isBelow(root, absolute) || resolvesInside(root, absolute), nil
}

func resolvesInside(root, path string) bool {
	canonicalRoot, rootErr := canonicalPath(root)
	canonicalCandidate, candidateErr := canonicalPath(path)
	if rootErr != nil || candidateErr != nil {
		return true
	}
	return isBelow(canonicalRoot, canonicalCandidate)
}

// isBelow compares the text of two absolute paths and says whether path is root
// or below it. Windows paths ignore case, and so does filepath.Rel; a directory
// that the file system treats as case sensitive is counted as inside as well.
func isBelow(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false // another volume
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
