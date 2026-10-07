import PlaitwayClient
import SwiftUI

extension ProfileState {
    var label: String {
        switch self {
        case .connected: String(localized: "Connected", bundle: .module)
        case .connecting: String(localized: "Connecting", bundle: .module)
        case .disconnecting: String(localized: "Disconnecting", bundle: .module)
        case .disconnected: String(localized: "Disconnected", bundle: .module)
        case .failed: String(localized: "Failed", bundle: .module)
        case .reconnecting: String(localized: "Reconnecting", bundle: .module)
        case .awaitingCredentials: String(localized: "Awaiting credentials", bundle: .module)
        case .unspecified, .UNRECOGNIZED: String(localized: "Unknown", bundle: .module)
        }
    }
}

extension ProfileKind {
    /// Protocol names are product names and stay untranslated.
    var label: String {
        switch self {
        case .openvpn: "OpenVPN"
        case .wireguard: "WireGuard"
        case .unspecified, .UNRECOGNIZED: String(localized: "Unknown", bundle: .module)
        }
    }
}

extension TunnelMode {
    static let choices: [TunnelMode] = [.auto, .full, .split]

    var label: String {
        switch self {
        case .auto, .unspecified, .UNRECOGNIZED: String(localized: "Auto", bundle: .module)
        case .full: String(localized: "Full tunnel", bundle: .module)
        case .split: String(localized: "Split tunnel", bundle: .module)
        }
    }
}

extension RouteState {
    /// `shadowedBy` is the name of the profile that holds the prefix instead.
    func label(shadowedBy: String? = nil) -> String {
        switch self {
        case .installed: String(localized: "Installed", bundle: .module)
        case .pending: String(localized: "Pending", bundle: .module)
        case .shadowed:
            if let shadowedBy {
                String(localized: "Shadowed by \(shadowedBy)", bundle: .module)
            } else {
                String(localized: "Shadowed", bundle: .module)
            }
        case .blocked: String(localized: "Blocked by local network", bundle: .module)
        case .failed: String(localized: "Failed", bundle: .module)
        case .unspecified, .UNRECOGNIZED: String(localized: "Unknown", bundle: .module)
        }
    }
}

extension RouteKind {
    var label: String {
        switch self {
        case .tunnel: String(localized: "Tunnel", bundle: .module)
        case .bypass: String(localized: "Bypass", bundle: .module)
        case .default: String(localized: "Default route", bundle: .module)
        case .unspecified, .UNRECOGNIZED: String(localized: "Unknown", bundle: .module)
        }
    }
}

extension LogLevel {
    /// Log level names are the same in every language.
    var tag: String {
        switch self {
        case .debug: "DEBUG"
        case .info: "INFO"
        case .warn: "WARN"
        case .error: "ERROR"
        case .unspecified, .UNRECOGNIZED: "INFO"
        }
    }

    var tint: Color {
        switch self {
        case .warn: .orange
        case .error: .red
        case .debug: .secondary
        case .info, .unspecified, .UNRECOGNIZED: .primary
        }
    }
}

/// The rows of the Routes list: what the daemon installed for a profile, or,
/// while it is not connected, the prefixes the profile names itself.
struct RouteRow: Identifiable, Equatable {
    /// The position in the list: the same prefix can be listed more than once.
    let id: Int
    let prefix: String
    let state: RouteState?
    let stateLabel: String?
    /// The daemon's own explanation; empty when there is none.
    let detail: String

    /// `name` maps a profile id to the name shown for it.
    static func rows(for profile: Profile, name: (String) -> String?) -> [RouteRow] {
        if !profile.status.routes.isEmpty {
            return profile.status.routes.enumerated().map { index, route in
                RouteRow(
                    id: index,
                    prefix: route.prefix,
                    state: route.state,
                    stateLabel: route.state.label(shadowedBy: route.shadowedBy.isEmpty ? nil : name(route.shadowedBy) ?? route.shadowedBy),
                    detail: route.detail
                )
            }
        }
        return profile.summary.routes.enumerated().map { RouteRow(id: $0, prefix: $1, state: nil, stateLabel: nil, detail: "") }
    }
}

/// A DNS entry the daemon installed for a profile.
struct DNSRow: Identifiable, Equatable {
    let id: Int
    let servers: String
    let domains: String
    let state: RouteState
    let detail: String

    static func rows(for profile: Profile) -> [DNSRow] {
        profile.status.dns.enumerated().map { index, dns in
            DNSRow(
                id: index,
                servers: dns.servers.joined(separator: ", "),
                // "." is the daemon's way of saying every domain.
                domains: dns.matchDomains.map { $0 == "." ? String(localized: "All domains", bundle: .module) : $0 }.joined(separator: ", "),
                state: dns.state,
                detail: dns.detail
            )
        }
    }
}

extension ImportWarning {
    /// "Line 4: up – removed, a profile cannot run programs".
    var summary: String {
        let place = line > 0 ? String(localized: "Line \(Int(line))", bundle: .module) + ": " : ""
        return place + [directive, message].filter { !$0.isEmpty }.joined(separator: " – ")
    }
}
