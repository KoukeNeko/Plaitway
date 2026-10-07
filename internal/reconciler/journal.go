package reconciler

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

const (
	kindRoute    = "route"
	kindResolver = "resolver"

	statePending = "pending"
	stateApplied = "applied"
	stateRemoved = "removed"

	// journalView is how many recent records Report returns.
	journalView = 256
	// defaultCompactAfter is how many appended records trigger a rewrite that
	// drops the finished ones, so the file does not grow without bound.
	defaultCompactAfter = 1000
)

// record is one line of the journal. A route is journaled pending before it is
// added, applied after, and removed after it is deleted; a crash between the
// steps leaves the last state for the next run to find.
type record struct {
	Seq   uint64         `json:"seq"`
	Time  time.Time      `json:"time"`
	Owner tunnel.OwnerID `json:"owner"`
	Kind  string         `json:"kind"`
	// Key is the destination prefix of a route, or the owner of a resolver entry.
	Key   string `json:"key"`
	State string `json:"state"`
	// Gateway and Iface are what the route was asked to use. They are enough to
	// recognize it again while Fingerprint, which includes the kernel's flags,
	// is not known yet.
	Gateway     string `json:"gateway,omitempty"`
	Iface       string `json:"iface,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Note        string `json:"note,omitempty"`
}

func (r record) view() tunnel.JournalRecord {
	return tunnel.JournalRecord{Time: r.Time, Owner: r.Owner, Kind: r.Kind, Key: r.Key, State: r.State}
}

// journal is the write-ahead log. It is not safe for concurrent use; the
// Reconciler serializes access.
type journal struct {
	path     string
	file     *os.File
	now      func() time.Time
	log      *slog.Logger
	seq      uint64
	live     map[string]record // latest record per key, until it is removed
	recent   []tunnel.JournalRecord
	appended int
	// compactAfter is how many records may be appended between rewrites.
	compactAfter int
	// noSync skips the disk sync; tests that write thousands of records set it.
	noSync bool
}

func liveKey(kind, key string) string { return kind + " " + key }

// openJournal reads the journal a previous run left behind and rewrites it
// without the finished records, which also drops a line cut short by a crash.
// What is left, see unresolved, is what that run did not clean up.
func openJournal(path string, now func() time.Time, log *slog.Logger) (*journal, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating journal directory: %w", err)
	}
	j := &journal{path: path, now: now, log: log, live: make(map[string]record), compactAfter: defaultCompactAfter}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("reading journal: %w", err)
	}
	for i, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec record
		if err := json.Unmarshal(line, &rec); err != nil {
			log.Warn("skipping unreadable journal line", "line", i+1, "err", err)
			continue
		}
		j.seq = max(j.seq, rec.Seq)
		j.track(rec)
	}
	if err := j.rewrite(); err != nil {
		return nil, err
	}
	return j, nil
}

func (j *journal) track(rec record) {
	key := liveKey(rec.Kind, rec.Key)
	if rec.State == stateRemoved {
		delete(j.live, key)
		return
	}
	j.live[key] = rec
}

// unresolved returns what the journal still considers installed, oldest first.
func (j *journal) unresolved() []record {
	out := make([]record, 0, len(j.live))
	for _, rec := range j.live {
		out = append(out, rec)
	}
	slices.SortFunc(out, func(a, b record) int { return cmp.Compare(a.Seq, b.Seq) })
	return out
}

// append writes the record and syncs it to disk before returning, so that
// whatever happens next, the journal is at least as new as the host.
func (j *journal) append(rec record) error {
	j.seq++
	rec.Seq, rec.Time = j.seq, j.now()
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encoding journal record: %w", err)
	}
	if _, err := j.file.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("writing journal: %w", err)
	}
	if !j.noSync {
		if err := j.file.Sync(); err != nil {
			return fmt.Errorf("syncing journal: %w", err)
		}
	}
	j.track(rec)
	j.recent = append(j.recent, rec.view())
	if len(j.recent) > journalView {
		j.recent = slices.Delete(j.recent, 0, len(j.recent)-journalView)
	}
	if j.appended++; j.appended >= j.compactAfter {
		if err := j.rewrite(); err != nil {
			j.log.Warn("compacting the journal failed", "err", err)
		}
	}
	return nil
}

// rewrite replaces the file with the unresolved records, atomically, and
// reopens it for appending.
func (j *journal) rewrite() error {
	tmp := j.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("creating journal: %w", err)
	}
	enc := json.NewEncoder(f)
	for _, rec := range j.unresolved() {
		if err := enc.Encode(rec); err != nil {
			f.Close()
			return fmt.Errorf("writing journal: %w", err)
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("syncing journal: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing journal: %w", err)
	}
	if err := os.Rename(tmp, j.path); err != nil {
		return fmt.Errorf("replacing journal: %w", err)
	}
	if dir, err := os.Open(filepath.Dir(j.path)); err == nil {
		dir.Sync() // the rename itself; best effort, not every filesystem allows it
		dir.Close()
	}
	if j.file != nil {
		j.file.Close()
	}
	j.file, err = os.OpenFile(j.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("reopening journal: %w", err)
	}
	j.appended = 0
	return nil
}

func (j *journal) view() []tunnel.JournalRecord { return slices.Clone(j.recent) }

func (j *journal) close() error {
	if j.file == nil {
		return nil
	}
	err := j.file.Close()
	j.file = nil
	return err
}
