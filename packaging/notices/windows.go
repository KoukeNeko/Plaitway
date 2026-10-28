package main

// The notices of the Windows installer (go run ./packaging/notices -windows).
//
// They list what the installed files are made of, from the same kind of data
// as the macOS document:
//
//   - the Go modules compiled into plaitwayd.exe and plaitway.exe (go list for
//     windows, as the macOS document does for darwin);
//   - Wintun, whose notice and license text the repository keeps next to its
//     pin;
//   - OpenVPN with its TAP-Windows6 driver and libraries, as pinned in
//     packaging/windows/openvpn/openvpn.json, with the license.txt of the
//     OpenVPN installer that fetch-openvpn.ps1 laid out in the payload;
//   - a pointer to THIRD_PARTY_NOTICES_RUST.md, which lists the crates of the
//     Rust app.
//
// OpenVPN, the driver and LZO are GPLv2, and the installer ships them as the
// unmodified binaries of the OpenVPN project. The document carries a written
// offer (GPLv2 section 3(b)) that names the upstream source of each.

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	wintunPinPath    = "packaging/windows/wintun/wintun.json"
	wintunNoticePath = "packaging/windows/wintun/THIRD_PARTY.txt"
	openvpnPinPath   = "packaging/windows/openvpn/openvpn.json"
)

// windowsInputs is where the Windows document finds its data.
type windowsInputs struct {
	goarch  string
	openvpn string // the openvpn folder of the payload; its license.txt is the text
	contact string
}

func windowsFlags() *windowsInputs {
	inputs := &windowsInputs{}
	flag.StringVar(&inputs.goarch, "goarch", "amd64", "with -windows: amd64 or arm64")
	flag.StringVar(&inputs.openvpn, "openvpn", "build/windows/payload/openvpn", "with -windows: the openvpn folder of the payload (fetch-openvpn.ps1), for its license.txt")
	flag.StringVar(&inputs.contact, "windows-contact", os.Getenv("PLAITWAY_SOURCE_CONTACT"), "with -windows: where the written source offer says to ask (default: $PLAITWAY_SOURCE_CONTACT, else the https URL of the go.mod module)")
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
	goMods, err := goComponentsFor(root, goTarget{"windows", inputs.goarch, "0"})
	if err != nil {
		return "", err
	}
	wintun, err := wintunComponent(root)
	if err != nil {
		return "", err
	}
	openvpn, err := openvpnComponents(root, inputs.openvpn)
	if err != nil {
		return "", err
	}
	contact := inputs.contact
	if contact == "" {
		if contact, err = moduleURL(root); err != nil {
			return "", err
		}
	}
	return renderWindows(wintun, goMods, openvpn, contact), nil
}

// openvpnComponent is a piece of the OpenVPN package, as openvpn.json lists it.
type openvpnComponent struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	License string `json:"license"`
	Source  string `json:"source"`
	GPL     bool   `json:"gpl"`
}

type openvpnPin struct {
	Version    string             `json:"version"`
	Components []openvpnComponent `json:"components"`
}

// openvpn is what the package ships of OpenVPN: its components, and the license.txt of
// the official installer, which holds the license of every one of them.
type openvpn struct {
	version    string
	components []openvpnComponent
	license    document
}

// openvpnComponents reads the pin of the shipped OpenVPN and the license.txt that the fetch put in the payload.
func openvpnComponents(root, folder string) (openvpn, error) {
	pinData, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(openvpnPinPath)))
	if err != nil {
		return openvpn{}, err
	}
	var pin openvpnPin
	if err := json.Unmarshal(pinData, &pin); err != nil {
		return openvpn{}, fmt.Errorf("%s: %w", openvpnPinPath, err)
	}
	if len(pin.Components) == 0 {
		return openvpn{}, fmt.Errorf("%s lists no components", openvpnPinPath)
	}
	if !filepath.IsAbs(folder) {
		folder = filepath.Join(root, filepath.FromSlash(folder))
	}
	text, err := os.ReadFile(filepath.Join(folder, "license.txt"))
	if err != nil {
		return openvpn{}, fmt.Errorf("%w (run packaging/windows/fetch-openvpn.ps1 first)", err)
	}
	return openvpn{
		version: pin.Version, components: pin.Components,
		license: document{"License texts of the OpenVPN installer (license.txt)", string(text)},
	}, nil
}

// renderWindows is the document of the installer: the programs and Go modules of the
// package, and a pointer to the Rust crates' own file.
func renderWindows(wintun component, goMods []component, vpn openvpn, contact string) string {
	var b strings.Builder
	b.WriteString(`# Third-party notices

Plaitway for Windows contains the software listed below. This file is generated
by packaging/notices from the upstream sources; do not edit it by hand.

The installer ships OpenVPN, unmodified as the OpenVPN project builds and signs it,
with its TAP-Windows6 driver, in the openvpn folder. OpenVPN, the driver and LZO
are free software under the GNU General Public License version 2: see "Source
offer" below.

The Rust crates the app is built from are listed, with their licence texts, in
THIRD_PARTY_NOTICES_RUST.md beside this file.

## Bundled programs

`)
	writeTable(&b, []component{wintun})
	b.WriteString("\n## OpenVPN " + vpn.version + " and its libraries (openvpn folder)\n\n")
	b.WriteString("| Component | Version | License | Source |\n|---|---|---|---|\n")
	for _, c := range vpn.components {
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", c.Name, c.Version, c.License, c.Source)
	}
	b.WriteString("\n## Go modules compiled into plaitwayd and plaitway\n\n")
	writeTable(&b, goMods)
	b.WriteString(trademarksSection(`
Windows is a trademark of the Microsoft group of companies. Wintun is a project
of WireGuard LLC.
`))
	writeWindowsSourceOffer(&b, vpn, contact)
	b.WriteString("\n## License text of OpenVPN and its libraries\n\n```text\n" + normalize(vpn.license.body) + "\n```\n")
	writeTexts(&b, append([]component{wintun}, goMods...))
	return b.String()
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

// writeWindowsSourceOffer is the GPLv2 section 3(b) offer for the GPL programs of the openvpn folder. They are the
// unmodified binaries of the OpenVPN project, so the corresponding source is the project's own release of each.
func writeWindowsSourceOffer(b *strings.Builder, vpn openvpn, contact string) {
	b.WriteString("\n## Source offer\n\n")
	b.WriteString(`The programs of the openvpn folder that are licensed under the GNU General Public
License version 2 (GPLv2) are the unmodified binaries that the OpenVPN project
builds and signs (OpenVPN ` + vpn.version + `, installer OpenVPN-` + vpn.version + `-I001-amd64.msi). Plaitway does not
change them. The corresponding source code is the release of each, at:

`)
	for _, c := range vpn.components {
		if c.GPL {
			fmt.Fprintf(b, "- %s %s: %s\n", c.Name, c.Version, c.Source)
		}
	}
	fmt.Fprintf(b, `
The scripts that control the compilation of the Windows installer of OpenVPN are
in https://github.com/OpenVPN/openvpn-build.

Written offer, GPLv2 section 3(b): for three years from the day you received
this copy of Plaitway, you may obtain a complete machine-readable copy of the
source code listed above from
%s
for a charge no more than the cost of physically performing the distribution.
`, contact)
}