package linux

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRunDNSCommandEnvironment(t *testing.T) {
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path=/tmp/elsewhere")
	t.Setenv("SYSTEMD_LOG_LEVEL", "debug")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := runDNSCommand(ctx, "/usr/bin/env", nil, "")
	if err != nil {
		t.Skipf("/usr/bin/env: %v", err)
	}
	got := strings.Fields(string(out))
	slices.Sort(got)
	if want := []string{"LC_ALL=C", "PATH=/usr/bin:/bin"}; !slices.Equal(got, want) {
		t.Errorf("environment = %q, want %q", got, want)
	}
}

// The adapter tries /bin/resolvectl when /usr/bin/resolvectl does not exist,
// and takes that for a missing program by this error.
func TestRunDNSCommandMissingProgram(t *testing.T) {
	_, err := runDNSCommand(context.Background(), "/nonexistent/resolvectl", []string{"dns"}, "")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want one that is fs.ErrNotExist", err)
	}
}

// TestLiveResolvedReadOnly reads this machine's systemd-resolved with the verbs
// that only read, so that the parsing is checked against the installed
// resolvectl and not only against the fixtures. It writes nothing.
func TestLiveResolvedReadOnly(t *testing.T) {
	d := NewDNS(DNSOptions{Logger: dnsTestLog()}).(*dnsConfigurator)
	if err := d.checkResolved(); err != nil {
		t.Skipf("no systemd-resolved to read: %v", err)
	}
	// Not checkResolved's verdict: the list is read again for its lines.
	out, _ := d.resolvectl("dns")
	read := 0
	for _, line := range strings.Split(out, "\n") {
		m := linkLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil || validateIface(m[1]) != nil {
			continue
		}
		cfg, err := d.readLink(m[1])
		if errors.Is(err, errLinkGone) {
			continue // gone since the list was made
		}
		if err != nil {
			t.Errorf("readLink(%s): %v", m[1], err)
			continue
		}
		read++
		t.Logf("%s: %v", m[1], cfg)
	}
	if read == 0 {
		t.Skip("resolved lists no link")
	}
}
