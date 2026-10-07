package profile

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

func wantInvalid(t *testing.T, err error, contains string) {
	t.Helper()
	var invalid *InvalidError
	if !errors.As(err, &invalid) {
		t.Fatalf("error = %v, want an InvalidError", err)
	}
	if !strings.Contains(invalid.Error(), contains) {
		t.Fatalf("message = %q, want it to contain %q", invalid.Error(), contains)
	}
}

func storedContent(t *testing.T, s *Store, id string) string {
	t.Helper()
	content, err := s.Content(id)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestUpdateContentStoresTheParsedTextAndRefreshesTheSummary(t *testing.T) {
	s := openStore(t, t.TempDir())
	p := importOK(t, s, ImportRequest{Name: "home", Content: []byte(wgContent)}).Profile
	if p.Summary.PublicKey != "public-of-"+strconv.Itoa(len(wgContent)) {
		t.Fatalf("public key at import = %q", p.Summary.PublicKey)
	}

	edit := "[Interface]\nPrivateKey = a-longer-key\nscript /tmp/evil.sh\n[Peer]\nEndpoint = h:51820\n"
	res, err := s.UpdateContent(p.ID, []byte(edit))
	if err != nil {
		t.Fatal(err)
	}

	// What is stored is what Parse kept, never the upload.
	wantStored := "[Interface]\nPrivateKey = a-longer-key\n[Peer]\nEndpoint = h:51820\n"
	if got := storedContent(t, s, p.ID); got != wantStored {
		t.Fatalf("stored content = %q, want %q", got, wantStored)
	}
	if len(res.Warnings) != 1 || res.Warnings[0].Line != 3 || res.Warnings[0].Directive != "script" {
		t.Fatalf("warnings = %+v, want the stripped script directive on line 3", res.Warnings)
	}
	if want := "public-of-" + strconv.Itoa(len(wantStored)); res.Profile.Summary.PublicKey != want {
		t.Fatalf("summary public key = %q, want %q (derived from the new text)", res.Profile.Summary.PublicKey, want)
	}
	got, err := s.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary.PublicKey != res.Profile.Summary.PublicKey {
		t.Fatalf("Get after UpdateContent has public key %q, the result said %q", got.Summary.PublicKey, res.Profile.Summary.PublicKey)
	}
	if got.Name != "home" || got.Settings != p.Settings || got.Imported != p.Imported {
		t.Fatalf("UpdateContent changed more than the text: %+v", got)
	}
}

func TestUpdateContentSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	p := importOK(t, s, ImportRequest{Name: "home", Content: []byte(wgContent)}).Profile
	edit := "[Interface]\nPrivateKey = other\n[Peer]\nEndpoint = h:51820\n"
	if _, err := s.UpdateContent(p.ID, []byte(edit)); err != nil {
		t.Fatal(err)
	}

	reopened := openStore(t, dir)
	if got := storedContent(t, reopened, p.ID); got != edit {
		t.Fatalf("content after reopen = %q", got)
	}
	got, _ := reopened.Get(p.ID)
	if want := "public-of-" + strconv.Itoa(len(edit)); got.Summary.PublicKey != want {
		t.Fatalf("public key after reopen = %q, want %q", got.Summary.PublicKey, want)
	}
}

func TestUpdateContentRefusesWhatImportRefuses(t *testing.T) {
	s := openStore(t, t.TempDir())
	p := importOK(t, s, ImportRequest{Name: "office", Content: []byte(ovpnContent)}).Profile

	for name, tt := range map[string]struct {
		content  []byte
		contains string
	}{
		"rejected by Parse": {[]byte("client\nreject\n"), `line 2: directive "reject" is not allowed`},
		"empty":             {nil, "empty"},
		"oversized":         {bytes.Repeat([]byte("a"), MaxContentSize+1), "limit"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := s.UpdateContent(p.ID, tt.content)
			wantInvalid(t, err, tt.contains)
			if got := storedContent(t, s, p.ID); got != ovpnContent {
				t.Fatalf("a refused update changed the content to %q", got)
			}
		})
	}
	if got, _ := s.Get(p.ID); got.Summary.PublicKey != "" || len(got.Summary.Endpoints) != 1 {
		t.Fatalf("a refused update changed the summary: %+v", got.Summary)
	}
}

func TestUpdateContentAcceptsExactlyTheLimit(t *testing.T) {
	s := openStore(t, t.TempDir())
	p := importOK(t, s, ImportRequest{Name: "office", Content: []byte(ovpnContent)}).Profile
	if _, err := s.UpdateContent(p.ID, bytes.Repeat([]byte("a"), MaxContentSize)); err != nil {
		t.Fatalf("content of exactly MaxContentSize: %v", err)
	}
}

func TestUpdateContentRefusesTextOfTheOtherKind(t *testing.T) {
	s := openStore(t, t.TempDir())
	ovpn := importOK(t, s, ImportRequest{Name: "office", Content: []byte(ovpnContent)}).Profile
	wg := importOK(t, s, ImportRequest{Name: "home", Content: []byte(wgContent)}).Profile

	_, err := s.UpdateContent(ovpn.ID, []byte(wgContent))
	wantInvalid(t, err, "this profile is OpenVPN, but the text is WireGuard")
	_, err = s.UpdateContent(wg.ID, []byte(ovpnContent))
	wantInvalid(t, err, "this profile is WireGuard, but the text is OpenVPN")

	if got := storedContent(t, s, ovpn.ID); got != ovpnContent {
		t.Fatalf("OpenVPN content = %q after the refusal", got)
	}
	if got := storedContent(t, s, wg.ID); got != wgContent {
		t.Fatalf("WireGuard content = %q after the refusal", got)
	}
}

// A text that cannot be detected is left to the Parse of the profile's own
// backend, as an import with an explicit kind is.
func TestUpdateContentTakesUndetectableTextForTheProfilesOwnKind(t *testing.T) {
	s := openStore(t, t.TempDir())
	p := importOK(t, s, ImportRequest{Name: "home", Content: []byte(wgContent)}).Profile
	if _, err := s.UpdateContent(p.ID, []byte("anything at all\n")); err != nil {
		t.Fatal(err)
	}
	if got := storedContent(t, s, p.ID); got != "anything at all\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestUpdateContentUnknownProfile(t *testing.T) {
	s := openStore(t, t.TempDir())
	if _, err := s.UpdateContent("AAAAAAAAAAAAAAAAAAAAAAAAAA", []byte(ovpnContent)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestUpdateContentWritesAPrivateFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	s := openStore(t, t.TempDir())
	p := importOK(t, s, ImportRequest{Name: "home", Content: []byte(wgContent)}).Profile
	if _, err := s.UpdateContent(p.ID, []byte(wgContent+"# edited\n")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(s.contentPath(p.ID))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("content mode = %v, want 0600", fi.Mode().Perm())
	}
}

// A failed write must leave the store, in memory and on disk, as it was: here
// the content can be replaced but the index cannot, and the old text has to
// come back.
func TestFailedUpdateContentRestoresTheOldText(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a non-root POSIX user to make the directory read-only")
	}
	dir := t.TempDir()
	s := openStore(t, dir)
	p := importOK(t, s, ImportRequest{Name: "home", Content: []byte(wgContent)}).Profile

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)

	if _, err := s.UpdateContent(p.ID, []byte("[Interface]\nPrivateKey = new\n")); err == nil {
		t.Fatal("UpdateContent succeeded although the index could not be written")
	}
	if got := storedContent(t, s, p.ID); got != wgContent {
		t.Fatalf("content = %q, want the old text back", got)
	}
	if got, _ := s.Get(p.ID); got.Summary.PublicKey != p.Summary.PublicKey {
		t.Fatalf("summary changed to %+v", got.Summary)
	}
	entries, err := os.ReadDir(filepath.Join(dir, contentDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("files in the profiles directory = %v, want only the content", entries)
	}
}

func TestUpdateContentIsSafeAgainstConcurrentUse(t *testing.T) {
	s := openStore(t, t.TempDir())
	p := importOK(t, s, ImportRequest{Name: "home", Content: []byte(wgContent)}).Profile

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			text := "[Interface]\nPrivateKey = " + strings.Repeat("k", i+1) + "\n"
			if _, err := s.UpdateContent(p.ID, []byte(text)); err != nil {
				t.Error(err)
			}
			if _, err := s.Content(p.ID); err != nil {
				t.Error(err)
			}
			s.List()
		}()
	}
	wg.Wait()

	// Whichever write came last, the text and the summary belong together.
	got, _ := s.Get(p.ID)
	if want := "public-of-" + strconv.Itoa(len(storedContent(t, s, p.ID))); got.Summary.PublicKey != want {
		t.Fatalf("summary public key %q does not belong to the stored text (want %q)", got.Summary.PublicKey, want)
	}
}

// Profiles stored before the summary carried a public key get it when the
// store opens, without rewriting their text.
func TestOpenDerivesThePublicKeyOfWireGuardProfilesStoredBefore(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	wg := importOK(t, s, ImportRequest{Name: "home", Content: []byte(wgContent)}).Profile
	ovpn := importOK(t, s, ImportRequest{Name: "office", Content: []byte(ovpnContent)}).Profile

	// Rewrite the index as the previous version wrote it: no public key.
	index, err := os.ReadFile(filepath.Join(dir, indexFile))
	if err != nil {
		t.Fatal(err)
	}
	stripped := strings.ReplaceAll(string(index), `"PublicKey": "`+wg.Summary.PublicKey+`"`, `"PublicKey": ""`)
	if stripped == string(index) {
		t.Fatal("the index does not hold the public key where the test expects it")
	}
	if err := os.WriteFile(filepath.Join(dir, indexFile), []byte(stripped), 0o600); err != nil {
		t.Fatal(err)
	}

	reopened := openStore(t, dir)
	if got, _ := reopened.Get(wg.ID); got.Summary.PublicKey != wg.Summary.PublicKey {
		t.Fatalf("public key after open = %q, want %q", got.Summary.PublicKey, wg.Summary.PublicKey)
	}
	if got, _ := reopened.Get(ovpn.ID); got.Summary.PublicKey != "" {
		t.Fatalf("an OpenVPN profile got the public key %q", got.Summary.PublicKey)
	}
	if got := storedContent(t, reopened, wg.ID); got != wgContent {
		t.Fatalf("the text was rewritten: %q", got)
	}

	// And it was written down, not only held in memory.
	again := openStore(t, dir)
	if got, _ := again.Get(wg.ID); got.Summary.PublicKey != wg.Summary.PublicKey {
		t.Fatalf("public key after a second open = %q", got.Summary.PublicKey)
	}
	data, _ := os.ReadFile(filepath.Join(dir, indexFile))
	if !strings.Contains(string(data), wg.Summary.PublicKey) {
		t.Fatal("the derived public key was not persisted")
	}
}

// A profile that Parse no longer accepts keeps an empty key and is reported
// without Parse's error, which may quote the private key.
func TestOpenDoesNotLogWhyAStoredProfileCannotBeParsed(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	p := importOK(t, s, ImportRequest{Name: "home", Content: []byte(wgContent)}).Profile
	index, err := os.ReadFile(filepath.Join(dir, indexFile))
	if err != nil {
		t.Fatal(err)
	}
	stripped := strings.ReplaceAll(string(index), `"PublicKey": "`+p.Summary.PublicKey+`"`, `"PublicKey": ""`)
	if err := os.WriteFile(filepath.Join(dir, indexFile), []byte(stripped), 0o600); err != nil {
		t.Fatal(err)
	}

	backends := stubBackends()
	wireGuard := backends[tunnel.KindWireGuard]
	wireGuard.Parse = func([]byte) (tunnel.Parsed, error) {
		return tunnel.Parsed{}, errors.New(`PrivateKey "TOP-SECRET" is invalid`)
	}
	backends[tunnel.KindWireGuard] = wireGuard
	var out bytes.Buffer
	reopened, err := Open(dir, backends, slog.New(slog.NewTextHandler(&out, nil)))
	if err != nil {
		t.Fatal(err)
	}

	if got, _ := reopened.Get(p.ID); got.Summary.PublicKey != "" {
		t.Fatalf("public key = %q, want none", got.Summary.PublicKey)
	}
	if !strings.Contains(out.String(), p.ID) || strings.Contains(out.String(), "TOP-SECRET") {
		t.Fatalf("log = %q, want a warning that names the profile and not the error", out.String())
	}
}

func TestSettingsPersistAndOldIndexesGetDefaults(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	wg := importOK(t, s, ImportRequest{Name: "home", Content: []byte(wgContent)}).Profile
	ovpn := importOK(t, s, ImportRequest{Name: "office", Content: []byte(ovpnContent)}).Profile

	if _, err := s.Update(wg.ID, Change{Settings: &Settings{ExcludePrivateIPs: true, OnDemand: OnDemand{WiFi: true}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ovpn.ID, Change{Settings: &Settings{OnDemand: OnDemand{Ethernet: true, WiFi: true}}}); err != nil {
		t.Fatal(err)
	}

	reopened := openStore(t, dir)
	gotWG, _ := reopened.Get(wg.ID)
	if !gotWG.Settings.ExcludePrivateIPs || gotWG.Settings.OnDemand != (OnDemand{WiFi: true}) {
		t.Fatalf("WireGuard settings after reopen: %+v", gotWG.Settings)
	}
	gotOpenVPN, _ := reopened.Get(ovpn.ID)
	if gotOpenVPN.Settings.ExcludePrivateIPs || gotOpenVPN.Settings.OnDemand != (OnDemand{Ethernet: true, WiFi: true}) {
		t.Fatalf("OpenVPN settings after reopen: %+v", gotOpenVPN.Settings)
	}

	// An index written before these settings existed reads with both off.
	old := `{"schema":1,"profiles":[{"id":"AAAAAAAAAAAAAAAAAAAAAAAAAA","name":"old","kind":"wireguard","auto_connect":true,"tunnel_mode":"full","priority":1,"imported":"2026-10-01T00:00:00Z","summary":{}}]}`
	oldDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(oldDir, indexFile), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(oldDir, contentDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, contentDir, "AAAAAAAAAAAAAAAAAAAAAAAAAA.profile"), []byte(wgContent), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := openStore(t, oldDir).Get("AAAAAAAAAAAAAAAAAAAAAAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Settings.AutoConnect || got.Settings.TunnelMode != tunnel.ModeFull || got.Settings.ExcludePrivateIPs || got.Settings.OnDemand.Active() {
		t.Fatalf("settings of the old profile = %+v", got.Settings)
	}
}

func TestExcludePrivateIPsIsForWireGuardOnly(t *testing.T) {
	s := openStore(t, t.TempDir())
	ovpn := importOK(t, s, ImportRequest{Name: "office", Content: []byte(ovpnContent)}).Profile
	wg := importOK(t, s, ImportRequest{Name: "home", Content: []byte(wgContent)}).Profile

	_, err := s.Update(ovpn.ID, Change{Settings: &Settings{ExcludePrivateIPs: true}})
	wantInvalid(t, err, "WireGuard")
	if got, _ := s.Get(ovpn.ID); got.Settings.ExcludePrivateIPs {
		t.Fatal("the refused setting was stored")
	}
	if _, err := s.Update(wg.ID, Change{Settings: &Settings{ExcludePrivateIPs: true}}); err != nil {
		t.Fatalf("WireGuard: %v", err)
	}

	_, err = s.Import(ImportRequest{Name: "other", Content: []byte(ovpnContent), Settings: Settings{ExcludePrivateIPs: true}})
	wantInvalid(t, err, "WireGuard")
	if n := len(s.List()); n != 2 {
		t.Fatalf("%d profiles after a refused import, want 2", n)
	}
}

func TestImportKeepsTheNewSettings(t *testing.T) {
	s := openStore(t, t.TempDir())
	p := importOK(t, s, ImportRequest{
		Name: "home", Content: []byte(wgContent),
		Settings: Settings{ExcludePrivateIPs: true, OnDemand: OnDemand{Ethernet: true}},
	}).Profile
	if !p.Settings.ExcludePrivateIPs || p.Settings.OnDemand != (OnDemand{Ethernet: true}) {
		t.Fatalf("settings = %+v", p.Settings)
	}
}

func TestOnDemandIsActiveWhenEitherKindIsChecked(t *testing.T) {
	for rules, want := range map[OnDemand]bool{
		{}:                           false,
		{Ethernet: true}:             true,
		{WiFi: true}:                 true,
		{Ethernet: true, WiFi: true}: true,
	} {
		if got := rules.Active(); got != want {
			t.Errorf("%+v.Active() = %v, want %v", rules, got, want)
		}
	}
}
