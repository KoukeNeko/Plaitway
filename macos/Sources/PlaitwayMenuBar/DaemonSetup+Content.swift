import PlaitwayClient

/// What the window shows instead of the profiles while the helper is not ready.
struct SetupContent: Equatable {
    enum Action: Equatable {
        case installHelper
        case openSystemSettings
        case retry
        case reinstallHelper

        var label: String {
            switch self {
            case .installHelper: String(localized: "Install Helper", bundle: .module)
            case .openSystemSettings: String(localized: "Open System Settings", bundle: .module)
            case .retry: String(localized: "Retry", bundle: .module)
            case .reinstallHelper: String(localized: "Reinstall Helper", bundle: .module)
            }
        }
    }

    let symbol: String
    let title: String
    let detail: String?
    let showsProgress: Bool
    let primary: Action?
    let secondary: Action?
}

extension DaemonSetup {
    /// The state as the menu's disabled first line says it; nil when there is nothing to say.
    var menuStatus: String? {
        switch self {
        case .ready: nil
        case .versionMismatch: String(localized: "Helper out of date", bundle: .module)
        case .connecting: String(localized: "Connecting", bundle: .module)
        case .needsInstall: String(localized: "Helper not installed", bundle: .module)
        case .misplaced: String(localized: "Not in Applications", bundle: .module)
        case .needsApproval: String(localized: "Approval required", bundle: .module)
        case .missingFromBundle: String(localized: "Helper not found", bundle: .module)
        case .notResponding, .development: String(localized: "Helper unavailable", bundle: .module)
        }
    }

    /// The window's content; nil while the profiles can be shown.
    var content: SetupContent? {
        switch self {
        case .ready, .versionMismatch:
            nil
        case .connecting:
            SetupContent(symbol: "lock.shield", title: String(localized: "Connecting", bundle: .module), detail: nil, showsProgress: true, primary: nil, secondary: nil)
        case .needsInstall:
            SetupContent(
                symbol: "lock.shield",
                title: String(localized: "Helper not installed", bundle: .module),
                detail: String(localized: "The helper manages routes and DNS with administrator rights. macOS asks for approval once.", bundle: .module),
                showsProgress: false, primary: .installHelper, secondary: nil
            )
        case .misplaced:
            SetupContent(
                symbol: "folder",
                title: String(localized: "Not in Applications", bundle: .module),
                detail: String(localized: "Move Plaitway to the Applications folder and open it again.", bundle: .module),
                showsProgress: false, primary: nil, secondary: nil
            )
        case .needsApproval:
            SetupContent(
                symbol: "hand.raised",
                title: String(localized: "Approval required", bundle: .module),
                detail: String(localized: "Allow Plaitway in Login Items.", bundle: .module),
                showsProgress: false, primary: .openSystemSettings, secondary: nil
            )
        case .missingFromBundle:
            SetupContent(
                symbol: "exclamationmark.triangle",
                title: String(localized: "Helper not found", bundle: .module),
                detail: String(localized: "The helper is missing from this copy of Plaitway. Copy the app to Applications again.", bundle: .module),
                showsProgress: false, primary: .retry, secondary: nil
            )
        case .notResponding:
            SetupContent(
                symbol: "exclamationmark.triangle",
                title: String(localized: "Helper unavailable", bundle: .module),
                // The registration is fine; the log is the only trace of why the daemon does not run.
                detail: [
                    String(localized: "Registered, not answering.", bundle: .module),
                    String(localized: "Helper log: \(DaemonLocation.logPath)", bundle: .module),
                ].joined(separator: "\n"),
                showsProgress: false, primary: .retry, secondary: .reinstallHelper
            )
        case .development:
            SetupContent(
                symbol: "hammer",
                title: String(localized: "Helper unavailable", bundle: .module),
                detail: String(localized: "Start plaitwayd and set PLAITWAY_SOCKET.", bundle: .module),
                showsProgress: false, primary: .retry, secondary: nil
            )
        }
    }
}
