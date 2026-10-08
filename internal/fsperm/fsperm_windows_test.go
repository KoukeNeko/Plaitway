//go:build windows

package fsperm

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/KoukeNeko/Plaitway/internal/fsperm/fspermtest"
)

// fileAllAccess is what "FA" grants: FILE_ALL_ACCESS.
const fileAllAccess = 0x1F01FF

// everyoneOpenList lets Everyone and the user of the test into a directory and
// what it holds, protected from inheritance so that it stays whatever the
// parent says.
func everyoneOpenList(p principals, protected bool) string {
	prefix := "D:"
	if protected {
		prefix = "D:P"
	}
	return fmt.Sprintf("%s(A;OICI;FA;;;WD)(A;OICI;FA;;;%s)", prefix, p.user)
}

type entry struct {
	sid   string
	mask  uint32
	flags uint8
}

// readList returns the access list of path as it is on disk.
func readList(t *testing.T, path string) (protected bool, entries []entry) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("read the access list of %s: %v", path, err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		t.Fatalf("DACL of %s: %v, %v", path, acl, err)
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Fatalf("entry %d of %s has type %d, want only allow entries", i, path, ace.Header.AceType)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		entries = append(entries, entry{sid: sid.String(), mask: uint32(ace.Mask), flags: ace.Header.AceFlags})
	}
	slices.SortFunc(entries, func(a, b entry) int {
		if a.sid < b.sid {
			return -1
		}
		return 1
	})
	return control&windows.SE_DACL_PROTECTED != 0, entries
}

func sidStrings(sids ...*windows.SID) []string {
	out := make([]string, len(sids))
	for i, sid := range sids {
		out[i] = sid.String()
	}
	slices.Sort(out)
	return out
}

func entrySIDs(entries []entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.sid
	}
	return out
}

func mustPrincipals(t *testing.T) principals {
	t.Helper()
	p, err := currentPrincipals()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func mustSID(t *testing.T, text string) *windows.SID {
	t.Helper()
	sid, err := windows.StringToSid(text)
	if err != nil {
		t.Fatal(err)
	}
	return sid
}

func mustBePrivate(t *testing.T, path string, want bool) {
	t.Helper()
	got, err := IsPrivate(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("IsPrivate(%s) = %v, want %v", path, got, want)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

// useDataRoot makes root the data root of the daemon for the length of the test.
func useDataRoot(t *testing.T, root string) {
	t.Helper()
	previous := dataRoot
	dataRoot = func() (string, error) { return root, nil }
	t.Cleanup(func() { dataRoot = previous })
}

// useNewDataRoot makes a directory of the test the data root, which does not
// exist yet: the root of the production daemon is created by its first run.
func useNewDataRoot(t *testing.T) (root string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), dataDirName)
	useDataRoot(t, root)
	return root
}

// mustBeEmpty fails the test when something was put in dir.
func mustBeEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("%s holds %d entries, want none", dir, len(entries))
	}
}

// A new directory has the list it needs from the start: protected, full access
// for exactly the accounts of grantees, passed on to what is created in it.
func TestMkdirAllCreatesProtectedDirectories(t *testing.T) {
	p := mustPrincipals(t)
	root := filepath.Join(t.TempDir(), "state")
	leaf := filepath.Join(root, "profiles")
	if err := MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{root, leaf} {
		protected, entries := readList(t, dir)
		if !protected {
			t.Errorf("%s inherits from its parent", dir)
		}
		if got, want := entrySIDs(entries), sidStrings(p.grantees()...); !slices.Equal(got, want) {
			t.Errorf("%s grants %v, want %v", dir, got, want)
		}
		for _, e := range entries {
			if e.mask != fileAllAccess || e.flags != windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE {
				t.Errorf("%s entry %+v, want full access that its children inherit", dir, e)
			}
		}
		mustBePrivate(t, dir, true)
	}

	// What the daemon later creates in it needs no list of its own.
	file := filepath.Join(leaf, "profile")
	mustWrite(t, file, "key")
	mustBePrivate(t, file, true)
}

// The production daemon (SYSTEM, elevated) gets exactly SYSTEM and
// Administrators; the access list the helper applies is read back from disk.
func TestSystemAndAdministratorsOnlyList(t *testing.T) {
	p := mustPrincipals(t)
	dir := filepath.Join(t.TempDir(), "locked")
	mustMkdir(t, dir)
	// The owner can always rewrite the list, which lets the cleanup delete it.
	t.Cleanup(func() { fspermtest.SetList(t, dir, fmt.Sprintf("D:P(A;OICI;FA;;;%s)", p.user)) })

	handle, err := openObject(dir, repairAccess)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	if err := replaceList(handle, dir, []*windows.SID{p.system, p.administrators}, sddlInheritToChildren); err != nil {
		t.Fatal(err)
	}

	protected, entries := readList(t, dir)
	if !protected {
		t.Error("the list inherits from the parent")
	}
	if got, want := entrySIDs(entries), sidStrings(p.system, p.administrators); !slices.Equal(got, want) {
		t.Fatalf("list grants %v, want exactly SYSTEM and Administrators %v", got, want)
	}
	for _, e := range entries {
		if e.mask != fileAllAccess {
			t.Errorf("%s has access mask %#x, want full control", e.sid, e.mask)
		}
	}
	// Without an account of its own, nobody else is private to this directory.
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	onlySystem := principals{system: p.system, administrators: p.administrators, user: p.system}
	if got, err := daclIsPrivate(sd, onlySystem); err != nil || !got {
		t.Errorf("a list of SYSTEM and Administrators is private to SYSTEM: %v, %v", got, err)
	}
}

func TestRestrictRepairsAListThatLetsOthersIn(t *testing.T) {
	p := mustPrincipals(t)
	for name, sddl := range map[string]string{
		"everyone":  everyoneOpenList(p, false),
		"users":     fmt.Sprintf("D:(A;OICI;FRFX;;;BU)(A;OICI;FA;;;SY)(A;OICI;FA;;;%s)", p.user),
		"null list": "",
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "state")
			if err := MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if sddl == "" {
				// A NULL DACL: the object has no list, so everybody has full access.
				err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
					windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION, nil, nil, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				fspermtest.SetList(t, dir, sddl)
			}
			mustBePrivate(t, dir, false)

			changed, err := Restrict(dir)
			if err != nil || !changed {
				t.Fatalf("Restrict = %v, %v; want it to repair the list", changed, err)
			}
			mustBePrivate(t, dir, true)
			if protected, entries := readList(t, dir); !protected || slices.Contains(entrySIDs(entries), "S-1-1-0") {
				t.Errorf("after the repair protected = %v, entries %v", protected, entries)
			}
			if changed, err := Restrict(dir); err != nil || changed {
				t.Errorf("second Restrict = %v, %v; want nothing left to do", changed, err)
			}
		})
	}
}

func TestRestrictPassesTheRepairOnToWhatTheDirectoryHolds(t *testing.T) {
	p := mustPrincipals(t)
	dir := filepath.Join(t.TempDir(), "state")
	mustMkdir(t, dir)
	fspermtest.SetList(t, dir, everyoneOpenList(p, false))
	file := filepath.Join(dir, "profiles.json")
	mustWrite(t, file, "{}")
	mustBePrivate(t, file, false) // it inherited the open list

	if _, err := Restrict(dir); err != nil {
		t.Fatal(err)
	}
	mustBePrivate(t, file, true)
}

// A child that has a list of its own is not touched by repairing the directory
// above it, so each one has to be looked at: this is the pre-created profiles
// directory and profile file that Everyone could read.
func TestRestrictRepairsChildrenWithAListOfTheirOwn(t *testing.T) {
	p := mustPrincipals(t)
	root := filepath.Join(t.TempDir(), "state")
	if err := MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	profiles := filepath.Join(root, "profiles")
	nested := filepath.Join(profiles, "nested")
	mustMkdir(t, nested)
	key := filepath.Join(profiles, "office.profile")
	deepKey := filepath.Join(nested, "deep.profile")
	index := filepath.Join(root, "profiles.json")
	for _, dir := range []string{profiles, nested} {
		fspermtest.SetList(t, dir, everyoneOpenList(p, true))
	}
	for _, file := range []string{key, deepKey, index} {
		mustWrite(t, file, "PrivateKey = x")
		fspermtest.SetList(t, file, fmt.Sprintf("D:P(A;;FR;;;WD)(A;;FA;;;%s)", p.user))
		mustBePrivate(t, file, false)
	}

	changed, err := Restrict(root)
	if err != nil || !changed {
		t.Fatalf("Restrict = %v, %v; want it to repair the children", changed, err)
	}
	for _, path := range []string{root, profiles, nested, key, deepKey, index} {
		mustBePrivate(t, path, true)
		if _, entries := readList(t, path); slices.Contains(entrySIDs(entries), "S-1-1-0") {
			t.Errorf("%s still names Everyone: %v", path, entries)
		}
	}
	if changed, err := Restrict(root); err != nil || changed {
		t.Errorf("second Restrict = %v, %v; want nothing left to do", changed, err)
	}
}

// A list that names only the right accounts is not rewritten because it is
// inherited: a development state directory is not touched and nothing is
// reported.
func TestRestrictLeavesAnInheritedPrivateListAlone(t *testing.T) {
	// %TEMP% may grant more than this (other users, sandbox accounts), so the
	// parent is one that grants exactly what a private list does.
	parent := filepath.Join(t.TempDir(), "parent")
	if err := MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "state")
	mustMkdir(t, dir)
	if protected, _ := readList(t, dir); protected {
		t.Fatal("the directory does not inherit from its parent")
	}

	changed, err := Restrict(dir)
	if err != nil || changed {
		t.Fatalf("Restrict = %v, %v; want no change", changed, err)
	}
	if protected, _ := readList(t, dir); protected {
		t.Error("the list was made protected although it was private")
	}
}

func TestRestrictRepairsAFileWithoutInheritingToChildren(t *testing.T) {
	p := mustPrincipals(t)
	file := filepath.Join(t.TempDir(), "plaitwayd.log")
	mustWrite(t, file, "x")
	fspermtest.SetList(t, file, fmt.Sprintf("D:(A;;FA;;;WD)(A;;FA;;;%s)", p.user))

	if changed, err := Restrict(file); err != nil || !changed {
		t.Fatalf("Restrict = %v, %v; want it to repair the list", changed, err)
	}
	protected, entries := readList(t, file)
	if !protected {
		t.Error("the list inherits from the parent")
	}
	for _, e := range entries {
		if e.flags != 0 {
			t.Errorf("entry %+v of a file has inheritance flags", e)
		}
	}
	mustBePrivate(t, file, true)
}

// A junction in the tree would send the daemon's writes (private keys) to a
// directory that someone else chose, so Restrict refuses it and does not touch
// what it points to.
func TestRestrictRefusesAJunctionBelowTheRoot(t *testing.T) {
	p := mustPrincipals(t)
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	mustMkdir(t, outside)
	fspermtest.SetList(t, outside, everyoneOpenList(p, true))
	_, listBefore := readList(t, outside)
	root := filepath.Join(base, "state")
	if err := MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	fspermtest.MakeJunction(t, filepath.Join(root, "profiles"), outside)

	changed, err := Restrict(root)
	if !errors.Is(err, ErrReparsePoint) || changed {
		t.Fatalf("Restrict = %v, %v; want ErrReparsePoint", changed, err)
	}
	if !strings.Contains(err.Error(), "profiles") {
		t.Errorf("the error does not name the junction: %v", err)
	}
	mustBeEmpty(t, outside)
	if _, listAfter := readList(t, outside); !slices.Equal(listAfter, listBefore) {
		t.Errorf("the target of the junction was changed: %v -> %v", listBefore, listAfter)
	}
}

func TestRestrictRefusesARootThatIsAJunction(t *testing.T) {
	p := mustPrincipals(t)
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	mustMkdir(t, outside)
	fspermtest.SetList(t, outside, everyoneOpenList(p, true))
	_, listBefore := readList(t, outside)
	root := filepath.Join(base, "state")
	fspermtest.MakeJunction(t, root, outside)

	if _, err := Restrict(root); !errors.Is(err, ErrReparsePoint) {
		t.Fatalf("Restrict = %v, want ErrReparsePoint", err)
	}
	if _, listAfter := readList(t, outside); !slices.Equal(listAfter, listBefore) {
		t.Errorf("the target of the junction was changed: %v -> %v", listBefore, listAfter)
	}
}

func makeSymlinkOrSkip(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
			t.Skipf("creating a symbolic link needs a privilege this session lacks: %v", err)
		}
		t.Fatal(err)
	}
}

func TestRestrictRefusesASymbolicLinkToAFile(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	mustWrite(t, target, "keep")
	root := filepath.Join(base, "state")
	mustMkdir(t, root)
	makeSymlinkOrSkip(t, filepath.Join(root, "profiles.json"), target)

	if _, err := Restrict(root); !errors.Is(err, ErrReparsePoint) {
		t.Fatalf("Restrict = %v, want ErrReparsePoint", err)
	}
}

// The owner decides which accounts are trusted: SYSTEM, Administrators and the
// account of the process. Anyone else could give itself access again.
func TestOwnerTrust(t *testing.T) {
	p := principals{
		system:         mustSID(t, "S-1-5-18"),
		administrators: mustSID(t, "S-1-5-32-544"),
		user:           mustSID(t, "S-1-5-21-1111-2222-3333-1001"),
	}
	tests := []struct {
		name  string
		owner *windows.SID
		want  bool
	}{
		{"SYSTEM", mustSID(t, "S-1-5-18"), true},
		{"Administrators", mustSID(t, "S-1-5-32-544"), true},
		{"the account of the process", mustSID(t, "S-1-5-21-1111-2222-3333-1001"), true},
		{"another user", mustSID(t, "S-1-5-21-1111-2222-3333-1002"), false},
		{"Everyone", mustSID(t, "S-1-1-0"), false},
		{"Users", mustSID(t, "S-1-5-32-545"), false},
		{"a service account", mustSID(t, "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"), false},
		{"CREATOR OWNER", mustSID(t, "S-1-3-0"), false},
		{"no owner", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkOwner(`C:\x\profiles`, tt.owner, p)
			if (err == nil) != tt.want {
				t.Fatalf("checkOwner = %v, want trusted = %v", err, tt.want)
			}
			if err != nil && (!errors.Is(err, ErrUntrustedOwner) || !strings.Contains(err.Error(), `C:\x\profiles`)) {
				t.Errorf("error %q does not name the path and wrap ErrUntrustedOwner", err)
			}
		})
	}
}

func TestCheckAttributes(t *testing.T) {
	const path = `C:\x\profiles`
	for name, attributes := range map[string]uint32{
		"directory":        windows.FILE_ATTRIBUTE_DIRECTORY,
		"file":             windows.FILE_ATTRIBUTE_ARCHIVE,
		"hidden system":    windows.FILE_ATTRIBUTE_HIDDEN | windows.FILE_ATTRIBUTE_SYSTEM,
		"compressed":       windows.FILE_ATTRIBUTE_COMPRESSED,
		"directory marked": windows.FILE_ATTRIBUTE_DIRECTORY | windows.FILE_ATTRIBUTE_NOT_CONTENT_INDEXED,
	} {
		if err := checkAttributes(path, attributes); err != nil {
			t.Errorf("%s: %v, want it accepted", name, err)
		}
	}
	for name, attributes := range map[string]uint32{
		"junction":     windows.FILE_ATTRIBUTE_DIRECTORY | windows.FILE_ATTRIBUTE_REPARSE_POINT,
		"symbolic":     windows.FILE_ATTRIBUTE_ARCHIVE | windows.FILE_ATTRIBUTE_REPARSE_POINT,
		"only reparse": windows.FILE_ATTRIBUTE_REPARSE_POINT,
	} {
		if err := checkAttributes(path, attributes); !errors.Is(err, ErrReparsePoint) {
			t.Errorf("%s: %v, want ErrReparsePoint", name, err)
		}
	}
}

// foreignOwnedPath finds a real object that another
// account owns (TrustedInstaller owns the Windows files), since a session that
// is not elevated cannot hand an object to another user.
func foreignOwnedPath(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(os.Getenv("SystemRoot"), name)
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		t.Skipf("cannot read the owner of %s: %v", path, err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		t.Skipf("cannot read the owner of %s: %v", path, err)
	}
	if mustPrincipals(t).trusts(owner) {
		t.Skipf("%s is owned by %s, a trusted account here", path, owner)
	}
	return path
}

func TestRestrictRefusesADirectoryAnotherAccountOwns(t *testing.T) {
	path := foreignOwnedPath(t, "System32")
	changed, err := Restrict(path)
	if !errors.Is(err, ErrUntrustedOwner) || changed {
		t.Fatalf("Restrict(%s) = %v, %v; want ErrUntrustedOwner", path, changed, err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("the error does not name the path: %v", err)
	}
}

func TestOpenAppendRefusesAFileAnotherAccountOwns(t *testing.T) {
	path := foreignOwnedPath(t, filepath.Join("System32", "notepad.exe"))
	f, err := OpenAppend(path)
	if err == nil {
		f.Close()
	}
	if !errors.Is(err, ErrUntrustedOwner) {
		t.Fatalf("OpenAppend(%s) = %v, want ErrUntrustedOwner", path, err)
	}
}

func TestOpenAppendCreatesAPrivateFileAndAppends(t *testing.T) {
	p := mustPrincipals(t)
	path := filepath.Join(t.TempDir(), "plaitwayd.log")

	for _, line := range []string{"first\n", "second\n"} {
		f, err := OpenAppend(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(line); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}

	if got, err := os.ReadFile(path); err != nil || string(got) != "first\nsecond\n" {
		t.Fatalf("file = %q, %v; want both lines appended", got, err)
	}
	protected, entries := readList(t, path)
	if !protected {
		t.Error("the list inherits from the parent")
	}
	if got, want := entrySIDs(entries), sidStrings(p.grantees()...); !slices.Equal(got, want) {
		t.Errorf("the file grants %v, want %v", got, want)
	}
}

func TestOpenAppendRestrictsALogThatEarlierRunsLeftOpen(t *testing.T) {
	p := mustPrincipals(t)
	path := filepath.Join(t.TempDir(), "plaitwayd.log")
	mustWrite(t, path, "old\n")
	fspermtest.SetList(t, path, fmt.Sprintf("D:(A;;FA;;;WD)(A;;FA;;;%s)", p.user))

	f, err := OpenAppend(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	mustBePrivate(t, path, true)
}

// The log is opened where a link points only if the link is not one: a symbolic
// link at the log would make a privileged daemon append to any file.
func TestOpenAppendRefusesASymbolicLinkAndLeavesItsTargetAlone(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target.txt")
	mustWrite(t, target, "keep\n")
	link := filepath.Join(base, "plaitwayd.log")
	makeSymlinkOrSkip(t, link, target)

	f, err := OpenAppend(link)
	if err == nil {
		f.Close()
	}
	if !errors.Is(err, ErrReparsePoint) {
		t.Fatalf("OpenAppend = %v, want ErrReparsePoint", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "keep\n" {
		t.Errorf("the target of the link was changed: %q", got)
	}
}

// A symbolic link needs a privilege to make; a junction at the log's own path
// is another reparse point that the same check has to stop.
func TestOpenAppendRefusesAJunctionInPlaceOfTheLog(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	mustMkdir(t, outside)
	link := filepath.Join(base, "plaitwayd.log")
	fspermtest.MakeJunction(t, link, outside)

	f, err := OpenAppend(link)
	if err == nil {
		f.Close()
	}
	if !errors.Is(err, ErrReparsePoint) {
		t.Fatalf("OpenAppend = %v, want ErrReparsePoint", err)
	}
	mustBeEmpty(t, outside)
}

func TestOpenAppendOutsideTheDataRootLeavesTheDirectoryAlone(t *testing.T) {
	useNewDataRoot(t)
	p := mustPrincipals(t)
	dir := filepath.Join(t.TempDir(), "logs")
	mustMkdir(t, dir)
	fspermtest.SetList(t, dir, everyoneOpenList(p, true))
	_, before := readList(t, dir)

	file := filepath.Join(dir, "plaitwayd.log")
	f, err := OpenAppend(file)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	mustBePrivate(t, file, true)
	if _, after := readList(t, dir); !slices.Equal(after, before) {
		t.Errorf("the list of a directory outside the data root was rewritten: %v -> %v", before, after)
	}
	if err := MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, after := readList(t, dir); !slices.Equal(after, before) {
		t.Errorf("MkdirAll rewrote the list of an existing directory outside the data root")
	}
}

// The log directory of the production daemon is repaired every time the log is
// opened, like the state directory is when the store is.
func TestOpenAppendInsideTheDataRootRepairsTheLogDirectory(t *testing.T) {
	p := mustPrincipals(t)
	root := useNewDataRoot(t)
	logs := filepath.Join(root, "Logs")
	mustMkdir(t, logs)
	file := filepath.Join(logs, "plaitwayd.log")
	mustWrite(t, file, "old\n")
	for _, path := range []string{logs, file} {
		fspermtest.SetList(t, path, everyoneOpenList(p, true))
		mustBePrivate(t, path, false)
	}

	f, err := OpenAppend(file)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	mustBePrivate(t, logs, true)
	mustBePrivate(t, file, true)
}

// Looking at the data root in MkdirAll must not hide from the daemon that it
// then repairs the state directory: that is the one that logs a warning.
func TestMkdirAllInsideTheDataRootLeavesTheRepairAndItsWarningToRestrict(t *testing.T) {
	p := mustPrincipals(t)
	root := useNewDataRoot(t)
	mustMkdir(t, root)
	fspermtest.SetList(t, root, everyoneOpenList(p, true))

	if err := MkdirAll(filepath.Join(root, "profiles"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustBePrivate(t, root, false)

	var logged bytes.Buffer
	if err := RestrictAndWarn(slog.New(slog.NewTextHandler(&logged, nil)), "state directory", root); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logged.String(), "state directory was accessible to others") {
		t.Fatalf("the repair was not reported: %q", logged.String())
	}
	mustBePrivate(t, root, true)
}

// A data root that another account made is refused before anything is created
// in it. TrustedInstaller's System32 stands in for it: a session that is not
// elevated cannot make a directory owned by another account.
func TestDataRootOwnedByAnotherAccountIsRefusedBeforeAnythingIsCreated(t *testing.T) {
	foreign := foreignOwnedPath(t, "System32")
	useDataRoot(t, foreign)
	logFile := filepath.Join(foreign, "Logs", "plaitwayd.log")

	if err := MkdirAll(foreign, 0o755); !errors.Is(err, ErrUntrustedOwner) {
		t.Fatalf("MkdirAll = %v, want ErrUntrustedOwner", err)
	}
	f, err := OpenAppend(logFile)
	if err == nil {
		f.Close()
	}
	if !errors.Is(err, ErrUntrustedOwner) {
		t.Fatalf("OpenAppend = %v, want ErrUntrustedOwner", err)
	}
	if _, statErr := os.Stat(filepath.Dir(logFile)); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("the log directory exists: %v", statErr)
	}
}

func TestMkdirAllInsideTheDataRootRefusesAJunctionAsTheLogDirectory(t *testing.T) {
	root := useNewDataRoot(t)
	outside := filepath.Join(t.TempDir(), "outside")
	mustMkdir(t, outside)
	mustMkdir(t, root)
	logs := filepath.Join(root, "Logs")
	fspermtest.MakeJunction(t, logs, outside)

	if err := MkdirAll(logs, 0o755); !errors.Is(err, ErrReparsePoint) {
		t.Fatalf("MkdirAll = %v, want ErrReparsePoint", err)
	}
	f, err := OpenAppend(filepath.Join(logs, "plaitwayd.log"))
	if err == nil {
		f.Close()
	}
	if !errors.Is(err, ErrReparsePoint) {
		t.Fatalf("OpenAppend = %v, want ErrReparsePoint", err)
	}
	mustBeEmpty(t, outside)
}

// Found on a real elevated run: a junction at the data root made the daemon
// create Logs in the target before it refused. Refusing has to come first, or a
// standard user who plants the junction chooses where a privileged process makes
// directories.
func TestMkdirAllInsideTheDataRootCreatesNothingThroughAJunction(t *testing.T) {
	tests := []struct {
		name string
		// plant makes the junction and returns the directory MkdirAll is asked for.
		plant func(t *testing.T, root, outside string) string
	}{
		{"junction as the data root", func(t *testing.T, root, outside string) string {
			fspermtest.MakeJunction(t, root, outside)
			return filepath.Join(root, "Logs")
		}},
		{"junction below the data root", func(t *testing.T, root, outside string) string {
			mustMkdir(t, root)
			fspermtest.MakeJunction(t, filepath.Join(root, "profiles"), outside)
			return filepath.Join(root, "profiles", "new")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := useNewDataRoot(t)
			outside := filepath.Join(t.TempDir(), "outside")
			mustMkdir(t, outside)
			dir := test.plant(t, root, outside)

			if err := MkdirAll(dir, 0o755); !errors.Is(err, ErrReparsePoint) {
				t.Fatalf("MkdirAll = %v, want ErrReparsePoint", err)
			}
			mustBeEmpty(t, outside)
		})
	}
}

func TestDataRootIsBelowProgramData(t *testing.T) {
	programData, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, windows.KF_FLAG_DEFAULT)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DataRoot()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(programData, "Plaitway"); got != want {
		t.Fatalf("DataRoot = %q, want %q", got, want)
	}
}

func TestMkdirAllRefusesAFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	mustWrite(t, file, "")
	if err := MkdirAll(filepath.Join(file, "dir"), 0o700); err == nil {
		t.Fatal("MkdirAll created a directory below a file")
	}
}

func TestMkdirAllLeavesAnExistingDirectoryAlone(t *testing.T) {
	dir := t.TempDir()
	protectedBefore, entriesBefore := readList(t, dir)
	if err := MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	protectedAfter, entriesAfter := readList(t, dir)
	if protectedAfter != protectedBefore || !slices.Equal(entriesAfter, entriesBefore) {
		t.Error("MkdirAll changed the list of an existing directory")
	}
}

func TestIsPrivateFailsForAMissingPath(t *testing.T) {
	if _, err := IsPrivate(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("IsPrivate of a missing path succeeded")
	}
}

func TestRestrictAndWarnReportsARepairOnceAndNothingElse(t *testing.T) {
	p := mustPrincipals(t)
	var logged bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logged, nil))
	dir := filepath.Join(t.TempDir(), "state")
	if err := MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := RestrictAndWarn(log, "state directory", dir); err != nil {
			t.Fatal(err)
		}
	}
	if logged.Len() != 0 {
		t.Fatalf("a private directory was reported: %s", logged.String())
	}

	fspermtest.SetList(t, dir, everyoneOpenList(p, true))
	for range 2 {
		if err := RestrictAndWarn(log, "state directory", dir); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Count(logged.String(), "state directory was accessible to others"); got != 1 {
		t.Fatalf("the repair was reported %d times, want once: %s", got, logged.String())
	}
	if !strings.Contains(logged.String(), "level=WARN") || !strings.Contains(logged.String(), "repaired=1") {
		t.Errorf("warning %q lacks the level or the number of repaired objects", logged.String())
	}
}
