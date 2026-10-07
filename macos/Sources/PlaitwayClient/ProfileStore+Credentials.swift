import Foundation
import GRPCCore
import PlaitwayAPI

/// A profile that waits for the user to type what the daemon asked for.
public struct CredentialPrompt: Identifiable, Equatable, Sendable {
    public var id: String { profileID }
    public let profileID: String
    public let kind: CredentialKind
    /// The daemon refused what was sent before; `message` is its reason, which
    /// may be empty.
    public let rejected: Bool
    public let message: String
}

// Credential requests. A profile that asks is answered from the credential
// store when it has an answer, so the user types each password once. The sheet
// appears when there is none, and again when the daemon refuses what was sent.
extension ProfileStore {
    /// Sends what the user typed, and keeps it for the next request.
    public func provideCredentials(profileID: String, username: String = "", password: String) async throws {
        guard let prompt = credentialPrompts.first(where: { $0.profileID == profileID }) else {
            throw RPCError(code: .failedPrecondition, message: "the profile is not waiting for credentials")
        }
        let credentials = Credentials(username: username, password: password)
        await saveCredentials(credentials, profileID: profileID, kind: prompt.kind)
        try await send(credentials, profileID: profileID, kind: prompt.kind)
        credentialPrompts.removeAll { $0.profileID == profileID }
    }

    /// The user gave up on the sheet: stop connecting the profile.
    public func cancelCredentials(profileID: String) async throws {
        credentialPrompts.removeAll { $0.profileID == profileID }
        answeredProfiles.remove(profileID)
        try await setEnabled(false, profileID: profileID)
    }

    /// Keeps an answer for the next request. Connecting matters more than
    /// remembering, so a failure is logged and the next request asks again.
    func saveCredentials(_ credentials: Credentials, profileID: String, kind: CredentialKind) async {
        do {
            try await credentialStore.save(credentials, profileID: profileID, kind: kind)
        } catch {
            Self.logger.error("could not save the credentials of \(profileID): \(error)")
        }
    }

    /// Whether the credential store holds an answer for the profile.
    public func hasSavedCredentials(profileID: String, kind: CredentialKind) async -> Bool {
        do {
            return try await credentialStore.hasCredentials(profileID: profileID, kind: kind)
        } catch {
            Self.logger.error("could not look up the saved credentials of \(profileID): \(error)")
            return false
        }
    }

    public func forgetSavedCredentials(profileID: String) async {
        for kind in [CredentialKind.userPassword, .keyPassphrase] {
            await removeSavedCredentials(profileID: profileID, kind: kind)
        }
    }

    // MARK: Watching the requests

    /// Called for every profile the daemon reports.
    func reviewCredentialRequest(_ profile: Profile) {
        guard profile.state == .awaitingCredentials else {
            credentialPrompts.removeAll { $0.profileID == profile.id }
            // CONNECTING is the daemon checking what was sent; only an attempt
            // that ended starts the next one with a clean slate.
            switch profile.state {
            case .connected, .disconnected, .failed: answeredProfiles.remove(profile.id)
            default: break
            }
            return
        }

        let kind = profile.credentialRequest.kind
        if credentialPrompts.contains(where: { $0.profileID == profile.id && $0.kind == kind }) { return }
        // The Keychain may be slow to answer; one lookup per profile at a time.
        guard resolvingProfiles.insert(profile.id).inserted else { return }

        let rejected = answeredProfiles.contains(profile.id) || !profile.lastError.isEmpty
        Task {
            await resolveRequest(of: profile, kind: kind, rejected: rejected)
            resolvingProfiles.remove(profile.id)
        }
    }

    func forgetCredentials(of profileID: String) {
        credentialPrompts.removeAll { $0.profileID == profileID }
        answeredProfiles.remove(profileID)
        Task { await forgetSavedCredentials(profileID: profileID) }
    }

    func discardPromptsOfRemovedProfiles() {
        let existing = Set(profiles.map(\.id))
        credentialPrompts.removeAll { !existing.contains($0.profileID) }
    }

    // MARK: Helpers

    /// Answers the request from the credential store, or asks the user.
    private func resolveRequest(of profile: Profile, kind: CredentialKind, rejected: Bool) async {
        if rejected {
            await removeSavedCredentials(profileID: profile.id, kind: kind)
            answeredProfiles.remove(profile.id)
            showPrompt(for: profile, rejected: true)
            return
        }

        let saved = await savedCredentials(profileID: profile.id, kind: kind)
        // The Keychain took its time; the profile may have been answered, stopped or deleted meanwhile.
        guard let current = profiles.first(where: { $0.id == profile.id }), current.state == .awaitingCredentials else { return }
        guard let saved else {
            showPrompt(for: current, rejected: false)
            return
        }
        do {
            try await send(saved, profileID: profile.id, kind: kind)
        } catch {
            // Not a refusal of the credentials: the daemon could not take them.
            Self.logger.error("could not answer the credential request of \(profile.id): \(error)")
            if profiles.first(where: { $0.id == profile.id })?.state == .awaitingCredentials {
                showPrompt(for: current, rejected: false)
            }
        }
    }

    private func send(_ credentials: Credentials, profileID: String, kind: CredentialKind) async throws {
        answeredProfiles.insert(profileID)
        do {
            _ = try await api.provideCredentials(.with {
                $0.profileID = profileID
                $0.kind = kind
                $0.username = credentials.username
                $0.password = credentials.password
            })
        } catch {
            answeredProfiles.remove(profileID)
            throw error
        }
    }

    private func showPrompt(for profile: Profile, rejected: Bool) {
        let prompt = CredentialPrompt(
            profileID: profile.id,
            kind: profile.credentialRequest.kind,
            rejected: rejected,
            message: profile.lastError
        )
        credentialPrompts.removeAll { $0.profileID == profile.id }
        credentialPrompts.append(prompt)
    }

    private func savedCredentials(profileID: String, kind: CredentialKind) async -> Credentials? {
        do {
            return try await credentialStore.credentials(profileID: profileID, kind: kind)
        } catch {
            // Including a refusal by the user when the Keychain asks: the sheet asks instead.
            Self.logger.error("could not read the saved credentials of \(profileID): \(error)")
            return nil
        }
    }

    private func removeSavedCredentials(profileID: String, kind: CredentialKind) async {
        do {
            try await credentialStore.remove(profileID: profileID, kind: kind)
        } catch {
            Self.logger.error("could not remove the saved credentials of \(profileID): \(error)")
        }
    }
}
