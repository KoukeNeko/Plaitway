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

func TestRenderWindowsShipsOpenVPNWithASourceOfferAndPointsAtTheCrateNotices(t *testing.T) {
	mk := func(name string) component {
		return component{name: name, version: "1", license: "MIT", source: "src", docs: []document{{"MIT", mitText}}}
	}
	vpn := openvpn{
		version: "2.7.1",
		components: []openvpnComponent{
			{Name: "OpenVPN", Version: "2.7.1", License: "GPL-2.0", Source: "https://example.org/openvpn.tar.gz", GPL: true},
			{Name: "OpenSSL", Version: "3.6.1", License: "Apache-2.0", Source: "https://example.org/openssl", GPL: false},
		},
		license: document{"OpenVPN licenses", "THE LICENSE TEXT OF OPENVPN"},
	}
	out := renderWindows(mk("Wintun"), []component{mk("goMod")}, vpn, "https://example.org/contact")
	flat := strings.Join(strings.Fields(out), " ")
	for _, want := range []string{"THIRD_PARTY_NOTICES_RUST.md", "## Trademarks", "Windows is a trademark", "goMod",
		"## Source offer", "Written offer, GPLv2 section 3(b)", "https://example.org/contact",
		"- OpenVPN 2.7.1: https://example.org/openvpn.tar.gz", "THE LICENSE TEXT OF OPENVPN", "| OpenSSL | 3.6.1 | Apache-2.0 |"} {
		if !strings.Contains(flat, want) {
			t.Errorf("the Windows notices lack %q:\n%s", want, out)
		}
	}
	if strings.Contains(flat, "- OpenSSL 3.6.1:") {
		t.Errorf("the source offer lists OpenSSL, which is not GPL:\n%s", out)
	}
	if strings.Contains(out, ".NET") || strings.Contains(out, "NuGet") || strings.Contains(flat, "ships no OpenVPN") {
		t.Errorf("the Windows notices mention .NET, NuGet or an OpenVPN that is not shipped:\n%s", out)
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
