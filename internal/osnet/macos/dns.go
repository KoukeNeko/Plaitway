package macos

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

const (
	scutilPath      = "/usr/sbin/scutil"
	dscacheutilPath = "/usr/bin/dscacheutil"
	killallPath     = "/usr/bin/killall"
	commandTimeout  = 10 * time.Second
)

// CommandRunner runs a program with stdin and returns its combined output.
type CommandRunner func(ctx context.Context, name string, args []string, stdin string) ([]byte, error)

// DNSOptions configures NewDNS.
type DNSOptions struct {
	// Run runs the system tools; nil runs them for real.
	Run CommandRunner
}

// dnsConfigurator implements osnet.DNSConfigurator with scutil. It writes
// State: keys of the dynamic store, which vanish with configd and never
// touch the user's network settings.
type dnsConfigurator struct {
	run CommandRunner
}

func newDNSConfigurator(opts DNSOptions) *dnsConfigurator {
	return &dnsConfigurator{run: opts.Run}
}

func (d *dnsConfigurator) Apply(owner string, entries []osnet.DNSEntry) error {
	if err := validateOwner(owner); err != nil {
		return err
	}
	if len(entries) == 0 {
		return d.Remove(owner)
	}
	keys, err := planDNSKeys(owner, entries)
	if err != nil {
		return fmt.Errorf("apply DNS for %s: %w", owner, err)
	}
	existing, err := d.keysOf(owner)
	if err != nil {
		return fmt.Errorf("apply DNS for %s: %w", owner, err)
	}
	wanted := make([]string, len(keys))
	for i, key := range keys {
		wanted[i] = key.Name
	}
	stale := slices.DeleteFunc(slices.Clone(existing), func(k string) bool { return slices.Contains(wanted, k) })
	return d.write(owner, applyScript(owner, keys, stale), wanted)
}

// Remove deletes everything applied for owner. It also accepts a key returned by
// Owned, which stands for the owner that key belongs to: the Reconciler's sweep
// of leftovers passes the keys it found.
func (d *dnsConfigurator) Remove(ownerOrKey string) error {
	owner := ownerOrKey
	if o, ok := ownerOfKey(ownerOrKey); ok {
		owner = o
	}
	if err := validateOwner(owner); err != nil {
		return err
	}
	existing, err := d.keysOf(owner)
	if err != nil {
		return fmt.Errorf("remove DNS of %s: %w", owner, err)
	}
	if len(existing) == 0 {
		return nil
	}
	return d.write(owner, removeScript(existing), nil)
}

func (d *dnsConfigurator) Owned() ([]string, error) {
	out, err := d.scutil("list " + dnsKeyPattern + "\nquit\n")
	if err != nil {
		return nil, fmt.Errorf("list DNS keys: %w", err)
	}
	keys, err := parseKeyList(out)
	if err != nil {
		return nil, err
	}
	// The pattern is a search, not a match of the whole key.
	return slices.DeleteFunc(keys, func(k string) bool { _, ok := ownerOfKey(k); return !ok }), nil
}

// Flush drops the caches of the resolver library and of mDNSResponder. Both are
// tried; the errors are joined.
func (d *dnsConfigurator) Flush() error {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	var errs []error
	if out, err := d.run(ctx, dscacheutilPath, []string{"-flushcache"}, ""); err != nil {
		errs = append(errs, fmt.Errorf("dscacheutil -flushcache: %w: %s", err, strings.TrimSpace(string(out))))
	}
	if out, err := d.run(ctx, killallPath, []string{"-HUP", "mDNSResponder"}, ""); err != nil {
		errs = append(errs, fmt.Errorf("killall -HUP mDNSResponder: %w: %s", err, strings.TrimSpace(string(out))))
	}
	return errors.Join(errs...)
}

// keysOf lists the keys of one owner.
func (d *dnsConfigurator) keysOf(owner string) ([]string, error) {
	owned, err := d.Owned()
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(owned, func(k string) bool { o, _ := ownerOfKey(k); return o != owner }), nil
}

// write runs a script that changes the keys of owner, then reads them back.
// scutil exits 0 and prints a message when a command fails, so the keys are the
// proof: afterwards exactly the wanted keys must exist for the owner.
func (d *dnsConfigurator) write(owner, script string, wanted []string) error {
	out, err := d.scutil(script)
	if err != nil {
		return fmt.Errorf("write DNS of %s: %w", owner, err)
	}
	after, err := d.keysOf(owner)
	if err != nil {
		return fmt.Errorf("verify DNS of %s: %w", owner, err)
	}
	slices.Sort(after)
	want := slices.Sorted(slices.Values(wanted))
	if !slices.Equal(after, want) {
		return fmt.Errorf("write DNS of %s: keys are %v, want %v (scutil said %q)", owner, after, want, strings.TrimSpace(out))
	}
	return nil
}

// scutil runs one scutil session and returns what it printed.
func (d *dnsConfigurator) scutil(script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	out, err := d.run(ctx, scutilPath, nil, script)
	if err != nil {
		return "", fmt.Errorf("scutil: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
