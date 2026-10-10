package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/osnet/linux"
)

func checkReturns(backend linux.DNSBackend, err error) func() (linux.DNSBackend, error) {
	return func() (linux.DNSBackend, error) { return backend, err }
}

func TestWarnAboutDNSOfResolvedIsAsBefore(t *testing.T) {
	h := newResolverHost(t)
	h.link(t, filepath.Join(h.resolveDir, "stub-resolv.conf"))
	var quiet bytes.Buffer
	warnAboutDNS(logTo(&quiet), h.resolvConf, h.resolveDir, "", checkReturns(linux.DNSResolved, nil))
	if quiet.Len() != 0 {
		t.Errorf("resolved that answers logged %q", quiet.String())
	}
	var down bytes.Buffer
	warnAboutDNS(logTo(&down), h.resolvConf, h.resolveDir, "", checkReturns(linux.DNSResolved, errors.New("systemd-resolved is not running")))
	if !strings.Contains(down.String(), "cannot be applied") || !strings.Contains(down.String(), "systemd-resolved is not running") {
		t.Errorf("resolved that is down logged %q", down.String())
	}
}

func TestWarnAboutDNSSaysWhatResolvconfCanDo(t *testing.T) {
	var out bytes.Buffer
	warnAboutDNS(logTo(&out), "", "", filepath.Join(t.TempDir(), "resolvconf.conf"), checkReturns(linux.DNSResolvconf, nil))
	if strings.Count(out.String(), "\n") != 1 || !strings.Contains(out.String(), "level=INFO") ||
		!strings.Contains(out.String(), "full tunnel") || strings.Contains(out.String(), "level=WARN") {
		t.Errorf("a resolvconf that works logged:\n%s", out.String())
	}

	var broken bytes.Buffer
	warnAboutDNS(logTo(&broken), "", "", "", checkReturns(linux.DNSResolvconf, errors.New("resolvconf is not openresolv")))
	if strings.Count(broken.String(), "\n") != 2 || !strings.Contains(broken.String(), "level=WARN") || !strings.Contains(broken.String(), "not openresolv") {
		t.Errorf("a resolvconf that is not openresolv logged:\n%s", broken.String())
	}
}

func TestWarnAboutDNSWarnsWhenResolvconfIsTurnedOff(t *testing.T) {
	conf := filepath.Join(t.TempDir(), "resolvconf.conf")
	newResolverHost(t).write(t, conf, "# resolv_conf=/etc/resolv.conf\nresolvconf=NO\n")
	var out bytes.Buffer
	warnAboutDNS(logTo(&out), "", "", conf, checkReturns(linux.DNSResolvconf, nil))
	if !strings.Contains(out.String(), "level=WARN") || !strings.Contains(out.String(), "resolvconf is disabled") || !strings.Contains(out.String(), conf) {
		t.Errorf("a resolvconf that is off logged:\n%s", out.String())
	}
}

func TestWarnAboutDNSWithNeitherSaysWhatToInstall(t *testing.T) {
	var out bytes.Buffer
	warnAboutDNS(logTo(&out), "", "", "", checkReturns(linux.DNSNone, errors.New("neither resolvectl nor resolvconf is installed")))
	for _, want := range []string{"level=WARN", "cannot be applied", "neither resolvectl nor resolvconf", "openresolv"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the log lacks %q:\n%s", want, out.String())
		}
	}
}

func TestResolvconfDisabled(t *testing.T) {
	dir := t.TempDir()
	h := newResolverHost(t)
	tests := []struct {
		name, content string
		want          bool
	}{
		{"off", "resolvconf=NO\n", true},
		{"off in lower case and quotes", "resolvconf=\"no\"\n", true},
		{"off with spaces and other lines", "# a comment\nresolv_conf=/etc/resolv.conf\n  resolvconf = NO  \n", true},
		{"on", "resolvconf=YES\n", false},
		{"commented out", "# resolvconf=NO\n", false},
		{"another variable", "resolv_conf=/etc/resolv.conf\nname_servers=127.0.0.1\n", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		path := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "_"))
		h.write(t, path, tt.content)
		if got := resolvconfDisabled(path); got != tt.want {
			t.Errorf("%s: resolvconfDisabled = %t, want %t", tt.name, got, tt.want)
		}
	}
	if resolvconfDisabled(filepath.Join(dir, "missing")) {
		t.Error("a file that is not there turns resolvconf off")
	}
}
