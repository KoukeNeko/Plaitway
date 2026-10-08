//go:build windows

package fsperm

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	// ErrWritableByStandardUsers means an account other than SYSTEM,
	// Administrators and TrustedInstaller can change a file, or a directory
	// above it, that a privileged process is about to trust.
	ErrWritableByStandardUsers = errors.New("can be changed by an account that is not SYSTEM, Administrators or TrustedInstaller")
	// ErrNotLocalFixedDisk means a path is on a network share, a removable
	// drive or somewhere else that is not there for the whole life of the
	// machine and not administered by it.
	ErrNotLocalFixedDisk = errors.New("not on a local fixed disk")
)

const (
	// trustedInstallerSID is NT SERVICE\TrustedInstaller, the owner of the
	// Windows and Program Files trees.
	trustedInstallerSID = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"
	// creatorOwnerSID is a template that becomes the creator's SID in a child; an
	// entry that names it and is not inherit-only matches no token.
	creatorOwnerSID = "S-1-3-0"

	fileDeleteChild = 0x40

	// rightsToReplaceAnObject lets an account take the object away or hand
	// itself everything. Held on a directory above the executable, it lets the
	// account put another directory in its place.
	rightsToReplaceAnObject = windows.DELETE | fileDeleteChild | windows.WRITE_DAC | windows.WRITE_OWNER
	// rightsToChangeContents adds what changes the object's contents. Held on
	// the executable it rewrites the code; held on the directory of the
	// executable it plants a library next to it.
	rightsToChangeContents = rightsToReplaceAnObject | windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA
	// genericRightsToWrite are in an access list unmapped only on entries that
	// are inherit-only, but a list can be written by hand.
	genericRightsToWrite = windows.GENERIC_WRITE | windows.GENERIC_ALL

	accessAllowedCallbackACEType = 0x9

	// The executable and the directory it is in are checked for every right that
	// changes contents; the directories above it only for the ones that replace.
	objectsCheckedForContents = 2
)

// CheckAdminOnlyPath refuses a path that a standard user could use to change
// what a privileged process runs when it starts path: a file, or any directory
// above it, that is owned by anyone but SYSTEM, Administrators or
// TrustedInstaller, that is a junction or a symbolic link, or whose access list
// lets another account change it. The directory of path is held to the same
// standard as the file, because a library placed next to an executable is
// loaded by it. The directories above that only have to keep their children in
// place: a standard user who can add a sibling to a directory in the path has
// changed nothing the path resolves to. Program Files passes; a profile or a
// temporary directory does not.
func CheckAdminOnlyPath(path string) error {
	trusted, err := adminSIDs()
	if err != nil {
		return err
	}
	return checkAdminOnly(path, trusted)
}

// adminSIDs are the accounts that may own and change a path that is to be run
// with privileges.
func adminSIDs() ([]*windows.SID, error) {
	trusted := make([]*windows.SID, 0, 3)
	for _, wellKnown := range []windows.WELL_KNOWN_SID_TYPE{windows.WinLocalSystemSid, windows.WinBuiltinAdministratorsSid} {
		sid, err := windows.CreateWellKnownSid(wellKnown)
		if err != nil {
			return nil, fmt.Errorf("look up a well-known account: %w", err)
		}
		trusted = append(trusted, sid)
	}
	installer, err := windows.StringToSid(trustedInstallerSID)
	if err != nil {
		return nil, fmt.Errorf("look up TrustedInstaller: %w", err)
	}
	return append(trusted, installer), nil
}

func checkAdminOnly(path string, trusted []*windows.SID) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := requireLocalFixedDisk(absolute); err != nil {
		return err
	}
	return checkChain(pathChain(absolute), trusted)
}

// checkChain checks the objects of a path in order, from the volume root to the
// path itself.
func checkChain(chain []string, trusted []*windows.SID) error {
	for position, objectPath := range chain {
		rights := rightsToCheckAt(objectPath, len(chain)-position)
		if err := checkAdminOnlyObject(objectPath, rights, trusted); err != nil {
			return err
		}
	}
	return nil
}

// rightsToCheckAt says what must not be open to a standard user on an object
// that has remaining objects of the chain left, counting itself.
func rightsToCheckAt(objectPath string, remaining int) uint32 {
	switch {
	case remaining <= objectsCheckedForContents:
		return rightsToChangeContents
	case filepath.Dir(objectPath) == objectPath:
		// Nobody can delete or rename a volume root, and a data volume lets
		// Authenticated Users modify it, which includes deleting.
		return rightsToReplaceAnObject &^ windows.DELETE
	default:
		return rightsToReplaceAnObject
	}
}

// driveTypeOf is replaced by the tests: a machine has no removable drive to ask
// about when they run.
var driveTypeOf = windows.GetDriveType

// requireLocalFixedDisk refuses a share and a removable drive: its access
// lists are not the ones of this machine, and it may be gone when the service
// starts at boot.
func requireLocalFixedDisk(absolute string) error {
	volume := filepath.VolumeName(strings.TrimPrefix(absolute, verbatimPrefix))
	if len(volume) != len(`C:`) || volume[1] != ':' {
		return fmt.Errorf("%w: %s", ErrNotLocalFixedDisk, absolute)
	}
	root, err := windows.UTF16PtrFromString(volume + `\`)
	if err != nil {
		return err
	}
	if driveTypeOf(root) != windows.DRIVE_FIXED {
		return fmt.Errorf("%w: %s", ErrNotLocalFixedDisk, absolute)
	}
	return nil
}

// pathChain lists the volume root, every directory below it and path itself, in
// that order.
func pathChain(absolute string) []string {
	absolute = strings.TrimPrefix(absolute, verbatimPrefix)
	var chain []string
	for current := absolute; ; current = filepath.Dir(current) {
		chain = append([]string{current}, chain...)
		if filepath.Dir(current) == current {
			return chain
		}
	}
}

func checkAdminOnlyObject(path string, rights uint32, trusted []*windows.SID) (err error) {
	handle, err := openObject(path, inspectAccess)
	if err != nil {
		return err
	}
	defer closeInto(handle, &err)

	info, err := fileInformation(handle)
	if err != nil {
		return fmt.Errorf("read the attributes of %s: %w", path, err)
	}
	if err := checkAttributes(path, info.FileAttributes); err != nil {
		return err
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read the access list of %s: %w", path, err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("read the owner of %s: %w", path, err)
	}
	if !containsSID(trusted, owner) {
		return fmt.Errorf("%w: %s is owned by %s", ErrUntrustedOwner, path, describeSID(owner))
	}
	writer, err := untrustedWriter(sd, rights, trusted)
	if err != nil {
		return fmt.Errorf("check the access list of %s: %w", path, err)
	}
	if writer != nil {
		return fmt.Errorf("%w: %s grants it to %s", ErrWritableByStandardUsers, path, describeSID(writer))
	}
	return nil
}

// untrustedWriter returns the first account outside trusted that an entry of
// the access list allows any of rights, or nil. An entry that denies is not
// looked at: an access list is read in order, and treating a deny as absent
// can only refuse a path that is fine, never accept one that is not. A missing
// list gives everyone everything.
func untrustedWriter(sd *windows.SECURITY_DESCRIPTOR, rights uint32, trusted []*windows.SID) (*windows.SID, error) {
	acl, _, err := sd.DACL()
	if errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) || (err == nil && acl == nil) {
		return windows.StringToSid("S-1-1-0") // Everyone
	}
	if err != nil {
		return nil, err
	}
	inert, err := windows.StringToSid(creatorOwnerSID)
	if err != nil {
		return nil, err
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return nil, fmt.Errorf("read entry %d: %w", i, err)
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE, accessAllowedCallbackACEType:
		default:
			return nil, fmt.Errorf("entry %d is of type %d, which this check does not understand", i, ace.Header.AceType)
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || uint32(ace.Mask)&(rights|genericRightsToWrite) == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !containsSID(trusted, sid) && !sid.Equals(inert) {
			return sid, nil
		}
	}
	return nil, nil
}

func containsSID(sids []*windows.SID, sid *windows.SID) bool {
	if sid == nil {
		return false
	}
	for _, candidate := range sids {
		if candidate.Equals(sid) {
			return true
		}
	}
	return false
}
