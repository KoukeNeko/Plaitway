import Foundation
import PlaitwayClient
import Testing

/// Runs the real Go daemon (the binary named by PLAITWAY_DAEMON) on its
/// in-memory backend (`-fake`), with its own socket and an empty state
/// directory, so that every test starts from no profiles. Without `fake` it
/// runs the real engines, which without root can parse and store profiles but
/// not connect them: for what only the real profile parser decides.
@MainActor
final class DaemonProcess {
    struct SetupError: Error, CustomStringConvertible {
        let description: String
    }

    private let directory: String
    private let binary: String
    private let fake: Bool
    private var process: Process?

    let socketPath: String
    let stateDirectory: String

    init(fake: Bool = true) throws {
        self.fake = fake
        guard let binary = ProcessInfo.processInfo.environment["PLAITWAY_DAEMON"] else {
            throw SetupError(description: "PLAITWAY_DAEMON is not set; run the tests with `make test`")
        }
        self.binary = binary
        // The socket path must stay under 104 bytes, so NSTemporaryDirectory() rather than a deep path.
        directory = (NSTemporaryDirectory() as NSString).appendingPathComponent("pw-\(UUID().uuidString.prefix(8))")
        socketPath = (directory as NSString).appendingPathComponent("d.sock")
        stateDirectory = (directory as NSString).appendingPathComponent("state")
        try FileManager.default.createDirectory(atPath: directory, withIntermediateDirectories: true)
    }

    func start() async throws {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: binary)
        let runDirectory = (directory as NSString).appendingPathComponent("run")
        process.arguments = (fake ? ["-fake"] : []) + ["-socket", socketPath, "-state-dir", stateDirectory, "-run-dir", runDirectory]
        process.standardError = FileHandle.nullDevice
        try process.run()
        self.process = process

        let deadline = ContinuousClock.now + .seconds(5)
        while !FileManager.default.fileExists(atPath: socketPath) {
            if !process.isRunning { throw SetupError(description: "daemon exited with status \(process.terminationStatus)") }
            if ContinuousClock.now > deadline { throw SetupError(description: "daemon did not create \(socketPath)") }
            try await Task.sleep(for: .milliseconds(20))
        }
    }

    /// SIGKILL, like a crash: the socket file is left behind.
    func kill() {
        guard let process, process.isRunning else { return }
        Foundation.kill(process.processIdentifier, SIGKILL)
        process.waitUntilExit()
    }

    func cleanUp() {
        kill()
        try? FileManager.default.removeItem(atPath: directory)
    }
}
