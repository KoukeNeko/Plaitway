//go:build windows

package fsperm

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

// restrictedMessageSuffix follows the label of the directory in the warning.
const restrictedMessageSuffix = " was accessible to others, restricted to SYSTEM, Administrators and this account"

// MkdirAll creates dir and its missing parents, each with a protected access
// list from the start, so that no moment exists in which another user could
// read what is put in it. An existing directory is left as it is (Restrict
// checks it), and perm has no meaning here. Inside the data root even an
// existing directory is checked, see DataRoot.
func MkdirAll(dir string, _ os.FileMode) error {
	p, err := currentPrincipals()
	if err != nil {
		return err
	}
	// Before anything is created: a link planted at or below the data root would
	// otherwise make this process create directories where its planter chose.
	if _, err := checkDataRoot(dir, p); err != nil {
		return err
	}
	if err := makeDirs(dir); err != nil {
		return err
	}
	// Again, for what appeared while the directories were made.
	_, err = checkDataRoot(dir, p)
	return err
}

func makeDirs(dir string) error {
	fi, err := os.Stat(dir)
	switch {
	case err == nil && fi.IsDir():
		return nil
	case err == nil:
		return &os.PathError{Op: "mkdir", Path: dir, Err: syscall.ENOTDIR}
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	if parent := filepath.Dir(dir); parent != dir {
		if err := makeDirs(parent); err != nil {
			return err
		}
	}
	return createPrivateDir(dir)
}

func createPrivateDir(dir string) error {
	p, err := currentPrincipals()
	if err != nil {
		return err
	}
	attributes, err := newSecurityAttributes(p.grantees(), sddlInheritToChildren)
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	err = windows.CreateDirectory(name, attributes)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return nil // another process made it first; Restrict checks it
	}
	if err != nil {
		return &os.PathError{Op: "mkdir", Path: dir, Err: err}
	}
	return nil
}

// OpenAppend opens path for appending. A new file is created with a protected
// access list from the start. An existing one has to be a regular file owned by
// a trusted account, and gets the same list if it has another. The checks are
// made on the handle that is returned, so they hold for the file that is
// written to, and a link is refused instead of followed.
func OpenAppend(path string) (*os.File, error) {
	p, err := currentPrincipals()
	if err != nil {
		return nil, err
	}
	if err := secureLogDirectory(path, p); err != nil {
		return nil, err
	}
	handle, err := createOrOpenLog(path, p)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if err := restrictOpenFile(handle, path, p); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

// secureLogDirectory is the part of OpenAppend that concerns the directory of
// the log. Inside the data root it is checked like the rest of the root, and
// repaired like a directory of the daemon; anywhere else it belongs to whoever
// chose the location.
func secureLogDirectory(logPath string, p principals) error {
	inside, err := checkDataRoot(logPath, p)
	if err != nil || !inside {
		return err
	}
	if _, err := restrictTree(filepath.Dir(logPath), p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func createOrOpenLog(path string, p principals) (windows.Handle, error) {
	attributes, err := newSecurityAttributes(p.grantees(), sddlNoInheritance)
	if err != nil {
		return 0, err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	handle, err := windows.CreateFile(name, windows.FILE_APPEND_DATA|repairAccess, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		attributes, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, explainDenial(plainLocation(path), &os.PathError{Op: "open", Path: path, Err: err}, p)
	}
	return handle, nil
}

func restrictOpenFile(handle windows.Handle, path string, p principals) error {
	found, err := inspect(handle, path, p)
	if err != nil || found.private {
		return err
	}
	return replaceList(handle, path, p.grantees(), sddlNoInheritance)
}

// IsPrivate reports whether the access list of path (a link itself, not what it
// points to) gives nothing to anyone but SYSTEM, Administrators and the account
// of this process.
func IsPrivate(path string) (private bool, err error) {
	p, err := currentPrincipals()
	if err != nil {
		return false, err
	}
	handle, err := openObject(path, inspectAccess)
	if err != nil {
		return false, err
	}
	defer closeInto(handle, &err)
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, &os.PathError{Op: "read access list", Path: path, Err: err}
	}
	private, err = daclIsPrivate(sd, p)
	if err != nil {
		return false, &os.PathError{Op: "read access list", Path: path, Err: err}
	}
	return private, nil
}

// Restrict makes path, and everything below it, private to SYSTEM,
// Administrators and the account of this process, and reports whether it had to
// change anything. An object that is a link, or that an untrusted account owns,
// is refused with an error instead: that account can always give itself access
// again. A list that is already private is left alone, so a directory under the
// user's own %TEMP% is not rewritten.
func Restrict(path string) (changed bool, err error) {
	repaired, err := restrictAndCount(path)
	return repaired > 0, err
}

// RestrictAndWarn is Restrict for a directory of the daemon, and logs a warning
// that names label when it had to change something.
func RestrictAndWarn(log *slog.Logger, label, dir string) error {
	repaired, err := restrictAndCount(dir)
	if err != nil {
		return err
	}
	if repaired > 0 {
		log.Warn(label+restrictedMessageSuffix, "dir", dir, "repaired", repaired)
	}
	return nil
}

func restrictAndCount(path string) (repaired int, err error) {
	p, err := currentPrincipals()
	if err != nil {
		return 0, err
	}
	return restrictTree(path, p)
}
