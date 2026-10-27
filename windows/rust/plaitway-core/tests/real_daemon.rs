//! Read-only checks against a real `plaitwayd`. They run only when PLAITWAY_REAL_PIPE names its pipe,
//! and never change anything: no profile is connected, reordered, imported or removed.

use std::time::{Duration, Instant};

use plaitway_core::{Link, Session, Standing};

fn pipe() -> Option<String> {
    std::env::var("PLAITWAY_REAL_PIPE").ok()
}

#[test]
fn the_real_daemons_answers_fit_the_model() {
    let Some(pipe) = pipe() else {
        eprintln!("skipped: PLAITWAY_REAL_PIPE is not set");
        return;
    };
    let s = Session::start(&pipe, || {});
    s.watch_diagnostics(true);
    s.watch_logs(Some(""));
    let end = Instant::now() + Duration::from_secs(8);
    let v = loop {
        let v = s.view();
        if v.link == Link::Online && v.diagnostics.is_some() {
            std::thread::sleep(Duration::from_millis(500)); // let the log tail arrive
            break s.view();
        }
        assert!(Instant::now() < end, "never came online with diagnostics: {v:#?}");
        std::thread::sleep(Duration::from_millis(50));
    };

    // Profiles: shapes only, so no names or routes are printed.
    eprintln!("profiles: {}", v.model.tunnels.len());
    for t in &v.model.tunnels {
        eprintln!("  state={:?} enabled={} priority={} wanted={} requests={} since={}", t.state, t.enabled, t.priority, t.wanted, t.requests.len(), t.since.is_some());
        assert!(!t.id.is_empty() && !t.name.is_empty());
    }
    let routes = v.model.routes();
    for r in &routes {
        let holders = r.requests.iter().filter(|q| q.1 == Standing::Holds).count();
        assert!(holders <= 1, "{} has {holders} holders", r.prefix);
        assert_eq!(r.holder.is_some(), holders == 1);
    }
    eprintln!("routes asked for: {}, held: {}", routes.len(), routes.iter().filter(|r| r.holder.is_some()).count());
    assert!(v.model.tunnels.windows(2).all(|w| w[0].priority <= w[1].priority), "sorted by priority");

    // Diagnostics.
    let d = v.diagnostics.unwrap();
    eprintln!("daemon version={} privileged={} gateway_set={} interface_set={} interfaces={} last_change={} reason={:?}", d.version, d.privileged, !d.gateway_v4.is_empty(), !d.interface_v4.is_empty(), d.interfaces.len(), d.last_change.is_some(), d.last_change_reason);
    eprintln!("owned={} stale={} resolvers={} journal={}", d.owned.len(), d.stale.len(), d.resolvers.len(), d.journal.len());
    assert!(!d.version.is_empty());
    for s in &d.stale {
        assert!(!s.key.is_empty(), "a stale route needs a key to be removable");
    }
    for j in &d.journal {
        assert!(j.time > 0, "journal entries carry a time");
    }
    // The owned routes are the ones the profiles hold (the daemon may also own bypass routes).
    let held: usize = v.model.tunnels.iter().map(|t| t.holds().0).sum();
    eprintln!("owned by the daemon: {}, held by profiles: {held}", d.owned.len());

    // The daemon's own log.
    let log = v.log.unwrap();
    eprintln!("daemon log lines: {}", log.lines.len());
    assert!(log.lines.iter().all(|l| l.time > 0), "every line has a time");
    for l in log.lines.iter().take(3) {
        eprintln!("  {:?} {}", l.level, l.text.chars().take(70).collect::<String>());
    }
}
