import Foundation
import Observation
import os
import PlaitwayClient
import SwiftUI

enum SidebarItem: Hashable {
    case profile(String)
    case diagnostics
}

/// Something a command did not manage, for an alert.
struct AppAlert: Identifiable {
    enum Title {
        case importFailed, deleteFailed, reorderFailed, saveFailed, connectFailed, helperFailed, loginItemFailed, resyncFailed, removeFailed

        var text: String {
            switch self {
            case .importFailed: String(localized: "Import failed", bundle: .module)
            case .deleteFailed: String(localized: "Delete failed", bundle: .module)
            case .reorderFailed: String(localized: "Reorder failed", bundle: .module)
            case .saveFailed: String(localized: "Save failed", bundle: .module)
            case .connectFailed: String(localized: "Connect failed", bundle: .module)
            case .helperFailed: String(localized: "Helper failed", bundle: .module)
            case .loginItemFailed: String(localized: "Launch at Login failed", bundle: .module)
            case .resyncFailed: String(localized: "Resync failed", bundle: .module)
            case .removeFailed: String(localized: "Remove failed", bundle: .module)
            }
        }
    }

    let id = UUID()
    let title: String
    let message: String
}

/// What came of importing the files the user picked or dropped.
struct ImportReport: Identifiable {
    struct Outcome: Identifiable {
        enum Result {
            case imported(name: String, warnings: [ImportWarning])
            case failed(message: String)
        }

        let id = UUID()
        let filename: String
        let result: Result
    }

    let id = UUID()
    let outcomes: [Outcome]

    /// Worth a sheet: a file failed or the daemon changed something in it.
    var needsAttention: Bool {
        outcomes.contains {
            switch $0.result {
            case .imported(_, let warnings): !warnings.isEmpty
            case .failed: true
            }
        }
    }
}

enum ProfileOrder {
    static func moving(_ ids: [String], fromOffsets: IndexSet, toOffset: Int) -> [String] {
        var ids = ids
        ids.move(fromOffsets: fromOffsets, toOffset: toOffset)
        return ids
    }

    /// `delta` -1 moves the profile up one place, +1 down; nil at either end.
    static func moving(_ ids: [String], id: String, by delta: Int) -> [String]? {
        guard let index = ids.firstIndex(of: id), ids.indices.contains(index + delta) else { return nil }
        var ids = ids
        ids.swapAt(index, index + delta)
        return ids
    }
}

/// The app's state beyond the profiles: which page is open, what is being
/// asked or reported, and the helper. Every command goes to the daemon and
/// comes back through the store.
@MainActor
@Observable
final class AppModel {
    let store: ProfileStore
    let traffic = TrafficHistory()
    let installer: DaemonInstaller
    let loginItem: LoginItem
    let appVersion: String?
    let isOverridden: Bool

    var selection: SidebarItem?
    /// The page of a profile that is open; it stays when another profile is selected.
    var profileSection: ProfileSection = .overview
    /// The menu bar item is the user's to hide: here, in System Settings and by dragging it out.
    var showsMenuBarItem: Bool {
        didSet { UserDefaults.standard.set(showsMenuBarItem, forKey: Self.showsMenuBarItemKey) }
    }
    var importReport: ImportReport?
    var alert: AppAlert?
    /// The profile whose deletion waits for the user's confirmation.
    var pendingDeletion: String?
    var isConfirmingUninstall = false
    var isConfirmingReinstall = false
    private(set) var isSettling = false

    @ObservationIgnored private var editors: [String: ProfileEditor] = [:]
    @ObservationIgnored private var settling: Task<Void, Never>?
    @ObservationIgnored private var polling: Task<Void, Never>?
    private static let showsMenuBarItemKey = "showsMenuBarItem"
    private static let settleDuration: Duration = .seconds(8)
    private static let pollInterval: Duration = .seconds(2)
    private static let logger = Logger(subsystem: "Plaitway", category: "AppModel")

    init(
        store: ProfileStore,
        installer: DaemonInstaller = DaemonInstaller(),
        loginItem: LoginItem = LoginItem(),
        appVersion: String? = Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String,
        isOverridden: Bool = DaemonLocation.override() != nil
    ) {
        self.store = store
        self.installer = installer
        self.loginItem = loginItem
        self.appVersion = appVersion
        self.isOverridden = isOverridden
        showsMenuBarItem = UserDefaults.standard.object(forKey: Self.showsMenuBarItemKey) as? Bool ?? true
    }

    var setup: DaemonSetup {
        DaemonSetup(
            connection: store.connection,
            registration: installer.registration,
            daemonVersion: store.daemonInfo?.version,
            appVersion: appVersion,
            isOverridden: isOverridden,
            isSettling: isSettling
        )
    }

    /// The helper stopped answering after it had shown its profiles: they stay in the window
    /// (with a note) instead of giving way to a setup page, which a restart would flash.
    var keepsProfilesInView: Bool {
        switch setup {
        case .notResponding, .connecting: !store.profiles.isEmpty
        default: false
        }
    }

    /// A daemon answers although this app did not register the helper:
    /// `scripts/dev-install-daemon.sh` installed it. Registering it here would add
    /// a second job with the same label, so the app leaves it alone.
    var isHelperExternal: Bool {
        guard !isOverridden, store.connection == .connected else { return false }
        return installer.registration == .notRegistered || installer.registration == .misplaced
    }

    /// The profiles the helper keeps switched on, whether or not the app runs; none
    /// while the helper does not answer, because what the profiles show is then stale.
    var switchedOnProfiles: [Profile] {
        setup.isUsable ? store.profiles.filter(\.desiredEnabled) : []
    }

    func start() {
        store.start()
        trackTraffic()
        polling = Task { await pollHelperStatus() }
    }

    /// Samples the byte counters at every change of the profiles, whatever page is open, so
    /// that the graph of a profile has its history when the page is opened.
    private func trackTraffic() {
        let profiles = withObservationTracking {
            store.profiles
        } onChange: { [weak self] in
            Task { @MainActor in self?.trackTraffic() }
        }
        traffic.record(profiles)
    }

    func stop() async {
        polling?.cancel()
        settling?.cancel()
        await store.stop()
    }

    func profileName(_ id: String) -> String? {
        store.profiles.first { $0.id == id }?.name
    }

    /// The editor of a profile's text; made when first asked for and kept until the profile is gone.
    func editor(for profile: Profile) -> ProfileEditor {
        if let editor = editors[profile.id] { return editor }
        let editor = ProfileEditor(profileID: profile.id, kind: profile.kind)
        editors[profile.id] = editor
        return editor
    }

    var selectedProfile: Profile? {
        guard case .profile(let id) = selection else { return nil }
        return store.profiles.first { $0.id == id }
    }

    // MARK: Helper

    /// What an alert about a helper that could not be registered points to.
    private static var installFallback: String {
        String(localized: "Fallback: sudo scripts/dev-install-daemon.sh", bundle: .module)
    }

    func installHelper() {
        do {
            try installer.install()
            beginSettling()
        } catch {
            report(error, as: .helperFailed, hint: Self.installFallback)
        }
    }

    /// Unregisters and registers the helper again, which drops every tunnel; the
    /// views ask first (`isConfirmingReinstall`).
    func reinstallHelper() async {
        guard !isHelperExternal else { return }
        do {
            try await installer.reinstall()
            beginSettling()
        } catch {
            report(error, as: .helperFailed, hint: Self.installFallback)
        }
    }

    func uninstallHelper() async {
        do {
            try await installer.uninstall()
        } catch {
            report(error, as: .helperFailed)
        }
    }

    /// Looks at the helper again; the daemon may have come up since.
    func retry() {
        installer.refresh()
        beginSettling()
    }

    func perform(_ action: SetupContent.Action) async {
        switch action {
        case .installHelper: installHelper()
        case .openSystemSettings: installer.openSystemSettings()
        case .retry: retry()
        case .reinstallHelper: isConfirmingReinstall = true
        }
    }

    func setLaunchAtLogin(_ enabled: Bool) {
        do {
            try loginItem.setEnabled(enabled)
        } catch {
            report(error, as: .loginItemFailed)
        }
    }

    private func beginSettling() {
        isSettling = true
        settling?.cancel()
        settling = Task {
            try? await Task.sleep(for: Self.settleDuration)
            if !Task.isCancelled { isSettling = false }
        }
    }

    /// The user approves the helper in System Settings and comes back; nothing
    /// tells the app, so it looks.
    private func pollHelperStatus() async {
        while !Task.isCancelled {
            try? await Task.sleep(for: Self.pollInterval)
            guard store.connection != .connected else { continue }
            let before = installer.registration
            installer.refresh()
            if before != .enabled && installer.registration == .enabled { beginSettling() }
        }
    }

    // MARK: Profiles

    func setEnabled(_ enabled: Bool, profileID: String) {
        Task {
            do {
                try await store.setEnabled(enabled, profileID: profileID)
            } catch {
                report(error, as: .connectFailed)
            }
        }
    }

    /// Switches off every profile that is on. Returns whether all of them are off;
    /// a profile that stays on is reported.
    func disconnectAll() async -> Bool {
        var allOff = true
        for profile in switchedOnProfiles {
            do {
                try await store.setEnabled(false, profileID: profile.id)
            } catch {
                report(error, as: .connectFailed)
                allOff = false
            }
        }
        return allOff
    }

    /// Imports the files in order and selects the last profile that was stored.
    func importFiles(_ urls: [URL]) async {
        var outcomes: [ImportReport.Outcome] = []
        var lastImported: String?
        for url in urls {
            let filename = url.lastPathComponent
            do {
                let loaded = try ProfileImporter.load(from: url)
                let result = try await store.importProfile(
                    content: loaded.content, sourceFilename: filename, credentials: loaded.credentials
                )
                lastImported = result.profile.id
                outcomes.append(.init(filename: filename, result: .imported(name: result.profile.name, warnings: result.warnings)))
            } catch {
                Self.logger.error("import of \(filename) failed: \(error)")
                outcomes.append(.init(filename: filename, result: .failed(message: userMessage(for: error))))
            }
        }

        let report = ImportReport(outcomes: outcomes)
        if report.needsAttention { importReport = report }
        if let lastImported { await select(profile: lastImported) }
    }

    func requestDeletion(of profileID: String) {
        pendingDeletion = profileID
    }

    func delete(profileID: String) async {
        do {
            try await store.deleteProfile(id: profileID)
        } catch {
            report(error, as: .deleteFailed)
        }
    }

    func move(fromOffsets: IndexSet, toOffset: Int) async {
        await reorder(ProfileOrder.moving(store.profiles.map(\.id), fromOffsets: fromOffsets, toOffset: toOffset))
    }

    func move(profileID: String, by delta: Int) async {
        guard let ids = ProfileOrder.moving(store.profiles.map(\.id), id: profileID, by: delta) else { return }
        await reorder(ids)
    }

    private func reorder(_ ids: [String]) async {
        do {
            try await store.reorder(ids: ids)
        } catch {
            report(error, as: .reorderFailed)
        }
    }

    /// Returns whether the profile has the name, or is about to; when it does not,
    /// the text the user typed is no longer worth showing.
    @discardableResult
    func rename(profileID: String, to name: String) async -> Bool {
        let name = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let profile = store.profiles.first(where: { $0.id == profileID }), !name.isEmpty else { return false }
        guard name != profile.name else { return true }
        do {
            try await store.updateProfile(id: profileID, name: name)
            return true
        } catch {
            report(error, as: .saveFailed)
            return false
        }
    }

    /// Changes one setting; the others stay as the daemon reports them.
    func changeSettings(profileID: String, _ change: (inout ProfileSettings) -> Void) async {
        guard var settings = store.profiles.first(where: { $0.id == profileID })?.settings else { return }
        change(&settings)
        do {
            try await store.updateProfile(id: profileID, settings: settings)
        } catch {
            report(error, as: .saveFailed)
        }
    }

    // MARK: Diagnostics

    func resync() async {
        do {
            try await store.resync()
        } catch {
            report(error, as: .resyncFailed)
        }
    }

    func removeStaleRoute(key: String) async {
        do {
            try await store.removeStaleRoute(key: key)
        } catch where DaemonFailure(error) == .notFound {
            // Already gone: a refresh or a resync removed it while the dialog was open.
            Self.logger.info("the stale route \(key) was already gone")
        } catch {
            report(error, as: .removeFailed)
        }
    }

    // MARK: Credentials

    func submitCredentials(profileID: String, username: String, password: String) async {
        do {
            try await store.provideCredentials(profileID: profileID, username: username, password: password)
        } catch {
            report(error, as: .connectFailed)
        }
    }

    func cancelCredentials(profileID: String) async {
        do {
            try await store.cancelCredentials(profileID: profileID)
        } catch {
            report(error, as: .connectFailed)
        }
    }

    // MARK: Selection

    /// Keeps something selected while there is something to select: after a
    /// deletion the first profile, and the first profile at launch.
    func reconcileSelection() {
        let existing = Set(store.profiles.map(\.id))
        editors = editors.filter { existing.contains($0.key) }
        switch selection {
        case .diagnostics:
            return
        case .profile(let id) where store.profiles.contains(where: { $0.id == id }):
            return
        case .profile, nil:
            selection = store.profiles.first.map { .profile($0.id) }
        }
    }

    /// Selects a profile once the store shows it; the daemon's event may arrive after the reply.
    private func select(profile id: String) async {
        let deadline = ContinuousClock.now + .seconds(2)
        while !store.profiles.contains(where: { $0.id == id }), ContinuousClock.now < deadline {
            try? await Task.sleep(for: .milliseconds(20))
        }
        if store.profiles.contains(where: { $0.id == id }) { selection = .profile(id) }
    }

    func report(_ error: any Error, as title: AppAlert.Title, hint: String? = nil) {
        Self.logger.error("\(title.text): \(error)")
        alert = AppAlert(title: title.text, message: [userMessage(for: error), hint].compactMap { $0 }.joined(separator: "\n"))
    }
}
