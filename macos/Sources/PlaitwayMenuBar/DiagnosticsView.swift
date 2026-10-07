import AppKit
import PlaitwayClient
import SwiftUI

struct DiagnosticsView: View {
    private enum Page: CaseIterable, Hashable {
        case overview, daemonLog

        var label: String {
            switch self {
            case .overview: String(localized: "Overview", bundle: .module)
            case .daemonLog: String(localized: "Helper log", bundle: .module)
            }
        }
    }

    @Environment(AppModel.self) private var model
    @State private var diagnostics = DiagnosticsModel()
    @State private var page: Page = .overview
    @State private var isResyncing = false
    @State private var staleToRemove: StaleRoute?

    var body: some View {
        VStack(spacing: 0) {
            SectionPicker(selection: $page, label: \.label)
            switch page {
            // The reading is polled only while it is on screen: on the helper log page
            // every GetDiagnostics call would show up in the log being read.
            case .overview: overview.task { await diagnostics.run(store: model.store) }
            case .daemonLog: LogView(profileID: "")
            }
        }
        .navigationTitle(Text("Diagnostics", bundle: .module))
        .toolbar {
            if page == .overview {
                ToolbarItemGroup(placement: .primaryAction) {
                    Button { copyReport() } label: {
                        Label { Text("Copy Report", bundle: .module) } icon: { Image(systemName: "doc.on.doc") }
                    }
                    .help(Text("Copy Report", bundle: .module))
                    .disabled(diagnostics.diagnostics == nil)
                    .accessibilityIdentifier("diagnostics.copyReport")
                    Button {
                        Task { await resync() }
                    } label: {
                        Label { Text("Resync", bundle: .module) } icon: { Image(systemName: "arrow.triangle.2.circlepath") }
                    }
                    .help(Text("Resync", bundle: .module))
                    .disabled(isResyncing)
                    .accessibilityIdentifier("diagnostics.resync")
                }
            }
        }
        .confirmationDialog(
            Text("Remove route \(staleRoutePrefix)?", bundle: .module),
            isPresented: Binding(get: { staleToRemove != nil }, set: { if !$0 { staleToRemove = nil } }),
            titleVisibility: .visible,
            presenting: staleToRemove
        ) { route in
            Button(role: .destructive) {
                Task {
                    await model.removeStaleRoute(key: route.key)
                    await diagnostics.refresh(store: model.store)
                }
            } label: { Text("Remove", bundle: .module) }
            Button(role: .cancel) { } label: { Text("Cancel", bundle: .module) }
        }
    }

    private var staleRoutePrefix: String {
        staleToRemove?.prefix ?? ""
    }

    private func resync() async {
        isResyncing = true
        defer { isResyncing = false }
        await model.resync()
        await diagnostics.refresh(store: model.store)
    }

    private func copyReport() {
        guard let current = diagnostics.diagnostics else { return }
        let report = DiagnosticsReport.text(
            current,
            appVersion: model.appVersion,
            daemon: model.store.daemonInfo,
            profileName: model.profileName
        )
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(report, forType: .string)
    }

    @ViewBuilder
    private var overview: some View {
        if let current = diagnostics.diagnostics {
            Form {
                // What needs doing comes first: the product exists to leave none of these behind.
                if !current.staleRoutes.isEmpty {
                    StaleRoutesSection(routes: current.staleRoutes) { staleToRemove = $0 }
                }
                NetworkSection(network: current.network)
                OwnedRoutesSection(routes: current.ownedRoutes)
                ResolverSection(entries: current.resolverEntries)
                JournalSection(entries: current.recentJournal)
            }
            .formStyle(.grouped)
            .accessibilityIdentifier("diagnostics.overview")
        } else {
            VStack {
                if let failure = diagnostics.failure {
                    Text(verbatim: failure).foregroundStyle(.secondary)
                } else {
                    ProgressView().controlSize(.small)
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }
}

private struct NetworkSection: View {
    let network: NetworkInfo

    var body: some View {
        Section {
            if !network.defaultGatewayV4.isEmpty {
                InfoRow("Default gateway", "\(network.defaultGatewayV4) · \(network.defaultInterfaceV4)")
            }
            if !network.defaultGatewayV6.isEmpty {
                InfoRow("Default gateway IPv6", "\(network.defaultGatewayV6) · \(network.defaultInterfaceV6)")
            }
            if !network.interfaces.isEmpty {
                InfoRow("Interfaces", network.interfaces.joined(separator: ", "))
            }
            if network.hasLastChange {
                InfoRow("Last change", "\(network.lastChange.date.formatted(date: .omitted, time: .standard)) · \(network.lastChangeReason)")
            }
        } header: {
            Text("Network", bundle: .module)
        }
    }
}

private struct OwnedRoutesSection: View {
    let routes: [OwnedRoute]
    @Environment(AppModel.self) private var model

    var body: some View {
        Section {
            if routes.isEmpty {
                Text("None", bundle: .module).foregroundStyle(.secondary)
            }
            ForEach(Array(routes.enumerated()), id: \.offset) { _, route in
                LabeledContent {
                    StatusLabel(route.state)
                } label: {
                    VStack(alignment: .leading, spacing: 2) {
                        Text(verbatim: route.prefix).monospaced()
                        Text(verbatim: "\(model.profileName(route.owner) ?? route.owner) · \(route.kind.label) · \(route.via)")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }
            }
        } header: {
            Text("Routes", bundle: .module)
        }
    }
}

private struct StaleRoutesSection: View {
    let routes: [StaleRoute]
    let remove: (StaleRoute) -> Void

    var body: some View {
        Section {
            ForEach(routes, id: \.key) { route in
                LabeledContent {
                    Button { remove(route) } label: { Text("Remove", bundle: .module) }
                        .accessibilityIdentifier("stale.remove.\(route.key)")
                } label: {
                    VStack(alignment: .leading, spacing: 2) {
                        Label {
                            Text(verbatim: route.prefix).monospaced()
                        } icon: {
                            Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                        }
                        Text(verbatim: via(route)).font(.caption).foregroundStyle(.secondary)
                        Text(verbatim: route.reason).font(.caption).foregroundStyle(.secondary)
                        if route.owned {
                            Text("Installed by Plaitway", bundle: .module).font(.caption).foregroundStyle(.secondary)
                        }
                    }
                }
            }
        } header: {
            Text("Stale routes", bundle: .module)
        }
    }

    private func via(_ route: StaleRoute) -> String {
        [route.gateway, route.interface].filter { !$0.isEmpty }.joined(separator: " · ")
    }
}

private struct ResolverSection: View {
    let entries: [String]

    var body: some View {
        Section {
            DisclosureGroup {
                if entries.isEmpty {
                    Text("None", bundle: .module).foregroundStyle(.secondary)
                }
                ForEach(entries, id: \.self) { entry in
                    Text(verbatim: entry).monospaced().textSelection(.enabled)
                }
            } label: {
                Text("Resolver entries", bundle: .module)
            }
        }
    }
}

private struct JournalSection: View {
    let entries: [JournalEntry]
    @Environment(AppModel.self) private var model

    var body: some View {
        Section {
            DisclosureGroup {
                if entries.isEmpty {
                    Text("None", bundle: .module).foregroundStyle(.secondary)
                }
                // Newest first.
                ForEach(Array(entries.reversed().prefix(50).enumerated()), id: \.offset) { _, entry in
                    LabeledContent {
                        Text(verbatim: entry.state)
                    } label: {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(verbatim: "\(entry.kind) \(entry.key)").monospaced()
                            Text(verbatim: caption(entry)).font(.caption).foregroundStyle(.secondary)
                        }
                    }
                }
            } label: {
                Text("Recent changes", bundle: .module)
            }
        }
    }

    private func caption(_ entry: JournalEntry) -> String {
        let time = entry.hasTime ? entry.time.date.formatted(date: .omitted, time: .standard) : ""
        let owner = entry.owner.isEmpty ? "" : (model.profileName(entry.owner) ?? entry.owner)
        return [time, owner].filter { !$0.isEmpty }.joined(separator: " · ")
    }
}

/// Everything the Diagnostics page knows, as plain text for a bug report. No log lines: they
/// are the person's to review before they share them.
enum DiagnosticsReport {
    static func text(
        _ diagnostics: Diagnostics,
        appVersion: String?,
        daemon: DaemonInfo?,
        profileName: (String) -> String?
    ) -> String {
        var lines: [String] = []
        lines.append("Plaitway \(appVersion ?? "-")")
        if let daemon {
            lines.append("Helper \(daemon.version) (privileged: \(daemon.privileged))")
            for engine in daemon.engines {
                lines.append("  \(engine.kind == .wireguard ? "WireGuard" : "OpenVPN"): \(engine.available ? engine.version : engine.detail)")
            }
        }

        let network = diagnostics.network
        lines.append("")
        lines.append("Network")
        lines.append("  gateway IPv4: \(network.defaultGatewayV4) \(network.defaultInterfaceV4)")
        lines.append("  gateway IPv6: \(network.defaultGatewayV6) \(network.defaultInterfaceV6)")
        lines.append("  interfaces: \(network.interfaces.joined(separator: ", "))")
        if network.hasLastChange {
            lines.append("  last change: \(network.lastChange.date.formatted(.iso8601)) \(network.lastChangeReason)")
        }

        lines.append("")
        lines.append("Owned routes")
        for route in diagnostics.ownedRoutes {
            lines.append("  \(route.prefix) \(route.state.label()) \(route.kind.label) via \(route.via) (\(profileName(route.owner) ?? route.owner))")
        }

        lines.append("")
        lines.append("Stale routes")
        for route in diagnostics.staleRoutes {
            lines.append("  \(route.prefix) via \(route.gateway) \(route.interface): \(route.reason)\(route.owned ? " (installed by Plaitway)" : "")")
        }

        lines.append("")
        lines.append("Resolver entries")
        for entry in diagnostics.resolverEntries { lines.append("  \(entry)") }

        lines.append("")
        lines.append("Recent changes")
        for entry in diagnostics.recentJournal.suffix(20) {
            let time = entry.hasTime ? entry.time.date.formatted(.iso8601) : "-"
            lines.append("  \(time) \(entry.kind) \(entry.key) \(entry.state) \(entry.owner.isEmpty ? "" : profileName(entry.owner) ?? entry.owner)")
        }
        return lines.joined(separator: "\n")
    }
}
