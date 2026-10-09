package linux

import (
	"errors"
	"strings"
	"testing"
)

func TestCheckResolvedSaysWhyDNSCannotBeWritten(t *testing.T) {
	running := newFakeResolved("tun0")
	if err := CheckResolved(DNSOptions{Run: running.run, Logger: dnsTestLog()}); err != nil {
		t.Errorf("resolved is running: %v", err)
	}

	down := newFakeResolved("tun0")
	down.down = true
	err := CheckResolved(DNSOptions{Run: down.run, Logger: dnsTestLog()})
	if !errors.Is(err, errNoResolved) || !strings.Contains(err.Error(), "systemd-resolved is not running") {
		t.Errorf("resolved is down: %v, want systemd-resolved is not running", err)
	}

	missing := newFakeResolved()
	clear(missing.programs)
	if err := CheckResolved(DNSOptions{Run: missing.run, Logger: dnsTestLog()}); !errors.Is(err, errNoResolvectl) {
		t.Errorf("resolvectl is missing: %v, want resolvectl not found", err)
	}
}
