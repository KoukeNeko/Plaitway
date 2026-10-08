package windows

import (
	"errors"
	"fmt"
	"slices"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// SweepResult is what SweepOwnedDNS found and what it could not take away.
type SweepResult struct {
	// Removed are the keys that were listed and are gone.
	Removed []string
	// Remaining are the keys still listed after the sweep.
	Remaining []string
}

// SweepOwnedDNS removes every DNS rule that carries Plaitway's marker, whoever
// wrote it, and flushes the resolver cache. It needs nothing of the Reconciler
// or the daemon, because it is for the moments when neither runs: an uninstall
// after the service failed to start, where a catch-all rule that survived a
// crash would keep all DNS of the PC on a server that is no longer there, across
// reboots.
//
// The error joins every failure; the result is valid with it, and says what is
// left.
func SweepOwnedDNS(dns osnet.DNSConfigurator) (SweepResult, error) {
	found, err := dns.Owned()
	if err != nil {
		return SweepResult{}, fmt.Errorf("list the DNS rules: %w", err)
	}
	var errs []error
	for _, key := range found {
		// A key stands for its owner, so the rules after the first of an owner are
		// gone already; removing nothing is not an error.
		if err := dns.Remove(key); err != nil {
			errs = append(errs, fmt.Errorf("remove the DNS rule %s: %w", key, err))
		}
	}
	if len(found) > 0 {
		if err := dns.Flush(); err != nil {
			errs = append(errs, fmt.Errorf("flush the resolver cache: %w", err))
		}
	}
	remaining, err := dns.Owned()
	if err != nil {
		return SweepResult{}, errors.Join(append(errs, fmt.Errorf("list the DNS rules again: %w", err))...)
	}
	removed := slices.DeleteFunc(slices.Clone(found), func(key string) bool { return slices.Contains(remaining, key) })
	return SweepResult{Removed: removed, Remaining: remaining}, errors.Join(errs...)
}
