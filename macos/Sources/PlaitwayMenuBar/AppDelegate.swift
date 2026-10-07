import AppKit
import Observation
import PlaitwayClient
import SwiftUI

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate, NSMenuItemValidation {
    private let model: AppModel
    private let windows: WindowController
    private let statusItem: NSStatusItem
    private let menu = NSMenu()
    private var visibilityObservation: NSKeyValueObservation?
    /// What the icon and the menu show now. A traffic counter changes the profiles every
    /// second or two; redrawing only when these change keeps an open menu steady.
    private var shownAggregate: AggregateState?
    private var shownMenu: MenuModel?
    /// Set once the helper's state is known and the window had its chance to open.
    private var hasOfferedInstallation = false
    private var isPoweringOff = false

    override init() {
        do {
            // A daemon behind PLAITWAY_SOCKET (debug builds) is not the helper: the
            // Keychain is neither read to answer it nor cleaned up on its word.
            let credentials: any CredentialStore = DaemonLocation.override() == nil ? KeychainCredentialStore() : InMemoryCredentialStore()
            let store = try ProfileStore(socketPath: DaemonLocation.socketPath(), credentialStore: credentials)
            model = AppModel(store: store)
        } catch {
            // Creating a transport for a Unix socket target does not fail at runtime.
            fatalError("cannot create the daemon client: \(error)")
        }
        windows = WindowController(model: model)
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
        // Set at once and never changed: the system remembers where the person put the item by it.
        statusItem.autosaveName = "app.plaitway.status"
        super.init()
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        // One window per page: the View menu would otherwise offer tabs for them.
        NSWindow.allowsAutomaticWindowTabbing = false
        NSApp.mainMenu = MainMenu.make(target: self)
        // Otherwise AppKit re-enables every item that has a target and action,
        // overriding isEnabled = false while the daemon is unavailable.
        menu.autoenablesItems = false
        statusItem.menu = menu
        // The item can be dragged out of the menu bar with Command, or switched off in System
        // Settings; the setting in this app follows what the person did.
        statusItem.behavior = .removalAllowed
        visibilityObservation = statusItem.observe(\.isVisible) { [weak self] item, _ in
            let isVisible = item.isVisible
            Task { @MainActor in
                if let self, !isVisible, self.model.showsMenuBarItem { self.model.showsMenuBarItem = false }
            }
        }
        // Logging out or shutting down must not wait for an answer to the quit prompt.
        NSWorkspace.shared.notificationCenter.addObserver(forName: NSWorkspace.willPowerOffNotification, object: nil, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.isPoweringOff = true }
        }
        model.start()
        render()
    }

    /// The helper keeps its profiles connected when the app is gone, which is not what
    /// everyone expects of Quit: with profiles switched on, the user chooses.
    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        guard !isPoweringOff, !model.switchedOnProfiles.isEmpty else { return .terminateNow }
        switch QuitPrompt.ask() {
        case .quit:
            return .terminateNow
        case .cancel:
            return .terminateCancel
        case .disconnectAndQuit:
            Task {
                let allOff = await model.disconnectAll()
                // A profile that stays on is reported in the window.
                if !allOff { windows.show() }
                NSApp.reply(toApplicationShouldTerminate: allOff)
            }
            return .terminateLater
        }
    }

    /// Opening the app again, from Finder or the Dock, shows the window.
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        windows.show()
        return true
    }

    /// Profile files opened from Finder: imported like the ones the user picks.
    func application(_ application: NSApplication, open urls: [URL]) {
        windows.show()
        Task { await model.importFiles(urls) }
    }

    /// The Dock icon is there while a window is open; its menu is the profiles, as in the menu bar.
    func applicationDockMenu(_ sender: NSApplication) -> NSMenu? {
        let dock = NSMenu()
        populate(dock, with: MenuModel(setup: model.setup, profiles: model.store.profiles), includesWindowCommands: false)
        return dock
    }

    // MARK: Rendering

    /// Redraws from the model and re-arms itself: Observation reports one change
    /// per registration, so each change registers the next.
    private func render() {
        var needsWindow = false
        var setup = DaemonSetup.connecting
        withObservationTracking {
            setup = model.setup
            let aggregate = AggregateState(setup: setup, profiles: model.store.profiles)
            if aggregate != shownAggregate {
                shownAggregate = aggregate
                updateIcon(aggregate)
            }
            let content = MenuModel(setup: setup, profiles: model.store.profiles)
            if content != shownMenu {
                shownMenu = content
                menu.removeAllItems()
                populate(menu, with: content, includesWindowCommands: true)
            }
            if statusItem.isVisible != model.showsMenuBarItem { statusItem.isVisible = model.showsMenuBarItem }
            needsWindow = !model.store.credentialPrompts.isEmpty
        } onChange: { [weak self] in
            Task { @MainActor in self?.render() }
        }
        // A profile asks for credentials while no window is open: the sheet needs one.
        if needsWindow && !windows.isVisible { windows.show() }

        // First run: the window offers the install. The helper may still be answering
        // (installed by the script, or started a moment ago), so it waits for the connection.
        if !hasOfferedInstallation && setup != .connecting {
            hasOfferedInstallation = true
            if setup.offersInstallation { windows.show() }
        }
    }

    private func updateIcon(_ aggregate: AggregateState) {
        guard let button = statusItem.button else { return }
        button.image = NSImage(systemSymbolName: aggregate.symbolName, accessibilityDescription: aggregate.label)
        button.appearsDisabled = aggregate.appearsDisabled
        button.setAccessibilityLabel("Plaitway")
        button.setAccessibilityValue(aggregate.label)
        button.toolTip = aggregate.label
    }

    /// The menu is text only: macOS 27 hides the images of menu items unless each asks to keep
    /// them, and a state read from an icon is lost on the way. Each profile is a row with its
    /// state as a second line and a mark.
    private func populate(_ menu: NSMenu, with content: MenuModel, includesWindowCommands: Bool) {
        if let status = content.status {
            let item = NSMenuItem(title: status, action: nil, keyEquivalent: "")
            item.isEnabled = false
            menu.addItem(item)
        }
        if let summary = content.summary {
            menu.addItem(NSMenuItem.sectionHeader(title: summary))
        }
        for profile in content.items {
            let item = NSMenuItem(title: profile.title, action: #selector(profileChosen(_:)), keyEquivalent: "")
            item.target = self
            item.representedObject = profile.profileID
            item.subtitle = profile.subtitle
            item.state = switch profile.mark {
            case .off: .off
            case .on: .on
            case .mixed: .mixed
            }
            item.isEnabled = profile.isEnabled
            menu.addItem(item)
        }
        if content.canDisconnectAll {
            menu.addItem(.separator())
            let item = NSMenuItem(title: String(localized: "Disconnect All", bundle: .module), action: #selector(disconnectAll), keyEquivalent: "")
            item.target = self
            menu.addItem(item)
        }
        guard includesWindowCommands else { return }

        if !menu.items.isEmpty { menu.addItem(.separator()) }
        menu.addItem(commandItem(String(localized: "Open Plaitway", bundle: .module), #selector(openWindow)))
        let importItem = commandItem(String(localized: "Import Profile…", bundle: .module), #selector(importProfile), key: "i")
        importItem.isEnabled = content.canImport
        menu.addItem(importItem)
        menu.addItem(commandItem(String(localized: "Settings…", bundle: .module), #selector(openSettings), key: ","))
        menu.addItem(.separator())
        menu.addItem(NSMenuItem(title: String(localized: "Quit Plaitway", bundle: .module), action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q"))
    }

    private func commandItem(_ title: String, _ action: Selector, key: String = "") -> NSMenuItem {
        let item = NSMenuItem(title: title, action: action, keyEquivalent: key)
        item.target = self
        return item
    }

    // MARK: Actions

    @objc private func profileChosen(_ sender: NSMenuItem) {
        guard let id = sender.representedObject as? String,
              let profile = model.store.profiles.first(where: { $0.id == id }) else { return }
        switch MenuModel.item(for: profile).action {
        case .connect, .retry: model.setEnabled(true, profileID: id)
        case .disconnect: model.setEnabled(false, profileID: id)
        case .answerCredentials: windows.show()
        }
    }

    @objc func disconnectAll() {
        Task { _ = await model.disconnectAll() }
    }

    @objc private func openWindow() {
        windows.show()
    }

    @objc func openSettings() {
        windows.showSettings()
    }

    @objc func importProfile() {
        windows.show()
        guard model.setup.isUsable else { return }
        ImportPanel.choose { [model] urls in Task { await model.importFiles(urls) } }
    }

    // The Profile menu of the main menu bar acts on the profile the window shows.

    @objc func toggleSelectedProfile() {
        guard let profile = model.selectedProfile else { return }
        model.setEnabled(!profile.desiredEnabled, profileID: profile.id)
    }

    @objc func moveSelectedProfileUp() {
        guard let profile = model.selectedProfile else { return }
        Task { await model.move(profileID: profile.id, by: -1) }
    }

    @objc func moveSelectedProfileDown() {
        guard let profile = model.selectedProfile else { return }
        Task { await model.move(profileID: profile.id, by: 1) }
    }

    @objc func deleteSelectedProfile() {
        guard let profile = model.selectedProfile else { return }
        model.requestDeletion(of: profile.id)
    }

    @objc func showProfileSection(_ sender: NSMenuItem) {
        guard ProfileSection.allCases.indices.contains(sender.tag) else { return }
        windows.show()
        if model.selectedProfile == nil, let first = model.store.profiles.first { model.selection = .profile(first.id) }
        model.profileSection = ProfileSection.allCases[sender.tag]
    }

    @objc func showDiagnostics() {
        windows.show()
        model.selection = .diagnostics
    }

    @objc func resync() {
        Task { await model.resync() }
    }

    func validateMenuItem(_ item: NSMenuItem) -> Bool {
        let hasProfile = model.selectedProfile != nil && model.setup.isUsable
        switch item.action {
        case #selector(toggleSelectedProfile):
            let profile = model.selectedProfile
            item.title = String(localized: profile?.desiredEnabled == true ? "Disconnect" : "Connect", bundle: .module)
            return hasProfile && profile?.state != .disconnecting
        case #selector(showProfileSection(_:)):
            item.state = model.selectedProfile != nil && ProfileSection.allCases.firstIndex(of: model.profileSection) == item.tag ? .on : .off
            return model.setup.isUsable && !model.store.profiles.isEmpty
        case #selector(deleteSelectedProfile):
            return hasProfile
        case #selector(moveSelectedProfileUp):
            return hasProfile && model.store.profiles.first?.id != model.selectedProfile?.id
        case #selector(moveSelectedProfileDown):
            return hasProfile && model.store.profiles.last?.id != model.selectedProfile?.id
        case #selector(disconnectAll):
            return !model.switchedOnProfiles.isEmpty
        case #selector(resync):
            return model.setup.isUsable
        case #selector(importProfile):
            return model.setup.isUsable
        default:
            return true
        }
    }
}
