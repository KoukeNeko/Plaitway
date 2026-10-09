package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectLicense(t *testing.T) {
	tests := []struct {
		name, text, want string
		wantErr          bool
	}{
		{name: "gpl2", text: "                    GNU GENERAL PUBLIC LICENSE\n                       Version 2, June 1991", want: "GPL-2.0"},
		{name: "apache", text: "Apache License\nVersion 2.0, January 2004", want: "Apache-2.0"},
		{name: "mit", text: "MIT License\n\nPermission is hereby granted, free of charge, to any person", want: "MIT"},
		{name: "bsd3", text: "Redistribution and use in source and binary forms, with or without\n* Neither the name of the copyright holder", want: "BSD-3-Clause"},
		{name: "bsd2", text: "Redistribution and use in source and binary forms, with or without\nmodification, are permitted", want: "BSD-2-Clause"},
		{name: "isc", text: "Permission to use, copy, modify, and/or distribute this software for any purpose", want: "ISC"},
		{name: "unknown", text: "All rights reserved.", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := detectLicense(tt.text)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("detectLicense() = %q, %v; want %q (error %v)", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func writeTarball(t *testing.T, files map[string]string) (path, sum string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "src.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(data)
	return path, hex.EncodeToString(h[:])
}

func TestTarballFileAndChecksum(t *testing.T) {
	path, sum := writeTarball(t, map[string]string{"pkg-1/COPYING": "license text"})

	if err := verifySHA256(path, sum); err != nil {
		t.Fatalf("verifySHA256 with the right sum: %v", err)
	}
	if err := verifySHA256(path, strings.Repeat("0", 64)); err == nil {
		t.Fatal("verifySHA256 accepted a wrong checksum")
	}
	got, err := tarballFile(path, "pkg-1/COPYING")
	if err != nil || got != "license text" {
		t.Fatalf("tarballFile() = %q, %v", got, err)
	}
	if _, err := tarballFile(path, "pkg-1/MISSING"); err == nil {
		t.Fatal("tarballFile found a file that is not in the archive")
	}
}

func TestDirectoryComponent(t *testing.T) {
	dir := t.TempDir()
	if _, err := directoryComponent("nolicense", "1.0", "src", dir); err == nil {
		t.Fatal("a directory without a license file must be an error")
	}
	mustWrite(t, filepath.Join(dir, "LICENSE"), "Permission is hereby granted, free of charge, to any person")
	mustWrite(t, filepath.Join(dir, "NOTICE.txt"), "attribution")
	c, err := directoryComponent("pkg", "1.0", "src", dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.license != "MIT" || len(c.docs) != 2 || c.docs[1].kind != "Notice" {
		t.Fatalf("directoryComponent() = %+v", c)
	}
}

func TestRenderListsEachTextOnce(t *testing.T) {
	mit := "Permission is hereby granted, free of charge, to any person"
	mk := func(name string) component {
		return component{name: name, version: "1", license: "MIT", source: "src", docs: []document{{"MIT", mit + "\r\n"}}}
	}
	out := render(nil, []component{mk("alpha"), mk("beta")}, nil, "https://example.org/plaitway")
	if n := strings.Count(out, "    "+mit); n != 1 {
		t.Fatalf("license text appears %d times, want 1", n)
	}
	if !strings.Contains(out, "Applies to: alpha, beta") {
		t.Fatalf("both users must be listed:\n%s", out)
	}
}

// offerComponents are the bundled programs as bundledComponents describes them.
func offerComponents() []component {
	mk := func(name, version, license, archive, sum string, shipped bool) component {
		return component{
			name: name, version: version, license: license,
			source:  "https://example.org/" + archive + " (SHA-256 " + sum + ")",
			archive: archive, sha256: sum, shipped: shipped,
		}
	}
	return []component{
		mk("OpenVPN", "2.7.7", "GPL-2.0 with OpenSSL and Apache-2.0 linking exceptions", "openvpn-2.7.7.tar.gz", "aa11", true),
		mk("OpenSSL", "3.5.9", "Apache-2.0", "openssl-3.5.9.tar.gz", "bb22", false),
		mk("LZO", "2.10", "GPL-2.0", "lzo-2.10.tar.gz", "cc33", true),
		mk("LZ4 (library)", "1.10.0", "BSD-2-Clause", "lz4-1.10.0.tar.gz", "dd44", true),
	}
}

func TestRenderSourceOffer(t *testing.T) {
	out := render(offerComponents(), nil, nil, "https://example.org/plaitway")
	offer := section(t, out, "## Source offer")

	for _, want := range []string{
		"Contents/Resources/Source",
		"openvpn-2.7.7.tar.gz", "lzo-2.10.tar.gz", "lz4-1.10.0.tar.gz",
		"aa11", "cc33", "dd44",
		"build-openvpn.sh", "lib.sh", "openvpn-deps.env",
		"section 3(a)", "section 3(b)", "three years",
		"https://example.org/plaitway",
		"https://example.org/openssl-3.5.9.tar.gz",
	} {
		if !strings.Contains(offer, want) {
			t.Errorf("the source offer lacks %q:\n%s", want, offer)
		}
	}
	// OpenSSL's archive is a URL, not a file in the bundle.
	if strings.Contains(offer, "- openssl-3.5.9.tar.gz") {
		t.Errorf("the offer lists the OpenSSL archive as shipped:\n%s", offer)
	}
}

// The Linux package bundles no program and has no Swift code: its notices say
// that OpenVPN comes from the distribution, and carry no source offer.
func TestRenderWithoutBundledPrograms(t *testing.T) {
	mit := "Permission is hereby granted, free of charge, to any person"
	mod := component{name: "alpha", version: "1", license: "MIT", source: "src", docs: []document{{"MIT", mit}}}
	out := render(nil, []component{mod}, nil, "")
	for _, want := range []string{"OpenVPN is not part of this package", "## Go modules compiled into plaitwayd and plaitway", "| alpha |", "## Trademarks", "Applies to: alpha"} {
		if !strings.Contains(out, want) {
			t.Errorf("the notices lack %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"## Source offer", "## Bundled programs", "## Swift packages"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("the notices have %q:\n%s", unwanted, out)
		}
	}
}

func TestGenerateRefusesAnUnknownPlatform(t *testing.T) {
	if _, err := generate(t.TempDir(), "", "", "", "windows"); err == nil {
		t.Fatal("generate() accepted the platform windows")
	}
}

func TestRenderTrademarks(t *testing.T) {
	out := render(offerComponents(), nil, nil, "https://example.org/plaitway")
	trademarks := section(t, out, "## Trademarks")
	for _, want := range []string{"OpenVPN is a registered trademark", "WireGuard is a registered trademark"} {
		if !strings.Contains(trademarks, want) {
			t.Errorf("the trademark notice lacks %q:\n%s", want, trademarks)
		}
	}
}

// section returns the text under a "## " heading, up to the next one, with
// every run of white space collapsed so that line wrapping does not matter.
func section(t *testing.T, doc, heading string) string {
	t.Helper()
	_, rest, ok := strings.Cut(doc, heading)
	if !ok {
		t.Fatalf("no %q in:\n%s", heading, doc)
	}
	body, _, _ := strings.Cut(rest, "\n## ")
	return strings.Join(strings.Fields(body), " ")
}

func TestModuleURL(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module github.com/example/plaitway\n\ngo 1.27\n")
	got, err := moduleURL(root)
	if err != nil || got != "https://github.com/example/plaitway" {
		t.Fatalf("moduleURL() = %q, %v", got, err)
	}

	mustWrite(t, filepath.Join(root, "go.mod"), "module plaitway\n")
	if _, err := moduleURL(root); err == nil {
		t.Fatal("a module path that is not a web address must be an error")
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
