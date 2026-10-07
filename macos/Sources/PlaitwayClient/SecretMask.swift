import Foundation

/// Keeps the secrets of a profile off the screen while it is edited. The mask
/// finds them in the profile text and shows a numbered placeholder such as
/// `‹secret 1›` in their place; `restore` puts each secret back where its
/// placeholder still stands after the user's edits.
///
/// What is a secret: a WireGuard `PrivateKey` or `PresharedKey` value (also
/// in a line that is commented out), the body of an OpenVPN `<key>`,
/// `<tls-auth>`, `<tls-crypt>`, `<tls-crypt-v2>`, `<pkcs12>`, `<secret>`,
/// `<auth-user-pass>` or `<http-proxy-user-pass>` block, and a PEM private key
/// or OpenVPN static key anywhere else in the text.
///
/// What an edit does to a placeholder: left alone, its secret comes back; moved
/// with cut and paste, the secret moves with it; deleted or typed over, the
/// secret is gone; copied, `restore` fails, because it cannot know where the
/// secret belongs; changed to a number the mask does not know, or damaged, it
/// is ordinary text.
public struct SecretMask: Sendable {
    public enum Failure: Error, Equatable, Sendable {
        /// The text holds the placeholder of secret `number` more than once;
        /// `line` is where the second one stands.
        case duplicatedPlaceholder(number: Int, line: Int)
    }

    /// The text with its secrets put back.
    public struct Restoration: Sendable {
        public let text: String
        private let expansions: [Expansion]

        fileprivate struct Expansion: Sendable {
            /// Line of the displayed text that holds the placeholder.
            let line: Int
            /// Lines the secret adds when it replaces the placeholder.
            let extraLines: Int
        }

        fileprivate init(text: String, expansions: [Expansion]) {
            self.text = text
            self.expansions = expansions
        }

        /// The line of the displayed text that shows what stands on `line` (1-based) of
        /// the restored text. The daemon names lines of the text it was sent, and a
        /// secret of several lines shows as one.
        public func displayLine(forRestoredLine line: Int) -> Int {
            var added = 0
            for expansion in expansions {
                let first = expansion.line + added
                if line < first { break }
                if line <= first + expansion.extraLines { return expansion.line }
                added += expansion.extraLines
            }
            return line - added
        }
    }

    /// The text to show and edit.
    public let displayText: String
    private let secrets: [Int: String]

    public init(text: String, kind: ProfileKind) {
        let utf8 = text.utf8
        // A placeholder that is in the text already is not ours: skip its number.
        let taken = Set(Self.placeholders(in: text).map(\.number))
        var display = ""
        var secrets: [Int: String] = [:]
        var cursor = utf8.startIndex
        var number = 0
        for range in Self.secretRanges(in: text, kind: kind) {
            repeat { number += 1 } while taken.contains(number)
            display += utf8.string(cursor..<range.lowerBound)
            display += Self.placeholder(number)
            secrets[number] = utf8.string(range)
            cursor = range.upperBound
        }
        display += utf8.string(cursor..<utf8.endIndex)
        displayText = display
        self.secrets = secrets
    }

    /// Replaces each placeholder of `edited` by its secret. Throws
    /// `Failure.duplicatedPlaceholder` when a secret would have to be put in two places.
    public func restore(_ edited: String) throws -> Restoration {
        let utf8 = edited.utf8
        var text = ""
        var expansions: [Restoration.Expansion] = []
        var restored: Set<Int> = []
        var cursor = utf8.startIndex
        var line = 1
        var scanned = utf8.startIndex
        for (range, number) in Self.placeholders(in: edited) {
            guard let secret = secrets[number] else { continue }
            line += utf8[scanned..<range.lowerBound].count { $0 == UInt8(ascii: "\n") }
            scanned = range.lowerBound
            guard restored.insert(number).inserted else { throw Failure.duplicatedPlaceholder(number: number, line: line) }
            text += utf8.string(cursor..<range.lowerBound)
            text += secret
            cursor = range.upperBound
            expansions.append(.init(line: line, extraLines: secret.utf8.count { $0 == UInt8(ascii: "\n") }))
        }
        text += utf8.string(cursor..<utf8.endIndex)
        return Restoration(text: text, expansions: expansions)
    }

    // MARK: Placeholders

    private static let opening = Array("‹secret ".utf8)
    private static let closing = Array("›".utf8)

    private static func placeholder(_ number: Int) -> String { "‹secret \(number)›" }

    /// Where `text` has something that looks like a placeholder, for the view to
    /// style. Whether the mask knows its number is another matter.
    public static func placeholderRanges(in text: String) -> [Range<String.Index>] {
        placeholders(in: text).map(\.range)
    }

    private static func placeholders(in text: String) -> [(range: Range<String.Index>, number: Int)] {
        let utf8 = text.utf8
        var found: [(range: Range<String.Index>, number: Int)] = []
        var cursor = utf8.startIndex
        while let start = utf8[cursor...].firstRange(of: opening) {
            cursor = start.upperBound
            var digitsEnd = cursor
            while digitsEnd < utf8.endIndex, (UInt8(ascii: "0")...UInt8(ascii: "9")).contains(utf8[digitsEnd]) {
                digitsEnd = utf8.index(after: digitsEnd)
            }
            guard utf8[cursor..<digitsEnd].first != UInt8(ascii: "0"), utf8[digitsEnd...].starts(with: closing),
                  let number = Int(Substring(utf8[cursor..<digitsEnd])) else { continue }
            cursor = utf8.index(digitsEnd, offsetBy: closing.count)
            found.append((start.lowerBound..<cursor, number))
        }
        return found
    }

    // MARK: Finding secrets

    /// Blocks whose body is a secret.
    private static let secretBlocks: Set<String> = [
        "key", "tls-auth", "tls-crypt", "tls-crypt-v2", "pkcs12", "secret", "auth-user-pass", "http-proxy-user-pass",
    ]

    /// The text to hide, in order of appearance and without overlap. A block that is
    /// never closed runs to the end of the text, as it does for OpenVPN.
    private static func secretRanges(in text: String, kind: ProfileKind) -> [Range<String.Index>] {
        let utf8 = text.utf8
        let lines = utf8.lineRanges()
        let readsWireGuard = kind != .openvpn
        let readsOpenVPN = kind != .wireguard
        var found: [Range<String.Index>] = []
        // The block whose lines are verbatim text and not directives.
        var verbatimBlock: String?
        // Labels of PEM blocks that have no END line after the last place that was looked: a
        // text of many BEGIN lines and no END would otherwise be searched to its end for each.
        var withoutFooter: Set<String> = []
        var index = 0
        while index < lines.count {
            let line = lines[index]
            var next = index + 1
            if let tag = verbatimBlock, OpenVPNLine.closes(tag, line: line, in: utf8) {
                verbatimBlock = nil
            } else if verbatimBlock == nil, readsOpenVPN, let tag = OpenVPNLine(line, in: utf8).openedBlock(in: utf8) {
                if secretBlocks.contains(tag) {
                    let closing = lines[next...].firstIndex { OpenVPNLine.closes(tag, line: $0, in: utf8) } ?? lines.count
                    let bodyLines = lines[next..<closing]
                    if let first = bodyLines.first, let last = bodyLines.last {
                        let body = first.lowerBound..<last.upperBound
                        if utf8[body].contains(where: { !String.UTF8View.isBlank($0) && $0 != UInt8(ascii: "\n") }) {
                            found.append(body)
                        }
                    }
                    next = closing + 1
                } else if tag != "connection" {
                    verbatimBlock = tag
                }
            } else if verbatimBlock == nil, readsWireGuard, let value = wireGuardSecret(in: line, text) {
                found.append(value)
            } else if let (range, last) = privateKeyPEM(at: index, lines: lines, utf8, withoutFooter: &withoutFooter) {
                found.append(range)
                next = last + 1
            }
            index = next
        }
        return found
    }

    /// The value of a `PrivateKey` or `PresharedKey` line. A commented-out key
    /// is as secret as a live one. The daemon trims every part of the line with
    /// Go's `strings.TrimSpace`, which takes in the no-break space, the ideographic
    /// space and the other Unicode white space, so this reads scalars and not bytes: a
    /// key behind a no-break space is a key the daemon accepts.
    private static func wireGuardSecret(in line: Range<String.Index>, _ text: String) -> Range<String.Index>? {
        let scalars = text.unicodeScalars
        func isBlank(_ scalar: Unicode.Scalar) -> Bool { scalar.properties.isWhitespace }
        var cursor = line.lowerBound
        while cursor < line.upperBound, isBlank(scalars[cursor]) || scalars[cursor] == "#" {
            cursor = scalars.index(after: cursor)
        }
        var nameEnd = cursor
        while nameEnd < line.upperBound, scalars[nameEnd] != "=", !isBlank(scalars[nameEnd]) {
            nameEnd = scalars.index(after: nameEnd)
        }
        let name = String(scalars[cursor..<nameEnd]).lowercased()
        guard name == "privatekey" || name == "presharedkey" else { return nil }
        var equals = nameEnd
        while equals < line.upperBound, isBlank(scalars[equals]) { equals = scalars.index(after: equals) }
        guard equals < line.upperBound, scalars[equals] == "=" else { return nil }
        let valueStart = scalars.index(after: equals)
        let valueEnd = scalars[valueStart..<line.upperBound].firstIndex(of: "#") ?? line.upperBound
        var first = valueStart
        var last = valueEnd
        while first < last, isBlank(scalars[first]) { first = scalars.index(after: first) }
        while first < last, isBlank(scalars[scalars.index(before: last)]) { last = scalars.index(before: last) }
        return first < last ? first..<last : nil
    }

    /// A PEM private key or an OpenVPN static key that starts on line `first`:
    /// from its BEGIN line to its END line. Without an END line it is not one.
    private static func privateKeyPEM(
        at first: Int, lines: [Range<String.Index>], _ utf8: String.UTF8View, withoutFooter: inout Set<String>
    ) -> (range: Range<String.Index>, last: Int)? {
        let begin = utf8.trimmed(lines[first])
        guard utf8[begin].starts(with: "-----BEGIN ".utf8) else { return nil }
        let header = utf8.string(begin)
        guard header.hasSuffix("-----"), header.utf8.count > "-----BEGIN -----".utf8.count else { return nil }
        let label = header.dropFirst("-----BEGIN ".count).dropLast("-----".count)
        let upperLabel = label.uppercased()
        guard upperLabel.contains("PRIVATE KEY") || upperLabel.hasPrefix("OPENVPN") else { return nil }
        // No footer was found after an earlier BEGIN of this label, so there is none after this one.
        guard !withoutFooter.contains(String(label)) else { return nil }
        let footer = Array("-----END \(label)-----".utf8)
        guard let last = lines[(first + 1)...].firstIndex(where: { utf8[utf8.trimmed($0)].elementsEqual(footer) }) else {
            withoutFooter.insert(String(label))
            return nil
        }
        return (begin.lowerBound..<utf8.trimmed(lines[last]).upperBound, last)
    }
}
