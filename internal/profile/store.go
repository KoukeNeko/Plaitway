// Package profile persists the imported VPN profiles in the daemon's state
// directory. The profile text is untrusted input to a root daemon: every import
// goes through the backend's Parse, and what is stored is the sanitized
// Parsed.Content, never the raw upload.
//
// Layout of the state directory (mode 0700, files 0600; on Windows an access
// list for SYSTEM, Administrators and the daemon's own user, and a directory
// that someone else owns or that is a link is refused, see internal/fsperm):
//
//	profiles.json            schema version and the metadata of every profile
//	profiles/<id>.profile    the profile content
//
// profiles.json is the commit point: a profile exists once it is listed there,
// and every change of it is one atomic replace of that file. Content files that
// the index does not list (an interrupted import or delete) are removed when
// the store is opened, unless the index itself is missing: then nothing says
// that they are leftovers, and they are kept.
package profile

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/fsperm"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

const (
	schemaVersion = 1

	// MaxContentSize bounds what an import may upload.
	MaxContentSize = 1 << 20
	maxIndexSize   = 16 << 20

	maxNameRunes = 100

	indexFile  = "profiles.json"
	contentDir = "profiles"
	contentExt = ".profile"
	tmpPrefix  = ".tmp-"
)

// ErrNotFound is wrapped by every error about an id (or key) that does not exist.
var ErrNotFound = errors.New("not found")

// InvalidError means the caller supplied something unacceptable: a rejected
// profile, a bad name, a wrong id list. Its text is meant for the caller.
type InvalidError struct{ Err error }

func (e *InvalidError) Error() string { return e.Err.Error() }
func (e *InvalidError) Unwrap() error { return e.Err }

func invalidf(format string, args ...any) error {
	return &InvalidError{Err: fmt.Errorf(format, args...)}
}

// Settings are the user's per-profile choices.
type Settings struct {
	AutoConnect bool
	TunnelMode  tunnel.Mode
	// Priority orders the profiles; lower wins overlapping routes and DNS
	// domains. Valid values start at 1.
	Priority int
	// ExcludePrivateIPs keeps the private ranges out of the tunnel. It edits
	// WireGuard's AllowedIPs, so only a WireGuard profile may set it.
	ExcludePrivateIPs bool
	OnDemand          OnDemand
}

// OnDemand names the kinds of primary network on which the profile is connected.
type OnDemand struct {
	Ethernet bool `json:"ethernet"`
	WiFi     bool `json:"wifi"`
}

// Active reports whether the profile follows the network at all.
func (o OnDemand) Active() bool { return o.Ethernet || o.WiFi }

// Profile is the stored metadata of one profile. Its Summary is shared with the
// store and must not be modified.
type Profile struct {
	ID       string
	Name     string
	Kind     tunnel.Kind
	Settings Settings
	Imported time.Time
	// Summary holds the facts Parse read from the content at import time.
	Summary tunnel.Summary
}

type ImportRequest struct {
	// Name is used as given (after cleaning); empty derives one from
	// SourceFilename, then from the profile itself.
	Name           string
	Content        []byte
	SourceFilename string
	// Kind zero means: detect it from the filename and the content.
	Kind tunnel.Kind
	// Settings.Priority is ignored: an imported profile goes last.
	Settings Settings
}

type ImportResult struct {
	Profile  Profile
	Warnings []tunnel.Warning
}

// Change is a partial update; nil fields stay as they are.
type Change struct {
	Name *string
	// Settings replaces the settings, except that a zero Priority keeps the
	// current one (zero is not a valid priority) and a zero TunnelMode is auto.
	Settings *Settings
}

// Store is safe for concurrent use.
type Store struct {
	dir      string
	backends map[tunnel.Kind]tunnel.Backend
	log      *slog.Logger

	mu       sync.RWMutex
	profiles []Profile // priority order
}

// Open creates the state directory when needed and loads the profiles in it.
// A profiles.json that cannot be read, is of another schema version or is
// inconsistent makes Open fail instead of starting empty, which would
// overwrite the profiles it could not read.
func Open(dir string, backends map[tunnel.Kind]tunnel.Backend, log *slog.Logger) (*Store, error) {
	s := &Store{dir: dir, backends: backends, log: log}
	if err := fsperm.MkdirAll(filepath.Join(dir, contentDir), 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("state directory: %w", err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("state directory %s is not a directory (a symbolic link is not followed)", dir)
	}
	// Profiles hold private keys: nobody else may list or read the directory.
	if err := fsperm.RestrictAndWarn(log, "state directory", dir); err != nil {
		return nil, fmt.Errorf("restrict state directory: %w", err)
	}
	indexed, err := s.load()
	if err != nil {
		return nil, err
	}
	s.sweep(indexed)
	s.backfillPublicKeys()
	return s, nil
}

// List returns every profile in priority order.
func (s *Store) List() []Profile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.profiles)
}

func (s *Store) Get(id string) (Profile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i := s.indexOf(id)
	if i < 0 {
		return Profile{}, notFound(id)
	}
	return s.profiles[i], nil
}

// Content returns the stored profile text.
func (s *Store) Content(id string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.indexOf(id) < 0 {
		return nil, notFound(id)
	}
	content, err := readRegular(s.contentPath(id), MaxContentSize)
	if err != nil {
		return nil, fmt.Errorf("read profile %s: %w", id, err)
	}
	return content, nil
}

// Import validates the content with the backend of its kind and stores it.
func (s *Store) Import(req ImportRequest) (ImportResult, error) {
	if err := checkSize(req.Content); err != nil {
		return ImportResult{}, err
	}
	kind := req.Kind
	if kind == 0 {
		var ok bool
		if kind, ok = DetectKind(req.SourceFilename, req.Content); !ok {
			return ImportResult{}, invalidf("cannot tell whether this is an OpenVPN or a WireGuard profile")
		}
	}
	parsed, err := s.parse(kind, req.Content)
	if err != nil {
		return ImportResult{}, err
	}
	if err := checkSettings(kind, req.Settings); err != nil {
		return ImportResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	id, err := s.newID()
	if err != nil {
		return ImportResult{}, err
	}
	p := Profile{
		ID:   id,
		Name: s.uniqueName(importName(req, parsed, kind)),
		Kind: kind,
		Settings: Settings{
			AutoConnect:       req.Settings.AutoConnect,
			TunnelMode:        req.Settings.TunnelMode,
			Priority:          s.lastPriority() + 1,
			ExcludePrivateIPs: req.Settings.ExcludePrivateIPs,
			OnDemand:          req.Settings.OnDemand,
		},
		Imported: time.Now().UTC().Truncate(time.Millisecond),
		Summary:  parsed.Summary,
	}
	if err := writeFileAtomic(s.contentPath(id), parsed.Content); err != nil {
		return ImportResult{}, fmt.Errorf("store profile content: %w", err)
	}
	if err := s.commit(append(slices.Clone(s.profiles), p)); err != nil {
		if rmErr := os.Remove(s.contentPath(id)); rmErr != nil {
			err = errors.Join(err, rmErr)
		}
		return ImportResult{}, err
	}
	return ImportResult{Profile: p, Warnings: parsed.Warnings}, nil
}

// UpdateContent replaces the text of a profile and refreshes its summary. The
// text goes through the Parse of the profile's own backend, as an import does,
// and must not be recognizably of the other kind. The result has the same shape
// as an import's: the profile as it is now and what Parse removed.
func (s *Store) UpdateContent(id string, content []byte) (ImportResult, error) {
	if err := checkSize(content); err != nil {
		return ImportResult{}, err
	}
	current, err := s.Get(id)
	if err != nil {
		return ImportResult{}, err
	}
	if detected, ok := DetectKind("", content); ok && detected != current.Kind {
		return ImportResult{}, invalidf("this profile is %s, but the text is %s", displayName(current.Kind), displayName(detected))
	}
	parsed, err := s.parse(current.Kind, content)
	if err != nil {
		return ImportResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	i := s.indexOf(id)
	if i < 0 {
		return ImportResult{}, notFound(id)
	}
	old, err := readRegular(s.contentPath(id), MaxContentSize)
	if err != nil {
		return ImportResult{}, fmt.Errorf("read profile %s: %w", id, err)
	}
	p := s.profiles[i]
	p.Summary = parsed.Summary
	if err := writeFileAtomic(s.contentPath(id), parsed.Content); err != nil {
		return ImportResult{}, fmt.Errorf("store profile content: %w", err)
	}
	next := slices.Clone(s.profiles)
	next[i] = p
	if err := s.commit(next); err != nil {
		if restoreErr := writeFileAtomic(s.contentPath(id), old); restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("restore profile content: %w", restoreErr))
		}
		return ImportResult{}, err
	}
	return ImportResult{Profile: p, Warnings: parsed.Warnings}, nil
}

// checkSize refuses text that is empty or larger than an import may upload.
func checkSize(content []byte) error {
	if len(content) == 0 {
		return invalidf("profile is empty")
	}
	if len(content) > MaxContentSize {
		return invalidf("profile is %d bytes, the limit is %d", len(content), MaxContentSize)
	}
	return nil
}

// parse validates content with the backend of kind.
func (s *Store) parse(kind tunnel.Kind, content []byte) (tunnel.Parsed, error) {
	backend, ok := s.backends[kind]
	if !ok {
		return tunnel.Parsed{}, invalidf("profile kind %d is not supported", kind)
	}
	parsed, err := backend.Parse(content)
	if err != nil {
		return tunnel.Parsed{}, &InvalidError{Err: err}
	}
	if len(parsed.Content) == 0 || len(parsed.Content) > MaxContentSize {
		return tunnel.Parsed{}, fmt.Errorf("backend returned %d bytes of content for a %d byte profile", len(parsed.Content), len(content))
	}
	return parsed, nil
}

// checkSettings refuses the settings that make no sense for a profile of kind.
func checkSettings(kind tunnel.Kind, settings Settings) error {
	if settings.ExcludePrivateIPs && kind != tunnel.KindWireGuard {
		return invalidf("excluding private IPs works on WireGuard's AllowedIPs, and this profile is %s", displayName(kind))
	}
	return nil
}

func (s *Store) Update(id string, change Change) (Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	i := s.indexOf(id)
	if i < 0 {
		return Profile{}, notFound(id)
	}
	p := s.profiles[i]
	if change.Name != nil {
		name := cleanName(*change.Name)
		if name == "" {
			return Profile{}, invalidf("name must not be empty")
		}
		if s.nameTaken(name, id) {
			return Profile{}, invalidf("name %q is already used by another profile", name)
		}
		p.Name = name
	}
	if change.Settings != nil {
		if change.Settings.Priority < 0 {
			return Profile{}, invalidf("priority must not be negative")
		}
		if err := checkSettings(p.Kind, *change.Settings); err != nil {
			return Profile{}, err
		}
		keep := p.Settings.Priority
		p.Settings = *change.Settings
		if p.Settings.Priority == 0 {
			p.Settings.Priority = keep
		}
	}
	next := slices.Clone(s.profiles)
	next[i] = p
	if err := s.commit(next); err != nil {
		return Profile{}, err
	}
	return p, nil
}

// Delete removes the profile and its content.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	i := s.indexOf(id)
	if i < 0 {
		return notFound(id)
	}
	if err := s.commit(slices.Delete(slices.Clone(s.profiles), i, i+1)); err != nil {
		return err
	}
	// The profile is gone once the index says so; a leftover content file is
	// swept the next time the store is opened.
	if err := os.Remove(s.contentPath(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.log.Warn("could not remove content of deleted profile", "id", id, "err", err)
	}
	return nil
}

// Reorder rewrites every priority: ids lists every profile, highest priority first.
func (s *Store) Reorder(ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := make([]Profile, 0, len(ids))
	for _, id := range ids {
		i := s.indexOf(id)
		if i < 0 {
			return notFound(id)
		}
		if slices.ContainsFunc(next, func(p Profile) bool { return p.ID == id }) {
			return invalidf("profile %q is listed twice", id)
		}
		next = append(next, s.profiles[i])
	}
	if len(next) != len(s.profiles) {
		return invalidf("ids must list all %d profiles, got %d", len(s.profiles), len(next))
	}
	for i := range next {
		next[i].Settings.Priority = i + 1
	}
	return s.commit(next)
}

func notFound(id string) error { return fmt.Errorf("profile %q: %w", id, ErrNotFound) }

// indexOf requires s.mu.
func (s *Store) indexOf(id string) int {
	return slices.IndexFunc(s.profiles, func(p Profile) bool { return p.ID == id })
}

func (s *Store) lastPriority() int {
	last := 0
	for _, p := range s.profiles {
		last = max(last, p.Settings.Priority)
	}
	return last
}

func (s *Store) nameTaken(name, exceptID string) bool {
	return slices.ContainsFunc(s.profiles, func(p Profile) bool {
		return p.ID != exceptID && strings.EqualFold(p.Name, name)
	})
}

func (s *Store) uniqueName(base string) string {
	name := base
	for n := 2; s.nameTaken(name, ""); n++ {
		name = fmt.Sprintf("%s %d", base, n)
	}
	return name
}

func (s *Store) contentPath(id string) string {
	return filepath.Join(s.dir, contentDir, id+contentExt)
}

// newID returns 128 random bits as 26 base32 characters, which are also safe
// in a file name on a case-insensitive file system.
func (s *Store) newID() (string, error) {
	for {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return "", fmt.Errorf("generate profile id: %w", err)
		}
		id := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:])
		if s.indexOf(id) < 0 {
			return id, nil
		}
	}
}

// commit sorts next, writes it as the new index and only then makes it the
// current state, so that a failed write leaves the store as it was.
func (s *Store) commit(next []Profile) error {
	slices.SortStableFunc(next, comparePriority)
	index := diskIndex{Schema: schemaVersion, Profiles: make([]diskProfile, len(next))}
	for i, p := range next {
		index.Profiles[i] = toDisk(p)
	}
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return fmt.Errorf("encode profile index: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(s.dir, indexFile), data); err != nil {
		return fmt.Errorf("write profile index: %w", err)
	}
	s.profiles = next
	return nil
}

func comparePriority(a, b Profile) int {
	if c := a.Settings.Priority - b.Settings.Priority; c != 0 {
		return c
	}
	if c := a.Imported.Compare(b.Imported); c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}

// load reads the index and reports whether there was one.
func (s *Store) load() (indexed bool, err error) {
	data, err := readRegular(filepath.Join(s.dir, indexFile), maxIndexSize)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read profile index: %w", err)
	}
	var index diskIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return false, fmt.Errorf("parse profile index: %w", err)
	}
	if index.Schema != schemaVersion {
		return false, fmt.Errorf("profile index has schema version %d, this daemon reads version %d", index.Schema, schemaVersion)
	}
	profiles := make([]Profile, 0, len(index.Profiles))
	for _, d := range index.Profiles {
		p, err := fromDisk(d)
		if err != nil {
			return false, fmt.Errorf("profile index: %w", err)
		}
		if slices.ContainsFunc(profiles, func(o Profile) bool { return o.ID == p.ID }) {
			return false, fmt.Errorf("profile index: id %s appears twice", p.ID)
		}
		profiles = append(profiles, p)
	}
	slices.SortStableFunc(profiles, comparePriority)
	s.profiles = profiles
	return true, nil
}

// backfillPublicKeys derives the public key of the WireGuard profiles stored
// before the summary carried one, so that they show it without being edited. A
// profile whose text cannot be read or parsed any more keeps an empty key.
func (s *Store) backfillPublicKeys() {
	next := slices.Clone(s.profiles)
	changed := false
	for i, p := range next {
		if p.Kind != tunnel.KindWireGuard || p.Summary.PublicKey != "" {
			continue
		}
		content, err := readRegular(s.contentPath(p.ID), MaxContentSize)
		if err != nil {
			s.log.Warn("could not read a stored profile to derive its public key", "id", p.ID, "err", err)
			continue
		}
		// The error of Parse is not logged: it may quote the text, which holds the private key.
		parsed, err := s.parse(p.Kind, content)
		if err != nil || parsed.Summary.PublicKey == "" {
			s.log.Warn("could not derive the public key of a stored profile", "id", p.ID)
			continue
		}
		next[i].Summary.PublicKey = parsed.Summary.PublicKey
		changed = true
	}
	if !changed {
		return
	}
	if err := s.commit(next); err != nil {
		s.log.Warn("could not store the derived public keys", "err", err)
	}
}

// sweep removes what an interrupted write left behind: temporary files and
// content that no profile in the index refers to. Without an index (the first
// start, or an owner who removed a damaged one) it removes only the temporary
// files: the content may be all that is left of the profiles.
func (s *Store) sweep(indexed bool) {
	kept := 0
	for _, dir := range []string{s.dir, filepath.Join(s.dir, contentDir)} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			s.log.Warn("could not list state directory for cleanup", "dir", dir, "err", err)
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			orphan := dir != s.dir && strings.HasSuffix(name, contentExt) &&
				s.indexOf(strings.TrimSuffix(name, contentExt)) < 0
			if orphan && !indexed {
				kept++
				continue
			}
			if !strings.HasPrefix(name, tmpPrefix) && !orphan {
				continue
			}
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				s.log.Warn("could not remove leftover file", "file", filepath.Join(dir, name), "err", err)
			}
		}
	}
	if kept > 0 {
		s.log.Warn("profile index is missing, the stored profile files were kept", "files", kept, "dir", filepath.Join(s.dir, contentDir))
	}
}
