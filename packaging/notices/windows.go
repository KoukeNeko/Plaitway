package main

// The notices of the Windows installer (go run ./packaging/notices -windows).
//
// They list what the installed files are made of, from the same kind of data
// as the macOS document:
//
//   - the Go modules compiled into plaitwayd.exe and plaitway.exe (go list for
//     windows, as the macOS document does for darwin);
//   - the NuGet packages whose files are in the app's publish folder, read from
//     the app's project.assets.json and the NuGet cache, texts verbatim;
//   - the .NET runtime that a self-contained publish carries, named by the
//     publish folder's runtimeconfig.json;
//   - Wintun, whose notice and license text the repository keeps next to its
//     pin.
//
// There is no source offer: the installer ships no GPL program. OpenVPN is the
// user's own installation, which the daemon finds (packaging/windows/README.md).

import (
	"embed"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// vendoredLicenses holds the texts of packages that name a license in their
// metadata and carry none. Each is the upstream file, byte for byte; the test
// pins their hashes.
//
//go:embed windows/licenses/*
var vendoredLicenses embed.FS

const (
	appAssetsPath       = "windows/Sources/Plaitway.App/obj/project.assets.json"
	runtimeConfigName   = "Plaitway.runtimeconfig.json"
	wintunPinPath       = "packaging/windows/wintun/wintun.json"
	wintunNoticePath    = "packaging/windows/wintun/THIRD_PARTY.txt"
	nugetPackagesEnv    = "NUGET_PACKAGES"
	nugetPackagesInHome = ".nuget/packages"
	dotnetFramework     = "Microsoft.NETCore.App"
	dotnetSourceURL     = "https://github.com/dotnet/runtime"
	nugetGalleryURL     = "https://www.nuget.org/packages/"
	vendoredFolder      = "windows/licenses"
)

// buildOnlyPackagePrefixes are packages the app restores that put no file into
// its publish folder: tools of the build.
var buildOnlyPackagePrefixes = []string{"Microsoft.Windows.SDK.BuildTools"}

// shippedPackPrefixes are packages the app downloads rather than references and
// whose files still end up in the publish folder (the Windows SDK projection).
var shippedPackPrefixes = []string{"Microsoft.Windows.SDK.NET.Ref"}

// expressionOnlyLicenses says where the text comes from for a package whose
// metadata names a license by its SPDX identifier and ships no file.
var expressionOnlyLicenses = []struct{ idPrefix, file, upstream string }{
	{"Google.Protobuf", "Google.Protobuf.LICENSE", "https://github.com/protocolbuffers/protobuf/blob/main/LICENSE"},
	{"Grpc.", "Grpc.LICENSE", "https://github.com/grpc/grpc-dotnet/blob/master/LICENSE"},
	{"Microsoft.Extensions.", "Microsoft.Extensions.LICENSE", "https://github.com/dotnet/runtime/blob/main/LICENSE.TXT"},
}

var (
	licenseFileNames = []string{"LICENSE", "LICENSE.txt", "LICENSE.md", "COPYING"}
	noticeFileNames  = []string{"NOTICE", "NOTICE.txt", "NOTICES.txt", "ThirdPartyNotices.txt", "THIRD-PARTY-NOTICES.TXT"}
)

// windowsInputs is where the Windows document finds its data.
type windowsInputs struct {
	publishDir, assetsFile, nugetRoot, goarch string
}

func windowsFlags() *windowsInputs {
	inputs := &windowsInputs{}
	flag.StringVar(&inputs.publishDir, "publish", "", "with -windows: the app's publish folder (runtimeconfig.json names the .NET runtime it carries)")
	flag.StringVar(&inputs.assetsFile, "assets", "", "with -windows: project.assets.json of the app (default: "+appAssetsPath+")")
	flag.StringVar(&inputs.nugetRoot, "nuget", "", "with -windows: the NuGet package cache (default: $"+nugetPackagesEnv+", else ~/"+nugetPackagesInHome+")")
	flag.StringVar(&inputs.goarch, "goarch", "amd64", "with -windows: amd64 or arm64")
	return inputs
}

func runWindows(root, outPath string, inputs windowsInputs) {
	doc, err := generateWindows(root, inputs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "notices:", err)
		os.Exit(1)
	}
	emit(outPath, doc)
}

func generateWindows(root string, inputs windowsInputs) (string, error) {
	if inputs.publishDir == "" {
		return "", errors.New("-publish is required with -windows: the app's publish folder")
	}
	if inputs.assetsFile == "" {
		inputs.assetsFile = filepath.Join(root, filepath.FromSlash(appAssetsPath))
	}
	if inputs.nugetRoot == "" {
		var err error
		if inputs.nugetRoot, err = defaultNugetRoot(); err != nil {
			return "", err
		}
	}
	runtimeID, err := dotnetRuntimeID(inputs.goarch)
	if err != nil {
		return "", err
	}

	goMods, err := goComponentsFor(root, goTarget{"windows", inputs.goarch, "0"})
	if err != nil {
		return "", err
	}
	packages, err := nugetComponents(inputs.assetsFile, inputs.nugetRoot)
	if err != nil {
		return "", err
	}
	dotnet, err := dotnetRuntimeComponent(inputs.publishDir, inputs.nugetRoot, runtimeID)
	if err != nil {
		return "", err
	}
	wintun, err := wintunComponent(root)
	if err != nil {
		return "", err
	}
	return renderWindows(wintun, goMods, packages, dotnet), nil
}

func defaultNugetRoot() (string, error) {
	if folder := os.Getenv(nugetPackagesEnv); folder != "" {
		return folder, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find the NuGet package cache: %w; pass -nuget", err)
	}
	return filepath.Join(home, filepath.FromSlash(nugetPackagesInHome)), nil
}

// dotnetRuntimeID is the runtime identifier of the runtime pack for a Go
// architecture.
func dotnetRuntimeID(goarch string) (string, error) {
	switch goarch {
	case "amd64":
		return "win-x64", nil
	case "arm64":
		return "win-arm64", nil
	}
	return "", fmt.Errorf("-goarch %q: Windows builds are amd64 or arm64", goarch)
}

// nugetPackage is a library of project.assets.json that is a NuGet package.
type nugetPackage struct {
	id, version, folder string
}

// assetsFile is the part of project.assets.json that the notices read.
type assetsFile struct {
	Libraries map[string]struct {
		Type string `json:"type"`
		Path string `json:"path"`
	} `json:"libraries"`
	Project struct {
		Frameworks map[string]struct {
			DownloadDependencies []struct {
				Name string `json:"name"`
			} `json:"downloadDependencies"`
		} `json:"frameworks"`
	} `json:"project"`
}

// readAssetsPackages lists the packages that contribute to the publish folder:
// the referenced ones except the tools of the build, and the packs that are
// downloaded and still shipped.
func readAssetsPackages(assetsPath string) ([]nugetPackage, error) {
	data, err := os.ReadFile(assetsPath)
	if err != nil {
		return nil, fmt.Errorf("%w (restore the app first: dotnet build windows/Plaitway.sln)", err)
	}
	var assets assetsFile
	if err := json.Unmarshal(data, &assets); err != nil {
		return nil, fmt.Errorf("%s: %w", assetsPath, err)
	}
	var out []nugetPackage
	for key, library := range assets.Libraries {
		id, version, ok := strings.Cut(key, "/")
		if !ok || library.Type != "package" || hasAnyPrefix(id, buildOnlyPackagePrefixes) {
			continue
		}
		out = append(out, nugetPackage{id: id, version: version, folder: library.Path})
	}
	for _, framework := range assets.Project.Frameworks {
		for _, download := range framework.DownloadDependencies {
			if hasAnyPrefix(download.Name, shippedPackPrefixes) {
				out = append(out, nugetPackage{id: download.Name})
			}
		}
	}
	return out, nil
}

func hasAnyPrefix(text string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

// nugetComponents describes each package of the app, sorted by name.
func nugetComponents(assetsPath, nugetRoot string) ([]component, error) {
	packages, err := readAssetsPackages(assetsPath)
	if err != nil {
		return nil, err
	}
	var out []component
	for _, pkg := range packages {
		folder, err := packageFolder(nugetRoot, pkg)
		if err != nil {
			return nil, err
		}
		c, err := nugetComponent(folder)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	sortComponents(out)
	return out, nil
}

// packageFolder is the folder of a package in the cache. A downloaded pack has
// no version here; the newest folder is the one the build used, since a
// restore keeps no older one in step with the project.
func packageFolder(nugetRoot string, pkg nugetPackage) (string, error) {
	if pkg.folder != "" {
		return filepath.Join(nugetRoot, filepath.FromSlash(pkg.folder)), nil
	}
	idFolder := filepath.Join(nugetRoot, strings.ToLower(pkg.id))
	entries, err := os.ReadDir(idFolder)
	if err != nil {
		return "", fmt.Errorf("%s is not in the NuGet cache: %w", pkg.id, err)
	}
	var versions []string
	for _, entry := range entries {
		if entry.IsDir() {
			versions = append(versions, entry.Name())
		}
	}
	if len(versions) == 0 {
		return "", fmt.Errorf("%s has no version in %s", pkg.id, idFolder)
	}
	sort.Strings(versions)
	return filepath.Join(idFolder, versions[len(versions)-1]), nil
}

// nuspec is the part of a package's metadata that the notices read.
type nuspec struct {
	Metadata struct {
		ID         string `xml:"id"`
		Version    string `xml:"version"`
		Copyright  string `xml:"copyright"`
		LicenseURL string `xml:"licenseUrl"`
		License    struct {
			Type  string `xml:"type,attr"`
			Value string `xml:",chardata"`
		} `xml:"license"`
		Repository struct {
			URL string `xml:"url,attr"`
		} `xml:"repository"`
	} `xml:"metadata"`
}

func readNuspec(folder string) (nuspec, error) {
	matches, err := filepath.Glob(filepath.Join(folder, "*.nuspec"))
	if err != nil || len(matches) != 1 {
		return nuspec{}, fmt.Errorf("%s: expected one .nuspec file", folder)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		return nuspec{}, err
	}
	var spec nuspec
	if err := xml.Unmarshal(data, &spec); err != nil {
		return nuspec{}, fmt.Errorf("%s: %w", matches[0], err)
	}
	return spec, nil
}

// nugetComponent reads the license and the notices of one package folder. The
// text of the package is used when it has one; a package that only names its
// license by identifier gets the vendored upstream text; one that only links
// to terms is listed with the link, since there is no text to copy.
func nugetComponent(folder string) (component, error) {
	spec, err := readNuspec(folder)
	if err != nil {
		return component{}, err
	}
	meta := spec.Metadata
	c := component{name: meta.ID, version: meta.Version, source: packageSource(meta.ID, meta.Version, meta.Repository.URL)}

	body, found, err := packageLicenseText(folder, spec)
	if err != nil {
		return component{}, fmt.Errorf("%s %s: %w", meta.ID, meta.Version, err)
	}
	switch {
	case found:
		c.license, err = detectWindowsLicense(body)
		if err != nil {
			return component{}, fmt.Errorf("%s %s: %w", meta.ID, meta.Version, err)
		}
		c.docs = append(c.docs, document{c.license, body})
	case meta.LicenseURL != "":
		c.license = "terms at " + meta.LicenseURL
	default:
		return component{}, fmt.Errorf("%s %s: no license text, expression or link in %s", meta.ID, meta.Version, folder)
	}
	if notice, err := firstFileFold(folder, noticeFileNames...); err == nil {
		c.docs = append(c.docs, document{"Notice", notice})
	} else if !errors.Is(err, fs.ErrNotExist) {
		return component{}, fmt.Errorf("%s %s: %w", meta.ID, meta.Version, err)
	}
	return c, nil
}

func packageSource(id, version, repository string) string {
	if repository != "" {
		return strings.TrimSuffix(repository, ".git")
	}
	return nugetGalleryURL + id + "/" + version
}

// packageLicenseText returns the text of the license and whether there is one.
func packageLicenseText(folder string, spec nuspec) (string, bool, error) {
	license := spec.Metadata.License
	switch license.Type {
	case "file":
		body, err := os.ReadFile(filepath.Join(folder, filepath.FromSlash(strings.TrimSpace(license.Value))))
		return string(body), err == nil, err
	case "expression":
		if body, err := firstFileFold(folder, licenseFileNames...); err == nil {
			return body, true, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", false, err
		}
		return vendoredLicenseText(spec.Metadata.ID, strings.TrimSpace(license.Value))
	}
	return "", false, nil
}

// vendoredLicenseText is the upstream text kept for a package that names the
// license without shipping it. A package for which nothing is kept is an
// error: its license has to be read by a person before it ships.
func vendoredLicenseText(id, expression string) (string, bool, error) {
	for _, source := range expressionOnlyLicenses {
		if !strings.HasPrefix(id, source.idPrefix) {
			continue
		}
		data, err := vendoredLicenses.ReadFile(path.Join(vendoredFolder, source.file))
		if err != nil {
			return "", false, err
		}
		return string(data), true, nil
	}
	return "", false, fmt.Errorf("names the license %q and ships no text; add the upstream file to %s and to expressionOnlyLicenses", expression, vendoredFolder)
}

// firstFileFold is the first of names that exists in dir, comparing names
// without regard to case (a NuGet package is a zip, whose names have the case
// of whoever packed it). It returns fs.ErrNotExist when there is none.
func firstFileFold(dir string, names ...string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, name := range names {
		for _, entry := range entries {
			if entry.IsDir() || !strings.EqualFold(entry.Name(), name) {
				continue
			}
			body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			return string(body), err
		}
	}
	return "", fmt.Errorf("none of %s in %s: %w", strings.Join(names, ", "), dir, fs.ErrNotExist)
}

// detectWindowsLicense knows the licenses of macOS dependencies and the
// Microsoft terms of the Windows App SDK. They are separate so that a text of
// the one kind never changes how the other document is made.
func detectWindowsLicense(text string) (string, error) {
	if id, err := detectLicense(text); err == nil {
		return id, nil
	}
	if strings.Contains(text, "MICROSOFT SOFTWARE LICENSE TERMS") {
		return "Microsoft Software License Terms", nil
	}
	return "", errors.New("license text not recognized")
}

// runtimeConfig is the part of Plaitway.runtimeconfig.json that names the
// runtime of a self-contained publish.
type runtimeConfig struct {
	RuntimeOptions struct {
		IncludedFrameworks []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"includedFrameworks"`
	} `json:"runtimeOptions"`
}

// dotnetRuntimeComponent describes the .NET runtime that the publish folder
// carries, from the runtime pack the build used. A framework-dependent publish
// carries none, and is not what an installer ships.
func dotnetRuntimeComponent(publishDir, nugetRoot, runtimeID string) (component, error) {
	data, err := os.ReadFile(filepath.Join(publishDir, runtimeConfigName))
	if err != nil {
		return component{}, err
	}
	var config runtimeConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return component{}, fmt.Errorf("%s: %w", runtimeConfigName, err)
	}
	for _, framework := range config.RuntimeOptions.IncludedFrameworks {
		if framework.Name == dotnetFramework {
			return runtimePackComponent(nugetRoot, runtimeID, framework.Version)
		}
	}
	return component{}, fmt.Errorf("%s names no included %s: publish self-contained", runtimeConfigName, dotnetFramework)
}

func runtimePackComponent(nugetRoot, runtimeID, version string) (component, error) {
	folder := filepath.Join(nugetRoot, strings.ToLower(dotnetFramework+".Runtime."+runtimeID), version)
	license, err := firstFileFold(folder, licenseFileNames...)
	if err != nil {
		return component{}, err
	}
	id, err := detectLicense(license)
	if err != nil {
		return component{}, fmt.Errorf(".NET runtime: %w", err)
	}
	c := component{name: ".NET runtime", version: version, license: id, source: dotnetSourceURL, docs: []document{{id, license}}}
	if notice, err := firstFileFold(folder, noticeFileNames...); err == nil {
		c.docs = append(c.docs, document{"Notice", notice})
	} else if !errors.Is(err, fs.ErrNotExist) {
		return component{}, err
	}
	return c, nil
}

// wintunPin is the part of wintun.json that the notices read.
type wintunPin struct {
	Version string `json:"version"`
	URL     string `json:"url"`
}

// wintunComponent is Wintun with the notice the repository keeps for it: that
// text carries the license and says what shipping the DLL asks of the installer.
func wintunComponent(root string) (component, error) {
	pinData, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(wintunPinPath)))
	if err != nil {
		return component{}, err
	}
	var pin wintunPin
	if err := json.Unmarshal(pinData, &pin); err != nil {
		return component{}, fmt.Errorf("%s: %w", wintunPinPath, err)
	}
	notice, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(wintunNoticePath)))
	if err != nil {
		return component{}, err
	}
	return component{
		name: "Wintun", version: pin.Version, license: "Wintun Prebuilt Binaries License", source: pin.URL,
		docs: []document{{"Wintun Prebuilt Binaries License", string(notice)}},
	}, nil
}

func renderWindows(wintun component, goMods, packages []component, dotnet component) string {
	var b strings.Builder
	b.WriteString(`# Third-party notices

Plaitway for Windows contains the software listed below. This file is generated
by packaging/notices from the upstream sources; do not edit it by hand.

The installer ships no OpenVPN. The helper runs the OpenVPN that is installed on
the computer, if there is one.

## Bundled programs

`)
	writeTable(&b, []component{wintun})
	b.WriteString("\n## .NET runtime\n\n")
	writeTable(&b, []component{dotnet})
	b.WriteString("\n## NuGet packages of the app\n\n")
	writeTable(&b, packages)
	b.WriteString("\n## Go modules compiled into plaitwayd and plaitway\n\n")
	writeTable(&b, goMods)
	b.WriteString(trademarksSection(`
Windows is a trademark of the Microsoft group of companies. Wintun is a project
of WireGuard LLC.
`))
	writeTexts(&b, append(append([]component{wintun, dotnet}, packages...), goMods...))
	return b.String()
}
