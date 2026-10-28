package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mitText = "Permission is hereby granted, free of charge, to any person"

// mustWriteAll is mustWrite for a path whose folders do not exist yet.
func mustWriteAll(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, path, body)
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

func TestRenderWindowsPointsAtTheCrateNoticesAndHasNoSourceOffer(t *testing.T) {
	mk := func(name string) component {
		return component{name: name, version: "1", license: "MIT", source: "src", docs: []document{{"MIT", mitText}}}
	}
	out := renderWindows(mk("Wintun"), []component{mk("goMod")})
	flat := strings.Join(strings.Fields(out), " ")
	for _, want := range []string{"THIRD_PARTY_NOTICES_RUST.md", "ships no OpenVPN", "## Trademarks", "Windows is a trademark", "goMod"} {
		if !strings.Contains(flat, want) {
			t.Errorf("the Windows notices lack %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, ".NET") || strings.Contains(out, "NuGet") || strings.Contains(out, "Source offer") {
		t.Errorf("the Windows notices mention .NET, NuGet or a source offer:\n%s", out)
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
