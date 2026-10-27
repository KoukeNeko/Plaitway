use plaitway_client::pb::{self, profile_event::Event};

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Kind {
    OpenVpn,
    WireGuard,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum State {
    Disconnected,
    Connecting,
    Connected,
    Disconnecting,
    Failed,
    Reconnecting,
    AwaitingCredentials,
}

/// How a connected profile stands on one prefix it asked for.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Standing {
    Holds,
    /// A profile with higher priority owns the prefix.
    Waits,
    Blocked,
    Failed,
    Pending,
}

#[derive(Clone, PartialEq, Debug)]
pub struct Request {
    pub prefix: String,
    pub standing: Standing,
    /// Id of the profile that holds the prefix, when this one waits.
    pub shadowed_by: Option<String>,
}

#[derive(Clone, PartialEq, Debug)]
pub struct Tunnel {
    pub id: String,
    pub name: String,
    pub kind: Kind,
    pub enabled: bool,
    pub state: State,
    pub priority: i32,
    pub error: String,
    /// Prefixes the profile names itself, connected or not.
    pub wanted: usize,
    /// Live only while connected.
    pub requests: Vec<Request>,
}

impl Tunnel {
    pub fn from_pb(p: &pb::Profile) -> Tunnel {
        let state = match pb::ProfileState::try_from(p.state).unwrap_or(pb::ProfileState::Unspecified) {
            pb::ProfileState::Connecting => State::Connecting,
            pb::ProfileState::Connected => State::Connected,
            pb::ProfileState::Disconnecting => State::Disconnecting,
            pb::ProfileState::Failed => State::Failed,
            pb::ProfileState::Reconnecting => State::Reconnecting,
            pb::ProfileState::AwaitingCredentials => State::AwaitingCredentials,
            _ => State::Disconnected,
        };
        let requests = p
            .status
            .iter()
            .flat_map(|s| s.routes.iter())
            .map(|r| Request {
                prefix: r.prefix.clone(),
                standing: match pb::RouteState::try_from(r.state).unwrap_or(pb::RouteState::Unspecified) {
                    pb::RouteState::Installed => Standing::Holds,
                    pb::RouteState::Shadowed => Standing::Waits,
                    pb::RouteState::Blocked => Standing::Blocked,
                    pb::RouteState::Failed => Standing::Failed,
                    _ => Standing::Pending,
                },
                shadowed_by: (!r.shadowed_by.is_empty()).then(|| r.shadowed_by.clone()),
            })
            .collect();
        Tunnel {
            id: p.id.clone(),
            name: p.name.clone(),
            kind: if p.kind == pb::ProfileKind::Wireguard as i32 { Kind::WireGuard } else { Kind::OpenVpn },
            enabled: p.desired_enabled,
            state,
            priority: p.settings.as_ref().map_or(0, |s| s.priority),
            error: p.last_error.clone(),
            wanted: p.summary.as_ref().map_or(0, |s| s.routes.len()),
            requests,
        }
    }

    pub fn connected(&self) -> bool {
        self.state == State::Connected
    }

    /// (prefixes held, prefixes the profile asks for).
    pub fn holds(&self) -> (usize, usize) {
        (self.requests.iter().filter(|r| r.standing == Standing::Holds).count(), self.wanted.max(self.requests.len()))
    }
}

/// One prefix and every connected profile asking for it.
#[derive(Clone, PartialEq, Debug)]
pub struct RouteRow {
    pub prefix: String,
    /// Index into `Model::tunnels`.
    pub holder: Option<usize>,
    /// (tunnel index, standing), in priority order.
    pub requests: Vec<(usize, Standing)>,
}

#[derive(Clone, Default, PartialEq, Debug)]
pub struct Model {
    /// Highest priority first.
    pub tunnels: Vec<Tunnel>,
}

impl Model {
    pub fn apply(&mut self, e: pb::ProfileEvent) {
        match e.event {
            Some(Event::Snapshot(s)) => self.tunnels = s.profiles.iter().map(Tunnel::from_pb).collect(),
            Some(Event::Changed(p)) => {
                let t = Tunnel::from_pb(&p);
                match self.tunnels.iter_mut().find(|x| x.id == t.id) {
                    Some(x) => *x = t,
                    None => self.tunnels.push(t),
                }
            }
            Some(Event::Removed(id)) => self.tunnels.retain(|t| t.id != id),
            None => return,
        }
        self.tunnels.sort_by(|a, b| a.priority.cmp(&b.priority).then_with(|| a.id.cmp(&b.id)));
    }

    pub fn ids(&self) -> Vec<String> {
        self.tunnels.iter().map(|t| t.id.clone()).collect()
    }

    pub fn index(&self, id: &str) -> Option<usize> {
        self.tunnels.iter().position(|t| t.id == id)
    }

    /// Every prefix a connected profile asks for. The default routes come first, the rest in the
    /// order the profiles (by priority) name them.
    pub fn routes(&self) -> Vec<RouteRow> {
        let mut rows: Vec<RouteRow> = vec![];
        for (i, t) in self.tunnels.iter().enumerate() {
            for r in &t.requests {
                let at = match rows.iter().position(|row| row.prefix == r.prefix) {
                    Some(at) => at,
                    None => {
                        rows.push(RouteRow { prefix: r.prefix.clone(), holder: None, requests: vec![] });
                        rows.len() - 1
                    }
                };
                rows[at].requests.push((i, r.standing));
                if r.standing == Standing::Holds {
                    rows[at].holder = Some(i);
                }
            }
        }
        rows.sort_by_key(|r| !(r.prefix == "0.0.0.0/0" || r.prefix == "::/0")); // stable: defaults first
        rows
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn profile(id: &str, priority: i32, state: pb::ProfileState, routes: &[(&str, pb::RouteState, &str)]) -> pb::Profile {
        pb::Profile {
            id: id.into(),
            name: id.to_uppercase(),
            state: state as i32,
            desired_enabled: state != pb::ProfileState::Disconnected,
            settings: Some(pb::ProfileSettings { priority, ..Default::default() }),
            summary: Some(pb::ProfileSummary { routes: routes.iter().map(|r| r.0.to_string()).collect(), ..Default::default() }),
            status: (state == pb::ProfileState::Connected).then(|| pb::TunnelStatus {
                routes: routes.iter().map(|(p, s, by)| pb::RouteStatus { prefix: p.to_string(), state: *s as i32, shadowed_by: by.to_string(), ..Default::default() }).collect(),
                ..Default::default()
            }),
            ..Default::default()
        }
    }

    fn snapshot(ps: Vec<pb::Profile>) -> pb::ProfileEvent {
        pb::ProfileEvent { event: Some(Event::Snapshot(pb::ProfileSnapshot { profiles: ps })) }
    }

    fn two() -> Model {
        use pb::{ProfileState::*, RouteState::*};
        let mut m = Model::default();
        m.apply(snapshot(vec![
            profile("office", 1, Connected, &[("10.20.0.0/16", Installed, ""), ("0.0.0.0/0", Shadowed, "home")]),
            profile("home", 0, Connected, &[("192.168.1.0/24", Installed, ""), ("0.0.0.0/0", Installed, "")]),
        ]));
        m
    }

    #[test]
    fn a_snapshot_is_sorted_by_priority() {
        assert_eq!(two().ids(), ["home", "office"]);
    }

    #[test]
    fn routes_group_requests_by_prefix_with_defaults_first() {
        let m = two();
        let rows = m.routes();
        assert_eq!(rows.iter().map(|r| r.prefix.as_str()).collect::<Vec<_>>(), ["0.0.0.0/0", "192.168.1.0/24", "10.20.0.0/16"]);
        assert_eq!(rows[0].holder, Some(0));
        assert_eq!(rows[0].requests, [(0, Standing::Holds), (1, Standing::Waits)]);
        assert_eq!(rows[2].holder, Some(1));
    }

    #[test]
    fn holds_counts_what_a_profile_won() {
        let m = two();
        assert_eq!(m.tunnels[0].holds(), (2, 2));
        assert_eq!(m.tunnels[1].holds(), (1, 2));
    }

    #[test]
    fn a_disconnected_profile_asks_for_nothing_but_still_names_its_routes() {
        use pb::{ProfileState::*, RouteState::*};
        let mut m = two();
        m.apply(pb::ProfileEvent { event: Some(Event::Changed(profile("home", 0, Disconnected, &[("0.0.0.0/0", Installed, "")]))) });
        assert!(m.tunnels[0].requests.is_empty());
        assert_eq!(m.tunnels[0].wanted, 1);
        assert!(!m.tunnels[0].enabled);
        assert_eq!(m.routes().iter().find(|r| r.prefix == "0.0.0.0/0").unwrap().holder, None, "the route waits for the other tunnel's next update");
    }

    #[test]
    fn changed_adds_or_replaces_and_removed_deletes() {
        use pb::{ProfileState::*, RouteState::*};
        let mut m = two();
        m.apply(pb::ProfileEvent { event: Some(Event::Changed(profile("lab", 0, Connected, &[("172.16.8.0/24", Installed, "")]))) });
        assert_eq!(m.ids(), ["home", "lab", "office"], "equal priority falls back to the id");
        m.apply(pb::ProfileEvent { event: Some(Event::Removed("home".into())) });
        assert_eq!(m.ids(), ["lab", "office"]);
        m.apply(pb::ProfileEvent { event: None });
        assert_eq!(m.tunnels.len(), 2);
    }
}

