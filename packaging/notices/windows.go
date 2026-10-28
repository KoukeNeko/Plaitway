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
//   - a pointer to THIRD_PARTY_NOTICES_RUST.md, which lists the crates of the
//     Rust app.
//
// There is no source offer: the installer ships no GPL program. OpenVPN is the
// user's own installation, which the daemon finds (packaging/windows/README.md).

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
)

// windowsInputs is where the Windows document finds its data.
type windowsInputs struct {
	goarch string
}

func windowsFlags() *windowsInputs {
	inputs := &windowsInputs{}
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
	goMods, err := goComponentsFor(root, goTarget{"windows", inputs.goarch, "0"})
	if err != nil {
		return "", err
	}
	wintun, err := wintunComponent(root)
	if err != nil {
		return "", err
	}
	return renderWindows(wintun, goMods), nil
}

// renderWindows is the document of the installer: the programs and Go modules of the
// package, and a pointer to the Rust crates' own file.
func renderWindows(wintun component, goMods []component) string {
	var b strings.Builder
	b.WriteString(`# Third-party notices

Plaitway for Windows contains the software listed below. This file is generated
by packaging/notices from the upstream sources; do not edit it by hand.

The installer ships no OpenVPN. The helper runs the OpenVPN that is installed on
the computer, if there is one.

The Rust crates the app is built from are listed, with their licence texts, in
THIRD_PARTY_NOTICES_RUST.md beside this file.

## Bundled programs

`)
	writeTable(&b, []component{wintun})
	b.WriteString("\n## Go modules compiled into plaitwayd and plaitway\n\n")
	writeTable(&b, goMods)
	b.WriteString(trademarksSection(`
Windows is a trademark of the Microsoft group of companies. Wintun is a project
of WireGuard LLC.
`))
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
