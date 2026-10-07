import Foundation

// What `SecretMask` and `ConfigTokenizer` share: reading profile text as lines
// of UTF-8, the way the daemon does. Every character that carries meaning in a
// profile is ASCII, so all of it works on the UTF-8 view and its indices, and
// text in any other script passes through untouched. Swift's own `Character`
// would join CR LF into one element and make a line break unreadable.

extension String.UTF8View {
    /// The lines of the text, each without its line break (LF or CR LF). The
    /// text after the last line break is a line only when it is not empty. A
    /// byte order mark at the start belongs to no line, as for the daemon.
    func lineRanges() -> [Range<Index>] {
        var lines: [Range<Index>] = []
        var start = startIndex
        if self[start...].starts(with: [0xEF, 0xBB, 0xBF]) { start = index(start, offsetBy: 3) }
        while start < endIndex {
            guard let lineFeed = self[start...].firstIndex(of: UInt8(ascii: "\n")) else {
                lines.append(start..<endIndex)
                break
            }
            var end = lineFeed
            if end > start, self[index(before: end)] == UInt8(ascii: "\r") { end = index(before: end) }
            lines.append(start..<end)
            start = index(after: lineFeed)
        }
        return lines
    }

    /// `range` without the blanks (space, tab, CR) at both ends.
    func trimmed(_ range: Range<Index>) -> Range<Index> {
        var lower = range.lowerBound
        var upper = range.upperBound
        while lower < upper, Self.isBlank(self[lower]) { formIndex(after: &lower) }
        while upper > lower, Self.isBlank(self[index(before: upper)]) { formIndex(before: &upper) }
        return lower..<upper
    }

    func string(_ range: Range<Index>) -> String {
        String(Substring(self[range]))
    }

    static func isBlank(_ byte: UInt8) -> Bool {
        byte == UInt8(ascii: " ") || byte == UInt8(ascii: "\t") || byte == UInt8(ascii: "\r")
    }
}

/// One OpenVPN line split into parameters with the rules of OpenVPN's own
/// parser (and the daemon's): blanks separate them, a parameter that starts
/// with a double or single quote runs to the closing quote, a backslash escapes
/// the next character outside single quotes, and a `#` or `;` where a
/// parameter would start begins a comment that runs to the end of the line.
struct OpenVPNLine {
    /// Each parameter with its quotes.
    let fields: [Range<String.Index>]
    let comment: Range<String.Index>?

    init(_ line: Range<String.Index>, in utf8: String.UTF8View) {
        var fields: [Range<String.Index>] = []
        var comment: Range<String.Index>?
        var cursor = line.lowerBound
        while cursor < line.upperBound {
            let byte = utf8[cursor]
            if String.UTF8View.isBlank(byte) {
                cursor = utf8.index(after: cursor)
            } else if byte == UInt8(ascii: "#") || byte == UInt8(ascii: ";") {
                comment = cursor..<line.upperBound
                break
            } else {
                let start = cursor
                let quote = byte == UInt8(ascii: "\"") || byte == UInt8(ascii: "'") ? byte : nil
                if quote != nil { cursor = utf8.index(after: cursor) }
                while cursor < line.upperBound {
                    let current = utf8[cursor]
                    if let quote {
                        if current == quote {
                            cursor = utf8.index(after: cursor)
                            break
                        }
                    } else if String.UTF8View.isBlank(current) {
                        break
                    }
                    if current == UInt8(ascii: "\\"), quote != UInt8(ascii: "'") {
                        let escaped = utf8.index(after: cursor)
                        cursor = escaped < line.upperBound ? escaped : cursor
                    }
                    cursor = utf8.index(after: cursor)
                }
                fields.append(start..<cursor)
            }
        }
        self.fields = fields
        self.comment = comment
    }

    /// The name of the block that a line opens: a lone `<tag>` of letters,
    /// digits, `-` and `_`, optionally followed by a comment. Lower-case.
    func openedBlock(in utf8: String.UTF8View) -> String? {
        guard fields.count == 1 else { return nil }
        var text = utf8.string(fields[0])
        if let quote = text.first, quote == "\"" || quote == "'", text.count > 1, text.last == quote {
            text = String(text.dropFirst().dropLast())
        }
        guard text.utf8.count >= 3, text.hasPrefix("<"), text.hasSuffix(">") else { return nil }
        let tag = text.dropFirst().dropLast()
        return tag.utf8.allSatisfy(Self.isTagByte) ? tag.lowercased() : nil
    }

    private static func isTagByte(_ byte: UInt8) -> Bool {
        switch byte {
        case UInt8(ascii: "a")...UInt8(ascii: "z"), UInt8(ascii: "A")...UInt8(ascii: "Z"), UInt8(ascii: "0")...UInt8(ascii: "9"),
             UInt8(ascii: "-"), UInt8(ascii: "_"):
            true
        default:
            false
        }
    }

    /// Whether the line closes the block `tag` (lower-case): `</tag>` and nothing else.
    static func closes(_ tag: String, line: Range<String.Index>, in utf8: String.UTF8View) -> Bool {
        utf8.string(utf8.trimmed(line)).lowercased() == "</\(tag)>"
    }
}
