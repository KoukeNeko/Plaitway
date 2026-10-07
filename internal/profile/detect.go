package profile

import (
	"path/filepath"
	"strings"
	"unicode"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// openVPNDirectives are first words that only an OpenVPN profile uses.
var openVPNDirectives = map[string]bool{
	"client": true, "remote": true, "dev": true, "proto": true, "tls-client": true,
	"nobind": true, "ca": true, "cert": true, "key": true, "auth-user-pass": true,
	"<ca>": true, "<cert>": true, "<key>": true, "<tls-auth>": true, "<tls-crypt>": true,
}

// DetectKind tells the two profile formats apart. The content decides: a
// wg-quick file has an [Interface] section, an OpenVPN file uses OpenVPN
// directives. The filename only breaks the tie for a file with neither.
func DetectKind(sourceFilename string, content []byte) (tunnel.Kind, bool) {
	openVPN := false
	for line := range strings.Lines(string(content)) {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if strings.EqualFold(line, "[Interface]") {
			return tunnel.KindWireGuard, true
		}
		if fields := strings.Fields(line); openVPNDirectives[strings.ToLower(fields[0])] {
			openVPN = true
		}
	}
	if openVPN || strings.EqualFold(filepath.Ext(sourceFilename), ".ovpn") {
		return tunnel.KindOpenVPN, true
	}
	return 0, false
}

// importName picks the name of a new profile: the one given, else the source
// file's name without its extension, else a name found in the profile, else
// the kind's own name.
func importName(req ImportRequest, parsed tunnel.Parsed, kind tunnel.Kind) string {
	candidates := []string{req.Name, fileStem(req.SourceFilename), parsed.SuggestedName, displayName(kind)}
	for _, c := range candidates {
		if name := cleanName(c); name != "" {
			return name
		}
	}
	return "Profile"
}

// displayName is how the kind is written in names and messages.
func displayName(kind tunnel.Kind) string {
	switch kind {
	case tunnel.KindOpenVPN:
		return "OpenVPN"
	case tunnel.KindWireGuard:
		return "WireGuard"
	}
	return ""
}

// fileStem is the base name without extension; the filename comes from the
// client, so both path separators are honoured.
func fileStem(filename string) string {
	filename = filename[strings.LastIndexAny(filename, `/\`)+1:]
	return strings.TrimSuffix(filename, filepath.Ext(filename))
}

// cleanName trims the name, drops control characters and invalid UTF-8 (which
// a protobuf string cannot carry) and cuts it to a length the UI can show.
func cleanName(name string) string {
	name = strings.ToValidUTF8(name, "")
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if runes := []rune(name); len(runes) > maxNameRunes {
		name = strings.TrimSpace(string(runes[:maxNameRunes]))
	}
	return name
}
