//! Keeps a `Model` current from the daemon on a background thread, and runs commands on it.

use std::{
    sync::{Arc, Mutex},
    time::Duration,
};

use plaitway_client::{connect, pb};
use tokio::sync::mpsc;

use crate::Model;

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
    /// The last command the daemon refused or could not receive; cleared by the next one that works.
    pub error: Option<String>,
}

enum Cmd {
    SetEnabled(String, bool),
    Reorder(Vec<String>),
}

pub struct Session {
    shared: Arc<Mutex<View>>,
    cmds: mpsc::UnboundedSender<Cmd>,
}

type Wake = Arc<dyn Fn() + Send + Sync>;

impl Session {
    /// Connect to `pipe` and keep trying. `wake` is called after every change to the view, from the
    /// background thread; it must be cheap and must not take locks the UI holds while reading.
    pub fn start(pipe: &str, wake: impl Fn() + Send + Sync + 'static) -> Session {
        let shared = Arc::new(Mutex::new(View { link: Link::Connecting, model: Model::default(), error: None }));
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
}

fn update(s: &Mutex<View>, wake: &Wake, f: impl FnOnce(&mut View)) {
    f(&mut s.lock().unwrap());
    wake();
}

/// What was on screen can no longer be trusted once the daemon is gone.
fn offline(v: &mut View, why: String) {
    v.link = Link::Offline(why);
    v.model = Model::default();
}

async fn run(pipe: String, s: Arc<Mutex<View>>, wake: Wake, mut cmds: mpsc::UnboundedReceiver<Cmd>) {
    loop {
        update(&s, &wake, |v| v.link = Link::Connecting);
        match connect(&pipe).await {
            Ok(client) => {
                let end = serve(client, &s, &wake, &mut cmds).await;
                update(&s, &wake, |v| offline(v, end));
            }
            Err(e) => update(&s, &wake, |v| offline(v, e.to_string())),
        }
        // Commands sent while offline would act on a state the user can no longer see.
        while cmds.try_recv().is_ok() {}
        tokio::time::sleep(Duration::from_secs(1)).await;
    }
}

/// Watch profiles and run commands until the connection fails; returns why it ended.
async fn serve(mut client: plaitway_client::DaemonServiceClient<plaitway_client::Channel>, s: &Mutex<View>, wake: &Wake, cmds: &mut mpsc::UnboundedReceiver<Cmd>) -> String {
    let mut stream = match client.watch_profiles(pb::WatchProfilesRequest {}).await {
        Ok(r) => r.into_inner(),
        Err(e) => return e.message().to_string(),
    };
    loop {
        tokio::select! {
            m = stream.message() => match m {
                Ok(Some(ev)) => update(s, wake, |v| {
                    if matches!(ev.event, Some(pb::profile_event::Event::Snapshot(_))) {
                        v.link = Link::Online;
                    }
                    v.model.apply(ev);
                }),
                Ok(None) => return "the daemon closed the connection".into(),
                Err(e) => return e.message().to_string(),
            },
            Some(c) = cmds.recv() => {
                let r = match c {
                    Cmd::SetEnabled(id, enabled) => client.set_profile_enabled(pb::SetProfileEnabledRequest { id, enabled }).await.map(|_| ()),
                    Cmd::Reorder(ids) => client.reorder_profiles(pb::ReorderProfilesRequest { ids }).await.map(|_| ()),
                };
                update(s, wake, |v| v.error = r.err().map(|e| e.message().to_string()));
            }
        }
    }
}


