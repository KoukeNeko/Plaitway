package windows

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	win "golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

const (
	// policyConfigPath is where the DNS Client keeps the NRPT rules of the local
	// machine, one subkey per rule; Add-DnsClientNrptRule writes here.
	policyConfigPath = `SYSTEM\CurrentControlSet\Services\Dnscache\Parameters\DnsPolicyConfig`
	// groupPolicyConfigPath is where a group policy or DirectAccess delivers its
	// rules, in the same layout. When it holds any, Windows applies those and
	// ignores the local table.
	groupPolicyConfigPath = `SOFTWARE\Policies\Microsoft\Windows NT\DNSClient\DnsPolicyConfig`

	valueNames      = "Name"
	valueServers    = "GenericDNSServers"
	valueOptions    = "ConfigOptions"
	valueVersion    = "Version"
	valueComment    = "Comment"
	valueDisplay    = "DisplayName"
	ruleVersion     = 2 // the version Add-DnsClientNrptRule writes
	ruleDisplayName = "Plaitway"
	// optionGenericServers says that GenericDNSServers holds the servers to ask.
	optionGenericServers = 0x8

	// refreshPolicyForce is RP_FORCE: apply the policy although nothing changed.
	refreshPolicyForce = 1
)

var (
	userenv = win.NewLazySystemDLL("userenv.dll")
	dnsapi  = win.NewLazySystemDLL("dnsapi.dll")

	procRefreshPolicyEx       = userenv.NewProc("RefreshPolicyEx")
	procDnsFlushResolverCache = dnsapi.NewProc("DnsFlushResolverCache")
)

// DNSOptions configures NewDNS.
type DNSOptions struct {
	// Logger receives the configurator's diagnostics; nil uses slog.Default().
	Logger *slog.Logger
}

// NewDNS returns the DNS configurator of this PC. Writing needs an elevated
// process or the LocalSystem account.
func NewDNS(opts DNSOptions) osnet.DNSConfigurator {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return newDNSConfigurator(&registryStore{root: registry.LOCAL_MACHINE, path: policyConfigPath}, log, systemCalls{
		refresh:          refreshPolicy,
		flush:            flushResolverCache,
		groupPolicyRules: groupPolicyRules,
	})
}

// SweepDNS is SweepOwnedDNS on this PC.
func SweepDNS(log *slog.Logger) (SweepResult, error) {
	return SweepOwnedDNS(NewDNS(DNSOptions{Logger: log}))
}

// groupPolicyRules lists the rules that a group policy delivers.
func groupPolicyRules() ([]string, error) {
	return (&registryStore{root: registry.LOCAL_MACHINE, path: groupPolicyConfigPath}).ruleKeys()
}

// refreshPolicy asks Windows to apply the machine policy again, which is what
// makes the DNS Client reread the NRPT rules that were written to the registry.
func refreshPolicy() error {
	machine := uintptr(1)
	if ok, _, err := procRefreshPolicyEx.Call(machine, refreshPolicyForce); ok == 0 {
		return fmt.Errorf("RefreshPolicyEx: %w", err)
	}
	return nil
}

// flushResolverCache empties the cache of the DNS Client, as ipconfig
// /flushdns does.
func flushResolverCache() error {
	if ok, _, err := procDnsFlushResolverCache.Call(); ok == 0 {
		return fmt.Errorf("DnsFlushResolverCache: %w", err)
	}
	return nil
}

// registryStore keeps the rules as subkeys of path under root. Production uses
// the DNS Client's key in HKEY_LOCAL_MACHINE; tests use a scratch key.
type registryStore struct {
	root registry.Key
	path string
}

func (s *registryStore) ruleKeys() ([]string, error) {
	parent, err := registry.OpenKey(s.root, s.path, registry.ENUMERATE_SUB_KEYS)
	if errors.Is(err, registry.ErrNotExist) {
		return nil, nil // no rule has ever been written
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", s.path, err)
	}
	defer parent.Close()
	names, err := parent.ReadSubKeyNames(-1)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", s.path, err)
	}
	return names, nil
}

func (s *registryStore) read(key string) (rule, error) {
	k, err := registry.OpenKey(s.root, s.path+`\`+key, registry.QUERY_VALUE)
	if err != nil {
		return rule{}, fmt.Errorf("open rule %s: %w", key, err)
	}
	defer k.Close()
	var r rule
	if r.Names, _, err = k.GetStringsValue(valueNames); err != nil {
		return rule{}, fmt.Errorf("read %s of rule %s: %w", valueNames, key, err)
	}
	servers, _, err := k.GetStringValue(valueServers)
	if err != nil {
		return rule{}, fmt.Errorf("read %s of rule %s: %w", valueServers, key, err)
	}
	for _, server := range strings.Split(servers, serverSeparator) {
		if server = strings.TrimSpace(server); server != "" {
			r.Servers = append(r.Servers, server)
		}
	}
	// A rule somebody else wrote may have no comment.
	if r.Comment, _, err = k.GetStringValue(valueComment); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return rule{}, fmt.Errorf("read %s of rule %s: %w", valueComment, key, err)
	}
	return r, nil
}

// write stores the rule. Name goes last, because a rule without a namespace is
// not one: the DNS Client may reread its rules at any moment and must not find
// a half-written rule that already applies.
func (s *registryStore) write(key string, r rule) error {
	k, _, err := registry.CreateKey(s.root, s.path+`\`+key, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("create rule %s: %w", key, err)
	}
	defer k.Close()
	if err := k.DeleteValue(valueNames); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("retire the old %s of rule %s: %w", valueNames, key, err)
	}
	steps := []struct {
		name string
		set  func() error
	}{
		{valueVersion, func() error { return k.SetDWordValue(valueVersion, ruleVersion) }},
		{valueOptions, func() error { return k.SetDWordValue(valueOptions, optionGenericServers) }},
		{valueServers, func() error { return k.SetStringValue(valueServers, strings.Join(r.Servers, serverSeparator)) }},
		{valueDisplay, func() error { return k.SetStringValue(valueDisplay, ruleDisplayName) }},
		{valueComment, func() error { return k.SetStringValue(valueComment, r.Comment) }},
		{valueNames, func() error { return k.SetStringsValue(valueNames, r.Names) }},
	}
	for _, step := range steps {
		if err := step.set(); err != nil {
			return fmt.Errorf("write %s of rule %s: %w", step.name, key, err)
		}
	}
	return nil
}

func (s *registryStore) remove(key string) error {
	parent, err := registry.OpenKey(s.root, s.path, registry.ENUMERATE_SUB_KEYS)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", s.path, err)
	}
	defer parent.Close()
	if err := registry.DeleteKey(parent, key); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("delete rule %s: %w", key, err)
	}
	return nil
}

// ManualSweepCommand is the PowerShell command that removes every rule this
// package wrote, for the person who finds a rule left behind when nothing of
// Plaitway is left to remove it. It is what SweepDNS does.
func ManualSweepCommand() string {
	return fmt.Sprintf(`Get-ChildItem 'HKLM:\%s' | Where-Object PSChildName -like '%s*' | Remove-Item; Clear-DnsClientCache`, policyConfigPath, ruleKeyPrefix)
}
