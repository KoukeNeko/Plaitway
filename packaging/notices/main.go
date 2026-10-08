// Command notices generates THIRD_PARTY_NOTICES.md from real data: the pinned
// OpenVPN source tarballs (packaging/openvpn-deps.env), the Go modules linked
// into plaitwayd and plaitway, and the Swift packages in macos/Package.resolved.
// License and notice texts are copied verbatim from the upstream sources, never
// retyped. The source offer it writes is the one for the copy of the source
// that package-app.sh puts into Contents/Resources/Source.
//
// Run from the repository root after build-openvpn.sh and `swift build`:
//
//	go run ./packaging/notices -o packaging/THIRD_PARTY_NOTICES.md
//
// With -platform linux it lists the Go modules linked into the Linux build and
// nothing else: the Linux package bundles no OpenVPN and has no Swift code.
// With -windows it writes the notices of the Windows installer instead, from
// the Go modules, the NuGet packages of the app and the .NET runtime of its
// publish folder (windows.go).
package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// document is a license or notice text copied verbatim from upstream.
type document struct {
	kind string // a license id such as "MIT", or "Notice"
	body string
}

// component is one piece of third-party software and the texts shipped with it.
type component struct {
	name, version, license, source string
	docs                           []document
	// archive and sha256 name the source archive of a bundled program, and
	// shipped says that the archive is in Contents/Resources/Source.
	archive, sha256 string
	shipped         bool
}

// sourceScripts are the build scripts that ship in Contents/Resources/Source
// next to the archives (package-app.sh copies them).
var sourceScripts = []string{"build-openvpn.sh", "lib.sh", "openvpn-deps.env"}

func main() {
	root := flag.String("root", ".", "repository root")
	sources := flag.String("sources", "", "directory with the OpenVPN source tarballs (default: $PLAITWAY_OPENVPN_WORK/src, else build/openvpn-work/src)")
	checkouts := flag.String("checkouts", "", "SwiftPM checkouts directory (default: macos/.build/checkouts)")
	contact := flag.String("contact", os.Getenv("PLAITWAY_SOURCE_CONTACT"),
		"where the written source offer says to ask (default: $PLAITWAY_SOURCE_CONTACT, else the https URL of the go.mod module)")
	platform := flag.String("platform", "macos", "macos, or linux for the Go modules of the Linux build only")
	outPath := flag.String("o", "", "output file (default: stdout)")
	windowsMode := flag.Bool("windows", false, "write the notices of the Windows installer instead (see windows.go)")
	windows := windowsFlags()
	flag.Parse()

	if *windowsMode {
		runWindows(*root, *outPath, *windows)
		return
	}
	if *checkouts == "" {
		*checkouts = filepath.Join(*root, "macos", ".build", "checkouts")
	}

	if *sources == "" {
		work := os.Getenv("PLAITWAY_OPENVPN_WORK")
		if work == "" {
			work = filepath.Join(*root, "build", "openvpn-work")
		}
		*sources = filepath.Join(work, "src")
	}
	doc, err := generate(*root, *sources, *checkouts, *contact, *platform)
	if err != nil {
		fmt.Fprintln(os.Stderr, "notices:", err)
		os.Exit(1)
	}
	emit(*outPath, doc)
}

// emit writes the finished document to the file, or to stdout without one.
func emit(outPath, doc string) {
	if outPath == "" {
		fmt.Print(doc)
		return
	}
	if err := os.WriteFile(outPath, []byte(doc), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "notices:", err)
		os.Exit(1)
	}
}

func generate(root, sources, checkouts, contact, platform string) (string, error) {
	switch platform {
	case "linux":
		goMods, err := goComponents(root, "linux", "amd64")
		if err != nil {
			return "", err
		}
		return render(nil, goMods, nil, contact), nil
	case "macos":
	default:
		return "", fmt.Errorf("unknown platform %q: macos or linux", platform)
	}
	bundled, err := bundledComponents(root, sources)
	if err != nil {
		return "", err
	}
	if contact == "" {
		if contact, err = moduleURL(root); err != nil {
			return "", err
		}
	}
	goMods, err := goComponents(root, "darwin", "arm64")
	if err != nil {
		return "", err
	}
	swiftPkgs, err := swiftComponents(root, checkouts)
	if err != nil {
		return "", err
	}
	return render(bundled, goMods, swiftPkgs, contact), nil
}

// moduleURL is the https address of the repository named by the module path in
// go.mod, for the source offer.
func moduleURL(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "module" {
			host, _, _ := strings.Cut(fields[1], "/")
			if !strings.Contains(host, ".") {
				break
			}
			return "https://" + fields[1], nil
		}
	}
	return "", errors.New("go.mod has no module path that is a web address; pass -contact or set PLAITWAY_SOURCE_CONTACT")
}

// parseEnv reads KEY=VALUE lines, skipping blanks and # comments.
func parseEnv(file string) (map[string]string, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	vars := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s: malformed line %q", file, line)
		}
		vars[key] = value
	}
	return vars, sc.Err()
}

// bundledComponents describes the binaries built by build-openvpn.sh. The
// tarball checksum is verified here too, so the texts quoted below belong to
// the exact source named in the written offer.
func bundledComponents(root, sources string) ([]component, error) {
	vars, err := parseEnv(filepath.Join(root, "packaging", "openvpn-deps.env"))
	if err != nil {
		return nil, err
	}
	type spec struct {
		prefix, name, license string
		files                 []string // paths inside the tarball, the first is the license
		notice                string   // extra file shown as a notice instead of a license
		shipped               bool     // the archive goes into Contents/Resources/Source; keep in step with package-app.sh
	}
	specs := []spec{
		{"OPENVPN", "OpenVPN", "GPL-2.0 with OpenSSL and Apache-2.0 linking exceptions", []string{"COPYRIGHT.GPL"}, "COPYING", true},
		{"OPENSSL", "OpenSSL", "Apache-2.0", []string{"LICENSE.txt"}, "", false},
		{"LZO", "LZO", "GPL-2.0", []string{"COPYING"}, "", true},
		{"LZ4", "LZ4 (library)", "BSD-2-Clause", []string{"lib/LICENSE"}, "", true},
	}
	var out []component
	for _, s := range specs {
		url, version, sum := vars[s.prefix+"_URL"], vars[s.prefix+"_VERSION"], vars[s.prefix+"_SHA256"]
		if url == "" || version == "" || sum == "" {
			return nil, fmt.Errorf("openvpn-deps.env: %s_URL, _VERSION or _SHA256 missing", s.prefix)
		}
		tarball := filepath.Join(sources, path.Base(url))
		if err := verifySHA256(tarball, sum); err != nil {
			return nil, err
		}
		top := strings.TrimSuffix(path.Base(url), ".tar.gz")
		c := component{
			name: s.name, version: version, license: s.license,
			source:  fmt.Sprintf("%s (SHA-256 %s)", url, sum),
			archive: path.Base(url), sha256: sum, shipped: s.shipped,
		}
		for _, f := range s.files {
			body, err := tarballFile(tarball, top+"/"+f)
			if err != nil {
				return nil, err
			}
			kind, err := detectLicense(body)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", s.name, f, err)
			}
			c.docs = append(c.docs, document{kind, body})
		}
		if s.notice != "" {
			body, err := tarballFile(tarball, top+"/"+s.notice)
			if err != nil {
				return nil, err
			}
			c.docs = append(c.docs, document{"Notice", body})
		}
		out = append(out, c)
	}
	return out, nil
}

func verifySHA256(file, want string) error {
	f, err := os.Open(file)
	if err != nil {
		return fmt.Errorf("%w (run packaging/build-openvpn.sh first)", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("%s: SHA-256 %s does not match the pinned %s", file, got, want)
	}
	return nil
}

func tarballFile(tarball, name string) (string, error) {
	f, err := os.Open(tarball)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("%s: %w", tarball, err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return "", fmt.Errorf("%s: %s not found", tarball, name)
		}
		if err != nil {
			return "", fmt.Errorf("%s: %w", tarball, err)
		}
		if hdr.Name == name {
			body, err := io.ReadAll(tr)
			return string(body), err
		}
	}
}

// goComponents lists the modules linked into the daemon and the command line
// client for goos and goarch, plus the Go standard library, which is linked
// into every Go binary. The architecture does not change the set.
func goComponents(root, goos, goarch string) ([]component, error) {
	return goComponentsFor(root, goTarget{goos, goarch, "1"})
}

// goTarget is the platform whose build of the daemon and the client is listed;
// the modules differ by platform.
type goTarget struct{ goos, goarch, cgo string }

func goComponentsFor(root string, target goTarget) ([]component, error) {
	const format = `{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}`
	cmd := exec.Command("go", "list", "-deps", "-f", format, "./cmd/plaitwayd", "./cmd/plaitway")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS="+target.goos, "GOARCH="+target.goarch, "CGO_ENABLED="+target.cgo)
	cmd.Stderr = os.Stderr
	listing, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w", err)
	}
	seen := map[string]bool{}
	moduleDirs := map[string]string{}
	var out []component
	for _, line := range strings.Split(string(listing), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 3 {
			return nil, fmt.Errorf("go list: module %q has no source directory (run go mod download)", line)
		}
		if seen[fields[0]] {
			continue
		}
		seen[fields[0]] = true
		moduleDirs[fields[0]] = fields[2]
		c, err := directoryComponent(fields[0], fields[1], "https://pkg.go.dev/"+fields[0], fields[2])
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	goroot, err := exec.Command("go", "env", "GOROOT", "GOVERSION").Output()
	if err != nil {
		return nil, fmt.Errorf("go env: %w", err)
	}
	env := strings.Fields(string(goroot))
	if len(env) != 2 {
		return nil, fmt.Errorf("go env: unexpected output %q", goroot)
	}
	licenseDir, err := goLicenseDir(env[0], moduleDirs)
	if err != nil {
		return nil, err
	}
	std, err := directoryComponent("Go standard library", strings.TrimPrefix(env[1], "go"), "https://go.dev", licenseDir)
	if err != nil {
		return nil, err
	}
	out = append(out, std)
	sortComponents(out)
	return out, nil
}

// goXModule is a module of the Go project that is linked into both programs.
const goXModule = "golang.org/x/sys"

// goLicenseDir returns the directory with the LICENSE of the Go standard
// library. A Go tree has it in GOROOT, Homebrew installs the toolchain in
// libexec and keeps it beside that, and Gentoo deletes it. The modules of the
// Go project carry the same file, so their copy stands in, and modules maps
// the linked modules to their directories.
func goLicenseDir(goroot string, modules map[string]string) (string, error) {
	for _, dir := range []string{goroot, filepath.Dir(goroot)} {
		_, err := os.Stat(filepath.Join(dir, "LICENSE"))
		if err == nil {
			return dir, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	if dir := modules[goXModule]; dir != "" {
		return dir, nil
	}
	return "", fmt.Errorf("the Go standard library: no LICENSE in %s or beside it, and %s is not linked", goroot, goXModule)
}

type swiftResolved struct {
	Pins []struct {
		Identity string `json:"identity"`
		Location string `json:"location"`
		State    struct {
			Version string `json:"version"`
		} `json:"state"`
	} `json:"pins"`
}

// swiftComponents lists every package in Package.resolved. Some are build-time
// only; listing a superset is safe, omitting a linked one is not.
func swiftComponents(root, checkouts string) ([]component, error) {
	data, err := os.ReadFile(filepath.Join(root, "macos", "Package.resolved"))
	if err != nil {
		return nil, err
	}
	var resolved swiftResolved
	if err := json.Unmarshal(data, &resolved); err != nil {
		return nil, fmt.Errorf("Package.resolved: %w", err)
	}
	var out []component
	for _, pin := range resolved.Pins {
		dir := filepath.Join(checkouts, pin.Identity)
		source := strings.TrimSuffix(pin.Location, ".git")
		c, err := directoryComponent(pin.Identity, pin.State.Version, source, dir)
		if err != nil {
			return nil, fmt.Errorf("%w (build the Swift package first)", err)
		}
		out = append(out, c)
	}
	sortComponents(out)
	return out, nil
}

func sortComponents(cs []component) {
	sort.Slice(cs, func(i, j int) bool { return cs[i].name < cs[j].name })
}

// directoryComponent reads the license and any notice file at the top of a
// source directory.
func directoryComponent(name, version, source, dir string) (component, error) {
	c := component{name: name, version: version, source: source}
	licenseBody, err := firstFile(dir, "LICENSE", "LICENSE.txt", "LICENSE.md", "COPYING")
	if err != nil {
		return c, fmt.Errorf("%s: %w", name, err)
	}
	c.license, err = detectLicense(licenseBody)
	if err != nil {
		return c, fmt.Errorf("%s: %w", name, err)
	}
	c.docs = append(c.docs, document{c.license, licenseBody})
	if noticeBody, err := firstFile(dir, "NOTICE", "NOTICE.txt", "NOTICES.txt"); err == nil {
		c.docs = append(c.docs, document{"Notice", noticeBody})
	}
	return c, nil
}

func firstFile(dir string, names ...string) (string, error) {
	for _, n := range names {
		body, err := os.ReadFile(filepath.Join(dir, n))
		if err == nil {
			return string(body), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return "", fmt.Errorf("none of %s in %s", strings.Join(names, ", "), dir)
}

// detectLicense names the license of a verbatim license text. An unrecognized
// text is an error: a new dependency with unusual terms needs a human look
// before it ships.
func detectLicense(text string) (string, error) {
	switch {
	case strings.Contains(text, "GNU GENERAL PUBLIC LICENSE") && strings.Contains(text, "Version 2, June 1991"):
		return "GPL-2.0", nil
	case strings.Contains(text, "GNU GENERAL PUBLIC LICENSE") && strings.Contains(text, "Version 3, 29 June 2007"):
		return "GPL-3.0", nil
	case strings.Contains(text, "Apache License") && strings.Contains(text, "Version 2.0, January 2004"):
		return "Apache-2.0", nil
	case strings.Contains(text, "Mozilla Public License Version 2.0"):
		return "MPL-2.0", nil
	case strings.Contains(text, "Permission is hereby granted, free of charge"):
		return "MIT", nil
	case strings.Contains(text, "Redistribution and use in source and binary forms") &&
		strings.Contains(text, "Neither the name"):
		return "BSD-3-Clause", nil
	case strings.Contains(text, "Redistribution and use in source and binary forms"):
		return "BSD-2-Clause", nil
	case strings.Contains(text, "Permission to use, copy, modify, and/or distribute this software"):
		return "ISC", nil
	}
	return "", errors.New("license text not recognized")
}

// render writes the notices. Without bundled programs (the Linux package) there
// is no source offer to write and no table of programs; without Swift packages
// there is no table of them.
func render(bundled, goMods, swiftPkgs []component, contact string) string {
	var b strings.Builder
	b.WriteString(`# Third-party notices

Plaitway bundles the software listed below. This file is generated by
packaging/notices from the upstream sources; do not edit it by hand.

`)
	if len(bundled) > 0 {
		writeSourceOffer(&b, bundled, contact)
		b.WriteString("\n## Bundled programs\n\n")
		writeTable(&b, bundled)
	} else {
		b.WriteString(`OpenVPN is not part of this package: it depends on the openvpn package of
the distribution, whose license and source come from that package.
`)
	}
	b.WriteString("\n## Go modules compiled into plaitwayd and plaitway\n\n")
	writeTable(&b, goMods)
	if len(swiftPkgs) > 0 {
		b.WriteString("\n## Swift packages\n\n")
		writeTable(&b, swiftPkgs)
	}
	b.WriteString(trademarksSection(""))
	writeTexts(&b, append(append(append([]component{}, bundled...), goMods...), swiftPkgs...))
	return b.String()
}

// trademarksSection is the notice about the names of other projects; extra is
// further sentences of a platform, or nothing.
func trademarksSection(extra string) string {
	return `
## Trademarks

OpenVPN is a registered trademark of OpenVPN Inc. WireGuard is a registered
trademark of Jason A. Donenfeld. Plaitway is not affiliated with or endorsed
by either.
` + extra
}

// writeTexts writes each distinct license or notice text once, with the
// components it applies to.
func writeTexts(b *strings.Builder, all []component) {
	b.WriteString("\n## License and notice texts\n")
	type group struct {
		document
		users []string
	}
	groups := map[document]*group{}
	var order []*group
	for _, c := range all {
		for _, d := range c.docs {
			d.body = normalize(d.body)
			g := groups[d]
			if g == nil {
				g = &group{document: d}
				groups[d] = g
				order = append(order, g)
			}
			g.users = append(g.users, c.name)
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i].kind < order[j].kind })
	for _, g := range order {
		fmt.Fprintf(b, "\n### %s\n\nApplies to: %s\n\n", g.kind, strings.Join(g.users, ", "))
		for _, line := range strings.Split(g.body, "\n") {
			b.WriteString("    " + line + "\n")
		}
	}
}

// writeSourceOffer writes the GPLv2 source offer for the bundled programs: the
// source ships with the app (section 3(a)) and a written offer (section 3(b))
// covers a copy that lost it. OpenSSL and the other programs that are not
// shipped appear by URL.
func writeSourceOffer(b *strings.Builder, bundled []component, contact string) {
	b.WriteString(`## Source offer

OpenVPN and LZO are licensed under the GNU General Public License version 2
(GPLv2). Plaitway.app contains them as unmodified upstream releases, compiled
by build-openvpn.sh together with the libraries below, which are linked
statically into the openvpn executable.

The complete corresponding source code accompanies this copy of Plaitway, as
GPLv2 section 3(a) requires. Contents/Resources/Source holds:

`)
	for _, c := range bundled {
		if c.shipped {
			fmt.Fprintf(b, "- %s: %s %s (SHA-256 %s)\n", c.archive, c.name, c.version, c.sha256)
		}
	}
	fmt.Fprintf(b, "- %s: the scripts that control compilation, as they built the bundled openvpn\n", strings.Join(sourceScripts, ", "))
	b.WriteString(`
To rebuild, put the scripts in a folder and the archives in
../build/openvpn-work/src relative to it, then run build-openvpn.sh (it needs
the Xcode command line tools).

Not shipped, because its license (Apache-2.0) has no source offer:

`)
	for _, c := range bundled {
		if !c.shipped {
			fmt.Fprintf(b, "- %s %s: %s\n", c.name, c.version, c.source)
		}
	}
	fmt.Fprintf(b, `
Written offer, GPLv2 section 3(b): for three years from the day you received
this copy of Plaitway, you may obtain a complete machine-readable copy of the
source code listed above, including the scripts, from
%s
for a charge no more than the cost of physically performing the distribution.

`, contact)
}

func writeTable(b *strings.Builder, cs []component) {
	b.WriteString("| Component | Version | License | Source |\n|---|---|---|---|\n")
	for _, c := range cs {
		fmt.Fprintf(b, "| %s | %s | %s | %s |\n", c.name, c.version, c.license, c.source)
	}
}

// normalize makes equal texts compare equal despite line endings and padding.
func normalize(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.Join(lines, "\n")
}
