//! Against a real `plaitwayd -fake` (real gRPC server and reconciler, in-memory tunnels). It changes
//! that daemon's profiles, so it runs only when PLAITWAY_FAKE_PIPE names one started for this.

use std::time::{Duration, Instant};

use plaitway_client::{connect, pb};
use plaitway_core::{Link, Session, View};

const KEY: &str = "kNVyW0xE0F0CDpvW9vVEbN9wC0VvJ5ms8LQ3dY1dXkM=";

fn wg(addr: &str, allowed: &str) -> String {
    format!("[Interface]\nPrivateKey = {KEY}\nAddress = {addr}\n[Peer]\nPublicKey = {KEY}\nEndpoint = 203.0.113.5:51820\nAllowedIPs = {allowed}\n")
}

fn until(s: &Session, what: &str, done: impl Fn(&View) -> bool) -> View {
    let end = Instant::now() + Duration::from_secs(8);
    loop {
        let v = s.view();
        if done(&v) {
            return v;
        }
        assert!(Instant::now() < end, "timed out waiting for: {what}\n{v:#?}");
        std::thread::sleep(Duration::from_millis(30));
    }
}

#[tokio::test(flavor = "multi_thread")]
async fn routes_logs_and_diagnostics_from_a_real_reconciler() {
    let Ok(pipe) = std::env::var("PLAITWAY_FAKE_PIPE") else {
        eprintln!("skipped: PLAITWAY_FAKE_PIPE is not set");
        return;
    };
    let mut c = connect(&pipe).await.expect("connect");
    // Start from nothing, so a second run gives the same picture.
    for p in c.list_profiles(pb::ListProfilesRequest {}).await.unwrap().into_inner().profiles {
        c.set_profile_enabled(pb::SetProfileEnabledRequest { id: p.id.clone(), enabled: false }).await.unwrap();
        c.delete_profile(pb::DeleteProfileRequest { id: p.id }).await.unwrap();
    }
    let mut ids = vec![];
    for (name, content) in [("alpha", wg("10.6.0.2/32", "0.0.0.0/0, 192.168.1.0/24")), ("beta", wg("10.7.0.2/32", "192.168.1.0/24, 10.9.0.0/16")), ("gamma", wg("10.8.0.2/32", "10.9.0.0/16"))] {
        let r = c.import_profile(pb::ImportProfileRequest { name: name.into(), content: content.into_bytes(), kind: pb::ProfileKind::Wireguard as i32, ..Default::default() }).await.expect("import").into_inner();
        ids.push(r.profile.unwrap().id);
    }
    c.reorder_profiles(pb::ReorderProfilesRequest { ids: ids.clone() }).await.unwrap();

    let s = Session::start(&pipe, || {});
    s.watch_diagnostics(true);
    until(&s, "online with three profiles", |v| v.link == Link::Online && v.model.tunnels.len() == 3);
    for id in &ids {
        s.set_enabled(id, true);
    }
    let v = until(&s, "all connected", |v| v.model.tunnels.iter().all(|t| t.connected()));
    for t in &v.model.tunnels {
        eprintln!("{} state={:?} priority={} wanted={} requests={:?} since={:?}", t.name, t.state, t.priority, t.wanted, t.requests.iter().map(|r| (r.prefix.as_str(), r.standing, r.shadowed_by.is_some())).collect::<Vec<_>>(), t.since.is_some());
    }
    let rows = v.model.routes();
    for r in &rows {
        eprintln!("route {} holder={:?} requests={:?}", r.prefix, r.holder.map(|i| &v.model.tunnels[i].name), r.requests);
    }
    // `-fake` swaps the reconciler for one that does not shadow anything, so every profile holds what it
    // asked for and no route waits; ownership by priority can only be seen against the real reconciler.
    let prefixes: Vec<&str> = rows.iter().map(|r| r.prefix.as_str()).collect();
    assert!(prefixes.contains(&"0.0.0.0/0"), "the two halves of the default route are shown as one: {prefixes:?}");
    assert!(!prefixes.contains(&"0.0.0.0/1") && !prefixes.contains(&"128.0.0.0/1"));
    assert!(prefixes.contains(&"192.168.1.0/24") && prefixes.contains(&"10.9.0.0/16"));
    // Diagnostics while connected.
    let d = until(&s, "diagnostics with owned routes", |v| v.diagnostics.as_ref().is_some_and(|d| !d.owned.is_empty())).diagnostics.unwrap();
    eprintln!("owned={} stale={} resolvers={} journal={}", d.owned.len(), d.stale.len(), d.resolvers.len(), d.journal.len());
    for o in &d.owned {
        eprintln!("  owned {} owner={} via={}", o.prefix, o.owner, o.via);
    }
    for j in d.journal.iter().take(4) {
        eprintln!("  journal {} {} {} t={}", j.owner, j.key, j.state, j.time > 0);
    }
    assert!(d.journal.iter().all(|j| j.time > 0));

    // The log of one profile.
    s.watch_logs(Some(&ids[0]));
    let v = until(&s, "a log line for alpha", |v| v.log.as_ref().is_some_and(|l| !l.lines.is_empty()));
    for l in v.log.unwrap().lines.iter().take(4) {
        eprintln!("  log {:?} {}", l.level, l.text.chars().take(80).collect::<String>());
    }

    // The new priority order comes back from the daemon.
    s.reorder(vec![ids[2].clone(), ids[0].clone(), ids[1].clone()]);
    let v = until(&s, "gamma first", |v| v.model.tunnels.first().is_some_and(|t| t.id == ids[2]));
    eprintln!("order after reorder: {:?}", v.model.tunnels.iter().map(|t| t.name.as_str()).collect::<Vec<_>>());
    assert_eq!(v.model.ids(), [ids[2].clone(), ids[0].clone(), ids[1].clone()]);

    for id in &ids {
        s.set_enabled(id, false);
    }
    until(&s, "all disconnected", |v| v.model.tunnels.iter().all(|t| !t.enabled));
    for id in ids {
        c.delete_profile(pb::DeleteProfileRequest { id }).await.unwrap();
    }
}

