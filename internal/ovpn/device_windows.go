package ovpn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
	"github.com/KoukeNeko/Plaitway/internal/winiface"
)

const (
	// tunnelInterfaceMetric makes the tunnel the preferred interface among
	// routes that tie. It is added to the metric of every route through the
	// interface; the routes themselves are the Reconciler's. It is the value the
	// WireGuard engine uses, so the two kinds of tunnel rank alike.
	tunnelInterfaceMetric = 5

	// minIPv6MTU is the smallest MTU IPv6 works with; Windows refuses less.
	minIPv6MTU = 1280

	// tapctlTimeout bounds one run of tapctl. Making an adapter installs a
	// device, which takes seconds.
	tapctlTimeout = 60 * time.Second
	// adapterReadyTimeout bounds the wait for a new adapter to be known to the
	// IP stack, and adapterUpTimeout the wait for it to come up once openvpn has
	// opened it.
	adapterReadyTimeout  = 20 * time.Second
	adapterReadyInterval = 200 * time.Millisecond
	adapterUpTimeout     = 15 * time.Second
	// releaseTimeout bounds the removal of the adapter at the end.
	releaseTimeout = 30 * time.Second
)

// adapterTool makes and removes the adapters openvpn uses.
type adapterTool interface {
	create(ctx context.Context, name string) error
	remove(ctx context.Context, name string) error
}

// tapctl is OpenVPN's own tool for it. It makes a TAP-Windows6 adapter named
// as asked (the default hardware id of "tapctl create" is that driver's) and
// refuses a name that is taken. It needs administrator rights, which the
// daemon has.
type tapctl struct {
	path string
	log  *slog.Logger
}

func (t tapctl) create(ctx context.Context, name string) error {
	return t.run(ctx, "create", "--name", name)
}

func (t tapctl) remove(ctx context.Context, name string) error {
	return t.run(ctx, "delete", name)
}

func (t tapctl) run(ctx context.Context, args ...string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("tapctl %s: %w", args[0], err)
	}
	release, err := lockTrusted(t.path, "")
	if err != nil {
		return fmt.Errorf("tapctl is not trusted: %w", err)
	}
	defer release()

	// Not exec.CommandContext: tapctl that is stopped halfway through "create"
	// leaves an adapter that has no name yet, which nothing will ever find again.
	// It is let finish, and only a tapctl that hangs is killed.
	cmd := exec.Command(t.path, args...)
	cmd.Env = childEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	if system, err := windows.GetSystemDirectory(); err == nil {
		cmd.Dir = system // a library is never looked for in the daemon's directory
	}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start tapctl: %w", err)
	}
	hung := time.AfterFunc(tapctlTimeout, func() { _ = cmd.Process.Kill() })
	err = cmd.Wait()
	if !hung.Stop() {
		return fmt.Errorf("tapctl %s did not finish within %s", args[0], tapctlTimeout)
	}
	text := strings.TrimSpace(strings.ReplaceAll(output.String(), "\x00", ""))
	if err != nil {
		return fmt.Errorf("tapctl %s: %w: %s", args[0], err, truncate(text, 200))
	}
	t.log.Debug("tapctl", "args", args, "output", text)
	return nil
}

// configurableLink is the part of a winiface.Link the engine uses.
type configurableLink interface {
	SetMTU(mtu uint32, family winiface.Family) error
	SetAddresses(prefixes []netip.Prefix) error
	SetInterfaceMetric(metric uint32) error
	WaitUp(ctx context.Context) error
}

type adapterDeviceConfig struct {
	tool   adapterTool
	prefix string
	owner  tunnel.OwnerID
	log    *slog.Logger

	// What the system answers; tests replace them.
	interfaceNames func() ([]string, error)
	findByName     func(name string) (luid uint64, err error)
	linkOf         func(luid uint64) (configurableLink, error)
	readyTimeout   time.Duration
}

// adapterDevice gives one openvpn an adapter of its own, named after the
// profile, and sets the tunnel's addresses on it. The adapters of the owner's
// other OpenVPN installations are never touched: openvpn is told the name of
// this one, and without it would take the first adapter it finds.
type adapterDevice struct {
	adapterDeviceConfig
	adapter string

	mu       sync.Mutex
	made     bool   // create was started: release removes what it left
	luid     uint64 // known once the adapter is
	released bool
}

func newAdapterDevice(cfg adapterDeviceConfig) *adapterDevice {
	if cfg.interfaceNames == nil {
		cfg.interfaceNames = systemInterfaceNames
	}
	if cfg.findByName == nil {
		cfg.findByName = func(name string) (uint64, error) {
			link, err := winiface.FindByName(name)
			return link.LUID, err
		}
	}
	if cfg.linkOf == nil {
		cfg.linkOf = func(luid uint64) (configurableLink, error) { return winiface.FindByLUID(luid) }
	}
	if cfg.readyTimeout == 0 {
		cfg.readyTimeout = adapterReadyTimeout
	}
	return &adapterDevice{adapterDeviceConfig: cfg, adapter: adapterName(cfg.prefix, cfg.owner)}
}

func systemInterfaceNames() ([]string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	names := make([]string, len(interfaces))
	for i, iface := range interfaces {
		names[i] = iface.Name
	}
	return names, nil
}

func (d *adapterDevice) options() []string { return []string{"--dev-node", d.adapter} }

func (d *adapterDevice) name() string { return d.adapter }

// sweptPrefixes remembers the prefixes whose leftovers were removed in this
// process.
var sweptPrefixes sync.Map // prefix -> *sync.Once

func (d *adapterDevice) acquire(ctx context.Context) error {
	once, _ := sweptPrefixes.LoadOrStore(d.prefix, new(sync.Once))
	once.(*sync.Once).Do(func() { d.removeLeftovers(ctx) })

	if err := d.removeIfPresent(ctx, d.adapter); err != nil {
		return fmt.Errorf("remove the adapter a previous run left: %w", err)
	}
	d.mu.Lock()
	d.made = true
	d.mu.Unlock()
	if err := d.tool.create(ctx, d.adapter); err != nil {
		return fmt.Errorf("create the adapter %s: %w", d.adapter, err)
	}
	luid, err := d.waitForAdapter(ctx)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.luid = luid
	d.mu.Unlock()
	return nil
}

// waitForAdapter waits until the IP stack knows the new adapter by its name.
func (d *adapterDevice) waitForAdapter(ctx context.Context) (uint64, error) {
	deadline := time.Now().Add(d.readyTimeout)
	for {
		luid, err := d.findByName(d.adapter)
		if err == nil {
			return luid, nil
		}
		if !errors.Is(err, winiface.ErrNotFound) {
			return 0, fmt.Errorf("find the adapter %s: %w", d.adapter, err)
		}
		if !time.Now().Before(deadline) {
			return 0, fmt.Errorf("the adapter %s did not appear within %s", d.adapter, d.readyTimeout)
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(adapterReadyInterval):
		}
	}
}

// removeLeftovers removes the adapters of this engine kind that a daemon that
// crashed left behind: Windows keeps a device after the process that made it is
// gone. It runs once per process, before the first adapter is made, so it never
// meets one in use.
func (d *adapterDevice) removeLeftovers(ctx context.Context) {
	names, err := d.interfaceNames()
	if err != nil {
		d.log.Warn("cannot list the network interfaces to look for leftover adapters", "err", err)
		return
	}
	for _, name := range leftoverAdapters(d.prefix, names) {
		d.log.Warn("removing an adapter a previous run left", "adapter", name)
		if err := d.tool.remove(ctx, name); err != nil {
			d.log.Warn("cannot remove the leftover adapter", "adapter", name, "err", err)
		}
	}
}

// leftoverAdapters picks the names that an engine with prefix gave: the prefix
// and the eight hex digits of adapterName, nothing else. An adapter of another
// program, or the owner's own OpenVPN adapters, never match.
func leftoverAdapters(prefix string, names []string) []string {
	own := regexp.MustCompile(`(?i)^` + regexp.QuoteMeta(prefix) + `[0-9a-f]{8}$`)
	var out []string
	for _, name := range names {
		if own.MatchString(name) {
			out = append(out, name)
		}
	}
	return out
}

func (d *adapterDevice) removeIfPresent(ctx context.Context, name string) error {
	names, err := d.interfaceNames()
	if err != nil {
		return fmt.Errorf("list the network interfaces: %w", err)
	}
	if !slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(n, name) }) {
		return nil
	}
	return d.tool.remove(ctx, name)
}

func (d *adapterDevice) configure(ctx context.Context, up upInfo) error {
	d.mu.Lock()
	luid := d.luid
	d.mu.Unlock()
	if luid == 0 {
		return errors.New("the adapter is not known")
	}
	addresses := adapterAddresses(up)
	if len(addresses) == 0 {
		return errors.New("openvpn reported no tunnel address")
	}
	link, err := d.linkOf(luid)
	if err != nil {
		return fmt.Errorf("find the adapter %s: %w", d.adapter, err)
	}
	if err := setMTUs(link, addresses, up.MTU); err != nil {
		return err
	}
	if err := link.SetAddresses(addresses); err != nil {
		return err
	}
	if err := link.SetInterfaceMetric(tunnelInterfaceMetric); err != nil {
		return err
	}
	upCtx, cancel := context.WithTimeout(ctx, adapterUpTimeout)
	defer cancel()
	return link.WaitUp(upCtx)
}

// adapterAddresses are the addresses to set: openvpn's, with the IPv4 one given
// the subnet its peer is in when openvpn named a peer instead of a netmask.
func adapterAddresses(up upInfo) []netip.Prefix {
	out := make([]netip.Prefix, len(up.Addresses))
	for i, prefix := range up.Addresses {
		out[i] = prefix
		if prefix.Addr().Is4() {
			out[i] = onLinkPrefix(prefix, up.Peer)
		}
	}
	return out
}

// setMTUs sets the MTU of each IP family the tunnel has an address in. An MTU
// openvpn did not report is left to the adapter; IPv6 does not work below
// minIPv6MTU, so a smaller MTU leaves the IPv6 interface alone.
func setMTUs(link configurableLink, addrs []netip.Prefix, mtu int) error {
	if mtu <= 0 {
		return nil
	}
	for _, family := range mtuFamilies(addrs, mtu) {
		if err := link.SetMTU(uint32(mtu), family); err != nil {
			return fmt.Errorf("set the MTU: %w", err)
		}
	}
	return nil
}

func mtuFamilies(addrs []netip.Prefix, mtu int) []winiface.Family {
	var families []winiface.Family
	if slices.ContainsFunc(addrs, func(p netip.Prefix) bool { return p.Addr().Is4() }) {
		families = append(families, winiface.IPv4)
	}
	if mtu >= minIPv6MTU && slices.ContainsFunc(addrs, func(p netip.Prefix) bool { return p.Addr().Is6() }) {
		families = append(families, winiface.IPv6)
	}
	return families
}

// release removes the adapter, after openvpn has closed it.
func (d *adapterDevice) release() {
	d.mu.Lock()
	if !d.made || d.released {
		d.mu.Unlock()
		return
	}
	d.released = true
	d.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
	defer cancel()
	if err := d.removeIfPresent(ctx, d.adapter); err != nil {
		d.log.Warn("cannot remove the adapter; the next start of the daemon will", "adapter", d.adapter, "err", err)
	}
}
