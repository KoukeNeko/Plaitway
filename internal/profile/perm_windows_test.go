//go:build windows

package profile

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/KoukeNeko/Plaitway/internal/fsperm"
	"github.com/KoukeNeko/Plaitway/internal/fsperm/fspermtest"
)

// openForEveryone is a protected list that lets Everyone read (and the user of
// the test do anything), the way a directory someone prepared in advance could.
func openForEveryone(t *testing.T) string {
	t.Helper()
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("D:P(A;OICI;FR;;;WD)(A;OICI;FA;;;%s)", tokenUser.User.Sid)
}

// A junction at "profiles" would make the daemon write private keys wherever
// its maker pointed it. Open refuses, and nothing reaches that directory.
func TestOpenRefusesAJunctionInPlaceOfTheProfilesDirectory(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "state")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	fspermtest.MakeJunction(t, filepath.Join(dir, contentDir), outside)

	_, err := Open(dir, stubBackends(), discardLog())
	if !errors.Is(err, fsperm.ErrReparsePoint) {
		t.Fatalf("Open = %v, want an error wrapping ErrReparsePoint", err)
	}
	entries, readErr := os.ReadDir(outside)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("the directory the junction points to holds %v (%v), want nothing", entries, readErr)
	}
}

// A profiles directory and a profile that were made in advance with a list of
// their own are not covered by the repair of the state directory above them.
func TestOpenRepairsWhatWasPreparedBelowTheStateDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := fsperm.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	profiles := filepath.Join(dir, contentDir)
	if err := os.Mkdir(profiles, 0o700); err != nil {
		t.Fatal(err)
	}
	fspermtest.SetList(t, profiles, openForEveryone(t))
	profile := filepath.Join(profiles, "ABC"+contentExt)
	if err := os.WriteFile(profile, []byte("client\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fspermtest.SetList(t, profile, strings.Replace(openForEveryone(t), "OICI", "", -1))

	var logged bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logged, nil))
	open := func() {
		t.Helper()
		if _, err := Open(dir, stubBackends(), log); err != nil {
			t.Fatal(err)
		}
	}
	open()

	assertPrivate(t, profiles, 0o700)
	assertPrivate(t, profile, 0o600)
	if got := strings.Count(logged.String(), "state directory was accessible to others"); got != 1 {
		t.Fatalf("the repair was logged %d times, want once: %s", got, logged.String())
	}
	logged.Reset()
	open()
	if strings.Contains(logged.String(), "accessible to others") {
		t.Fatalf("a second start reported the repair again: %s", logged.String())
	}
}

// A development daemon keeps its state below %TEMP%, owned by its own user: the
// first start and the next ones say nothing.
func TestOpenOfADirectoryTheDaemonMadeItselfWarnsAboutNothing(t *testing.T) {
	var logged bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logged, nil))
	dir := filepath.Join(t.TempDir(), "plaitway-state")
	for range 3 {
		s, err := Open(dir, stubBackends(), log)
		if err != nil {
			t.Fatal(err)
		}
		if len(s.List()) == 0 {
			if _, err := s.Import(ImportRequest{Name: "Office", Content: []byte(ovpnContent)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if logged.Len() != 0 {
		t.Fatalf("the daemon reported its own directory: %s", logged.String())
	}
}

// assertPrivate checks that nobody but SYSTEM, Administrators and the user of
// the test reaches path. Windows has no mode bits to compare.
func assertPrivate(t *testing.T, path string, _ os.FileMode) {
	t.Helper()
	private, err := fsperm.IsPrivate(path)
	if err != nil {
		t.Fatal(err)
	}
	if !private {
		t.Errorf("%s is reachable by accounts other than SYSTEM, Administrators and the owner", path)
	}
}

// loosen lets Everyone into dir, and into what is inside it.
func loosen(t *testing.T, dir string) {
	t.Helper()
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString(fmt.Sprintf("D:(A;OICI;FA;;;WD)(A;OICI;FA;;;%s)", tokenUser.User.Sid))
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	err = windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
	if err != nil {
		t.Fatal(err)
	}
}

// makeIndexUnwritable makes the replacement of the index in dir fail, while the
// files below dir can still be written: a directory stands where the index is.
func makeIndexUnwritable(t *testing.T, dir string) {
	t.Helper()
	index := filepath.Join(dir, indexFile)
	if err := os.RemoveAll(index); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(index, "in the way"), 0o700); err != nil {
		t.Fatal(err)
	}
}
