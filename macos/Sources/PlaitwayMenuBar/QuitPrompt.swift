import AppKit

/// What Quit asks while profiles are switched on: the helper, not the app, keeps them connected.
@MainActor
enum QuitPrompt {
    enum Answer {
        case quit
        case disconnectAndQuit
        case cancel
    }

    static func ask() -> Answer {
        let alert = NSAlert()
        alert.messageText = String(localized: "Quit Plaitway?", bundle: .module)
        alert.informativeText = String(localized: "Connected profiles stay connected.", bundle: .module)
        alert.addButton(withTitle: String(localized: "Quit", bundle: .module))
        alert.addButton(withTitle: String(localized: "Disconnect All and Quit", bundle: .module))
        let cancel = alert.addButton(withTitle: String(localized: "Cancel", bundle: .module))
        cancel.keyEquivalent = "\u{1b}"
        // The app may have no window and no Dock icon; the alert must not appear behind other apps.
        NSApp.activate()
        switch alert.runModal() {
        case .alertFirstButtonReturn: return .quit
        case .alertSecondButtonReturn: return .disconnectAndQuit
        default: return .cancel
        }
    }
}
