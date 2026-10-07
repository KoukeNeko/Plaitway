import Foundation
import PlaitwayClient
import Testing

private let privateKey = "kPRIVATEkMATERIALkAAAAAAAAAAAAAAAAAAAAAAAA="
private let presharedKey = "kPRESHAREDkMATERIALkBBBBBBBBBBBBBBBBBBBBBB="

private let wireGuardProfile = """
[Interface]
PrivateKey = \(privateKey)
Address = 10.6.0.2/32
DNS = 10.6.0.1

[Peer]
PublicKey = kPUBLICkKEYkCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=
PresharedKey = \(presharedKey)
AllowedIPs = 0.0.0.0/0
Endpoint = 203.0.113.5:51820

"""

private func wireGuard(_ text: String) -> SecretMask { SecretMask(text: text, kind: .wireguard) }
private func openVPN(_ text: String) -> SecretMask { SecretMask(text: text, kind: .openvpn) }

private func restored(_ mask: SecretMask, _ edited: String) throws -> String {
    try mask.restore(edited).text
}

struct SecretMaskTests {
    // MARK: WireGuard

    @Test func hidesTheKeysOfAWireGuardProfile() throws {
        let mask = wireGuard(wireGuardProfile)

        #expect(mask.displayText == wireGuardProfile
            .replacingOccurrences(of: privateKey, with: "‹secret 1›")
            .replacingOccurrences(of: presharedKey, with: "‹secret 2›"))
        #expect(try restored(mask, mask.displayText) == wireGuardProfile)
    }

    @Test(arguments: [
        "PrivateKey=§",
        "privatekey = §",
        "PRIVATEKEY\t=\t§",
        "   PrivateKey   =   §   ",
        "PrivateKey = §# no space before the comment",
        "PrivateKey = §   # a comment",
    ])
    func findsTheValueWhateverTheSpacingAndTheTrailingComment(line: String) throws {
        let text = "[Interface]\n\(line)\nAddress = 10.0.0.2/32\n"
        let mask = wireGuard(text.replacingOccurrences(of: "§", with: privateKey))

        #expect(!mask.displayText.contains(privateKey))
        #expect(mask.displayText.contains("Address = 10.0.0.2/32"))
        // Only the value is hidden, not what stands around it.
        #expect(mask.displayText == text.replacingOccurrences(of: "§", with: "‹secret 1›"))
        #expect(try restored(mask, mask.displayText) == text.replacingOccurrences(of: "§", with: privateKey))
    }

    /// The daemon trims a line with Go's strings.TrimSpace, which takes in the no-break space, the
    /// ideographic space and the rest of Unicode white space: a key behind one of them is accepted.
    @Test(arguments: ["\u{A0}", "\u{3000}", "\u{2003}", "\u{85}", "\u{B}", "\u{C}", "\u{202F}", "\u{1680}"])
    func aKeyBehindUnicodeWhiteSpaceIsHidden(space: String) throws {
        for name in ["PrivateKey", "PresharedKey"] {
            for text in [
                "[Interface]\n\(space)\(name) = \(privateKey)\n",
                "[Interface]\n\(name)\(space)=\(space)\(privateKey)\(space)\n",
                "[Interface]\n#\(space)\(name)\(space)=\(privateKey)\n",
            ] {
                let mask = wireGuard(text)
                #expect(!mask.displayText.contains(privateKey), "\(name) behind U+\(String(space.unicodeScalars.first!.value, radix: 16)) is shown: \(mask.displayText)")
                #expect(mask.displayText.contains("‹secret 1›"))
                #expect(try restored(mask, mask.displayText) == text)
            }
        }
    }

    @Test func aCommentAfterTheKeyStaysVisible() {
        let mask = wireGuard("PrivateKey = \(privateKey) # laptop key\n")
        #expect(mask.displayText == "PrivateKey = ‹secret 1› # laptop key\n")
    }

    @Test func aKeyThatIsCommentedOutIsStillHidden() throws {
        let text = "[Interface]\n# PrivateKey = \(privateKey)\n#PresharedKey=\(presharedKey)\n"
        let mask = wireGuard(text)
        #expect(mask.displayText == "[Interface]\n# PrivateKey = ‹secret 1›\n#PresharedKey=‹secret 2›\n")
        #expect(try restored(mask, mask.displayText) == text)
    }

    @Test func aKeyWithoutAValueHasNothingToHide() {
        let text = "[Interface]\nPrivateKey =\nPrivateKey = # nothing\nPrivateKey\n"
        #expect(wireGuard(text).displayText == text)
    }

    @Test func otherKeysAndTheirValuesAreLeftAlone() {
        let text = "[Interface]\nPrivateKeyFile = /etc/key\nMyPrivateKey = x\nAddress = 10.0.0.2/32 # PrivateKey = x\n"
        #expect(wireGuard(text).displayText == text)
    }

    @Test func keepsCRLFLineEndingsOfAWireGuardProfile() throws {
        let text = wireGuardProfile.replacingOccurrences(of: "\n", with: "\r\n")
        let mask = wireGuard(text)

        #expect(mask.displayText.contains("PrivateKey = ‹secret 1›\r\n"))
        #expect(!mask.displayText.contains(privateKey))
        #expect(try restored(mask, mask.displayText) == text)
    }

    @Test func keepsAByteOrderMarkAndTextInOtherScripts() throws {
        let text = "\u{FEFF}# 備註 ‹ 🌐\n[Interface]\nPrivateKey = \(privateKey) # 筆電\n"
        let mask = wireGuard(text)

        #expect(mask.displayText == "\u{FEFF}# 備註 ‹ 🌐\n[Interface]\nPrivateKey = ‹secret 1› # 筆電\n")
        #expect(try restored(mask, mask.displayText) == text)
    }

    @Test func aKeyOnTheFirstLineIsHiddenBehindAByteOrderMark() throws {
        let text = "\u{FEFF}PrivateKey = \(privateKey)\n"
        let mask = wireGuard(text)
        #expect(mask.displayText == "\u{FEFF}PrivateKey = ‹secret 1›\n")
        #expect(try restored(mask, mask.displayText) == text)
    }

    // MARK: OpenVPN

    @Test func hidesTheKeyBlocksOfTheRouterProfile() throws {
        let text = try repositoryText("internal/ovpn/testdata/merlin.ovpn")
        let mask = openVPN(text)

        let lines = mask.displayText.split(separator: "\n", omittingEmptySubsequences: false).map(String.init)
        // The certificates are public and stay; the private key and the tls-crypt key do not.
        #expect(lines.contains("<ca>") && lines.contains("bWVybGluIGNh"))
        #expect(lines.contains("bWVybGluIGNlcnQ="))
        #expect(!lines.contains("bWVybGluIGtleQ=="))
        #expect(!lines.contains("ffeeddccbbaa99887766554433221100"))
        #expect(mask.displayText.contains("<key>\n‹secret 1›\n</key>"))
        #expect(mask.displayText.contains("<tls-crypt>\n‹secret 2›\n</tls-crypt>"))
        #expect(try restored(mask, mask.displayText) == text)
    }

    @Test func hidesTheKeyBlocksOfAProfileWithCRLF() throws {
        let text = try repositoryText("internal/ovpn/testdata/windows.ovpn")
        #expect(text.contains("\r\n"))
        let mask = openVPN(text)

        #expect(mask.displayText.contains("<key>\r\n‹secret 1›\r\n</key>\r\n"))
        #expect(mask.displayText.contains("<tls-auth>\r\n‹secret 2›\r\n</tls-auth>"))
        #expect(!mask.displayText.contains("ZmFrZSBrZXk="))
        #expect(try restored(mask, mask.displayText) == text)
    }

    @Test func aSecretOfSeveralLinesComesBackWithItsOwnLineBreaks() throws {
        let body = "-----BEGIN PRIVATE KEY-----\r\nAAAA\r\nBBBB\r\n-----END PRIVATE KEY-----"
        let text = "client\r\n<key>\r\n\(body)\r\n</key>\r\nverb 3\r\n"
        let mask = openVPN(text)

        #expect(mask.displayText == "client\r\n<key>\r\n‹secret 1›\r\n</key>\r\nverb 3\r\n")
        #expect(try restored(mask, mask.displayText) == text)
    }

    @Test func leavesTheOtherBlocksAndTheDirectivesAlone() throws {
        for name in ["asus", "connection-blocks"] {
            let text = try repositoryText("internal/ovpn/testdata/\(name).ovpn")
            let mask = openVPN(text)
            let hidden = text.contains("<key>") || text.contains("<tls-")
            #expect((mask.displayText != text) == hidden, "\(name)")
            #expect(try restored(mask, mask.displayText) == text, "\(name)")
            #expect(mask.displayText.contains("<ca>\n-----BEGIN CERTIFICATE-----") || !text.contains("<ca>"), "\(name)")
        }
    }

    @Test(arguments: ["tls-auth", "tls-crypt", "tls-crypt-v2", "pkcs12", "secret", "auth-user-pass", "http-proxy-user-pass", "key"])
    func hidesEverySecretBlock(tag: String) throws {
        let text = "client\n<\(tag)>\nline one\nline two\n</\(tag)>\nverb 3\n"
        let mask = openVPN(text)

        #expect(mask.displayText == "client\n<\(tag)>\n‹secret 1›\n</\(tag)>\nverb 3\n")
        #expect(try restored(mask, mask.displayText) == text)
    }

    @Test(arguments: ["ca", "cert", "dh", "crl-verify", "extra-certs", "peer-fingerprint", "connection"])
    func showsTheBlocksThatHoldNothingSecret(tag: String) {
        let text = "client\n<\(tag)>\nline one\nline two\n</\(tag)>\nverb 3\n"
        #expect(openVPN(text).displayText == text)
    }

    @Test func findsABlockWhateverTheSpacingAroundItsTags() throws {
        let text = "client\n  <key>  \t\n  body line  \n\t</key>   \n<TLS-CRYPT> # the static key\nkey data\n</tls-crypt>\n"
        let mask = openVPN(text)

        #expect(mask.displayText == "client\n  <key>  \t\n‹secret 1›\n\t</key>   \n<TLS-CRYPT> # the static key\n‹secret 2›\n</tls-crypt>\n")
        #expect(try restored(mask, mask.displayText) == text)
    }

    @Test func aTagInACommentOrInsideAnotherBlockOpensNothing() {
        let text = """
        client
        # <key>
        ; <tls-auth>
        <ca>
        <key>
        not a block, a line of the certificate
        </ca>
        verb 3

        """
        #expect(openVPN(text).displayText == text)
    }

    @Test func aBlockWithoutABodyHidesNothing() {
        let text = "client\n<key>\n</key>\n<tls-auth>\n \t\n\n</tls-auth>\n"
        #expect(openVPN(text).displayText == text)
    }

    @Test func aBlockThatIsNeverClosedRunsToTheEnd() throws {
        let text = "client\n<key>\nSECRET LINE ONE\nSECRET LINE TWO\n"
        let mask = openVPN(text)

        #expect(mask.displayText == "client\n<key>\n‹secret 1›\n")
        #expect(try restored(mask, mask.displayText) == text)
    }

    @Test func aBlockWithoutALineBreakAtTheEndStillCloses() throws {
        let text = "client\n<key>\nSECRET\n</key>"
        let mask = openVPN(text)
        #expect(mask.displayText == "client\n<key>\n‹secret 1›\n</key>")
        #expect(try restored(mask, mask.displayText) == text)
    }

    // MARK: PEM text outside the blocks

    @Test func aPrivateKeyOutsideAnyBlockIsHidden() throws {
        let key = "-----BEGIN PRIVATE KEY-----\nAAAA\nBBBB\n-----END PRIVATE KEY-----"
        let text = "client\n\(key)\nverb 3\n"
        let mask = openVPN(text)

        #expect(mask.displayText == "client\n‹secret 1›\nverb 3\n")
        #expect(try restored(mask, mask.displayText) == text)
    }

    @Test(arguments: [
        ("ENCRYPTED PRIVATE KEY", true), ("RSA PRIVATE KEY", true), ("EC PRIVATE KEY", true),
        ("OpenVPN Static key V1", true), ("OpenVPN tls-crypt-v2 client key", true),
        ("CERTIFICATE", false), ("PUBLIC KEY", false), ("DH PARAMETERS", false),
    ])
    func hidesPEMTextOutsideBlocksOnlyWhenItIsAKey(label: String, secret: Bool) {
        let text = "client\n  -----BEGIN \(label)-----\n  AAAA\n  -----END \(label)-----\n"
        #expect((openVPN(text).displayText != text) == secret)
    }

    @Test func aPrivateKeyInsideAnotherBlockIsHiddenWithoutHidingTheBlock() throws {
        let text = "client\n<cert>\n-----BEGIN CERTIFICATE-----\nCCCC\n-----END CERTIFICATE-----\n-----BEGIN PRIVATE KEY-----\nKKKK\n-----END PRIVATE KEY-----\n</cert>\n"
        let mask = openVPN(text)

        #expect(mask.displayText == "client\n<cert>\n-----BEGIN CERTIFICATE-----\nCCCC\n-----END CERTIFICATE-----\n‹secret 1›\n</cert>\n")
        #expect(try restored(mask, mask.displayText) == text)
    }

    @Test func pemTextWithoutItsEndLineIsNotAKey() {
        let text = "client\n-----BEGIN PRIVATE KEY-----\nAAAA\nverb 3\n"
        #expect(openVPN(text).displayText == text)
    }

    @Test func aPrivateKeyInAWireGuardFileIsHiddenToo() throws {
        let text = "[Interface]\n-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"
        let mask = wireGuard(text)
        #expect(mask.displayText == "[Interface]\n‹secret 1›\n")
        #expect(try restored(mask, mask.displayText) == text)
    }

    // MARK: Placeholders that were there before

    @Test func aTextThatAlreadyHasAPlaceholderGetsNumbersThatDoNotCollideWithIt() throws {
        let text = "# was ‹secret 1› and ‹secret 3›\n[Interface]\nPrivateKey = \(privateKey)\nPresharedKey = \(presharedKey)\n"
        let mask = wireGuard(text)

        #expect(mask.displayText == "# was ‹secret 1› and ‹secret 3›\n[Interface]\nPrivateKey = ‹secret 2›\nPresharedKey = ‹secret 4›\n")
        #expect(try restored(mask, mask.displayText) == text)
    }

    @Test func findsPlaceholdersByTheirExactShape() {
        let text = "a ‹secret 1› b ‹secret 12›‹secret 7› ‹secret 0› ‹secret 01› ‹secret › ‹secret x› ‹secret 5 ‹Secret 6› ‹secret 99999999999999999999›"
        let found = SecretMask.placeholderRanges(in: text).map { String(text[$0]) }
        #expect(found == ["‹secret 1›", "‹secret 12›", "‹secret 7›"])
    }

    // MARK: Edits

    private static let ovpnText = "client\nremote vpn.example.net 1194\n<key>\nKEY BODY\n</key>\n<tls-crypt>\nCRYPT BODY\n</tls-crypt>\nverb 3\n"

    @Test func textTypedNextToAPlaceholderDoesNotTouchTheSecret() throws {
        let mask = wireGuard(wireGuardProfile)
        let edited = mask.displayText
            .replacingOccurrences(of: "PrivateKey = ‹secret 1›", with: "PrivateKey = ‹secret 1›abc")
            .replacingOccurrences(of: "PresharedKey = ‹secret 2›", with: "PresharedKey = xyz‹secret 2›")

        #expect(try restored(mask, edited) == wireGuardProfile
            .replacingOccurrences(of: "PrivateKey = \(privateKey)", with: "PrivateKey = \(privateKey)abc")
            .replacingOccurrences(of: "PresharedKey = \(presharedKey)", with: "PresharedKey = xyz\(presharedKey)"))
    }

    @Test func aPlaceholderThatIsDeletedTakesItsSecretWithIt() throws {
        let mask = openVPN(Self.ovpnText)
        let edited = mask.displayText.replacingOccurrences(of: "‹secret 1›\n", with: "")

        let result = try restored(mask, edited)
        #expect(result == "client\nremote vpn.example.net 1194\n<key>\n</key>\n<tls-crypt>\nCRYPT BODY\n</tls-crypt>\nverb 3\n")
        #expect(!result.contains("KEY BODY"))
    }

    @Test func textTypedOverAPlaceholderReplacesTheSecret() throws {
        let mask = wireGuard(wireGuardProfile)
        let edited = mask.displayText.replacingOccurrences(of: "‹secret 1›", with: "NEW-PRIVATE-KEY")

        let result = try restored(mask, edited)
        #expect(result.contains("PrivateKey = NEW-PRIVATE-KEY\n"))
        #expect(!result.contains(privateKey))
        #expect(result.contains("PresharedKey = \(presharedKey)\n"))
    }

    @Test(arguments: ["‹secret 1", "secret 1›", "‹secret ›", "‹secret1›", "‹Secret 1›", "‹secret 1 ›", "secret 1", "‹secret 01›"])
    func aDamagedPlaceholderIsTextAndItsSecretIsGone(damaged: String) throws {
        let mask = wireGuard("[Interface]\nPrivateKey = \(privateKey)\n")
        let edited = mask.displayText.replacingOccurrences(of: "‹secret 1›", with: damaged)

        #expect(try restored(mask, edited) == "[Interface]\nPrivateKey = \(damaged)\n")
    }

    @Test func aPlaceholderOfAnUnknownNumberStaysAsItIs() throws {
        let mask = wireGuard("[Interface]\nPrivateKey = \(privateKey)\n")
        let edited = mask.displayText + "# ‹secret 2› ‹secret 9›\n"

        #expect(try restored(mask, edited) == "[Interface]\nPrivateKey = \(privateKey)\n# ‹secret 2› ‹secret 9›\n")
    }

    @Test func aPlaceholderThatIsCopiedIsAnErrorNotAGuess() throws {
        let mask = openVPN(Self.ovpnText)
        let edited = mask.displayText + "# ‹secret 2›\n"

        #expect(throws: SecretMask.Failure.duplicatedPlaceholder(number: 2, line: 10)) { try mask.restore(edited) }
    }

    @Test func aMovedPlaceholderCarriesItsSecretAlong() throws {
        let mask = openVPN(Self.ovpnText)
        // Cut the first block's placeholder and paste it into the second block, and the other way round.
        let edited = mask.displayText
            .replacingOccurrences(of: "<key>\n‹secret 1›\n", with: "<key>\n‹secret 2›\n")
            .replacingOccurrences(of: "<tls-crypt>\n‹secret 2›\n", with: "<tls-crypt>\n‹secret 1›\n")

        #expect(try restored(mask, edited) == Self.ovpnText
            .replacingOccurrences(of: "KEY BODY", with: "TMP").replacingOccurrences(of: "CRYPT BODY", with: "KEY BODY").replacingOccurrences(of: "TMP", with: "CRYPT BODY"))
    }

    @Test func aSecretComesBackAsItWasEvenWhenItLooksLikeAPlaceholder() throws {
        let text = "client\n<key>\n‹secret 1›\n‹secret 2›\n</key>\n"
        let mask = openVPN(text)

        #expect(mask.displayText == "client\n<key>\n‹secret 3›\n</key>\n")
        #expect(try restored(mask, mask.displayText) == text)
    }

    @Test func theTextOfADisplayWithoutSecretsIsReturnedAsItIs() throws {
        let text = "client\nremote vpn.example.net 1194\n"
        let mask = openVPN(text)
        #expect(mask.displayText == text)
        #expect(try restored(mask, "client\nremote other 443\n\n") == "client\nremote other 443\n\n")
    }

    // MARK: Lines

    @Test func mapsALineOfTheRestoredTextBackToTheLineOfTheDisplay() throws {
        let text = "client\n<key>\nA\nB\nC\n</key>\nverb 3\n<tls-auth>\nD\nE\n</tls-auth>\nremote x 1\n"
        let mask = openVPN(text)
        #expect(mask.displayText == "client\n<key>\n‹secret 1›\n</key>\nverb 3\n<tls-auth>\n‹secret 2›\n</tls-auth>\nremote x 1\n")
        let restoration = try mask.restore(mask.displayText)
        #expect(restoration.text == text)

        // Every line that is not part of a secret is found again in the display.
        let secretLines: Set<String> = ["A", "B", "C", "D", "E"]
        let restoredLines = text.split(separator: "\n", omittingEmptySubsequences: false).map(String.init)
        let displayLines = mask.displayText.split(separator: "\n", omittingEmptySubsequences: false).map(String.init)
        for (index, line) in restoredLines.enumerated() where !line.isEmpty && !secretLines.contains(line) {
            let shown = restoration.displayLine(forRestoredLine: index + 1)
            #expect(displayLines[shown - 1] == line, "restored line \(index + 1) is \(line)")
        }
        // Lines inside a secret map to the line of its placeholder: the key is restored lines 3 to 5, the tls-auth key 9 and 10.
        for (line, shown) in [(3, 3), (4, 3), (5, 3), (9, 7), (10, 7)] {
            #expect(restoration.displayLine(forRestoredLine: line) == shown, "line \(line)")
        }
    }

    @Test func linesBeforeTheFirstSecretAndAfterTheLastAreCountedAsTheyAre() throws {
        let mask = wireGuard("[Interface]\nPrivateKey = \(privateKey)\nAddress = 10.0.0.2/32\n")
        let restoration = try mask.restore(mask.displayText)
        #expect([1, 2, 3].map { restoration.displayLine(forRestoredLine: $0) } == [1, 2, 3])
    }

    @Test func theLineMapFollowsTheEditedTextNotTheOriginalOne() throws {
        let mask = openVPN("client\n<key>\nA\nB\n</key>\nverb 3\n")
        // Two lines added above the placeholder; the secret then spans restored lines 5 and 6.
        let restoration = try mask.restore("client\n# one\n# two\n<key>\n‹secret 1›\n</key>\nverb 3\n")

        #expect(restoration.text == "client\n# one\n# two\n<key>\nA\nB\n</key>\nverb 3\n")
        #expect([1, 4, 5, 6, 7, 8].map { restoration.displayLine(forRestoredLine: $0) } == [1, 4, 5, 5, 6, 7])
    }
}

// MARK: Random edits

private struct RandomProfile {
    let text: String
    let kind: ProfileKind
    /// What the mask has to hide, keyed by the number of its placeholder.
    let secrets: [Int: String]

    init(seed: UInt64) {
        var random = SeededGenerator(state: seed)
        let lineBreak = Bool.random(using: &random) ? "\n" : "\r\n"
        let alphabet = Array("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/")
        func base64(_ count: Int) -> String { String((0..<count).map { _ in alphabet.randomElement(using: &random)! }) }
        func blanks() -> String { String(repeating: [" ", "\t", ""].randomElement(using: &random)!, count: Int.random(in: 0...3, using: &random)) }

        var lines: [String] = []
        var secrets: [Int: String] = [:]
        var number = 0
        func addSecret(_ secret: String) { number += 1; secrets[number] = secret }

        if Bool.random(using: &random) {
            kind = .wireguard
            // Every profile has at least one secret.
            let key = base64(43) + "="
            lines.append("[Interface]")
            lines.append("PrivateKey = \(key)")
            addSecret(key)
            for index in 0..<Int.random(in: 1...6, using: &random) {
                switch Int.random(in: 0..<5, using: &random) {
                case 0:
                    let key = base64(43) + "="
                    lines.append("\(blanks())PrivateKey\(blanks())=\(blanks())\(key)\(blanks())")
                    addSecret(key)
                case 1:
                    let key = base64(43) + "="
                    lines.append("\(blanks())PresharedKey = \(key) # note \(index)")
                    addSecret(key)
                case 2: lines.append("# a comment \(base64(8)) ‹ › 備註")
                case 3: lines.append("")
                default: lines.append("Address = 10.\(index).0.2/32")
                }
            }
            lines.append("[Peer]")
            lines.append("PublicKey = \(base64(43))=")
        } else {
            kind = .openvpn
            let key = base64(40)
            lines.append("client")
            lines.append("<key>")
            lines.append(key)
            lines.append("</key>")
            addSecret(key)
            for index in 0..<Int.random(in: 1...6, using: &random) {
                switch Int.random(in: 0..<4, using: &random) {
                case 0:
                    let body = (0..<Int.random(in: 1...4, using: &random)).map { _ in base64(32) }.joined(separator: lineBreak)
                    let tag = ["key", "tls-crypt", "tls-auth", "secret"].randomElement(using: &random)!
                    lines.append("\(blanks())<\(tag)>\(blanks())")
                    lines.append(body)
                    lines.append("\(blanks())</\(tag)>\(blanks())")
                    addSecret(body)
                case 1:
                    lines.append("<ca>")
                    lines.append("-----BEGIN CERTIFICATE-----")
                    lines.append(base64(32))
                    lines.append("-----END CERTIFICATE-----")
                    lines.append("</ca>")
                case 2: lines.append("; comment \(base64(6)) ‹ ›")
                default: lines.append("remote host\(index).example.net 1194")
                }
            }
        }
        text = lines.joined(separator: lineBreak) + lineBreak
        self.secrets = secrets
    }
}

struct SecretMaskPropertyTests {
    private static let insertions = ["x", " ", "# note", "\n", "\r\n", "Address = 10.0.0.9/32", "備註", "🌐", "9", "<key>", "</key>", "PrivateKey = "]

    @Test(arguments: 0..<150)
    func editsAwayFromPlaceholdersKeepEverySecretByteIdentical(seed: Int) throws {
        let profile = RandomProfile(seed: UInt64(seed))
        let mask = SecretMask(text: profile.text, kind: profile.kind)

        // The mask finds exactly what the profile was built to hold, and hides all of it.
        let numbers = SecretMask.placeholderRanges(in: mask.displayText).map { Self.number(of: mask.displayText, $0) }
        #expect(numbers == profile.secrets.keys.sorted())
        for secret in profile.secrets.values { #expect(!mask.displayText.contains(secret)) }
        #expect(try mask.restore(mask.displayText).text == profile.text)

        var random = SeededGenerator(state: UInt64(seed) &+ 1_000_003)
        var edited = mask.displayText
        for _ in 0..<Int.random(in: 1...8, using: &random) {
            edited = Self.edit(edited, using: &random)
        }

        // What `edited` has to become: its placeholders replaced by the secrets.
        var expected = edited
        for range in SecretMask.placeholderRanges(in: edited).reversed() {
            expected.replaceSubrange(range, with: try #require(profile.secrets[Self.number(of: edited, range)]))
        }
        let result = try mask.restore(edited).text
        #expect(result == expected)
        for secret in profile.secrets.values { #expect(result.contains(secret)) }
    }

    /// One insertion, deletion or replacement that leaves every placeholder whole.
    private static func edit(_ text: String, using random: inout SeededGenerator) -> String {
        let protected = SecretMask.placeholderRanges(in: text)
        let scalars = text.unicodeScalars
        // The stretches between placeholders, where an edit may happen.
        var gaps: [Range<String.Index>] = []
        var start = text.startIndex
        for range in protected {
            gaps.append(start..<range.lowerBound)
            start = range.upperBound
        }
        gaps.append(start..<text.endIndex)

        let gap = gaps.randomElement(using: &random)!
        let length = scalars.distance(from: gap.lowerBound, to: gap.upperBound)
        let from = scalars.index(gap.lowerBound, offsetBy: Int.random(in: 0...length, using: &random))
        let to = scalars.index(from, offsetBy: Int.random(in: 0...min(6, scalars.distance(from: from, to: gap.upperBound)), using: &random))
        var result = text
        result.replaceSubrange(from..<to, with: insertions.randomElement(using: &random)!)
        return result
    }

    private static func number(of text: String, _ range: Range<String.Index>) -> Int {
        Int(text[range].dropFirst("‹secret ".count).dropLast())!
    }
}

struct SecretMaskCostTests {
    /// A hostile profile is accepted by the daemon with any body in an allowed block: many BEGIN
    /// lines and no END line must not be searched to the end of the text once for each.
    @Test func manyBeginLinesWithoutAnEndAreScannedOnce() {
        let lines = String(repeating: "-----BEGIN PRIVATE KEY-----\n", count: 30_000)
        let text = "client\n<ca>\n" + lines + "</ca>\n"
        let start = ContinuousClock.now
        let mask = openVPN(text)
        let elapsed = ContinuousClock.now - start
        #expect(mask.displayText == text, "no key was found: nothing is hidden")
        #expect(elapsed < .seconds(3), "took \(elapsed)")
    }

    @Test func aKeyAfterManyUnfinishedOnesOfAnotherLabelIsStillHidden() throws {
        let unfinished = String(repeating: "-----BEGIN RSA PRIVATE KEY-----\n", count: 50)
        let text = "<ca>\n" + unfinished + "-----BEGIN PRIVATE KEY-----\nSECRETBODY\n-----END PRIVATE KEY-----\n</ca>\n"
        let mask = openVPN(text)
        #expect(!mask.displayText.contains("SECRETBODY"))
        #expect(try restored(mask, mask.displayText) == text)
    }
}
