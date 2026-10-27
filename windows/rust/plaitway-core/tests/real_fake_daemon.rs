//! Against a real `plaitwayd -fake` (real gRPC server and reconciler, in-memory tunnels). It changes
//! that daemon's profiles, so it runs only when PLAITWAY_FAKE_PIPE names one started for this.

use std::time::{Duration, Instant};

use plaitway_client::{connect, pb};
use plaitway_core::{Link, Session, View};

const KEY: &str = "kNVyW0xE0F0CDpvW9vVEbN9wC0VvJ5ms8LQ3dY1dXkM=";

fn wg(allowed: &str, addr: &str) -> String {
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
    for (name, content) in [("alpha", wg("0.0.0.0/0, 192.168.1.0/24", "10.6.0.2/32")), ("beta", wg("192.168.1.0/24, 10.9.0.0/16", "10.7.0.2/32")), ("gamma", wg("10.9.0.0/16", "10.8.0.2/32"))] {
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


fn tunnel_named<'a>(v: &'a View, name: &str) -> Option<&'a plaitway_core::Tunnel> {
    v.model.tunnels.iter().find(|t| t.name == name)
}

#[tokio::test(flavor = "multi_thread")]
async fn managing_profiles_against_the_real_daemon() {
    let Ok(pipe) = std::env::var("PLAITWAY_FAKE_PIPE") else {
        eprintln!("skipped: PLAITWAY_FAKE_PIPE is not set");
        return;
    };
    let mut c = connect(&pipe).await.expect("connect");
    for p in c.list_profiles(pb::ListProfilesRequest {}).await.unwrap().into_inner().profiles {
        let _ = c.set_profile_enabled(pb::SetProfileEnabledRequest { id: p.id.clone(), enabled: false }).await;
        c.delete_profile(pb::DeleteProfileRequest { id: p.id }).await.unwrap();
    }
    let s = Session::start(&pipe, || {});
    until(&s, "online", |v| v.link == Link::Online);

    // Import: a WireGuard profile, one the daemon refuses, one that asks for a password.
    s.import("Cafe", "cafe.conf", wg("10.50.0.0/16", "10.6.0.2/32").into_bytes());
    let v = until(&s, "imported", |v| tunnel_named(v, "Cafe").is_some());
    let cafe = tunnel_named(&v, "Cafe").unwrap().clone();
    eprintln!("imported: kind={:?} wanted={} endpoints={:?} addresses={:?} dns={:?} settings={:?} info={:?}", cafe.kind, cafe.wanted, cafe.summary.endpoints, cafe.summary.addresses, cafe.summary.dns, cafe.settings, v.info);
    // The -fake daemon refuses what the line `# fake: reject` is in, whatever else the text says.
    s.import("Bad", "bad.conf", b"client\n# fake: reject\n".to_vec());
    let v = until(&s, "a refusal", |v| v.error.is_some());
    eprintln!("refusal: {:?}", v.error);
    s.import("Corp", "corp.ovpn", format!("client\nremote vpn.example.com 1194\nauth-user-pass\nroute 10.60.0.0 255.255.0.0\n<key>\n{KEY}\n</key>\n").into_bytes());
    let v = until(&s, "the password profile", |v| tunnel_named(v, "Corp").is_some());
    let corp = tunnel_named(&v, "Corp").unwrap().clone();
    eprintln!("corp: kind={:?} requires_credentials={} wanted={}", corp.kind, corp.summary.requires_credentials, corp.wanted);

    // Credentials.
    s.set_enabled(&corp.id, true);
    let v = until(&s, "credentials wanted or connected", |v| tunnel_named(v, "Corp").is_some_and(|t| t.credential.is_some() || t.connected()));
    eprintln!("corp after enabling: state={:?} credential={:?} error={:?}", tunnel_named(&v, "Corp").unwrap().state, tunnel_named(&v, "Corp").unwrap().credential, v.error);
    if tunnel_named(&v, "Corp").unwrap().credential.is_some() {
        s.provide_credentials(&corp.id, plaitway_core::CredentialKind::UserPassword, "ann", "hunter2");
    }
    until(&s, "connected", |v| tunnel_named(v, "Corp").is_some_and(|t| t.connected()));

    // Rename, settings, content.
    s.rename(&cafe.id, "Cafe Wi-Fi");
    until(&s, "renamed", |v| tunnel_named(v, "Cafe Wi-Fi").is_some());
    let mut settings = tunnel_named(&s.view(), "Cafe Wi-Fi").unwrap().settings;
    settings.auto_connect = true;
    settings.exclude_private_ips = true;
    s.set_settings(&cafe.id, settings);
    let v = until(&s, "settings", |v| tunnel_named(v, "Cafe Wi-Fi").is_some_and(|t| t.settings.auto_connect));
    let after = tunnel_named(&v, "Cafe Wi-Fi").unwrap();
    assert_eq!(after.settings.priority, cafe.settings.priority, "the priority survives a settings change");
    assert!(after.settings.exclude_private_ips);

    s.watch_content(Some(&cafe.id));
    let v = until(&s, "the text", |v| v.content.as_ref().is_some_and(|c| c.text.is_some()));
    let text = v.content.unwrap().text.unwrap();
    eprintln!("content has the key: {}, {} lines", text.contains(KEY), text.lines().count());
    s.save_content(&cafe.id, &text.replace("10.50.0.0/16", "10.55.0.0/16"), false);
    until(&s, "saved", |v| v.content.as_ref().and_then(|c| c.text.as_deref()).is_some_and(|t| t.contains("10.55.0.0/16")));
    s.save_content(&cafe.id, &format!("{text}\n# fake: reject\n"), false);
    let v = until(&s, "a refused edit", |v| v.error.is_some());
    eprintln!("refused edit: {:?}", v.error);

    for id in [&cafe.id, &corp.id] {
        s.set_enabled(id, false);
        s.delete(id);
    }
    until(&s, "deleted", |v| v.model.tunnels.is_empty());
}



/// What `wgcf generate` writes for Cloudflare WARP: two addresses, four resolvers, an MTU, a default route
/// for both families and an endpoint by name. Made here with a fresh key, so no account is needed.
fn warp_shaped() -> String {
    let mut key = [0u8; 32];
    for (i, b) in key.iter_mut().enumerate() {
        *b = (i as u8).wrapping_mul(37).wrapping_add(11);
    }
    key[0] &= 248;
    key[31] = (key[31] & 127) | 64;
    // Base64 of 32 bytes without a library: 3 bytes make 4 characters.
    const T: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut s = String::new();
    for c in key.chunks(3) {
        let n = (c[0] as u32) << 16 | (*c.get(1).unwrap_or(&0) as u32) << 8 | *c.get(2).unwrap_or(&0) as u32;
        s.push(T[(n >> 18) as usize & 63] as char);
        s.push(T[(n >> 12) as usize & 63] as char);
        s.push(if c.len() > 1 { T[(n >> 6) as usize & 63] as char } else { '=' });
        s.push(if c.len() > 2 { T[n as usize & 63] as char } else { '=' });
    }
    format!(
        "[Interface]\nPrivateKey = {s}\nAddress = 172.16.0.2/32\nAddress = 2606:4700:110:8a36:df92:102a:9602:fa8b/128\nDNS = 1.1.1.1\nDNS = 1.0.0.1\nDNS = 2606:4700:4700::1111\nDNS = 2606:4700:4700::1001\nMTU = 1280\n\n[Peer]\nPublicKey = bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=\nAllowedIPs = 0.0.0.0/0\nAllowedIPs = ::/0\nEndpoint = engage.cloudflareclient.com:2408\n"
    )
}

#[tokio::test(flavor = "multi_thread")]
async fn a_warp_shaped_wireguard_profile_is_understood() {
    let Ok(pipe) = std::env::var("PLAITWAY_FAKE_PIPE") else {
        eprintln!("skipped: PLAITWAY_FAKE_PIPE is not set");
        return;
    };
    let mut c = connect(&pipe).await.expect("connect");
    for p in c.list_profiles(pb::ListProfilesRequest {}).await.unwrap().into_inner().profiles {
        let _ = c.set_profile_enabled(pb::SetProfileEnabledRequest { id: p.id.clone(), enabled: false }).await;
        c.delete_profile(pb::DeleteProfileRequest { id: p.id }).await.unwrap();
    }
    let s = Session::start(&pipe, || {});
    until(&s, "online", |v| v.link == Link::Online);
    s.import("warp", "wgcf-profile.conf", warp_shaped().into_bytes());
    let v = until(&s, "imported", |v| v.model.tunnels.iter().any(|t| t.name == "warp"));
    let t = v.model.tunnels.iter().find(|t| t.name == "warp").unwrap().clone();
    eprintln!("kind={:?} endpoints={:?} addresses={:?} dns={:?} wanted={} info={:?}", t.kind, t.summary.endpoints, t.summary.addresses, t.summary.dns, t.wanted, v.info);
    assert_eq!(t.kind, plaitway_core::Kind::WireGuard);
    assert_eq!(t.summary.endpoints, ["engage.cloudflareclient.com:2408"]);
    assert_eq!(t.summary.addresses.len(), 2, "one per family");
    assert_eq!(t.summary.dns.len(), 4);

    s.set_enabled(&t.id, true);
    let v = until(&s, "connected", |v| v.model.tunnels.iter().any(|t| t.name == "warp" && t.connected()));
    let rows = v.model.routes();
    let prefixes: Vec<&str> = rows.iter().map(|r| r.prefix.as_str()).collect();
    eprintln!("routes: {prefixes:?}");
    assert!(prefixes.contains(&"0.0.0.0/0"), "the IPv4 halves are one route: {prefixes:?}");
    // The profile names ::/0 too, but this daemon installs no IPv6 route: the page says so with "holds 1 of 2".
    let warp = v.model.tunnels.iter().find(|t| t.name == "warp").unwrap();
    assert_eq!((warp.holds(), warp.wanted), ((1, 2), 2), "{:?}", warp.requests);
    assert!(!prefixes.iter().any(|p| p.ends_with("/1")), "no half is left over: {prefixes:?}");

    s.watch_diagnostics(true);
    let d = until(&s, "owned routes", |v| v.diagnostics.as_ref().is_some_and(|d| !d.owned.is_empty())).diagnostics.unwrap();
    eprintln!("owned: {:?}", d.owned.iter().map(|o| (o.prefix.as_str(), format!("{:?}", o.kind))).collect::<Vec<_>>());
    assert!(d.owned.iter().any(|o| o.prefix == "0.0.0.0/0" && o.kind == plaitway_core::RouteKind::Default), "diagnostics show the default route as one: {:?}", d.owned);
    assert!(!d.owned.iter().any(|o| o.prefix.ends_with("/1")));

    s.set_enabled(&t.id, false);
    s.delete(&t.id);
    until(&s, "gone", |v| v.model.tunnels.is_empty());
}

