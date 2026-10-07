import Foundation
import PlaitwayClient
import Testing

struct ProfileImporterTests {
    private static let certificate = "-----BEGIN CERTIFICATE-----\nMIIBtest\n-----END CERTIFICATE-----"
    private static let privateKey = "-----BEGIN PRIVATE KEY-----\nMIIEtest\n-----END PRIVATE KEY-----"
    private static let staticKey = "#\n# 2048 bit OpenVPN static key\n#\n-----BEGIN OpenVPN Static key V1-----\n0123\n-----END OpenVPN Static key V1-----"

    /// A directory with the given files; removed when the body returns.
    private func withDirectory(_ files: [String: String], _ body: (URL) throws -> Void) throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("pw-import-\(UUID().uuidString.prefix(8))")
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        for (name, content) in files {
            let url = directory.appendingPathComponent(name)
            try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
            try Data(content.utf8).write(to: url)
        }
        try body(directory)
    }

    private func inline(_ profile: String, files: [String: String]) throws -> String {
        var result = ""
        try withDirectory(files) { directory in
            result = try ProfileImporter.inlineReferencedFiles(in: profile, relativeTo: directory).text
        }
        return result
    }

    @Test func inlinesCaCertAndKeyFiles() throws {
        let result = try inline(
            "client\nremote vpn.example.net 1194\nca ca.crt\ncert client.crt\nkey keys/client.key\nverb 3\n",
            files: ["ca.crt": Self.certificate + "\n", "client.crt": Self.certificate, "keys/client.key": Self.privateKey + "\n"]
        )
        #expect(result == """
        client
        remote vpn.example.net 1194
        <ca>
        \(Self.certificate)
        </ca>
        <cert>
        \(Self.certificate)
        </cert>
        <key>
        \(Self.privateKey)
        </key>
        verb 3

        """)
    }

    @Test func inlinesTlsAuthWithItsKeyDirection() throws {
        let result = try inline("client\ntls-auth ta.key 1\n", files: ["ta.key": Self.staticKey])
        #expect(result == "client\n<tls-auth>\n\(Self.staticKey)\n</tls-auth>\nkey-direction 1\n")
    }

    @Test func doesNotRepeatAKeyDirectionTheProfileAlreadyHas() throws {
        let result = try inline("key-direction 1\ntls-auth ta.key 1\n", files: ["ta.key": Self.staticKey])
        #expect(result.components(separatedBy: "key-direction").count == 2)
    }

    @Test func inlinesTlsCrypt() throws {
        let result = try inline("tls-crypt tc.key\n", files: ["tc.key": Self.staticKey])
        #expect(result == "<tls-crypt>\n\(Self.staticKey)\n</tls-crypt>\n")
    }

    @Test func leavesInlineBlocksAndComplexLinesAlone() throws {
        let profile = """
        client
        # ca ignored.crt
        ; key ignored.key
        dh none
        ca [inline]
        <ca>
        ca not-a-directive-inside-a-block.crt
        \(Self.certificate)
        </ca>
        auth-user-pass
        remote-cert-tls server

        """
        #expect(try inline(profile, files: [:]) == profile)
    }

    @Test func resolvesRelativePathsAgainstTheProfilesDirectory() throws {
        try withDirectory(["profiles/ca.crt": Self.certificate, "profiles/certs/key.pem": Self.privateKey]) { root in
            let profileDirectory = root.appendingPathComponent("profiles")
            let result = try ProfileImporter.inlineReferencedFiles(
                in: "ca ca.crt\nkey \"certs/key.pem\"\nca certs/../ca.crt\n", relativeTo: profileDirectory
            ).text
            #expect(result == "<ca>\n\(Self.certificate)\n</ca>\n<key>\n\(Self.privateKey)\n</key>\n<ca>\n\(Self.certificate)\n</ca>\n")
        }
    }

    // MARK: files outside the profile's directory

    /// `profile/` next to `outside.pem`, which a hostile profile would like to read.
    private func withProfileDirectoryAndASecretBesideIt(_ body: (URL, URL) throws -> Void) throws {
        try withDirectory(["profile/ca.crt": Self.certificate, "outside.pem": Self.privateKey]) { root in
            try body(root.appendingPathComponent("profile"), root)
        }
    }

    private func refusesOutsideTheProfileDirectory(_ profile: String, relativeTo directory: URL, named path: String) {
        #expect {
            try ProfileImporter.inlineReferencedFiles(in: profile, relativeTo: directory)
        } throws: { error in
            guard case ProfileImporter.Failure.outsideProfileDirectory(_, let refused) = error else { return false }
            return refused == path
        }
    }

    @Test(arguments: ["../outside.pem", "certs/../../outside.pem"])
    func refusesAPathThatLeavesTheProfilesDirectory(path: String) throws {
        try withProfileDirectoryAndASecretBesideIt { profileDirectory, _ in
            refusesOutsideTheProfileDirectory("key \(path)\n", relativeTo: profileDirectory, named: path)
        }
    }

    @Test func refusesAnAbsolutePathEvenInsideTheProfilesDirectory() throws {
        try withProfileDirectoryAndASecretBesideIt { profileDirectory, root in
            for path in [root.appendingPathComponent("outside.pem").path, profileDirectory.appendingPathComponent("ca.crt").path] {
                refusesOutsideTheProfileDirectory("ca \(path)\n", relativeTo: profileDirectory, named: path)
            }
        }
    }

    @Test func refusesAHomeDirectoryPath() throws {
        try withProfileDirectoryAndASecretBesideIt { profileDirectory, _ in
            refusesOutsideTheProfileDirectory("key ~/.ssh/id_ed25519\n", relativeTo: profileDirectory, named: "~/.ssh/id_ed25519")
        }
    }

    @Test func refusesASymlinkThatPointsOutsideTheProfilesDirectory() throws {
        try withProfileDirectoryAndASecretBesideIt { profileDirectory, root in
            try FileManager.default.createSymbolicLink(
                at: profileDirectory.appendingPathComponent("link.pem"), withDestinationURL: root.appendingPathComponent("outside.pem")
            )
            try FileManager.default.createSymbolicLink(at: profileDirectory.appendingPathComponent("linked"), withDestinationURL: root)
            refusesOutsideTheProfileDirectory("key link.pem\n", relativeTo: profileDirectory, named: "link.pem")
            refusesOutsideTheProfileDirectory("key linked/outside.pem\n", relativeTo: profileDirectory, named: "linked/outside.pem")
        }
    }

    @Test func aProfileDirectoryReachedThroughASymlinkIsStillTheProfilesDirectory() throws {
        try withProfileDirectoryAndASecretBesideIt { profileDirectory, root in
            let alias = root.appendingPathComponent("alias")
            try FileManager.default.createSymbolicLink(at: alias, withDestinationURL: profileDirectory)
            let result = try ProfileImporter.inlineReferencedFiles(in: "ca ca.crt\n", relativeTo: alias).text
            #expect(result == "<ca>\n\(Self.certificate)\n</ca>\n")
        }
    }

    @Test func understandsWindowsLineEndings() throws {
        let result = try inline("client\r\nca ca.crt\r\n<cert>\r\n\(Self.certificate)\r\n</cert>\r\nverb 3\r\n", files: ["ca.crt": Self.certificate])
        #expect(!result.contains("\r"))
        #expect(result.contains("<ca>\n\(Self.certificate)\n</ca>"))
        #expect(result.contains("</cert>\nverb 3"))
    }

    @Test func namesTheMissingFile() throws {
        try withDirectory([:]) { directory in
            #expect {
                try ProfileImporter.inlineReferencedFiles(in: "client\nca nowhere.crt\n", relativeTo: directory)
            } throws: { error in
                guard case ProfileImporter.Failure.missingFile(let directive, let path) = error else { return false }
                return directive == "ca" && path.hasSuffix("/nowhere.crt")
            }
        }
    }

    @Test func refusesAFileThatIsNotKeyMaterial() throws {
        try withDirectory(["notes.txt": "just some text\n"]) { directory in
            #expect {
                try ProfileImporter.inlineReferencedFiles(in: "key notes.txt\n", relativeTo: directory)
            } throws: { error in
                guard case ProfileImporter.Failure.notKeyMaterial(let directive, _) = error else { return false }
                return directive == "key"
            }
        }
    }

    @Test func refusesABinaryFile() throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("pw-import-\(UUID().uuidString.prefix(8))")
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        try Data([0x30, 0x82, 0x00, 0xFF, 0xFE]).write(to: directory.appendingPathComponent("cert.der"))

        #expect {
            try ProfileImporter.inlineReferencedFiles(in: "cert cert.der\n", relativeTo: directory)
        } throws: { error in
            guard case ProfileImporter.Failure.notText = error else { return false }
            return true
        }
    }

    @Test func refusesToReadWhatIsNotARegularFile() throws {
        // A hostile profile can name a pipe or a directory next to it; reading a pipe never ends.
        try withDirectory(["sub/placeholder": ""]) { directory in
            #expect(mkfifo(directory.appendingPathComponent("pipe.pem").path, 0o600) == 0)
            for name in ["pipe.pem", "sub"] {
                #expect {
                    try ProfileImporter.inlineReferencedFiles(in: "ca \(name)\n", relativeTo: directory)
                } throws: { error in
                    guard case ProfileImporter.Failure.notText(let named) = error else { return false }
                    return named.hasSuffix(name)
                }
            }
        }
    }

    @Test func followsASymlinkToARegularFile() throws {
        try withDirectory(["real/ca.crt": Self.certificate]) { directory in
            try FileManager.default.createSymbolicLink(
                at: directory.appendingPathComponent("link.crt"), withDestinationURL: directory.appendingPathComponent("real/ca.crt")
            )
            let result = try ProfileImporter.inlineReferencedFiles(in: "ca link.crt\n", relativeTo: directory).text
            #expect(result == "<ca>\n\(Self.certificate)\n</ca>\n")
        }
    }

    // MARK: auth-user-pass

    @Test func replacesAnAuthUserPassFileByItsBareFormAndReturnsWhatItHolds() throws {
        try withDirectory(["office.ovpn": "client\nremote vpn.example.net 1194\nauth-user-pass creds.txt\nverb 3\n", "creds.txt": "alice\ns3cret\n"]) { directory in
            let loaded = try ProfileImporter.load(from: directory.appendingPathComponent("office.ovpn"))
            #expect(String(decoding: loaded.content, as: UTF8.self) == "client\nremote vpn.example.net 1194\nauth-user-pass\nverb 3\n")
            #expect(loaded.credentials == Credentials(username: "alice", password: "s3cret"))
        }
    }

    @Test func readsCredentialsTheWayOpenVPNDoes() throws {
        // CR LF, trailing lines and spaces inside the password are kept or dropped as openvpn does.
        let result = try { () throws -> (text: String, credentials: Credentials?) in
            var result: (text: String, credentials: Credentials?) = ("", nil)
            try withDirectory(["pw.txt": "bob\r\npass word \r\nignored\r\n"]) { directory in
                result = try ProfileImporter.inlineReferencedFiles(in: "AUTH-USER-PASS \"pw.txt\"\n", relativeTo: directory)
            }
            return result
        }()
        #expect(result.text == "auth-user-pass\n")
        #expect(result.credentials == Credentials(username: "bob", password: "pass word "))
    }

    @Test func leavesABareAuthUserPassAndItsInlineBlockAlone() throws {
        let bare = "client\nauth-user-pass\n"
        #expect(try inline(bare, files: [:]) == bare)
        let block = "<auth-user-pass>\nalice\ns3cret\n</auth-user-pass>\n"
        #expect(try inline(block, files: [:]) == block)
        try withDirectory([:]) { directory in
            let credentials = try ProfileImporter.inlineReferencedFiles(in: bare, relativeTo: directory).credentials
            #expect(credentials == nil)
        }
    }

    @Test(arguments: ["../outside.pem", "/etc/hosts", "~/creds.txt"])
    func refusesAnAuthUserPassFileOutsideTheProfilesDirectory(path: String) throws {
        try withProfileDirectoryAndASecretBesideIt { profileDirectory, _ in
            #expect {
                try ProfileImporter.inlineReferencedFiles(in: "auth-user-pass \(path)\n", relativeTo: profileDirectory)
            } throws: { error in
                guard case ProfileImporter.Failure.outsideProfileDirectory(let directive, let refused) = error else { return false }
                return directive == "auth-user-pass" && refused == path
            }
        }
    }

    @Test(arguments: ["alice\n", "alice", "\ns3cret\n", "alice\n\n", ""])
    func refusesAnAuthUserPassFileWithoutBothLines(content: String) throws {
        try withDirectory(["creds.txt": content]) { directory in
            #expect {
                try ProfileImporter.inlineReferencedFiles(in: "auth-user-pass creds.txt\n", relativeTo: directory)
            } throws: { error in
                guard case ProfileImporter.Failure.notCredentials(let path) = error else { return false }
                return path.hasSuffix("/creds.txt")
            }
        }
    }

    @Test func namesAnAuthUserPassFileThatIsMissing() throws {
        try withDirectory([:]) { directory in
            #expect {
                try ProfileImporter.inlineReferencedFiles(in: "auth-user-pass nowhere.txt\n", relativeTo: directory)
            } throws: { error in
                guard case ProfileImporter.Failure.missingFile(let directive, let path) = error else { return false }
                return directive == "auth-user-pass" && path.hasSuffix("/nowhere.txt")
            }
        }
    }

    // MARK: load

    @Test func loadsAnOvpnFileWithItsReferencedFiles() throws {
        try withDirectory([
            "office.ovpn": "client\nremote vpn.example.net 1194\nca ca.crt\n",
            "ca.crt": Self.certificate,
        ]) { directory in
            let data = try ProfileImporter.load(from: directory.appendingPathComponent("office.ovpn")).content
            #expect(String(decoding: data, as: UTF8.self) == "client\nremote vpn.example.net 1194\n<ca>\n\(Self.certificate)\n</ca>\n")
        }
    }

    @Test func passesAWireGuardFileThroughUnchanged() throws {
        // A line that would be a file reference in an OpenVPN profile.
        let text = "[Interface]\nPrivateKey = AAAA\nAddress = 10.0.0.2/32\n\n[Peer]\nPublicKey = BBBB\nAllowedIPs = 0.0.0.0/0\nEndpoint = 203.0.113.5:51820\n"
        try withDirectory(["home.conf": text]) { directory in
            let data = try ProfileImporter.load(from: directory.appendingPathComponent("home.conf")).content
            #expect(String(decoding: data, as: UTF8.self) == text)
        }
    }

    @Test func recognisesAnOpenVPNProfileWithoutTheOvpnExtension() throws {
        try withDirectory(["router.conf": "client\nremote vpn.example.net\nca ca.crt\n", "ca.crt": Self.certificate]) { directory in
            let data = try ProfileImporter.load(from: directory.appendingPathComponent("router.conf")).content
            #expect(String(decoding: data, as: UTF8.self).contains("<ca>"))
        }
    }

    @Test func refusesAProfileThatIsTooLarge() throws {
        try withDirectory(["huge.ovpn": String(repeating: "# padding\n", count: 120_000)]) { directory in
            #expect {
                try ProfileImporter.load(from: directory.appendingPathComponent("huge.ovpn"))
            } throws: { error in
                guard case ProfileImporter.Failure.tooLarge = error else { return false }
                return true
            }
        }
    }

    @Test func reportsAMissingProfile() throws {
        #expect {
            try ProfileImporter.load(from: URL(fileURLWithPath: "/nonexistent/profile.ovpn"))
        } throws: { error in
            guard case ProfileImporter.Failure.unreadable(let path, _) = error else { return false }
            return path == "/nonexistent/profile.ovpn"
        }
    }

    @Test(arguments: [
        "ca \"my certs/ca file.crt\"",
        "ca 'my certs/ca file.crt'",
        "ca my\\ certs/ca\\ file.crt",
        "  CA   \"my certs/ca file.crt\"  ",
    ])
    func readsPathsTheWayOpenVPNDoes(line: String) throws {
        let result = try inline(line + "\n", files: ["my certs/ca file.crt": Self.certificate])
        #expect(result == "<ca>\n\(Self.certificate)\n</ca>\n")
    }
}
