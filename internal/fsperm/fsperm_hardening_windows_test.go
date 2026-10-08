//go:build windows

package fsperm

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/KoukeNeko/Plaitway/internal/fsperm/fspermtest"
)

// exactPath spells the absolute path so that Windows leaves every name in it
// alone: the only way to make, reach and remove "dotted." or "spaced ". It adds
// the prefix to the text as it is, because turning the text into an absolute
// path would already drop the dot.
func exactPath(t *testing.T, path string) string {
	t.Helper()
	if !filepath.IsAbs(path) {
		t.Fatalf("%s is not absolute", path)
	}
	return verbatimPrefix + path
}

// removeAllExact removes dir at cleanup, before t.TempDir does: its own removal
// goes through the normalised spelling and cannot delete such names.
func removeAllExact(t *testing.T, dir string) {
	t.Helper()
	t.Cleanup(func() {
		if err := os.RemoveAll(exactPath(t, dir)); err != nil {
			t.Errorf("remove %s: %v", dir, err)
		}
	})
}

// openFileList is what an earlier run could have left on a file: Everyone may
// read it, and the account of the test may do anything.
func openFileList(p principals) string {
	return fmt.Sprintf("D:P(A;;FR;;;WD)(A;;FA;;;%s)", p.user)
}

func mustNotNameEveryone(t *testing.T, path string) {
	t.Helper()
	if _, entries := readList(t, path); slices.Contains(entrySIDs(entries), "S-1-1-0") {
		t.Errorf("%s still names Everyone: %v", path, entries)
	}
}

// mustList fails the test unless dir lists exactly these names, which shows that
// the odd ones were made as they are written and not normalised on the way.
func mustList(t *testing.T, dir string, want []string) {
	t.Helper()
	entries, err := os.ReadDir(exactPath(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("%s lists %q, want %q", dir, got, want)
	}
}

func mustLink(t *testing.T, existing, link string) {
	t.Helper()
	if err := os.Link(existing, link); err != nil {
		t.Fatal(err)
	}
}

// unusedDriveRoot finds a drive letter that names nothing.
func unusedDriveRoot(t *testing.T) string {
	t.Helper()
	for letter := 'Z'; letter >= 'A'; letter-- {
		root := string(letter) + `:\`
		if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
			return root
		}
	}
	t.Skip("every drive letter is in use")
	return ""
}

var errNoShortName = errors.New("the volume does not make short names")

// shortNameOf returns the 8.3 spelling of dir, which only exists on a volume
// that makes short names.
func shortNameOf(dir string) (string, error) {
	long, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return "", err
	}
	buffer := make([]uint16, windows.MAX_PATH)
	length, err := windows.GetShortPathName(long, &buffer[0], uint32(len(buffer)))
	if err != nil {
		return "", err
	}
	short := windows.UTF16ToString(buffer[:length])
	if strings.EqualFold(filepath.Base(short), filepath.Base(dir)) {
		return "", errNoShortName
	}
	return short, nil
}

func shortNameOrSkip(t *testing.T, dir string) string {
	t.Helper()
	short, err := shortNameOf(dir)
	if err != nil {
		t.Skipf("no short name for %s: %v", dir, err)
	}
	return short
}

// A data root with the layouts that the tests below look for: a directory of
// logs, a junction that leads into the root from outside, and a sibling that
// only shares the start of its name.
type rootLayout struct {
	root, logs, alias, elsewhere, lookalike string
}

func newRootLayout(t *testing.T) rootLayout {
	t.Helper()
	base := t.TempDir()
	layout := rootLayout{
		root:      filepath.Join(base, "Plaitway Data Root"),
		alias:     filepath.Join(base, "alias"),
		elsewhere: filepath.Join(base, "elsewhere"),
		lookalike: filepath.Join(base, "Plaitway Data Root X"),
	}
	layout.logs = filepath.Join(layout.root, "Logs")
	for _, dir := range []string{layout.logs, layout.elsewhere, filepath.Join(layout.lookalike, "Logs")} {
		mustMkdir(t, dir)
	}
	fspermtest.MakeJunction(t, layout.alias, layout.root)
	return layout
}

func TestIsBelow(t *testing.T) {
	const root = `C:\ProgramData\Plaitway`
	tests := []struct {
		path string
		want bool
	}{
		{`C:\ProgramData\Plaitway`, true},
		{`C:\ProgramData\Plaitway\Logs\plaitwayd.log`, true},
		{`c:\programdata\PLAITWAY\logs`, true},
		{`C:\ProgramData\Plaitway\..\Plaitway\run`, true},
		{`C:\ProgramData\PlaitwayX\Logs`, false},
		{`C:\ProgramData`, false},
		{`C:\ProgramData\Plaitway\..\Other`, false},
		{`D:\ProgramData\Plaitway`, false},
	}
	for _, tt := range tests {
		if got := isBelow(root, filepath.Clean(tt.path)); got != tt.want {
			t.Errorf("isBelow(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

// Every spelling of a place under the root is inside it: a path that is only
// written differently must not skip the checks of the data root.
func TestIsInsideRecognisesEverySpellingOfThePlace(t *testing.T) {
	layout := newRootLayout(t)
	logFile := filepath.Join(layout.logs, "plaitwayd.log")
	tests := []struct {
		name, path string
		want       bool
	}{
		{"plain", logFile, true},
		{"the root itself", layout.root, true},
		{"verbatim prefix", exactPath(t, logFile), true},
		{"verbatim root", exactPath(t, layout.root), true},
		{"trailing separator", layout.root + `\`, true},
		{"upper case", strings.ToUpper(logFile), true},
		{"lower case", strings.ToLower(logFile), true},
		{"dot dot that stays inside", filepath.Join(layout.root, "run", "..", "Logs", "plaitwayd.log"), true},
		{"dot dot that leaves", filepath.Join(layout.root, "..", "elsewhere", "plaitwayd.log"), false},
		{"junction to the root", filepath.Join(layout.alias, "Logs", "plaitwayd.log"), true},
		{"verbatim junction to the root", exactPath(t, filepath.Join(layout.alias, "Logs", "plaitwayd.log")), true},
		{"not made yet", filepath.Join(layout.logs, "not", "yet", "there.log"), true},
		{"sibling with the same start", filepath.Join(layout.lookalike, "Logs", "plaitwayd.log"), false},
		{"elsewhere", filepath.Join(layout.elsewhere, "plaitwayd.log"), false},
		{"verbatim elsewhere", exactPath(t, filepath.Join(layout.elsewhere, "plaitwayd.log")), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := isInside(layout.root, tt.path)
			if err != nil || got != tt.want {
				t.Errorf("isInside(%q) = %v, %v; want %v", tt.path, got, err, tt.want)
			}
		})
	}
}

func TestIsInsideRecognisesAShortName(t *testing.T) {
	layout := newRootLayout(t)
	short := shortNameOrSkip(t, layout.root)
	for _, path := range []string{short, filepath.Join(short, "Logs", "plaitwayd.log"), exactPath(t, short)} {
		if got, err := isInside(layout.root, path); err != nil || !got {
			t.Errorf("isInside(%q) = %v, %v; want true", path, got, err)
		}
	}
}

// The root of a first run does not exist yet; where a path would end up is
// still known from the part that does.
func TestIsInsideWithARootThatDoesNotExistYet(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "Plaitway Data Root")
	mustMkdir(t, filepath.Join(base, "elsewhere"))
	for path, want := range map[string]bool{
		exactPath(t, filepath.Join(root, "Logs", "plaitwayd.log")):                 true,
		filepath.Join(root, "Logs", "plaitwayd.log"):                               true,
		exactPath(t, filepath.Join(base, "elsewhere", "plaitwayd.log")):            false,
		filepath.Join(base, "elsewhere", "..", "Plaitway Data Root", "run", "x"):   true,
		filepath.Join(base, "elsewhere", "..", "Plaitway Data Root X", "run", "x"): false,
		exactPath(t, filepath.Join(base, "Plaitway Data Root X", "plaitwayd.log")): false,
	} {
		if got, err := isInside(root, path); err != nil || got != want {
			t.Errorf("isInside(%q) = %v, %v; want %v", path, got, err, want)
		}
	}
}

// A path that cannot be resolved is treated as inside, so that it gets the
// checks of the root.
func TestIsInsideFailsClosedForAPathThatCannotBeResolved(t *testing.T) {
	layout := newRootLayout(t)
	unresolvable := filepath.Join(unusedDriveRoot(t), "Plaitway", "Logs", "plaitwayd.log")
	if _, err := canonicalPath(unresolvable); err == nil {
		t.Fatalf("%s can be resolved, the test needs one that cannot", unresolvable)
	}
	if got, err := isInside(layout.root, unresolvable); err != nil || !got {
		t.Errorf("isInside(%q) = %v, %v; want true", unresolvable, got, err)
	}
}

func TestCanonicalPathSpellsEverySpellingTheSame(t *testing.T) {
	layout := newRootLayout(t)
	want, err := canonicalPath(layout.logs)
	if err != nil {
		t.Fatal(err)
	}
	spellings := []string{
		exactPath(t, layout.logs),
		strings.ToUpper(layout.logs),
		filepath.Join(layout.alias, "Logs"),
		filepath.Join(layout.logs, "..", "Logs"),
		layout.logs + `\`,
	}
	for _, spelling := range spellings {
		if got, err := canonicalPath(spelling); err != nil || !strings.EqualFold(got, want) {
			t.Errorf("canonicalPath(%q) = %q, %v; want %q", spelling, got, err, want)
		}
	}
	got, err := canonicalPath(filepath.Join(layout.logs, "later", "x.log"))
	if wantMissing := filepath.Join(want, "later", "x.log"); err != nil || !strings.EqualFold(got, wantMissing) {
		t.Errorf("a path below a missing directory = %q, %v; want %q", got, err, wantMissing)
	}
}

// The data root has a junction in it. Spelling the log differently must not
// get around the refusal that the plain spelling gets.
func TestDataRootChecksApplyWhateverTheSpellingOfThePath(t *testing.T) {
	layout := newRootLayout(t)
	useDataRoot(t, layout.root)
	fspermtest.MakeJunction(t, filepath.Join(layout.root, "profiles"), layout.elsewhere)
	spellings := map[string]string{
		"plain":       layout.logs,
		"verbatim":    exactPath(t, layout.logs),
		"junction":    filepath.Join(layout.alias, "Logs"),
		"upper case":  strings.ToUpper(layout.logs),
		"dot dot":     filepath.Join(layout.root, "run", "..", "Logs"),
		"dot segment": filepath.Join(layout.logs, ".") + `\.`,
	}
	if short, err := shortNameOf(layout.root); err == nil {
		spellings["short name"] = filepath.Join(short, "Logs")
	}
	for name, logs := range spellings {
		t.Run(name, func(t *testing.T) {
			logFile := filepath.Join(logs, "plaitwayd.log")
			file, err := OpenAppend(logFile)
			if err == nil {
				file.Close()
			}
			if !errors.Is(err, ErrReparsePoint) {
				t.Errorf("OpenAppend(%q) = %v, want ErrReparsePoint", logFile, err)
			}
			if err := MkdirAll(logs, 0o755); !errors.Is(err, ErrReparsePoint) {
				t.Errorf("MkdirAll(%q) = %v, want ErrReparsePoint", logs, err)
			}
		})
	}
	mustBeEmpty(t, layout.elsewhere)
}

// The names that Win32 changes (a trailing dot or space) are the names the
// listing of a directory shows and a normal path cannot reach. Restrict has to
// repair what they hold, or say that it cannot.
func TestRestrictRepairsNamesThatWin32WouldChange(t *testing.T) {
	p := mustPrincipals(t)
	root := filepath.Join(t.TempDir(), "state")
	mustMkdir(t, root)
	removeAllExact(t, root)

	files := []string{"dotted.", "spaced ", "plain"}
	directories := []string{"dotted-dir.", "spaced-dir ", "plain-dir"}
	var open []string
	for _, name := range files {
		path := exactPath(t, filepath.Join(root, name))
		mustWrite(t, path, "PrivateKey = x")
		fspermtest.SetList(t, path, openFileList(p))
		open = append(open, path)
	}
	for _, name := range directories {
		dir := exactPath(t, filepath.Join(root, name))
		mustMkdir(t, dir)
		fspermtest.SetList(t, dir, everyoneOpenList(p, true))
		inner := filepath.Join(dir, "inner.txt")
		mustWrite(t, inner, "PrivateKey = x")
		fspermtest.SetList(t, inner, openFileList(p))
		open = append(open, dir, inner)
	}
	for _, path := range open {
		mustBePrivate(t, path, false)
	}
	mustList(t, root, append(slices.Clone(files), directories...))

	changed, err := Restrict(root)
	if err != nil || !changed {
		t.Fatalf("Restrict = %v, %v; want it to repair the entries", changed, err)
	}
	for _, path := range open {
		mustBePrivate(t, path, true)
		mustNotNameEveryone(t, path)
	}
	if changed, err := Restrict(root); err != nil || changed {
		t.Errorf("second Restrict = %v, %v; want nothing left to do", changed, err)
	}
}

// A junction under a name that Win32 changes is no easier to follow.
func TestRestrictRefusesALinkUnderANameThatWin32WouldChange(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	mustMkdir(t, outside)
	root := filepath.Join(base, "state")
	mustMkdir(t, root)
	removeAllExact(t, root)
	// mklink would drop the dot, but a rename keeps the junction and the name.
	junction := filepath.Join(root, "profiles")
	fspermtest.MakeJunction(t, junction, outside)
	if err := os.Rename(junction, exactPath(t, junction+".")); err != nil {
		t.Fatal(err)
	}
	mustList(t, root, []string{"profiles."})

	if _, err := Restrict(root); !errors.Is(err, ErrReparsePoint) {
		t.Fatalf("Restrict = %v, want ErrReparsePoint", err)
	}
	mustBeEmpty(t, outside)
}

func TestForgiveVanished(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "present"), "x")
	notFound := &os.PathError{Op: "open", Path: "name", Err: windows.ERROR_FILE_NOT_FOUND}
	other := fmt.Errorf("%w: name", ErrReparsePoint)
	at := plainLocation(dir)

	t.Run("an entry that is still listed", func(t *testing.T) {
		err := forgiveVanished(at, "present", notFound)
		if !errors.Is(err, ErrUnreachable) || errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "present") {
			t.Fatalf("forgiveVanished = %v; want ErrUnreachable naming the entry, and not a not-found", err)
		}
	})
	t.Run("an entry that is gone", func(t *testing.T) {
		if err := forgiveVanished(at, "removed", notFound); err != nil {
			t.Fatalf("forgiveVanished = %v, want it forgiven", err)
		}
	})
	t.Run("a directory that is gone", func(t *testing.T) {
		if err := forgiveVanished(plainLocation(filepath.Join(dir, "missing")), "present", notFound); err != nil {
			t.Fatalf("forgiveVanished = %v, want it forgiven", err)
		}
	})
	t.Run("an error that is not a not-found", func(t *testing.T) {
		if err := forgiveVanished(at, "removed", other); !errors.Is(err, ErrReparsePoint) {
			t.Fatalf("forgiveVanished = %v, want the error as it was", err)
		}
	})
	t.Run("no error", func(t *testing.T) {
		if err := forgiveVanished(at, "present", nil); err != nil {
			t.Fatalf("forgiveVanished = %v", err)
		}
	})
}

func TestCheckLinks(t *testing.T) {
	const path = `C:\x\profiles\office.profile`
	for name, tt := range map[string]struct {
		isDir bool
		links uint32
	}{
		"file with one name":                 {false, 1},
		"file the file system did not count": {false, 0},
		"directory":                          {true, 1},
	} {
		if err := checkLinks(path, tt.isDir, tt.links); err != nil {
			t.Errorf("%s: %v, want it accepted", name, err)
		}
	}
	err := checkLinks(path, false, 2)
	if !errors.Is(err, ErrHardLink) || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "2") {
		t.Errorf("a file with two names: %v, want ErrHardLink naming the path and the count", err)
	}
}

// The access list belongs to the file: repairing the name inside the tree would
// rewrite the list of a file that its other name keeps reachable.
func TestRestrictRefusesAHardLinkAndLeavesTheOtherNameAlone(t *testing.T) {
	p := mustPrincipals(t)
	base := t.TempDir()
	secret := filepath.Join(base, "outside.txt")
	mustWrite(t, secret, "keep")
	fspermtest.SetList(t, secret, openFileList(p))
	_, listBefore := readList(t, secret)
	root := filepath.Join(base, "state")
	profiles := filepath.Join(root, "profiles")
	if err := MkdirAll(profiles, 0o700); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(profiles, "office.profile")
	mustLink(t, secret, linked)

	changed, err := Restrict(root)
	if !errors.Is(err, ErrHardLink) || changed {
		t.Fatalf("Restrict = %v, %v; want ErrHardLink", changed, err)
	}
	if !strings.Contains(err.Error(), "office.profile") {
		t.Errorf("the error does not name the file: %v", err)
	}
	if _, listAfter := readList(t, secret); !slices.Equal(listAfter, listBefore) {
		t.Errorf("the other name of the file was changed: %v -> %v", listBefore, listAfter)
	}
	mustBePrivate(t, secret, false)
}

func TestRestrictRefusesAFileThatIsGivenDirectlyAndHasAnotherName(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "plaitwayd.log")
	mustWrite(t, file, "x")
	mustLink(t, file, filepath.Join(base, "other-name"))

	if _, err := Restrict(file); !errors.Is(err, ErrHardLink) {
		t.Fatalf("Restrict = %v, want ErrHardLink", err)
	}
}

// A log that has another name would have the daemon write to, and rewrite the
// list of, a file that someone else keeps a name for.
func TestOpenAppendRefusesALogThatHasAnotherName(t *testing.T) {
	p := mustPrincipals(t)
	base := t.TempDir()
	log := filepath.Join(base, "plaitwayd.log")
	mustWrite(t, log, "old\n")
	fspermtest.SetList(t, log, openFileList(p))
	_, listBefore := readList(t, log)
	mustLink(t, log, filepath.Join(base, "other-name"))

	file, err := OpenAppend(log)
	if err == nil {
		file.Close()
	}
	if !errors.Is(err, ErrHardLink) {
		t.Fatalf("OpenAppend = %v, want ErrHardLink", err)
	}
	if got, _ := os.ReadFile(log); string(got) != "old\n" {
		t.Errorf("the log was written to: %q", got)
	}
	if _, listAfter := readList(t, log); !slices.Equal(listAfter, listBefore) {
		t.Errorf("the list of the file was changed: %v -> %v", listBefore, listAfter)
	}
}

// Inside the data root a hard link anywhere is refused, by whichever call
// looks at the root first.
func TestDataRootRefusesAHardLinkBelowIt(t *testing.T) {
	root := useNewDataRoot(t)
	profiles := filepath.Join(root, "profiles")
	if err := MkdirAll(profiles, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	mustWrite(t, outside, "keep")
	mustLink(t, outside, filepath.Join(profiles, "office.profile"))

	if err := MkdirAll(filepath.Join(root, "run"), 0o700); !errors.Is(err, ErrHardLink) {
		t.Errorf("MkdirAll = %v, want ErrHardLink", err)
	}
	file, err := OpenAppend(filepath.Join(root, "Logs", "plaitwayd.log"))
	if err == nil {
		file.Close()
	}
	if !errors.Is(err, ErrHardLink) {
		t.Errorf("OpenAppend = %v, want ErrHardLink", err)
	}
}

// What the helpers make, and what a normal run does to it, has one name: a
// file that is written again and renamed over its old self is never refused.
func TestFilesMadeAndRepairedByTheHelpersHaveOneName(t *testing.T) {
	root := useNewDataRoot(t)
	logs := filepath.Join(root, "Logs")
	if err := MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(logs, "plaitwayd.log")
	for range 2 {
		file, err := OpenAppend(logFile)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(logFile, logFile+".1"); err != nil {
		t.Fatal(err)
	}
	file, err := OpenAppend(logFile)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()

	for _, path := range []string{logFile, logFile + ".1"} {
		handle, err := openObject(path, inspectAccess)
		if err != nil {
			t.Fatal(err)
		}
		info, err := fileInformation(handle)
		windows.CloseHandle(handle)
		if err != nil || info.NumberOfLinks != singleName {
			t.Errorf("%s has %d names, %v; want one", path, info.NumberOfLinks, err)
		}
	}
	if _, err := Restrict(root); err != nil {
		t.Errorf("Restrict of what the helpers made = %v", err)
	}
}
