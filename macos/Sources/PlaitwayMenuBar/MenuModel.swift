import PlaitwayClient

/// What the status item's menu lists, before it becomes NSMenuItems.
struct MenuModel: Equatable {
    /// What choosing a profile does: the menu has one row per profile and no buttons, so the
    /// row does what the profile needs.
    enum Action: Equatable {
        case connect
        case disconnect
        /// A failed profile is switched on already; asking again retries.
        case retry
        /// The credential sheet is in the window.
        case answerCredentials
    }

    struct Item: Equatable {
        enum Mark: Equatable { case off, on, mixed }

        let profileID: String
        let title: String
        let subtitle: String
        let mark: Mark
        let action: Action
        let isEnabled: Bool
    }

    /// Why the profiles below cannot be used, when they cannot.
    let status: String?
    /// The first line above the profiles: how the connections stand together.
    let summary: String?
    let items: [Item]
    let canDisconnectAll: Bool
    let canImport: Bool

    init(setup: DaemonSetup, profiles: [Profile]) {
        status = setup.menuStatus
        canImport = setup.isUsable
        // Profiles of a daemon that is not answering would show states that are no longer true.
        let shown = setup.isUsable ? profiles : []
        items = shown.map(Self.item)
        canDisconnectAll = shown.contains(where: \.desiredEnabled)
        summary = shown.isEmpty ? nil : Self.summary(of: shown, setup: setup)
    }

    static func item(for profile: Profile) -> Item {
        let (mark, action): (Item.Mark, Action) = switch profile.state {
        case .connected: (.on, .disconnect)
        case .connecting, .reconnecting, .disconnecting: (.mixed, .disconnect)
        case .failed: (.off, .retry)
        case .awaitingCredentials: (.mixed, .answerCredentials)
        case .disconnected, .unspecified, .UNRECOGNIZED: (.off, .connect)
        }
        return Item(
            profileID: profile.id,
            title: profile.name,
            subtitle: "\(profile.kind.label) · \(profile.state.label)",
            mark: mark,
            action: action,
            isEnabled: profile.state != .disconnecting
        )
    }

    private static func summary(of profiles: [Profile], setup: DaemonSetup) -> String {
        let aggregate = AggregateState(setup: setup, profiles: profiles)
        let connected = profiles.filter { $0.state == .connected }.count
        if aggregate == .connected || (connected > 0 && aggregate == .connecting) {
            return String(localized: "Connected: \(connected)", bundle: .module)
        }
        return aggregate.label
    }
}
