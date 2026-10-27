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

fn log_texts(v: &View) -> Vec<String> {
    v.log.as_ref().map(|l| l.lines.iter().map(|e| e.text.clone()).collect()).unwrap_or_default()
}

#[test]
fn a_profiles_log_has_its_tail_and_then_live_lines() {
    let p = pipe("logs");
    daemon(&p, 50);
    let s = Session::start(&p, || {});
    until(&s, "online", |v| v.link == Link::Online);
    assert!(s.view().log.is_none());

    s.watch_logs(Some("home"));
    let v = until(&s, "the tail of home", |v| log_texts(v) == ["tunnel up"]);
    assert_eq!(v.log.unwrap().profile_id, "home");

    // A live line shows up without asking again, and only this profile's.
    s.set_enabled("lab", false);
    s.set_enabled("home", false);
    let v = until(&s, "disconnected line", |v| log_texts(v).last().is_some_and(|t| t == "disconnected"));
    assert_eq!(log_texts(&v), ["tunnel up", "disconnected"]);

    // The daemon's own log is a different stream.
    s.watch_logs(Some(""));
    let v = until(&s, "daemon log", |v| log_texts(v).first().is_some_and(|t| t == "plaitwayd started"));
    assert!(!log_texts(&v).contains(&"disconnected".to_string()));

    s.watch_logs(None);
    until(&s, "no log", |v| v.log.is_none());
}

#[test]
fn the_log_view_follows_a_daemon_that_goes_away_and_comes_back() {
    // The same session keeps what the user was looking at when the connection returns.
    let p = pipe("logs-late");
    let s = Session::start(&p, || {});
    s.watch_logs(Some("lab"));
    until(&s, "offline", |v| matches!(v.link, Link::Offline(_)));
    daemon(&p, 50);
    until(&s, "the lab log after the daemon appears", |v| v.link == Link::Online && log_texts(v) == ["tunnel up"]);
}

#[test]
fn diagnostics_follow_the_daemon_while_watched() {
    let p = pipe("diag");
    daemon(&p, 50);
    let s = Session::start(&p, || {});
    until(&s, "online", |v| v.link == Link::Online);
    assert!(s.view().diagnostics.is_none(), "nothing is fetched until asked for");

    s.watch_diagnostics(true);
    let v = until(&s, "diagnostics", |v| v.diagnostics.is_some());
    let d = v.diagnostics.unwrap();
    assert_eq!((d.version.as_str(), d.privileged, d.gateway_v4.as_str(), d.interface_v4.as_str()), ("fake", true, "192.168.1.1", "Ethernet"));
    // Home, Office and Lab hold 3 + 1 + 1 prefixes.
    assert_eq!(d.owned.len(), 5);
    assert_eq!(d.owned.iter().filter(|r| r.owner == "home").count(), 3);
    assert_eq!(d.stale.len(), 1);
    assert!(d.journal.iter().any(|j| j.owner == "lab" && j.state == "applied"));

    // A profile change refreshes them by itself.
    s.set_enabled("home", false);
    let v = until(&s, "owned routes follow the change", |v| v.diagnostics.as_ref().is_some_and(|d| d.owned.iter().all(|r| r.owner != "home")));
    let d = v.diagnostics.unwrap();
    assert_eq!(d.owned.len(), 5, "every prefix still has an owner");
    assert_eq!(d.owned.iter().filter(|r| r.owner == "office").count(), 4, "office takes the three home held and keeps its own");

    s.watch_diagnostics(false);
    until(&s, "cleared", |v| v.diagnostics.is_none());
}

#[test]
fn a_stale_route_can_be_removed_and_resync_is_accepted() {
    let p = pipe("stale");
    daemon(&p, 50);
    let s = Session::start(&p, || {});
    until(&s, "online", |v| v.link == Link::Online);
    s.watch_diagnostics(true);
    let v = until(&s, "the stale route", |v| v.diagnostics.as_ref().is_some_and(|d| d.stale.len() == 1));
    let key = v.diagnostics.unwrap().stale[0].key.clone();

    s.watch_logs(Some(""));
    s.remove_stale(&key);
    until(&s, "gone", |v| v.diagnostics.as_ref().is_some_and(|d| d.stale.is_empty()));
    until(&s, "the daemon logged it", |v| log_texts(v).iter().any(|t| t.contains("removed stale route")));

    s.remove_stale(&key);
    let v = until(&s, "an error for the second removal", |v| v.error.is_some());
    assert!(v.error.unwrap().contains("no such stale route"));

    s.resync();
    until(&s, "resync logged and the error cleared", |v| v.error.is_none() && log_texts(v).iter().any(|t| t.contains("resync")));
}


const KEY: &str = "kNVyW0xE0F0CDpvW9vVEbN9wC0VvJ5ms8LQ3dY1dXkM=";

fn wg(allowed: &str) -> Vec<u8> {
    format!("[Interface]\nPrivateKey = {KEY}\nAddress = 10.6.0.2/32\n[Peer]\nEndpoint = 203.0.113.5:51820\nAllowedIPs = {allowed}\n").into_bytes()
}

fn tunnel<'a>(v: &'a View, name: &str) -> Option<&'a plaitway_core::Tunnel> {
    v.model.tunnels.iter().find(|t| t.name == name)
}

#[test]
fn a_profile_can_be_imported_renamed_changed_and_deleted() {
    let p = pipe("manage");
    daemon(&p, 50);
    let s = Session::start(&p, || {});
    until(&s, "online", |v| v.link == Link::Online && v.model.tunnels.len() == 3);

    s.import("Cafe", "cafe.conf", wg("10.50.0.0/16"));
    let v = until(&s, "the new profile", |v| tunnel(v, "Cafe").is_some());
    assert_eq!(v.model.tunnels.len(), 4);
    assert_eq!(tunnel(&v, "Cafe").unwrap().wanted, 1);
    assert_eq!(tunnel(&v, "Cafe").unwrap().kind, plaitway_core::Kind::WireGuard);
    assert!(v.info.as_deref().is_some_and(|i| i.starts_with("Imported Cafe")), "{:?}", v.info);
    let id = tunnel(&v, "Cafe").unwrap().id.clone();

    // The name comes from the file when none is given.
    s.import("", r"C:\profiles\Campus.conf", wg("10.51.0.0/16"));
    until(&s, "named from the file", |v| tunnel(v, "Campus").is_some());

    s.rename(&id, "Cafe Wi-Fi");
    until(&s, "renamed", |v| tunnel(v, "Cafe Wi-Fi").is_some() && tunnel(v, "Cafe").is_none());

    let mut settings = tunnel(&s.view(), "Cafe Wi-Fi").unwrap().settings;
    settings.auto_connect = true;
    settings.mode = plaitway_core::TunnelMode::Split;
    s.set_settings(&id, settings);
    let v = until(&s, "settings saved", |v| tunnel(v, "Cafe Wi-Fi").is_some_and(|t| t.settings.auto_connect));
    assert_eq!(tunnel(&v, "Cafe Wi-Fi").unwrap().settings.mode, plaitway_core::TunnelMode::Split);
    assert_eq!(tunnel(&v, "Cafe Wi-Fi").unwrap().settings.priority, tunnel(&v, "Cafe Wi-Fi").unwrap().priority, "the priority is not lost");

    s.delete(&id);
    until(&s, "deleted", |v| tunnel(v, "Cafe Wi-Fi").is_none());
    s.delete(&id);
    let v = until(&s, "a second delete is refused", |v| v.error.is_some());
    assert!(v.error.unwrap().contains("no such profile"));
}

#[test]
fn a_rejected_profile_comes_back_as_an_error_with_its_line() {
    let p = pipe("reject");
    daemon(&p, 50);
    let s = Session::start(&p, || {});
    until(&s, "online", |v| v.link == Link::Online);
    s.import("Bad", "bad.conf", b"[Interface]\nnothing useful\n".to_vec());
    let v = until(&s, "an error", |v| v.error.is_some());
    assert!(v.error.unwrap().starts_with("line 1:"));
    assert!(tunnel(&s.view(), "Bad").is_none());

    s.import("Scripts", "s.ovpn", b"client\nremote vpn.example.com 1194\nscript-security 2\n".to_vec());
    let v = until(&s, "imported with a warning", |v| v.info.as_deref().is_some_and(|i| i.contains("script-security")));
    assert!(v.info.unwrap().contains("line 3"));
    assert_eq!(v.error, None);
}

#[test]
fn the_text_of_a_profile_can_be_read_and_saved() {
    let p = pipe("content");
    daemon(&p, 50);
    let s = Session::start(&p, || {});
    until(&s, "online", |v| v.link == Link::Online);
    s.import("Text", "t.conf", wg("10.52.0.0/16"));
    let id = until(&s, "imported", |v| tunnel(v, "Text").is_some()).model.tunnels.iter().find(|t| t.name == "Text").unwrap().id.clone();

    s.watch_content(Some(&id));
    let v = until(&s, "the text", |v| v.content.as_ref().is_some_and(|c| c.text.is_some()));
    assert!(v.content.unwrap().text.unwrap().contains("AllowedIPs = 10.52.0.0/16"));

    s.save_content(&id, &String::from_utf8(wg("10.53.0.0/16, 10.54.0.0/16")).unwrap(), false);
    let v = until(&s, "the saved text and routes", |v| v.content.as_ref().and_then(|c| c.text.as_deref()).is_some_and(|t| t.contains("10.53.0.0/16")) && tunnel(v, "Text").is_some_and(|t| t.wanted == 2));
    assert_eq!(v.error, None);

    s.save_content(&id, "garbage", false);
    let v = until(&s, "a refused save", |v| v.error.is_some());
    assert!(v.content.unwrap().text.unwrap().contains("10.53.0.0/16"), "the stored text is untouched");

    s.watch_content(None);
    until(&s, "nothing shown", |v| v.content.is_none());
}

#[test]
fn a_profile_that_needs_a_password_waits_for_it() {
    let p = pipe("creds");
    daemon(&p, 50);
    let s = Session::start(&p, || {});
    until(&s, "online", |v| v.link == Link::Online);
    s.import("Corp", "corp.ovpn", b"client\nremote vpn.example.com 1194\nauth-user-pass\nroute 10.60.0.0 255.255.0.0\n".to_vec());
    let v = until(&s, "imported", |v| tunnel(v, "Corp").is_some());
    let t = tunnel(&v, "Corp").unwrap();
    assert!(t.summary.requires_credentials);
    let id = t.id.clone();

    s.set_enabled(&id, true);
    let v = until(&s, "asking for credentials", |v| tunnel(v, "Corp").is_some_and(|t| t.credential.is_some()));
    assert_eq!(tunnel(&v, "Corp").unwrap().credential, Some(plaitway_core::CredentialKind::UserPassword));
    assert_eq!(tunnel(&v, "Corp").unwrap().state, plaitway_core::State::AwaitingCredentials);

    s.provide_credentials(&id, plaitway_core::CredentialKind::UserPassword, "ann", "");
    until(&s, "an empty password is refused", |v| v.error.is_some());
    s.provide_credentials(&id, plaitway_core::CredentialKind::UserPassword, "ann", "hunter2");
    let v = until(&s, "connected", |v| tunnel(v, "Corp").is_some_and(|t| t.connected()));
    assert_eq!(tunnel(&v, "Corp").unwrap().credential, None);
    assert_eq!(tunnel(&v, "Corp").unwrap().holds().0, 1);
}
