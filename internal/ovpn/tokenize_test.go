package ovpn

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSplitFields(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"plain", "remote host 1194", []string{"remote", "host", "1194"}},
		{"tabs and runs of blanks", "  route \t10.0.0.0   255.0.0.0  ", []string{"route", "10.0.0.0", "255.0.0.0"}},
		{"double quotes keep blanks", `pull-filter ignore "dhcp option"`, []string{"pull-filter", "ignore", "dhcp option"}},
		{"escaped quote in double quotes", `x "a\"b"`, []string{"x", `a"b`}},
		{"escaped backslash", `x "a\\b"`, []string{"x", `a\b`}},
		{"single quotes have no escapes", `x 'a\b'`, []string{"x", `a\b`}},
		{"escaped blank in bare word", `x a\ b`, []string{"x", "a b"}},
		{"empty quoted parameter", `x "" y`, []string{"x", "", "y"}},
		{"comment after parameters", "remote h 1194 # backup", []string{"remote", "h", "1194"}},
		{"semicolon comment", "remote h ; backup", []string{"remote", "h"}},
		{"hash inside a word is not a comment", "x a#b", []string{"x", "a#b"}},
		{"hash inside quotes is not a comment", `x "# y"`, []string{"x", "# y"}},
		{"whole line comment", "# nothing", nil},
		{"blank line", "   ", nil},
		{"adjacent quotes split", `x "a""b"`, []string{"x", "a", "b"}},
		{"utf-8 survives", "x 名稱", []string{"x", "名稱"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := splitFields(tt.in)
			if err != nil {
				t.Fatalf("splitFields(%q): %v", tt.in, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("splitFields(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSplitFieldsErrors(t *testing.T) {
	tests := []struct{ name, in string }{
		{"unclosed double quote", `x "abc`},
		{"unclosed single quote", `x 'abc`},
		{"bad escape", `x a\nb`},
		{"bad escape in quotes", `x "a\nb"`},
		{"trailing backslash", `x abc\`},
		{"too many parameters", strings.Repeat("a ", maxTokensPerLine+1)},
		{"parameter too long", "x " + strings.Repeat("a", maxTokenBytes+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := splitFields(tt.in); err == nil {
				t.Fatalf("splitFields(%q) = %q, want an error", tt.in, got)
			}
		})
	}
}

// quoteField must write a parameter so that splitFields reads exactly the same
// value back. This is what makes the stored profile independent of how the
// original was quoted.
func TestQuoteFieldRoundTrip(t *testing.T) {
	values := []string{
		"plain", "a b", "", `a"b`, `a\b`, "a#b", "#lead", ";lead", "'q'", "tab\there",
		"<ca>", "[[INLINE]]", "名稱", `\\`, `"`, "a;b", "x=y,z:w/v.u@t",
	}
	for _, v := range values {
		line := "name " + quoteField(v)
		got, err := splitFields(line)
		if err != nil {
			t.Fatalf("splitFields(%q): %v", line, err)
		}
		if want := []string{"name", v}; !reflect.DeepEqual(got, want) {
			t.Errorf("quoteField(%q) = %q reads back as %q", v, quoteField(v), got)
		}
	}
}

func TestNormalize(t *testing.T) {
	t.Run("crlf becomes lf and bom is dropped", func(t *testing.T) {
		got, err := normalize([]byte("\xef\xbb\xbfclient\r\ndev tun\r\n"))
		if err != nil || got != "client\ndev tun\n" {
			t.Fatalf("normalize = %q, %v", got, err)
		}
	})
	rejects := []struct {
		name string
		in   []byte
		line int
	}{
		{"NUL byte", []byte("client\nremote h\x00 1194\n"), 2},
		{"other control character", []byte("client\n\x01\n"), 2},
		{"escape character", []byte("client\x1b[31m\n"), 1},
		{"DEL", []byte("a\n\nb\x7f\n"), 3},
		{"lone carriage return", []byte("client\rdev tun\n"), 1},
		{"invalid UTF-8", []byte("client\nremote \xff\xfe\n"), 0},
		{"too large", []byte(strings.Repeat("# x\n", maxProfileBytes/4+1)), 0},
	}
	for _, tt := range rejects {
		t.Run(tt.name, func(t *testing.T) {
			_, err := normalize(tt.in)
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("normalize error = %v, want *ParseError", err)
			}
			if pe.Line != tt.line {
				t.Errorf("error line = %d, want %d (%v)", pe.Line, tt.line, err)
			}
		})
	}
}

func TestScanBlocks(t *testing.T) {
	text := "client\n<ca>\nline one\n  </key> not the end\n</ca>\nremote h\n"
	items, err := scan(text, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3: %+v", len(items), items)
	}
	if items[1].kind != kindBlock || items[1].name != "ca" || items[1].body != "line one\n  </key> not the end\n" {
		t.Errorf("block = %+v", items[1])
	}
	if items[2].line != 6 || items[2].name != "remote" {
		t.Errorf("item after the block = %+v", items[2])
	}
}

func TestScanBlockErrors(t *testing.T) {
	tests := []struct {
		name string
		in   string
		line int
	}{
		{"unterminated", "client\n<ca>\nabc\n", 2},
		{"text after the closing tag", "<ca>\nabc\n</ca> up /bin/sh\n", 3},
		{"nested connection", "<connection>\n<connection>\nremote h\n</connection>\n</connection>\n", 2},
		{"error inside a connection block keeps the file line", "client\n<connection>\nremote h\nroute 'x\n</connection>\n", 4},
		{"quote error", "client\nremote \"h\n", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := scan(tt.in, 1, false)
			var pe *ParseError
			if !errors.As(err, &pe) || pe.Line != tt.line {
				t.Fatalf("scan error = %v, want line %d", err, tt.line)
			}
		})
	}
}

// The closing tag is recognised after leading blanks, never later than OpenVPN
// would see it, so a body cannot hide directives from the parser.
func TestScanClosingTagWithIndent(t *testing.T) {
	items, err := scan("<ca>\nabc\n   </ca>\nup /bin/sh\n", 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[1].name != "up" {
		t.Fatalf("items = %+v; want the block then the up directive", items)
	}
}

func TestScanConnectionBodyIsScannedOnce(t *testing.T) {
	// The first </connection> ends the block, as in OpenVPN, even though a
	// <tls-crypt> block inside would have contained it had blocks nested.
	text := "<connection>\nremote h\n<tls-crypt>\nkey\n</connection>\nup /bin/sh\n"
	_, err := scan(text, 1, false)
	var pe *ParseError
	if !errors.As(err, &pe) || pe.Line != 3 {
		t.Fatalf("scan error = %v; want the unterminated <tls-crypt> at line 3", err)
	}
}
