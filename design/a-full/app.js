const $ = (s, r = document) => r.querySelector(s);
const $$ = (s, r = document) => [...r.querySelectorAll(s)];

const P = {
  home:   { name: 'Home',   proto: 'WireGuard', since: '3 h 12 min', up: '3:12:08', rx: 4200, tx: 380, wants: ['default', 'dns', 'lan'],
            rows: [['Endpoint', 'vpn.home.example:51820'], ['Address', '10.8.0.2/32'], ['DNS', '1.1.1.1, 1.0.0.1'], ['Public key', 'hX3q…7vTk=']] },
  office: { name: 'Office', proto: 'OpenVPN',  since: '5 h 48 min', up: '5:48:31', rx: 410,  tx: 32,  wants: ['default', 'dns', 'corp', 'lan'],
            rows: [['Remote', 'gw.office.example:1194 (udp)'], ['Address', '10.20.0.14/16'], ['DNS', '10.20.0.2'], ['Cipher', 'AES-256-GCM']] },
  lab:    { name: 'Lab',    proto: 'WireGuard', since: '41 min', up: '0:41:12', rx: 38,   tx: 6,   wants: ['default', 'labnet'],
            rows: [['Endpoint', 'lab.example.net:51820'], ['Address', '172.16.8.5/32'], ['DNS', '172.16.8.1'], ['Public key', 'q9Lm…2aXc=']] },
};
const D = [
  { id: 'default', cidr: '0.0.0.0/0', label: 'Internet', say: 'the internet' },
  { id: 'dns', cidr: 'DNS', label: 'Name resolution', say: 'DNS' },
  { id: 'lan', cidr: '192.168.1.0/24', label: 'Local network', say: 'the local network' },
  { id: 'corp', cidr: '10.20.0.0/16', label: 'Office servers', say: 'the office servers' },
  { id: 'labnet', cidr: '172.16.8.0/24', label: 'Lab bench', say: 'the lab bench' },
];
const dest = id => D.find(d => d.id === id);
const S = { order: ['home', 'office', 'lab'], on: { home: true, office: true, lab: true }, sel: 'lan', open: false };

const holder = d => S.order.find(p => S.on[p] && P[p].wants.includes(d));
const held = p => P[p].wants.filter(d => holder(d) === p);
const waiting = p => P[p].wants.filter(d => holder(d) !== p);
const wanters = d => S.order.filter(p => S.on[p] && P[p].wants.includes(d));
const col = p => `var(--${p})`;
const fmt = kb => kb >= 1000 ? (kb / 1000).toFixed(1) + ' MB/s' : kb + ' KB/s';
const names = ids => ids.map(d => dest(d).say);
const list = a => a.length < 2 ? a.join('') : a.slice(0, -1).join(', ') + ' and ' + a.at(-1);
const ico = (d, s = '') => `<svg viewBox="0 0 16 16" ${s}><path d="${d}"/></svg>`;
const I = {
  route: 'M2 4h12M2 8h12M2 12h12', pulse: 'M2 8h3l2-5 3 10 2-5h2',
  gear: 'M8 5.6a2.4 2.4 0 100 4.8 2.4 2.4 0 000-4.8zM8 1.5v2M8 12.5v2M1.5 8h2M12.5 8h2M3.4 3.4l1.4 1.4M11.2 11.2l1.4 1.4M3.4 12.6l1.4-1.4M11.2 4.8l1.4-1.4',
};

/* ---------- shell ---------- */
function side(r) {
  const nav = (href, icon, label) => `<button class="nav ${r.join('/').startsWith(href) ? 'sel' : ''}" onclick="go('${href}')" title="${label}">${ico(icon)}<span class="lbl">${label}</span></button>`;
  const profs = S.order.map((p, i) => {
    const sel = r[0] === 'profile' && r[1] === p;
    const sub = S.on[p] ? `Holds ${held(p).length} of ${P[p].wants.length}` : 'Disconnected';
    return `<button class="nav prof ${S.on[p] ? '' : 'off'} ${sel ? 'sel' : ''}" style="--c:${col(p)}" onclick="go('profile/${p}/overview')" title="${P[p].name}">
      <span class="d"></span><span class="t">${P[p].name}<small>${P[p].proto} · ${sub}</small></span><span class="p" title="Priority">${i + 1}</span></button>`;
  }).join('');
  return `${nav('routing', I.route, 'Routing')}
    <div class="sec"><span>PROFILES</span><button title="Import profile" onclick="go('empty')">+</button></div>${profs}
    <div class="foot">${nav('diagnostics', I.pulse, 'Diagnostics')}${nav('settings', I.gear, 'Settings')}</div>`;
}

/* ---------- routing ---------- */
function routing() {
  const hd = holder('default');
  const lost = S.order.filter(p => S.on[p]).reduce((n, p) => n + waiting(p).length, 0);
  const sum = hd ? `${P[hd].name} holds the internet and DNS${holder('dns') !== hd ? ' is with ' + P[holder('dns')].name : ''}.` : 'No profile holds the internet.';
  const tuns = S.order.map(p => {
    const on = S.on[p];
    return `<div class="tun ${on ? '' : 'off'}" data-p="${p}" style="--c:${col(p)}">
      <div class="n"><i></i>${P[p].name}<span class="proto">${P[p].proto}</span></div>
      <p>${on ? 'Connected · ' + P[p].since : 'Disconnected'}</p>
      <div class="r mono">${on ? `<span><s>↓</s> ${fmt(P[p].rx)}</span><span><s>↑</s> ${fmt(P[p].tx)}</span>` : ''}
        <button class="btn sm" style="font-family:inherit" onclick="toggle('${p}')">${on ? 'Disconnect' : 'Connect'}</button></div></div>`;
  }).join('');
  const dests = D.map(d => {
    const h = holder(d.id), lw = wanters(d.id).filter(p => p !== h);
    return `<button class="dest ${S.sel === d.id ? 'sel' : ''}" data-d="${d.id}" onclick="sel('${d.id}')">
      <b class="mono">${d.cidr}</b><small>${d.label}</small>
      <div class="chips">${h ? `<span class="chip" style="--c:${col(h)}">${P[h].name} holds</span>` : '<span class="chip lost">No holder</span>'}${lw.map(p => `<span class="chip lost">${P[p].name} waits</span>`).join('')}</div></button>`;
  }).join('');
  const tot = S.order.filter(p => S.on[p]).reduce((a, p) => [a[0] + P[p].rx, a[1] + P[p].tx], [0, 0]);
  return `<div class="view">
    <div class="head"><div><h1>Routing</h1><p>${sum}${lost ? ` <em>${lost} wanted ${lost === 1 ? 'route is' : 'routes are'} not in effect.</em>` : ''}</p></div>
      <div class="acts"><button class="btn" onclick="disconnectAll()">${Object.values(S.on).some(Boolean) ? 'Disconnect all' : 'Connect all'}</button></div></div>
    <div class="bay"><svg></svg><div class="dests">${dests}</div><div class="tuns">${tuns}</div></div>
    <div id="why"></div>
    <div class="traf"><div class="k"><b>TRAFFIC, LAST 5 MIN</b><span class="mono">↓ ${fmt(tot[0])}</span><span class="mono">↑ ${fmt(tot[1])}</span></div>${spark()}</div></div>`;
}
function why() {
  const d = dest(S.sel), h = holder(S.sel), lw = wanters(S.sel).filter(p => p !== h);
  let t = h ? `<b class="mono">${d.cidr}</b> is held by <b>${P[h].name}</b>.` : `<b class="mono">${d.cidr}</b> has no holder.`;
  t += lw.length ? ` ${list(lw.map(p => P[p].name))} also ${lw.length > 1 ? 'want' : 'wants'} it but come${lw.length > 1 ? '' : 's'} later in priority, so ${lw.length > 1 ? 'those requests wait' : 'that request waits'}.` : h ? ' Nobody else asks for it.' : '';
  const act = lw.length ? `<button class="btn" onclick="raise('${lw[0]}')">Move ${P[lw[0]].name} above ${P[h].name}</button>` : '';
  $('#why').innerHTML = `<div class="why"><span>${t}</span><div class="acts">${act}</div></div>`;
}
function spark() {
  const on = S.order.filter(p => S.on[p]), max = Math.max(...on.map(p => P[p].rx), 1);
  const pts = (p, k) => Array.from({ length: 41 }, (_, i) => {
    const n = .6 + .4 * Math.sin(i * .7 + k * 2) * Math.cos(i * .23 + k);
    return `${i * 25},${82 - (P[p].rx / max) * 70 * n}`;
  });
  return `<svg viewBox="0 0 1000 84" preserveAspectRatio="none" fill="none">${on.map((p, k) => {
    const a = pts(p, k + 1);
    return (k === 0 ? `<path d="M${a.join('L')}L1000,84L0,84Z" fill="${col(p)}" opacity=".14"/>` : '') +
      `<path d="M${a.join('L')}" stroke="${col(p)}" stroke-width="2" vector-effect="non-scaling-stroke"/>`;
  }).join('')}</svg>`;
}
// a small x on the dashed line at t = .8 of its curve: the request that waits
function cross(x1, y1, m, y2, x2, c) {
  const t = .8, u = 1 - t, x = u ** 3 * x1 + 3 * u * u * t * m + 3 * u * t * t * m + t ** 3 * x2, y = u ** 3 * y1 + 3 * u * u * t * y1 + 3 * u * t * t * y2 + t ** 3 * y2;
  return `<path d="M${x - 5} ${y - 5}l10 10M${x + 5} ${y - 5}l-10 10" stroke="${c}" stroke-width="2.4" stroke-linecap="round"/>`;
}
function lines(anim) {
  const bay = $('.bay'); if (!bay || document.body.classList.contains('compact')) return;
  const b = bay.getBoundingClientRect(), svg = $('svg', bay);
  let out = '';
  S.order.filter(p => S.on[p]).forEach(p => {
    const t = $(`.tun[data-p=${p}]`).getBoundingClientRect(), ws = D.filter(d => P[p].wants.includes(d.id));
    ws.forEach((d, i) => {
      const r = $(`.dest[data-d=${d.id}]`).getBoundingClientRect();
      const x1 = r.right - b.left, y1 = r.top + r.height / 2 - b.top, x2 = t.left - b.left, y2 = t.top - b.top + (t.height * (i + 1)) / (ws.length + 1);
      const m = (x1 + x2) / 2, ok = holder(d.id) === p;
      out += `<path class="ln ${ok ? 'held' : 'lost'} ${anim && ok ? 'draw' : ''}" data-p="${p}" data-d="${d.id}" style="--len:${Math.hypot(x2 - x1, y2 - y1) * 1.2 | 0}" stroke="${col(p)}" d="M${x1} ${y1}C${m} ${y1} ${m} ${y2} ${x2} ${y2}"/>`
        + `<circle cx="${x1}" cy="${y1}" r="5" fill="var(--card)" stroke="${col(p)}" stroke-width="3" class="ln-dot"/><circle cx="${x2}" cy="${y2}" r="5" fill="var(--card)" stroke="${col(p)}" stroke-width="3"/>`
        + (ok ? '' : cross(x1, y1, m, y2, x2, col(p)));
    });
  });
  svg.innerHTML = out;
  const hl = (k, v) => $$('.ln', svg).forEach(l => l.classList.toggle('dim', !!v && l.dataset[k] !== v));
  $$('.tun', bay).forEach(e => { e.onmouseenter = () => hl('p', e.dataset.p); e.onmouseleave = () => hl('p', ''); });
  $$('.dest', bay).forEach(e => { e.onmouseenter = () => hl('d', e.dataset.d); e.onmouseleave = () => hl('d', ''); });
}

/* ---------- profile ---------- */
const LOGS = {
  home: [['21:04:09', 'Handshake with vpn.home.example:51820 completed'], ['21:04:09', 'Took 0.0.0.0/0 and DNS 1.1.1.1'], ['21:04:10', 'Took 192.168.1.0/24'], ['21:09:40', 'Network changed: Wi-Fi “Cafe-5G”, gateway 192.168.43.1'], ['21:09:41', 'Routes put right again, 3 changes'], ['21:30:02', 'Keepalive ok, last handshake 38 s ago']],
  office: [['18:27:50', 'TLS handshake with gw.office.example:1194 completed'], ['18:27:52', 'Took 10.20.0.0/16'], ['18:27:52', '192.168.1.0/24 waits: Home holds it', 'w'], ['18:27:52', '0.0.0.0/0 and DNS wait: Home holds them', 'w'], ['21:09:41', 'Routes put right again, 1 change']],
  lab: [['20:23:07', 'Handshake with lab.example.net:51820 completed'], ['20:23:07', 'Took 172.16.8.0/24'], ['20:23:07', '0.0.0.0/0 waits: Home holds it', 'w']],
};
const CONF = {
  home: ['[Interface]', 'PrivateKey = ••••••••••••••••••••••••••••••••', 'Address = 10.8.0.2/32', 'DNS = 1.1.1.1, 1.0.0.1', '', '[Peer]', 'PublicKey = hX3q…7vTk=', 'Endpoint = vpn.home.example:51820', 'AllowedIPs = 0.0.0.0/0, 192.168.1.0/24', 'PersistentKeepalive = 25'],
  office: ['client', 'dev tun', 'proto udp', 'remote gw.office.example 1194', 'cipher AES-256-GCM', 'auth-user-pass', 'route 10.20.0.0 255.255.0.0', 'route 192.168.1.0 255.255.255.0', 'redirect-gateway def1', 'dhcp-option DNS 10.20.0.2'],
  lab: ['[Interface]', 'PrivateKey = ••••••••••••••••••••••••••••••••', 'Address = 172.16.8.5/32', 'DNS = 172.16.8.1', '', '[Peer]', 'PublicKey = q9Lm…2aXc=', 'Endpoint = lab.example.net:51820', 'AllowedIPs = 0.0.0.0/0, 172.16.8.0/24'],
};
const sw = (on, id = '') => `<button class="sw ${on ? 'on' : ''}" role="switch" aria-checked="${on}" onclick="this.classList.toggle('on')"></button>`;

function profile(p, tab) {
  const on = S.on[p], H = held(p), W = waiting(p);
  const tabs = [['overview', 'Overview'], ['routes', 'Routes'], ['logs', 'Logs'], ['config', 'Configuration'], ['settings', 'Settings']];
  let body = '';
  if (tab === 'overview') {
    body = `<div class="panel state ${on ? '' : 'off'}"><div class="big">${on ? '✓' : '–'}</div>
      <div><h2>${on ? 'Connected' : 'Disconnected'}</h2><p>${on ? (H.length ? 'Holds ' + list(names(H)) : 'Connected, every route it wants is held by another profile') : 'Not holding any route'}</p></div>
      ${on ? `<div class="up"><b class="mono">${P[p].up}</b><small>Uptime</small></div>` : ''}
      <button class="btn ${on ? '' : 'pri'}" onclick="toggle('${p}')">${on ? 'Disconnect' : 'Connect'}</button></div>`;
    if (on) body += `<div class="panel"><h3>Traffic</h3><div style="display:flex;gap:56px;padding:10px 18px 0"><div><small style="color:var(--muted)">Received</small><div class="mono" style="font-size:20px;font-weight:600">${fmt(P[p].rx)}</div></div><div><small style="color:var(--muted)">Sent</small><div class="mono" style="font-size:20px;font-weight:600">${fmt(P[p].tx)}</div></div></div><div style="padding:0 18px 14px">${spark1(p)}</div></div>`;
    if (on && W.length) body += `<div class="panel"><div class="warnline"><span>${W.length} wanted ${W.length === 1 ? 'route is' : 'routes are'} not in effect: ${list(names(W))}.</span><button class="btn sm" style="margin-left:auto;color:var(--ink)" onclick="go('profile/${p}/routes')">Show</button></div></div>`;
    body += `<div class="panel rows">${P[p].rows.map(r => `<div><span class="k">${r[0]}</span><span class="v mono">${r[1]}</span></div>`).join('')}</div>`;
  } else if (tab === 'routes') {
    body = `<div class="panel"><table><tr><th>Destination</th><th>What</th><th>State</th></tr>${P[p].wants.map(id => {
      const d = dest(id), h = holder(id);
      const st = !on ? '<span class="st idle">Disconnected</span>' : h === p ? '<span class="st">In effect</span>' : `<span class="st lost">Waiting for ${P[h].name}</span>`;
      return `<tr><td class="mono">${d.cidr}</td><td>${d.label}</td><td>${st}</td></tr>`;
    }).join('')}</table></div>`;
  } else if (tab === 'logs') {
    body = `<div class="panel logbox mono">${LOGS[p].map(l => `<div class="${l[2] || ''}"><span>${l[0]}</span>${l[1]}</div>`).join('')}</div>`;
  } else if (tab === 'config') {
    body = `<div class="panel"><div class="editor mono"><div class="ln-n">${CONF[p].map((_, i) => i + 1).join('\n')}</div><pre>${CONF[p].join('\n')}</pre></div>
      <div style="display:flex;gap:8px;justify-content:flex-end;padding:10px 14px;border-top:1px solid var(--line)"><button class="btn sm">Revert</button><button class="btn sm pri">Save</button></div></div>`;
  } else {
    body = `<div class="panel rows">
      <div><span class="k">Connect at sign-in</span><span class="v"></span>${sw(true)}</div>
      <div><span class="k">Take the default route</span><span class="v"></span>${sw(true)}</div>
      <div><span class="k">Use this profile's DNS</span><span class="v"></span>${sw(true)}</div>
      <div><span class="k">Reconnect after a network change</span><span class="v"></span>${sw(p !== 'lab')}</div></div>
      <div class="panel rows"><div><span class="k">Remove profile</span><span class="v" style="color:var(--muted)">Deletes its configuration and credentials</span><button class="btn sm danger">Delete…</button></div></div>`;
  }
  return `<div class="view" style="--c:${col(p)}"><div class="head"><div><h1>${P[p].name}</h1><p>${P[p].proto} · priority ${S.order.indexOf(p) + 1} of 3</p></div></div>
    <div class="tabs">${tabs.map(t => `<button class="${t[0] === tab ? 'sel' : ''}" onclick="go('profile/${p}/${t[0]}')">${t[1]}</button>`).join('')}</div>${body}</div>`;
}
function spark1(p) {
  const a = Array.from({ length: 41 }, (_, i) => `${i * 25},${70 - 50 * (.6 + .4 * Math.sin(i * .7 + 2) * Math.cos(i * .23 + 1))}`);
  return `<svg viewBox="0 0 1000 80" preserveAspectRatio="none" fill="none" style="width:100%;height:64px;display:block"><path d="M${a.join('L')}L1000,80L0,80Z" fill="${col(p)}" opacity=".14"/><path d="M${a.join('L')}" stroke="${col(p)}" stroke-width="2" vector-effect="non-scaling-stroke"/></svg>`;
}

/* ---------- diagnostics, settings, empty, setup ---------- */
function diagnostics(tab) {
  const tabs = `<div class="tabs"><button class="${tab !== 'log' ? 'sel' : ''}" onclick="go('diagnostics')">Overview</button><button class="${tab === 'log' ? 'sel' : ''}" onclick="go('diagnostics/log')">Daemon log</button></div>`;
  let b;
  if (tab === 'log') {
    b = `<div class="panel logbox mono">${[['21:30:02', 'reconciler: idle, 3 profiles, 5 owned routes'], ['21:09:41', 'reconciler: network change, resync'], ['21:09:41', 'reconciler: wrote journal entry 118 before changing'], ['21:09:41', 'reconciler: 4 changes applied'], ['20:23:07', 'profile lab: connected'], ['18:27:52', 'profile office: connected']].map(l => `<div><span>${l[0]}</span>${l[1]}</div>`).join('')}</div>`;
  } else {
    b = `<div class="panel rows"><h3 style="padding:14px 18px 6px">Network</h3><div><span class="k">Interface</span><span class="v mono">Wi-Fi “Cafe-5G”</span></div><div><span class="k">Gateway</span><span class="v mono">192.168.43.1</span></div><div><span class="k">Resolver</span><span class="v mono">1.1.1.1 via Home</span></div></div>
    <div class="panel"><h3 style="padding-bottom:6px">Routes the daemon owns</h3><table><tr><th>Destination</th><th>Via</th><th>State</th></tr>${D.map(d => { const h = holder(d.id); return `<tr><td class="mono">${d.cidr}</td><td>${h ? P[h].name : '—'}</td><td>${h ? '<span class="st">In effect</span>' : '<span class="st idle">Unowned</span>'}</td></tr>`; }).join('')}</table></div>
    <div class="panel rows"><h3 style="padding:14px 18px 6px">Recent changes</h3><div><span class="k mono" style="width:90px">21:09:41</span><span class="v">Network changed, 4 routes put right again</span></div><div><span class="k mono" style="width:90px">20:23:07</span><span class="v">Lab connected, 172.16.8.0/24 added</span></div></div>
    <div class="panel rows"><h3 style="padding:14px 18px 6px">Stale routes</h3><div><span class="k mono" style="width:150px">10.99.0.0/24</span><span class="v">Left by a profile that is gone</span><button class="btn sm">Remove</button></div></div>`;
  }
  return `<div class="view"><div class="head"><div><h1>Diagnostics</h1><p>What the daemon owns and what changed lately.</p></div><div class="acts"><button class="btn">Resync</button><button class="btn">Copy report</button></div></div>${tabs}${b}</div>`;
}
const settings = () => `<div class="view"><div class="head"><div><h1>Settings</h1></div></div>
  <div class="panel rows"><div><span class="k">Language</span><span class="v">English</span><button class="btn sm">Change</button></div>
  <div><span class="k">Start with Windows</span><span class="v"></span>${sw(true)}</div>
  <div><span class="k">Closing the window</span><span class="v">Keeps Plaitway in the notification area</span>${sw(true)}</div></div>
  <div class="panel rows"><div><span class="k">Helper</span><span class="v"><span class="st">Installed and running</span></span><button class="btn sm">Reinstall</button></div>
  <div><span class="k">Version</span><span class="v mono">0.6.1 · daemon protocol 1</span></div></div></div>`;
const empty = () => `<div class="view center" style="--c:var(--home)"><div class="box"><div class="drop"><div class="lines"><i style="background:var(--home)"></i><i style="background:var(--office)"></i><i style="background:var(--lab)"></i></div><h1>Add your first VPN</h1><p>Drop a WireGuard <span class="mono">.conf</span> or OpenVPN <span class="mono">.ovpn</span> file here.</p><button class="btn pri" style="margin-top:8px">Import profile…</button></div><p>Several profiles can run at once. Plaitway decides who holds which route.</p></div></div>`;
const setup = () => `<div class="view center"><div class="box"><h1>Plaitway needs its helper</h1><p>The helper is a small service that changes your routes and DNS. It installs once and asks Windows for permission.</p><button class="btn pri">Install helper</button><p style="font-size:13px">Without it, profiles can be imported but not connected.</p></div></div>`;

/* ---------- router ---------- */
function route() { return (location.hash.slice(2) || 'routing').split('/'); }
let last = '';
function render() {
  const r = route(), key = r.join('/');
  document.body.classList.toggle('compact', innerWidth < 1000);
  $('.side').innerHTML = side(r);
  const m = $('.main');
  m.innerHTML = r[0] === 'profile' ? profile(r[1], r[2] || 'overview') : r[0] === 'diagnostics' ? diagnostics(r[1]) : r[0] === 'settings' ? settings() : r[0] === 'empty' ? empty() : r[0] === 'setup' ? setup() : routing();
  if (key === last) $('.view', m).style.animation = 'none';
  if (r[0] === 'routing') { why(); lines(key !== last); }
  last = key;
}
const go = h => { location.hash = '/' + h; };
const sel = id => { S.sel = id; render(); };
const toggle = p => { S.on[p] = !S.on[p]; render(); };
const disconnectAll = () => { const any = Object.values(S.on).some(Boolean); Object.keys(S.on).forEach(p => S.on[p] = !any); render(); };
const raise = p => { const h = holder(S.sel); S.order.splice(S.order.indexOf(p), 1); S.order.splice(S.order.indexOf(h), 0, p); render(); };
addEventListener('hashchange', render);
addEventListener('resize', () => { document.body.classList.toggle('compact', innerWidth < 1000); render(); });
const q = new URLSearchParams(location.search);
if (q.get('theme')) document.documentElement.dataset.theme = q.get('theme');
else if (!document.documentElement.dataset.theme && matchMedia('(prefers-color-scheme:dark)').matches) document.documentElement.dataset.theme = 'dark';
if (q.get('off')) q.get('off').split(',').forEach(p => S.on[p] = false);
if (q.get('sel')) S.sel = q.get('sel');
render();
