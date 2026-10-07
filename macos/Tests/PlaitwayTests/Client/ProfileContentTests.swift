import CryptoKit
import Foundation
import GRPCCore
import PlaitwayClient
import Testing

private func text(_ data: Data) -> String { String(decoding: data, as: UTF8.self) }

private func lineCount(_ text: String) -> Int { text.filter { $0 == "\n" }.count }

private func base64(_ key: Curve25519.KeyAgreement.PublicKey) -> String { key.rawRepresentation.base64EncodedString() }

/// A WireGuard profile that the real parser accepts: its keys are real ones, which `Fixture.wireGuard()` is not.
private func realWireGuard(privateKey: String) -> String {
    """
    [Interface]
    PrivateKey = \(privateKey)
    Address = 10.6.0.2/32

    [Peer]
    PublicKey = \(base64(Curve25519.KeyAgreement.PrivateKey().publicKey))
    AllowedIPs = 10.6.0.0/24
    Endpoint = 203.0.113.5:51820

    """
}

@MainActor
struct ProfileContentTests {
    // MARK: read

    @Test func readsTheStoredTextNotWhatWasUploaded() async throws {
        try await withDaemon { _, store in
            let id = try await store.importFixture("office.ovpn", Fixture.openVPN(extra: ["up /bin/sh"]))
            #expect(try await store.profileContent(id: id) == text(Fixture.openVPN()))
        }
    }

    @Test func keepsAByteOrderMarkTheTextStartsWith() async throws {
        try await withDaemon { _, store in
            let id = try await store.importFixture("bom.ovpn", Data([0xEF, 0xBB, 0xBF]) + Fixture.openVPN())
            #expect(try await store.profileContent(id: id).hasPrefix("\u{FEFF}client"))
        }
    }

    @Test func readingAnUnknownProfileIsNotFound() async throws {
        try await withDaemon { _, store in
            do {
                _ = try await store.profileContent(id: "nope")
                Issue.record("an unknown profile id was accepted")
            } catch let error as RPCError {
                #expect(error.code == .notFound)
            }
        }
    }

    // MARK: update

    @Test func replacesTheTextAndRefreshesTheSummary() async throws {
        try await withDaemon { _, store in
            let id = try await store.importFixture("home.conf", Fixture.wireGuard())
            let before = try #require(store.profile(id)).summary
            #expect(!before.publicKey.isEmpty)

            let base = text(Fixture.wireGuard(allowedIPs: "10.9.0.0/16", endpoint: "198.51.100.4:51821"))
                .replacingOccurrences(of: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", with: "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=")
            let result = try await store.updateProfileContent(id: id, content: base + "PostUp = /bin/evil\n")

            #expect(try await store.profileContent(id: id) == base)
            #expect(result.profile.id == id)
            #expect(result.profile.summary.endpoints.first?.host == "198.51.100.4")
            #expect(result.profile.summary.endpoints.first?.port == 51821)
            #expect(result.profile.summary.routes == ["10.9.0.0/16"])
            #expect(!result.profile.summary.publicKey.isEmpty)
            #expect(result.profile.summary.publicKey != before.publicKey)
            #expect(result.warnings.map(\.directive) == ["PostUp"])
            #expect(result.warnings.first?.line == Int32(lineCount(base) + 1))

            try await waitUntil("the watch to show the new summary") {
                store.profile(id)?.summary.publicKey == result.profile.summary.publicKey
            }
        }
    }

    @Test func updatingTheTextKeepsTheNameAndTheSettings() async throws {
        try await withDaemon { _, store in
            var settings = ProfileSettings()
            settings.autoConnect = true
            settings.tunnelMode = .split
            let id = try await store.importProfile(
                content: Fixture.openVPN(), sourceFilename: "office.ovpn", name: "Office", settings: settings
            ).profile.id

            let result = try await store.updateProfileContent(id: id, content: text(Fixture.openVPN(host: "edited.example.net")))

            #expect(result.profile.name == "Office")
            #expect(result.profile.settings.autoConnect)
            #expect(result.profile.settings.tunnelMode == .split)
            #expect(result.profile.summary.endpoints.first?.host == "edited.example.net")
        }
    }

    @Test func textOfTheOtherKindIsRefusedWithTheDaemonsMessage() async throws {
        try await withDaemon { _, store in
            let office = try await store.importFixture("office.ovpn")
            let home = try await store.importFixture("home.conf", Fixture.wireGuard())

            for (id, content, message) in [
                (office, text(Fixture.wireGuard()), "this profile is OpenVPN, but the text is WireGuard"),
                (home, text(Fixture.openVPN()), "this profile is WireGuard, but the text is OpenVPN"),
            ] {
                do {
                    _ = try await store.updateProfileContent(id: id, content: content)
                    Issue.record("text of the other kind was accepted")
                } catch let error as RPCError {
                    #expect(error.code == .invalidArgument)
                    #expect(error.message == message)
                    #expect(DaemonFailure(error) == .rejected(message: message))
                    #expect(ConfigDiagnostic(error) == ConfigDiagnostic(line: nil, message: message))
                }
            }
            #expect(try await store.profileContent(id: office) == text(Fixture.openVPN()))
            #expect(try await store.profileContent(id: home) == text(Fixture.wireGuard()))
        }
    }

    @Test func rejectedTextChangesNothing() async throws {
        try await withDaemon { _, store in
            let id = try await store.importFixture("office.ovpn")
            do {
                _ = try await store.updateProfileContent(id: id, content: text(Fixture.openVPN(markers: ["# fake: reject"])))
                Issue.record("a rejected text was accepted")
            } catch let error as RPCError {
                #expect(error.code == .invalidArgument)
                #expect(error.message.hasPrefix("line 6: "))
                #expect(ConfigDiagnostic(error)?.line == 6)
            }
            #expect(try await store.profileContent(id: id) == text(Fixture.openVPN()))
            #expect(store.profile(id)?.summary.endpoints.first?.host == "vpn.example.net")
        }
    }

    @Test func emptyTextIsRefused() async throws {
        try await withDaemon { _, store in
            let id = try await store.importFixture("office.ovpn")
            await #expect(throws: RPCError.self) { _ = try await store.updateProfileContent(id: id, content: "") }
        }
    }

    @Test func updatingAnUnknownProfileIsNotFound() async throws {
        try await withDaemon { _, store in
            do {
                _ = try await store.updateProfileContent(id: "nope", content: text(Fixture.openVPN()))
                Issue.record("an unknown profile id was accepted")
            } catch let error as RPCError {
                #expect(error.code == .notFound)
            }
        }
    }

    // MARK: reconnect

    @Test func theNewTextOfARunningProfileWaitsForTheNextConnection() async throws {
        try await withDaemon { _, store in
            let id = try await store.importFixture("office.ovpn")
            try await store.setEnabled(true, profileID: id)
            try await store.waitForState(id, .connected)
            let interface = try #require(store.profile(id)).status.interfaceName

            // A text that makes the fake engine fail to connect, so a restart is visible.
            _ = try await store.updateProfileContent(id: id, content: text(Fixture.openVPN(markers: ["# fake: fail"])), reconnect: false)
            try await Task.sleep(for: .milliseconds(300))
            #expect(store.profile(id)?.state == .connected)
            #expect(store.profile(id)?.status.interfaceName == interface)

            try await store.setEnabled(false, profileID: id)
            try await store.waitForState(id, .disconnected)
            try await store.setEnabled(true, profileID: id)
            try await store.waitForState(id, .failed)
        }
    }

    @Test func reconnectRestartsAnEnabledProfileWithTheNewText() async throws {
        try await withDaemon { _, store in
            let id = try await store.importFixture("office.ovpn")
            try await store.setEnabled(true, profileID: id)
            try await store.waitForState(id, .connected)

            let result = try await store.updateProfileContent(
                id: id, content: text(Fixture.openVPN(markers: ["# fake: fail"])), reconnect: true
            )

            #expect(result.profile.desiredEnabled)
            try await store.waitForState(id, .failed)
        }
    }

    @Test func reconnectLeavesADisabledProfileDisconnected() async throws {
        try await withDaemon { _, store in
            let id = try await store.importFixture("office.ovpn")
            let result = try await store.updateProfileContent(id: id, content: text(Fixture.openVPN(host: "other.example.net")), reconnect: true)
            #expect(result.profile.state == .disconnected)
            #expect(!result.profile.desiredEnabled)
        }
    }

    // MARK: the real parser

    @Test func theRealParserNamesTheLineItRejects() async throws {
        try await withDaemon(fake: false) { _, store in
            let id = try await store.importFixture("office.ovpn")
            let broken = text(Fixture.openVPN(extra: ["route 10.0.0.0 notamask"]))

            do {
                _ = try await store.updateProfileContent(id: id, content: broken)
                Issue.record("a bad route was accepted")
            } catch let error as RPCError {
                #expect(error.code == .invalidArgument)
                #expect(error.message == #"line 6: route: "notamask" is not a netmask"#)
                #expect(ConfigDiagnostic(error) == ConfigDiagnostic(line: 6, message: #"route: "notamask" is not a netmask"#))
            }
        }
    }

    @Test func theRealParserNamesTheLineOfAWireGuardRejection() async throws {
        try await withDaemon(fake: false) { _, store in
            let valid = realWireGuard(privateKey: Curve25519.KeyAgreement.PrivateKey().rawRepresentation.base64EncodedString())
            let id = try await store.importFixture("home.conf", Data(valid.utf8))

            do {
                _ = try await store.updateProfileContent(id: id, content: realWireGuard(privateKey: "nope"))
                Issue.record("a bad private key was accepted")
            } catch let error as RPCError {
                #expect(ConfigDiagnostic(error) == ConfigDiagnostic(line: 2, message: "PrivateKey is not a base64-encoded 32-byte key"))
            }
        }
    }

    // The daemon counts the lines of the text it was sent; the editor shows the masked one.
    @Test func aRejectionBehindAMaskedKeyBlockIsMarkedOnTheLineTheEditorShows() async throws {
        try await withDaemon(fake: false) { _, store in
            let original = """
            client
            dev tun
            remote vpn.example.net 1194
            <key>
            -----BEGIN PRIVATE KEY-----
            AAAA
            BBBB
            -----END PRIVATE KEY-----
            </key>

            """
            let id = try await store.importFixture("office.ovpn", Data(original.utf8))
            let mask = SecretMask(text: try await store.profileContent(id: id), kind: .openvpn)
            #expect(mask.displayText.contains("<key>\n‹secret 1›\n</key>"))

            let edited = mask.displayText + "route 10.0.0.0 notamask\n"
            let shownLine = edited.split(separator: "\n", omittingEmptySubsequences: false).firstIndex { $0.hasPrefix("route") }! + 1
            let restoration = try mask.restore(edited)
            do {
                _ = try await store.updateProfileContent(id: id, content: restoration.text)
                Issue.record("a bad netmask was accepted")
            } catch let error as RPCError {
                let diagnostic = try #require(ConfigDiagnostic(error))
                let reported = try #require(diagnostic.line)
                // The key block is four lines in the text the daemon read and one in the editor.
                #expect(reported == shownLine + 3)
                #expect(restoration.displayLine(forRestoredLine: reported) == shownLine)
            }
        }
    }

    @Test func theRealWireGuardPublicKeyIsTheOneCryptoKitDerives() async throws {
        try await withDaemon(fake: false) { _, store in
            let privateKey = Curve25519.KeyAgreement.PrivateKey()
            let id = try await store.importFixture("home.conf", Data(realWireGuard(privateKey: privateKey.rawRepresentation.base64EncodedString()).utf8))
            #expect(store.profile(id)?.summary.publicKey == base64(privateKey.publicKey))

            let replacement = Curve25519.KeyAgreement.PrivateKey()
            let result = try await store.updateProfileContent(id: id, content: realWireGuard(privateKey: replacement.rawRepresentation.base64EncodedString()))
            #expect(result.profile.summary.publicKey == base64(replacement.publicKey))
        }
    }
}

// The settings that came with profile editing travel the existing update path.
@MainActor
struct ProfileSettingsTests {
    @Test func excludingPrivateIPsIsStoredForAWireGuardProfile() async throws {
        try await withDaemon { _, store in
            let id = try await store.importFixture("home.conf", Fixture.wireGuard())
            #expect(store.profile(id)?.settings.excludePrivateIps == false)

            var settings = try #require(store.profile(id)).settings
            settings.excludePrivateIps = true
            try await store.updateProfile(id: id, settings: settings)

            try await waitUntil("the setting") { store.profile(id)?.settings.excludePrivateIps == true }
            #expect(store.profile(id)?.settings.priority == settings.priority)
        }
    }

    @Test func excludingPrivateIPsIsRefusedForAnOpenVPNProfile() async throws {
        try await withDaemon { _, store in
            let id = try await store.importFixture("office.ovpn")
            var settings = try #require(store.profile(id)).settings
            settings.excludePrivateIps = true
            do {
                try await store.updateProfile(id: id, settings: settings)
                Issue.record("the option was accepted for OpenVPN")
            } catch let error as RPCError {
                #expect(error.code == .invalidArgument)
                #expect(error.message == "excluding private IPs works on WireGuard's AllowedIPs, and this profile is OpenVPN")
            }
            #expect(store.profile(id)?.settings.excludePrivateIps == false)
        }
    }

    @Test func onDemandRulesAreStoredAndFollowTheNetwork() async throws {
        try await withDaemon { _, store in
            let id = try await store.importFixture("office.ovpn")
            #expect(store.profile(id)?.settings.onDemand.isActive == false)

            // The fake daemon's network is Wi-Fi. Saving the rules applies them to it at once.
            var settings = try #require(store.profile(id)).settings
            settings.onDemand.wifi = true
            try await store.updateProfile(id: id, settings: settings)
            try await store.waitForState(id, .connected)
            #expect(store.profile(id)?.settings.onDemand.wifi == true)
            #expect(store.profile(id)?.settings.onDemand.ethernet == false)
            #expect(store.profile(id)?.settings.onDemand.isActive == true)

            settings = try #require(store.profile(id)).settings
            settings.onDemand.wifi = false
            settings.onDemand.ethernet = true
            try await store.updateProfile(id: id, settings: settings)
            try await store.waitForState(id, .disconnected)
            #expect(store.profile(id)?.settings.onDemand.ethernet == true)
        }
    }

    @Test func clearingBothRulesTurnsOnDemandOff() async throws {
        try await withDaemon { _, store in
            var settings = ProfileSettings()
            settings.onDemand.ethernet = true
            let id = try await store.importFixture("office.ovpn", settings: settings)
            #expect(store.profile(id)?.settings.onDemand.isActive == true)

            settings.onDemand = OnDemandRules()
            try await store.updateProfile(id: id, settings: settings)
            try await waitUntil("on demand to be off") { store.profile(id)?.settings.onDemand.isActive == false }
        }
    }
}

struct OnDemandRulesTests {
    @Test(arguments: [(false, false, false), (true, false, true), (false, true, true), (true, true, true)])
    func isActiveWhenEitherNetworkIsChecked(ethernet: Bool, wifi: Bool, active: Bool) {
        var rules = OnDemandRules()
        rules.ethernet = ethernet
        rules.wifi = wifi
        #expect(rules.isActive == active)
    }
}
