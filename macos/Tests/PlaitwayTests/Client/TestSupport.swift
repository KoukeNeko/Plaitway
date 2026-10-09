import Foundation
import GRPCCore
import PlaitwayClient
import Testing

struct Timeout: Error, CustomStringConvertible {
    let description: String
}

@MainActor
func waitUntil(_ what: String, timeout: Duration = .seconds(10), until condition: () -> Bool) async throws {
    let deadline = ContinuousClock.now + timeout
    while !condition() {
        if ContinuousClock.now > deadline { throw Timeout(description: "timed out waiting for \(what)") }
        try await Task.sleep(for: .milliseconds(20))
    }
}

@MainActor
func waitUntilAsync(_ what: String, timeout: Duration = .seconds(10), until condition: () async -> Bool) async throws {
    let deadline = ContinuousClock.now + timeout
    while await !condition() {
        if ContinuousClock.now > deadline { throw Timeout(description: "timed out waiting for \(what)") }
        try await Task.sleep(for: .milliseconds(20))
    }
}

/// Starts a daemon and a store connected to it, and always tears both down.
@MainActor
func withDaemon(
    fake: Bool = true,
    credentials: any CredentialStore = InMemoryCredentialStore(),
    _ body: (DaemonProcess, ProfileStore) async throws -> Void
) async throws {
    let daemon = try DaemonProcess(fake: fake)
    defer { daemon.cleanUp() }
    try await daemon.start()
    let store = try ProfileStore(socketPath: daemon.socketPath, credentialStore: credentials)
    store.start()
    do {
        try await waitUntil("the first snapshot") { store.connection == .connected }
        try await body(daemon, store)
    } catch {
        await store.stop()
        throw error
    }
    await store.stop()
}

extension ProfileStore {
    func profile(_ id: String) -> Profile? {
        profiles.first { $0.id == id }
    }

    /// Imports a profile built from `Fixture`, waits until the store shows it and returns its id.
    @discardableResult
    func importFixture(_ filename: String, _ content: Data = Fixture.openVPN(), settings: ProfileSettings = .init()) async throws -> String {
        let id = try await importProfile(content: content, sourceFilename: filename, settings: settings).profile.id
        try await waitUntil("\(filename) to appear") { profile(id) != nil }
        return id
    }

    func waitForState(_ id: String, _ state: ProfileState) async throws {
        try await waitUntil("\(id) to be \(state)") { profile(id)?.state == state }
    }

    /// Every state `id` is in from now on, in order and without repeats. It is told of each change of the
    /// store as it happens, not asked for the state now and then: a state that lasts half a second is
    /// over before a busy machine gets round to asking.
    func recordStates(of id: String) -> StateRecorder {
        let recorder = StateRecorder()
        recorder.observe { [self] in
            guard let state = profile(id)?.state, recorder.states.last != state else { return }
            recorder.states.append(state)
        }
        return recorder
    }
}

@MainActor
final class StateRecorder {
    fileprivate(set) var states: [ProfileState] = []
    private var isStopped = false

    /// Calls `read` now and after each change of what it reads, until `stop`.
    fileprivate func observe(_ read: @escaping @MainActor () -> Void) {
        guard !isStopped else { return }
        withObservationTracking {
            read()
        } onChange: { [weak self] in
            // onChange runs before the change is made: read once it is.
            Task { @MainActor in self?.observe(read) }
        }
    }

    func stop() { isStopped = true }
}

enum Fixture {
    /// A profile in the style of the router profile the product has to handle.
    /// The fake daemon reads `markers` (`# fake: ...`) to decide how it behaves.
    static func openVPN(host: String = "vpn.example.net", markers: [String] = [], extra: [String] = []) -> Data {
        let lines = [
            "client", "dev tun", "proto tcp-client", "remote \(host) 1194", "route 192.168.1.0 255.255.255.0",
        ] + extra + markers + [
            "<ca>", "-----BEGIN CERTIFICATE-----", "MIIBtestonlynotacertificate", "-----END CERTIFICATE-----", "</ca>",
        ]
        return Data((lines.joined(separator: "\n") + "\n").utf8)
    }

    static func wireGuard(allowedIPs: String = "0.0.0.0/0", endpoint: String = "203.0.113.5:51820") -> Data {
        let text = """
        [Interface]
        PrivateKey = AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
        Address = 10.6.0.2/32
        DNS = 10.6.0.1

        [Peer]
        PublicKey = BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=
        AllowedIPs = \(allowedIPs)
        Endpoint = \(endpoint)

        """
        return Data(text.utf8)
    }
}

/// The first line of `stream` that satisfies `predicate`; fails when the stream
/// ends or `timeout` passes first.
func firstLine(
    in stream: AsyncThrowingStream<LogLine, any Error>,
    timeout: Duration = .seconds(10),
    where predicate: @escaping @Sendable (LogLine) -> Bool
) async throws -> LogLine {
    try await withThrowingTaskGroup(of: LogLine.self) { group in
        group.addTask {
            for try await line in stream where predicate(line) { return line }
            throw Timeout(description: "the log stream ended without a matching line")
        }
        group.addTask {
            try await Task.sleep(for: timeout)
            throw Timeout(description: "timed out waiting for a log line")
        }
        defer { group.cancelAll() }
        return try await group.next()!
    }
}

/// A text file of the repository, such as a profile in internal/ovpn/testdata.
func repositoryText(_ path: String) throws -> String {
    // This file is five levels below the repository root: macos/Tests/PlaitwayTests/Client/TestSupport.swift.
    var url = URL(fileURLWithPath: #filePath).resolvingSymlinksInPath()
    for _ in 0..<5 { url.deleteLastPathComponent() }
    return try String(contentsOf: url.appendingPathComponent(path), encoding: .utf8)
}

/// SplitMix64: the same sequence for the same seed on every run.
struct SeededGenerator: RandomNumberGenerator {
    var state: UInt64

    mutating func next() -> UInt64 {
        state &+= 0x9E37_79B9_7F4A_7C15
        var z = state
        z = (z ^ (z >> 30)) &* 0xBF58_476D_1CE4_E5B9
        z = (z ^ (z >> 27)) &* 0x94D0_49BB_1331_11EB
        return z ^ (z >> 31)
    }
}
