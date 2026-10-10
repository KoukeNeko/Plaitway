package linux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// DNSBackend names the program that writes the DNS settings of the tunnels.
type DNSBackend string

const (
	// DNSResolved is systemd-resolved, driven through resolvectl: settings for
	// each link, and for some domains only.
	DNSResolved DNSBackend = "systemd-resolved"
	// DNSResolvconf is openresolv's resolvconf, for a host without
	// systemd-resolved: the servers of a full tunnel only.
	DNSResolvconf DNSBackend = "resolvconf"
	// DNSNone is a host with neither: no DNS setting can be written.
	DNSNone DNSBackend = "none"
)

// DetectDNSBackend says which backend this host has. It is systemd-resolved when
// resolvectl is installed, resolvconf when that is not and a resolvconf is, and
// none otherwise. exists reports whether a file is there.
func DetectDNSBackend(exists func(path string) bool) DNSBackend {
	if exists(resolvectlPath) || exists(resolvectlAltPath) {
		return DNSResolved
	}
	for _, path := range resolvconfPaths {
		if exists(path) {
			return DNSResolvconf
		}
	}
	return DNSNone
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// NewDNS returns the DNS configurator of this host, which is the one DetectDNSBackend
// names. On a host with neither it is the one of systemd-resolved, whose errors
// say that resolvectl is missing. Writing needs root.
func NewDNS(opts DNSOptions) osnet.DNSConfigurator {
	if opts.Run == nil {
		opts.Run = runDNSCommand
	}
	switch DetectDNSBackend(fileExists) {
	case DNSResolvconf:
		return newResolvconfConfigurator(opts)
	case DNSNone:
		d := newDNSConfigurator(opts)
		d.noResolvconf = true
		return d
	}
	return newDNSConfigurator(opts)
}

// CheckDNS says which backend this host uses and why it cannot write DNS
// settings right now, nil when it can. It only reads and needs no privilege:
// it asks the question the first Apply asks, so that a daemon can say so at
// start rather than when the first tunnel comes up.
func CheckDNS(opts DNSOptions) (DNSBackend, error) {
	if opts.Run == nil {
		opts.Run = runDNSCommand
	}
	switch backend := DetectDNSBackend(fileExists); backend {
	case DNSResolvconf:
		r := newResolvconfConfigurator(opts)
		r.mu.Lock()
		defer r.mu.Unlock()
		return backend, r.check()
	case DNSNone:
		return backend, errors.New("neither resolvectl nor resolvconf is installed")
	default:
		return backend, CheckResolved(opts)
	}
}

// runDNSCommand runs a system tool with a fixed, minimal environment: the
// daemon is root and the tool needs nothing from it, and resolvectl would
// follow DBUS_SYSTEM_BUS_ADDRESS to wherever it points. LC_ALL=C keeps the
// error texts the adapters look for in English. resolvconf is a script that
// runs tools from /sbin.
func runDNSCommand(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"LC_ALL=C", "PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	cmd.Stdin = strings.NewReader(stdin)
	return cmd.CombinedOutput()
}
