//! Keeps a `Model` current from the daemon on a background thread, and runs commands on it.
//! What the UI is looking at (a log, the diagnostics) is told to the session, which keeps it
//! current and picks it up again after a reconnect.

use std::{
    sync::{Arc, Mutex},
    time::Duration,
};

use plaitway_client::{connect, pb, Channel, DaemonServiceClient, Streaming};
use tokio::sync::mpsc;

use crate::{CredentialKind, Diagnostics, LogEntry, LogView, Model, Settings, LOG_KEEP};

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
    /// The text of the profile being edited; `text` is None while it is being fetched.
    pub content: Option<ContentView>,
    /// The last command the daemon refused or could not receive; cleared by the next one that works.
    pub error: Option<String>,
    /// What the last command that worked has to say (an import's warnings); cleared by the next command.
    pub info: Option<String>,
}

#[derive(Clone, PartialEq, Debug)]
pub struct ContentView {
    pub id: String,
    pub text: Option<String>,
}

enum Cmd {
    SetEnabled(String, bool),
    Reorder(Vec<String>),
    /// None stops watching.
    WatchLogs(Option<String>),
    WatchDiagnostics(bool),
    Resync,
    RemoveStale(String),
    Import { name: String, filename: String, content: Vec<u8> },
    Delete(String),
    Update { id: String, name: Option<String>, settings: Option<Settings> },
    WatchContent(Option<String>),
    SaveContent { id: String, text: String, reconnect: bool },
    Credentials { id: String, kind: CredentialKind, username: String, password: String },
}

/// What the UI wants to see; kept across reconnects.
#[derive(Default)]
struct Want {
    logs: Option<String>,
    diagnostics: bool,
    content: Option<String>,
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
        let shared = Arc::new(Mutex::new(View { link: Link::Connecting, model: Model::default(), log: None, diagnostics: None, content: None, error: None, info: None }));
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

    /// Store a profile file. An empty `name` is taken from the file name.
    pub fn import(&self, name: &str, filename: &str, content: Vec<u8>) {
        let _ = self.cmds.send(Cmd::Import { name: name.into(), filename: filename.into(), content });
    }

    pub fn delete(&self, id: &str) {
        let _ = self.cmds.send(Cmd::Delete(id.into()));
    }

    pub fn rename(&self, id: &str, name: &str) {
        let _ = self.cmds.send(Cmd::Update { id: id.into(), name: Some(name.into()), settings: None });
    }

    pub fn set_settings(&self, id: &str, settings: Settings) {
        let _ = self.cmds.send(Cmd::Update { id: id.into(), name: None, settings: Some(settings) });
    }

    /// Fetch the text of profile `id` into `View::content`; None stops showing it.
    pub fn watch_content(&self, id: Option<&str>) {
        let _ = self.cmds.send(Cmd::WatchContent(id.map(String::from)));
    }

    pub fn save_content(&self, id: &str, text: &str, reconnect: bool) {
        let _ = self.cmds.send(Cmd::SaveContent { id: id.into(), text: text.into(), reconnect });
    }

    pub fn provide_credentials(&self, id: &str, kind: CredentialKind, username: &str, password: &str) {
        let _ = self.cmds.send(Cmd::Credentials { id: id.into(), kind, username: username.into(), password: password.into() });
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
    v.content = None;
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
        Cmd::WatchContent(id) => want.content = id,
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
    let mut content_shown: Option<String> = None;
    if want.diagnostics {
        refresh(&mut client, s, wake).await;
    }
    loop {
        if content_shown != want.content {
            content_shown = want.content.clone();
            fetch_content(&mut client, s, wake, content_shown.as_deref()).await;
        }
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
                    Cmd::Import { name, filename, content } => match client.import_profile(pb::ImportProfileRequest { name, source_filename: filename, content, ..Default::default() }).await {
                        Ok(r) => {
                            let r = r.into_inner();
                            update(s, wake, |v| v.info = Some(import_summary(r.profile.as_ref().map_or("", |p| p.name.as_str()), &r.warnings)));
                            Ok(())
                        }
                        Err(e) => Err(e),
                    },
                    Cmd::Delete(id) => client.delete_profile(pb::DeleteProfileRequest { id }).await.map(|_| ()),
                    Cmd::Update { id, name, settings } => client.update_profile(pb::UpdateProfileRequest { id, name, settings: settings.map(Settings::to_pb) }).await.map(|_| ()),
                    Cmd::SaveContent { id, text, reconnect } => {
                        let r = client.update_profile_content(pb::UpdateProfileContentRequest { id: id.clone(), content: text.into_bytes(), reconnect }).await;
                        if let Ok(r) = &r {
                            let w = &r.get_ref().warnings;
                            update(s, wake, |v| v.info = Some(import_summary("", w)));
                            fetch_content(&mut client, s, wake, Some(&id)).await;
                        }
                        r.map(|_| ())
                    }
                    Cmd::Credentials { id, kind, username, password } => {
                        let kind = match kind {
                            CredentialKind::UserPassword => pb::CredentialKind::UserPassword,
                            CredentialKind::KeyPassphrase => pb::CredentialKind::KeyPassphrase,
                        };
                        client.provide_credentials(pb::ProvideCredentialsRequest { profile_id: id, kind: kind as i32, username, password }).await.map(|_| ())
                    }
                    c @ (Cmd::WatchLogs(_) | Cmd::WatchDiagnostics(_) | Cmd::WatchContent(_)) => {
                        absorb(c, want);
                        if want.diagnostics {
                            refresh(&mut client, s, wake).await;
                        } else {
                            update(s, wake, |v| v.diagnostics = None);
                        }
                        continue;
                    }
                };
                update(s, wake, |v| {
                    v.error = r.err().map(|e| e.message().to_string());
                    if v.error.is_some() {
                        v.info = None;
                    }
                });
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


/// "Imported Home" and one line per warning, the way the daemon words them.
fn import_summary(name: &str, warnings: &[pb::ImportWarning]) -> String {
    let mut text = if name.is_empty() { "Saved".to_string() } else { format!("Imported {name}") };
    for w in warnings {
        text.push_str(&format!("\nline {}: {} {}", w.line, w.directive, w.message));
    }
    text
}

/// Get the text of a profile for the editor; the view says "loading" until it arrives.
async fn fetch_content(client: &mut Client, s: &Mutex<View>, wake: &Wake, id: Option<&str>) {
    let Some(id) = id else {
        update(s, wake, |v| v.content = None);
        return;
    };
    update(s, wake, |v| v.content = Some(ContentView { id: id.into(), text: None }));
    match client.get_profile_content(pb::GetProfileContentRequest { id: id.into() }).await {
        Ok(r) => {
            let text = String::from_utf8_lossy(&r.into_inner().content).into_owned();
            update(s, wake, |v| {
                if v.content.as_ref().is_some_and(|c| c.id == id) {
                    v.content = Some(ContentView { id: id.into(), text: Some(text) });
                }
            });
        }
        Err(e) => update(s, wake, |v| v.error = Some(e.message().to_string())),
    }
}
