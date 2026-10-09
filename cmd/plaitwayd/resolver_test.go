package main

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resolverHost is /etc and /run/systemd/resolve in a temporary directory, with
// the files systemd-resolved writes.
type resolverHost struct {
	resolvConf, resolveDir string
}

func newResolverHost(t *testing.T) resolverHost {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := resolverHost{resolvConf: filepath.Join(dir, "etc", "resolv.conf"), resolveDir: filepath.Join(dir, "run", "systemd", "resolve")}
	for _, d := range []string{filepath.Dir(h.resolvConf), h.resolveDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h.write(t, filepath.Join(h.resolveDir, "stub-resolv.conf"), "# systemd-resolved\nnameserver 127.0.0.53\noptions edns0 trust-ad\nsearch .\n")
	h.write(t, filepath.Join(h.resolveDir, "resolv.conf"), "nameserver 192.168.1.1\n")
	return h
}

func (resolverHost) write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (h resolverHost) link(t *testing.T, target string) {
	t.Helper()
	if err := os.Symlink(target, h.resolvConf); err != nil {
		t.Fatal(err)
	}
}

func TestResolvConfThatAsksSystemdResolved(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, h resolverHost)
	}{
		{"link to the stub file", func(t *testing.T, h resolverHost) { h.link(t, filepath.Join(h.resolveDir, "stub-resolv.conf")) }},
		{"relative link to the stub file, as distributions make it", func(t *testing.T, h resolverHost) { h.link(t, "../run/systemd/resolve/stub-resolv.conf") }},
		{"link to the file that lists the servers", func(t *testing.T, h resolverHost) { h.link(t, filepath.Join(h.resolveDir, "resolv.conf")) }},
		{"link to a link to the stub file", func(t *testing.T, h resolverHost) {
			middle := filepath.Join(filepath.Dir(h.resolvConf), "middle")
			if err := os.Symlink(filepath.Join(h.resolveDir, "stub-resolv.conf"), middle); err != nil {
				t.Fatal(err)
			}
			h.link(t, middle)
		}},
		{"copy of the stub file", func(t *testing.T, h resolverHost) {
			h.write(t, h.resolvConf, "# written by a script\nnameserver 127.0.0.53\n")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newResolverHost(t)
			tt.setup(t, h)
			if problem := resolvConfProblem(h.resolvConf, h.resolveDir); problem != "" {
				t.Fatalf("problem %q for a resolv.conf that asks systemd-resolved", problem)
			}
		})
	}
}

func TestResolvConfThatDoesNotAskSystemdResolved(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, h resolverHost)
		want  string
	}{
		{"file with other servers", func(t *testing.T, h resolverHost) {
			h.write(t, h.resolvConf, "nameserver 192.168.1.1\nnameserver 8.8.8.8\n")
		}, "is not a file of systemd-resolved"},
		{"empty file", func(t *testing.T, h resolverHost) { h.write(t, h.resolvConf, "") }, "is not a file of systemd-resolved"},
		{"the stub address in a comment", func(t *testing.T, h resolverHost) {
			h.write(t, h.resolvConf, "# nameserver 127.0.0.53\nnameserver 10.0.0.1\n")
		}, "is not a file of systemd-resolved"},
		{"link to the file of another program", func(t *testing.T, h resolverHost) {
			other := filepath.Join(filepath.Dir(h.resolvConf), "resolv.conf.dnsmasq")
			h.write(t, other, "nameserver 127.0.0.1\n")
			h.link(t, other)
		}, "resolv.conf.dnsmasq"},
		{"file of resolved's name in another directory", func(t *testing.T, h resolverHost) {
			elsewhere := filepath.Join(filepath.Dir(h.resolvConf), "stub-resolv.conf")
			h.write(t, elsewhere, "nameserver 10.0.0.1\n")
			h.link(t, elsewhere)
		}, "is not a file of systemd-resolved"},
		{"link to a file of resolved that does not exist", func(t *testing.T, h resolverHost) { h.link(t, filepath.Join(h.resolveDir, "no-such-file")) }, "cannot be read"},
		{"no file", func(*testing.T, resolverHost) {}, "cannot be read"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newResolverHost(t)
			tt.setup(t, h)
			if problem := resolvConfProblem(h.resolvConf, h.resolveDir); !strings.Contains(problem, tt.want) {
				t.Fatalf("problem %q, want it to say %q", problem, tt.want)
			}
		})
	}
}

// The path itself may be a file of resolved: the stub file of a host where
// /etc/resolv.conf is a bind mount of it.
func TestResolvConfThatIsAFileOfSystemdResolved(t *testing.T) {
	h := newResolverHost(t)
	for _, name := range resolvedFiles {
		if problem := resolvConfProblem(filepath.Join(h.resolveDir, name), h.resolveDir); problem != "" {
			t.Errorf("%s: problem %q", name, problem)
		}
	}
}

// Resolved's directory is not there when it never ran: a resolv.conf that is a
// copy of the stub still names the stub.
func TestResolvConfThatNamesTheStubWhenResolvedHasNoDirectory(t *testing.T) {
	h := newResolverHost(t)
	h.write(t, h.resolvConf, "nameserver 127.0.0.53\n")
	if problem := resolvConfProblem(h.resolvConf, filepath.Join(h.resolveDir, "gone")); problem != "" {
		t.Fatalf("problem %q", problem)
	}
}

func logTo(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestWarnAboutResolverIsSilentWhenDNSCanBeApplied(t *testing.T) {
	h := newResolverHost(t)
	h.link(t, filepath.Join(h.resolveDir, "stub-resolv.conf"))
	var out bytes.Buffer
	warnAboutResolver(logTo(&out), h.resolvConf, h.resolveDir, func() error { return nil })
	if out.Len() != 0 {
		t.Fatalf("logged %q", out.String())
	}
}

// The two conditions are independent: one warning each, and never more.
func TestWarnAboutResolverWarnsOncePerProblem(t *testing.T) {
	h := newResolverHost(t)
	h.write(t, h.resolvConf, "nameserver 192.168.1.1\n")
	down := errors.New("systemd-resolved is not running")

	var both bytes.Buffer
	warnAboutResolver(logTo(&both), h.resolvConf, h.resolveDir, func() error { return down })
	if lines := strings.Count(both.String(), "\n"); lines != 2 {
		t.Fatalf("%d lines, want 2:\n%s", lines, both.String())
	}
	for _, want := range []string{"level=WARN", "DNS settings of tunnels cannot be applied", "systemd-resolved is not running", "may have no effect on programs", h.resolvConf, "reason="} {
		if !strings.Contains(both.String(), want) {
			t.Errorf("the log lacks %q:\n%s", want, both.String())
		}
	}

	var onlyDown bytes.Buffer
	h.write(t, h.resolvConf, "nameserver 127.0.0.53\n")
	warnAboutResolver(logTo(&onlyDown), h.resolvConf, h.resolveDir, func() error { return down })
	if strings.Count(onlyDown.String(), "\n") != 1 || !strings.Contains(onlyDown.String(), "cannot be applied") {
		t.Errorf("resolved down, resolv.conf fine:\n%s", onlyDown.String())
	}

	var onlyFile bytes.Buffer
	h.write(t, h.resolvConf, "nameserver 192.168.1.1\n")
	warnAboutResolver(logTo(&onlyFile), h.resolvConf, h.resolveDir, func() error { return nil })
	if strings.Count(onlyFile.String(), "\n") != 1 || !strings.Contains(onlyFile.String(), "may have no effect") {
		t.Errorf("resolved fine, resolv.conf elsewhere:\n%s", onlyFile.String())
	}
}
