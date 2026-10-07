import PlaitwayAPI

// The generated types the UI works with, under the names it uses.
public typealias Profile = Plaitway_V1_Profile
public typealias ProfileState = Plaitway_V1_ProfileState
public typealias ProfileKind = Plaitway_V1_ProfileKind
public typealias ProfileSettings = Plaitway_V1_ProfileSettings
public typealias OnDemandRules = Plaitway_V1_OnDemandRules
public typealias ProfileSummary = Plaitway_V1_ProfileSummary
public typealias TunnelMode = Plaitway_V1_TunnelMode
public typealias TunnelStatus = Plaitway_V1_TunnelStatus
public typealias CredentialKind = Plaitway_V1_CredentialKind
public typealias RouteState = Plaitway_V1_RouteState
public typealias RouteKind = Plaitway_V1_RouteKind
public typealias RouteStatus = Plaitway_V1_RouteStatus
public typealias DnsStatus = Plaitway_V1_DnsStatus
public typealias DaemonInfo = Plaitway_V1_DaemonInfo
public typealias EngineInfo = Plaitway_V1_EngineInfo
public typealias Diagnostics = Plaitway_V1_Diagnostics
public typealias NetworkInfo = Plaitway_V1_NetworkInfo
public typealias OwnedRoute = Plaitway_V1_OwnedRoute
public typealias StaleRoute = Plaitway_V1_StaleRoute
public typealias JournalEntry = Plaitway_V1_JournalEntry
public typealias LogLine = Plaitway_V1_LogLine
public typealias LogLevel = Plaitway_V1_LogLevel
public typealias ImportWarning = Plaitway_V1_ImportWarning

extension OnDemandRules {
    /// On-demand activation is on when either network type is checked.
    public var isActive: Bool { ethernet || wifi }
}
