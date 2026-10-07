import PlaitwayClient

/// What the menu bar icon says about all profiles together. What needs the person comes
/// before what is under way: a profile waiting for a password does not get there by itself.
enum AggregateState: Equatable {
    case unavailable
    case idle
    case connected
    case connecting
    case needsCredentials
    case problem

    init(setup: DaemonSetup, profiles: [Profile]) {
        switch setup {
        case .connecting:
            self = .connecting
        case .ready, .versionMismatch:
            let states = Set(profiles.map(\.state))
            if states.contains(.failed) {
                self = .problem
            } else if states.contains(.awaitingCredentials) {
                self = .needsCredentials
            } else if !states.isDisjoint(with: [.connecting, .reconnecting, .disconnecting]) {
                self = .connecting
            } else if states.contains(.connected) {
                self = .connected
            } else {
                self = .idle
            }
        case .needsInstall, .misplaced, .needsApproval, .missingFromBundle, .notResponding, .development:
            self = .unavailable
        }
    }

    /// A template symbol with a shape of its own per state: the menu bar draws it in one colour.
    var symbolName: String {
        switch self {
        case .unavailable: "exclamationmark.triangle"
        case .idle: "lock.shield"
        case .connected: "lock.shield.fill"
        case .connecting: "lock.rotation"
        case .needsCredentials: "key.fill"
        case .problem: "exclamationmark.shield"
        }
    }

    /// The icon of an idle app is dimmed, as the system's own menu extras are when they are off.
    var appearsDisabled: Bool {
        self == .idle
    }

    var label: String {
        switch self {
        case .unavailable: String(localized: "Helper unavailable", bundle: .module)
        case .idle: String(localized: "Not connected", bundle: .module)
        case .connected: String(localized: "Connected", bundle: .module)
        case .connecting: String(localized: "Connecting", bundle: .module)
        case .needsCredentials: String(localized: "Awaiting credentials", bundle: .module)
        case .problem: String(localized: "Problem", bundle: .module)
        }
    }
}
