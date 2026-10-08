import AppKit
import PlaitwayClient
import SwiftUI

enum ProfileSection: PageSet {
    case overview, routes, logs, configuration, settings

    var label: String {
        switch self {
        case .overview: String(localized: "Overview", bundle: .module)
        case .routes: String(localized: "Routes and DNS", bundle: .module)
        case .logs: String(localized: "Logs", bundle: .module)
        case .configuration: String(localized: "Configuration", bundle: .module)
        case .settings: String(localized: "Settings", bundle: .module)
        }
    }
}

struct ProfileDetailView: View {
    let profile: Profile
    @Environment(AppModel.self) private var model

    var body: some View {
        Group {
            switch model.profileSection {
            case .overview: OverviewTab(profile: profile, showRoutes: { model.profileSection = .routes })
            case .routes: RoutesTab(profile: profile)
            case .logs: LogView(profileID: profile.id)
            case .configuration: ConfigurationTab(profile: profile)
            case .settings: SettingsTab(profile: profile)
            }
        }
        // A profile of its own: typed text and the log stream do not carry over.
        .id(profile.id)
        .safeAreaInset(edge: .bottom, spacing: 0) { ProfileActionBar(profile: profile) }
        .navigationTitle(Text(verbatim: profile.name))
        .navigationSubtitle(Text(verbatim: "\(profile.kind.label) · \(profile.state.label)"))
    }
}

/// What can be done with the profile as a whole, at the bottom of each of its pages.
private struct ProfileActionBar: View {
    let profile: Profile
    @Environment(AppModel.self) private var model

    var body: some View {
        VStack(spacing: 0) {
            Divider()
            HStack(spacing: 8) {
                Spacer()
                if profile.state == .failed {
                    Button { model.setEnabled(true, profileID: profile.id) } label: { Text("Retry", bundle: .module) }
                        .accessibilityIdentifier("profile.retry")
                }
                ConnectButton(profile: profile)
            }
            .controlSize(.large)
            .padding(12)
        }
        .background(.bar)
    }
}

/// Connects or disconnects: one action at a time, named for what pressing it does.
struct ConnectButton: View {
    let profile: Profile
    @Environment(AppModel.self) private var model

    var body: some View {
        let button = Button {
            model.setEnabled(!profile.desiredEnabled, profileID: profile.id)
        } label: {
            Text(profile.desiredEnabled ? "Disconnect" : "Connect", bundle: .module)
        }
        .disabled(profile.state == .disconnecting)
        .accessibilityIdentifier("profile.toggle")

        if profile.desiredEnabled {
            button
        } else {
            button.buttonStyle(.borderedProminent)
        }
    }
}

/// A label and a value on one line.
struct InfoRow: View {
    let label: LocalizedStringKey
    let value: String
    /// A value that changes every second cannot be selected.
    let isSelectable: Bool

    init(_ label: LocalizedStringKey, _ value: String, isSelectable: Bool = true) {
        self.label = label
        self.value = value
        self.isSelectable = isSelectable
    }

    var body: some View {
        LabeledContent {
            if isSelectable {
                Text(verbatim: value).textSelection(.enabled)
            } else {
                Text(verbatim: value)
            }
        } label: {
            Text(label, bundle: .module)
        }
    }
}

// MARK: Overview

private struct OverviewTab: View {
    let profile: Profile
    let showRoutes: () -> Void

    var body: some View {
        Form {
            Section { StatusHero(profile: profile) }

            if !lostRoutes.isEmpty || !profile.status.warnings.isEmpty {
                Section {
                    if !lostRoutes.isEmpty {
                        LabeledContent {
                            HStack(spacing: 10) {
                                Text(verbatim: "\(lostRoutes.count)").monospacedDigit()
                                Button(action: showRoutes) { Text("Show", bundle: .module) }
                                    .buttonStyle(.link)
                            }
                        } label: {
                            Label { Text("Routes not in effect", bundle: .module) } icon: {
                                Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                            }
                        }
                        .accessibilityIdentifier("profile.lostRoutes")
                    }
                    ForEach(profile.status.warnings, id: \.self) { warning in
                        Label {
                            Text(verbatim: warning).textSelection(.enabled)
                        } icon: {
                            Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                        }
                    }
                } header: {
                    Text("Attention", bundle: .module)
                }
            }

            if profile.state == .connected {
                Section { TrafficView(profile: profile) }
            }

            Section {
                if !remote.isEmpty { InfoRow("Remote", remote) }
                if !profile.status.interfaceName.isEmpty { InfoRow("Interface", profile.status.interfaceName) }
                if !addresses.isEmpty { InfoRow("Addresses", addresses) }
                if !profile.summary.dnsServers.isEmpty { InfoRow("DNS", profile.summary.dnsServers.joined(separator: ", ")) }
                if !profile.summary.publicKey.isEmpty { PublicKeyRow(key: profile.summary.publicKey) }
            }
        }
        .formStyle(.grouped)
        .accessibilityIdentifier("profile.overview")
    }

    private var lostRoutes: [RouteStatus] {
        profile.status.routes.filter(\.state.isLost)
    }

    /// The endpoint in use, or the ones the profile names while it is not connected.
    private var remote: String {
        if !profile.status.remote.isEmpty { return profile.status.remote }
        return profile.summary.endpoints
            .map { Formatting.endpoint(host: $0.host, port: $0.port, protocol: $0.protocol) }
            .joined(separator: "\n")
    }

    private var addresses: String {
        (profile.status.addresses.isEmpty ? profile.summary.addresses : profile.status.addresses).joined(separator: ", ")
    }
}

/// The state, why it is so, and for how long: what a person opens the page to see. The glyph sits
/// on a tile of its colour, centred against the words, and the uptime is laid out as the rates
/// below it are, the name above its value.
private struct StatusHero: View {
    let profile: Profile

    var body: some View {
        HStack(alignment: profile.lastError.isEmpty ? .center : .top, spacing: 16) {
            StatusGlyph(profile.state)
                .font(.system(size: 28))
                .frame(width: 52, height: 52)
                .background(profile.state.tint.opacity(0.14), in: .rect(cornerRadius: 13))
            VStack(alignment: .leading, spacing: 4) {
                Text(verbatim: profile.state.label)
                    .font(.title2.weight(.semibold))
                    .accessibilityIdentifier("profile.state")
                if !profile.lastError.isEmpty {
                    Text(verbatim: profile.lastError)
                        .foregroundStyle(profile.state == .failed ? Color.red : Color.secondary)
                        .textSelection(.enabled)
                        .accessibilityIdentifier("profile.cause")
                }
            }
            // The tile's height is the line's, so that a one-line state is centred on it.
            .frame(minHeight: 52, alignment: profile.lastError.isEmpty ? .center : .top)
            Spacer(minLength: 12)
            if profile.state == .connected, profile.status.hasConnectedSince {
                VStack(alignment: .trailing, spacing: 2) {
                    Text("Uptime", bundle: .module).font(.caption).foregroundStyle(.secondary)
                    Text(timerInterval: profile.status.connectedSince.date...Date.distantFuture, pauseTime: nil, countsDown: false, showsHours: true)
                        .font(.title3.weight(.semibold))
                        .monospacedDigit()
                        .help(Text(verbatim: profile.status.connectedSince.date.formatted(date: .abbreviated, time: .shortened)))
                }
            }
        }
        .padding(.vertical, 6)
        .accessibilityElement(children: .contain)
    }
}

private struct PublicKeyRow: View {
    let key: String

    var body: some View {
        LabeledContent {
            HStack(spacing: 6) {
                Text(verbatim: key).textSelection(.enabled).lineLimit(1).truncationMode(.middle)
                Button {
                    NSPasteboard.general.clearContents()
                    NSPasteboard.general.setString(key, forType: .string)
                } label: {
                    Image(systemName: "doc.on.doc")
                }
                .buttonStyle(.borderless)
                .help(Text("Copy", bundle: .module))
                .accessibilityLabel(Text("Copy", bundle: .module))
                .accessibilityIdentifier("profile.publicKey.copy")
            }
        } label: {
            Text("Public key", bundle: .module)
        }
        .accessibilityIdentifier("profile.publicKey")
    }
}
