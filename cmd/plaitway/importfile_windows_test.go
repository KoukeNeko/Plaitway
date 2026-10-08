package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Windows spells a path in ways that Unix does not, and a file system that
// ignores case. Whichever way a profile names a file, it is inside the
// directory of the profile or it is not read.
func TestInlineFilesAcceptsWindowsSpellingsOfAFileInTheProfileDirectory(t *testing.T) {
	t.Parallel()
	dir := profileDir(t)
	wantCA := "<ca>\n" + pemCA + "\n</ca>\n"
	for name, c := range map[string]struct{ line, dir string }{
		"backslashes, written twice":              {`ca sub\\..\\ca.crt`, dir},
		"a subdirectory":                          {`cert sub\\other.crt`, dir},
		"another case":                            {`ca CA.CRT`, dir},
		"a directory in another case":             {`ca ca.crt`, strings.ToUpper(dir)},
		"a directory in the extended-length form": {`ca ca.crt`, `\\?\` + dir},
		"quoted, with a space":                    {`ca "my ca.crt"`, dir},
		"the absolute path":                       {`ca ` + filepath.ToSlash(filepath.Join(dir, "ca.crt")), dir},
		"the absolute path, backslashes written twice": {
			`ca ` + strings.ReplaceAll(filepath.Join(dir, "ca.crt"), `\`, `\\`), dir,
		},
	} {
		got, _, err := inlineFiles(c.line+"\n", c.dir)
		want := wantCA
		if strings.HasPrefix(c.line, "cert") {
			want = "<cert>\n" + pemCert + "\n</cert>\n"
		}
		if err != nil || got != want {
			t.Errorf("%s: %q, %v; want %q", name, got, err, want)
		}
	}
}

// \certs\ca.crt is the root of the current drive and C:ca.crt the working
// directory of drive C; neither is a file of the profile directory, although
// the directory holds a file that Join would make of them.
func TestInlineFilesDoesNotReadAPathThatDoesNotStartInTheProfileDirectory(t *testing.T) {
	t.Parallel()
	dir := profileDir(t)
	if err := os.Mkdir(filepath.Join(dir, "certs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "certs", "ca.crt"), []byte(pemCA+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, line := range map[string]string{
		"the root of the drive":      `ca \\certs\\ca.crt`,
		"the root, forward slashes":  `ca /certs/ca.crt`,
		"a drive and no root":        `ca ` + filepath.VolumeName(dir) + `ca.crt`,
		"a drive and a relative dir": `ca ` + filepath.VolumeName(dir) + `certs/ca.crt`,
	} {
		if got, _, err := inlineFiles(line+"\n", dir); err == nil {
			t.Errorf("%s: read %q", name, got)
		}
	}
}

func TestIsRooted(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]bool{
		`ca.crt`:                false,
		`sub\ca.crt`:            false,
		`sub/ca.crt`:            false,
		`..\ca.crt`:             false,
		`C:\certs\ca.crt`:       true,
		`C:/certs/ca.crt`:       true,
		`C:ca.crt`:              true,
		`\certs\ca.crt`:         true,
		`/certs/ca.crt`:         true,
		`\\server\share\ca.crt`: true,
		`\\?\C:\certs\ca.crt`:   true,
		``:                      false,
	} {
		if got := isRooted(path); got != want {
			t.Errorf("isRooted(%q) = %v, want %v", path, got, want)
		}
	}
}
