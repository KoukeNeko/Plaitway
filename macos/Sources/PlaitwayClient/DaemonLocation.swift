import Foundation

public enum DaemonLocation {
    /// Where the LaunchDaemon listens: the `-socket` argument in the daemon's
    /// launchd plist (packaging/package-app.sh) says the same.
    public static let productionSocketPath = "/var/run/plaitway/plaitwayd.sock"

    /// What the production daemon keeps on disk, readable by root only: the
    /// constants of cmd/plaitwayd (`productionStateDir`, `productionLogFile`).
    /// Unregistering the helper leaves both behind.
    public static let stateDirectory = "/Library/Application Support/Plaitway"
    public static let logPath = "/Library/Logs/Plaitway/plaitwayd.log"

    /// The socket the app connects to: `override` in a debug build, otherwise the production socket.
    public static func socketPath(environment: [String: String] = ProcessInfo.processInfo.environment) -> String {
        override(environment: environment) ?? productionSocketPath
    }

    /// The socket named by `PLAITWAY_SOCKET`, for running against a daemon started
    /// with `-socket` (the development daemon, `plaitwayd -fake`). The app does not
    /// manage the daemon behind such a socket.
    ///
    /// Debug builds only (`swift run`, `swift test`); a release build ignores the
    /// variable. The app trusts the daemon behind its socket and answers its
    /// credential requests from the Keychain, so a signed app that followed
    /// `PLAITWAY_SOCKET` would hand every saved password to whatever process the
    /// user, or anything running as the user, put on that socket. The compile-time
    /// switch cannot be turned on from the environment or by copying the bundle.
    public static func override(environment: [String: String] = ProcessInfo.processInfo.environment) -> String? {
        #if DEBUG
        return environment["PLAITWAY_SOCKET"].flatMap { $0.isEmpty ? nil : $0 }
        #else
        return nil
        #endif
    }
}
