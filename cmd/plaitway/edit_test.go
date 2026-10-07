package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
)

const (
	// A WireGuard profile with secrets, for the fake backend, which does not
	// look at the keys.
	wgSecrets = "[Interface]\nPrivateKey = WGPRIVATESECRET\nAddress = 10.6.0.2/32\n" +
		"[Peer]\nPresharedKey = WGPRESHAREDSECRET\nEndpoint = 203.0.113.5:51820\nAllowedIPs = 0.0.0.0/0\n"
	ovpnSecrets = ovpnProfile + "<ca>\nCAPUBLIC\n</ca>\n<key>\nOVPNKEYSECRET\n</key>\n<tls-crypt>\nTLSCRYPTSECRET\n</tls-crypt>\n"
	// wgProfile has seven lines; the fake backend refuses the marker.
	rejectedText = wgProfile + "# fake: reject\n"
	rejectedLine = "line 8: rejected by"
)

// fakeEditor is an editor that a test scripts. Each time it runs it logs the
// text it was given, the file's path and the permissions of the file and of
// its directory, and then replaces the file with the next text in the queue, if
// there is one: the first run takes the first text.
type fakeEditor struct {
	dir     string
	command string // $EDITOR: the script, with the queue and the log as arguments
}

const fakeEditorScript = `#!/bin/sh
queue=$1; log=$2; file=$3
n=$(( $(cat "$log/count" 2>/dev/null || echo 0) + 1 ))
echo "$n" > "$log/count"
echo "$file" > "$log/path"
cp "$file" "$log/seen.$n"
{ ls -ld "$file"; ls -ld "$(dirname "$file")"; } | cut -c1-10 > "$log/modes.$n"
if [ -f "$queue/$n" ]; then cp "$queue/$n" "$file"; fi
`

func newFakeEditor(t *testing.T, replacements ...string) *fakeEditor {
	t.Helper()
	dir := shortDir(t)
	queue, logDir, script := filepath.Join(dir, "queue"), filepath.Join(dir, "log"), filepath.Join(dir, "editor.sh")
	for _, d := range []string{queue, logDir} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(script, []byte(fakeEditorScript), 0o700); err != nil {
		t.Fatal(err)
	}
	for i, text := range replacements {
		if err := os.WriteFile(filepath.Join(queue, strconv.Itoa(i+1)), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &fakeEditor{dir: dir, command: script + " " + queue + " " + logDir}
}

func (e *fakeEditor) logged(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.dir, "log", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// runs is how often the editor was started.
func (e *fakeEditor) runs(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.dir, "log", "count"))
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// saw is the text the editor was given in its nth run.
func (e *fakeEditor) saw(t *testing.T, n int) string { return e.logged(t, "seen."+strconv.Itoa(n)) }

// goneAfter checks that the file the editor was given is no longer there, nor
// the directory that held it.
func (e *fakeEditor) goneAfter(t *testing.T) {
	t.Helper()
	path := strings.TrimSpace(e.logged(t, "path"))
	for _, p := range []string{path, filepath.Dir(path)} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still there after the command ended (stat: %v)", p, err)
		}
	}
}

func withEditor(command string) func(*app) {
	return func(a *app) {
		inner := a.getenv
		a.getenv = func(name string) string {
			if name == "EDITOR" {
				return command
			}
			return inner(name)
		}
	}
}

// answering is a person at the terminal who gives the answers in turn and
// whose questions are collected in asked.
func answering(asked *[]string, answers ...string) func(*app) {
	return func(a *app) {
		a.prompt = func(_ context.Context, label string, secret bool) (string, error) {
			*asked = append(*asked, label)
			if secret || len(answers) == 0 {
				return "", errors.New("asked more than expected: " + label)
			}
			answer := answers[0]
			answers = answers[1:]
			return answer, nil
		}
	}
}

func tweaks(fs ...func(*app)) func(*app) {
	return func(a *app) {
		for _, f := range fs {
			f(a)
		}
	}
}

// storedText is what the daemon holds for a profile.
func (d *testDaemon) storedText(name string) string {
	d.t.Helper()
	return d.mustRun("", "show", "-secrets", name).stdout
}

func TestMaskSecrets(t *testing.T) {
	t.Parallel()
	cases := map[string]struct{ in, want string }{
		"private key":      {"PrivateKey = abc=\n", "PrivateKey = [hidden]\n"},
		"no spaces":        {"privatekey=abc", "privatekey= [hidden]"},
		"preshared key":    {"[Peer]\nPresharedKey = abc=\n", "[Peer]\nPresharedKey = [hidden]\n"},
		"comment on line":  {"  PresharedKey   =   xyz  # note\r\n", "  PresharedKey   = [hidden]\r\n"},
		"commented out":    {"# PrivateKey = old\n; privatekey = older\n", "# PrivateKey = [hidden]\n; privatekey = [hidden]\n"},
		"public values":    {"PublicKey = abc=\nEndpoint = h:1\nAddress = 10.0.0.2/32\n", "PublicKey = abc=\nEndpoint = h:1\nAddress = 10.0.0.2/32\n"},
		"openvpn key":      {"<key>\nSECRET\n</key>\n", "<key>\n[hidden]\n</key>\n"},
		"certificates":     {"<ca>\nPUBLIC\n</ca>\n<cert>\nPUBLIC==\n</cert>\n", "<ca>\nPUBLIC\n</ca>\n<cert>\nPUBLIC==\n</cert>\n"},
		"directive named":  {"key client.key\ntls-crypt file\n", "key client.key\ntls-crypt file\n"},
		"indented":         {"  <tls-crypt> # keep\nA\nB\n  </tls-crypt>\nremote h\n", "  <tls-crypt> # keep\n[hidden]\n  </tls-crypt>\nremote h\n"},
		"upper case":       {"<KEY>\nS\n</KEY>\n", "<KEY>\n[hidden]\n</KEY>\n"},
		"never closed":     {"<key>\nS\nremote h\n", "<key>\n[hidden]\n"},
		"never closed, no": {"<key>", "<key>\n[hidden]\n"},
		"in a connection":  {"<connection>\nremote h\n<key>\nS\n</key>\n</connection>\n", "<connection>\nremote h\n<key>\n[hidden]\n</key>\n</connection>\n"},
		"crlf":             {"<key>\r\nS\r\n</key>\r\nremote h\r\n", "<key>\r\n[hidden]\r\n</key>\r\nremote h\r\n"},
		"pem key in a certificate block": {
			"<cert>\n-----BEGIN CERTIFICATE-----\nCERT\n-----END CERTIFICATE-----\n-----BEGIN PRIVATE KEY-----\nSECRET\n-----END PRIVATE KEY-----\n</cert>\n",
			"<cert>\n-----BEGIN CERTIFICATE-----\nCERT\n-----END CERTIFICATE-----\n-----BEGIN PRIVATE KEY-----\n[hidden]\n-----END PRIVATE KEY-----\n</cert>\n",
		},
		"pem key kinds": {
			"-----BEGIN RSA PRIVATE KEY-----\nA\n-----END RSA PRIVATE KEY-----\n-----BEGIN ENCRYPTED PRIVATE KEY-----\nB\n-----END ENCRYPTED PRIVATE KEY-----\n-----BEGIN OpenVPN Static key V1-----\nC\n-----END OpenVPN Static key V1-----\n",
			"-----BEGIN RSA PRIVATE KEY-----\n[hidden]\n-----END RSA PRIVATE KEY-----\n-----BEGIN ENCRYPTED PRIVATE KEY-----\n[hidden]\n-----END ENCRYPTED PRIVATE KEY-----\n-----BEGIN OpenVPN Static key V1-----\n[hidden]\n-----END OpenVPN Static key V1-----\n",
		},
		"pem public key": {"-----BEGIN PUBLIC KEY-----\nPUB\n-----END PUBLIC KEY-----\n", "-----BEGIN PUBLIC KEY-----\nPUB\n-----END PUBLIC KEY-----\n"},
		"two blocks":     {"<key>\nA\n</key>\n<tls-auth>\nB\n</tls-auth>\n", "<key>\n[hidden]\n</key>\n<tls-auth>\n[hidden]\n</tls-auth>\n"},
	}
	for name, c := range cases {
		if got := maskSecrets(c.in); got != c.want {
			t.Errorf("%s: maskSecrets(%q) = %q, want %q", name, c.in, got, c.want)
		}
	}
	for tag := range secretBlocks {
		in := "<" + tag + ">\nSECRET\n</" + tag + ">\n"
		if got := maskSecrets(in); strings.Contains(got, "SECRET") {
			t.Errorf("the body of <%s> is shown: %q", tag, got)
		}
	}
	for _, tag := range []string{"key", "tls-auth", "tls-crypt", "tls-crypt-v2", "pkcs12", "secret"} {
		if !secretBlocks[tag] {
			t.Errorf("<%s> is not hidden", tag)
		}
	}
}

func TestCleanTextKeepsTheLinesOfATextOnly(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"a\tb\nc\n":           "a\tb\nc\n",
		"a\r\nb\r\n":          "a\nb\n",
		"a\rb":                "a\uFFFDb",
		"x\x1b[2Jy\x07":       "x\uFFFD[2Jy\uFFFD",
		"c1 \u009b control":   "c1 \uFFFD control",
		"\u4e2d\u6587 text\n": "\u4e2d\u6587 text\n",
	} {
		if got := cleanText(in); got != want {
			t.Errorf("cleanText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShow(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	d.importText("wg", wgSecrets)
	d.importText("ovpn", ovpnSecrets)

	t.Run("secrets are hidden", func(t *testing.T) {
		wg := d.mustRun("", "show", "wg")
		want := strings.NewReplacer("WGPRIVATESECRET", "[hidden]", "WGPRESHAREDSECRET", "[hidden]").Replace(wgSecrets)
		if wg.stdout != want || wg.stderr != "" {
			t.Errorf("show wg: %+v\nwant stdout %q", wg, want)
		}
		ovpn := d.mustRun("", "show", "ovpn").stdout
		want = strings.NewReplacer("OVPNKEYSECRET", "[hidden]", "TLSCRYPTSECRET", "[hidden]").Replace(ovpnSecrets)
		if ovpn != want {
			t.Errorf("show ovpn: %q, want %q", ovpn, want)
		}
	})

	t.Run("-secrets prints the stored text", func(t *testing.T) {
		if got := d.mustRun("", "show", "-secrets", "wg").stdout; got != wgSecrets {
			t.Errorf("show -secrets wg: %q", got)
		}
		// Flags are accepted after the profile too.
		if got := d.mustRun("", "show", "ovpn", "-secrets").stdout; got != ovpnSecrets {
			t.Errorf("show ovpn -secrets: %q", got)
		}
	})

	t.Run("unknown profile", func(t *testing.T) {
		if r := d.run("", "show", "nosuch"); r.code != exitFailure || r.stdout != "" || !strings.Contains(r.stderr, `no profile named "nosuch"`) {
			t.Errorf("show nosuch: %+v", r)
		}
	})
}

func TestShowAndEditWantTheTextAndOpenNothingWithoutIt(t *testing.T) {
	t.Parallel()
	editor := newFakeEditor(t, "changed\n")
	socket := (&scripted{
		list: func() ([]*pb.Profile, error) {
			return []*pb.Profile{profileOf("ID", "home", pb.ProfileState_PROFILE_STATE_DISCONNECTED)}, nil
		},
		getContent: func(*pb.GetProfileContentRequest) (*pb.GetProfileContentResponse, error) {
			return nil, status.Error(codes.PermissionDenied, "uid 501 is not an administrator")
		},
	}).serve(t)

	for _, command := range []string{"show", "edit"} {
		r := runAt(t, socket, "", withEditor(editor.command), command, "home")
		if r.code != exitFailure || r.stdout != "" || r.stderr != "plaitway: permission denied: uid 501 is not an administrator\n" {
			t.Errorf("%s: %+v", command, r)
		}
	}
	if n := editor.runs(t); n != 0 {
		t.Errorf("the editor ran %d times without a text to edit", n)
	}
}

func TestShowTextCannotActOnTheTerminal(t *testing.T) {
	t.Parallel()
	socket := (&scripted{
		list: func() ([]*pb.Profile, error) {
			return []*pb.Profile{profileOf("ID", "home", pb.ProfileState_PROFILE_STATE_DISCONNECTED)}, nil
		},
		getContent: func(*pb.GetProfileContentRequest) (*pb.GetProfileContentResponse, error) {
			return &pb.GetProfileContentResponse{Content: []byte("[Interface]\r\n# \x1b]0;title\x07\r\nAddress = 10.0.0.2/32\r\n")}, nil
		},
	}).serve(t)
	if r := runAt(t, socket, "", nil, "show", "home"); r.code != 0 || r.stdout != "[Interface]\n# �]0;title�\nAddress = 10.0.0.2/32\n" {
		t.Errorf("show: %+v", r)
	}
}

func TestEditSavesTheChangedText(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	d.importText("home", wgSecrets)
	d.importText("work", ovpnProfile)
	keyBefore := d.profile("home").Summary.PublicKey
	if keyBefore == "" {
		t.Fatal("the profile has no public key")
	}
	changed := strings.Replace(wgSecrets, "WGPRIVATESECRET", "ANOTHERPRIVATESECRET", 1)

	editor := newFakeEditor(t, changed, ovpnProfile+"route 10.9.0.0 255.255.0.0\n")
	r := d.runWith(context.Background(), "", withEditor(editor.command), "edit", "home")
	if r.code != 0 || r.stdout != "updated home\n" || r.stderr != "" {
		t.Errorf("edit: %+v", r)
	}
	if got := d.storedText("home"); got != changed {
		t.Errorf("stored text %q, want %q", got, changed)
	}
	if keyAfter := d.profile("home").Summary.PublicKey; keyAfter == "" || keyAfter == keyBefore {
		t.Errorf("the public key is %q after the private key changed from the one behind %q", keyAfter, keyBefore)
	}

	// The editor is given the text with its secrets, in a private file of a
	// private directory, named for the kind of profile; none of it stays behind.
	if got := editor.saw(t, 1); got != wgSecrets {
		t.Errorf("the editor was given %q, want %q", got, wgSecrets)
	}
	if got := editor.logged(t, "modes.1"); got != "-rw-------\ndrwx------\n" {
		t.Errorf("modes of the file and its directory: %q, want -rw------- and drwx------", got)
	}
	if ext := filepath.Ext(strings.TrimSpace(editor.logged(t, "path"))); ext != ".conf" {
		t.Errorf("a WireGuard profile is edited as a %q file", ext)
	}
	editor.goneAfter(t)

	if r := d.runWith(context.Background(), "", withEditor(editor.command), "edit", "work"); r.code != 0 || r.stdout != "updated work\n" {
		t.Errorf("edit work: %+v", r)
	}
	if ext := filepath.Ext(strings.TrimSpace(editor.logged(t, "path"))); ext != ".ovpn" {
		t.Errorf("an OpenVPN profile is edited as a %q file", ext)
	}
}

func TestEditCommandMayCarryArgumentsAndVisualWins(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		visual, editor string
		want           []string
		wantErr        bool
	}{
		"visual first":    {"code --wait", "nano", []string{"code", "--wait"}, false},
		"editor":          {"", "nano -w", []string{"nano", "-w"}, false},
		"neither":         {"", "", []string{"vi"}, false},
		"blank variables": {"  ", "\t", []string{"vi"}, false},
		"quoted":          {`"/Applications/My Editor/ed" --new-window`, "", []string{"/Applications/My Editor/ed", "--new-window"}, false},
		"nothing in it":   {"# nothing", "", nil, true},
	} {
		getenv := func(name string) string {
			return map[string]string{"VISUAL": c.visual, "EDITOR": c.editor}[name]
		}
		got, err := editorCommand(getenv)
		if (err != nil) != c.wantErr || !slices.Equal(got, c.want) {
			t.Errorf("%s: %q, %v; want %q (error %v)", name, got, err, c.want, c.wantErr)
		}
	}
}

func TestEditUnchangedFileSendsNothing(t *testing.T) {
	t.Parallel()
	editor := newFakeEditor(t) // leaves the file alone
	var sent atomic.Int32
	socket := (&scripted{
		list: func() ([]*pb.Profile, error) {
			return []*pb.Profile{profileOf("ID", "home", pb.ProfileState_PROFILE_STATE_DISCONNECTED)}, nil
		},
		getContent: func(*pb.GetProfileContentRequest) (*pb.GetProfileContentResponse, error) {
			return &pb.GetProfileContentResponse{Content: []byte(wgSecrets)}, nil
		},
		updateContent: func(*pb.UpdateProfileContentRequest) (*pb.ImportProfileResponse, error) {
			sent.Add(1)
			return nil, status.Error(codes.Internal, "nothing should have been sent")
		},
	}).serve(t)

	r := runAt(t, socket, "", withEditor(editor.command), "edit", "home")
	if r.code != 0 || r.stdout != "home: unchanged\n" || r.stderr != "" {
		t.Errorf("edit: %+v", r)
	}
	if sent.Load() != 0 || editor.runs(t) != 1 {
		t.Errorf("%d updates sent after %d editor runs", sent.Load(), editor.runs(t))
	}
	editor.goneAfter(t)
}

func TestEditSendsTheTextAndWhetherToReconnect(t *testing.T) {
	t.Parallel()
	for _, reconnect := range []bool{false, true} {
		editor := newFakeEditor(t, "changed\n")
		var (
			mu  sync.Mutex
			got *pb.UpdateProfileContentRequest
		)
		socket := (&scripted{
			list: func() ([]*pb.Profile, error) {
				return []*pb.Profile{profileOf("ID", "home", pb.ProfileState_PROFILE_STATE_CONNECTED)}, nil
			},
			getContent: func(*pb.GetProfileContentRequest) (*pb.GetProfileContentResponse, error) {
				return &pb.GetProfileContentResponse{Content: []byte("before\n")}, nil
			},
			updateContent: func(req *pb.UpdateProfileContentRequest) (*pb.ImportProfileResponse, error) {
				mu.Lock()
				defer mu.Unlock()
				got = req
				p := profileOf("ID", "home", pb.ProfileState_PROFILE_STATE_CONNECTED)
				p.DesiredEnabled = true
				return &pb.ImportProfileResponse{Profile: p}, nil
			},
		}).serve(t)

		args := []string{"edit", "home"}
		want := "updated home (applies on the next connection)\n"
		if reconnect {
			args = []string{"edit", "-reconnect", "home"}
			want = "updated home\n"
		}
		r := runAt(t, socket, "", withEditor(editor.command), args...)
		if r.code != 0 || r.stdout != want {
			t.Errorf("reconnect=%v: %+v, want stdout %q", reconnect, r, want)
		}
		mu.Lock()
		if got == nil || got.Id != "ID" || string(got.Content) != "changed\n" || got.Reconnect != reconnect {
			t.Errorf("reconnect=%v: the daemon was sent %v", reconnect, got)
		}
		mu.Unlock()
	}
}

func TestEditReconnectRestartsAnEnabledProfile(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	d.importText("home", wgSecrets)
	d.mustRun("", "connect", "home")
	starts := func() int { return strings.Count(d.mustRun("", "logs", "home").stdout, "starting") }
	if n := starts(); n != 1 {
		t.Fatalf("%d starts after connect", n)
	}

	editor := newFakeEditor(t, wgProfile+"# first\n", wgProfile+"# second\n")
	r := d.runWith(context.Background(), "", withEditor(editor.command), "edit", "home")
	if r.code != 0 || r.stdout != "updated home (applies on the next connection)\n" {
		t.Errorf("edit: %+v", r)
	}
	if n := starts(); n != 1 {
		t.Errorf("the tunnel was restarted (%d starts) although -reconnect was not given", n)
	}

	r = d.runWith(context.Background(), "", withEditor(editor.command), "edit", "-reconnect", "home")
	if r.code != 0 || r.stdout != "updated home\n" {
		t.Errorf("edit -reconnect: %+v", r)
	}
	if n := starts(); n != 2 {
		t.Errorf("%d starts after edit -reconnect, want 2", n)
	}
	d.waitState("home", "PROFILE_STATE_CONNECTED")
	if got := d.storedText("home"); got != wgProfile+"# second\n" {
		t.Errorf("stored text %q", got)
	}
}

func TestEditRefusedTextCanBeEditedAgainOrDropped(t *testing.T) {
	t.Parallel()
	corrected := wgProfile + "# corrected\n"

	t.Run("edited again and saved", func(t *testing.T) {
		t.Parallel()
		d := startDaemon(t)
		d.importText("home", wgProfile)
		editor := newFakeEditor(t, rejectedText, corrected)
		var asked []string
		r := d.runWith(context.Background(), "", tweaks(withEditor(editor.command), answering(&asked, "")), "edit", "home")
		if r.code != 0 || r.stdout != "updated home\n" || r.stderr != "rejected: "+rejectedLine+` "# fake: reject"`+"\n" {
			t.Errorf("edit: %+v", r)
		}
		if len(asked) != 1 || asked[0] != "Edit again or discard? [E/d] " {
			t.Errorf("asked %q", asked)
		}
		// The second round starts from what the person wrote, not from the stored text.
		if editor.runs(t) != 2 || editor.saw(t, 2) != rejectedText {
			t.Errorf("%d editor runs; the second was given %q", editor.runs(t), editor.saw(t, 2))
		}
		if got := d.storedText("home"); got != corrected {
			t.Errorf("stored text %q, want %q", got, corrected)
		}
		editor.goneAfter(t)
	})

	t.Run("dropped at the prompt", func(t *testing.T) {
		t.Parallel()
		d := startDaemon(t)
		d.importText("home", wgProfile)
		editor := newFakeEditor(t, rejectedText, corrected)
		var asked []string
		r := d.runWith(context.Background(), "", tweaks(withEditor(editor.command), answering(&asked, "d")), "edit", "home")
		if r.code != exitFailure || r.stdout != "" || !strings.Contains(r.stderr, "rejected: "+rejectedLine) ||
			!strings.HasSuffix(r.stderr, "plaitway: changes discarded\n") {
			t.Errorf("edit: %+v", r)
		}
		if editor.runs(t) != 1 || d.storedText("home") != wgProfile {
			t.Errorf("%d editor runs, stored text %q", editor.runs(t), d.storedText("home"))
		}
		editor.goneAfter(t)
	})

	t.Run("an unclear answer is asked again", func(t *testing.T) {
		t.Parallel()
		d := startDaemon(t)
		d.importText("home", wgProfile)
		editor := newFakeEditor(t, rejectedText, corrected)
		var asked []string
		r := d.runWith(context.Background(), "", tweaks(withEditor(editor.command), answering(&asked, "what", "EDIT")), "edit", "home")
		if r.code != 0 || len(asked) != 2 || d.storedText("home") != corrected {
			t.Errorf("edit: %+v, asked %q", r, asked)
		}
	})

	t.Run("end of input at the prompt drops it", func(t *testing.T) {
		t.Parallel()
		d := startDaemon(t)
		d.importText("home", wgProfile)
		editor := newFakeEditor(t, rejectedText)
		ended := func(a *app) {
			a.prompt = func(context.Context, string, bool) (string, error) { return "", io.EOF }
		}
		r := d.runWith(context.Background(), "", tweaks(withEditor(editor.command), ended), "edit", "home")
		if r.code != exitFailure || !strings.HasSuffix(r.stderr, "plaitway: changes discarded\n") || d.storedText("home") != wgProfile {
			t.Errorf("edit: %+v", r)
		}
	})

	t.Run("without a terminal the refused text is dropped", func(t *testing.T) {
		t.Parallel()
		d := startDaemon(t)
		d.importText("home", wgProfile)
		editor := newFakeEditor(t, rejectedText, corrected)
		r := d.runWith(context.Background(), "", withEditor(editor.command), "edit", "home")
		if r.code != exitFailure || r.stdout != "" ||
			r.stderr != "rejected: "+rejectedLine+` "# fake: reject"`+"\nplaitway: changes discarded\n" {
			t.Errorf("edit: %+v", r)
		}
		if editor.runs(t) != 1 || d.storedText("home") != wgProfile {
			t.Errorf("%d editor runs, stored text %q", editor.runs(t), d.storedText("home"))
		}
		editor.goneAfter(t)
	})

	t.Run("a text of the other kind", func(t *testing.T) {
		t.Parallel()
		d := startDaemon(t)
		d.importText("home", wgProfile)
		editor := newFakeEditor(t, ovpnProfile)
		r := d.runWith(context.Background(), "", withEditor(editor.command), "edit", "home")
		if r.code != exitFailure || !strings.Contains(r.stderr, "rejected: this profile is WireGuard, but the text is OpenVPN") ||
			d.storedText("home") != wgProfile {
			t.Errorf("edit: %+v", r)
		}
	})

	t.Run("a file that is no text", func(t *testing.T) {
		t.Parallel()
		d := startDaemon(t)
		d.importText("home", wgProfile)
		editor := newFakeEditor(t, "[Interface]\x00\n")
		var asked []string
		r := d.runWith(context.Background(), "", tweaks(withEditor(editor.command), answering(&asked, "d")), "edit", "home")
		if r.code != exitFailure || !strings.Contains(r.stderr, "rejected: ") || !strings.Contains(r.stderr, "is not a text file") || len(asked) != 1 {
			t.Errorf("edit: %+v, asked %q", r, asked)
		}
		editor.goneAfter(t)
	})
}

func TestEditPrintsWhatTheDaemonRemoved(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	d.importText("home", wgProfile)
	editor := newFakeEditor(t, wgProfile+"PostUp = /bin/hook\n")
	r := d.runWith(context.Background(), "", withEditor(editor.command), "edit", "home")
	if r.code != 0 || r.stdout != "updated home\n" || r.stderr != "warning: line 8: PostUp: removed, a profile cannot run programs\n" {
		t.Errorf("edit: %+v", r)
	}
	if got := d.storedText("home"); got != wgProfile {
		t.Errorf("stored text %q", got)
	}
}

func TestEditFailures(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	d.importText("home", wgProfile)

	t.Run("an editor that fails changes nothing", func(t *testing.T) {
		dir := shortDir(t)
		script := filepath.Join(dir, "fail.sh")
		if err := os.WriteFile(script, []byte("#!/bin/sh\necho changed > \"$1\"\nexit 3\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		r := d.runWith(context.Background(), "", withEditor(script), "edit", "home")
		if r.code != exitFailure || !strings.Contains(r.stderr, "editor "+script+": exit status 3") || d.storedText("home") != wgProfile {
			t.Errorf("edit: %+v", r)
		}
	})

	t.Run("an editor that does not exist", func(t *testing.T) {
		r := d.runWith(context.Background(), "", withEditor(filepath.Join(shortDir(t), "no-such-editor")), "edit", "home")
		if r.code != exitFailure || !strings.Contains(r.stderr, "no-such-editor") {
			t.Errorf("edit: %+v", r)
		}
	})

	t.Run("unknown profile", func(t *testing.T) {
		editor := newFakeEditor(t, "x\n")
		r := d.runWith(context.Background(), "", withEditor(editor.command), "edit", "nosuch")
		if r.code != exitFailure || !strings.Contains(r.stderr, `no profile named "nosuch"`) || editor.runs(t) != 0 {
			t.Errorf("edit: %+v", r)
		}
	})

	t.Run("a profile and nothing else", func(t *testing.T) {
		if r := d.run("", "edit"); r.code != exitUsage {
			t.Errorf("edit without a profile: %+v", r)
		}
	})
}

// Whatever ends the command, the text of the profile is not left in a file.
func TestEditRemovesItsFileWhenTheCommandIsStopped(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	d.importText("home", wgSecrets)

	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		dir := shortDir(t)
		started := filepath.Join(dir, "started")
		script := filepath.Join(dir, "hang.sh")
		// exec makes the editor the process that $$ names, so that the test
		// can tell whether it was stopped.
		body := "#!/bin/sh\necho \"$1 $$\" > \"" + started + ".tmp\" && mv \"" + started + ".tmp\" \"" + started + "\"\nexec sleep 60\n"
		if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(clientBinary, "edit", "home")
		cmd.Env = []string{"PLAITWAY_SOCKET=" + d.socket, "EDITOR=" + script}
		var stderr syncBuffer
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		var path string
		var editorPID int
		deadline := time.Now().Add(waitTimeout)
		for path == "" {
			if data, err := os.ReadFile(started); err == nil {
				fields := strings.Fields(string(data))
				editorPID, _ = strconv.Atoi(fields[1])
				path = fields[0]
			} else if time.Now().After(deadline) {
				cmd.Process.Kill()
				t.Fatalf("%v: the editor did not start; stderr %q", sig, stderr.String())
			} else {
				time.Sleep(20 * time.Millisecond)
			}
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%v: the file is not there while the editor runs: %v", sig, err)
		}

		if err := cmd.Process.Signal(sig); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != exitFailure {
				t.Errorf("%v: the command ended with %v, want exit status %d", sig, err, exitFailure)
			}
		case <-time.After(waitTimeout):
			cmd.Process.Kill()
			t.Fatalf("%v: the command did not end", sig)
		}

		for _, p := range []string{path, filepath.Dir(path)} {
			if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%v: %s is still there (stat: %v)", sig, p, err)
			}
		}
		if err := syscall.Kill(editorPID, 0); !errors.Is(err, syscall.ESRCH) {
			syscall.Kill(editorPID, syscall.SIGKILL)
			t.Errorf("%v: the editor is still running (kill 0: %v)", sig, err)
		}
		if !strings.Contains(stderr.String(), "interrupted") {
			t.Errorf("%v: stderr %q", sig, stderr.String())
		}
		if got := d.storedText("home"); got != wgSecrets {
			t.Errorf("%v: stored text %q", sig, got)
		}
	}
}

func TestHelpListsTheEditingCommands(t *testing.T) {
	t.Parallel()
	r := runAt(t, "", "", nil, "help")
	for _, want := range []string{"show profile", "edit profile", "set profile"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("help lacks %q:\n%s", want, r.stdout)
		}
	}
}
