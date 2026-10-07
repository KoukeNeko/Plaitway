import Foundation
import GRPCCore
import PlaitwayClient
import Testing

/// A credential store that is slow to remove an item, like a Keychain that stops to ask.
private struct SlowRemovalStore: CredentialStore {
    let items = InMemoryCredentialStore()

    func credentials(profileID: String, kind: CredentialKind) async throws -> Credentials? {
        try await items.credentials(profileID: profileID, kind: kind)
    }

    func hasCredentials(profileID: String, kind: CredentialKind) async throws -> Bool {
        try await items.hasCredentials(profileID: profileID, kind: kind)
    }

    func save(_ credentials: Credentials, profileID: String, kind: CredentialKind) async throws {
        try await items.save(credentials, profileID: profileID, kind: kind)
    }

    func remove(profileID: String, kind: CredentialKind) async throws {
        try await Task.sleep(for: .milliseconds(300))
        try await items.remove(profileID: profileID, kind: kind)
    }
}

@MainActor
struct CredentialFlowTests {
    private static let needsCredentials = Fixture.openVPN(markers: ["# fake: needs-credentials"])

    @Test func asksOnceAndThenAnswersFromTheCredentialStore() async throws {
        let saved = InMemoryCredentialStore()
        try await withDaemon(credentials: saved) { _, store in
            let id = try await store.importFixture("router.ovpn", Self.needsCredentials)

            try await store.setEnabled(true, profileID: id)
            try await waitUntil("the credential prompt") { !store.credentialPrompts.isEmpty }
            let prompt = try #require(store.credentialPrompts.first)
            #expect(prompt.profileID == id)
            #expect(prompt.kind == .userPassword)
            #expect(!prompt.rejected)

            try await store.provideCredentials(profileID: id, username: "alice", password: "s3cret")
            try await store.waitForState(id, .connected)
            #expect(store.credentialPrompts.isEmpty)
            #expect(try await saved.credentials(profileID: id, kind: .userPassword) == Credentials(username: "alice", password: "s3cret"))

            // The next connection needs no sheet: the store answers by itself.
            try await store.setEnabled(false, profileID: id)
            try await store.waitForState(id, .disconnected)
            try await store.setEnabled(true, profileID: id)
            try await waitUntil("the reconnection") {
                #expect(store.credentialPrompts.isEmpty, "the sheet was shown although the credentials were saved")
                return store.profile(id)?.state == .connected
            }
        }
    }

    @Test func asksAgainWhenTheDaemonRefusesWhatWasTyped() async throws {
        let saved = InMemoryCredentialStore()
        try await withDaemon(credentials: saved) { _, store in
            let id = try await store.importFixture("router.ovpn", Self.needsCredentials)
            try await store.setEnabled(true, profileID: id)
            try await waitUntil("the credential prompt") { !store.credentialPrompts.isEmpty }

            // The fake daemon refuses the password "wrong" once.
            try await store.provideCredentials(profileID: id, username: "alice", password: "wrong")
            try await waitUntil("the prompt to come back") { store.credentialPrompts.first?.rejected == true }
            #expect(store.credentialPrompts.first?.message == "authentication failed")
            #expect(try await saved.credentials(profileID: id, kind: .userPassword) == nil, "refused credentials were kept")

            try await store.provideCredentials(profileID: id, username: "alice", password: "right")
            try await store.waitForState(id, .connected)
            #expect(store.credentialPrompts.isEmpty)
            #expect(try await saved.credentials(profileID: id, kind: .userPassword)?.password == "right")
        }
    }

    @Test func forgetsSavedCredentialsThatTheDaemonRefusesAndAsks() async throws {
        let saved = InMemoryCredentialStore()
        try await withDaemon(credentials: saved) { _, store in
            let id = try await store.importFixture("router.ovpn", Self.needsCredentials)
            try await saved.save(Credentials(username: "alice", password: "wrong"), profileID: id, kind: .userPassword)

            try await store.setEnabled(true, profileID: id)
            // The saved password is sent without asking, refused, and then the sheet appears.
            try await waitUntil("the sheet after the refusal") { store.credentialPrompts.first?.rejected == true }
            #expect(try await saved.credentials(profileID: id, kind: .userPassword) == nil)
        }
    }

    @Test func usesCredentialsSavedBeforeTheFirstConnection() async throws {
        let saved = InMemoryCredentialStore()
        try await withDaemon(credentials: saved) { _, store in
            let id = try await store.importFixture("router.ovpn", Self.needsCredentials)
            try await saved.save(Credentials(username: "alice", password: "s3cret"), profileID: id, kind: .userPassword)

            try await store.setEnabled(true, profileID: id)
            try await waitUntil("the connection") {
                #expect(store.credentialPrompts.isEmpty)
                return store.profile(id)?.state == .connected
            }
        }
    }

    @Test func cancellingTheSheetStopsConnecting() async throws {
        try await withDaemon { _, store in
            let id = try await store.importFixture("router.ovpn", Self.needsCredentials)
            try await store.setEnabled(true, profileID: id)
            try await waitUntil("the credential prompt") { !store.credentialPrompts.isEmpty }

            try await store.cancelCredentials(profileID: id)
            try await store.waitForState(id, .disconnected)
            #expect(store.credentialPrompts.isEmpty)
            #expect(store.profile(id)?.desiredEnabled == false)
        }
    }

    @Test func theSheetGoesAwayWhenSomeoneElseDisconnects() async throws {
        try await withDaemon { daemon, store in
            let id = try await store.importFixture("router.ovpn", Self.needsCredentials)
            try await store.setEnabled(true, profileID: id)
            try await waitUntil("the credential prompt") { !store.credentialPrompts.isEmpty }

            try await store.setEnabled(false, profileID: id)
            try await waitUntil("the prompt to go") { store.credentialPrompts.isEmpty }
        }
    }

    @Test func theSheetGoesAwayWithTheDaemon() async throws {
        try await withDaemon { daemon, store in
            let id = try await store.importFixture("router.ovpn", Self.needsCredentials)
            try await store.setEnabled(true, profileID: id)
            try await waitUntil("the credential prompt") { !store.credentialPrompts.isEmpty }

            daemon.kill()
            try await waitUntil("the outage") { store.connection == .unavailable }
            #expect(store.credentialPrompts.isEmpty)
        }
    }

    @Test func answeringWithoutARequestIsRefused() async throws {
        try await withDaemon { _, store in
            let id = try await store.importFixture("router.ovpn")
            do {
                try await store.provideCredentials(profileID: id, username: "alice", password: "s3cret")
                Issue.record("credentials were accepted without a request")
            } catch let error as RPCError {
                #expect(error.code == .failedPrecondition)
            }
        }
    }

    @Test func deletingAProfileForgetsItsCredentials() async throws {
        let saved = InMemoryCredentialStore()
        try await withDaemon(credentials: saved) { _, store in
            let id = try await store.importFixture("router.ovpn", Self.needsCredentials)
            try await saved.save(Credentials(username: "alice", password: "s3cret"), profileID: id, kind: .userPassword)
            try await saved.save(Credentials(password: "phrase"), profileID: id, kind: .keyPassphrase)
            let other = try await store.importFixture("other.ovpn")
            try await saved.save(Credentials(username: "bob", password: "pw"), profileID: other, kind: .userPassword)

            try await store.deleteProfile(id: id)
            try await waitUntil("the profile to go") { store.profile(id) == nil }
            // The store forgets them once the daemon says the profile is gone.
            try await waitUntilAsync("the saved credentials to go") {
                let user = try? await saved.hasCredentials(profileID: id, kind: .userPassword)
                let phrase = try? await saved.hasCredentials(profileID: id, kind: .keyPassphrase)
                return user == false && phrase == false
            }
            #expect(try await saved.credentials(profileID: other, kind: .userPassword) != nil)
        }
    }

    @Test func deletingAProfileForgetsItsCredentialsBeforeItReturns() async throws {
        // The removed event may never arrive (the watch is down at that moment), so the
        // delete itself must leave nothing behind in the Keychain.
        let saved = SlowRemovalStore()
        try await withDaemon(credentials: saved) { _, store in
            let id = try await store.importFixture("router.ovpn", Self.needsCredentials)
            try await saved.save(Credentials(username: "alice", password: "s3cret"), profileID: id, kind: .userPassword)
            try await saved.save(Credentials(password: "phrase"), profileID: id, kind: .keyPassphrase)

            try await store.deleteProfile(id: id)
            #expect(try await saved.hasCredentials(profileID: id, kind: .userPassword) == false)
            #expect(try await saved.hasCredentials(profileID: id, kind: .keyPassphrase) == false)
        }
    }

    @Test func savedCredentialsCanBeForgotten() async throws {
        let saved = InMemoryCredentialStore()
        try await withDaemon(credentials: saved) { _, store in
            let id = try await store.importFixture("router.ovpn", Self.needsCredentials)
            var has = await store.hasSavedCredentials(profileID: id, kind: .userPassword)
            #expect(!has)
            try await saved.save(Credentials(username: "alice", password: "s3cret"), profileID: id, kind: .userPassword)
            has = await store.hasSavedCredentials(profileID: id, kind: .userPassword)
            #expect(has)
            has = await store.hasSavedCredentials(profileID: id, kind: .keyPassphrase)
            #expect(!has)

            await store.forgetSavedCredentials(profileID: id)
            has = await store.hasSavedCredentials(profileID: id, kind: .userPassword)
            #expect(!has)
        }
    }
}
