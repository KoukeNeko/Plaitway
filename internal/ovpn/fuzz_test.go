package ovpn

import (
	"bytes"
	"strings"
	"testing"
)

// FuzzParse feeds Parse arbitrary bytes. Whatever it accepts must be stored in
// a form that cannot make openvpn read a file, run a program or see a
// directive that was not validated.
func FuzzParse(f *testing.F) {
	for _, name := range []string{"asus.ovpn", "windows.ovpn", "merlin.ovpn", "connection-blocks.ovpn"} {
		data, err := readFixtureForFuzz(name)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	for _, seed := range []string{
		"client\nremote h 1194\nup /bin/sh\n",
		"client\nremote h 1194\n--ca /etc/passwd\n",
		"client\nremote h 1194\n<ca>\nx\n</ca>\nroute-up x\n",
		"client\n<connection>\nremote h\n</connection>\n",
		"client\nremote h 1194 # c\n\\#\nscript-security 2\n",
		"remote h\r\nup x\r\n",
		"client\nremote \"h\\\" 1194\n",
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, in []byte) {
		parsed, err := Parse(in)
		if err != nil {
			return
		}
		content := parsed.Content

		for _, c := range content {
			if c != '\n' && c != '\t' && (c < 0x20 || c == 0x7f) {
				t.Fatalf("stored content has control character 0x%02x:\n%q", c, content)
			}
		}
		items, err := scan(string(content), 1, false)
		if err != nil {
			t.Fatalf("stored content does not scan: %v\n%q", err, content)
		}
		var check func([]item)
		check = func(items []item) {
			for _, it := range items {
				switch it.kind {
				case kindDirective:
					if !has(allowedDirectives, it.name) {
						t.Fatalf("stored directive %q is not allowed:\n%q", it.name, content)
					}
					if has(rejectedFileDirectives, it.name) && !(it.name == "dh" && len(it.args) == 1 && it.args[0] == "none") {
						t.Fatalf("stored directive %q reads a file:\n%q", it.name, content)
					}
					if has(rejectedWithArgument, it.name) && len(it.args) > 0 {
						t.Fatalf("stored directive %q names a file:\n%q", it.name, content)
					}
					if removalReason(it.name) != "" {
						t.Fatalf("stored directive %q should have been removed:\n%q", it.name, content)
					}
					if n := len(directiveLine(it)); n > maxConfigLineBytes {
						t.Fatalf("stored directive %q is a line of %d bytes, which openvpn cannot read:\n%q", it.name, n, content)
					}
				case kindComment:
					if len(it.text) > maxConfigLineBytes {
						t.Fatalf("stored comment is a line of %d bytes, which openvpn cannot read:\n%q", len(it.text), content)
					}
				case kindBlock:
					if !has(allowedBlocks, it.name) {
						t.Fatalf("stored block %q is not allowed:\n%q", it.name, content)
					}
					for _, line := range strings.Split(it.body, "\n") {
						if len(line) > maxConfigLineBytes && strings.Contains(line, "</"+it.name+">") {
							t.Fatalf("stored block %q has a long line that openvpn would end the block in:\n%q", it.name, content)
						}
					}
				case kindConnection:
					check(it.children)
				}
			}
		}
		check(items)

		again, err := Parse(content)
		if err != nil {
			t.Fatalf("stored content is rejected on re-parse: %v\n%q", err, content)
		}
		if !bytes.Equal(again.Content, content) {
			t.Fatalf("re-parsing changed the content:\n%q\n%q", content, again.Content)
		}
		for _, w := range again.Warnings {
			// A kept directive can still carry a note ("route to a host name is
			// left to openvpn"); nothing may be left to remove.
			if strings.HasPrefix(w.Message, "removed") {
				t.Fatalf("stored content still has something to remove: %+v\n%q", w, content)
			}
		}
		if len(parsed.Summary.Endpoints) == 0 {
			t.Fatal("accepted a profile without a remote")
		}
	})
}
