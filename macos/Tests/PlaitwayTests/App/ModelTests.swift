import AppKit
import Foundation
import PlaitwayClient
import Testing
@testable import PlaitwayMenuBar

func makeProfile(_ id: String = "p", name: String = "Profile", state: ProfileState = .disconnected, desired: Bool = false) -> Profile {
    var profile = Profile()
    profile.id = id
    profile.name = name
    profile.state = state
    profile.desiredEnabled = desired
    return profile
}

struct DaemonSetupTests {
    struct Case: CustomTestStringConvertible, Sendable {
        var connection: ProfileStore.Connection = .unavailable
        var registration: DaemonRegistration = .enabled
        var daemonVersion: String? = nil
        var appVersion: String? = "0.1.0"
        var isOverridden = false
        var isSettling = false
        let expected: DaemonSetup
        var testDescription: String { "\(connection) \(registration) daemon=\(daemonVersion ?? "-") settling=\(isSettling) -> \(expected)" }
    }

    @Test(arguments: [
        Case(connection: .connected, daemonVersion: "0.1.0", expected: .ready),
        Case(connection: .connected, daemonVersion: "0.0.9", expected: .versionMismatch(daemon: "0.0.9", app: "0.1.0")),
        Case(connection: .connected, daemonVersion: "0.0.0-dev", appVersion: nil, expected: .ready),
        Case(connection: .connected, daemonVersion: "0.0.0-dev", isOverridden: true, expected: .ready),
        Case(connection: .connected, registration: .notRegistered, daemonVersion: nil, expected: .ready),
        Case(connection: .unavailable, registration: .notRegistered, expected: .needsInstall),
        // A helper that something else installed may be about to answer: not "not installed" yet.
        Case(connection: .connecting, registration: .notRegistered, expected: .connecting),
        Case(connection: .unavailable, registration: .misplaced, expected: .misplaced),
        Case(connection: .connecting, registration: .misplaced, expected: .connecting),
        Case(connection: .connected, registration: .misplaced, daemonVersion: "0.1.0", expected: .ready),
        Case(connection: .unavailable, registration: .requiresApproval, expected: .needsApproval),
        Case(connection: .unavailable, registration: .notFound, expected: .missingFromBundle),
        Case(connection: .unavailable, registration: .enabled, expected: .notResponding),
        Case(connection: .unavailable, registration: .enabled, isSettling: true, expected: .connecting),
        Case(connection: .connecting, registration: .enabled, expected: .connecting),
        Case(connection: .unavailable, registration: .notBundled, expected: .development),
        Case(connection: .connecting, registration: .notBundled, expected: .connecting),
        Case(connection: .unavailable, registration: .notRegistered, isOverridden: true, expected: .development),
    ])
    func resolves(_ item: Case) {
        let setup = DaemonSetup(
            connection: item.connection, registration: item.registration, daemonVersion: item.daemonVersion,
            appVersion: item.appVersion, isOverridden: item.isOverridden, isSettling: item.isSettling
        )
        #expect(setup == item.expected)
    }

    @Test func onlyAnAnsweringDaemonIsUsable() {
        #expect(DaemonSetup.ready.isUsable)
        #expect(DaemonSetup.versionMismatch(daemon: "1", app: "2").isUsable)
        for setup in [DaemonSetup.connecting, .needsInstall, .misplaced, .needsApproval, .missingFromBundle, .notResponding, .development] {
            #expect(!setup.isUsable, "\(setup)")
        }
    }

    @Test func oneThingToDoPerState() {
        #expect(DaemonSetup.needsInstall.content?.primary == .installHelper)
        #expect(DaemonSetup.needsApproval.content?.primary == .openSystemSettings)
        #expect(DaemonSetup.notResponding.content?.primary == .retry)
        #expect(DaemonSetup.notResponding.content?.secondary == .reinstallHelper)
        // Nothing to press: the app has to be moved and opened again.
        #expect(DaemonSetup.misplaced.content?.primary == nil)
        #expect(DaemonSetup.misplaced.content?.secondary == nil)
        #expect(DaemonSetup.misplaced.content?.detail?.isEmpty == false)
        #expect(DaemonSetup.missingFromBundle.content?.primary == .retry)
        #expect(DaemonSetup.connecting.content?.primary == nil)
        #expect(DaemonSetup.connecting.content?.showsProgress == true)
        // The profiles are shown once the daemon answers, even when it is out of date.
        #expect(DaemonSetup.ready.content == nil)
        #expect(DaemonSetup.versionMismatch(daemon: "1", app: "2").content == nil)
    }

    @Test func anUnansweredHelperSaysWhereToLook() throws {
        let detail = try #require(DaemonSetup.notResponding.content?.detail)
        #expect(detail.contains(DaemonLocation.logPath))
    }

    @Test func onlyAMissingHelperOpensTheWindowAtLaunch() {
        for setup in [DaemonSetup.needsInstall, .misplaced, .needsApproval] {
            #expect(setup.offersInstallation, "\(setup)")
        }
        // A helper that answers, however it was installed, needs no window.
        for setup in [DaemonSetup.ready, .versionMismatch(daemon: "1", app: "2"), .connecting, .missingFromBundle, .notResponding, .development] {
            #expect(!setup.offersInstallation, "\(setup)")
        }
    }

    @Test func everyBlockedStateIsNamedInTheMenu() {
        #expect(DaemonSetup.ready.menuStatus == nil)
        for setup in [DaemonSetup.versionMismatch(daemon: "1", app: "2"), .connecting, .needsInstall, .misplaced, .needsApproval, .missingFromBundle, .notResponding, .development] {
            #expect(setup.menuStatus?.isEmpty == false, "\(setup)")
        }
    }
}

struct AggregateStateTests {
    @Test(arguments: [
        ([ProfileState](), AggregateState.idle),
        ([.disconnected, .disconnected], .idle),
        ([.disconnected, .connected], .connected),
        ([.connected, .connecting], .connecting),
        ([.connected, .reconnecting], .connecting),
        ([.disconnected, .awaitingCredentials], .needsCredentials),
        ([.connecting, .awaitingCredentials], .needsCredentials),
        ([.connected, .awaitingCredentials], .needsCredentials),
        ([.failed, .awaitingCredentials], .problem),
        ([.connected, .disconnecting], .connecting),
        ([.connected, .failed], .problem),
        ([.connecting, .failed], .problem),
    ])
    func summarisesTheProfiles(states: [ProfileState], expected: AggregateState) {
        let profiles = states.enumerated().map { makeProfile("p\($0.offset)", state: $0.element) }
        #expect(AggregateState(setup: .ready, profiles: profiles) == expected)
        #expect(AggregateState(setup: .versionMismatch(daemon: "1", app: "2"), profiles: profiles) == expected)
    }

    @Test func aDaemonThatDoesNotAnswerHidesWhatTheProfilesSaid() {
        let profiles = [makeProfile(state: .connected)]
        for setup in [DaemonSetup.needsInstall, .misplaced, .needsApproval, .missingFromBundle, .notResponding, .development] {
            #expect(AggregateState(setup: setup, profiles: profiles) == .unavailable, "\(setup)")
        }
        #expect(AggregateState(setup: .connecting, profiles: profiles) == .connecting)
    }

    @Test(arguments: [AggregateState.unavailable, .idle, .connected, .connecting, .needsCredentials, .problem])
    @MainActor
    func hasAnIconAndALabel(state: AggregateState) {
        #expect(NSImage(systemSymbolName: state.symbolName, accessibilityDescription: nil) != nil, "\(state.symbolName) is not an SF Symbol")
        #expect(!state.label.isEmpty)
    }
}

struct MenuModelTests {
    private let profiles = [
        makeProfile("a", name: "Router", state: .connected, desired: true),
        makeProfile("b", name: "Home", state: .disconnected),
        makeProfile("c", name: "Office", state: .failed, desired: true),
        makeProfile("d", name: "Lab", state: .disconnecting, desired: false),
        makeProfile("e", name: "Cafe", state: .awaitingCredentials, desired: true),
        makeProfile("f", name: "Depot", state: .reconnecting, desired: true),
    ]

    @Test func listsTheProfilesInOrderWithTheirState() {
        let menu = MenuModel(setup: .ready, profiles: profiles)
        #expect(menu.status == nil)
        #expect(menu.canImport)
        #expect(menu.items.map(\.title) == ["Router", "Home", "Office", "Lab", "Cafe", "Depot"])
        #expect(menu.items.map(\.mark) == [.on, .off, .off, .mixed, .mixed, .mixed])
        #expect(menu.items.map(\.subtitle) == [ProfileState.connected, .disconnected, .failed, .disconnecting, .awaitingCredentials, .reconnecting].map { "\(ProfileKind.unspecified.label) · \($0.label)" })
        #expect(menu.items.map(\.isEnabled) == [true, true, true, false, true, true])
        #expect(menu.items.map(\.profileID) == ["a", "b", "c", "d", "e", "f"])
    }

    @Test func aRowDoesWhatItsProfileNeeds() {
        let menu = MenuModel(setup: .ready, profiles: profiles)
        // Connected and under way: choosing it switches the profile off. A failed one is switched
        // on already, so choosing it tries again; one that waits for a password opens the sheet.
        #expect(menu.items.map(\.action) == [.disconnect, .connect, .retry, .disconnect, .answerCredentials, .disconnect])
    }

    @Test func summarisesHowManyAreConnected() {
        let connected = MenuModel(setup: .ready, profiles: [makeProfile("a", state: .connected), makeProfile("b", state: .connected), makeProfile("c", state: .disconnected)])
        #expect(connected.summary == String(localized: "Connected: \(2)", bundle: .module))
        // A profile that is still connecting does not take the count away from the ones that are not.
        let mixed = MenuModel(setup: .ready, profiles: [makeProfile("a", state: .connected), makeProfile("b", state: .connecting)])
        #expect(mixed.summary == String(localized: "Connected: \(1)", bundle: .module))
        #expect(MenuModel(setup: .ready, profiles: [makeProfile("a", state: .disconnected)]).summary == AggregateState.idle.label)
        #expect(MenuModel(setup: .ready, profiles: [makeProfile("a", state: .failed)]).summary == AggregateState.problem.label)
        #expect(MenuModel(setup: .ready, profiles: []).summary == nil)
    }

    @Test func offersDisconnectAllOnlyWhileSomethingIsSwitchedOn() {
        #expect(MenuModel(setup: .ready, profiles: profiles).canDisconnectAll)
        #expect(!MenuModel(setup: .ready, profiles: [makeProfile("a", state: .disconnected)]).canDisconnectAll)
        #expect(!MenuModel(setup: .notResponding, profiles: profiles).canDisconnectAll)
    }

    @Test func anOutOfDateHelperStillWorksAndSaysSo() {
        let menu = MenuModel(setup: .versionMismatch(daemon: "0.0.9", app: "0.1.0"), profiles: profiles)
        #expect(menu.status == DaemonSetup.versionMismatch(daemon: "", app: "").menuStatus)
        #expect(menu.items.count == profiles.count)
        #expect(menu.canImport)
    }

    @Test(arguments: [DaemonSetup.needsInstall, .misplaced, .needsApproval, .missingFromBundle, .notResponding, .development, .connecting])
    func disablesEverythingWhenTheHelperDoesNotAnswer(setup: DaemonSetup) {
        let menu = MenuModel(setup: setup, profiles: profiles)
        #expect(menu.status == setup.menuStatus)
        #expect(menu.status?.isEmpty == false)
        #expect(menu.items.isEmpty)
        #expect(!menu.canImport)
    }
}

struct PresentationTests {
    @Test func movesProfilesLikeAListDrag() {
        let ids = ["a", "b", "c", "d"]
        #expect(ProfileOrder.moving(ids, fromOffsets: [3], toOffset: 0) == ["d", "a", "b", "c"])
        #expect(ProfileOrder.moving(ids, fromOffsets: [0], toOffset: 4) == ["b", "c", "d", "a"])
        #expect(ProfileOrder.moving(ids, fromOffsets: [1], toOffset: 3) == ["a", "c", "b", "d"])
        #expect(ProfileOrder.moving(ids, fromOffsets: [0, 1], toOffset: 4) == ["c", "d", "a", "b"])
    }

    @Test func movesOneProfileByOnePlace() {
        let ids = ["a", "b", "c"]
        #expect(ProfileOrder.moving(ids, id: "b", by: -1) == ["b", "a", "c"])
        #expect(ProfileOrder.moving(ids, id: "b", by: 1) == ["a", "c", "b"])
        #expect(ProfileOrder.moving(ids, id: "a", by: -1) == nil)
        #expect(ProfileOrder.moving(ids, id: "c", by: 1) == nil)
        #expect(ProfileOrder.moving(ids, id: "x", by: 1) == nil)
    }

    @Test func routeRowsShowStatesAndNameTheProfileThatShadows() {
        var profile = makeProfile(name: "Lab", state: .connected)
        var installed = RouteStatus(); installed.prefix = "192.168.1.0/24"; installed.state = .installed
        var shadowed = RouteStatus(); shadowed.prefix = "10.99.0.0/16"; shadowed.state = .shadowed; shadowed.shadowedBy = "other"; shadowed.detail = "held by a profile with higher priority"
        var blocked = RouteStatus(); blocked.prefix = "192.168.0.0/24"; blocked.state = .blocked
        profile.status.routes = [installed, shadowed, blocked]

        let rows = RouteRow.rows(for: profile) { $0 == "other" ? "Office" : nil }
        #expect(rows.map(\.prefix) == ["192.168.1.0/24", "10.99.0.0/16", "192.168.0.0/24"])
        #expect(rows.map(\.state) == [.installed, .shadowed, .blocked])
        #expect(rows[0].stateLabel == RouteState.installed.label())
        #expect(rows[1].stateLabel == RouteState.shadowed.label(shadowedBy: "Office"))
        #expect(rows[1].detail == "held by a profile with higher priority")
        #expect(rows[2].stateLabel == RouteState.blocked.label())

        // A profile that is gone is still named, by its id.
        #expect(RouteRow.rows(for: profile) { _ in nil }[1].stateLabel == RouteState.shadowed.label(shadowedBy: "other"))
    }

    @Test func aDisconnectedProfileShowsTheRoutesItNames() {
        var profile = makeProfile()
        profile.summary.routes = ["192.168.1.0/24"]
        let rows = RouteRow.rows(for: profile) { _ in nil }
        #expect(rows.map(\.prefix) == ["192.168.1.0/24"])
        #expect(rows[0].state == nil)
        #expect(rows[0].stateLabel == nil)
    }

    @Test func dnsRowsTurnTheDotIntoEveryDomain() {
        var profile = makeProfile(state: .connected)
        var everything = DnsStatus(); everything.servers = ["10.8.0.1", "10.8.0.2"]; everything.matchDomains = ["."]; everything.state = .installed
        var corporate = DnsStatus(); corporate.servers = ["10.9.0.1"]; corporate.matchDomains = ["corp.example", "lan"]; corporate.state = .failed; corporate.detail = "refused"
        profile.status.dns = [everything, corporate]

        let rows = DNSRow.rows(for: profile)
        #expect(rows.map(\.servers) == ["10.8.0.1, 10.8.0.2", "10.9.0.1"])
        #expect(rows[0].domains == String(localized: "All domains", bundle: .module))
        #expect(rows[1].domains == "corp.example, lan")
        #expect(rows[1].state == .failed)
        #expect(rows[1].detail == "refused")
    }

    @Test func everyStateHasALabel() {
        for state in [ProfileState.connected, .connecting, .disconnecting, .disconnected, .failed, .reconnecting, .awaitingCredentials, .unspecified] {
            #expect(!state.label.isEmpty)
        }
        // Product names are not translated.
        #expect(ProfileKind.openvpn.label == "OpenVPN")
        #expect(ProfileKind.wireguard.label == "WireGuard")
        #expect(TunnelMode.choices == [.auto, .full, .split])
    }

    @Test func formatsByteCountsAndEndpoints() {
        #expect(!Formatting.bytes(0).isEmpty)
        // Decimal units, as Finder counts them.
        let megabytes = Formatting.bytes(1_500_000)
        #expect(megabytes.contains("1.5") || megabytes.contains("1,5"))
        #expect(megabytes.contains("MB"))
        // A counter the daemon reports above Int64 does not trap.
        #expect(!Formatting.bytes(UInt64.max).isEmpty)
        #expect(Formatting.endpoint(host: "vpn.example.net", port: 1194, protocol: "tcp") == "vpn.example.net:1194 (tcp)")
        #expect(Formatting.endpoint(host: "vpn.example.net", port: 0, protocol: "") == "vpn.example.net")
    }

    @Test func logLinesAreCopiedAsPlainText() {
        let line = LogTail.Entry(id: 1, date: nil, level: .warn, text: "something odd")
        #expect(line.plainText == "WARN something odd")
        let timed = LogTail.Entry(id: 2, date: Date(timeIntervalSince1970: 0), level: .info, text: "up")
        // A time of day of the same width in every language: HH:mm:ss.
        #expect(timed.plainText.count == "00:00:00 INFO up".count)
        #expect(timed.plainText.hasSuffix(" INFO up"))
        #expect(Formatting.logTime(Date(timeIntervalSince1970: 0)).count == 8)
    }
}

@MainActor
struct TrafficHistoryTests {
    private func connected(_ id: String = "a", received: UInt64, sent: UInt64) -> Profile {
        var profile = makeProfile(id, state: .connected)
        profile.status.rxBytes = received
        profile.status.txBytes = sent
        return profile
    }

    private let start = Date(timeIntervalSince1970: 1_000)

    @Test func worksOutTheRatesFromTheCounters() {
        let history = TrafficHistory()
        history.record([connected(received: 1_000, sent: 100)], at: start)
        #expect(history.rate(for: "a") == nil, "one reading says nothing about speed")
        history.record([connected(received: 5_000, sent: 300)], at: start + 2)
        let rate = history.rate(for: "a")
        #expect(rate?.received == 2_000)
        #expect(rate?.sent == 100)
        #expect(history.samples["a"]?.count == 1)
    }

    @Test func showsTheMeanOfTheLastReadings() {
        let history = TrafficHistory()
        // Counters every two seconds: 1000, 1000, 4000 and 4000 bytes per second.
        for (index, counter) in [UInt64(0), 2_000, 4_000, 12_000, 20_000].enumerated() {
            history.record([connected(received: counter, sent: 0)], at: start + Double(index) * 2)
        }
        #expect(history.samples["a"]?.map(\.received) == [1_000, 1_000, 4_000, 4_000])
        // The last three samples only.
        #expect(history.rate(for: "a")?.received == 3_000)
    }

    @Test func ignoresASecondReadingForTheSameInterval() {
        let history = TrafficHistory()
        history.record([connected(received: 0, sent: 0)], at: start)
        history.record([connected(received: 10, sent: 0)], at: start + 0.2)
        #expect(history.samples["a"] == nil)
        // The interval is measured from the first reading, not from the one that was ignored.
        history.record([connected(received: 2_000, sent: 0)], at: start + 2)
        #expect(history.rate(for: "a")?.received == 1_000)
    }

    @Test func startsAgainWhenACounterGoesDown() {
        let history = TrafficHistory()
        history.record([connected(received: 1_000, sent: 0)], at: start)
        history.record([connected(received: 3_000, sent: 0)], at: start + 2)
        #expect(history.samples["a"]?.count == 1)
        history.record([connected(received: 100, sent: 0)], at: start + 4)
        #expect(history.samples["a"] == nil, "a reset says nothing about speed, and never a negative one")
        history.record([connected(received: 2_100, sent: 0)], at: start + 6)
        #expect(history.rate(for: "a")?.received == 1_000)
    }

    @Test func forgetsAProfileThatIsNotConnected() {
        let history = TrafficHistory()
        history.record([connected(received: 0, sent: 0)], at: start)
        history.record([connected(received: 2_000, sent: 0)], at: start + 2)
        history.record([makeProfile("a", state: .reconnecting)], at: start + 4)
        #expect(history.samples["a"] == nil)
        history.record([connected(received: 0, sent: 0)], at: start + 6)
        #expect(history.rate(for: "a") == nil, "a new connection has its own history")
    }

    @Test func keepsTwoMinutes() {
        let history = TrafficHistory()
        for step in 0...(TrafficHistory.capacity + 20) {
            history.record([connected(received: UInt64(step) * 1_000, sent: 0)], at: start + Double(step) * 2)
        }
        #expect(history.samples["a"]?.count == TrafficHistory.capacity)
    }
}

struct StatusPresentationTests {
    private static let profileStates: [ProfileState] = [.disconnected, .connecting, .connected, .disconnecting, .failed, .reconnecting, .awaitingCredentials]
    private static let routeStates: [RouteState] = [.pending, .installed, .shadowed, .blocked, .failed]

    @Test(arguments: profileStates)
    @MainActor
    func everyProfileStateHasASymbolThatExists(state: ProfileState) {
        #expect(NSImage(systemSymbolName: state.symbolName, accessibilityDescription: nil) != nil, "\(state.symbolName)")
    }

    @Test func noTwoProfileStatesShareAShape() {
        // Colour is the third channel: the glyph and the word already tell the states apart.
        let symbols = Self.profileStates.map(\.symbolName)
        #expect(Set(symbols).count == symbols.count)
    }

    @Test func theSidebarUsesTheShieldsOfTheMenuBarItem() {
        // A profile is the shield the menu bar item draws for the same state.
        #expect(ProfileState.connected.symbolName == AggregateState.connected.symbolName)
        #expect(ProfileState.disconnected.symbolName == AggregateState.idle.symbolName)
        #expect(ProfileState.connecting.symbolName == AggregateState.connecting.symbolName)
        #expect(ProfileState.awaitingCredentials.symbolName == AggregateState.needsCredentials.symbolName)
    }

    @Test(arguments: routeStates)
    @MainActor
    func everyRouteStateHasASymbolThatExists(state: RouteState) {
        #expect(NSImage(systemSymbolName: state.symbolName, accessibilityDescription: nil) != nil, "\(state.symbolName)")
    }

    @Test func aStandbyRouteIsNotPaintedAsAWarning() {
        #expect(RouteState.shadowed.tint == .secondary)
        #expect(RouteState.blocked.tint != RouteState.shadowed.tint)
        #expect(RouteState.blocked.isLost && RouteState.failed.isLost)
        #expect(!RouteState.shadowed.isLost && !RouteState.installed.isLost && !RouteState.pending.isLost)
    }

    @Test func onlyAnEndingStateMoves() {
        #expect(Self.profileStates.filter(\.isTransitional) == [.connecting, .disconnecting, .reconnecting])
    }

    @Test func everyProfileSectionHasAName() {
        #expect(ProfileSection.allCases.count == 5)
        #expect(ProfileSection.allCases.allSatisfy { !$0.label.isEmpty })
        #expect(Set(ProfileSection.allCases.map(\.label)).count == ProfileSection.allCases.count)
    }

    @Test func formatsARateAndLeavesTheLogTimeTheSameWidth() {
        #expect(Formatting.rate(1_500_000).contains("MB"))
        #expect(Formatting.rate(0).contains("/"), "a rate says per what")
        let times = [0, 3_600 * 13 + 61, 86_399].map { Formatting.logTime(Date(timeIntervalSince1970: TimeInterval($0))) }
        #expect(times.allSatisfy { $0.count == 8 })
    }
}

struct DiagnosticsReportTests {
    @Test func listsWhatABugReportNeedsAndNoLogLines() {
        var diagnostics = Diagnostics()
        diagnostics.network.defaultGatewayV4 = "192.168.1.1"
        diagnostics.network.defaultInterfaceV4 = "en0"
        diagnostics.network.interfaces = ["lo0", "en0", "utun4"]
        var owned = OwnedRoute()
        owned.prefix = "10.20.0.0/16"
        owned.owner = "p1"
        owned.via = "utun4"
        owned.state = .installed
        owned.kind = .tunnel
        diagnostics.ownedRoutes = [owned]
        var stale = StaleRoute()
        stale.prefix = "203.0.113.9/32"
        stale.gateway = "192.168.0.254"
        stale.interface = "en0"
        stale.reason = "the gateway is not on the interface's subnet"
        stale.owned = true
        diagnostics.staleRoutes = [stale]
        diagnostics.resolverEntries = ["State:/Network/Service/plaitway-p1/DNS"]

        let report = DiagnosticsReport.text(diagnostics, appVersion: "0.2.0", daemon: nil, profileName: { $0 == "p1" ? "Office" : nil })

        #expect(report.hasPrefix("Plaitway 0.2.0"))
        #expect(report.contains("gateway IPv4: 192.168.1.1 en0"))
        #expect(report.contains("10.20.0.0/16"))
        #expect(report.contains("(Office)"), "the profile is named, not its id")
        #expect(report.contains("203.0.113.9/32 via 192.168.0.254 en0: the gateway is not on the interface's subnet (installed by Plaitway)"))
        #expect(report.contains("plaitway-p1/DNS"))
    }
}
