//go:build !windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// inPlaceTree is a directory tree like /usr/sbin/openvpn in a temporary
// directory: top/usr/sbin/openvpn, with every mode 0755 and 0755 for the file.
type inPlaceTree struct {
	top, sbin, binary string
}

func newInPlaceTree(t *testing.T) inPlaceTree {
	t.Helper()
	// The path the checks see, with no link in it that the temporary
	// directory itself is behind.
	top, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tree := inPlaceTree{top: filepath.Join(top, "root")}
	tree.sbin = filepath.Join(tree.top, "usr", "sbin")
	tree.binary = filepath.Join(tree.sbin, "openvpn")
	if err := os.MkdirAll(tree.sbin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tree.binary, []byte(fakeOpenVPN), 0o755); err != nil {
		t.Fatal(err)
	}
	for dir := tree.sbin; len(dir) >= len(tree.top); dir = filepath.Dir(dir) {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return tree
}

// ownedBy says that every path belongs to root except the ones in others.
func ownedBy(others map[string]uint32) func(string) (uint32, error) {
	return func(path string) (uint32, error) { return others[path], nil }
}

func TestTrustInPlaceAcceptsWhatOnlyRootCanChange(t *testing.T) {
	tree := newInPlaceTree(t)
	got, err := trustInPlace(tree.binary, tree.top, ownedBy(nil))
	if err != nil || got != tree.binary {
		t.Fatalf("trustInPlace = %q, %v; want the binary", got, err)
	}
	// Mode 0555, as some distributions ship it.
	if err := os.Chmod(tree.binary, 0o555); err != nil {
		t.Fatal(err)
	}
	if _, err := trustInPlace(tree.binary, tree.top, ownedBy(nil)); err != nil {
		t.Fatalf("mode 0555: %v", err)
	}
}

// The path that was checked is the path that runs: a link is followed once,
// here, and not again when the daemon starts the binary.
func TestTrustInPlaceReturnsThePathBehindLinks(t *testing.T) {
	tree := newInPlaceTree(t)
	link := filepath.Join(tree.top, "usr", "bin")
	if err := os.Symlink("sbin", link); err != nil { // merged /usr
		t.Fatal(err)
	}
	got, err := trustInPlace(filepath.Join(link, "openvpn"), tree.top, ownedBy(nil))
	if err != nil || got != tree.binary {
		t.Fatalf("trustInPlace = %q, %v; want %q", got, err, tree.binary)
	}
}

func TestTrustInPlaceRefusesAndNamesTheFirstPathThatAnotherUserCanChange(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, tree inPlaceTree)
		owner   func(tree inPlaceTree) map[string]uint32
		blamed  func(tree inPlaceTree) string
		reason  string
	}{
		{
			name:    "binary writable by its group",
			prepare: func(t *testing.T, tree inPlaceTree) { chmod(t, tree.binary, 0o775) },
			blamed:  func(tree inPlaceTree) string { return tree.binary },
			reason:  "written to by its group or by others (mode 0775)",
		},
		{
			name:    "binary writable by others",
			prepare: func(t *testing.T, tree inPlaceTree) { chmod(t, tree.binary, 0o757) },
			blamed:  func(tree inPlaceTree) string { return tree.binary },
			reason:  "mode 0757",
		},
		{
			name:   "binary owned by a user",
			owner:  func(tree inPlaceTree) map[string]uint32 { return map[string]uint32{tree.binary: 1000} },
			blamed: func(tree inPlaceTree) string { return tree.binary },
			reason: "belongs to uid 1000, not to root",
		},
		{
			name:    "directory of the binary writable by others",
			prepare: func(t *testing.T, tree inPlaceTree) { chmod(t, tree.sbin, 0o777) },
			blamed:  func(tree inPlaceTree) string { return tree.sbin },
			reason:  "mode 0777",
		},
		{
			name:    "directory above writable by its group",
			prepare: func(t *testing.T, tree inPlaceTree) { chmod(t, filepath.Dir(tree.sbin), 0o775) },
			blamed:  func(tree inPlaceTree) string { return filepath.Dir(tree.sbin) },
			reason:  "mode 0775",
		},
		{
			name:    "sticky directory is no excuse",
			prepare: func(t *testing.T, tree inPlaceTree) { chmod(t, tree.sbin, os.ModeSticky|0o777) },
			blamed:  func(tree inPlaceTree) string { return tree.sbin },
			reason:  "mode 0777",
		},
		{
			name:   "directory owned by a user",
			owner:  func(tree inPlaceTree) map[string]uint32 { return map[string]uint32{filepath.Dir(tree.sbin): 1000} },
			blamed: func(tree inPlaceTree) string { return filepath.Dir(tree.sbin) },
			reason: "belongs to uid 1000, not to root",
		},
		{
			name:   "top itself owned by a user",
			owner:  func(tree inPlaceTree) map[string]uint32 { return map[string]uint32{tree.top: 1000} },
			blamed: func(tree inPlaceTree) string { return tree.top },
			reason: "belongs to uid 1000, not to root",
		},
		{
			// The file is looked at before its directories.
			name: "binary and directory both open",
			prepare: func(t *testing.T, tree inPlaceTree) {
				chmod(t, tree.binary, 0o777)
				chmod(t, tree.sbin, 0o777)
			},
			blamed: func(tree inPlaceTree) string { return tree.binary },
			reason: "mode 0777",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tree := newInPlaceTree(t)
			if tt.prepare != nil {
				tt.prepare(t, tree)
			}
			var others map[string]uint32
			if tt.owner != nil {
				others = tt.owner(tree)
			}
			got, err := trustInPlace(tree.binary, tree.top, ownedBy(others))
			if err == nil {
				t.Fatalf("trustInPlace accepted it and returned %q", got)
			}
			if got != "" {
				t.Errorf("trustInPlace returned %q with the error", got)
			}
			if !strings.Contains(err.Error(), tt.blamed(tree)+" ") || !strings.Contains(err.Error(), tt.reason) {
				t.Errorf("err = %q, want it to blame %s: %s", err, tt.blamed(tree), tt.reason)
			}
		})
	}
}

func chmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// A link that leads to a directory somebody else can write to is checked at its
// destination.
func TestTrustInPlaceChecksWhereALinkLeads(t *testing.T) {
	tree := newInPlaceTree(t)
	elsewhere := filepath.Join(tree.top, "opt")
	if err := os.Mkdir(elsewhere, 0o777); err != nil {
		t.Fatal(err)
	}
	chmod(t, elsewhere, 0o777)
	if err := os.WriteFile(filepath.Join(elsewhere, "openvpn"), []byte(fakeOpenVPN), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(tree.binary); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(elsewhere, "openvpn"), tree.binary); err != nil {
		t.Fatal(err)
	}

	_, err := trustInPlace(tree.binary, tree.top, ownedBy(nil))
	if err == nil || !strings.Contains(err.Error(), elsewhere+" ") {
		t.Fatalf("err = %v, want it to blame %s, where the link leads", err, elsewhere)
	}
}

func TestTrustInPlaceStopsAtTop(t *testing.T) {
	tree := newInPlaceTree(t)
	// Everything above top may be open to everybody: it is not looked at.
	chmod(t, filepath.Dir(tree.top), 0o777)
	if _, err := trustInPlace(tree.binary, tree.top, ownedBy(nil)); err != nil {
		t.Fatalf("a directory above top was looked at: %v", err)
	}
}

func TestTrustInPlaceReportsWhatIsWrongWithThePath(t *testing.T) {
	tree := newInPlaceTree(t)

	if _, err := trustInPlace("", tree.top, ownedBy(nil)); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("no path: %v", err)
	}
	missing := tree.binary + ".missing"
	if _, err := trustInPlace(missing, tree.top, ownedBy(nil)); err == nil || !strings.Contains(err.Error(), "openvpn not found at "+missing) {
		t.Errorf("missing binary: %v", err)
	}
	if _, err := trustInPlace(tree.sbin, tree.top, ownedBy(nil)); err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Errorf("a directory as the binary: %v", err)
	}
	failing := func(string) (uint32, error) { return 0, errors.New("no owner information") }
	if _, err := trustInPlace(tree.binary, tree.top, failing); err == nil || !strings.Contains(err.Error(), "no owner information") {
		t.Errorf("an owner that cannot be read: %v", err)
	}
}

// A path relative to the directory the daemon runs in is made absolute first,
// so that the directories above it are checked too.
func TestTrustInPlaceResolvesARelativePath(t *testing.T) {
	tree := newInPlaceTree(t)
	t.Chdir(tree.sbin)
	got, err := trustInPlace("openvpn", tree.top, ownedBy(nil))
	if err != nil || got != tree.binary {
		t.Fatalf("trustInPlace = %q, %v; want %q", got, err, tree.binary)
	}
	chmod(t, filepath.Dir(tree.sbin), 0o777)
	if _, err := trustInPlace("openvpn", tree.top, ownedBy(nil)); err == nil || !strings.Contains(err.Error(), filepath.Dir(tree.sbin)+" ") {
		t.Fatalf("err = %v, want it to blame %s, above the working directory", err, filepath.Dir(tree.sbin))
	}
}
