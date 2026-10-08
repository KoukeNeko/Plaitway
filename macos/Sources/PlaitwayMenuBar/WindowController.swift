import AppKit
import SwiftUI

/// The management window and the settings window. The app has no Dock icon while both are
/// closed; an open window makes it a regular app, so that it has a menu bar and can be found
/// with Command-Tab. Closing a window releases it with its views: the tasks they own (the
/// diagnostics poll, the log streams) would otherwise keep calling the daemon for as long as
/// the app runs.
@MainActor
final class WindowController: NSObject, NSWindowDelegate {
    private static let mainFrameName = "PlaitwayMainWindow"
    private static let settingsFrameName = "PlaitwaySettingsWindow"

    private let model: AppModel
    private var mainWindow: NSWindow?
    private var settingsWindow: NSWindow?

    /// The management window is open.
    var isVisible: Bool { mainWindow?.isVisible ?? false }
    /// The management window is the one that takes the keyboard.
    var isMainWindowKey: Bool { mainWindow?.isKeyWindow ?? false }

    init(model: AppModel) {
        self.model = model
    }

    func show() {
        if mainWindow == nil { mainWindow = makeMainWindow() }
        bringForward(mainWindow)
    }

    func showSettings() {
        if settingsWindow == nil { settingsWindow = makeSettingsWindow() }
        bringForward(settingsWindow)
    }

    private func bringForward(_ window: NSWindow?) {
        NSApp.setActivationPolicy(.regular)
        if window?.isMiniaturized == true { window?.deminiaturize(nil) }
        NSApp.activate()
        window?.makeKeyAndOrderFront(nil)
        // Since macOS 26 an activation from a status menu can leave the window behind the front
        // app. This looks again on the next turn, when the activation has been decided.
        Task { @MainActor in
            guard let window, window.isVisible, !window.isKeyWindow else { return }
            window.orderFrontRegardless()
            NSApp.activate(ignoringOtherApps: true)
        }
    }

    func windowWillClose(_ notification: Notification) {
        guard let closing = notification.object as? NSWindow else { return }
        // Not inside the delegate call: the window is still closing.
        Task { @MainActor in
            if closing === mainWindow, !closing.isVisible {
                closing.contentViewController = nil
                mainWindow = nil
            } else if closing === settingsWindow, !closing.isVisible {
                closing.contentViewController = nil
                settingsWindow = nil
            }
            // The Dock icon stays while the other window is open.
            if mainWindow?.isVisible != true, settingsWindow?.isVisible != true {
                NSApp.setActivationPolicy(.accessory)
            }
        }
    }

    private func makeMainWindow() -> NSWindow {
        let window = NSWindow(contentViewController: NSHostingController(rootView: RootView().environment(model)))
        // The title says where the person is (a profile, Diagnostics); the app's name is in the menu bar.
        window.title = ""
        // The sidebar runs the full height of the window, with the traffic lights on it, and the
        // toolbar floats over the page: the content reaches under the title bar.
        window.styleMask = [.titled, .closable, .miniaturizable, .resizable, .fullSizeContentView]
        window.isReleasedWhenClosed = false
        window.delegate = self
        window.contentMinSize = NSSize(width: 960, height: 480)
        window.setContentSize(NSSize(width: 1040, height: 660))
        if !window.setFrameUsingName(Self.mainFrameName) { window.center() }
        window.setFrameAutosaveName(Self.mainFrameName)
        return window
    }

    private func makeSettingsWindow() -> NSWindow {
        let window = NSWindow(contentViewController: NSHostingController(rootView: SettingsView().environment(model)))
        window.title = String(localized: "Settings", bundle: .module)
        window.styleMask = [.titled, .closable]
        window.isReleasedWhenClosed = false
        window.delegate = self
        if !window.setFrameUsingName(Self.settingsFrameName) { window.center() }
        window.setFrameAutosaveName(Self.settingsFrameName)
        return window
    }
}
