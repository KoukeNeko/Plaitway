import AppKit
import PlaitwayClient
import SwiftUI

/// The file picker for importing profiles, for the toolbar, the empty state and the menu.
@MainActor
enum ImportPanel {
    static func choose(then handle: @escaping @MainActor ([URL]) -> Void) {
        let panel = NSOpenPanel()
        panel.allowsMultipleSelection = true
        panel.canChooseDirectories = false
        // Any file can be chosen: the daemon tells an OpenVPN profile from a wg-quick one
        // by its content. Filtering by file type left .conf files grey on machines where
        // another app (the WireGuard app, for one) declares its own type for that extension.
        panel.prompt = String(localized: "Import", bundle: .module)
        let finish: @MainActor (NSApplication.ModalResponse) -> Void = { response in
            if response == .OK { handle(panel.urls) }
        }
        if let window = NSApp.keyWindow {
            panel.beginSheetModal(for: window, completionHandler: finish)
        } else {
            panel.begin(completionHandler: finish)
        }
    }
}

struct MainView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        NavigationSplitView {
            SidebarView()
                .navigationSplitViewColumnWidth(min: 220, ideal: 260, max: 340)
        } detail: {
            VStack(spacing: 0) {
                if case .versionMismatch(let daemon, let app) = model.setup {
                    VersionBanner(daemon: daemon, app: app)
                } else if model.setup == .notResponding {
                    HelperUnavailableBanner()
                }
                DetailView()
            }
        }
        .toolbar {
            if let profile = model.selectedProfile {
                ToolbarItemGroup(placement: .primaryAction) {
                    if profile.state == .failed {
                        Button { model.setEnabled(true, profileID: profile.id) } label: { Text("Retry", bundle: .module) }
                            .accessibilityIdentifier("profile.retry")
                    }
                    ConnectButton(profile: profile)
                }
                ToolbarGap()
            }
            ToolbarItem(placement: .primaryAction) {
                Button {
                    ImportPanel.choose { urls in Task { await model.importFiles(urls) } }
                } label: {
                    Label { Text("Import Profile…", bundle: .module) } icon: { Image(systemName: "plus") }
                }
                .help(Text("Import Profile…", bundle: .module))
                .accessibilityIdentifier("toolbar.import")
            }
        }
        .onChange(of: model.store.profiles.map(\.id), initial: true) { model.reconcileSelection() }
    }
}

struct SidebarView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        @Bindable var model = model

        List(selection: $model.selection) {
            if !model.store.profiles.isEmpty {
                Section {
                    ForEach(model.store.profiles, id: \.id) { profile in
                        ProfileRow(profile: profile)
                            .tag(SidebarItem.profile(profile.id))
                            .contextMenu { ProfileContextMenu(profile: profile) }
                    }
                    .onMove { source, destination in
                        Task { await model.move(fromOffsets: source, toOffset: destination) }
                    }
                } header: {
                    Text("Profiles", bundle: .module)
                }
            }

            Section {
                HStack(spacing: 10) {
                    // The same column as the shields above it.
                    Image(systemName: "stethoscope")
                        .font(.system(size: 18))
                        .foregroundStyle(.secondary)
                        .frame(width: 30)
                        .accessibilityHidden(true)
                    Text("Diagnostics", bundle: .module)
                }
                .padding(.vertical, 3)
                .tag(SidebarItem.diagnostics)
                    .accessibilityIdentifier("sidebar.diagnostics")
            }
        }
        .listStyle(.sidebar)
        .accessibilityIdentifier("sidebar")
    }
}

/// A profile in the sidebar: its shield in the colour of its state, its name, and under it what
/// it is and how it stands.
private struct ProfileRow: View {
    let profile: Profile

    var body: some View {
        HStack(spacing: 10) {
            StatusGlyph(profile.state)
                .font(.system(size: 22))
                .frame(width: 30)
            VStack(alignment: .leading, spacing: 1) {
                Text(verbatim: profile.name)
                    .lineLimit(1)
                    .truncationMode(.middle)
                Text(verbatim: "\(profile.kind.label) · \(profile.state.label)")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
        }
        .padding(.vertical, 3)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("sidebar.profile.\(profile.id)")
    }
}

private struct ProfileContextMenu: View {
    let profile: Profile
    @Environment(AppModel.self) private var model

    var body: some View {
        Button {
            model.setEnabled(!profile.desiredEnabled, profileID: profile.id)
        } label: {
            Text(profile.desiredEnabled ? "Disconnect" : "Connect", bundle: .module)
        }
        .disabled(profile.state == .disconnecting)
        Divider()
        Button { Task { await model.move(profileID: profile.id, by: -1) } } label: { Text("Move Up", bundle: .module) }
        Button { Task { await model.move(profileID: profile.id, by: 1) } } label: { Text("Move Down", bundle: .module) }
        Divider()
        Button(role: .destructive) { model.requestDeletion(of: profile.id) } label: { Text("Delete Profile…", bundle: .module) }
    }
}

/// The page the sidebar selects.
struct DetailView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        switch model.selection {
        case .profile(let id):
            if let profile = model.store.profiles.first(where: { $0.id == id }) {
                ProfileDetailView(profile: profile)
            } else {
                EmptyProfilesView()
            }
        case .diagnostics:
            DiagnosticsView()
        case nil:
            EmptyProfilesView()
        }
    }
}

private struct EmptyProfilesView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        ContentUnavailableView {
            Label { Text("No Profiles", bundle: .module) } icon: { Image(systemName: "lock.shield") }
        } actions: {
            Button {
                ImportPanel.choose { urls in Task { await model.importFiles(urls) } }
            } label: { Text("Import Profile…", bundle: .module) }
                .accessibilityIdentifier("empty.import")
        }
        // The window is not titled with the app's name, and keeps no profile's name from before.
        .navigationTitle(Text(verbatim: ""))
    }
}
