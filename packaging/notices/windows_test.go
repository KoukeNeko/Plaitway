package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	mitText = "Permission is hereby granted, free of charge, to any person"
	msTerms = "MICROSOFT SOFTWARE LICENSE TERMS\nMICROSOFT WINDOWS APP SDK"
)

// mustWriteAll is mustWrite for a path whose folders do not exist yet.
func mustWriteAll(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, path, body)
}

func writeNuspec(t *testing.T, folder, metadata string) {
	t.Helper()
	mustWrite(t, filepath.Join(folder, "pkg.nuspec"),
		`<?xml version="1.0"?><package xmlns="http://schemas.microsoft.com/packaging/2012/06/nuspec.xsd"><metadata>`+metadata+`</metadata></package>`)
}

func TestReadAssetsPackagesKeepsShippedPackagesOnly(t *testing.T) {
	assets := filepath.Join(t.TempDir(), "project.assets.json")
	mustWrite(t, assets, `{
  "libraries": {
    "Google.Protobuf/3.36.2": {"type": "package", "path": "google.protobuf/3.36.2"},
    "Microsoft.Windows.SDK.BuildTools/10.0.26100.4654": {"type": "package", "path": "microsoft.windows.sdk.buildtools/10.0.26100.4654"},
    "Plaitway.Client/0.3.4": {"type": "project", "path": "../Plaitway.Client/Plaitway.Client.csproj"}
  },
  "project": {"frameworks": {"net10.0-windows10.0.19041.0": {"downloadDependencies": [
    {"name": "Microsoft.NETCore.App.Runtime.win-x64", "version": "[10.0.12, 10.0.12]"},
    {"name": "Microsoft.Windows.SDK.NET.Ref", "version": "[10.0.19041.57, 10.0.19041.57]"}
  ]}}}
}`)

	got, err := readAssetsPackages(assets)
	if err != nil {
		t.Fatal(err)
	}

	var ids []string
	for _, pkg := range got {
		ids = append(ids, pkg.id)
	}
	want := "Google.Protobuf Microsoft.Windows.SDK.NET.Ref"
	if joined := strings.Join(sorted(ids), " "); joined != want {
		t.Fatalf("packages = %q, want %q: build tools and projects are not shipped, the runtime pack is described separately", joined, want)
	}
}

func sorted(values []string) []string {
	out := append([]string{}, values...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestReadAssetsPackagesNamesTheFixWhenNotRestored(t *testing.T) {
	_, err := readAssetsPackages(filepath.Join(t.TempDir(), "missing.json"))
	if err == nil || !strings.Contains(err.Error(), "restore the app first") {
		t.Fatalf("error = %v, want one that says to restore", err)
	}
}

func TestNugetComponentUsesTheLicenseFileOfThePackage(t *testing.T) {
	folder := t.TempDir()
	writeNuspec(t, folder, `<id>Microsoft.WindowsAppSDK.WinUI</id><version>2.3.9</version><license type="file">license.txt</license>`)
	mustWrite(t, filepath.Join(folder, "license.txt"), msTerms)
	mustWrite(t, filepath.Join(folder, "NOTICE.txt"), "third-party attribution")

	c, err := nugetComponent(folder)
	if err != nil {
		t.Fatal(err)
	}
	if c.license != "Microsoft Software License Terms" || len(c.docs) != 2 || c.docs[1].kind != "Notice" {
		t.Fatalf("component = %+v, want the Microsoft terms and one notice", c)
	}
	if c.source != "https://www.nuget.org/packages/Microsoft.WindowsAppSDK.WinUI/2.3.9" {
		t.Fatalf("source = %q, want the NuGet page when the package names no repository", c.source)
	}
}

func TestNugetComponentFindsALicenseFileByAnyCase(t *testing.T) {
	folder := t.TempDir()
	writeNuspec(t, folder, `<id>CommunityToolkit.Mvvm</id><version>8.4.2</version><license type="expression">MIT</license>`)
	mustWrite(t, filepath.Join(folder, "License.md"), mitText)

	c, err := nugetComponent(folder)
	if err != nil {
		t.Fatal(err)
	}
	if c.license != "MIT" {
		t.Fatalf("license = %q, want MIT read from License.md", c.license)
	}
}

func TestNugetComponentWithOnlyAnExpressionGetsTheVendoredText(t *testing.T) {
	folder := t.TempDir()
	writeNuspec(t, folder, `<id>Google.Protobuf</id><version>3.36.2</version><license type="expression">BSD-3-Clause</license>`)

	c, err := nugetComponent(folder)
	if err != nil {
		t.Fatal(err)
	}
	if c.license != "BSD-3-Clause" || len(c.docs) != 1 || !strings.Contains(c.docs[0].body, "Copyright 2008 Google Inc.") {
		t.Fatalf("component = %+v, want the text of the protobuf repository", c)
	}
}

func TestNugetComponentWithAnUnknownExpressionIsAnError(t *testing.T) {
	folder := t.TempDir()
	writeNuspec(t, folder, `<id>Some.New.Package</id><version>1.0.0</version><license type="expression">MIT</license>`)

	_, err := nugetComponent(folder)
	if err == nil || !strings.Contains(err.Error(), "ships no text") {
		t.Fatalf("error = %v, want a refusal that asks for the upstream text", err)
	}
}

func TestNugetComponentWithOnlyALinkIsListedWithTheLink(t *testing.T) {
	folder := t.TempDir()
	writeNuspec(t, folder, `<id>Microsoft.Windows.SDK.NET.Ref</id><version>10.0.19041.57</version><licenseUrl>https://aka.ms/WinSDKLicenseURL</licenseUrl>`)

	c, err := nugetComponent(folder)
	if err != nil {
		t.Fatal(err)
	}
	if c.license != "terms at https://aka.ms/WinSDKLicenseURL" || len(c.docs) != 0 {
		t.Fatalf("component = %+v, want the link and no text", c)
	}
}

func TestNugetComponentWithoutAnyLicenseIsAnError(t *testing.T) {
	folder := t.TempDir()
	writeNuspec(t, folder, `<id>Unlicensed</id><version>1.0.0</version>`)

	if _, err := nugetComponent(folder); err == nil {
		t.Fatal("a package without a license must be an error")
	}
}

func TestFirstFileFoldReportsMissingFilesAsNotExist(t *testing.T) {
	_, err := firstFileFold(t.TempDir(), "NOTICE")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("error = %v, want fs.ErrNotExist", err)
	}
}

// pinnedLicenses are the vendored files by the SHA-256 of their text with
// Unix line ends, which is how git may check them out on any platform.
var pinnedLicenses = map[string]string{
	"Google.Protobuf.LICENSE":      "6e5e117324afd944dcf67f36cf329843bc1a92229a8cd9bb573d7a83130fea7d",
	"Grpc.LICENSE":                 "cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30",
	"Microsoft.Extensions.LICENSE": "cfc21f5e8bd655ae997eec916138b707b1d290b83272c02a95c9f821b8c87310",
}

func TestVendoredLicensesAreTheUpstreamFiles(t *testing.T) {
	for _, source := range expressionOnlyLicenses {
		data, err := vendoredLicenses.ReadFile(vendoredFolder + "/" + source.file)
		if err != nil {
			t.Errorf("%s: %v", source.file, err)
			continue
		}
		sum := sha256.Sum256([]byte(strings.ReplaceAll(string(data), "\r\n", "\n")))
		if got, want := hex.EncodeToString(sum[:]), pinnedLicenses[source.file]; got != want {
			t.Errorf("%s has SHA-256 %s, want %s: it must stay the file at %s", source.file, got, want, source.upstream)
		}
		if _, err := detectLicense(string(data)); err != nil {
			t.Errorf("%s: %v", source.file, err)
		}
	}
}

func TestDotnetRuntimeComponentReadsThePackNamedByThePublishFolder(t *testing.T) {
	publish, cache := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(publish, runtimeConfigName),
		`{"runtimeOptions":{"includedFrameworks":[{"name":"Microsoft.NETCore.App","version":"10.0.12"}]}}`)
	pack := filepath.Join(cache, "microsoft.netcore.app.runtime.win-x64", "10.0.12")
	mustWriteAll(t, filepath.Join(pack, "LICENSE.TXT"), mitText)
	mustWriteAll(t, filepath.Join(pack, "THIRD-PARTY-NOTICES.TXT"), "zlib and others")

	c, err := dotnetRuntimeComponent(publish, cache, "win-x64")
	if err != nil {
		t.Fatal(err)
	}
	if c.name != ".NET runtime" || c.version != "10.0.12" || c.license != "MIT" || len(c.docs) != 2 {
		t.Fatalf("component = %+v", c)
	}
}

func TestDotnetRuntimeComponentRefusesAFrameworkDependentPublish(t *testing.T) {
	publish := t.TempDir()
	mustWrite(t, filepath.Join(publish, runtimeConfigName), `{"runtimeOptions":{"framework":{"name":"Microsoft.NETCore.App","version":"10.0.0"}}}`)

	_, err := dotnetRuntimeComponent(publish, t.TempDir(), "win-x64")
	if err == nil || !strings.Contains(err.Error(), "publish self-contained") {
		t.Fatalf("error = %v, want a refusal that asks for a self-contained publish", err)
	}
}

func TestDotnetRuntimeID(t *testing.T) {
	for goarch, want := range map[string]string{"amd64": "win-x64", "arm64": "win-arm64"} {
		if got, err := dotnetRuntimeID(goarch); err != nil || got != want {
			t.Errorf("dotnetRuntimeID(%q) = %q, %v; want %q", goarch, got, err, want)
		}
	}
	if _, err := dotnetRuntimeID("386"); err == nil {
		t.Error("dotnetRuntimeID accepted 386")
	}
}

func TestWintunComponentCarriesTheRepositoryNotice(t *testing.T) {
	root := t.TempDir()
	mustWriteAll(t, filepath.Join(root, "packaging", "windows", "wintun", "wintun.json"), `{"version":"0.14.1","url":"https://www.wintun.net/builds/wintun-0.14.1.zip"}`)
	mustWriteAll(t, filepath.Join(root, "packaging", "windows", "wintun", "THIRD_PARTY.txt"), "Prebuilt Binaries License")

	c, err := wintunComponent(root)
	if err != nil {
		t.Fatal(err)
	}
	if c.version != "0.14.1" || len(c.docs) != 1 || c.docs[0].body != "Prebuilt Binaries License" {
		t.Fatalf("component = %+v", c)
	}
}

func TestRenderWindowsHasNoSourceOfferAndSaysOpenVPNIsNotShipped(t *testing.T) {
	mk := func(name string) component {
		return component{name: name, version: "1", license: "MIT", source: "src", docs: []document{{"MIT", mitText}}}
	}
	out := renderWindows(mk("Wintun"), []component{mk("goMod")}, []component{mk("package")}, mk(".NET runtime"))

	for _, want := range []string{"## Bundled programs", "## .NET runtime", "## NuGet packages of the app", "## Go modules compiled into plaitwayd and plaitway", "## Trademarks", "Windows is a trademark", "ships no OpenVPN", "Applies to: Wintun, .NET runtime, package, goMod"} {
		if !strings.Contains(strings.Join(strings.Fields(out), " "), want) {
			t.Errorf("the Windows notices lack %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Source offer") {
		t.Errorf("the Windows notices carry a source offer for software they do not ship:\n%s", out)
	}
}

// macosRenderDigest is the SHA-256 of render's output for the fixed components
// below, taken before the Windows document was added. The macOS document must
// not change because of it.
const macosRenderDigest = "307542bfc07fe3270e1c4f1faf1009233e4f69d0b858226c01e1c6bfe4a5cf69"

func TestMacOSDocumentIsUnchanged(t *testing.T) {
	mk := func(name, license string) component {
		return component{name: name, version: "1.0", license: license, source: "https://example.org/" + name, docs: []document{{license, mitText + " " + license}}}
	}
	out := render(offerComponents(), []component{mk("alpha", "MIT"), mk("beta", "BSD-3-Clause")}, []component{mk("gamma", "Apache-2.0")}, "https://example.org/plaitway")

	sum := sha256.Sum256([]byte(out))
	if got := hex.EncodeToString(sum[:]); got != macosRenderDigest {
		t.Fatalf("the macOS document changed: SHA-256 %s, want %s\n%s", got, macosRenderDigest, out)
	}
}
