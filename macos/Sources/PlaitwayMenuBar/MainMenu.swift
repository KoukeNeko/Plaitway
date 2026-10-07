import AppKit

/// The menu bar the app shows while its window is open. The Edit menu is not
/// decoration: text fields take Copy and Paste through it. Every command a toolbar
/// button or a context menu offers is here too.
@MainActor
enum MainMenu {
    static func make(target: AppDelegate) -> NSMenu {
        let main = NSMenu()
        main.addItem(submenu(title: "Plaitway", items: [
            item(String(localized: "About Plaitway", bundle: .module), #selector(NSApplication.orderFrontStandardAboutPanel(_:))),
            .separator(),
            item(String(localized: "Settings…", bundle: .module), #selector(AppDelegate.openSettings), ",", target: target),
            .separator(),
            item(String(localized: "Hide Plaitway", bundle: .module), #selector(NSApplication.hide(_:)), "h"),
            item(String(localized: "Hide Others", bundle: .module), #selector(NSApplication.hideOtherApplications(_:)), "h", [.command, .option]),
            item(String(localized: "Show All", bundle: .module), #selector(NSApplication.unhideAllApplications(_:))),
            .separator(),
            item(String(localized: "Quit Plaitway", bundle: .module), #selector(NSApplication.terminate(_:)), "q"),
        ]))
        main.addItem(submenu(title: String(localized: "File", bundle: .module), items: [
            item(String(localized: "Import Profile…", bundle: .module), #selector(AppDelegate.importProfile), "i", target: target),
            .separator(),
            item(String(localized: "Close", bundle: .module), #selector(NSWindow.performClose(_:)), "w"),
        ]))
        main.addItem(submenu(title: String(localized: "Edit", bundle: .module), items: [
            item(String(localized: "Undo", bundle: .module), Selector(("undo:")), "z"),
            item(String(localized: "Redo", bundle: .module), Selector(("redo:")), "z", [.command, .shift]),
            .separator(),
            item(String(localized: "Cut", bundle: .module), #selector(NSText.cut(_:)), "x"),
            item(String(localized: "Copy", bundle: .module), #selector(NSText.copy(_:)), "c"),
            item(String(localized: "Paste", bundle: .module), #selector(NSText.paste(_:)), "v"),
            item(String(localized: "Select All", bundle: .module), #selector(NSText.selectAll(_:)), "a"),
            .separator(),
            // The search field of the logs, or the find bar of the editor, on the pages that have one.
            item(String(localized: "Find…", bundle: .module), #selector(AppDelegate.findInPage), "f", target: target),
        ]))
        main.addItem(submenu(title: String(localized: "Profile", bundle: .module), items: [
            // The title follows the profile: Connect, or Disconnect when it is on.
            item(String(localized: "Connect", bundle: .module), #selector(AppDelegate.toggleSelectedProfile), "k", target: target),
            item(String(localized: "Disconnect All", bundle: .module), #selector(AppDelegate.disconnectAll), "k", [.command, .shift], target: target),
            .separator(),
            item(String(localized: "Move Up", bundle: .module), #selector(AppDelegate.moveSelectedProfileUp), String(UnicodeScalar(NSUpArrowFunctionKey)!), [.command, .option], target: target),
            item(String(localized: "Move Down", bundle: .module), #selector(AppDelegate.moveSelectedProfileDown), String(UnicodeScalar(NSDownArrowFunctionKey)!), [.command, .option], target: target),
            .separator(),
            item(String(localized: "Delete Profile…", bundle: .module), #selector(AppDelegate.deleteSelectedProfile), "\u{8}", target: target),
        ]))
        let sections = ProfileSection.allCases.enumerated().map { index, section in
            let item = item(section.label, #selector(AppDelegate.showProfileSection(_:)), String(index + 1), target: target)
            item.tag = index
            return item
        }
        main.addItem(submenu(title: String(localized: "View", bundle: .module), items: sections + [
            .separator(),
            item(String(localized: "Toggle Sidebar", bundle: .module), #selector(NSSplitViewController.toggleSidebar(_:)), "s", [.command, .control]),
            .separator(),
            item(String(localized: "Diagnostics", bundle: .module), #selector(AppDelegate.showDiagnostics), "d", [.command, .shift], target: target),
            item(String(localized: "Resync", bundle: .module), #selector(AppDelegate.resync), "r", target: target),
        ]))
        let window = submenu(title: String(localized: "Window", bundle: .module), items: [
            item(String(localized: "Minimize", bundle: .module), #selector(NSWindow.performMiniaturize(_:)), "m"),
            item(String(localized: "Zoom", bundle: .module), #selector(NSWindow.performZoom(_:))),
        ])
        main.addItem(window)
        NSApp.windowsMenu = window.submenu
        return main
    }

    private static func submenu(title: String, items: [NSMenuItem]) -> NSMenuItem {
        let menu = NSMenu(title: title)
        items.forEach(menu.addItem)
        let holder = NSMenuItem(title: title, action: nil, keyEquivalent: "")
        holder.submenu = menu
        return holder
    }

    private static func item(
        _ title: String,
        _ action: Selector,
        _ key: String = "",
        _ modifiers: NSEvent.ModifierFlags = .command,
        target: AnyObject? = nil
    ) -> NSMenuItem {
        let item = NSMenuItem(title: title, action: action, keyEquivalent: key)
        item.keyEquivalentModifierMask = modifiers
        item.target = target
        return item
    }
}
