import Foundation
import GRPCCore
import GRPCNIOTransportHTTP2Posix
import Observation
import os
import PlaitwayAPI

/// The daemon's profiles as the UI sees them. The daemon is the only source of
/// truth: every command sends a request and the result arrives through the
/// watch stream, so the store never guesses a state.
///
/// Call `start()` once and `stop()` before dropping the store; the background
/// tasks keep it alive until then.
@MainActor
@Observable
public final class ProfileStore {
    public enum Connection: Equatable, Sendable {
        case connecting
        case connected
        case unavailable
    }

    public private(set) var connection: Connection = .connecting
    /// Last known profiles in priority order, highest first. Kept while the
    /// daemon is unavailable so the UI does not empty out on a reconnect.
    public private(set) var profiles: [Profile] = []
    /// What the daemon said about itself on the latest connection.
    public private(set) var daemonInfo: DaemonInfo?
    /// Profiles waiting for the user to type credentials, see `ProfileStore+Credentials.swift`.
    public internal(set) var credentialPrompts: [CredentialPrompt] = []

    @ObservationIgnored let credentialStore: any CredentialStore
    /// Profiles that were sent credentials during the current connection
    /// attempt: asking again means the daemon refused them.
    @ObservationIgnored var answeredProfiles: Set<String> = []
    /// Profiles whose credential request is being looked up in the credential store.
    @ObservationIgnored var resolvingProfiles: Set<String> = []
    @ObservationIgnored let api: Plaitway_V1_DaemonService.Client<HTTP2ClientTransport.Posix>
    @ObservationIgnored private let client: GRPCClient<HTTP2ClientTransport.Posix>
    @ObservationIgnored private var tasks: [Task<Void, Never>] = []

    /// How long a dropped watch waits before it is retried.
    private static let retryInterval: Duration = .milliseconds(500)
    static let logger = Logger(subsystem: "Plaitway", category: "ProfileStore")

    public init(
        socketPath: String,
        credentialStore: any CredentialStore = KeychainCredentialStore()
    ) throws {
        // grpc-swift's default reconnect backoff grows to 2 minutes; the menu
        // bar app should notice a restarted daemon within seconds.
        let transport = try HTTP2ClientTransport.Posix(
            target: .unixDomainSocket(path: socketPath),
            transportSecurity: .plaintext,
            config: .defaults { config in
                config.backoff = .init(initial: .milliseconds(200), max: .seconds(5), multiplier: 1.6, jitter: 0.2)
            }
        )
        client = GRPCClient(transport: transport)
        api = Plaitway_V1_DaemonService.Client(wrapping: client)
        self.credentialStore = credentialStore
    }

    public func start() {
        guard tasks.isEmpty else { return }
        let client = client
        tasks = [
            Task {
                do { try await client.runConnections() } catch { Self.logger.error("connection loop failed: \(error)") }
            },
            Task { await watchUntilCancelled() },
        ]
    }

    public func stop() async {
        client.beginGracefulShutdown()
        tasks.forEach { $0.cancel() }
        for task in tasks { await task.value }
        tasks = []
    }

    // MARK: Commands
    //
    // These throw `RPCError`: `.notFound` for an unknown id, `.permissionDenied`
    // for a call that needs an administrator, `.invalidArgument` for rejected
    // input and `.unavailable` when the daemon is down. `DaemonFailure` turns
    // one into something a UI can word.

    /// Asks the daemon to connect or disconnect a profile. Enabling a failed
    /// profile retries it.
    public func setEnabled(_ enabled: Bool, profileID: String) async throws {
        _ = try await api.setProfileEnabled(.with {
            $0.id = profileID
            $0.enabled = enabled
        })
    }

    /// Stores a new profile, last in priority order. A rejected profile fails
    /// with `.invalidArgument` and the daemon's reason as the message.
    /// - Parameter credentials: the user name and password the profile came
    ///   with (`ProfileImporter.Loaded`); they are saved for the new profile, so
    ///   that its first connection does not ask.
    public func importProfile(
        content: Data,
        sourceFilename: String,
        name: String = "",
        settings: ProfileSettings = .init(),
        credentials: Credentials? = nil
    ) async throws -> ProfileImport {
        let response = try await api.importProfile(.with {
            $0.name = name
            $0.content = content
            $0.sourceFilename = sourceFilename
            $0.settings = settings
        })
        if let credentials {
            await saveCredentials(credentials, profileID: response.profile.id, kind: .userPassword)
        }
        return ProfileImport(profile: response.profile, warnings: response.warnings)
    }

    /// Changes the name, the settings or both. A connected profile keeps running
    /// with the settings it was started with.
    public func updateProfile(id: String, name: String? = nil, settings: ProfileSettings? = nil) async throws {
        _ = try await api.updateProfile(.with {
            $0.id = id
            if let name { $0.name = name }
            if let settings { $0.settings = settings }
        })
    }

    /// The stored text of a profile, private keys and inline certificates
    /// included. Needs an administrator: anyone else gets `.permissionDenied`.
    public func profileContent(id: String) async throws -> String {
        let response = try await api.getProfileContent(.with { $0.id = id })
        // Not `String(data:encoding:)`, which drops a byte order mark the profile may start with.
        guard let text = String(validating: response.content, as: UTF8.self) else {
            throw RPCError(code: .dataLoss, message: "the profile text is not valid UTF-8")
        }
        return text
    }

    /// Replaces the text of a profile with the checks of an import: a rejected
    /// text fails with `.invalidArgument` and the daemon's reason (see
    /// `ConfigDiagnostic`), and so does text of the other kind. The result is the
    /// profile with its refreshed summary and what the daemon removed from the text.
    /// - Parameter reconnect: restarts an enabled profile with the new text at
    ///   once. Otherwise a running tunnel keeps the old text until it connects again.
    public func updateProfileContent(id: String, content: String, reconnect: Bool = false) async throws -> ProfileImport {
        let response = try await api.updateProfileContent(.with {
            $0.id = id
            $0.content = Data(content.utf8)
            $0.reconnect = reconnect
        })
        return ProfileImport(profile: response.profile, warnings: response.warnings)
    }

    /// Disconnects the profile and removes it, with the credentials saved for it.
    public func deleteProfile(id: String) async throws {
        _ = try await api.deleteProfile(.with { $0.id = id })
        // The watch reports the removal too, but it may be down right now, and
        // the Keychain would keep the password of a profile that no longer exists.
        await forgetSavedCredentials(profileID: id)
    }

    /// `ids` lists every profile, highest priority first.
    public func reorder(ids: [String]) async throws {
        _ = try await api.reorderProfiles(.with { $0.ids = ids })
    }

    public func fetchDiagnostics() async throws -> Diagnostics {
        try await api.getDiagnostics(.init())
    }

    /// Re-reads the network and rebuilds every route and DNS entry the daemon owns.
    public func resync() async throws {
        _ = try await api.resync(.init())
    }

    /// Removes a route that `fetchDiagnostics` listed as stale.
    public func removeStaleRoute(key: String) async throws {
        _ = try await api.removeStaleRoute(.with { $0.key = key })
    }

    /// The buffered tail of a profile's log and then its live lines, until the
    /// consumer stops iterating. An empty `profileID` is the daemon's own log.
    public func logs(profileID: String, tailLines: Int32 = 200) -> AsyncThrowingStream<LogLine, any Error> {
        let api = api
        return AsyncThrowingStream { continuation in
            let task = Task {
                do {
                    try await api.watchLogs(.with {
                        $0.profileID = profileID
                        $0.tailLines = tailLines
                    }) { response in
                        for try await line in response.messages {
                            continuation.yield(line)
                        }
                    }
                    continuation.finish()
                } catch {
                    continuation.finish(throwing: error)
                }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    // MARK: Watching

    private func watchUntilCancelled() async {
        while !Task.isCancelled {
            var failure: (any Error)?
            do {
                try await api.watchProfiles(.init()) { response in
                    for try await event in response.messages {
                        await self.apply(event)
                    }
                }
            } catch {
                failure = error
            }
            if Task.isCancelled { return }
            markUnavailable(because: failure)
            // Cancellation of the sleep is picked up by the loop condition.
            try? await Task.sleep(for: Self.retryInterval)
        }
    }

    /// The daemon being down or refusing us (for example permission denied)
    /// looks the same to the UI, so the cause is logged once per outage.
    private func markUnavailable(because failure: (any Error)?) {
        if connection != .unavailable {
            Self.logger.error("daemon unavailable: \(failure.map { "\($0)" } ?? "watch stream ended")")
        }
        connection = .unavailable
        // Whatever the engines were asking for is gone with the daemon; the
        // next snapshot says what is still asked.
        credentialPrompts = []
        answeredProfiles = []
    }

    private func apply(_ event: Plaitway_V1_ProfileEvent) {
        switch event.event {
        case .snapshot(let snapshot):
            profiles = Self.inPriorityOrder(snapshot.profiles)
            connection = .connected
            snapshot.profiles.forEach(reviewCredentialRequest)
            discardPromptsOfRemovedProfiles()
            Task { await refreshDaemonInfo() }
        case .changed(let changed):
            var updated = profiles
            if let index = updated.firstIndex(where: { $0.id == changed.id }) {
                updated[index] = changed
            } else {
                updated.append(changed)
            }
            profiles = Self.inPriorityOrder(updated)
            reviewCredentialRequest(changed)
        case .removed(let id):
            profiles.removeAll { $0.id == id }
            forgetCredentials(of: id)
        case nil:
            break
        }
    }

    /// Priority is the daemon's: lower wins. Events arrive one profile at a
    /// time, so profiles with equal priority (in the middle of a reorder) keep
    /// the position they have.
    private static func inPriorityOrder(_ profiles: [Profile]) -> [Profile] {
        profiles.enumerated()
            .sorted { ($0.element.settings.priority, $0.offset) < ($1.element.settings.priority, $1.offset) }
            .map(\.element)
    }

    private func refreshDaemonInfo() async {
        do {
            daemonInfo = try await api.getDaemonInfo(.init())
        } catch {
            Self.logger.error("could not read the daemon's version: \(error)")
        }
    }
}

/// An imported or edited profile and what the daemon removed or ignored in it.
public struct ProfileImport: Sendable {
    public let profile: Profile
    public let warnings: [ImportWarning]
}
