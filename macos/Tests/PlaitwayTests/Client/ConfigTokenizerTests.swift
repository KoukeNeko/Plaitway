import Foundation
import PlaitwayClient
import Testing

/// A token as the text it covers and its role, which reads better in an expectation than a range.
struct TokenPiece: Equatable, CustomStringConvertible {
    let text: String
    let role: ConfigToken.Role
    init(_ text: String, _ role: ConfigToken.Role) {
        self.text = text
        self.role = role
    }

    var description: String { "\(role) \(text.debugDescription)" }
}

private func pieces(_ text: String, _ kind: ProfileKind) -> [TokenPiece] {
    ConfigTokenizer.tokens(in: text, kind: kind).map { TokenPiece(String(text[$0.range]), $0.role) }
}

private func wireGuard(_ text: String) -> [TokenPiece] { pieces(text, .wireguard) }
private func openVPN(_ text: String) -> [TokenPiece] { pieces(text, .openvpn) }

private let privateKey = "kPRIVATEkMATERIALkAAAAAAAAAAAAAAAAAAAAAAAA="
private let publicKey = "kPUBLICkKEYkCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC="
private let presharedKey = "kPRESHAREDkMATERIALkBBBBBBBBBBBBBBBBBBBBBB="

/// What every token list has to satisfy, whatever the text: in order, inside the text, no overlap, no line breaks.
private func expectWellFormed(_ tokens: [ConfigToken], in text: String, sourceLocation: SourceLocation = #_sourceLocation) {
    var previousEnd = text.startIndex
    for token in tokens {
        #expect(!token.range.isEmpty, sourceLocation: sourceLocation)
        #expect(token.range.lowerBound >= previousEnd, "\(token) overlaps or precedes the token before it", sourceLocation: sourceLocation)
        #expect(token.range.upperBound <= text.endIndex, sourceLocation: sourceLocation)
        if token.role != .blockBody {
            #expect(!text[token.range].unicodeScalars.contains("\n"), "\(token.role) spans a line break", sourceLocation: sourceLocation)
        }
        previousEnd = token.range.upperBound
    }
}

struct WireGuardTokenizerTests {
    @Test func tokensAWgQuickProfile() {
        let text = """
        # Home router
        [Interface]
        PrivateKey = \(privateKey)
        Address = 10.6.0.2/32, fd00:6::2/128
        ListenPort = 51820
        DNS = 10.6.0.1, 1.1.1.1, home.lan
        MTU = 1380

        [Peer]
        PublicKey = \(publicKey)
        PresharedKey = \(presharedKey)
        AllowedIPs = 0.0.0.0/0, ::/0
        Endpoint = vpn.example.com:51820
        PersistentKeepalive = 25

        """

        #expect(wireGuard(text) == [
            TokenPiece("# Home router", .comment),
            TokenPiece("[Interface]", .section),
            TokenPiece("PrivateKey", .directive), TokenPiece(privateKey, .base64Key),
            TokenPiece("Address", .directive), TokenPiece("10.6.0.2/32", .cidr), TokenPiece("fd00:6::2/128", .cidr),
            TokenPiece("ListenPort", .directive), TokenPiece("51820", .port),
            TokenPiece("DNS", .directive), TokenPiece("10.6.0.1", .ipAddress), TokenPiece("1.1.1.1", .ipAddress), TokenPiece("home.lan", .hostname),
            TokenPiece("MTU", .directive), TokenPiece("1380", .number),
            TokenPiece("[Peer]", .section),
            TokenPiece("PublicKey", .directive), TokenPiece(publicKey, .base64Key),
            TokenPiece("PresharedKey", .directive), TokenPiece(presharedKey, .base64Key),
            TokenPiece("AllowedIPs", .directive), TokenPiece("0.0.0.0/0", .cidr), TokenPiece("::/0", .cidr),
            TokenPiece("Endpoint", .directive), TokenPiece("vpn.example.com", .hostname), TokenPiece("51820", .port),
            TokenPiece("PersistentKeepalive", .directive), TokenPiece("25", .number),
        ])
        expectWellFormed(ConfigTokenizer.tokens(in: text, kind: .wireguard), in: text)
    }

    @Test(arguments: [
        ("Endpoint = 203.0.113.5:51820", [TokenPiece("203.0.113.5", .ipAddress), TokenPiece("51820", .port)]),
        ("Endpoint = [2001:db8::1]:51820", [TokenPiece("2001:db8::1", .ipAddress), TokenPiece("51820", .port)]),
        ("Endpoint = vpn.example.com:443", [TokenPiece("vpn.example.com", .hostname), TokenPiece("443", .port)]),
        ("Endpoint = vpn.example.com", [TokenPiece("vpn.example.com", .argument)]),
        ("Endpoint = vpn.example.com:https", [TokenPiece("vpn.example.com", .hostname), TokenPiece("https", .argument)]),
    ])
    func splitsAnEndpointIntoHostAndPort(line: String, value: [TokenPiece]) {
        #expect(wireGuard(line) == [TokenPiece("Endpoint", .directive)] + value)
    }

    @Test func listsAreSplitAtTheCommasWhateverTheSpacing() {
        #expect(wireGuard("AllowedIPs=0.0.0.0/1,128.0.0.0/1 ,  ::/0") == [
            TokenPiece("AllowedIPs", .directive), TokenPiece("0.0.0.0/1", .cidr), TokenPiece("128.0.0.0/1", .cidr), TokenPiece("::/0", .cidr),
        ])
        #expect(wireGuard("DNS = 1.1.1.1,, lan.example,") == [
            TokenPiece("DNS", .directive), TokenPiece("1.1.1.1", .ipAddress), TokenPiece("lan.example", .hostname),
        ])
        #expect(wireGuard("Address = 10.0.0.2") == [TokenPiece("Address", .directive), TokenPiece("10.0.0.2", .ipAddress)])
        #expect(wireGuard("AllowedIPs = 10.0.0.0/eight, 10.0.0.256/8") == [
            TokenPiece("AllowedIPs", .directive), TokenPiece("10.0.0.0/eight", .argument), TokenPiece("10.0.0.256/8", .argument),
        ])
    }

    @Test func keysAreMatchedWithoutRegardForCaseAndSpacing() {
        #expect(wireGuard("\tlistenport\t=\t51820\t") == [TokenPiece("listenport", .directive), TokenPiece("51820", .port)])
        #expect(wireGuard("PERSISTENTKEEPALIVE=off") == [TokenPiece("PERSISTENTKEEPALIVE", .directive), TokenPiece("off", .argument)])
        #expect(wireGuard("Table = off") == [TokenPiece("Table", .directive), TokenPiece("off", .argument)])
        #expect(wireGuard("FwMark = 0x1234") == [TokenPiece("FwMark", .directive), TokenPiece("0x1234", .argument)])
        #expect(wireGuard("PostUp = iptables -A FORWARD -i %i -j ACCEPT") == [
            TokenPiece("PostUp", .directive), TokenPiece("iptables -A FORWARD -i %i -j ACCEPT", .argument),
        ])
        #expect(wireGuard("publickey=\(publicKey)") == [TokenPiece("publickey", .directive), TokenPiece(publicKey, .base64Key)])
    }

    @Test func aCommentRunsFromTheHashToTheEndOfTheLine() {
        #expect(wireGuard("Address = 10.0.0.2/32 # the LAN, 10.0.0.0/8 = nothing") == [
            TokenPiece("Address", .directive), TokenPiece("10.0.0.2/32", .cidr), TokenPiece("# the LAN, 10.0.0.0/8 = nothing", .comment),
        ])
        #expect(wireGuard("[Peer]# laptop") == [TokenPiece("[Peer]", .section), TokenPiece("# laptop", .comment)])
        #expect(wireGuard("  #PrivateKey = \(privateKey)") == [TokenPiece("#PrivateKey = \(privateKey)", .comment)])
        #expect(wireGuard("PrivateKey = # none yet") == [TokenPiece("PrivateKey", .directive), TokenPiece("# none yet", .comment)])
    }

    @Test func aLineThatIsNotADirectiveHasNoRole() {
        #expect(wireGuard("just some words\n\n   \n") == [])
        #expect(wireGuard("=value") == [TokenPiece("value", .argument)])
        #expect(wireGuard("[Interface") == [TokenPiece("[Interface", .section)])
    }

    @Test func linesEndingInCRLFHaveNoCRInTheirTokens() {
        let text = "[Interface]\r\nAddress = 10.0.0.2/32 # lan\r\nDNS = 1.1.1.1\r\n"
        #expect(wireGuard(text) == [
            TokenPiece("[Interface]", .section),
            TokenPiece("Address", .directive), TokenPiece("10.0.0.2/32", .cidr), TokenPiece("# lan", .comment),
            TokenPiece("DNS", .directive), TokenPiece("1.1.1.1", .ipAddress),
        ])
    }

    @Test func aByteOrderMarkAtTheStartBelongsToNoToken() {
        #expect(wireGuard("\u{FEFF}[Interface]\nAddress = 10.0.0.2/32\n") == [
            TokenPiece("[Interface]", .section), TokenPiece("Address", .directive), TokenPiece("10.0.0.2/32", .cidr),
        ])
        #expect(openVPN("\u{FEFF}client\nverb 3\n") == [TokenPiece("client", .directive), TokenPiece("verb", .directive), TokenPiece("3", .number)])
    }

    @Test func textInOtherScriptsKeepsItsRanges() {
        let text = "# 備註 🌐\nAddress = 10.0.0.2/32 # 筆電\nEndpoint = 例え.example:51820\n"
        #expect(wireGuard(text) == [
            TokenPiece("# 備註 🌐", .comment),
            TokenPiece("Address", .directive), TokenPiece("10.0.0.2/32", .cidr), TokenPiece("# 筆電", .comment),
            TokenPiece("Endpoint", .directive), TokenPiece("例え.example", .hostname), TokenPiece("51820", .port),
        ])
    }
}

struct OpenVPNTokenizerTests {
    @Test func tokensAProfileLineByLine() {
        let text = """
        # Name: Office
        client
        dev tun
        proto tcp-client
        remote vpn.example.net 1194 ; the main server
        remote 203.0.113.7 443 udp
        keepalive 10 30
        route 192.168.1.0 255.255.255.0
        route-ipv6 2001:db8::/32
        dhcp-option DNS 192.168.1.1
        pull-filter ignore "redirect-gateway"
        port 1194
        <ca>
        -----BEGIN CERTIFICATE-----
        AAAA
        -----END CERTIFICATE-----
        </ca>
        verb 3

        """

        #expect(openVPN(text) == [
            TokenPiece("# Name: Office", .comment),
            TokenPiece("client", .directive),
            TokenPiece("dev", .directive), TokenPiece("tun", .argument),
            TokenPiece("proto", .directive), TokenPiece("tcp-client", .argument),
            TokenPiece("remote", .directive), TokenPiece("vpn.example.net", .hostname), TokenPiece("1194", .port), TokenPiece("; the main server", .comment),
            TokenPiece("remote", .directive), TokenPiece("203.0.113.7", .ipAddress), TokenPiece("443", .port), TokenPiece("udp", .argument),
            TokenPiece("keepalive", .directive), TokenPiece("10", .number), TokenPiece("30", .number),
            TokenPiece("route", .directive), TokenPiece("192.168.1.0", .ipAddress), TokenPiece("255.255.255.0", .ipAddress),
            TokenPiece("route-ipv6", .directive), TokenPiece("2001:db8::/32", .cidr),
            TokenPiece("dhcp-option", .directive), TokenPiece("DNS", .argument), TokenPiece("192.168.1.1", .ipAddress),
            TokenPiece("pull-filter", .directive), TokenPiece("ignore", .argument), TokenPiece("\"redirect-gateway\"", .argument),
            TokenPiece("port", .directive), TokenPiece("1194", .port),
            TokenPiece("<ca>", .blockTag),
            TokenPiece("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----", .blockBody),
            TokenPiece("</ca>", .blockTag),
            TokenPiece("verb", .directive), TokenPiece("3", .number),
        ])
        expectWellFormed(ConfigTokenizer.tokens(in: text, kind: .openvpn), in: text)
    }

    @Test func aCommentStartsWhereAParameterWould() {
        #expect(openVPN("# one\n; two\n  # three\n\t; four") == [
            TokenPiece("# one", .comment), TokenPiece("; two", .comment), TokenPiece("# three", .comment), TokenPiece("; four", .comment),
        ])
        // In the middle of a parameter `#` and `;` are just characters.
        #expect(openVPN("dhcp-option DOMAIN lan#x;y") == [TokenPiece("dhcp-option", .directive), TokenPiece("DOMAIN", .argument), TokenPiece("lan#x;y", .argument)])
        #expect(openVPN("verb 3 # chatty") == [TokenPiece("verb", .directive), TokenPiece("3", .number), TokenPiece("# chatty", .comment)])
        #expect(openVPN("verb 3 #chatty;still the comment") == [TokenPiece("verb", .directive), TokenPiece("3", .number), TokenPiece("#chatty;still the comment", .comment)])
    }

    @Test func quotedParametersAreOneTokenWithTheirQuotes() {
        #expect(openVPN(#"remote "vpn.example.net" 1194"#) == [TokenPiece("remote", .directive), TokenPiece("\"vpn.example.net\"", .hostname), TokenPiece("1194", .port)])
        #expect(openVPN(#"setenv NAME "two words # not a comment" 'single ; quoted'"#) == [
            TokenPiece("setenv", .directive), TokenPiece("NAME", .argument),
            TokenPiece("\"two words # not a comment\"", .argument), TokenPiece("'single ; quoted'", .argument),
        ])
        #expect(openVPN(#"push "a \" quote" next"#) == [TokenPiece("push", .directive), TokenPiece(#""a \" quote""#, .argument), TokenPiece("next", .argument)])
        // A quote that is never closed runs to the end of the line.
        #expect(openVPN("push \"open\nverb 3") == [TokenPiece("push", .directive), TokenPiece("\"open", .argument), TokenPiece("verb", .directive), TokenPiece("3", .number)])
    }

    @Test(arguments: ["remote", "--remote", "http-proxy", "socks-proxy"])
    func readsTheHostAndThePortOfAServerDirective(written: String) {
        #expect(openVPN("\(written) proxy.example.net 8080") == [TokenPiece(written, .directive), TokenPiece("proxy.example.net", .hostname), TokenPiece("8080", .port)])
        #expect(openVPN("\(written) 2001:db8::10 8080") == [TokenPiece(written, .directive), TokenPiece("2001:db8::10", .ipAddress), TokenPiece("8080", .port)])
    }

    @Test func aBlockBodyIsOneTokenWhateverItHolds() {
        let text = "<tls-crypt>\n#\n-----BEGIN OpenVPN Static key V1-----\n  ffee  \n\nremote not a directive 1\n-----END OpenVPN Static key V1-----\n</tls-crypt>\n"
        #expect(openVPN(text) == [
            TokenPiece("<tls-crypt>", .blockTag),
            TokenPiece("#\n-----BEGIN OpenVPN Static key V1-----\n  ffee  \n\nremote not a directive 1\n-----END OpenVPN Static key V1-----", .blockBody),
            TokenPiece("</tls-crypt>", .blockTag),
        ])
    }

    @Test func theTagsOfABlockAreFoundWhateverTheSpacingAroundThem() {
        let text = "  <key>  \t\n  body  \n\t</key>   \n<TLS-AUTH> # static key\nkey data\n</tls-auth>"
        #expect(openVPN(text) == [
            TokenPiece("<key>", .blockTag),
            TokenPiece("  body  ", .blockBody),
            TokenPiece("</key>", .blockTag),
            TokenPiece("<TLS-AUTH>", .blockTag), TokenPiece("# static key", .comment),
            TokenPiece("key data", .blockBody),
            TokenPiece("</tls-auth>", .blockTag),
        ])
    }

    @Test func aBlockWithoutABodyHasNoBodyToken() {
        #expect(openVPN("<ca>\n</ca>\nverb 3\n") == [TokenPiece("<ca>", .blockTag), TokenPiece("</ca>", .blockTag), TokenPiece("verb", .directive), TokenPiece("3", .number)])
    }

    @Test func aBlockThatIsNeverClosedRunsToTheEnd() {
        #expect(openVPN("verb 3\n<ca>\nAAAA\nverb 4\n") == [
            TokenPiece("verb", .directive), TokenPiece("3", .number), TokenPiece("<ca>", .blockTag), TokenPiece("AAAA\nverb 4", .blockBody),
        ])
    }

    @Test func aTagInACommentOrAnotherBlockOpensNothing() {
        #expect(openVPN("# <ca>\nverb 3\n") == [TokenPiece("# <ca>", .comment), TokenPiece("verb", .directive), TokenPiece("3", .number)])
        #expect(openVPN("<ca>\n<key>\n</ca>\n") == [TokenPiece("<ca>", .blockTag), TokenPiece("<key>", .blockBody), TokenPiece("</ca>", .blockTag)])
        // Three parameters are not a tag.
        #expect(openVPN("<ca> extra\n") == [TokenPiece("<ca>", .directive), TokenPiece("extra", .argument)])
    }

    @Test func theLinesOfAConnectionBlockAreDirectives() throws {
        let text = try repositoryText("internal/ovpn/testdata/connection-blocks.ovpn")
        let found = openVPN(text)

        #expect(found.filter { $0.role == .blockTag }.map(\.text) == ["<connection>", "</connection>", "<connection>", "</connection>", "<connection>", "</connection>", "<ca>", "</ca>"])
        #expect(found.contains(TokenPiece("primary.example.com", .hostname)))
        #expect(found.contains(TokenPiece("2001:db8::10", .ipAddress)))
        #expect(found.contains(TokenPiece("8443", .port)))
        #expect(found.filter { $0.role == .blockBody }.map(\.text) == ["-----BEGIN CERTIFICATE-----\nY29ubmVjdGlvbiBjYQ==\n-----END CERTIFICATE-----"])
    }

    @Test func aCRLFProfileHasNoCRAtTheEndOfAnyToken() throws {
        let text = try repositoryText("internal/ovpn/testdata/windows.ovpn")
        #expect(text.contains("\r\n"))
        let found = openVPN(text)

        #expect(found.allSatisfy { !$0.text.hasSuffix("\r") && !$0.text.hasPrefix("\r") })
        #expect(found.contains(TokenPiece("remote", .directive)) && found.contains(TokenPiece("vpn.example.org", .hostname)) && found.contains(TokenPiece("443", .port)))
        #expect(found.first == TokenPiece("# Windows-style export, CRLF line endings", .comment))
        let key = try #require(found.first { $0.role == .blockBody && $0.text.contains("PRIVATE KEY") })
        #expect(key.text == "-----BEGIN PRIVATE KEY-----\r\nZmFrZSBrZXk=\r\n-----END PRIVATE KEY-----")
    }

    @Test func aStrayClosingTagIsATag() {
        #expect(openVPN("</connection>\n") == [TokenPiece("</connection>", .blockTag)])
    }

    @Test func tokensTheRouterProfiles() throws {
        let asus = openVPN(try repositoryText("internal/ovpn/testdata/asus.ovpn"))
        #expect(asus.first == TokenPiece("# Name: ASUS Router", .comment))
        for piece in [
            TokenPiece("auth-user-pass", .directive), TokenPiece("vpn.example.net", .hostname), TokenPiece("1194", .port), TokenPiece("tcp-client", .argument),
            TokenPiece("\"redirect-gateway\"", .argument), TokenPiece("192.168.1.0", .ipAddress), TokenPiece("255.255.255.0", .ipAddress), TokenPiece("AES-128-CBC", .argument),
        ] {
            #expect(asus.contains(piece), "\(piece)")
        }
        #expect(asus.filter { $0.role == .blockTag }.map(\.text) == ["<ca>", "</ca>", "<cert>", "</cert>", "<key>", "</key>"])
        #expect(asus.filter { $0.role == .blockBody }.count == 3)

        let merlin = openVPN(try repositoryText("internal/ovpn/testdata/merlin.ovpn"))
        for piece in [
            TokenPiece("# Exported from an ASUSWRT-Merlin router", .comment), TokenPiece("203.0.113.7", .ipAddress), TokenPiece("udp4", .argument),
            TokenPiece("AES-256-GCM:AES-128-GCM:AES-128-CBC", .argument), TokenPiece("192.168.1.1", .ipAddress), TokenPiece("def1", .argument),
        ] {
            #expect(merlin.contains(piece), "\(piece)")
        }
        #expect(merlin.filter { $0.role == .blockBody }.count == 4)
    }
}

struct TokenizerRobustnessTests {
    @Test func noTokensForAnUnknownKind() {
        #expect(ConfigTokenizer.tokens(in: "client\nremote a 1\n", kind: .unspecified).isEmpty)
        #expect(ConfigTokenizer.tokens(in: "[Interface]\n", kind: .UNRECOGNIZED(9)).isEmpty)
        #expect(ConfigTokenizer.tokens(in: "", kind: .openvpn).isEmpty)
        #expect(ConfigTokenizer.tokens(in: "", kind: .wireguard).isEmpty)
    }

    @Test func theRepositoryProfilesAreWellFormed() throws {
        for name in ["asus", "merlin", "windows", "connection-blocks"] {
            let text = try repositoryText("internal/ovpn/testdata/\(name).ovpn")
            expectWellFormed(ConfigTokenizer.tokens(in: text, kind: .openvpn), in: text)
            // A profile of the wrong kind is still read without trouble.
            expectWellFormed(ConfigTokenizer.tokens(in: text, kind: .wireguard), in: text)
        }
    }

    @Test(arguments: 0..<300)
    func anyTextGivesWellFormedTokens(seed: Int) {
        var random = SeededGenerator(state: UInt64(seed))
        let alphabet = Array("<>[]=#; \"'\\\t\r\n\n/:,.-_abcdefXYZ019é備🌐‹›")
        let text = String((0..<Int.random(in: 0...240, using: &random)).map { _ in alphabet.randomElement(using: &random)! })

        for kind in [ProfileKind.openvpn, .wireguard] {
            expectWellFormed(ConfigTokenizer.tokens(in: text, kind: kind), in: text)
        }
    }
}
