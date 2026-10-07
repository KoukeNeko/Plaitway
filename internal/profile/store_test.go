package profile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// stubBackends parse like a strict backend: a line "reject" fails the profile
// and a line "script ..." is removed with a warning.
func stubBackends() map[tunnel.Kind]tunnel.Backend {
	parse := func(content []byte) (tunnel.Parsed, error) {
		var kept [][]byte
		var warnings []tunnel.Warning
		for i, line := range bytes.Split(content, []byte("\n")) {
			switch {
			case string(bytes.TrimSpace(line)) == "reject":
				return tunnel.Parsed{}, fmt.Errorf("line %d: directive \"reject\" is not allowed", i+1)
			case bytes.HasPrefix(line, []byte("script")):
				warnings = append(warnings, tunnel.Warning{Line: i + 1, Directive: "script", Message: "removed"})
			default:
				kept = append(kept, line)
			}
		}
		return tunnel.Parsed{
			Summary:       tunnel.Summary{Endpoints: []tunnel.Endpoint{{Host: "vpn.example.com", Port: 1194, Protocol: "tcp"}}, Routes: []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")}},
			Warnings:      warnings,
			Content:       bytes.Join(kept, []byte("\n")),
			SuggestedName: "From Content",
		}, nil
	}
	// A WireGuard profile's public key is a function of its text.
	parseWireGuard := func(content []byte) (tunnel.Parsed, error) {
		parsed, err := parse(content)
		parsed.Summary.PublicKey = "public-of-" + fmt.Sprint(len(parsed.Content))
		return parsed, err
	}
	return map[tunnel.Kind]tunnel.Backend{
		tunnel.KindOpenVPN:   {Kind: tunnel.KindOpenVPN, Parse: parse},
		tunnel.KindWireGuard: {Kind: tunnel.KindWireGuard, Parse: parseWireGuard},
	}
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func openStore(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir, stubBackends(), discardLog())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const (
	ovpnContent = "client\nremote vpn.example.com 1194\n"
	wgContent   = "[Interface]\nPrivateKey = x\n[Peer]\nEndpoint = h:51820\n"
)

func importOK(t *testing.T, s *Store, req ImportRequest) ImportResult {
	t.Helper()
	res, err := s.Import(req)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	return res
}

func names(profiles []Profile) []string {
	var out []string
	for _, p := range profiles {
		out = append(out, p.Name)
	}
	return out
}

func TestOpenCreatesPrivateDirectoryAndImportWritesPrivateFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	dir := filepath.Join(t.TempDir(), "state")
	s := openStore(t, dir)
	res := importOK(t, s, ImportRequest{Name: "Office", Content: []byte(ovpnContent)})

	for path, want := range map[string]os.FileMode{
		dir:                                 0o700,
		filepath.Join(dir, "profiles"):      0o700,
		filepath.Join(dir, "profiles.json"): 0o600,
		s.contentPath(res.Profile.ID):       0o600,
	} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode = %v, want %v", path, fi.Mode().Perm(), want)
		}
	}
}

func TestOpenTightensLooseDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	openStore(t, dir)
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("state directory mode = %v, want 0700", fi.Mode().Perm())
	}
}

func TestImportStoresParsedContentNotTheUpload(t *testing.T) {
	s := openStore(t, t.TempDir())
	upload := "client\nscript /tmp/evil.sh\nremote vpn.example.com 1194\n"
	res := importOK(t, s, ImportRequest{Content: []byte(upload), SourceFilename: "x.ovpn"})

	got, err := s.Content(res.Profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := "client\nremote vpn.example.com 1194\n"; string(got) != want {
		t.Fatalf("stored content = %q, want %q", got, want)
	}
	if len(res.Warnings) != 1 || res.Warnings[0].Line != 2 || res.Warnings[0].Directive != "script" {
		t.Fatalf("warnings = %+v, want the stripped script directive on line 2", res.Warnings)
	}
}

func TestImportRejectsWithParseErrorText(t *testing.T) {
	s := openStore(t, t.TempDir())
	_, err := s.Import(ImportRequest{Content: []byte("client\nreject\n")})

	var invalid *InvalidError
	if !errors.As(err, &invalid) {
		t.Fatalf("error = %v, want an InvalidError", err)
	}
	if want := `line 2: directive "reject" is not allowed`; invalid.Error() != want {
		t.Fatalf("message = %q, want the Parse error text %q", invalid.Error(), want)
	}
	if n := len(s.List()); n != 0 {
		t.Fatalf("%d profiles stored after a rejected import", n)
	}
}

func TestImportRejectsBadInput(t *testing.T) {
	s := openStore(t, t.TempDir())
	for name, req := range map[string]ImportRequest{
		"empty":      {Content: nil},
		"oversized":  {Content: bytes.Repeat([]byte("a"), MaxContentSize+1), Kind: tunnel.KindOpenVPN},
		"unknown":    {Content: []byte("hello world")},
		"no backend": {Content: []byte(ovpnContent), Kind: tunnel.Kind(99)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := s.Import(req)
			var invalid *InvalidError
			if !errors.As(err, &invalid) {
				t.Fatalf("error = %v, want an InvalidError", err)
			}
		})
	}
	// Exactly at the limit is still accepted.
	if _, err := s.Import(ImportRequest{Content: bytes.Repeat([]byte("a"), MaxContentSize), Kind: tunnel.KindOpenVPN}); err != nil {
		t.Fatalf("content of exactly MaxContentSize: %v", err)
	}
}

func TestDetectKind(t *testing.T) {
	tests := []struct {
		name, filename, content string
		want                    tunnel.Kind
		ok                      bool
	}{
		{"wireguard by content", "x.conf", wgContent, tunnel.KindWireGuard, true},
		{"wireguard header case", "", "[interface]\nAddress = 10.0.0.2/32", tunnel.KindWireGuard, true},
		{"wireguard crlf", "", "[Interface]\r\nAddress = 10.0.0.2/32\r\n", tunnel.KindWireGuard, true},
		{"openvpn by content", "x.conf", ovpnContent, tunnel.KindOpenVPN, true},
		{"openvpn inline block", "", "<ca>\nMIIB\n</ca>\n", tunnel.KindOpenVPN, true},
		{"content beats extension", "x.ovpn", wgContent, tunnel.KindWireGuard, true},
		{"ovpn extension decides when content is silent", "work.OVPN", "verb 3\n", tunnel.KindOpenVPN, true},
		{"nothing to go on", "notes.txt", "hello\n", 0, false},
		{"comments do not count", "", "# client\n; remote x 1\n", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := DetectKind(tt.filename, []byte(tt.content))
			if got != tt.want || ok != tt.ok {
				t.Fatalf("DetectKind = %v, %v; want %v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestImportNames(t *testing.T) {
	s := openStore(t, t.TempDir())
	imp := func(req ImportRequest) string {
		if req.Content == nil {
			req.Content = []byte(ovpnContent)
		}
		return importOK(t, s, req).Profile.Name
	}
	tests := []struct {
		req  ImportRequest
		want string
	}{
		{ImportRequest{Name: "  Office  "}, "Office"},
		{ImportRequest{Name: "office"}, "office 2"}, // names are unique, ignoring case
		{ImportRequest{Name: "Office"}, "Office 3"},
		{ImportRequest{SourceFilename: "/Users/me/Downloads/Lab VPN.ovpn"}, "Lab VPN"},
		{ImportRequest{SourceFilename: `C:\vpn\home.conf`, Content: []byte(wgContent)}, "home"},
		{ImportRequest{SourceFilename: ".ovpn"}, "From Content"}, // no usable stem: name from the profile
		{ImportRequest{}, "From Content 2"},
		{ImportRequest{Name: "a\x00b\nc"}, "abc"},
		{ImportRequest{Name: "bad\xffname"}, "badname"}, // a protobuf string must be valid UTF-8
		{ImportRequest{Name: strings.Repeat("長", 150)}, strings.Repeat("長", 100)},
	}
	for _, tt := range tests {
		if got := imp(tt.req); got != tt.want {
			t.Errorf("import %+v named %q, want %q", tt.req.Name, got, tt.want)
		}
	}
}

func TestImportAppendsAtTheLowestPriority(t *testing.T) {
	s := openStore(t, t.TempDir())
	a := importOK(t, s, ImportRequest{Name: "a", Content: []byte(ovpnContent), Settings: Settings{Priority: 50, AutoConnect: true, TunnelMode: tunnel.ModeSplit}})
	b := importOK(t, s, ImportRequest{Name: "b", Content: []byte(ovpnContent)})

	if a.Profile.Settings.Priority != 1 || b.Profile.Settings.Priority != 2 {
		t.Fatalf("priorities = %d, %d; want 1, 2 whatever the request said", a.Profile.Settings.Priority, b.Profile.Settings.Priority)
	}
	if !a.Profile.Settings.AutoConnect || a.Profile.Settings.TunnelMode != tunnel.ModeSplit {
		t.Fatalf("requested settings were not kept: %+v", a.Profile.Settings)
	}
}

func TestProfilesSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	a := importOK(t, s, ImportRequest{Name: "a", Content: []byte(ovpnContent), Settings: Settings{AutoConnect: true, TunnelMode: tunnel.ModeFull}})
	b := importOK(t, s, ImportRequest{Name: "b", Content: []byte(wgContent)})
	if err := s.Reorder([]string{b.Profile.ID, a.Profile.ID}); err != nil {
		t.Fatal(err)
	}

	reopened := openStore(t, dir)
	got := reopened.List()
	if !slices.Equal(names(got), []string{"b", "a"}) {
		t.Fatalf("order after reopen = %v, want [b a]", names(got))
	}
	gotA, _ := reopened.Get(a.Profile.ID)
	wantA := a.Profile
	wantA.Settings.Priority = 2
	if fmt.Sprint(gotA) != fmt.Sprint(wantA) {
		t.Fatalf("profile a after reopen =\n%+v\nwant\n%+v", gotA, wantA)
	}
	if gotA.Kind != tunnel.KindOpenVPN || gotA.Settings.TunnelMode != tunnel.ModeFull || !gotA.Settings.AutoConnect {
		t.Fatalf("settings lost: %+v", gotA)
	}
	if len(gotA.Summary.Endpoints) != 1 || gotA.Summary.Routes[0] != netip.MustParsePrefix("192.168.1.0/24") {
		t.Fatalf("summary lost: %+v", gotA.Summary)
	}
	if content, err := reopened.Content(b.Profile.ID); err != nil || string(content) != wgContent {
		t.Fatalf("content after reopen = %q, %v", content, err)
	}
}

func TestUpdate(t *testing.T) {
	s := openStore(t, t.TempDir())
	a := importOK(t, s, ImportRequest{Name: "a", Content: []byte(ovpnContent)}).Profile
	importOK(t, s, ImportRequest{Name: "b", Content: []byte(ovpnContent)})

	rename := func(name string) error {
		_, err := s.Update(a.ID, Change{Name: &name})
		return err
	}
	if err := rename("Renamed"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(a.ID); got.Name != "Renamed" {
		t.Fatalf("name = %q", got.Name)
	}
	for _, bad := range []string{"", "   ", "B"} {
		var invalid *InvalidError
		if err := rename(bad); !errors.As(err, &invalid) {
			t.Errorf("rename to %q: error = %v, want an InvalidError", bad, err)
		}
	}
	if err := rename("Renamed"); err != nil {
		t.Fatalf("renaming a profile to its own name: %v", err)
	}

	p, err := s.Update(a.ID, Change{Settings: &Settings{AutoConnect: true, TunnelMode: tunnel.ModeSplit}})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Settings.AutoConnect || p.Settings.TunnelMode != tunnel.ModeSplit || p.Settings.Priority != 1 || p.Name != "Renamed" {
		t.Fatalf("after settings update: %+v (priority 0 must keep the current priority, the name must stay)", p)
	}
	if _, err := s.Update(a.ID, Change{Settings: &Settings{Priority: -1}}); err == nil {
		t.Fatal("negative priority accepted")
	}
	if _, err := s.Update("nope", Change{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v, want ErrNotFound", err)
	}
}

func TestDeleteRemovesContent(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	p := importOK(t, s, ImportRequest{Name: "a", Content: []byte(ovpnContent)}).Profile

	if err := s.Delete(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete: %v", err)
	}
	if _, err := os.Stat(s.contentPath(p.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("content file still there: %v", err)
	}
	if err := s.Delete(p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v, want ErrNotFound", err)
	}
	if n := len(openStore(t, dir).List()); n != 0 {
		t.Fatalf("%d profiles after reopen", n)
	}
}

func TestReorder(t *testing.T) {
	s := openStore(t, t.TempDir())
	var ids []string
	for _, name := range []string{"a", "b", "c"} {
		ids = append(ids, importOK(t, s, ImportRequest{Name: name, Content: []byte(ovpnContent)}).Profile.ID)
	}
	a, b, c := ids[0], ids[1], ids[2]

	if err := s.Reorder([]string{c, a, b}); err != nil {
		t.Fatal(err)
	}
	list := s.List()
	if !slices.Equal(names(list), []string{"c", "a", "b"}) {
		t.Fatalf("order = %v", names(list))
	}
	for i, p := range list {
		if p.Settings.Priority != i+1 {
			t.Errorf("%s priority = %d, want %d", p.Name, p.Settings.Priority, i+1)
		}
	}

	tests := []struct {
		name     string
		ids      []string
		notFound bool
	}{
		{"missing one", []string{a, b}, false},
		{"duplicate", []string{a, a, b}, false},
		{"unknown id", []string{a, b, "nope"}, true},
		{"empty", nil, false},
	}
	for _, tt := range tests {
		err := s.Reorder(tt.ids)
		var invalid *InvalidError
		switch {
		case tt.notFound && !errors.Is(err, ErrNotFound):
			t.Errorf("%s: error = %v, want ErrNotFound", tt.name, err)
		case !tt.notFound && !errors.As(err, &invalid):
			t.Errorf("%s: error = %v, want an InvalidError", tt.name, err)
		}
	}
	if !slices.Equal(names(s.List()), []string{"c", "a", "b"}) {
		t.Fatalf("a rejected reorder changed the order: %v", names(s.List()))
	}
}

func TestOpenRefusesWhatItCannotRead(t *testing.T) {
	tests := map[string]string{
		"corrupt":       "{not json",
		"future schema": `{"schema":2,"profiles":[]}`,
		"no schema":     `{"profiles":[]}`,
		"path as id":    `{"schema":1,"profiles":[{"id":"../../etc/passwd","name":"x","kind":"openvpn","tunnel_mode":"auto","priority":1}]}`,
		"unknown kind":  `{"schema":1,"profiles":[{"id":"AAAAAAAAAAAAAAAAAAAAAAAAAA","name":"x","kind":"ipsec","tunnel_mode":"auto","priority":1}]}`,
		"bad priority":  `{"schema":1,"profiles":[{"id":"AAAAAAAAAAAAAAAAAAAAAAAAAA","name":"x","kind":"openvpn","tunnel_mode":"auto","priority":0}]}`,
		"duplicate id":  `{"schema":1,"profiles":[{"id":"AAAAAAAAAAAAAAAAAAAAAAAAAA","name":"x","kind":"openvpn","tunnel_mode":"auto","priority":1},{"id":"AAAAAAAAAAAAAAAAAAAAAAAAAA","name":"y","kind":"openvpn","tunnel_mode":"auto","priority":2}]}`,
	}
	for name, index := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "profiles.json"), []byte(index), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(dir, stubBackends(), discardLog()); err == nil {
				t.Fatal("Open succeeded")
			}
			// The unreadable index must be left for the owner to inspect.
			if got, _ := os.ReadFile(filepath.Join(dir, "profiles.json")); string(got) != index {
				t.Fatalf("index was modified: %q", got)
			}
		})
	}
}

func TestOpenSweepsLeftovers(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	keep := importOK(t, s, ImportRequest{Name: "keep", Content: []byte(ovpnContent)}).Profile

	orphan := filepath.Join(dir, "profiles", "AAAAAAAAAAAAAAAAAAAAAAAAAA.profile")
	tmpIndex := filepath.Join(dir, ".tmp-123")
	tmpContent := filepath.Join(dir, "profiles", ".tmp-456")
	for _, path := range []string{orphan, tmpIndex, tmpContent} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	openStore(t, dir)

	for _, path := range []string{orphan, tmpIndex, tmpContent} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived Open: %v", filepath.Base(path), err)
		}
	}
	if _, err := os.Stat(s.contentPath(keep.ID)); err != nil {
		t.Fatalf("content of a listed profile was removed: %v", err)
	}
}

// An owner who deletes a profiles.json that the daemon refuses to read must not
// lose the profiles it listed: without an index nothing says that the content
// files are leftovers.
func TestOpenKeepsContentWhenTheIndexIsMissing(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	p := importOK(t, s, ImportRequest{Name: "keep", Content: []byte(ovpnContent)}).Profile
	if err := os.Remove(filepath.Join(dir, indexFile)); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(dir, ".tmp-123")
	if err := os.WriteFile(tmp, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	reopened := openStore(t, dir)

	if n := len(reopened.List()); n != 0 {
		t.Fatalf("%d profiles listed without an index", n)
	}
	if _, err := os.Stat(s.contentPath(p.ID)); err != nil {
		t.Fatalf("content of a profile was removed because the index is missing: %v", err)
	}
	if _, err := os.Stat(tmp); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a temporary file survived Open: %v", err)
	}
}

func TestContentRefusesSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges")
	}
	dir := t.TempDir()
	s := openStore(t, dir)
	p := importOK(t, s, ImportRequest{Name: "a", Content: []byte(ovpnContent)}).Profile

	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("do not read"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.contentPath(p.ID)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, s.contentPath(p.ID)); err != nil {
		t.Fatal(err)
	}
	if content, err := s.Content(p.ID); err == nil {
		t.Fatalf("Content followed a symlink and returned %q", content)
	}
}

// A failed write must leave the store, in memory and on disk, as it was.
func TestFailedWriteLeavesStoreUnchanged(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a non-root POSIX user to make the directory read-only")
	}
	dir := t.TempDir()
	s := openStore(t, dir)
	a := importOK(t, s, ImportRequest{Name: "a", Content: []byte(ovpnContent)}).Profile

	if err := os.Chmod(dir, 0o500); err != nil { // the index cannot be replaced
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)

	newName := "changed"
	if _, err := s.Update(a.ID, Change{Name: &newName}); err == nil {
		t.Fatal("Update succeeded in a read-only directory")
	}
	if err := s.Delete(a.ID); err == nil {
		t.Fatal("Delete succeeded in a read-only directory")
	}
	if got, err := s.Get(a.ID); err != nil || got.Name != "a" {
		t.Fatalf("profile after failed writes = %+v, %v; want it unchanged", got, err)
	}
	if _, err := os.Stat(s.contentPath(a.ID)); err != nil {
		t.Fatalf("content removed although the delete failed: %v", err)
	}
}

func TestFailedImportLeavesNoContentBehind(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a non-root POSIX user to make the directory read-only")
	}
	dir := t.TempDir()
	s := openStore(t, dir)
	if err := os.Chmod(dir, 0o500); err != nil { // the index cannot be written, the profiles directory still can
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)

	if _, err := s.Import(ImportRequest{Name: "a", Content: []byte(ovpnContent)}); err == nil {
		t.Fatal("Import succeeded although the index could not be written")
	}
	entries, err := os.ReadDir(filepath.Join(dir, "profiles"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("content left behind: %v", entries)
	}
	if n := len(s.List()); n != 0 {
		t.Fatalf("%d profiles listed after a failed import", n)
	}
}

func TestConcurrentUse(t *testing.T) {
	s := openStore(t, t.TempDir())
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := s.Import(ImportRequest{Name: "same", Content: []byte(ovpnContent)})
			if err != nil {
				t.Error(err)
				return
			}
			id := p.Profile.ID
			name := fmt.Sprintf("renamed %d", i)
			if _, err := s.Update(id, Change{Name: &name}); err != nil {
				t.Error(err)
			}
			s.List()
			if _, err := s.Content(id); err != nil {
				t.Error(err)
			}
			if i%2 == 0 {
				if err := s.Delete(id); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()

	list := s.List()
	if len(list) != 4 {
		t.Fatalf("%d profiles left, want 4", len(list))
	}
	seen := map[string]bool{}
	for _, p := range list {
		if seen[p.ID] || seen[strings.ToLower(p.Name)] {
			t.Fatalf("duplicate id or name in %v", names(list))
		}
		seen[p.ID], seen[strings.ToLower(p.Name)] = true, true
	}
}
