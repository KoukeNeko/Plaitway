//go:build windows

package fsperm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/KoukeNeko/Plaitway/internal/fsperm/fspermtest"
)

// The files of these tests belong to whoever runs them, so the account of the
// test process stands in for the administrators: it owns the objects and may
// be named in their lists. Everything else is an ordinary user, which is what
// the check exists to find.
const (
	usersSID = "BU"
	// The SDDL rights are spelled as masks, so that each test names the one
	// right it is about.
	rightReadExecute     = "0x1200a9"
	rightAddFile         = "0x2"
	rightAddDirectory    = "0x4"
	rightWriteEA         = "0x10"
	rightDeleteChild     = "0x40"
	rightDelete          = "0x10000"
	rightWriteDAC        = "0x40000"
	rightWriteOwner      = "0x80000"
	rightGenericWrite    = "0x40000000"
	rightWriteAttributes = "0x100"
	rightAll             = "FA"
)

func adminOnlyTrusted(t *testing.T) []*windows.SID {
	t.Helper()
	trusted, err := adminSIDs()
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return append(trusted, user.User.Sid)
}

// listFor is a protected access list for the test user and SYSTEM, plus the
// entries given.
func listFor(t *testing.T, extra ...string) string {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;%s)%s", user.User.Sid, strings.Join(extra, ""))
}

func allow(flags, rights, sid string) string {
	return fmt.Sprintf("(A;%s;%s;;;%s)", flags, rights, sid)
}

// installTree makes <root>\above\dir\program.exe, each with the access list
// given for it, and returns the path of the program.
func installTree(t *testing.T, aboveList, dirList, fileList string) string {
	t.Helper()
	root := t.TempDir()
	above := filepath.Join(root, "above")
	dir := filepath.Join(above, "dir")
	program := filepath.Join(dir, "program.exe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(program, []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Deepest first: a list that withholds the right to change the parent's
	// children must not stand in the way of the next call.
	fspermtest.SetList(t, program, fileList)
	fspermtest.SetList(t, dir, dirList)
	fspermtest.SetList(t, above, aboveList)
	return program
}

// checkInstallTree checks the three levels only; the directories of the real
// machine above the temporary directory are not what these tests are about.
func checkInstallTree(program string, trusted []*windows.SID) error {
	chain := pathChain(program)
	return checkChain(chain[len(chain)-3:], trusted)
}

func TestCheckAdminOnlyAcceptsAnAdministratorOnlyTreeThatUsersMayRead(t *testing.T) {
	readable := listFor(t, allow("OICI", rightReadExecute, usersSID))
	program := installTree(t, readable, readable, readable)

	if err := checkInstallTree(program, adminOnlyTrusted(t)); err != nil {
		t.Fatal(err)
	}
}

func TestCheckAdminOnlyRefusesEveryRightThatChangesTheExecutable(t *testing.T) {
	rights := map[string]string{
		"write data":     rightAddFile,
		"append data":    rightAddDirectory,
		"write EA":       rightWriteEA,
		"delete":         rightDelete,
		"change the DAC": rightWriteDAC,
		"take ownership": rightWriteOwner,
		"generic write":  rightGenericWrite,
		"full control":   rightAll,
	}
	for name, right := range rights {
		t.Run(name, func(t *testing.T) {
			closed := listFor(t, allow("OICI", rightReadExecute, usersSID))
			program := installTree(t, closed, closed, listFor(t, allow("", right, usersSID)))

			err := checkInstallTree(program, adminOnlyTrusted(t))

			if !errors.Is(err, ErrWritableByStandardUsers) || !strings.Contains(err.Error(), "program.exe") {
				t.Fatalf("a file that Users may %s: %v, want ErrWritableByStandardUsers naming the file", name, err)
			}
		})
	}
}

// A library next to the executable is loaded by it.
func TestCheckAdminOnlyRefusesUsersWhoCanAddToTheDirectoryOfTheExecutable(t *testing.T) {
	for name, right := range map[string]string{"add file": rightAddFile, "add subdirectory": rightAddDirectory, "delete child": rightDeleteChild} {
		t.Run(name, func(t *testing.T) {
			closed := listFor(t, allow("OICI", rightReadExecute, usersSID))
			program := installTree(t, closed, listFor(t, allow("OICI", right, usersSID)), closed)

			err := checkInstallTree(program, adminOnlyTrusted(t))

			if !errors.Is(err, ErrWritableByStandardUsers) || !strings.Contains(err.Error(), "dir") {
				t.Fatalf("a directory where Users may %s: %v, want it refused", name, err)
			}
		})
	}
}

// Adding a sibling to a directory above the executable changes nothing the
// path resolves to, which is how C:\ lets any user create a folder.
func TestCheckAdminOnlyAllowsUsersToAddBesideADirectoryAboveTheExecutable(t *testing.T) {
	closed := listFor(t, allow("OICI", rightReadExecute, usersSID))
	program := installTree(t, listFor(t, allow("", "0x6", usersSID)), closed, closed)

	if err := checkInstallTree(program, adminOnlyTrusted(t)); err != nil {
		t.Fatal(err)
	}
}

func TestCheckAdminOnlyRefusesUsersWhoCanReplaceADirectoryAboveTheExecutable(t *testing.T) {
	for name, right := range map[string]string{"delete child": rightDeleteChild, "delete": rightDelete, "change the DAC": rightWriteDAC, "take ownership": rightWriteOwner} {
		t.Run(name, func(t *testing.T) {
			closed := listFor(t, allow("OICI", rightReadExecute, usersSID))
			program := installTree(t, listFor(t, allow("", right, usersSID)), closed, closed)

			err := checkInstallTree(program, adminOnlyTrusted(t))

			if !errors.Is(err, ErrWritableByStandardUsers) || !strings.Contains(err.Error(), "above") {
				t.Fatalf("a parent where Users may %s: %v, want it refused", name, err)
			}
		})
	}
}

// An entry that only children inherit does not apply to the directory that
// carries it: Program Files hands CREATOR OWNER and Users rights this way.
func TestCheckAdminOnlyIgnoresEntriesThatOnlyChildrenInherit(t *testing.T) {
	inheritOnly := listFor(t, allow("OICIIO", rightAll, usersSID), allow("OICI", rightReadExecute, usersSID))
	program := installTree(t, inheritOnly, inheritOnly, listFor(t, allow("", rightReadExecute, usersSID)))

	if err := checkInstallTree(program, adminOnlyTrusted(t)); err != nil {
		t.Fatal(err)
	}
}

func TestCheckAdminOnlyIgnoresRightsThatDoNotChangeTheFile(t *testing.T) {
	closed := listFor(t, allow("OICI", rightReadExecute, usersSID))
	program := installTree(t, closed, closed, listFor(t, allow("", rightWriteAttributes, usersSID)))

	if err := checkInstallTree(program, adminOnlyTrusted(t)); err != nil {
		t.Fatal(err)
	}
}

// A list without any entry denies everyone, but a missing list allows
// everyone.
func TestCheckAdminOnlyRefusesAFileWithoutAnAccessList(t *testing.T) {
	closed := listFor(t, allow("OICI", rightReadExecute, usersSID))
	program := installTree(t, closed, closed, closed)
	if err := windows.SetNamedSecurityInfo(program, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	err := checkInstallTree(program, adminOnlyTrusted(t))

	if !errors.Is(err, ErrWritableByStandardUsers) {
		t.Fatalf("a file with a NULL access list: %v, want ErrWritableByStandardUsers", err)
	}
}

// ownByTestUser makes the test user the owner of the three levels of an install
// tree. An elevated process owns what it creates as the Administrators group,
// which is trusted, so the owner has to be set for a test about an untrusted one.
func ownByTestUser(t *testing.T, program string) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	chain := pathChain(program)
	for _, path := range chain[len(chain)-3:] {
		if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION, user.User.Sid, nil, nil, nil); err != nil {
			t.Fatalf("make the test user the owner of %s: %v", path, err)
		}
	}
}

func TestCheckAdminOnlyRefusesAnObjectOwnedByAnUntrustedAccount(t *testing.T) {
	closed := listFor(t, allow("OICI", rightReadExecute, usersSID))
	program := installTree(t, closed, closed, closed)
	ownByTestUser(t, program)
	withoutTestUser, err := adminSIDs()
	if err != nil {
		t.Fatal(err)
	}

	err = checkInstallTree(program, withoutTestUser)

	if !errors.Is(err, ErrUntrustedOwner) {
		t.Fatalf("an object owned by the test user, who is not trusted here: %v, want ErrUntrustedOwner", err)
	}
}

func TestCheckAdminOnlyRefusesAJunctionOnTheWay(t *testing.T) {
	closed := listFor(t, allow("OICI", rightReadExecute, usersSID))
	program := installTree(t, closed, closed, closed)
	link := filepath.Join(t.TempDir(), "link")
	fspermtest.MakeJunction(t, link, filepath.Dir(filepath.Dir(program)))

	err := checkAdminOnlyObject(link, rightsToReplaceAnObject, adminOnlyTrusted(t))

	if !errors.Is(err, ErrReparsePoint) {
		t.Fatalf("a junction: %v, want ErrReparsePoint", err)
	}
}

func TestCheckAdminOnlyRefusesPathsThatAreNotOnALocalFixedDisk(t *testing.T) {
	for _, path := range []string{`\\server\share\plaitwayd.exe`, `\\.\pipe\plaitwayd.exe`} {
		if err := checkAdminOnly(path, adminOnlyTrusted(t)); !errors.Is(err, ErrNotLocalFixedDisk) {
			t.Errorf("checkAdminOnly(%s) = %v, want ErrNotLocalFixedDisk", path, err)
		}
	}
}

func TestCheckAdminOnlyRefusesARemovableOrNetworkDrive(t *testing.T) {
	for _, driveType := range []uint32{windows.DRIVE_REMOVABLE, windows.DRIVE_REMOTE, windows.DRIVE_CDROM, windows.DRIVE_RAMDISK} {
		original := driveTypeOf
		driveTypeOf = func(*uint16) uint32 { return driveType }
		err := checkAdminOnly(`C:\Program Files\Plaitway\plaitwayd.exe`, adminOnlyTrusted(t))
		driveTypeOf = original
		if !errors.Is(err, ErrNotLocalFixedDisk) {
			t.Errorf("a drive of type %d: %v, want ErrNotLocalFixedDisk", driveType, err)
		}
	}
}

// Nobody can delete or rename a volume root, and data volumes let Authenticated
// Users modify theirs; any other directory above the executable is checked for
// the right to delete it.
func TestRightsToCheckAtLeaveDeleteOutForTheVolumeRootOnly(t *testing.T) {
	tests := []struct {
		path      string
		remaining int
		want      uint32
	}{
		{`C:\`, 4, rightsToReplaceAnObject &^ windows.DELETE},
		{`C:\Program Files`, 3, rightsToReplaceAnObject},
		{`C:\Program Files\Plaitway`, 2, rightsToChangeContents},
		{`C:\Program Files\Plaitway\plaitwayd.exe`, 1, rightsToChangeContents},
		{`C:\`, 2, rightsToChangeContents},
	}
	for _, test := range tests {
		if got := rightsToCheckAt(test.path, test.remaining); got != test.want {
			t.Errorf("rightsToCheckAt(%s, %d) = %#x, want %#x", test.path, test.remaining, got, test.want)
		}
	}
}

func TestPathChainListsTheRootThenEveryDirectoryThenThePath(t *testing.T) {
	got := pathChain(`C:\Program Files\Plaitway\plaitwayd.exe`)
	want := []string{`C:\`, `C:\Program Files`, `C:\Program Files\Plaitway`, `C:\Program Files\Plaitway\plaitwayd.exe`}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("pathChain = %q, want %q", got, want)
	}
	if verbatim := pathChain(`\\?\C:\a\b.exe`); verbatim[0] != `C:\` {
		t.Fatalf("pathChain of a verbatim path starts at %q, want C:\\", verbatim[0])
	}
}

// What the real machine looks like: Windows is owned by TrustedInstaller and is
// the model of an administrator-only tree.
func TestCheckAdminOnlyPathAcceptsTheSystemDirectory(t *testing.T) {
	system, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckAdminOnlyPath(filepath.Join(system, "cmd.exe")); err != nil {
		t.Fatalf("CheckAdminOnlyPath(System32\\cmd.exe) = %v, want it accepted", err)
	}
}

func TestCheckAdminOnlyPathRefusesAFileOfTheTestUser(t *testing.T) {
	program := filepath.Join(t.TempDir(), "program.exe")
	if err := os.WriteFile(program, []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := CheckAdminOnlyPath(program)

	if err == nil {
		t.Fatal("a file in the temporary directory of a user was accepted")
	}
}
