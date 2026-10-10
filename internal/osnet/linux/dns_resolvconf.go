package linux

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// resolvconfPaths are where openresolv installs resolvconf: /sbin on a system
// without a merged /usr, such as Gentoo, and /usr/sbin on the others.
var resolvconfPaths = []string{"/sbin/resolvconf", "/usr/sbin/resolvconf", "/usr/bin/resolvconf", "/bin/resolvconf"}

const (
	// openresolvName is what "resolvconf --version" prints first. Another
	// resolvconf has other options, so it is not driven.
	openresolvName = "openresolv"
	// noResolvconfFiles starts the message of "resolvconf -i" and "-l" when
	// nothing matches (and the exit status is 2). It goes on with "interface"
	// in openresolv 3.13 and "key" in 3.16.
	noResolvconfFiles = "No resolv.conf for "
	// maxResolvconfName keeps the name far below the 255 bytes of a file name.
	maxResolvconfName = 200
)

var (
	errNoResolvconf  = errors.New("resolvconf not found")
	errNotOpenresolv = errors.New("resolvconf is not openresolv")
	// errSplitDNS is the answer to an entry that is for some domains only. The
	// profile shows it as the reason the entry is not installed.
	errSplitDNS = errors.New("resolvconf has one list of servers for every name, so only a full tunnel can set DNS")
)

// resolvconfConfigurator implements osnet.DNSConfigurator with openresolv's
// resolvconf, for a host without systemd-resolved (Gentoo with OpenRC).
//
// resolvconf keeps one resolv.conf for each name it is given and writes
// /etc/resolv.conf from all of them. The entry of a full tunnel is added as the
// exclusive one, which makes it the only list of servers in use, and deleting it
// brings the others back. An entry for some domains only cannot be expressed
// there and is refused.
//
// Unlike the links of systemd-resolved, these entries do not go away with the
// tunnel: they stay in /run/resolvconf until deleted, whoever added them. The
// marker that Owned finds again after a crash is therefore in the name:
//
//	plaitway:<owner>:<interface>
//
// with both parts escaped so that neither holds a ':', a '.' (resolvconf reads
// what follows a dot as a protocol) or a character of a shell pattern.
type resolvconfConfigurator struct {
	run     CommandRunner
	log     *slog.Logger
	timeout time.Duration

	// mu serializes the calls of one operation.
	mu sync.Mutex
	// checked is set once resolvconf has said it is openresolv.
	checked bool
}

func newResolvconfConfigurator(opts DNSOptions) *resolvconfConfigurator {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &resolvconfConfigurator{run: opts.Run, log: log, timeout: dnsCommandTimeout}
}

func (r *resolvconfConfigurator) Apply(owner string, entries []osnet.DNSEntry) error {
	if err := validateOwner(owner); err != nil {
		return err
	}
	if len(entries) == 0 {
		return r.Remove(owner)
	}
	servers, err := planResolvconf(entries)
	if err != nil {
		return fmt.Errorf("apply DNS for %s: %w", owner, err)
	}
	names := make(map[string]string, len(servers)) // resolvconf name by interface
	for iface := range servers {
		name, err := resolvconfName(owner, iface)
		if err != nil {
			return fmt.Errorf("apply DNS for %s: %w", owner, err)
		}
		names[iface] = name
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.check(); err != nil {
		return fmt.Errorf("apply DNS for %s: %w", owner, err)
	}
	before, err := r.list(ownerPattern(owner))
	if err != nil {
		return fmt.Errorf("apply DNS for %s: %w", owner, err)
	}
	for _, iface := range slices.Sorted(maps.Keys(servers)) {
		if err := r.write(names[iface], servers[iface]); err != nil {
			// An interface with half of the configuration is worse than one
			// without: the Reconciler reports the failure and writes it all again.
			return errors.Join(fmt.Errorf("apply DNS for %s: %s: %w", owner, iface, err), r.revert(owner))
		}
	}
	// The interfaces the owner had and no longer uses go last: two lists of
	// servers for a moment leak less than none.
	if err := r.deleteNames(before, slices.Collect(maps.Values(names))); err != nil {
		return fmt.Errorf("apply DNS for %s: %w", owner, err)
	}
	return nil
}

// Remove deletes the entries of owner. It also accepts a key returned by
// Owned, which stands for the owner that key belongs to.
func (r *resolvconfConfigurator) Remove(ownerOrKey string) error {
	owner := ownerOrKey
	if o, ok := ownerOfKey(ownerOrKey); ok {
		owner = o
	}
	if err := validateOwner(owner); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.check(); err != nil {
		if errors.Is(err, errNoResolvconf) {
			return nil // nothing was applied through it, or nothing is left of it
		}
		return fmt.Errorf("remove DNS of %s: %w", owner, err)
	}
	if err := r.revert(owner); err != nil {
		return fmt.Errorf("remove DNS of %s: %w", owner, err)
	}
	return nil
}

// Owned lists the keys of every entry that carries the marker, which is what
// a daemon that was killed leaves in /run/resolvconf. A name that looks like
// ours and is not one Apply could have written belongs to somebody else and is
// not listed: deleting it would destroy their configuration.
func (r *resolvconfConfigurator) Owned() ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.check(); err != nil {
		if errors.Is(err, errNoResolvconf) {
			return nil, nil
		}
		return nil, fmt.Errorf("list DNS keys: %w", err)
	}
	names, err := r.list(dnsKeyPrefix + "*")
	if err != nil {
		return nil, fmt.Errorf("list DNS keys: %w", err)
	}
	var keys []string
	for _, name := range names {
		if owner, iface, ok := parseResolvconfName(name); ok {
			keys = append(keys, dnsKey(owner, iface))
		}
	}
	slices.Sort(keys)
	return keys, nil
}

// Flush does nothing: there is no cache here, and resolvconf updates what
// depends on its files whenever one changes.
func (r *resolvconfConfigurator) Flush() error { return nil }

// planResolvconf validates the entries and returns the servers of each
// interface, in Order order. An interface whose entries are for some domains
// only is an error, and nothing is written for any.
func planResolvconf(entries []osnet.DNSEntry) (map[string][]string, error) {
	links, err := planLinks(entries)
	if err != nil {
		return nil, err
	}
	servers := make(map[string][]string, len(links))
	for iface, cfg := range links {
		if !cfg.DefaultRoute {
			domains := make([]string, len(cfg.Domains))
			for i, d := range cfg.Domains {
				domains[i] = strings.TrimPrefix(d, "~")
			}
			return nil, fmt.Errorf("DNS for %s needs systemd-resolved: %w", strings.Join(domains, ", "), errSplitDNS)
		}
		servers[iface] = cfg.Servers
	}
	return servers, nil
}

// write adds the resolv.conf of one interface as the exclusive one, and reads
// back what resolvconf holds for it.
func (r *resolvconfConfigurator) write(name string, servers []string) error {
	var body strings.Builder
	for _, s := range servers {
		body.WriteString("nameserver " + s + "\n")
	}
	if _, err := r.resolvconf(body.String(), "-x", "-a", name); err != nil {
		return err
	}
	out, err := r.resolvconf("", "-l", name)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	var got []string
	for _, line := range strings.Split(out, "\n") {
		if fields := strings.Fields(line); len(fields) == 2 && fields[0] == "nameserver" {
			got = append(got, fields[1])
		}
	}
	if got = canonicalServers(got); !slices.Equal(got, servers) {
		return fmt.Errorf("resolvconf holds servers %v, wanted %v", got, servers)
	}
	return nil
}

// deleteNames deletes the entries in names that are not in keep. Deleting a name
// that is not there is not an error.
func (r *resolvconfConfigurator) deleteNames(names, keep []string) error {
	var errs []error
	for _, name := range names {
		if slices.Contains(keep, name) {
			continue
		}
		if _, err := r.resolvconf("", "-f", "-d", name); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// revert deletes every entry of owner, as it is now.
func (r *resolvconfConfigurator) revert(owner string) error {
	names, err := r.list(ownerPattern(owner))
	if err != nil {
		return err
	}
	return r.deleteNames(names, nil)
}

// list returns the names resolvconf has a resolv.conf for that match pattern.
func (r *resolvconfConfigurator) list(pattern string) ([]string, error) {
	out, err := r.resolvconf("", "-i", pattern)
	if err != nil {
		if strings.Contains(out, noResolvconfFiles) {
			return nil, nil
		}
		return nil, err
	}
	return strings.Fields(out), nil
}

// check says why resolvconf cannot be used: it is missing, it is not
// openresolv, or it does not answer. The answer is kept once it is good.
func (r *resolvconfConfigurator) check() error {
	if r.checked {
		return nil
	}
	out, err := r.resolvconf("", "--version")
	if err != nil {
		if errors.Is(err, errNoResolvconf) || errors.Is(err, errTimedOut) {
			return err
		}
		return fmt.Errorf("%w: %s", errNotOpenresolv, strings.TrimSpace(out))
	}
	if first, _, _ := strings.Cut(strings.TrimSpace(out), "\n"); !strings.Contains(first, openresolvName) {
		return fmt.Errorf("%w: %q", errNotOpenresolv, first)
	}
	r.checked = true
	return nil
}

// resolvconf runs resolvconf and returns its combined output, which a failure
// carries too.
func (r *resolvconfConfigurator) resolvconf(stdin string, args ...string) (string, error) {
	r.log.Debug("running resolvconf", "args", args)
	for _, path := range resolvconfPaths {
		ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
		out, err := r.run(ctx, path, args, stdin)
		timedOut := ctx.Err() != nil
		cancel()
		switch {
		case err == nil:
			return string(out), nil
		case errors.Is(err, fs.ErrNotExist):
			continue
		case timedOut:
			return string(out), fmt.Errorf("resolvconf %s: %w after %s", strings.Join(args, " "), errTimedOut, r.timeout)
		default:
			return string(out), fmt.Errorf("resolvconf %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	r.checked = false
	return "", errNoResolvconf
}

// escapeResolvconfPart makes s fit in a resolvconf name: no ':' (the separator),
// no '.' (the start of a protocol), no character of a shell pattern, and a
// result that reads back to s.
func escapeResolvconfPart(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), ".", "%2E")
}

// resolvconfName is the name of the entry of the interface iface of owner.
func resolvconfName(owner, iface string) (string, error) {
	name := dnsKeyPrefix + escapeResolvconfPart(owner) + ":" + escapeResolvconfPart(iface)
	if len(name) > maxResolvconfName {
		return "", fmt.Errorf("the name of the DNS entry of %q on %s is too long", owner, iface)
	}
	return name, nil
}

// ownerPattern is the pattern that matches the entries of owner and no other.
func ownerPattern(owner string) string {
	return dnsKeyPrefix + escapeResolvconfPart(owner) + ":*"
}

// parseResolvconfName returns the owner and the interface of a name that
// resolvconfName could have made.
func parseResolvconfName(name string) (owner, iface string, ok bool) {
	rest, ok := strings.CutPrefix(name, dnsKeyPrefix)
	if !ok {
		return "", "", false
	}
	ownerPart, ifacePart, ok := strings.Cut(rest, ":")
	if !ok {
		return "", "", false
	}
	owner, err := url.QueryUnescape(ownerPart)
	if err != nil || validateOwner(owner) != nil {
		return "", "", false
	}
	iface, err = url.QueryUnescape(ifacePart)
	if err != nil || validateIface(iface) != nil {
		return "", "", false
	}
	if again, err := resolvconfName(owner, iface); err != nil || again != name {
		return "", "", false
	}
	return owner, iface, true
}
