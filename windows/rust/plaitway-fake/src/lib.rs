//! A fake `plaitwayd`. It keeps profiles in memory and decides which connected profile holds
//! each prefix the way the real reconciler does: the lowest priority number wins, the others
//! are shadowed. Serving happens on a named pipe, so the client under test is the real one.

use std::{
    collections::VecDeque,
    io,
    pin::Pin,
    sync::{Arc, Mutex},
    task::{Context, Poll},
    time::{Duration, SystemTime, UNIX_EPOCH},
};

use plaitway_client::pb::{daemon_service_server::*, *};
use tokio::{
    io::{AsyncRead, AsyncWrite, ReadBuf},
    net::windows::named_pipe::{NamedPipeServer, ServerOptions},
    sync::broadcast,
};
use tonic::{transport::server::Connected, transport::Server, Request, Response, Status};

#[derive(Clone, Copy)]
pub struct Seed {
    pub id: &'static str,
    pub name: &'static str,
    pub kind: ProfileKind,
    pub routes: &'static [&'static str],
    pub connected: bool,
}

/// Home, Office and Lab, the same three the design prototype uses.
pub const DEMO: [Seed; 3] = [
    Seed { id: "home", name: "Home", kind: ProfileKind::Wireguard, routes: &["0.0.0.0/0", "192.168.1.0/24", "10.8.0.0/24"], connected: true },
    Seed { id: "office", name: "Office", kind: ProfileKind::Openvpn, routes: &["0.0.0.0/0", "192.168.1.0/24", "10.8.0.0/24", "10.20.0.0/16"], connected: true },
    Seed { id: "lab", name: "Lab", kind: ProfileKind::Wireguard, routes: &["192.168.1.0/24", "172.16.8.0/24"], connected: true },
];

struct P {
    id: String,
    name: String,
    kind: ProfileKind,
    routes: Vec<String>,
    enabled: bool,
    state: ProfileState,
    priority: i32,
    since: Option<prost_types::Timestamp>,
}

struct Shared {
    profiles: Mutex<Vec<P>>,
    events: broadcast::Sender<ProfileEvent>,
    logs: Mutex<VecDeque<LogLine>>,
    log_tx: broadcast::Sender<LogLine>,
    stale: Mutex<Vec<StaleRoute>>,
    journal: Mutex<Vec<JournalEntry>>,
    /// How long a connection takes to come up.
    connect_delay: Duration,
}

const LOG_KEEP: usize = 500;

impl Shared {
    /// One log line; `profile` is empty for the daemon's own log.
    fn log(&self, profile: &str, level: LogLevel, text: impl Into<String>) {
        let line = LogLine { time: Some(now()), level: level as i32, profile_id: profile.into(), text: text.into() };
        let mut logs = self.logs.lock().unwrap();
        if logs.len() == LOG_KEEP {
            logs.pop_front();
        }
        logs.push_back(line.clone());
        let _ = self.log_tx.send(line);
    }

    fn journal(&self, owner: &str, key: &str, state: &str) {
        self.journal.lock().unwrap().push(JournalEntry { time: Some(now()), owner: owner.into(), kind: "route".into(), key: key.into(), state: state.into() });
    }

    fn diagnostics(&self) -> Diagnostics {
        let view = self.view();
        let owned = view
            .iter()
            .flat_map(|p| p.status.iter().flat_map(move |s| s.routes.iter().filter(|r| r.state == RouteState::Installed as i32).map(move |r| OwnedRoute { prefix: r.prefix.clone(), owner: p.id.clone(), kind: RouteKind::Tunnel as i32, via: format!("plaitway-{}", p.id), state: r.state })))
            .collect();
        let journal = self.journal.lock().unwrap();
        Diagnostics {
            daemon: Some(DaemonInfo { version: "fake".into(), protocol_version: 1, privileged: true, engines: vec![] }),
            network: Some(NetworkInfo {
                default_gateway_v4: "192.168.1.1".into(),
                default_interface_v4: "Ethernet".into(),
                interfaces: vec!["Ethernet".into(), "Wi-Fi".into()],
                last_change: Some(now()),
                last_change_reason: "heartbeat".into(),
                ..Default::default()
            }),
            owned_routes: owned,
            stale_routes: self.stale.lock().unwrap().clone(),
            resolver_entries: vec!["home.example.net -> 10.8.0.1".into()],
            recent_journal: journal.iter().rev().take(20).cloned().collect(),
        }
    }

    /// Every profile as the daemon would report it, with route states decided by priority.
    fn view(&self) -> Vec<Profile> {
        let ps = self.profiles.lock().unwrap();
        let mut order: Vec<&P> = ps.iter().filter(|p| p.state == ProfileState::Connected).collect();
        order.sort_by_key(|p| p.priority);
        let mut owner: Vec<(&str, &str)> = vec![]; // prefix -> profile id
        for p in &order {
            for r in &p.routes {
                if !owner.iter().any(|(pre, _)| pre == r) {
                    owner.push((r, &p.id));
                }
            }
        }
        let mut out: Vec<Profile> = ps
            .iter()
            .map(|p| {
                let status = (p.state == ProfileState::Connected).then(|| TunnelStatus {
                    interface_name: format!("plaitway-{}", p.id),
                    remote: "vpn.example.net:51820".into(),
                    connected_since: p.since.clone(),
                    routes: p
                        .routes
                        .iter()
                        .map(|r| {
                            let holder = owner.iter().find(|(pre, _)| pre == r).map(|(_, id)| *id).unwrap();
                            if holder == p.id {
                                RouteStatus { prefix: r.clone(), state: RouteState::Installed as i32, ..Default::default() }
                            } else {
                                RouteStatus { prefix: r.clone(), state: RouteState::Shadowed as i32, shadowed_by: holder.to_string(), detail: "a profile with higher priority owns it".into() }
                            }
                        })
                        .collect(),
                    ..Default::default()
                });
                Profile {
                    id: p.id.clone(),
                    name: p.name.clone(),
                    kind: p.kind as i32,
                    desired_enabled: p.enabled,
                    state: p.state as i32,
                    settings: Some(ProfileSettings { priority: p.priority, ..Default::default() }),
                    summary: Some(ProfileSummary { routes: p.routes.clone(), ..Default::default() }),
                    status,
                    ..Default::default()
                }
            })
            .collect();
        out.sort_by_key(|p| p.settings.as_ref().unwrap().priority);
        out
    }

    /// Tell every watcher about the current state. Routes of one profile depend on the others, so all are sent.
    fn publish(&self) {
        for p in self.view() {
            let _ = self.events.send(ProfileEvent { event: Some(profile_event::Event::Changed(p)) });
        }
    }
}

pub struct Fake {
    s: Arc<Shared>,
}

impl Fake {
    pub fn new(seeds: &[Seed], connect_delay: Duration) -> Self {
        let profiles = seeds
            .iter()
            .enumerate()
            .map(|(i, s)| P {
                id: s.id.into(),
                name: s.name.into(),
                kind: s.kind,
                routes: s.routes.iter().map(|r| r.to_string()).collect(),
                enabled: s.connected,
                state: if s.connected { ProfileState::Connected } else { ProfileState::Disconnected },
                priority: i as i32,
                since: s.connected.then(now),
            })
            .collect();
        let fake = Fake { s: Arc::new(Shared { profiles: Mutex::new(profiles), events: broadcast::channel(256).0, logs: Mutex::new(VecDeque::new()), log_tx: broadcast::channel(256).0, stale: Mutex::new(vec![StaleRoute { key: "stale-1".into(), prefix: "10.99.0.0/16".into(), gateway: "10.8.0.1".into(), interface: "plaitway-old".into(), reason: "left by a crashed daemon".into(), owned: true }]), journal: Mutex::new(vec![]), connect_delay }) };
        fake.s.log("", LogLevel::Info, "plaitwayd started");
        for s in seeds.iter().filter(|s| s.connected) {
            fake.s.log(s.id, LogLevel::Info, "tunnel up");
            for r in s.routes {
                fake.s.journal(s.id, r, "applied");
            }
        }
        fake
    }
}

fn now() -> prost_types::Timestamp {
    let d = SystemTime::now().duration_since(UNIX_EPOCH).unwrap();
    prost_types::Timestamp { seconds: d.as_secs() as i64, nanos: d.subsec_nanos() as i32 }
}

type Rsp<T> = Result<Response<T>, Status>;

fn no<T>() -> Rsp<T> {
    Err(Status::unimplemented("the fake daemon does not do this"))
}

#[tonic::async_trait]
impl DaemonService for Fake {
    async fn get_daemon_info(&self, _: Request<GetDaemonInfoRequest>) -> Rsp<DaemonInfo> {
        Ok(Response::new(DaemonInfo { version: "fake".into(), protocol_version: 1, privileged: true, engines: vec![] }))
    }
    async fn get_diagnostics(&self, _: Request<GetDiagnosticsRequest>) -> Rsp<Diagnostics> {
        Ok(Response::new(self.s.diagnostics()))
    }
    async fn list_profiles(&self, _: Request<ListProfilesRequest>) -> Rsp<ListProfilesResponse> {
        Ok(Response::new(ListProfilesResponse { profiles: self.s.view() }))
    }
    async fn import_profile(&self, _: Request<ImportProfileRequest>) -> Rsp<ImportProfileResponse> {
        no()
    }
    async fn update_profile(&self, _: Request<UpdateProfileRequest>) -> Rsp<Profile> {
        no()
    }
    async fn get_profile_content(&self, _: Request<GetProfileContentRequest>) -> Rsp<GetProfileContentResponse> {
        no()
    }
    async fn update_profile_content(&self, _: Request<UpdateProfileContentRequest>) -> Rsp<ImportProfileResponse> {
        no()
    }
    async fn delete_profile(&self, _: Request<DeleteProfileRequest>) -> Rsp<DeleteProfileResponse> {
        no()
    }
    async fn reorder_profiles(&self, r: Request<ReorderProfilesRequest>) -> Rsp<ListProfilesResponse> {
        let ids = r.into_inner().ids;
        {
            let mut ps = self.s.profiles.lock().unwrap();
            if ids.len() != ps.len() || !ps.iter().all(|p| ids.contains(&p.id)) {
                return Err(Status::invalid_argument("ids must list every profile once"));
            }
            for p in ps.iter_mut() {
                p.priority = ids.iter().position(|i| *i == p.id).unwrap() as i32;
            }
        }
        self.s.log("", LogLevel::Info, format!("priority order is now {}", ids.join(", ")));
        self.s.publish();
        Ok(Response::new(ListProfilesResponse { profiles: self.s.view() }))
    }
    async fn set_profile_enabled(&self, r: Request<SetProfileEnabledRequest>) -> Rsp<Profile> {
        let r = r.into_inner();
        {
            let mut ps = self.s.profiles.lock().unwrap();
            let p = ps.iter_mut().find(|p| p.id == r.id).ok_or_else(|| Status::not_found("no such profile"))?;
            p.enabled = r.enabled;
            if r.enabled {
                p.state = ProfileState::Connecting;
            } else {
                p.state = ProfileState::Disconnected;
                p.since = None;
            }
        }
        if r.enabled {
            self.s.log(&r.id, LogLevel::Info, "connecting");
        } else {
            self.s.log(&r.id, LogLevel::Info, "disconnected");
            let routes = self.s.profiles.lock().unwrap().iter().find(|p| p.id == r.id).map(|p| p.routes.clone()).unwrap_or_default();
            for route in routes {
                self.s.journal(&r.id, &route, "removed");
            }
        }
        self.s.publish();
        if r.enabled {
            let (s, id) = (self.s.clone(), r.id.clone());
            tokio::spawn(async move {
                tokio::time::sleep(s.connect_delay).await;
                let routes = {
                    let mut ps = s.profiles.lock().unwrap();
                    match ps.iter_mut().find(|p| p.id == id && p.enabled && p.state == ProfileState::Connecting) {
                        Some(p) => {
                            p.state = ProfileState::Connected;
                            p.since = Some(now());
                            p.routes.clone()
                        }
                        None => vec![],
                    }
                };
                if !routes.is_empty() {
                    s.log(&id, LogLevel::Info, format!("tunnel up, {} routes", routes.len()));
                    for r in &routes {
                        s.journal(&id, r, "applied");
                    }
                }
                s.publish();
            });
        }
        let p = self.s.view().into_iter().find(|p| p.id == r.id).unwrap();
        Ok(Response::new(p))
    }
    async fn provide_credentials(&self, _: Request<ProvideCredentialsRequest>) -> Rsp<Profile> {
        no()
    }
    type WatchProfilesStream = Pin<Box<dyn tokio_stream::Stream<Item = Result<ProfileEvent, Status>> + Send>>;
    async fn watch_profiles(&self, _: Request<WatchProfilesRequest>) -> Rsp<Self::WatchProfilesStream> {
        // Subscribe before taking the snapshot so no change falls between the two.
        let mut rx = self.s.events.subscribe();
        let snapshot = ProfileEvent { event: Some(profile_event::Event::Snapshot(ProfileSnapshot { profiles: self.s.view() })) };
        let stream = async_stream::stream! {
            yield Ok(snapshot);
            loop {
                match rx.recv().await {
                    Ok(e) => yield Ok(e),
                    Err(broadcast::error::RecvError::Lagged(_)) => continue,
                    Err(_) => break,
                }
            }
        };
        Ok(Response::new(Box::pin(stream)))
    }
    type WatchLogsStream = Pin<Box<dyn tokio_stream::Stream<Item = Result<LogLine, Status>> + Send>>;
    async fn watch_logs(&self, r: Request<WatchLogsRequest>) -> Rsp<Self::WatchLogsStream> {
        let r = r.into_inner();
        let keep = if r.tail_lines > 0 { r.tail_lines as usize } else { 200 };
        let id = r.profile_id;
        // Subscribe before copying the tail so no line falls between the two.
        let mut rx = self.s.log_tx.subscribe();
        let mut tail: Vec<LogLine> = self.s.logs.lock().unwrap().iter().filter(|l| l.profile_id == id).cloned().collect();
        tail.drain(..tail.len().saturating_sub(keep));
        let stream = async_stream::stream! {
            for l in tail {
                yield Ok(l);
            }
            loop {
                match rx.recv().await {
                    Ok(l) if l.profile_id == id => yield Ok(l),
                    Ok(_) | Err(broadcast::error::RecvError::Lagged(_)) => continue,
                    Err(_) => break,
                }
            }
        };
        Ok(Response::new(Box::pin(stream)))
    }
    async fn resync(&self, _: Request<ResyncRequest>) -> Rsp<ResyncResponse> {
        self.s.log("", LogLevel::Info, "resync: routes and DNS rebuilt");
        Ok(Response::new(ResyncResponse {}))
    }
    async fn remove_stale_route(&self, r: Request<RemoveStaleRouteRequest>) -> Rsp<RemoveStaleRouteResponse> {
        let key = r.into_inner().key;
        let mut stale = self.s.stale.lock().unwrap();
        let before = stale.len();
        stale.retain(|s| s.key != key);
        if stale.len() == before {
            return Err(Status::not_found("no such stale route"));
        }
        drop(stale);
        self.s.log("", LogLevel::Warn, format!("removed stale route {key}"));
        Ok(Response::new(RemoveStaleRouteResponse {}))
    }
}

struct Conn(NamedPipeServer);

impl Connected for Conn {
    type ConnectInfo = ();
    fn connect_info(&self) -> Self::ConnectInfo {}
}

impl AsyncRead for Conn {
    fn poll_read(mut self: Pin<&mut Self>, cx: &mut Context<'_>, buf: &mut ReadBuf<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.0).poll_read(cx, buf)
    }
}

impl AsyncWrite for Conn {
    fn poll_write(mut self: Pin<&mut Self>, cx: &mut Context<'_>, buf: &[u8]) -> Poll<io::Result<usize>> {
        Pin::new(&mut self.0).poll_write(cx, buf)
    }
    fn poll_flush(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.0).poll_flush(cx)
    }
    fn poll_shutdown(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.0).poll_shutdown(cx)
    }
}

/// Serve `fake` on `pipe` until the future is dropped. The pipe exists before this returns its future's first poll
/// completes, so a client that connects right after `spawn` is not refused: call it from a task and wait for `ready`.
pub async fn serve(pipe: &str, fake: Fake, ready: tokio::sync::oneshot::Sender<()>) -> Result<(), tonic::transport::Error> {
    let name = pipe.to_string();
    let mut first = ServerOptions::new().first_pipe_instance(true).create(&name).expect("create the pipe");
    let _ = ready.send(());
    let incoming = async_stream::stream! {
        loop {
            if first.connect().await.is_err() { break; }
            let conn = Conn(first);
            first = ServerOptions::new().create(&name).expect("create the next pipe instance");
            yield Ok::<_, io::Error>(conn);
        }
    };
    Server::builder().add_service(DaemonServiceServer::new(fake)).serve_with_incoming(incoming).await
}



