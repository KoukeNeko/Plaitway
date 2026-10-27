use plaitway_client::pb;

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Level {
    Debug,
    Info,
    Warn,
    Error,
}

#[derive(Clone, PartialEq, Debug)]
pub struct LogEntry {
    /// Seconds since the epoch.
    pub time: i64,
    pub level: Level,
    pub text: String,
}

impl LogEntry {
    pub fn from_pb(l: &pb::LogLine) -> LogEntry {
        let level = match pb::LogLevel::try_from(l.level).unwrap_or(pb::LogLevel::Unspecified) {
            pb::LogLevel::Debug => Level::Debug,
            pb::LogLevel::Warn => Level::Warn,
            pb::LogLevel::Error => Level::Error,
            _ => Level::Info,
        };
        LogEntry { time: l.time.as_ref().map_or(0, |t| t.seconds), level, text: l.text.clone() }
    }
}

/// The log being watched: a profile's, or the daemon's own when `profile_id` is empty.
#[derive(Clone, PartialEq, Debug)]
pub struct LogView {
    pub profile_id: String,
    pub lines: Vec<LogEntry>,
}

/// The most lines kept; older ones fall off the top.
pub const LOG_KEEP: usize = 1000;

#[derive(Clone, PartialEq, Debug)]
pub struct OwnedRoute {
    pub prefix: String,
    pub owner: String,
    pub via: String,
}

#[derive(Clone, PartialEq, Debug)]
pub struct StaleRoute {
    /// What `remove_stale` takes.
    pub key: String,
    pub prefix: String,
    pub gateway: String,
    pub interface: String,
    pub reason: String,
    pub owned: bool,
}

#[derive(Clone, PartialEq, Debug)]
pub struct JournalLine {
    pub time: i64,
    pub owner: String,
    pub key: String,
    pub state: String,
}

#[derive(Clone, PartialEq, Debug, Default)]
pub struct Diagnostics {
    pub version: String,
    pub privileged: bool,
    pub gateway_v4: String,
    pub interface_v4: String,
    pub interfaces: Vec<String>,
    pub last_change: Option<i64>,
    pub last_change_reason: String,
    pub owned: Vec<OwnedRoute>,
    pub stale: Vec<StaleRoute>,
    pub resolvers: Vec<String>,
    /// Newest first.
    pub journal: Vec<JournalLine>,
}

impl Diagnostics {
    pub fn from_pb(d: pb::Diagnostics) -> Diagnostics {
        let net = d.network.unwrap_or_default();
        let daemon = d.daemon.unwrap_or_default();
        Diagnostics {
            version: daemon.version,
            privileged: daemon.privileged,
            gateway_v4: net.default_gateway_v4,
            interface_v4: net.default_interface_v4,
            interfaces: net.interfaces,
            last_change: net.last_change.map(|t| t.seconds),
            last_change_reason: net.last_change_reason,
            owned: d.owned_routes.into_iter().map(|r| OwnedRoute { prefix: r.prefix, owner: r.owner, via: r.via }).collect(),
            stale: d.stale_routes.into_iter().map(|s| StaleRoute { key: s.key, prefix: s.prefix, gateway: s.gateway, interface: s.interface, reason: s.reason, owned: s.owned }).collect(),
            resolvers: d.resolver_entries,
            journal: d.recent_journal.into_iter().map(|j| JournalLine { time: j.time.map_or(0, |t| t.seconds), owner: j.owner, key: j.key, state: j.state }).collect(),
        }
    }
}
