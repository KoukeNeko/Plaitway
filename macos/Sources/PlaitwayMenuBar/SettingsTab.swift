import PlaitwayClient
import SwiftUI

/// How one profile behaves: its name, when it connects, and what it may take over.
struct SettingsTab: View {
    let profile: Profile
    @Environment(AppModel.self) private var model
    @State private var name = ""
    @State private var hasSavedCredentials = false
    @FocusState private var isNameFocused: Bool

    var body: some View {
        Form {
            Section {
                TextField(text: $name) {
                    Text("Name", bundle: .module)
                }
                .focused($isNameFocused)
                .onSubmit(commitName)
                .onChange(of: isNameFocused) { _, focused in if !focused { commitName() } }
                .accessibilityIdentifier("settings.name")

                Toggle(isOn: Binding(
                    get: { profile.settings.autoConnect },
                    set: { value in Task { await model.changeSettings(profileID: profile.id) { $0.autoConnect = value } } }
                )) {
                    Text("Auto-connect", bundle: .module)
                }
                .accessibilityIdentifier("settings.autoConnect")

                Picker(selection: Binding(
                    get: { profile.settings.tunnelMode == .unspecified ? .auto : profile.settings.tunnelMode },
                    set: { value in Task { await model.changeSettings(profileID: profile.id) { $0.tunnelMode = value } } }
                )) {
                    ForEach(TunnelMode.choices, id: \.self) { mode in
                        Text(verbatim: mode.label).tag(mode)
                    }
                } label: {
                    Text("Tunnel mode", bundle: .module)
                }
                .accessibilityIdentifier("settings.tunnelMode")
                .help(appliesNextConnection)

                if profile.kind == .wireguard {
                    Toggle(isOn: Binding(
                        get: { profile.settings.excludePrivateIps },
                        set: { value in Task { await model.changeSettings(profileID: profile.id) { $0.excludePrivateIps = value } } }
                    )) {
                        Text("Exclude private IPs", bundle: .module)
                    }
                    .accessibilityIdentifier("settings.excludePrivateIPs")
                    .help(isIdle ? Text("Private address ranges stay outside the tunnel.", bundle: .module) : appliesNextConnection)
                }

                LabeledContent {
                    HStack {
                        Text(verbatim: "\(position)").monospacedDigit().accessibilityIdentifier("settings.priority")
                        Button { Task { await model.move(profileID: profile.id, by: -1) } } label: {
                            Image(systemName: "chevron.up")
                        }
                        .accessibilityLabel(Text("Move Up", bundle: .module))
                        .disabled(position <= 1)
                        Button { Task { await model.move(profileID: profile.id, by: 1) } } label: {
                            Image(systemName: "chevron.down")
                        }
                        .accessibilityLabel(Text("Move Down", bundle: .module))
                        .disabled(position >= model.store.profiles.count)
                    }
                } label: {
                    Text("Priority", bundle: .module)
                }
                .help(Text("A higher priority wins overlapping routes and DNS domains.", bundle: .module))
            }

            Section {
                Toggle(isOn: onDemandBinding(\.ethernet)) { Text("Ethernet", bundle: .module) }
                    .accessibilityIdentifier("settings.onDemand.ethernet")
                Toggle(isOn: onDemandBinding(\.wifi)) { Text("Wi-Fi", bundle: .module) }
                    .accessibilityIdentifier("settings.onDemand.wifi")
            } header: {
                Text("On demand", bundle: .module)
            } footer: {
                Text("Connects on the checked networks and disconnects on any other.", bundle: .module)
            }

            Section {
                if hasSavedCredentials {
                    Button {
                        Task {
                            await model.store.forgetSavedCredentials(profileID: profile.id)
                            hasSavedCredentials = false
                        }
                    } label: { Text("Remove Saved Credentials", bundle: .module) }
                        .accessibilityIdentifier("settings.forgetCredentials")
                }
                Button(role: .destructive) { model.requestDeletion(of: profile.id) } label: {
                    Text("Delete Profile…", bundle: .module)
                }
                .accessibilityIdentifier("settings.delete")
            }
        }
        .formStyle(.grouped)
        .task(id: profile.name) { if !isNameFocused { name = profile.name } }
        .task(id: profile.id) {
            var found = false
            for kind in [CredentialKind.userPassword, .keyPassphrase] {
                if await model.store.hasSavedCredentials(profileID: profile.id, kind: kind) { found = true }
            }
            hasSavedCredentials = found
        }
    }

    private var isIdle: Bool {
        profile.state == .disconnected || profile.state == .failed
    }

    /// The tunnel mode and the address ranges are read when a connection starts.
    private var appliesNextConnection: Text {
        isIdle ? Text(verbatim: "") : Text("Applies at next connection", bundle: .module)
    }

    private func onDemandBinding(_ keyPath: WritableKeyPath<OnDemandRules, Bool>) -> Binding<Bool> {
        Binding(
            get: { profile.settings.onDemand[keyPath: keyPath] },
            set: { value in Task { await model.changeSettings(profileID: profile.id) { $0.onDemand[keyPath: keyPath] = value } } }
        )
    }

    /// The profile's place in the priority order, 1 being the highest.
    private var position: Int {
        (model.store.profiles.firstIndex { $0.id == profile.id } ?? 0) + 1
    }

    private func commitName() {
        let id = profile.id
        let current = profile.name
        Task {
            // The daemon may refuse the name (another profile has it); the field then
            // goes back to the name the profile still has.
            if await !model.rename(profileID: id, to: name) { name = current }
        }
    }
}
