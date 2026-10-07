import PlaitwayClient
import SwiftUI

extension DaemonRegistration {
    var label: String {
        switch self {
        case .enabled: String(localized: "Installed", bundle: .module)
        case .requiresApproval: String(localized: "Approval required", bundle: .module)
        case .notRegistered: String(localized: "Not installed", bundle: .module)
        case .misplaced: String(localized: "Not in Applications", bundle: .module)
        case .notFound: String(localized: "Not found", bundle: .module)
        case .notBundled: String(localized: "Development build", bundle: .module)
        }
    }
}

/// The app's own settings and the helper, in the window that Settings… (⌘,) opens.
struct SettingsView: View {
    @Environment(AppModel.self) private var model
    /// The window's own: the main window presents the one the version banner asks for.
    @State private var isConfirmingReinstall = false

    var body: some View {
        Form {
            Section {
                Toggle(isOn: Binding(get: { model.loginItem.isEnabled }, set: { model.setLaunchAtLogin($0) })) {
                    Text("Launch at Login", bundle: .module)
                }
                .disabled(!model.loginItem.isAvailable)
                .accessibilityIdentifier("general.launchAtLogin")

                Toggle(isOn: Bindable(model).showsMenuBarItem) {
                    Text("Show in Menu Bar", bundle: .module)
                }
                .accessibilityIdentifier("general.showInMenuBar")
            }

            Section {
                InfoRow("Status", model.isHelperExternal ? String(localized: "Installed externally", bundle: .module) : model.installer.registration.label)
                if let info = model.store.daemonInfo {
                    InfoRow("Helper version", info.version)
                    ForEach(info.engines, id: \.kind) { engine in
                        InfoRow(engine.kind == .wireguard ? "WireGuard" : "OpenVPN", engineText(engine))
                    }
                    if !info.privileged {
                        InfoRow("Privileges", String(localized: "Limited", bundle: .module))
                    }
                }
                if let appVersion = model.appVersion {
                    InfoRow("App version", appVersion)
                }
                if canManageHelper {
                    HStack {
                        Button { isConfirmingReinstall = true } label: { Text("Reinstall Helper", bundle: .module) }
                            .accessibilityIdentifier("general.reinstall")
                        Button(role: .destructive) { model.isConfirmingUninstall = true } label: {
                            Text("Uninstall Helper…", bundle: .module)
                        }
                        .disabled(model.installer.registration == .notRegistered)
                        .accessibilityIdentifier("general.uninstall")
                    }
                }
            } header: {
                Text("Helper", bundle: .module)
            }
        }
        .formStyle(.grouped)
        .frame(width: 520)
        .fixedSize(horizontal: false, vertical: true)
        .modifier(AlertPresenter())
        .onAppear {
            model.installer.refresh()
            model.loginItem.refresh()
        }
        .confirmationDialog(
            Text("Reinstall helper?", bundle: .module),
            isPresented: $isConfirmingReinstall,
            titleVisibility: .visible
        ) {
            Button(role: .destructive) {
                Task { await model.reinstallHelper() }
            } label: { Text("Reinstall", bundle: .module) }
            Button(role: .cancel) { } label: { Text("Cancel", bundle: .module) }
        } message: {
            Text("Connected profiles disconnect.", bundle: .module)
        }
        .confirmationDialog(
            Text("Uninstall helper?", bundle: .module),
            isPresented: Bindable(model).isConfirmingUninstall,
            titleVisibility: .visible
        ) {
            Button(role: .destructive) {
                Task { await model.uninstallHelper() }
            } label: { Text("Uninstall", bundle: .module) }
            Button(role: .cancel) { } label: { Text("Cancel", bundle: .module) }
        } message: {
            // The daemon keeps its state across a reinstall, so unregistering leaves the profiles (and their keys) on disk.
            Text(verbatim: [
                String(localized: "Connected profiles disconnect.", bundle: .module),
                String(localized: "Profiles stay in \(DaemonLocation.stateDirectory), the log in \(DaemonLocation.logPath).", bundle: .module),
            ].joined(separator: "\n"))
        }
    }

    private var canManageHelper: Bool {
        !model.isOverridden && model.installer.registration != .notBundled && !model.isHelperExternal
    }

    /// The engine's version, or why it cannot run.
    private func engineText(_ engine: EngineInfo) -> String {
        if engine.available { return engine.version }
        return engine.detail.isEmpty ? String(localized: "Unavailable", bundle: .module) : engine.detail
    }
}
