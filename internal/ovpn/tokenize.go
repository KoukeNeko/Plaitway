package ovpn

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Limits on untrusted profile text. They are far above what real profiles
// need; the point is that a hostile file cannot make the root daemon allocate
// without bound.
const (
	maxProfileBytes       = 1 << 20
	maxDirectiveLineBytes = 4096
	maxBlockLineBytes     = 64 << 10
	maxTokensPerLine      = 32
	maxTokenBytes         = 1024
)

// maxConfigLineBytes is the longest line of the written-back profile that
// openvpn can read. It reads a line with fgets into a 257-byte buffer and
// refuses the file when one fills it, so 255 bytes plus the line feed are one
// too many. Inside a <connection> block it cuts longer lines in two without a
// word, and the second half then counts as a directive of its own.
const maxConfigLineBytes = 254

// ParseError is a profile rejection. Line is 1-based and 0 when the problem is
// not tied to one line.
type ParseError struct {
	Line int
	Msg  string
}

func (e *ParseError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("line %d: %s", e.Line, e.Msg)
	}
	return e.Msg
}

func errAt(line int, format string, args ...any) *ParseError {
	return &ParseError{Line: line, Msg: fmt.Sprintf(format, args...)}
}

type itemKind uint8

const (
	kindDirective  itemKind = iota + 1
	kindBlock               // <tag> ... </tag> with verbatim body
	kindConnection          // <connection> block, whose body is itself a profile
	kindComment
)

// item is one syntactic element of a profile.
type item struct {
	kind itemKind
	line int
	// name is the directive name as written (before normalisation) or the tag
	// name of a block.
	name string
	args []string
	// body is the verbatim content of a kindBlock: each line ends with "\n".
	body string
	// text is the full comment line of a kindComment, without leading blanks.
	text     string
	children []item // kindConnection
}

// normalize checks that content is plain text and returns it with LF line
// endings and without a byte order mark. The checks happen before any
// tokenizing so that no later stage sees NUL or other control characters.
func normalize(content []byte) (string, error) {
	if len(content) > maxProfileBytes {
		return "", errAt(0, "profile is larger than %d bytes", maxProfileBytes)
	}
	content = []byte(strings.TrimPrefix(string(content), "\xef\xbb\xbf"))
	if !utf8.Valid(content) {
		return "", errAt(0, "profile is not valid UTF-8 text")
	}
	line := 1
	for i, c := range content {
		switch {
		case c == '\n':
			line++
		case c == '\r':
			if i+1 >= len(content) || content[i+1] != '\n' {
				return "", errAt(line, "carriage return without line feed")
			}
		case c == '\t':
		case c < 0x20 || c == 0x7f:
			return "", errAt(line, "control character 0x%02x; profile must be text", c)
		}
	}
	return strings.ReplaceAll(string(content), "\r\n", "\n"), nil
}

const (
	stInitial = iota
	stBare
	stDouble
	stSingle
)

// splitFields splits one profile line into parameters with the rules of
// OpenVPN's own parser: blanks separate parameters; a parameter may be wrapped
// in double quotes (where \\ and \" escape) or single quotes (no escapes);
// outside single quotes a backslash may only precede a backslash, a double
// quote or a blank; # or ; where a parameter would start begin a comment that
// runs to the end of the line.
func splitFields(s string) ([]string, error) {
	var (
		fields    []string
		cur       []byte
		state     = stInitial
		backslash bool
	)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && !backslash && state != stSingle {
			backslash = true
			continue
		}
		var (
			out  byte
			emit bool
			done bool
		)
		switch state {
		case stInitial:
			switch {
			case isBlank(c):
			case c == '#' || c == ';':
				return fields, nil
			case !backslash && c == '"':
				state = stDouble
			case !backslash && c == '\'':
				state = stSingle
			default:
				out, emit, state = c, true, stBare
			}
		case stBare:
			if !backslash && isBlank(c) {
				done = true
			} else {
				out, emit = c, true
			}
		case stDouble:
			if !backslash && c == '"' {
				done = true
			} else {
				out, emit = c, true
			}
		case stSingle:
			if c == '\'' {
				done = true
			} else {
				out, emit = c, true
			}
		}
		if backslash && emit && out != '\\' && out != '"' && !isBlank(out) {
			return nil, fmt.Errorf("a backslash may only precede a backslash, a double quote or a blank")
		}
		backslash = false
		if emit {
			if len(cur) >= maxTokenBytes {
				return nil, fmt.Errorf("parameter longer than %d bytes", maxTokenBytes)
			}
			cur = append(cur, out)
		}
		if done {
			fields = append(fields, string(cur))
			cur, state = cur[:0], stInitial
			if len(fields) > maxTokensPerLine {
				return nil, fmt.Errorf("more than %d parameters", maxTokensPerLine)
			}
		}
	}
	switch {
	case backslash:
		return nil, fmt.Errorf("line ends with a backslash")
	case state == stDouble || state == stSingle:
		return nil, fmt.Errorf("quotation mark is never closed")
	case state == stBare:
		fields = append(fields, string(cur))
		if len(fields) > maxTokensPerLine {
			return nil, fmt.Errorf("more than %d parameters", maxTokensPerLine)
		}
	}
	return fields, nil
}

func isBlank(c byte) bool { return c == ' ' || c == '\t' }

// blockTag reports the tag name when the fields are a lone "<tag>".
func blockTag(fields []string) (string, bool) {
	if len(fields) != 1 {
		return "", false
	}
	f := fields[0]
	if len(f) < 3 || f[0] != '<' || f[len(f)-1] != '>' || f[1] == '/' {
		return "", false
	}
	tag := f[1 : len(f)-1]
	for i := 0; i < len(tag); i++ {
		c := tag[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return "", false
		}
	}
	return tag, true
}

// scan turns normalised profile text into items. firstLine is the number of
// the first line of text within the whole file; nested is true inside a
// <connection> block, where blocks cannot nest.
//
// A block ends at the first line that starts with its closing tag, even after
// leading blanks, so this scanner never ends a block later than OpenVPN
// would. A block body is kept verbatim and never looked at as directives.
func scan(text string, firstLine int, nested bool) ([]item, error) {
	lines := strings.Split(text, "\n")
	var items []item
	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		lineNo := firstLine + i
		trimmed := strings.TrimLeft(raw, " \t")
		if trimmed == "" {
			continue
		}
		if trimmed[0] == '#' || trimmed[0] == ';' {
			items = append(items, item{kind: kindComment, line: lineNo, text: strings.TrimRight(trimmed, " \t")})
			continue
		}
		if len(raw) > maxDirectiveLineBytes {
			return nil, errAt(lineNo, "line is longer than %d bytes", maxDirectiveLineBytes)
		}
		fields, err := splitFields(raw)
		if err != nil {
			return nil, errAt(lineNo, "%v", err)
		}
		if len(fields) == 0 {
			continue
		}
		tag, isBlock := blockTag(fields)
		if !isBlock {
			items = append(items, item{kind: kindDirective, line: lineNo, name: fields[0], args: fields[1:]})
			continue
		}

		closing := "</" + tag + ">"
		end := -1
		for j := i + 1; j < len(lines); j++ {
			t := strings.TrimLeft(lines[j], " \t")
			if !strings.HasPrefix(t, closing) {
				if len(lines[j]) > maxBlockLineBytes {
					return nil, errAt(firstLine+j, "line is longer than %d bytes", maxBlockLineBytes)
				}
				// openvpn reads a block in pieces of at most maxConfigLineBytes and
				// ends it at a piece that starts with the closing tag. A longer line
				// with the tag inside could end the block where this scanner sees
				// none, and the rest would be read as directives.
				if len(lines[j]) > maxConfigLineBytes && strings.Contains(lines[j], closing) {
					return nil, errAt(firstLine+j, "line is longer than %d bytes and contains %s", maxConfigLineBytes, closing)
				}
				continue
			}
			if rest := strings.Trim(t[len(closing):], " \t"); rest != "" {
				return nil, errAt(firstLine+j, "text after %s", closing)
			}
			end = j
			break
		}
		if end < 0 {
			return nil, errAt(lineNo, "<%s> is never closed", tag)
		}
		body := lines[i+1 : end]
		if tag == "connection" {
			if nested {
				return nil, errAt(lineNo, "<connection> blocks cannot be nested")
			}
			children, err := scan(strings.Join(body, "\n"), lineNo+1, true)
			if err != nil {
				return nil, err
			}
			items = append(items, item{kind: kindConnection, line: lineNo, name: tag, children: children})
		} else {
			var b strings.Builder
			for _, l := range body {
				b.WriteString(l)
				b.WriteByte('\n')
			}
			items = append(items, item{kind: kindBlock, line: lineNo, name: tag, body: b.String()})
		}
		i = end
	}
	return items, nil
}

// quoteField writes one parameter so that OpenVPN's parser reads back exactly
// s: bare when it only has safe characters, otherwise in double quotes.
func quoteField(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool { return !isSafeBare(r) }) < 0 {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r == '\\' || r == '"' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

func isSafeBare(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	return strings.ContainsRune("-_./:@%+=,~", r)
}
