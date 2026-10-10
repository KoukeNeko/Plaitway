package linux

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

const (
	// resolvectl lives in /usr/bin on systems with a merged /usr and in /bin on
	// the others.
	resolvectlPath    = "/usr/bin/resolvectl"
	resolvectlAltPath = "/bin/resolvectl"
	dnsCommandTimeout = 10 * time.Second
	// Every key Owned returns is plaitway:<owner>:<iface>, the owner escaped.
	dnsKeyPrefix = "plaitway:"
	// maxIfaceName is IFNAMSIZ less the terminating NUL.
	maxIfaceName = 15
	// resolvectl prints these when the link is not there to be read or reverted:
	// the interface does not exist (strerror text, hence LC_ALL=C), or it exists
	// but systemd-resolved has no object for it yet or any more.
	noSuchDevice  = "No such device"
	unknownObject = "Unknown object"
	// globalHeader is the first line "resolvectl dns" prints once it has reached
	// systemd-resolved.
	globalHeader = "Global:"
)

var (
	errNoResolvectl = errors.New("resolvectl not found")
	errNoResolved   = errors.New("systemd-resolved is not running")
	errLinkGone     = errors.New("no such link")
	errTimedOut     = errors.New("timed out")

	labelPattern = regexp.MustCompile(`^[A-Za-z0-9_]([A-Za-z0-9_-]{0,61}[A-Za-z0-9_])?$`)
	// linkLine is the line "resolvectl dns|domain|default-route IFACE" prints.
	linkLine = regexp.MustCompile(`^Link [0-9]+ \((.+)\):(.*)$`)
)

// CommandRunner runs a program with stdin and returns its combined output.
type CommandRunner func(ctx context.Context, name string, args []string, stdin string) ([]byte, error)

// DNSOptions configures NewDNS.
type DNSOptions struct {
	// Run runs the system tools; nil runs them for real.
	Run CommandRunner
	// Logger receives debug and warning messages; nil uses slog.Default.
	Logger *slog.Logger
}

// linkConfig is the resolver configuration of one link, in the form
// resolvectl takes and prints it.
type linkConfig struct {
	// Servers are in the order systemd-resolved tries them.
	Servers []string
	// Domains are routing-only domains ("~corp.example", "~." for every domain),
	// compared as a set.
	Domains []string
	// DefaultRoute lets the link receive queries that match no domain of any
	// link.
	DefaultRoute bool
}

func (c linkConfig) equal(o linkConfig) bool {
	return c.DefaultRoute == o.DefaultRoute && slices.Equal(c.Servers, o.Servers) &&
		slices.Equal(slices.Sorted(slices.Values(c.Domains)), slices.Sorted(slices.Values(o.Domains)))
}

func (c linkConfig) String() string {
	return fmt.Sprintf("servers %v, domains %v, default route %t", c.Servers, c.Domains, c.DefaultRoute)
}

// dnsConfigurator implements osnet.DNSConfigurator with systemd-resolved,
// driven through resolvectl. It writes per-link settings of the tunnel
// interface of the entry, which resolved forgets when the link goes away and
// which never touch the network settings of the host's other links.
type dnsConfigurator struct {
	run     CommandRunner
	log     *slog.Logger
	timeout time.Duration
	// noResolvconf is set on a host that has no resolvconf either, and makes the
	// error of a missing resolvectl say what could be installed.
	noResolvconf bool

	// mu guards applied, and serializes the resolvectl calls of one operation.
	mu sync.Mutex
	// applied is what Apply wrote and Remove has not reverted, by owner and
	// interface. It is the whole marker: the links are the tunnels of this
	// daemon and die with it, taking their settings along, so there is nothing
	// durable to find again after a restart.
	applied map[string]map[string]linkConfig
}

func newDNSConfigurator(opts DNSOptions) *dnsConfigurator {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &dnsConfigurator{
		run:     opts.Run,
		log:     log,
		timeout: dnsCommandTimeout,
		applied: make(map[string]map[string]linkConfig),
	}
}

func (d *dnsConfigurator) Apply(owner string, entries []osnet.DNSEntry) error {
	if err := validateOwner(owner); err != nil {
		return err
	}
	if len(entries) == 0 {
		return d.Remove(owner)
	}
	links, err := planLinks(entries)
	if err != nil {
		return fmt.Errorf("apply DNS for %s: %w", owner, err)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.checkResolved(); err != nil {
		return fmt.Errorf("apply DNS for %s: %w", owner, err)
	}
	// Track the links before writing them, so that whatever a failure leaves on
	// them is found by Remove.
	tracked := d.applied[owner]
	if tracked == nil {
		tracked = make(map[string]linkConfig)
		d.applied[owner] = tracked
	}
	var stale []string
	for _, iface := range slices.Sorted(maps.Keys(tracked)) {
		if _, ok := links[iface]; !ok {
			stale = append(stale, iface)
		}
	}
	maps.Copy(tracked, links)

	if err := d.applyLinks(links, stale); err != nil {
		// A link with half of the configuration is worse than one without: the
		// Reconciler reports the failure and writes everything again.
		return errors.Join(fmt.Errorf("apply DNS for %s: %w", owner, err), d.revertOwner(owner))
	}
	d.applied[owner] = links
	return nil
}

// Remove reverts the links applied for owner. It also accepts a key returned by
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
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.applied[owner]) == 0 {
		delete(d.applied, owner)
		return nil
	}
	if err := d.checkResolved(); err != nil {
		if unavailable(err) {
			// resolved keeps its settings in memory: with it gone, so are they.
			delete(d.applied, owner)
			return nil
		}
		return fmt.Errorf("remove DNS of %s: %w", owner, err)
	}
	if err := d.revertOwner(owner); err != nil {
		return fmt.Errorf("remove DNS of %s: %w", owner, err)
	}
	return nil
}

// Owned lists the keys of the links that still carry the servers Apply wrote.
// A link that is gone, or that somebody else has configured since, is no longer
// ours and is forgotten: reverting it would destroy their configuration.
func (d *dnsConfigurator) Owned() ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.applied) == 0 {
		return nil, nil
	}
	if err := d.checkResolved(); err != nil {
		if unavailable(err) {
			clear(d.applied)
			return nil, nil
		}
		return nil, fmt.Errorf("list DNS keys: %w", err)
	}
	var keys []string
	for _, owner := range slices.Sorted(maps.Keys(d.applied)) {
		links := d.applied[owner]
		for _, iface := range slices.Sorted(maps.Keys(links)) {
			servers, err := d.query("dns", iface)
			switch {
			case errors.Is(err, errLinkGone):
				d.log.Info("link of DNS entries is gone", "owner", owner, "iface", iface)
				delete(links, iface)
			case err != nil:
				return nil, fmt.Errorf("list DNS keys: %s: %w", iface, err)
			case !slices.Equal(canonicalServers(servers), links[iface].Servers):
				d.log.Warn("link no longer carries the DNS servers applied to it", "owner", owner, "iface", iface, "servers", servers)
				delete(links, iface)
			default:
				keys = append(keys, dnsKey(owner, iface))
			}
		}
		if len(links) == 0 {
			delete(d.applied, owner)
		}
	}
	return keys, nil
}

// Flush drops the caches of systemd-resolved.
func (d *dnsConfigurator) Flush() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.checkResolved(); err != nil {
		return fmt.Errorf("flush DNS caches: %w", err)
	}
	if _, err := d.resolvectl("flush-caches"); err != nil {
		return fmt.Errorf("flush DNS caches: %w", err)
	}
	return nil
}

// unavailable reports whether err says that resolved cannot be reached at all.
func unavailable(err error) bool {
	return errors.Is(err, errNoResolved) || errors.Is(err, errNoResolvectl)
}

// applyLinks writes every link and reads it back, then reverts the interfaces
// the owner had before and no longer uses. The new links go first: two links
// that answer for the same domains for a moment leak less than none.
func (d *dnsConfigurator) applyLinks(links map[string]linkConfig, stale []string) error {
	for _, iface := range slices.Sorted(maps.Keys(links)) {
		if err := d.writeLink(iface, links[iface]); err != nil {
			return fmt.Errorf("%s: %w", iface, err)
		}
	}
	for _, iface := range stale {
		if err := d.revertLink(iface); err != nil {
			return err
		}
	}
	return nil
}

// writeLink sets the three per-link settings and reads them back. The routing
// settings go first and the servers last, so that the link gets queries only
// once it is set up to get the right ones.
func (d *dnsConfigurator) writeLink(iface string, want linkConfig) error {
	route := "no"
	if want.DefaultRoute {
		route = "yes"
	}
	// "--" ends the options: no value can be read as one, whatever it contains.
	for _, args := range [][]string{
		{"default-route", "--", iface, route},
		append([]string{"domain", "--", iface}, want.Domains...),
		append([]string{"dns", "--", iface}, want.Servers...),
	} {
		if _, err := d.resolvectl(args...); err != nil {
			return err
		}
	}
	got, err := d.readLink(iface)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	if !got.equal(want) {
		return fmt.Errorf("resolved reports %s, wanted %s", got, want)
	}
	return nil
}

// readLink reads the configuration resolved holds for iface.
func (d *dnsConfigurator) readLink(iface string) (linkConfig, error) {
	servers, err := d.query("dns", iface)
	if err != nil {
		return linkConfig{}, err
	}
	domains, err := d.query("domain", iface)
	if err != nil {
		return linkConfig{}, err
	}
	route, err := d.query("default-route", iface)
	if err != nil {
		return linkConfig{}, err
	}
	cfg := linkConfig{Servers: canonicalServers(servers), Domains: canonicalDomains(domains)}
	switch {
	case slices.Equal(route, []string{"yes"}):
		cfg.DefaultRoute = true
	case slices.Equal(route, []string{"no"}):
	default:
		return linkConfig{}, fmt.Errorf("unexpected default route %q", route)
	}
	return cfg, nil
}

// query reads one setting of a link: the words resolvectl prints after
// "Link N (iface):". A link that does not exist, or that resolved does not know,
// is errLinkGone.
func (d *dnsConfigurator) query(verb, iface string) ([]string, error) {
	out, err := d.resolvectl(verb, "--", iface)
	if err != nil {
		if isLinkGone(out) {
			return nil, fmt.Errorf("%w: %w", errLinkGone, err)
		}
		return nil, err
	}
	for _, line := range strings.Split(out, "\n") {
		m := linkLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		if m[1] != iface {
			return nil, fmt.Errorf("resolvectl %s reported link %q, asked for %q", verb, m[1], iface)
		}
		return strings.Fields(m[2]), nil
	}
	return nil, fmt.Errorf("unexpected resolvectl %s output: %q", verb, strings.TrimSpace(out))
}

// revertOwner reverts every link tracked for owner. A link that is gone has
// nothing to revert. Links it could not revert stay tracked, so that the next
// attempt finds them.
func (d *dnsConfigurator) revertOwner(owner string) error {
	links := d.applied[owner]
	var errs []error
	for _, iface := range slices.Sorted(maps.Keys(links)) {
		if err := d.revertLink(iface); err != nil {
			errs = append(errs, err)
			continue
		}
		delete(links, iface)
	}
	if len(links) == 0 {
		delete(d.applied, owner)
	}
	return errors.Join(errs...)
}

func (d *dnsConfigurator) revertLink(iface string) error {
	out, err := d.resolvectl("revert", "--", iface)
	if err != nil && !isLinkGone(out) {
		return fmt.Errorf("%s: %w", iface, err)
	}
	return nil
}

func isLinkGone(out string) bool {
	return strings.Contains(out, noSuchDevice) || strings.Contains(out, unknownObject)
}

// checkResolved says why resolvectl cannot be used right now: errNoResolvectl,
// errNoResolved or a timeout; nil when systemd-resolved answers.
//
// "resolvectl dns" lists every link of the host. It fails when the kernel has a
// link that resolved has not heard of yet, as it does for a tunnel created a
// moment ago; it has printed the Global section by then, and that is the proof
// that resolved is there.
func (d *dnsConfigurator) checkResolved() error {
	out, err := d.resolvectl("dns")
	switch {
	case err == nil, hasLine(out, globalHeader):
		return nil
	case errors.Is(err, errNoResolvectl), errors.Is(err, errTimedOut):
		return err
	}
	detail := strings.TrimSpace(out)
	if detail == "" {
		detail = err.Error()
	}
	return fmt.Errorf("%w: %s", errNoResolved, detail)
}

func hasLine(out, line string) bool {
	return slices.ContainsFunc(strings.Split(out, "\n"), func(l string) bool { return strings.TrimSpace(l) == line })
}

// resolvectl runs resolvectl and returns its combined output, which a failure
// carries too.
func (d *dnsConfigurator) resolvectl(args ...string) (string, error) {
	d.log.Debug("running resolvectl", "args", args)
	for _, path := range []string{resolvectlPath, resolvectlAltPath} {
		ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
		out, err := d.run(ctx, path, args, "")
		timedOut := ctx.Err() != nil
		cancel()
		switch {
		case err == nil:
			return string(out), nil
		case errors.Is(err, fs.ErrNotExist):
			continue
		case timedOut:
			return string(out), fmt.Errorf("resolvectl %s: %w after %s", args[0], errTimedOut, d.timeout)
		default:
			return string(out), fmt.Errorf("resolvectl %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
		}
	}
	if d.noResolvconf {
		return "", fmt.Errorf("%w, and there is no resolvconf to set the servers of a full tunnel", errNoResolvectl)
	}
	return "", errNoResolvectl
}

// planLinks validates entries and merges them into one configuration per
// interface. The settings are written to the interface of the entry. Entries of
// one interface cannot stay apart, because resolved keeps one server list and
// one domain list per link: their servers are listed in Order order without
// duplicates, and their domains are one set. The link is the default route when
// any of them matches everything.
func planLinks(entries []osnet.DNSEntry) (map[string]linkConfig, error) {
	order := make([]int, len(entries))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(entries[a].Order, entries[b].Order) })

	links := make(map[string]linkConfig)
	for _, i := range order {
		entry := entries[i]
		if err := validateIface(entry.Iface); err != nil {
			return nil, fmt.Errorf("DNS entry %d: %w", i, err)
		}
		if len(entry.Servers) == 0 {
			return nil, fmt.Errorf("DNS entry %d has no servers", i)
		}
		if len(entry.MatchDomains) == 0 {
			return nil, fmt.Errorf("DNS entry %d has no match domains", i)
		}
		cfg := links[entry.Iface]
		for _, s := range entry.Servers {
			if !osnet.ValidDNSServer(s) {
				return nil, fmt.Errorf("DNS entry %d: invalid DNS server %q", i, s)
			}
			if server := s.Unmap().String(); !slices.Contains(cfg.Servers, server) {
				cfg.Servers = append(cfg.Servers, server)
			}
		}
		for _, dom := range entry.MatchDomains {
			name, err := normalizeDomain(dom)
			if err != nil {
				return nil, fmt.Errorf("DNS entry %d: %w", i, err)
			}
			if name == "." {
				cfg.DefaultRoute = true
			}
			// "~" makes a domain routing-only: queries for it go to the link, but
			// it is not appended to the names the host resolves.
			if route := "~" + name; !slices.Contains(cfg.Domains, route) {
				cfg.Domains = append(cfg.Domains, route)
			}
		}
		links[entry.Iface] = cfg
	}
	return links, nil
}

// normalizeDomain returns d as a lower-case name without a trailing dot, "." for
// every domain. A leading "~" is the routing-only mark resolved prints and is
// not doubled. Domains come from profiles and from servers and reach a command
// line of a root daemon, so anything but letters, digits, hyphens inside a label
// and underscores in dot-separated labels is rejected, IDN labels that are not
// already in punycode included.
func normalizeDomain(d string) (string, error) {
	name := strings.TrimPrefix(d, "~")
	if name == "." {
		return ".", nil
	}
	name = strings.TrimSuffix(name, ".")
	if name == "" || len(name) > 253 {
		return "", fmt.Errorf("invalid DNS domain %q", d)
	}
	for _, label := range strings.Split(name, ".") {
		if !labelPattern.MatchString(label) {
			return "", fmt.Errorf("invalid DNS domain %q", d)
		}
	}
	// Lower-casing comes after the check: it would turn U+212A into a "k".
	return strings.ToLower(name), nil
}

// validateIface accepts what the kernel accepts as an interface name, less what
// resolvectl would not read as a name: an option, or a number, which it takes
// for an interface index.
func validateIface(name string) error {
	if name == "" || len(name) > maxIfaceName || name == "." || name == ".." || name[0] == '-' {
		return fmt.Errorf("invalid interface name %q", name)
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; c <= ' ' || c == 0x7f || c == '/' || c == ':' {
			return fmt.Errorf("invalid interface name %q", name)
		}
	}
	// A superset of what strtol takes for a number: "+1", "0x1f", ...
	if _, err := strconv.ParseInt(name, 0, 64); err == nil {
		return fmt.Errorf("interface name %q is a number, which resolvectl reads as an interface index", name)
	}
	return nil
}

// validateOwner rejects the empty owner and owners that look like a key, so
// that Remove can tell a key from an owner.
func validateOwner(owner string) error {
	if owner == "" || strings.HasPrefix(owner, dnsKeyPrefix) {
		return fmt.Errorf("invalid DNS owner %q", owner)
	}
	return nil
}

// dnsKey is the key of the link iface of owner. An owner is a profile id and can
// hold anything, so it is escaped: ':' never occurs in the escaped owner, nor in
// an interface name, which makes the key unambiguous.
func dnsKey(owner, iface string) string {
	return dnsKeyPrefix + url.QueryEscape(owner) + ":" + iface
}

// ownerOfKey returns the owner a key written by dnsKey belongs to.
func ownerOfKey(key string) (owner string, ok bool) {
	rest, ok := strings.CutPrefix(key, dnsKeyPrefix)
	if !ok {
		return "", false
	}
	escaped, iface, ok := strings.Cut(rest, ":")
	if !ok || validateIface(iface) != nil {
		return "", false
	}
	owner, err := url.QueryUnescape(escaped)
	if err != nil || owner == "" || dnsKey(owner, iface) != key {
		return "", false
	}
	return owner, true
}

// canonicalServers puts what resolvectl printed in the form planLinks writes.
// Anything that is not a plain address (a port, a server name, an interface)
// stays as it is and so differs from what was written.
func canonicalServers(printed []string) []string {
	out := make([]string, len(printed))
	for i, s := range printed {
		out[i] = s
		if a, err := netip.ParseAddr(s); err == nil {
			out[i] = a.Unmap().String()
		}
	}
	return out
}

func canonicalDomains(printed []string) []string {
	out := make([]string, len(printed))
	for i, d := range printed {
		d = strings.ToLower(d)
		if d != "~." {
			d = strings.TrimSuffix(d, ".")
		}
		out[i] = d
	}
	return out
}
