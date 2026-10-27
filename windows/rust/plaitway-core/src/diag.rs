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

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum RouteKind {
    Tunnel,
    /// Keeps a tunnel's endpoint reachable outside the tunnel.
    Bypass,
    /// A default route, whole or as the two halves the daemon installs.
    Default,
}

#[derive(Clone, PartialEq, Debug)]
pub struct OwnedRoute {
    pub prefix: String,
    /// A profile id.
    pub owner: String,
    pub kind: RouteKind,
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

/// One owner's two half-default routes are shown as the one default route they stand for.
fn merge_halves(mut routes: Vec<OwnedRoute>) -> Vec<OwnedRoute> {
    for (low, high, whole) in [("0.0.0.0/1", "128.0.0.0/1", "0.0.0.0/0"), ("::/1", "8000::/1", "::/0")] {
        let owners: Vec<String> = routes.iter().filter(|r| r.prefix == low).map(|r| r.owner.clone()).collect();
        for owner in owners {
            if let Some(b) = routes.iter().position(|r| r.prefix == high && r.owner == owner) {
                routes.remove(b);
                if let Some(a) = routes.iter_mut().find(|r| r.prefix == low && r.owner == owner) {
                    a.prefix = whole.into();
                    a.kind = RouteKind::Default;
                }
            }
        }
    }
    routes
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
            owned: merge_halves(d.owned_routes.into_iter().map(|r| OwnedRoute { prefix: r.prefix, owner: r.owner, kind: match pb::RouteKind::try_from(r.kind) { Ok(pb::RouteKind::Bypass) => RouteKind::Bypass, Ok(pb::RouteKind::Default) => RouteKind::Default, _ => RouteKind::Tunnel }, via: r.via }).collect()),
            stale: d.stale_routes.into_iter().map(|s| StaleRoute { key: s.key, prefix: s.prefix, gateway: s.gateway, interface: s.interface, reason: s.reason, owned: s.owned }).collect(),
            resolvers: d.resolver_entries,
            journal: d.recent_journal.into_iter().map(|j| JournalLine { time: j.time.map_or(0, |t| t.seconds), owner: j.owner, key: j.key, state: j.state }).collect(),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn route(prefix: &str, owner: &str) -> OwnedRoute {
        OwnedRoute { prefix: prefix.into(), owner: owner.into(), kind: RouteKind::Default, via: "utun1".into() }
    }

    #[test]
    fn an_owners_two_halves_become_one_default_route() {
        let merged = merge_halves(vec![route("0.0.0.0/1", "a"), route("10.0.0.0/8", "a"), route("128.0.0.0/1", "a"), route("::/1", "a"), route("8000::/1", "a")]);
        assert_eq!(merged.iter().map(|r| r.prefix.as_str()).collect::<Vec<_>>(), ["0.0.0.0/0", "10.0.0.0/8", "::/0"]);
    }

    #[test]
    fn halves_of_different_owners_stay_apart() {
        let merged = merge_halves(vec![route("0.0.0.0/1", "a"), route("128.0.0.0/1", "b")]);
        assert_eq!(merged.len(), 2);
        assert_eq!(merged[0].prefix, "0.0.0.0/1");
    }

    #[test]
    fn a_lone_half_is_left_alone() {
        assert_eq!(merge_halves(vec![route("0.0.0.0/1", "a")])[0].prefix, "0.0.0.0/1");
    }
}

