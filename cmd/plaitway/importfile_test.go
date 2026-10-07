package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/ovpn"
)

const (
	pemCA     = "-----BEGIN CERTIFICATE-----\nCA\n-----END CERTIFICATE-----"
	pemCert   = "-----BEGIN CERTIFICATE-----\nCERT\n-----END CERTIFICATE-----"
	pemKey    = "-----BEGIN PRIVATE KEY-----\nKEY\n-----END PRIVATE KEY-----"
	pemStatic = "-----BEGIN OpenVPN Static key V1-----\nSTATIC\n-----END OpenVPN Static key V1-----"
)

// profileDir is a directory with the files a profile may name.
func profileDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"ca.crt":        pemCA + "\n",
		"client.crt":    pemCert + "\r\n", // a file written on Windows
		"client.key":    "\n" + pemKey + "\n\n",
		"ta.key":        pemStatic + "\n",
		"my ca.crt":     pemCA + "\n",
		"sub/other.crt": pemCert + "\n",
		"readme.txt":    "not key material\n",
		"binary.crt":    "-----BEGIN CERTIFICATE-----\x00\n",
	} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestInlineFiles(t *testing.T) {
	t.Parallel()
	dir := profileDir(t)
	for name, c := range map[string]struct{ in, want string }{
		"files become blocks": {
			"ca ca.crt\ncert client.crt\nkey client.key\n",
			"<ca>\n" + pemCA + "\n</ca>\n<cert>\n" + pemCert + "\n</cert>\n<key>\n" + pemKey + "\n</key>\n",
		},
		"tls-auth keeps its direction": {
			"tls-auth ta.key 1\n",
			"<tls-auth>\n" + pemStatic + "\n</tls-auth>\nkey-direction 1\n",
		},
		"the direction is not repeated": {
			"key-direction 0\ntls-auth ta.key 1\n",
			"key-direction 0\n<tls-auth>\n" + pemStatic + "\n</tls-auth>\n",
		},
		"tls-auth without a direction": {
			"tls-auth ta.key\n",
			"<tls-auth>\n" + pemStatic + "\n</tls-auth>\n",
		},
		"the other directives with an inline form": {
			"crl-verify ca.crt\nextra-certs ca.crt\ntls-crypt ta.key\ntls-crypt-v2 ta.key\ndh ca.crt\n",
			"<crl-verify>\n" + pemCA + "\n</crl-verify>\n<extra-certs>\n" + pemCA + "\n</extra-certs>\n<tls-crypt>\n" + pemStatic +
				"\n</tls-crypt>\n<tls-crypt-v2>\n" + pemStatic + "\n</tls-crypt-v2>\n<dh>\n" + pemCA + "\n</dh>\n",
		},
		"subdirectory, quotes, tabs and case": {
			"CA\t\"my ca.crt\"\nCert sub/other.crt\nkey './client.key'\n",
			"<ca>\n" + pemCA + "\n</ca>\n<cert>\n" + pemCert + "\n</cert>\n<key>\n" + pemKey + "\n</key>\n",
		},
		"line endings become LF": {
			"client\r\nca ca.crt\r\nremote x\r\n",
			"client\n<ca>\n" + pemCA + "\n</ca>\nremote x\n",
		},
		"what needs no file is left alone": {
			"dh none\nca [inline]\n# ca nothing.crt\n; key nothing.key\nremote x 1194\nauth-user-pass\n",
			"dh none\nca [inline]\n# ca nothing.crt\n; key nothing.key\nremote x 1194\nauth-user-pass\n",
		},
		"inline blocks are left alone": {
			"<ca>\n" + pemCA + "\n</ca>\n<connection>\nremote y\nca missing.crt\n</connection>\n<extra>\nkey missing.key\n</extra>\nca ca.crt\n",
			"<ca>\n" + pemCA + "\n</ca>\n<connection>\nremote y\nca missing.crt\n</connection>\n<extra>\nkey missing.key\n</extra>\n<ca>\n" + pemCA + "\n</ca>\n",
		},
		"no trailing newline": {"ca ca.crt", "<ca>\n" + pemCA + "\n</ca>"},
	} {
		got, _, err := inlineFiles(c.in, dir)
		if err != nil || got != c.want {
			t.Errorf("%s:\n got %q, %v\nwant %q", name, got, err, c.want)
		}
	}
}

func TestInlineFilesRefusesFilesThatAreNotKeyMaterialInTheProfileDirectory(t *testing.T) {
	t.Parallel()
	dir := profileDir(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.pem"), []byte(pemKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A link inside the directory does not make a file outside it inside.
	if err := os.Symlink(filepath.Join(outside, "secret.pem"), filepath.Join(dir, "link.key")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "linked-dir")); err != nil {
		t.Fatal(err)
	}
	// A link that stays inside is fine.
	if err := os.Symlink("ca.crt", filepath.Join(dir, "same-dir.crt")); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(dir, "big.crt")
	if err := os.WriteFile(big, []byte("-----BEGIN CERTIFICATE-----\n"+strings.Repeat("A", maxReferencedFileSize)), 0o600); err != nil {
		t.Fatal(err)
	}

	for name, c := range map[string]struct{ line, want string }{
		"parent directory":      {"key ../" + filepath.Base(outside) + "/secret.pem", "is outside the directory of the profile"},
		"absolute path":         {"key " + filepath.Join(outside, "secret.pem"), "is outside the directory of the profile"},
		"link to a file":        {"key link.key", "is outside the directory of the profile"},
		"link to a directory":   {"key linked-dir/secret.pem", "is outside the directory of the profile"},
		"home directory":        {"key ~/.ssh/id_rsa", "no such file"},
		"missing":               {"ca nothing.crt", "no such file"},
		"not key material":      {"ca readme.txt", "holds no certificate or key"},
		"not text":              {"ca binary.crt", "not a text file"},
		"too large":             {"ca big.crt", "larger than"},
		"directory":             {"ca sub", "not a regular file"},
		"device":                {"ca /dev/null", "is outside the directory of the profile"},
		"escape in a directory": {"ca sub/../../x.crt", "no such file"},
	} {
		_, _, err := inlineFiles(c.line+"\n", dir)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error with %q", name, err, c.want)
		}
	}
	if got, _, err := inlineFiles("ca same-dir.crt\n", dir); err != nil || !strings.Contains(got, pemCA) {
		t.Errorf("a link inside the directory: %q, %v", got, err)
	}
}

// The point of inlining: the daemon accepts the result and rejects the original.
func TestInlinedProfileIsAcceptedByTheDaemonParser(t *testing.T) {
	t.Parallel()
	dir := profileDir(t)
	path := filepath.Join(dir, "office.ovpn")
	profile := "client\ndev tun\nremote vpn.example.com 1194 udp\nca ca.crt\ncert client.crt\nkey client.key\ntls-auth ta.key 1\n"
	if err := os.WriteFile(path, []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := ovpn.Parse([]byte(profile)); err == nil {
		t.Fatal("the daemon accepts a profile that names files; inlining would be pointless")
	}
	content, _, err := readProfile(path)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ovpn.Parse(content)
	if err != nil {
		t.Fatalf("the daemon rejects the inlined profile: %v\n%s", err, content)
	}
	for _, block := range []string{"<ca>", "<cert>", "<key>", "<tls-auth>", "key-direction 1"} {
		if !bytes.Contains(parsed.Content, []byte(block)) {
			t.Errorf("the stored profile lacks %s:\n%s", block, parsed.Content)
		}
	}
}

func TestReadProfileLeavesWireGuardAlone(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// A line that looks like a file directive is a wg-quick setting here.
	conf := "[Interface]\nPrivateKey = k\nAddress = 10.6.0.2/32\n# ca missing.crt\n[Peer]\nEndpoint = 203.0.113.5:51820\nAllowedIPs = 0.0.0.0/0\n"
	path := filepath.Join(dir, "home.conf")
	if err := os.WriteFile(path, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, origin, err := readProfile(path); err != nil || string(got) != conf || origin != nil {
		t.Errorf("readProfile = %q, %v, %v", got, origin, err)
	}

	// A profile without anything that tells its kind is the daemon's to judge.
	odd := filepath.Join(dir, "odd.txt")
	if err := os.WriteFile(odd, []byte("something = else\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _, err := readProfile(odd); err != nil || string(got) != "something = else\n" {
		t.Errorf("readProfile of an unknown kind = %q, %v", got, err)
	}
}

func TestSplitWords(t *testing.T) {
	t.Parallel()
	for in, want := range map[string][]string{
		"":                       nil,
		"# ca x":                 nil,
		"; ca x":                 nil,
		"ca ca.crt":              {"ca", "ca.crt"},
		"  ca \t ca.crt  ":       {"ca", "ca.crt"},
		`ca "my ca.crt" 1`:       {"ca", "my ca.crt", "1"},
		`ca 'my ca.crt'`:         {"ca", "my ca.crt"},
		`ca my\ ca.crt`:          {"ca", "my ca.crt"},
		`ca "a\"b"`:              {"ca", `a"b`},
		`ca 'a\b'`:               {"ca", `a\b`},
		`ca ""`:                  {"ca", ""},
		`remote x 1194 # a note`: {"remote", "x", "1194", "#", "a", "note"},
	} {
		if got := splitWords(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitWords(%q) = %q, want %q", in, got, want)
		}
	}
}

// The daemon refuses a profile above profile.MaxContentSize; one that grows past
// it by inlining is not worth sending.
func TestReadProfileRefusesAProfileThatInliningMakesTooLarge(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	certificate := "-----BEGIN CERTIFICATE-----\n" + strings.Repeat("A", maxReferencedFileSize-100) + "\n-----END CERTIFICATE-----\n"
	if err := os.WriteFile(filepath.Join(dir, "big.crt"), []byte(certificate), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := "client\n" + strings.Repeat("extra-certs big.crt\n", 5)
	path := filepath.Join(dir, "p.ovpn")
	if err := os.WriteFile(path, []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readProfile(path); err == nil || !strings.Contains(err.Error(), "with its files inline") {
		t.Errorf("readProfile = %v", err)
	}
}

// Inlining adds lines, and the daemon counts the lines it received.
func TestInlinedLinesAreMappedBackToTheFile(t *testing.T) {
	t.Parallel()
	dir := profileDir(t)
	text := "client\nca ca.crt\nremote x\ntls-auth ta.key 1\nup /bin/hook\n"
	got, origin, err := inlineFiles(text, dir)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(got, "\n")
	if len(origin) != len(lines) {
		t.Fatalf("%d lines but %d origins:\n%s", len(lines), len(origin), got)
	}
	// Whatever stands for a line of the file, the directive that follows it
	// is where the daemon says it is.
	for i, line := range lines {
		want := map[string]int{"client": 1, "<ca>": 2, "</ca>": 2, "remote x": 3, "<tls-auth>": 4, "key-direction 1": 4, "up /bin/hook": 5}[line]
		if want != 0 && origin[i] != want {
			t.Errorf("content line %d %q is line %d of the file, origin says %d", i+1, line, want, origin[i])
		}
	}
	for n, want := range map[int]int{1: 1, 0: 0, -1: -1, len(origin) + 5: len(origin) + 5} {
		if got := originalLine(origin, n); got != want {
			t.Errorf("originalLine(%d) = %d, want %d", n, got, want)
		}
	}
	// The line the daemon reports for "up /bin/hook" is the last content line but one.
	upLine := 0
	for i, line := range lines {
		if line == "up /bin/hook" {
			upLine = i + 1
		}
	}
	if upLine <= 5 || originalLine(origin, upLine) != 5 {
		t.Errorf("up is content line %d, original line %d; want a line past 5 that maps to 5", upLine, originalLine(origin, upLine))
	}

	reported := strconv.Itoa(upLine)
	for in, want := range map[string]string{
		"line " + reported + ": up is refused": "line 5: up is refused",
		"line 0: not tied to a line":           "line 0: not tied to a line",
		"profile is empty":                     "profile is empty",
		"the line 3 is odd":                    "the line 3 is odd",
	} {
		if got := originalRejection(in, origin); got != want {
			t.Errorf("originalRejection(%q) = %q, want %q", in, got, want)
		}
	}
}
