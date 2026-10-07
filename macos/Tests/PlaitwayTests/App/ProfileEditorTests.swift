import Foundation
import PlaitwayClient
import Testing
@testable import PlaitwayMenuBar

@MainActor
struct ProfileEditorTests {
    /// The private key of `Fixture.wireGuard()`.
    private static let privateKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

    private func loadedEditor(_ store: ProfileStore) async throws -> (ProfileEditor, String) {
        let id = try await store.importFixture("home.conf", Fixture.wireGuard())
        let editor = ProfileEditor(profileID: id, kind: .wireguard)
        await editor.load(from: store)
        return (editor, id)
    }

    @Test func showsTheStoredTextWithItsSecretsHidden() async throws {
        try await withDaemon { _, store in
            let (editor, _) = try await loadedEditor(store)
            #expect(editor.phase == .ready)
            #expect(!editor.text.contains(Self.privateKey))
            #expect(editor.text.contains("‹secret 1›"))
            #expect(editor.text.contains("Address = 10.6.0.2/32"))
            #expect(!editor.isDirty, "a text nobody touched is not an edit")
        }
    }

    @Test func savesAnEditWithoutLosingTheSecret() async throws {
        try await withDaemon { _, store in
            let (editor, id) = try await loadedEditor(store)
            editor.text = editor.text.replacingOccurrences(of: "10.6.0.2/32", with: "10.6.0.9/32")
            #expect(editor.isDirty)

            let saved = try await editor.save(to: store, reconnect: false, isOn: false)

            #expect(saved)
            let stored = try await store.profileContent(id: id)
            #expect(stored.contains("Address = 10.6.0.9/32"))
            #expect(stored.contains(Self.privateKey), "the secret never left the daemon's text")
            #expect(!editor.text.contains(Self.privateKey))
            #expect(!editor.isDirty)
            #expect(editor.diagnostic == nil)
            #expect(!editor.runsOldText, "the profile is not on")
        }
    }

    @Test func aSavedProfileThatIsOnKeepsTheOldTextUntilItReconnects() async throws {
        try await withDaemon { _, store in
            let (editor, _) = try await loadedEditor(store)
            editor.text += "# a note\n"
            #expect(try await editor.save(to: store, reconnect: false, isOn: true))
            #expect(editor.runsOldText)

            editor.text += "# another\n"
            #expect(try await editor.save(to: store, reconnect: true, isOn: true))
            #expect(!editor.runsOldText, "a restart applies the text at once")
        }
    }

    @Test func showsTheSecretsOnRequestAndHidesThemAgainWithTheEditsMadeMeanwhile() async throws {
        try await withDaemon { _, store in
            let (editor, _) = try await loadedEditor(store)
            editor.toggleSecrets()
            #expect(editor.showsSecrets)
            #expect(editor.text.contains(Self.privateKey))
            #expect(!editor.isDirty)

            editor.text = editor.text.replacingOccurrences(of: "10.6.0.2/32", with: "10.6.0.7/32")
            #expect(editor.isDirty)

            editor.toggleSecrets()
            #expect(!editor.showsSecrets)
            #expect(!editor.text.contains(Self.privateKey))
            #expect(editor.text.contains("10.6.0.7/32"))
            #expect(editor.isDirty, "the edit survives hiding the secrets")
        }
    }

    @Test func aSecretTypedOverIsStoredAsTheNewSecret() async throws {
        try await withDaemon { _, store in
            let (editor, id) = try await loadedEditor(store)
            let replacement = "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC="
            editor.text = editor.text.replacingOccurrences(of: "‹secret 1›", with: replacement)
            #expect(try await editor.save(to: store, reconnect: false, isOn: false))
            let stored = try await store.profileContent(id: id)
            #expect(stored.contains("PrivateKey = \(replacement)"))
            #expect(!stored.contains(Self.privateKey))
        }
    }

    @Test func aRefusedTextStaysInTheEditorAndIsMarkedAtItsLine() async throws {
        try await withDaemon { _, store in
            let id = try await store.importFixture("office.ovpn", Fixture.openVPN())
            let editor = ProfileEditor(profileID: id, kind: .openvpn)
            await editor.load(from: store)
            let before = try await store.profileContent(id: id)
            // The fake daemon refuses a text with this marker, naming the line it stands on.
            editor.text += "# fake: reject\n"
            let markerLine = editor.text.split(separator: "\n", omittingEmptySubsequences: false).count - 1

            let saved = try await editor.save(to: store, reconnect: false, isOn: false)

            #expect(!saved)
            #expect(editor.diagnostic?.line == markerLine)
            #expect(editor.diagnostic?.message.isEmpty == false)
            #expect(editor.text.hasSuffix("# fake: reject\n"), "the person's text is not taken away")
            #expect(editor.isDirty)
            #expect(try await store.profileContent(id: id) == before, "a refused text changes nothing")
        }
    }

    @Test func aRefusalIsMarkedAtTheLineThePersonSees() async throws {
        try await withDaemon { _, store in
            let (editor, _) = try await loadedEditor(store)
            // The key is one line in the editor and one in the text, so a refusal of the line
            // after it names the same line in both; the marker makes the fake daemon refuse.
            editor.text = editor.text.replacingOccurrences(of: "DNS = 10.6.0.1", with: "# fake: reject")
            let line = try #require(editor.text.split(separator: "\n", omittingEmptySubsequences: false).firstIndex { $0 == "# fake: reject" }) + 1

            #expect(try await !editor.save(to: store, reconnect: false, isOn: false))
            #expect(editor.diagnostic?.line == line)
        }
    }

    @Test func aPlaceholderThatStandsTwiceIsNotSent() async throws {
        try await withDaemon { _, store in
            let (editor, id) = try await loadedEditor(store)
            let before = try await store.profileContent(id: id)
            editor.text += "# ‹secret 1›\n"

            let saved = try await editor.save(to: store, reconnect: false, isOn: false)

            #expect(!saved)
            #expect(editor.diagnostic?.line == editor.text.split(separator: "\n", omittingEmptySubsequences: false).count - 1)
            #expect(try await store.profileContent(id: id) == before)
            // It cannot be shown either: a secret is in one place.
            editor.toggleSecrets()
            #expect(!editor.showsSecrets)
        }
    }

    @Test func revertGoesBackToTheStoredText() async throws {
        try await withDaemon { _, store in
            let (editor, _) = try await loadedEditor(store)
            let original = editor.text
            editor.text += "garbage\n"
            #expect(editor.isDirty)
            editor.revert()
            #expect(editor.text == original)
            #expect(!editor.isDirty)
            #expect(editor.diagnostic == nil)
        }
    }

    @Test func aLoadDoesNotThrowAwayEdits() async throws {
        try await withDaemon { _, store in
            let (editor, _) = try await loadedEditor(store)
            editor.text += "# unsaved\n"
            await editor.load(from: store)
            #expect(editor.text.hasSuffix("# unsaved\n"))
        }
    }

    @Test func aProfileThatIsGoneCannotBeRead() async throws {
        try await withDaemon { _, store in
            let editor = ProfileEditor(profileID: "nope", kind: .openvpn)
            await editor.load(from: store)
            #expect(editor.phase == .unavailable(String(localized: "Profile not found", bundle: .module)))
            #expect(!editor.isDirty)
        }
    }
}
