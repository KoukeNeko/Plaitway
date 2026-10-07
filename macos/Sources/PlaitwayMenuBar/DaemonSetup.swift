import PlaitwayClient

/// Where the helper stands, from what the app knows: the connection to the
/// daemon, SMAppService's registration and the versions.
enum DaemonSetup: Equatable {
    case ready
    /// Connected, but the helper is from another version of the app.
    case versionMismatch(daemon: String, app: String)
    case connecting
    case needsInstall
    /// Not installed, and the app runs from where the helper cannot be registered.
    case misplaced
    case needsApproval
    case missingFromBundle
    /// Registered and not answering.
    case notResponding
    /// Not running from an app bundle, or talking to a daemon of its own
    /// (`PLAITWAY_SOCKET`): the app does not manage the helper.
    case development

    /// - Parameters:
    ///   - isOverridden: `PLAITWAY_SOCKET` names the daemon.
    ///   - isSettling: the helper was just registered or retried and may still be starting.
    init(
        connection: ProfileStore.Connection,
        registration: DaemonRegistration,
        daemonVersion: String?,
        appVersion: String?,
        isOverridden: Bool,
        isSettling: Bool
    ) {
        switch connection {
        case .connected:
            if !isOverridden, let appVersion, let daemonVersion, appVersion != daemonVersion {
                self = .versionMismatch(daemon: daemonVersion, app: appVersion)
            } else {
                self = .ready
            }
        case .connecting, .unavailable:
            if isOverridden || registration == .notBundled {
                self = connection == .connecting ? .connecting : .development
                return
            }
            // While connecting, a helper that something else installed may be about to answer.
            switch registration {
            case .notRegistered, .notBundled: self = connection == .connecting ? .connecting : .needsInstall
            case .misplaced: self = connection == .connecting ? .connecting : .misplaced
            case .requiresApproval: self = .needsApproval
            case .notFound: self = .missingFromBundle
            case .enabled: self = connection == .connecting || isSettling ? .connecting : .notResponding
            }
        }
    }

    /// There is no helper yet, or it waits for approval: the window opens at launch to say so.
    var offersInstallation: Bool {
        switch self {
        case .needsInstall, .misplaced, .needsApproval: true
        default: false
        }
    }

    /// The daemon answers and can be given commands.
    var isUsable: Bool {
        switch self {
        case .ready, .versionMismatch: true
        default: false
        }
    }
}
