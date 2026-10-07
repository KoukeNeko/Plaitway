package main

import (
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
)

func TestHumanBytes(t *testing.T) {
	t.Parallel()
	for in, want := range map[uint64]string{
		0:               "0 B",
		1023:            "1023 B",
		1024:            "1.0 KiB",
		1536:            "1.5 KiB",
		1<<20 - 1:       "1024.0 KiB",
		5 << 20:         "5.0 MiB",
		3 << 30:         "3.0 GiB",
		math.MaxUint64:  "16.0 EiB",
		1<<20 + 512<<10: "1.5 MiB",
	} {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestHumanDuration(t *testing.T) {
	t.Parallel()
	for in, want := range map[time.Duration]string{
		0:                                     "0s",
		400 * time.Millisecond:                "0s",
		45 * time.Second:                      "45s",
		59*time.Second + 600*time.Millisecond: "1m0s",
		3*time.Minute + 12*time.Second:        "3m12s",
		2*time.Hour + 5*time.Minute + 30*time.Second: "2h5m",
		26 * time.Hour:       "1d2h",
		100 * 24 * time.Hour: "100d0h",
	} {
		if got := humanDuration(in); got != want {
			t.Errorf("humanDuration(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestEnumNames(t *testing.T) {
	t.Parallel()
	for in, want := range map[pb.ProfileState]string{
		pb.ProfileState_PROFILE_STATE_CONNECTED:            "connected",
		pb.ProfileState_PROFILE_STATE_AWAITING_CREDENTIALS: "awaiting credentials",
		pb.ProfileState_PROFILE_STATE_RECONNECTING:         "reconnecting",
	} {
		if got := stateName(in); got != want {
			t.Errorf("stateName(%v) = %q, want %q", in, got, want)
		}
	}
	if kindName(pb.ProfileKind_PROFILE_KIND_OPENVPN) != "OpenVPN" || kindName(pb.ProfileKind_PROFILE_KIND_WIREGUARD) != "WireGuard" ||
		kindName(pb.ProfileKind_PROFILE_KIND_UNSPECIFIED) != "unknown" {
		t.Error("kindName")
	}
}

var ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")

func TestTableAlignsColumnsWhetherOrNotThePlainTextIsColored(t *testing.T) {
	t.Parallel()
	rows := [][]string{
		{"home", "connected", "10.6.0.2/32"},
		{"a longer name", "failed", ""},
		{"x", "connecting", "y"},
	}
	states := []pb.ProfileState{pb.ProfileState_PROFILE_STATE_CONNECTED, pb.ProfileState_PROFILE_STATE_FAILED, pb.ProfileState_PROFILE_STATE_CONNECTING}
	render := func(color bool) string {
		var out syncBuffer
		a := &app{stdout: &out, color: color}
		a.table([]string{"NAME", "STATE", "ADDRESSES"}, rows, func(row, col int, cell string) string {
			if col == 1 {
				return a.paintState(states[row], cell)
			}
			return cell
		})
		return out.String()
	}

	// The last column is not padded, so a line ends with its last text.
	want := "NAME           STATE       ADDRESSES\n" +
		"home           connected   10.6.0.2/32\n" +
		"a longer name  failed\n" +
		"x              connecting  y\n"
	plain := render(false)
	if plain != want {
		t.Errorf("plain table:\n%s\nwant:\n%s", plain, want)
	}

	colored := render(true)
	if !strings.Contains(colored, ansiGreen+"connected"+ansiReset) || !strings.Contains(colored, ansiRed+"failed"+ansiReset) ||
		!strings.Contains(colored, ansiYellow+"connecting"+ansiReset) {
		t.Errorf("colored table:\n%q", colored)
	}
	if ansi.ReplaceAllString(colored, "") != plain {
		t.Errorf("color changed the layout:\n%s\nvs\n%s", ansi.ReplaceAllString(colored, ""), plain)
	}
}

func TestCleanReplacesControlCharactersOnly(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"plain text, 台灣 and é": "plain text, 台灣 and é",
		"a\x1b[2Jb":            "a�[2Jb",
		"bell\x07":             "bell�",
		"line\nbreak\ttab":     "line�break�tab",
		"c1\u009b31m":          "c1�31m",
		"del\x7f":              "del�",
	} {
		if got := clean(in); got != want {
			t.Errorf("clean(%q) = %q, want %q", in, got, want)
		}
	}
}

// protojson varies its white space from run to run; the client's output must not.
func TestJSONOutputIsStable(t *testing.T) {
	t.Parallel()
	profile := &pb.Profile{Id: "ID", Name: "home", State: pb.ProfileState_PROFILE_STATE_CONNECTED,
		Status: &pb.TunnelStatus{InterfaceName: "utun4", ConnectedSince: timestamppb.New(time.Unix(1_700_000_000, 0))}}

	outputs := map[string]bool{}
	lines := map[string]bool{}
	for range 100 {
		var out, line syncBuffer
		a := &app{stdout: &out}
		if err := a.printJSON(&pb.ListProfilesResponse{Profiles: []*pb.Profile{profile}}); err != nil {
			t.Fatal(err)
		}
		outputs[out.String()] = true
		b := &app{stdout: &line}
		if err := b.printJSONLine(profile); err != nil {
			t.Fatal(err)
		}
		lines[line.String()] = true
	}
	if len(outputs) != 1 || len(lines) != 1 {
		t.Errorf("%d different outputs and %d different lines for the same message", len(outputs), len(lines))
	}
	for out := range outputs {
		var parsed struct {
			Profiles []struct{ ID, Name, State string }
		}
		if err := json.Unmarshal([]byte(out), &parsed); err != nil || len(parsed.Profiles) != 1 || parsed.Profiles[0].State != "PROFILE_STATE_CONNECTED" {
			t.Errorf("printJSON: %q, %v", out, err)
		}
		if !strings.Contains(out, "\n  \"profiles\": [\n") {
			t.Errorf("printJSON is not indented:\n%s", out)
		}
	}
	for line := range lines {
		if strings.Count(line, "\n") != 1 || !strings.HasPrefix(line, `{"id":"ID","name":"home"`) {
			t.Errorf("printJSONLine: %q", line)
		}
	}
}

func TestParseFlagsAnywhere(t *testing.T) {
	t.Parallel()
	type parsed struct {
		args    []string
		follow  bool
		lines   int
		socket  string
		wantErr string
	}
	for name, c := range map[string]struct {
		in   []string
		want parsed
	}{
		"flag after argument":    {[]string{"home", "-f"}, parsed{args: []string{"home"}, follow: true, lines: 200}},
		"flag before argument":   {[]string{"-f", "home"}, parsed{args: []string{"home"}, follow: true, lines: 200}},
		"value flag in between":  {[]string{"-f", "-n", "5", "home", "-socket", "/s"}, parsed{args: []string{"home"}, follow: true, lines: 5, socket: "/s"}},
		"double dash":            {[]string{"-n", "3", "--", "-f"}, parsed{args: []string{"-f"}, lines: 3}},
		"only the double dash":   {[]string{"--", "-f"}, parsed{args: []string{"-f"}, lines: 200}},
		"no arguments":           {nil, parsed{lines: 200}},
		"unknown flag":           {[]string{"-nope"}, parsed{wantErr: "logs: flag provided but not defined: -nope"}},
		"too many arguments":     {[]string{"a", "b"}, parsed{wantErr: "usage: plaitway logs [flags] [profile]"}},
		"flag without its value": {[]string{"home", "-n"}, parsed{wantErr: "logs: flag needs an argument: -n"}},
	} {
		a := &app{stdout: &syncBuffer{}, stderr: &syncBuffer{}, getenv: func(string) string { return "" }}
		fs := a.flagSet("logs")
		follow := fs.Bool("f", false, "")
		lines := fs.Int("n", defaultLogLines, "")
		args, err := a.parse(fs, c.in, 0, 1)
		if c.want.wantErr != "" {
			if err == nil || err.Error() != c.want.wantErr {
				t.Errorf("%s: error %v, want %q", name, err, c.want.wantErr)
			}
			if _, ok := err.(*usageError); !ok {
				t.Errorf("%s: %T is not a usage error", name, err)
			}
			continue
		}
		got := parsed{args: args, follow: *follow, lines: *lines, socket: a.socket}
		if err != nil || len(got.args) != len(c.want.args) || (len(got.args) > 0 && strings.Join(got.args, "|") != strings.Join(c.want.args, "|")) ||
			got.follow != c.want.follow || got.lines != c.want.lines || got.socket != c.want.socket {
			t.Errorf("%s: %+v, %v; want %+v", name, got, err, c.want)
		}
	}
}
