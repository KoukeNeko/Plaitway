//go:build windows

package fsperm

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// sddlProtectedDACL starts a DACL that takes nothing from the parent.
	sddlProtectedDACL = "D:P"
	// sddlFullAccess is the right to do anything with the object.
	sddlFullAccess = "FA"
	// A directory passes its list on to the files and directories created in it.
	sddlInheritToChildren = "OICI"
	sddlNoInheritance     = ""
)

func inheritanceFor(isDir bool) string {
	if isDir {
		return sddlInheritToChildren
	}
	return sddlNoInheritance
}

func newSecurityAttributes(grantees []*windows.SID, aceFlags string) (*windows.SecurityAttributes, error) {
	sd, err := newDescriptor(grantees, aceFlags)
	if err != nil {
		return nil, err
	}
	return &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}, nil
}

func newDescriptor(grantees []*windows.SID, aceFlags string) (*windows.SECURITY_DESCRIPTOR, error) {
	var sddl strings.Builder
	sddl.WriteString(sddlProtectedDACL)
	for _, sid := range grantees {
		fmt.Fprintf(&sddl, "(A;%s;%s;;;%s)", aceFlags, sddlFullAccess, sid.String())
	}
	sd, err := windows.SecurityDescriptorFromString(sddl.String())
	if err != nil {
		return nil, fmt.Errorf("build the security descriptor %q: %w", sddl.String(), err)
	}
	return sd, nil
}

// daclIsPrivate reports whether the access list in sd gives nothing to anyone
// but the accounts p trusts. An entry that denies access is harmless; a missing
// list gives everybody everything.
func daclIsPrivate(sd *windows.SECURITY_DESCRIPTOR, p principals) (bool, error) {
	if sd == nil {
		return false, nil
	}
	acl, _, err := sd.DACL()
	if errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) || (err == nil && acl == nil) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the access list: %w", err)
	}
	return grantsOnlyTo(acl, p)
}

func grantsOnlyTo(acl *windows.ACL, p principals) (bool, error) {
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return false, fmt.Errorf("read entry %d of the access list: %w", i, err)
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			if !p.trusts((*windows.SID)(unsafe.Pointer(&ace.SidStart))) {
				return false, nil
			}
		default:
			return false, nil
		}
	}
	return true, nil
}
