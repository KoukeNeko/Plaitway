import Foundation

/// A stretch of profile text and the part it plays in it, for syntax
/// highlighting. The role says what the text is; the view decides how it looks.
public struct ConfigToken: Equatable, Sendable {
    public enum Role: Equatable, Sendable {
        /// WireGuard: `[Interface]`, `[Peer]`.
        case section
        /// A WireGuard key such as `Address`, an OpenVPN directive such as `remote`.
        case directive
        /// A value or parameter that has no more specific role.
        case argument
        case number
        case ipAddress
        /// An address with its prefix length, `10.0.0.0/8`.
        case cidr
        case port
        case hostname
        /// WireGuard `PrivateKey`, `PublicKey` and `PresharedKey` values.
        case base64Key
        /// From `#` (or `;` in OpenVPN) to the end of the line.
        case comment
        /// OpenVPN `<ca>` and `</ca>`.
        case blockTag
        /// The text between the tags of an OpenVPN block, PEM data and all, as one token.
        case blockBody
    }

    /// The token's text in the string that was tokenized. A token never holds a
    /// line break, except for a `blockBody`.
    public let range: Range<String.Index>
    public let role: Role
}

/// Splits profile text into tokens the way the daemon reads it. The text may
/// be wrong or half-written: whatever has no role is left out, and the tokens
/// come in order of position and never overlap.
public enum ConfigTokenizer {
    /// The tokens of `text`, a profile of `kind`; none for a kind that is not OpenVPN or WireGuard.
    public static func tokens(in text: String, kind: ProfileKind) -> [ConfigToken] {
        var lexer = Lexer(utf8: text.utf8)
        switch kind {
        case .openvpn: lexer.scanOpenVPN()
        case .wireguard: lexer.scanWireGuard()
        default: break
        }
        return lexer.tokens
    }

    private struct Lexer {
        let utf8: String.UTF8View
        var tokens: [ConfigToken] = []

        mutating func add(_ range: Range<String.Index>?, _ role: ConfigToken.Role) {
            guard let range, !range.isEmpty else { return }
            tokens.append(ConfigToken(range: range, role: role))
        }

        // MARK: WireGuard

        mutating func scanWireGuard() {
            for line in utf8.lineRanges() {
                // The daemon cuts a line at the first `#`, wherever it stands.
                let comment = utf8[line].firstIndex(of: UInt8(ascii: "#")).map { $0..<line.upperBound }
                let content = utf8.trimmed(line.lowerBound..<(comment?.lowerBound ?? line.upperBound))
                if utf8[content].first == UInt8(ascii: "[") {
                    add(content, .section)
                } else if let equals = utf8[content].firstIndex(of: UInt8(ascii: "=")) {
                    let key = utf8.trimmed(content.lowerBound..<equals)
                    add(key, .directive)
                    wireGuardValue(of: utf8.string(key).lowercased(), utf8.trimmed(utf8.index(after: equals)..<content.upperBound))
                }
                add(comment, .comment)
            }
        }

        private mutating func wireGuardValue(of key: String, _ value: Range<String.Index>) {
            switch key {
            case "privatekey", "publickey", "presharedkey":
                add(value, .base64Key)
            case "address", "allowedips":
                for item in items(of: value) { add(item, addressRole(utf8.string(item)) ?? .argument) }
            case "dns":
                for item in items(of: value) { add(item, addressRole(utf8.string(item)) ?? .hostname) }
            case "endpoint":
                endpoint(value)
            case "listenport":
                add(value, isNumber(utf8.string(value)) ? .port : .argument)
            case "mtu", "persistentkeepalive", "fwmark":
                add(value, isNumber(utf8.string(value)) ? .number : .argument)
            default:
                add(value, .argument)
            }
        }

        /// `host:port` or `[IPv6]:port`.
        private mutating func endpoint(_ value: Range<String.Index>) {
            guard let colon = utf8[value].lastIndex(of: UInt8(ascii: ":")) else { return add(value, .argument) }
            var host = value.lowerBound..<colon
            if utf8[host].first == UInt8(ascii: "["), utf8[host].last == UInt8(ascii: "]") {
                host = utf8.index(after: host.lowerBound)..<utf8.index(before: host.upperBound)
            }
            let port = utf8.index(after: colon)..<value.upperBound
            add(host, isIPAddress(utf8.string(host)) ? .ipAddress : .hostname)
            add(port, isNumber(utf8.string(port)) ? .port : .argument)
        }

        /// The comma separated items of `value`, without their blanks.
        private func items(of value: Range<String.Index>) -> [Range<String.Index>] {
            var items: [Range<String.Index>] = []
            var start = value.lowerBound
            while true {
                let comma = utf8[start..<value.upperBound].firstIndex(of: UInt8(ascii: ",")) ?? value.upperBound
                let item = utf8.trimmed(start..<comma)
                if !item.isEmpty { items.append(item) }
                if comma == value.upperBound { return items }
                start = utf8.index(after: comma)
            }
        }

        // MARK: OpenVPN

        mutating func scanOpenVPN() {
            // The block whose lines are verbatim, with the lines seen in it so far.
            var block: (tag: String, body: Range<String.Index>?)?
            for line in utf8.lineRanges() {
                if let open = block {
                    if OpenVPNLine.closes(open.tag, line: line, in: utf8) {
                        add(open.body, .blockBody)
                        add(utf8.trimmed(line), .blockTag)
                        block = nil
                    } else {
                        block = (open.tag, (open.body?.lowerBound ?? line.lowerBound)..<line.upperBound)
                    }
                    continue
                }
                let parsed = OpenVPNLine(line, in: utf8)
                if let tag = parsed.openedBlock(in: utf8) {
                    add(parsed.fields[0], .blockTag)
                    add(parsed.comment, .comment)
                    // The body of a <connection> block is directives.
                    if tag != "connection" { block = (tag, nil) }
                } else {
                    directive(parsed)
                }
            }
            // A block that is never closed runs to the end, as it does for OpenVPN.
            add(block?.body, .blockBody)
        }

        private mutating func directive(_ line: OpenVPNLine) {
            if let name = line.fields.first {
                let text = utf8.string(name)
                let isClosingTag = line.fields.count == 1 && text.hasPrefix("</") && text.hasSuffix(">")
                add(name, isClosingTag ? .blockTag : .directive)
                let keyword = text.drop(while: { $0 == "-" }).lowercased()
                for (index, field) in line.fields.dropFirst().enumerated() {
                    add(field, argumentRole(index: index, of: keyword, utf8.string(field)))
                }
            }
            add(line.comment, .comment)
        }

        private func argumentRole(index: Int, of directive: String, _ field: String) -> ConfigToken.Role {
            var text = field
            if let quote = text.first, quote == "\"" || quote == "'", text.count > 1, text.last == quote {
                text = String(text.dropFirst().dropLast())
            }
            switch (directive, index) {
            case ("remote", 0), ("http-proxy", 0), ("socks-proxy", 0):
                return isIPAddress(text) ? .ipAddress : .hostname
            case ("remote", 1), ("http-proxy", 1), ("socks-proxy", 1), ("port", 0), ("lport", 0), ("rport", 0):
                return isNumber(text) ? .port : .argument
            default:
                return addressRole(text) ?? (isNumber(text) ? .number : .argument)
            }
        }

        // MARK: Shapes of values

        /// `ipAddress` for an address, `cidr` for an address with a prefix length.
        private func addressRole(_ text: String) -> ConfigToken.Role? {
            if isIPAddress(text) { return .ipAddress }
            if let slash = text.lastIndex(of: "/"), isIPAddress(String(text[..<slash])), isNumber(String(text[text.index(after: slash)...])) {
                return .cidr
            }
            return nil
        }

        private func isNumber(_ text: String) -> Bool {
            !text.isEmpty && text.utf8.allSatisfy { (UInt8(ascii: "0")...UInt8(ascii: "9")).contains($0) }
        }

        private func isIPAddress(_ text: String) -> Bool {
            var v4 = in_addr()
            var v6 = in6_addr()
            return inet_pton(AF_INET, text, &v4) == 1 || inet_pton(AF_INET6, text, &v6) == 1
        }
    }
}
