package main

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	// resolvConfPath is the file the C library reads the name servers from.
	resolvConfPath = "/etc/resolv.conf"
	// resolvedDir is where systemd-resolved keeps the files it offers for
	// resolvConfPath: stub-resolv.conf, which points the programs at its stub
	// listener, and resolv.conf, which lists the servers it uses.
	resolvedDir = "/run/systemd/resolve"
	// resolvedStubAddress is the address of resolved's stub listener, which
	// resolv.conf names when it is a copy of the stub file instead of a link.
	resolvedStubAddress = "127.0.0.53"
)

// resolvedFiles are the two files of resolvedDir that resolvConfPath may be or
// point at.
var resolvedFiles = []string{"stub-resolv.conf", "resolv.conf"}

// warnAboutResolver logs, once, what would keep the DNS settings of a tunnel
// from working: systemd-resolved cannot be reached, or the programs of the host
// do not ask it. It is informational and changes nothing; the DNS adapter
// reports the same failure on the profile when a tunnel needs it.
func warnAboutResolver(log *slog.Logger, resolvConf, resolvedDir string, checkResolved func() error) {
	if err := checkResolved(); err != nil {
		log.Warn("DNS settings of tunnels cannot be applied", "err", err)
	}
	if problem := resolvConfProblem(resolvConf, resolvedDir); problem != "" {
		log.Warn("DNS settings of tunnels may have no effect on programs: resolv.conf does not ask systemd-resolved",
			"path", resolvConf, "reason", problem)
	}
}

// resolvConfProblem says why the programs of the host do not ask
// systemd-resolved, or returns "" when they do: resolvConf is, or links to, one
// of its files, or is a copy of its stub file.
func resolvConfProblem(resolvConf, resolvedDir string) string {
	target, err := filepath.EvalSymlinks(resolvConf)
	if err != nil {
		return "cannot be read: " + err.Error()
	}
	if dir, err := filepath.EvalSymlinks(resolvedDir); err == nil && filepath.Dir(target) == dir &&
		slices.Contains(resolvedFiles, filepath.Base(target)) {
		return ""
	}
	if namesStub(target) {
		return ""
	}
	return fmt.Sprintf("it resolves to %s, which is not a file of systemd-resolved", target)
}

// namesStub reports whether the resolv.conf at path has resolved's stub as a
// name server.
func namesStub(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if fields := strings.Fields(scanner.Text()); len(fields) >= 2 && fields[0] == "nameserver" && fields[1] == resolvedStubAddress {
			return true
		}
	}
	return false
}
