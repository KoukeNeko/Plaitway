package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
)

// clean replaces the control characters of text that came from the daemon
// before it reaches a terminal. Engine output, such as the reason an OpenVPN
// server gives for refusing a login, passes through the daemon unchanged, and
// an escape sequence in it would otherwise act on the terminal.
func clean(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return unicode.ReplacementChar
		}
		return r
	}, text)
}

// cleanText is clean for text of several lines: line feeds and tabs stay, and
// a CRLF line ending, which a profile written on Windows has, becomes a line
// feed instead of a replacement character at the end of every line.
func cleanText(text string) string {
	return strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return unicode.ReplacementChar
		}
		return r
	}, strings.ReplaceAll(text, "\r\n", "\n"))
}

// enumName words an enum value: PROFILE_STATE_AWAITING_CREDENTIALS with the
// prefix "PROFILE_STATE_" is "awaiting credentials".
func enumName(value fmt.Stringer, prefix string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(value.String(), prefix), "_", " "))
}

func stateName(s pb.ProfileState) string { return enumName(s, "PROFILE_STATE_") }

func kindName(k pb.ProfileKind) string {
	switch k {
	case pb.ProfileKind_PROFILE_KIND_OPENVPN:
		return "OpenVPN"
	case pb.ProfileKind_PROFILE_KIND_WIREGUARD:
		return "WireGuard"
	}
	return "unknown"
}

// dash stands for a value a profile does not have.
func dash(text string) string {
	if text == "" {
		return "-"
	}
	return text
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, suffix := float64(n), ""
	for _, s := range []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"} {
		value /= unit
		suffix = s
		if value < unit {
			break
		}
	}
	return fmt.Sprintf("%.1f %s", value, suffix)
}

// humanDuration is the two largest units of d: 45s, 3m12s, 2h5m, 4d3h.
func humanDuration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
}

const (
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiRed    = "\x1b[31m"
	ansiReset  = "\x1b[0m"
)

// paintState colors the word for a state when stdout is a terminal.
func (a *app) paintState(s pb.ProfileState, text string) string {
	if !a.color {
		return text
	}
	switch s {
	case pb.ProfileState_PROFILE_STATE_CONNECTED:
		return ansiGreen + text + ansiReset
	case pb.ProfileState_PROFILE_STATE_FAILED:
		return ansiRed + text + ansiReset
	case pb.ProfileState_PROFILE_STATE_CONNECTING, pb.ProfileState_PROFILE_STATE_RECONNECTING,
		pb.ProfileState_PROFILE_STATE_AWAITING_CREDENTIALS, pb.ProfileState_PROFILE_STATE_DISCONNECTING:
		return ansiYellow + text + ansiReset
	}
	return text
}

// table prints rows under a header, in columns padded to the widest cell. The
// last column is not padded. paint, when it is not nil, may decorate a cell
// after it has been measured.
func (a *app) table(header []string, rows [][]string, paint func(row, col int, cell string) string) {
	all := append([][]string{header}, rows...)
	widths := make([]int, len(header))
	for _, row := range all {
		for i, cell := range row {
			widths[i] = max(widths[i], utf8.RuneCountInString(clean(cell)))
		}
	}
	for r, row := range all {
		var line strings.Builder
		for i, cell := range row {
			cell = clean(cell)
			pad := strings.Repeat(" ", widths[i]-utf8.RuneCountInString(cell))
			if paint != nil && r > 0 {
				cell = paint(r-1, i, cell)
			}
			line.WriteString(cell)
			if i < len(row)-1 {
				line.WriteString(pad + "  ")
			}
		}
		fmt.Fprintln(a.stdout, strings.TrimRight(line.String(), " "))
	}
}

// keyValues prints "name  value" lines with the values aligned.
func keyValues(w io.Writer, pairs [][2]string) {
	width := 0
	for _, p := range pairs {
		width = max(width, len(p[0]))
	}
	for _, p := range pairs {
		fmt.Fprintf(w, "%-*s  %s\n", width, p[0], clean(p[1]))
	}
}

// printJSON prints a message as the daemon's API defines it, so that scripts
// see the field names of proto/plaitway/v1/plaitway.proto. Unset fields are
// printed too: a script should not have to guess whether a list is empty or
// missing.
func (a *app) printJSON(m proto.Message) error {
	compact, err := marshalJSON(m)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", "  "); err != nil {
		return err
	}
	_, err = fmt.Fprintf(a.stdout, "%s\n", out.Bytes())
	return err
}

// printJSONLine prints a message on one line, for streams.
func (a *app) printJSONLine(m proto.Message) error {
	compact, err := marshalJSON(m)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(a.stdout, "%s\n", compact)
	return err
}

// marshalJSON is the compact JSON of a message. protojson varies its white
// space at random to keep programs from depending on its exact output, so it
// goes through json.Compact for a stable result.
func marshalJSON(m proto.Message) ([]byte, error) {
	raw, err := protojson.MarshalOptions{EmitUnpopulated: true}.Marshal(m)
	if err != nil {
		return nil, err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, err
	}
	return compact.Bytes(), nil
}
