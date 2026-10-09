package fake

import (
	"errors"
	"slices"
	"sync"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// DNSOp is one recorded call on DNS.
type DNSOp struct {
	Kind  string // apply, remove, flush
	Owner string
}

// DNS is an osnet.DNSConfigurator that keeps its entries in memory, keyed by
// owner. Everything in it carries the marker, so Owned lists every owner; the
// owner string is also the key Owned returns, so Remove(key) removes it.
type DNS struct {
	mu      sync.Mutex
	entries map[string][]osnet.DNSEntry
	ops     []DNSOp
	faults  map[string]*dnsFault
}

type dnsFault struct {
	err   error
	times int
}

func NewDNS() *DNS {
	return &DNS{
		entries: make(map[string][]osnet.DNSEntry),
		faults:  make(map[string]*dnsFault),
	}
}

func (d *DNS) Apply(owner string, entries []osnet.DNSEntry) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.faultLocked("apply"); err != nil {
		return err
	}
	d.ops = append(d.ops, DNSOp{Kind: "apply", Owner: owner})
	if len(entries) == 0 {
		delete(d.entries, owner)
		return nil
	}
	d.entries[owner] = cloneEntries(entries)
	return nil
}

func (d *DNS) Remove(owner string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.faultLocked("remove"); err != nil {
		return err
	}
	d.ops = append(d.ops, DNSOp{Kind: "remove", Owner: owner})
	delete(d.entries, owner)
	return nil
}

func (d *DNS) Owned() ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.faultLocked("owned"); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(d.entries))
	for k := range d.entries {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys, nil
}

func (d *DNS) Flush() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.faultLocked("flush"); err != nil {
		return err
	}
	d.ops = append(d.ops, DNSOp{Kind: "flush"})
	return nil
}

// Entries returns what is applied for owner.
func (d *DNS) Entries(owner string) []osnet.DNSEntry {
	d.mu.Lock()
	defer d.mu.Unlock()
	return cloneEntries(d.entries[owner])
}

// All returns everything that is applied, by owner.
func (d *DNS) All() map[string][]osnet.DNSEntry {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string][]osnet.DNSEntry, len(d.entries))
	for k, v := range d.entries {
		out[k] = cloneEntries(v)
	}
	return out
}

// Leave plants entries as an earlier run that crashed would have left them:
// marked, and unknown to the current process. Nothing is recorded.
func (d *DNS) Leave(owner string, entries ...osnet.DNSEntry) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.entries[owner] = cloneEntries(entries)
}

// Ops returns the recorded calls, oldest first.
func (d *DNS) Ops() []DNSOp {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.ops)
}

// Fail makes the next times calls of op (apply, remove, owned or flush) return
// err. It replaces an earlier fault for the same op.
func (d *DNS) Fail(op string, err error, times int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if times <= 0 {
		times = 1
	}
	d.faults[op] = &dnsFault{err: err, times: times}
}

func (d *DNS) faultLocked(op string) error {
	f, ok := d.faults[op]
	if !ok {
		return nil
	}
	f.times--
	if f.times == 0 {
		delete(d.faults, op)
	}
	if f.err == nil {
		return errors.New("fake dns: injected fault")
	}
	return f.err
}

func cloneEntries(in []osnet.DNSEntry) []osnet.DNSEntry {
	if in == nil {
		return nil
	}
	out := make([]osnet.DNSEntry, len(in))
	for i, e := range in {
		out[i] = osnet.DNSEntry{
			Servers:      slices.Clone(e.Servers),
			MatchDomains: slices.Clone(e.MatchDomains),
			Order:        e.Order,
			Iface:        e.Iface,
		}
	}
	return out
}
