//go:build windows

// Package fspermtest builds the hostile layouts that the tests of the packages
// using fsperm need: a junction, an access list that is not private.
package fspermtest

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

// MakeJunction makes link a junction to target. Unlike a symbolic link, it
// needs no privilege, which is why an attacker can make one.
func MakeJunction(t *testing.T, link, target string) {
	t.Helper()
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J %s %s: %v\n%s", link, target, err, out)
	}
}

// SetList replaces the access list of path with the one sddl describes. The
// list is protected from inheritance when sddl says so ("D:P(...)") and
// inherits otherwise, the way a directory under %TEMP% does.
func SetList(t *testing.T, path, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	var info windows.SECURITY_INFORMATION = windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION
	if control, _, _ := sd.Control(); control&windows.SE_DACL_PROTECTED != 0 {
		info = windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, info, nil, nil, acl, nil); err != nil {
		t.Fatalf("set the access list of %s: %v", path, err)
	}
}
