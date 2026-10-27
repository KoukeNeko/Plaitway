//! The session against the fake daemon on a real named pipe.

use std::time::{Duration, Instant};

use plaitway_core::{Link, Session, Standing, View};
use plaitway_fake::{serve, Fake, DEMO};

fn pipe(test: &str) -> String {
    format!(r"\\.\pipe\plaitway-core-test-{}-{test}", std::process::id())
}

/// Start the fake daemon on its own thread; the pipe exists when this returns.
fn daemon(pipe: &str, delay_ms: u64) {
    let pipe = pipe.to_string();
    let (tx, rx) = std::sync::mpsc::channel();
    std::thread::spawn(move || {
        tokio::runtime::Runtime::new().unwrap().block_on(async move {
            let (ready, wait) = tokio::sync::oneshot::channel();
            let fake = Fake::new(&DEMO, Duration::from_millis(delay_ms));
            let server = tokio::spawn(async move { serve(&pipe, fake, ready).await });
            let _ = wait.await;
            tx.send(()).unwrap();
            let _ = server.await;
        });
    });
    rx.recv().unwrap();
}

/// Poll the view until `done` holds, or fail after five seconds with the last view.
fn until(s: &Session, what: &str, done: impl Fn(&View) -> bool) -> View {
    let end = Instant::now() + Duration::from_secs(5);
    loop {
        let v = s.view();
        if done(&v) {
            return v;
        }
        assert!(Instant::now() < end, "timed out waiting for: {what}\n{v:#?}");
        std::thread::sleep(Duration::from_millis(20));
    }
}

fn holder_of(v: &View, prefix: &str) -> Option<String> {
    let row = v.model.routes().into_iter().find(|r| r.prefix == prefix)?;
    row.holder.map(|i| v.model.tunnels[i].id.clone())
}

#[test]
fn the_view_follows_the_daemon_and_commands_reach_it() {
    let p = pipe("follow");
    daemon(&p, 50);
    let s = Session::start(&p, || {});
    let v = until(&s, "online with three profiles", |v| v.link == Link::Online && v.model.tunnels.len() == 3);
    assert_eq!(v.model.ids(), ["home", "office", "lab"]);
    assert_eq!(holder_of(&v, "0.0.0.0/0").as_deref(), Some("home"));
    assert_eq!(holder_of(&v, "10.8.0.0/24").as_deref(), Some("home"));
    assert_eq!(holder_of(&v, "10.20.0.0/16").as_deref(), Some("office"));
    assert_eq!(holder_of(&v, "172.16.8.0/24").as_deref(), Some("lab"));

    // Disconnect the holder: its routes pass to the next profile that asks for them.
    s.set_enabled("home", false);
    let v = until(&s, "office holds the default route", |v| holder_of(v, "0.0.0.0/0").as_deref() == Some("office"));
    assert!(!v.model.tunnels[0].enabled);
    assert_eq!(v.error, None);

    // Reconnect: the connection takes a moment, then home wins again.
    s.set_enabled("home", true);
    until(&s, "home holds the default route again", |v| holder_of(v, "0.0.0.0/0").as_deref() == Some("home"));

    // Raise lab above everyone.
    s.reorder(vec!["lab".into(), "home".into(), "office".into()]);
    let v = until(&s, "lab first", |v| v.model.ids() == ["lab", "home", "office"]);
    assert_eq!(holder_of(&v, "192.168.1.0/24").as_deref(), Some("lab"));
    let row = v.model.routes().into_iter().find(|r| r.prefix == "192.168.1.0/24").unwrap();
    assert_eq!(row.requests.iter().map(|r| r.1).collect::<Vec<_>>(), [Standing::Holds, Standing::Waits, Standing::Waits]);
}

#[test]
fn a_refused_command_shows_up_as_an_error_and_the_next_good_one_clears_it() {
    let p = pipe("refused");
    daemon(&p, 50);
    let s = Session::start(&p, || {});
    until(&s, "online", |v| v.link == Link::Online);
    s.set_enabled("nobody", true);
    let v = until(&s, "an error", |v| v.error.is_some());
    assert!(v.error.unwrap().contains("no such profile"));
    s.reorder(vec!["home".into()]); // not every profile listed
    until(&s, "invalid argument", |v| v.error.as_deref().is_some_and(|e| e.contains("every profile")));
    s.set_enabled("home", true);
    until(&s, "cleared", |v| v.error.is_none());
}

#[test]
fn the_session_waits_for_a_daemon_that_starts_late() {
    let p = pipe("late");
    let s = Session::start(&p, || {});
    until(&s, "offline", |v| matches!(v.link, Link::Offline(_)));
    daemon(&p, 50);
    until(&s, "online after the daemon appears", |v| v.link == Link::Online && v.model.tunnels.len() == 3);
}

#[test]
fn every_change_wakes_the_ui() {
    let p = pipe("wake");
    daemon(&p, 50);
    let n = std::sync::Arc::new(std::sync::atomic::AtomicUsize::new(0));
    let n2 = n.clone();
    let s = Session::start(&p, move || {
        n2.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
    });
    until(&s, "online", |v| v.link == Link::Online);
    let before = n.load(std::sync::atomic::Ordering::SeqCst);
    s.set_enabled("lab", false);
    until(&s, "lab off", |v| v.model.tunnels.iter().any(|t| t.id == "lab" && !t.enabled));
    assert!(n.load(std::sync::atomic::Ordering::SeqCst) > before);
}
