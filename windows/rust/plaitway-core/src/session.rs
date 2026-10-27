//! Keeps a `Model` current from the daemon on a background thread, and runs commands on it.
//! What the UI is looking at (a log, the diagnostics) is told to the session, which keeps it
//! current and picks it up again after a reconnect.

use std::{
    sync::{Arc, Mutex},
    time::Duration,
};

use plaitway_client::{connect, pb, Channel, DaemonServiceClient, Streaming};
use tokio::sync::mpsc;

use crate::{Diagnostics, LogEntry, LogView, Model, LOG_KEEP};

#[derive(Clone, PartialEq, Debug)]
pub enum Link {
    Connecting,
    Online,
    /// The daemon could not be reached; it is tried again every second.
    Offline(String),
}

/// A copy of everything the UI draws from.
#[derive(Clone, Debug)]
pub struct View {
    pub link: Link,
    pub model: Model,
    /// The log being watched, if any.
    pub log: Option<LogView>,
    /// Kept current while `watch_diagnostics(true)`.
    pub diagnostics: Option<Diagnostics>,
    /// The last command the daemon refused or could not receive; cleared by the next one that works.
    pub error: Option<String>,
}

enum Cmd {
    SetEnabled(String, bool),
    Reorder(Vec<String>),
    /// None stops watching.
    WatchLogs(Option<String>),
    WatchDiagnostics(bool),
    Resync,
    RemoveStale(String),
}

/// What the UI wants to see; kept across reconnects.
#[derive(Default)]
struct Want {
    logs: Option<String>,
    diagnostics: bool,
}

pub struct Session {
    shared: Arc<Mutex<View>>,
    cmds: mpsc::UnboundedSender<Cmd>,
}

type Wake = Arc<dyn Fn() + Send + Sync>;
type Client = DaemonServiceClient<Channel>;
type LogStream = Streaming<pb::LogLine>;

impl Session {
    /// Connect to `pipe` and keep trying. `wake` is called after every change to the view, from the
    /// background thread; it must be cheap and must not take locks the UI holds while reading.
    pub fn start(pipe: &str, wake: impl Fn() + Send + Sync + 'static) -> Session {
        let shared = Arc::new(Mutex::new(View { link: Link::Connecting, model: Model::default(), log: None, diagnostics: None, error: None }));
        let (cmds, rx) = mpsc::unbounded_channel();
        let (pipe, wake, s) = (pipe.to_string(), Arc::new(wake) as Wake, shared.clone());
        std::thread::Builder::new()
            .name("plaitway-session".into())
            .spawn(move || {
                let rt = tokio::runtime::Builder::new_current_thread().enable_all().build().unwrap();
                rt.block_on(run(pipe, s, wake, rx));
            })
            .expect("start the session thread");
        Session { shared, cmds }
    }

    pub fn view(&self) -> View {
        self.shared.lock().unwrap().clone()
    }

    pub fn set_enabled(&self, id: &str, enabled: bool) {
        let _ = self.cmds.send(Cmd::SetEnabled(id.into(), enabled));
    }

    /// `ids` lists every profile, highest priority first.
    pub fn reorder(&self, ids: Vec<String>) {
        let _ = self.cmds.send(Cmd::Reorder(ids));
    }

    /// Show the log of profile `id`, or the daemon's own for an empty id; None stops. Asking for
    /// the log that is already shown changes nothing.
    pub fn watch_logs(&self, id: Option<&str>) {
        let _ = self.cmds.send(Cmd::WatchLogs(id.map(String::from)));
    }

    /// Keep `View::diagnostics` current while true: fetched now and after every profile change.
    pub fn watch_diagnostics(&self, on: bool) {
        let _ = self.cmds.send(Cmd::WatchDiagnostics(on));
    }

    pub fn resync(&self) {
        let _ = self.cmds.send(Cmd::Resync);
    }

    pub fn remove_stale(&self, key: &str) {
        let _ = self.cmds.send(Cmd::RemoveStale(key.into()));
    }
}

fn update(s: &Mutex<View>, wake: &Wake, f: impl FnOnce(&mut View)) {
    f(&mut s.lock().unwrap());
    wake();
}

/// What was on screen can no longer be trusted once the daemon is gone.
fn offline(v: &mut View, why: String) {
    v.link = Link::Offline(why);
    v.model = Model::default();
    v.diagnostics = None;
    if let Some(l) = &mut v.log {
        l.lines.clear();
    }
}

async fn run(pipe: String, s: Arc<Mutex<View>>, wake: Wake, mut cmds: mpsc::UnboundedReceiver<Cmd>) {
    let mut want = Want::default();
    loop {
        update(&s, &wake, |v| v.link = Link::Connecting);
        match connect(&pipe).await {
            Ok(client) => {
                let end = serve(client, &s, &wake, &mut cmds, &mut want).await;
                update(&s, &wake, |v| offline(v, end));
            }
            Err(e) => update(&s, &wake, |v| offline(v, e.to_string())),
        }
        // Actions sent while offline would act on a state the user can no longer see; what is being
        // looked at is remembered.
        while let Ok(c) = cmds.try_recv() {
            absorb(c, &mut want);
        }
        tokio::time::sleep(Duration::from_secs(1)).await;
    }
}

fn absorb(c: Cmd, want: &mut Want) {
    match c {
        Cmd::WatchLogs(id) => want.logs = id,
        Cmd::WatchDiagnostics(on) => want.diagnostics = on,
        _ => {}
    }
}

/// Watch profiles and run commands until the connection fails; returns why it ended.
async fn serve(mut client: Client, s: &Mutex<View>, wake: &Wake, cmds: &mut mpsc::UnboundedReceiver<Cmd>, want: &mut Want) -> String {
    let mut profiles = match client.watch_profiles(pb::WatchProfilesRequest {}).await {
        Ok(r) => r.into_inner(),
        Err(e) => return e.message().to_string(),
    };
    let mut logs: Option<LogStream> = None;
    // The log is started from `want`, so a reconnect picks up where the user is looking.
    let mut shown: Option<String> = None;
    if want.diagnostics {
        refresh(&mut client, s, wake).await;
    }
    loop {
        if shown != want.logs {
            shown = want.logs.clone();
            logs = None;
            update(s, wake, |v| v.log = shown.clone().map(|id| LogView { profile_id: id, lines: vec![] }));
            if let Some(id) = &shown {
                match client.watch_logs(pb::WatchLogsRequest { profile_id: id.clone(), tail_lines: 200 }).await {
                    Ok(r) => logs = Some(r.into_inner()),
                    Err(e) => return e.message().to_string(),
                }
            }
        }
        tokio::select! {
            m = profiles.message() => match m {
                Ok(Some(ev)) => {
                    update(s, wake, |v| {
                        if matches!(ev.event, Some(pb::profile_event::Event::Snapshot(_))) {
                            v.link = Link::Online;
                        }
                        v.model.apply(ev);
                    });
                    if want.diagnostics {
                        refresh(&mut client, s, wake).await;
                    }
                }
                Ok(None) => return "the daemon closed the connection".into(),
                Err(e) => return e.message().to_string(),
            },
            m = async { match &mut logs { Some(l) => l.message().await, None => std::future::pending().await } } => match m {
                Ok(Some(line)) => update(s, wake, |v| {
                    if let Some(l) = &mut v.log {
                        l.lines.push(LogEntry::from_pb(&line));
                        let extra = l.lines.len().saturating_sub(LOG_KEEP);
                        l.lines.drain(..extra);
                    }
                }),
                Ok(None) => logs = None,
                Err(e) => return e.message().to_string(),
            },
            Some(c) = cmds.recv() => {
                let r = match c {
                    Cmd::SetEnabled(id, enabled) => client.set_profile_enabled(pb::SetProfileEnabledRequest { id, enabled }).await.map(|_| ()),
                    Cmd::Reorder(ids) => client.reorder_profiles(pb::ReorderProfilesRequest { ids }).await.map(|_| ()),
                    Cmd::Resync => client.resync(pb::ResyncRequest {}).await.map(|_| ()),
                    Cmd::RemoveStale(key) => client.remove_stale_route(pb::RemoveStaleRouteRequest { key }).await.map(|_| ()),
                    c @ (Cmd::WatchLogs(_) | Cmd::WatchDiagnostics(_)) => {
                        absorb(c, want);
                        if want.diagnostics {
                            refresh(&mut client, s, wake).await;
                        } else {
                            update(s, wake, |v| v.diagnostics = None);
                        }
                        continue;
                    }
                };
                update(s, wake, |v| v.error = r.err().map(|e| e.message().to_string()));
                if want.diagnostics {
                    refresh(&mut client, s, wake).await;
                }
            }
        }
    }
}

/// Fetch the diagnostics; a failure is shown as the error and the old ones stay.
async fn refresh(client: &mut Client, s: &Mutex<View>, wake: &Wake) {
    match client.get_diagnostics(pb::GetDiagnosticsRequest {}).await {
        Ok(r) => update(s, wake, |v| v.diagnostics = Some(Diagnostics::from_pb(r.into_inner()))),
        Err(e) => update(s, wake, |v| v.error = Some(e.message().to_string())),
    }
}

