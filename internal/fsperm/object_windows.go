//go:build windows

package fsperm

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

const (
	// inspectAccess is enough to read the attributes, the owner and the access list.
	inspectAccess = windows.READ_CONTROL | windows.FILE_READ_ATTRIBUTES
	// repairAccess adds the right to replace the access list.
	repairAccess = inspectAccess | windows.WRITE_DAC
	// None of these rights touches the data, so the share mode never stands in
	// the way of the process that has the file open.
	shareEverything = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
	// Open a link itself instead of what it points to (the attributes then
	// tell it is one); BACKUP_SEMANTICS is what lets a directory be opened.
	doNotFollow = windows.FILE_FLAG_OPEN_REPARSE_POINT | windows.FILE_FLAG_BACKUP_SEMANTICS
)

var (
	// ErrUntrustedOwner means an object is owned by an account that could
	// re-grant itself access whatever list is put on the object.
	ErrUntrustedOwner = errors.New("untrusted owner")
	// ErrReparsePoint means a path is a junction, symbolic link or another
	// reparse point, which would send the daemon's writes somewhere else.
	ErrReparsePoint = errors.New("reparse point (junction or symbolic link) not allowed")
	// ErrHardLink means a file has more than one name. The access list
	// belongs to the file and not to a name, so a file that is reachable
	// through a name outside the tree cannot be made private from inside it.
	ErrHardLink = errors.New("file with more than one hard link not allowed")
	// ErrUnreachable means a directory lists a name that cannot be opened,
	// which an object that was merely removed in the meantime does not do.
	ErrUnreachable = errors.New("listed in its directory but cannot be opened")
)

// singleName is the link count of a file that has no other name.
const singleName = 1

// finding is what is known about an object that passed the checks.
type finding struct {
	isDir, private bool
}

// openObject opens an existing file or directory without following a link.
func openObject(path string, access uint32) (windows.Handle, error) {
	return openLocation(plainLocation(path), access)
}

// openLocation is openObject for a place whose exact spelling differs from the
// one that is shown.
func openLocation(at location, access uint32) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(at.exact)
	if err != nil {
		return 0, err
	}
	handle, err := windows.CreateFile(name, access, shareEverything, nil, windows.OPEN_EXISTING, doNotFollow, 0)
	if err != nil {
		return 0, &os.PathError{Op: "open", Path: at.shown, Err: err}
	}
	return handle, nil
}

// closeInto closes handle and reports a failure through err unless err already
// carries one.
func closeInto(handle windows.Handle, err *error) {
	if closeErr := windows.CloseHandle(handle); closeErr != nil && *err == nil {
		*err = closeErr
	}
}

// inspect checks the object behind handle: it must not be a link, its owner
// must be trusted, a file must have no other name, and it tells whether its
// access list is private already. Everything is read from the handle, so a
// path swapped for another object after the open changes nothing.
func inspect(handle windows.Handle, path string, p principals) (finding, error) {
	info, err := fileInformation(handle)
	if err != nil {
		return finding{}, &os.PathError{Op: "stat", Path: path, Err: err}
	}
	attributes := info.FileAttributes
	if err := checkAttributes(path, attributes); err != nil {
		return finding{}, err
	}
	isDir := attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return finding{}, &os.PathError{Op: "read access list", Path: path, Err: err}
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return finding{}, &os.PathError{Op: "read owner", Path: path, Err: err}
	}
	// The owner comes first: an object nobody trusts is refused for that,
	// whatever else is wrong with it.
	if err := checkOwner(path, owner, p); err != nil {
		return finding{}, err
	}
	if err := checkLinks(path, isDir, info.NumberOfLinks); err != nil {
		return finding{}, err
	}
	private, err := daclIsPrivate(sd, p)
	if err != nil {
		return finding{}, &os.PathError{Op: "read access list", Path: path, Err: err}
	}
	return finding{isDir: isDir, private: private}, nil
}

func fileInformation(handle windows.Handle) (windows.ByHandleFileInformation, error) {
	var info windows.ByHandleFileInformation
	err := windows.GetFileInformationByHandle(handle, &info)
	return info, err
}

// checkLinks refuses a file that has another name. A directory has none, and
// reports a count of one.
func checkLinks(path string, isDir bool, links uint32) error {
	if isDir || links <= singleName {
		return nil
	}
	return fmt.Errorf("%w: %s has %d names", ErrHardLink, path, links)
}

func checkAttributes(path string, attributes uint32) error {
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("%w: %s", ErrReparsePoint, path)
	}
	return nil
}

// checkOwner refuses an object that an untrusted account owns. Its owner can
// always rewrite the access list, so there is nothing to repair.
func checkOwner(path string, owner *windows.SID, p principals) error {
	if p.trusts(owner) {
		return nil
	}
	return fmt.Errorf("%w: %s is owned by %s, and only SYSTEM, Administrators and the account of this process may own it",
		ErrUntrustedOwner, path, describeSID(owner))
}

func describeSID(sid *windows.SID) string {
	if sid == nil {
		return "nobody"
	}
	account, domain, _, err := sid.LookupAccount("")
	if err != nil {
		return sid.String()
	}
	return fmt.Sprintf(`%s\%s (%s)`, domain, account, sid)
}

// explainDenial improves an "access denied" for at: when the object has an
// owner or is a link that must not be used, that is the reason to report, and it
// is a clearer one. Any other error is returned as it is.
func explainDenial(at location, err error, p principals) (explained error) {
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return err
	}
	handle, openErr := openLocation(at, inspectAccess)
	if openErr != nil {
		return err
	}
	defer closeInto(handle, &explained)
	if _, inspectErr := inspect(handle, at.shown, p); inspectErr != nil {
		return inspectErr
	}
	return err
}

// replaceList gives the object behind handle a protected access list of full
// access for grantees.
func replaceList(handle windows.Handle, path string, grantees []*windows.SID, aceFlags string) error {
	sd, err := newDescriptor(grantees, aceFlags)
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("build the access list: %w", err)
	}
	err = windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
	if err != nil {
		return &os.PathError{Op: "restrict", Path: path, Err: err}
	}
	return nil
}
