import AppKit
import PlaitwayClient
import SwiftUI

/// What the profile has put in the routing table and in DNS, and for each entry whether it is
/// in effect and, when it is not, why.
struct RoutesTab: View {
    let profile: Profile
    @Environment(AppModel.self) private var model
    @State private var selection = Set<RouteRow.ID>()

    var body: some View {
        let routes = RouteRow.rows(for: profile, name: model.profileName)
        let dns = DNSRow.rows(for: profile)
        if routes.isEmpty && dns.isEmpty && profile.summary.dnsServers.isEmpty {
            ContentUnavailableView {
                Label { Text("No Routes", bundle: .module) } icon: { Image(systemName: "arrow.triangle.branch") }
            }
        } else {
            VStack(spacing: 0) {
                Table(routes, selection: $selection) {
                    TableColumn(Text("Prefix", bundle: .module)) { row in
                        Text(verbatim: row.prefix).monospaced().textSelection(.enabled)
                    }
                    .width(min: 140, ideal: 180)
                    TableColumn(Text("State", bundle: .module)) { row in
                        if let state = row.state {
                            StatusLabel(state, text: row.stateLabel)
                        }
                    }
                    .width(min: 150, ideal: 220)
                    TableColumn(Text("Reason", bundle: .module)) { row in
                        Text(verbatim: row.detail).foregroundStyle(.secondary).textSelection(.enabled)
                    }
                }
                .contextMenu(forSelectionType: RouteRow.ID.self) { ids in
                    Button { copy(prefixes(of: ids, in: routes)) } label: { Text("Copy", bundle: .module) }
                        .disabled(ids.isEmpty)
                }
                .accessibilityIdentifier("profile.routes")

                if !dns.isEmpty || !profile.summary.dnsServers.isEmpty {
                    DNSList(rows: dns, declared: profile.summary.dnsServers)
                }
            }
        }
    }

    private func prefixes(of ids: Set<RouteRow.ID>, in routes: [RouteRow]) -> String {
        routes.filter { ids.contains($0.id) }.map(\.prefix).joined(separator: "\n")
    }

    private func copy(_ text: String) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
    }
}

private struct DNSList: View {
    let rows: [DNSRow]
    /// The servers the profile names, shown while it is not connected.
    let declared: [String]

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("DNS", bundle: .module).font(.headline)
            if rows.isEmpty {
                Text(verbatim: declared.joined(separator: ", ")).monospaced().textSelection(.enabled)
            }
            ForEach(rows) { row in
                HStack(alignment: .firstTextBaseline) {
                    VStack(alignment: .leading, spacing: 2) {
                        Text(verbatim: row.servers).monospaced().textSelection(.enabled)
                        Text(verbatim: row.domains).font(.caption).foregroundStyle(.secondary)
                        if !row.detail.isEmpty {
                            Text(verbatim: row.detail).font(.caption).foregroundStyle(.secondary)
                        }
                    }
                    Spacer()
                    StatusLabel(row.state)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(16)
        .accessibilityIdentifier("profile.dns")
    }
}
