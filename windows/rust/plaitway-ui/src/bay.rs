//! The Routing page: routes on the left, tunnels on the right, one line per request. A line is solid
//! where the tunnel holds the route and dotted with a cross where it waits behind a higher priority.
//! Below 1000 px of window width the page becomes a list of routes with chips. It draws what the
//! daemon says (`plaitway_core::Model`) and hands back commands; it decides nothing about who holds what.
//! All geometry is in logical pixels; `s` scales it when drawing.

use std::time::Instant;

use plaitway_core::{Model, Standing, State, Tunnel};

use crate::gfx::Gfx;

pub const COMPACT_BELOW: f32 = 1000.0;
const ROUTE_H: f32 = 56.0;
const ROUTE_GAP: f32 = 16.0;
const ROUTE_W: f32 = 260.0;
const TUN_H: f32 = 104.0;
const TUN_GAP: f32 = 14.0;
const CHIP_ROUTE_H: f32 = 78.0;
const DRAW_IN: f32 = 0.6;
const LIST_TOP: f32 = 84.0;

/// (light, dark) colours, one per profile.
const HUES: [(u32, u32); 6] = [(0x0A9B94, 0x3AD0C6), (0xD9680F, 0xFF9A4D), (0xC0306B, 0xFF6B9D), (0x2F6FD0, 0x6FA3FF), (0x3E9B3E, 0x7BD67B), (0x7F4FD0, 0xB48CFF)];

pub struct Pal {
    pub dark: bool,
    pub ink: [f32; 4],
    pub muted: [f32; 4],
    pub line: [f32; 4],
    pub card: [f32; 4],
}

/// What the page asks the app to do.
#[derive(Debug, PartialEq)]
pub enum Out {
    Nothing,
    Redraw,
    SetEnabled(String, bool),
    Reorder(Vec<String>),
}

#[derive(Clone, Copy, PartialEq, Debug)]
pub enum Hover {
    None,
    /// A row of `Model::routes()`.
    Route(usize),
    /// An index into `Model::tunnels`.
    Tun(usize),
}

pub struct Bay {
    pub sel: usize,
    pub hover: Hover,
    pub compact: bool,
    pub reduced_motion: bool,
    btn_hover: Option<usize>,
    /// While dragging: the tunnel being moved and the slot order it is previewed in.
    drag: Option<(usize, Vec<usize>)>,
    anim: Option<Instant>,
    signature: u64,
}

type R = (f32, f32, f32, f32);

struct Layout {
    routes: Vec<R>,
    /// By slot, not by tunnel.
    tuns: Vec<R>,
    btns: Vec<R>,
    why: R,
}

fn inside(r: R, x: f32, y: f32) -> bool {
    x >= r.0 && x < r.0 + r.2 && y >= r.1 && y < r.1 + r.3
}

fn color(hex: u32, a: f32) -> [f32; 4] {
    [((hex >> 16) & 255) as f32 / 255.0, ((hex >> 8) & 255) as f32 / 255.0, (hex & 255) as f32 / 255.0, a]
}

fn c_alpha(c: [f32; 4], a: f32) -> [f32; 4] {
    [c[0], c[1], c[2], a]
}

/// A profile keeps its colour when the priorities change: it comes from the profile's rank by id.
pub fn hue(model: &Model, i: usize, dark: bool) -> [f32; 4] {
    let mut ids = model.ids();
    ids.sort();
    let rank = ids.iter().position(|x| *x == model.tunnels[i].id).unwrap_or(0);
    let (light, night) = HUES[rank % HUES.len()];
    color(if dark { night } else { light }, 1.0)
}

fn title(prefix: &str) -> (String, String) {
    match prefix {
        "0.0.0.0/0" => ("Default route".into(), "All internet traffic".into()),
        "::/0" => ("Default route (IPv6)".into(), "All internet traffic".into()),
        p => (p.into(), String::new()),
    }
}

impl Bay {
    pub fn new() -> Self {
        Bay { sel: 0, hover: Hover::None, compact: false, reduced_motion: false, btn_hover: None, drag: None, anim: Some(Instant::now()), signature: 0 }
    }

    /// Order the tunnels are drawn in: the daemon's, or the preview while one is dragged.
    fn order(&self, n: usize) -> Vec<usize> {
        self.drag.as_ref().map(|d| d.1.clone()).filter(|o| o.len() == n).unwrap_or_else(|| (0..n).collect())
    }

    /// Call after the model changed: the lines draw in again when who holds what changed.
    pub fn sync(&mut self, model: &Model) {
        use std::hash::{Hash, Hasher};
        let mut h = std::collections::hash_map::DefaultHasher::new();
        for t in &model.tunnels {
            (&t.id, t.enabled, t.state as u8).hash(&mut h);
            for r in &t.requests {
                (&r.prefix, r.standing as u8).hash(&mut h);
            }
        }
        let sig = h.finish();
        if sig != self.signature {
            self.signature = sig;
            self.restart_anim();
        }
        self.sel = self.sel.min(model.routes().len().saturating_sub(1));
    }

    fn layout(&self, w: f32, n_routes: usize, n_tuns: usize) -> Layout {
        let y0 = LIST_TOP;
        if self.compact {
            let cw = ((w - 8.0 * (n_tuns.max(1) - 1) as f32) / n_tuns.max(1) as f32).max(40.0);
            let tuns: Vec<R> = (0..n_tuns).map(|k| (k as f32 * (cw + 8.0), y0, cw, 44.0)).collect();
            let routes: Vec<R> = (0..n_routes).map(|i| (0.0, y0 + 44.0 + 20.0 + i as f32 * (CHIP_ROUTE_H + 10.0), w, CHIP_ROUTE_H)).collect();
            let bottom = routes.last().map_or(y0 + 44.0, |r| r.1 + r.3);
            return Layout { routes, btns: tuns.clone(), tuns, why: (0.0, bottom + 20.0, w, 56.0) };
        }
        let tw = (w * 0.28).clamp(250.0, 320.0);
        let routes: Vec<R> = (0..n_routes).map(|i| (0.0, y0 + i as f32 * (ROUTE_H + ROUTE_GAP), ROUTE_W, ROUTE_H)).collect();
        let tuns: Vec<R> = (0..n_tuns).map(|k| (w - tw, y0 + k as f32 * (TUN_H + TUN_GAP), tw, TUN_H)).collect();
        let btns = tuns.iter().map(|t| (t.0 + tw - 16.0 - 96.0, t.1 + TUN_H - 14.0 - 28.0, 96.0, 28.0)).collect();
        let bottom = routes.last().map_or(0.0, |r| r.1 + r.3).max(tuns.last().map_or(0.0, |t| t.1 + TUN_H));
        Layout { routes, tuns, btns, why: (0.0, bottom + 24.0, w, 56.0) }
    }

    /// What is under (`x`, `y`), in logical pixels from the content origin, and whether it is a
    /// connect button. Tunnels are tunnel indexes.
    fn hit(&self, m: &Model, x: f32, y: f32, w: f32) -> (Hover, Option<usize>) {
        let (n, rows) = (m.tunnels.len(), m.routes().len());
        let (l, order) = (self.layout(w, rows, n), self.order(n));
        if let Some(k) = l.btns.iter().position(|r| inside(*r, x, y)) {
            return (Hover::Tun(order[k]), Some(order[k]));
        }
        if let Some(k) = l.tuns.iter().position(|r| inside(*r, x, y)) {
            return (Hover::Tun(order[k]), None);
        }
        if let Some(d) = l.routes.iter().position(|r| inside(*r, x, y)) {
            return (Hover::Route(d), None);
        }
        (Hover::None, None)
    }

    pub fn set_reduced_motion(&mut self, on: bool) {
        self.reduced_motion = on;
        self.restart_anim();
    }

    fn restart_anim(&mut self) {
        self.anim = if self.reduced_motion { None } else { Some(Instant::now()) };
    }

    fn progress(&self) -> f32 {
        match self.anim {
            Some(t) => {
                let p = (t.elapsed().as_secs_f32() / DRAW_IN).min(1.0);
                1.0 - (1.0 - p) * (1.0 - p)
            }
            None => 1.0,
        }
    }

    /// True while the lines are still drawing in; the caller keeps a timer running until then.
    pub fn animating(&self) -> bool {
        self.anim.is_some_and(|t| t.elapsed().as_secs_f32() < DRAW_IN)
    }

    pub fn mouse_move(&mut self, m: &Model, x: f32, y: f32, w: f32) -> bool {
        if self.drag.is_some() {
            // The dragged tunnel takes whichever slot the pointer is over.
            let slot = (((y - LIST_TOP) / (TUN_H + TUN_GAP)).floor().max(0.0) as usize).min(m.tunnels.len() - 1);
            let (p, order) = self.drag.as_mut().unwrap();
            let at = order.iter().position(|q| q == p).unwrap();
            let moved = slot != at;
            if moved {
                order.remove(at);
                order.insert(slot, *p);
                self.restart_anim();
            }
            return moved;
        }
        let (h, b) = self.hit(m, x, y, w);
        let changed = h != self.hover || b != self.btn_hover;
        self.hover = h;
        self.btn_hover = b;
        changed
    }

    pub fn leave(&mut self) -> bool {
        let changed = self.hover != Hover::None;
        self.hover = Hover::None;
        self.btn_hover = None;
        changed
    }

    /// Press: a button toggles, a route is selected, a tunnel card starts a drag (wide layout only).
    pub fn press(&mut self, m: &Model, x: f32, y: f32, w: f32) -> Out {
        match self.hit(m, x, y, w) {
            (_, Some(t)) => Out::SetEnabled(m.tunnels[t].id.clone(), !m.tunnels[t].enabled),
            (Hover::Route(d), _) => {
                self.sel = d;
                Out::Redraw
            }
            (Hover::Tun(t), _) if !self.compact => {
                self.drag = Some((t, (0..m.tunnels.len()).collect()));
                Out::Redraw
            }
            _ => Out::Nothing,
        }
    }

    /// Release: ends a drag, and asks for the new priority order when it changed.
    pub fn release(&mut self, m: &Model) -> Out {
        match self.drag.take() {
            None => Out::Nothing,
            Some((_, order)) if order.iter().copied().eq(0..order.len()) => Out::Redraw,
            Some((_, order)) => Out::Reorder(order.iter().map(|&i| m.tunnels[i].id.clone()).collect()),
        }
    }

    pub fn dragging(&self) -> bool {
        self.drag.is_some()
    }

    /// Up and Down walk the route list.
    pub fn key(&mut self, m: &Model, vk: u32) -> bool {
        let last = m.routes().len().saturating_sub(1);
        let sel = match vk {
            0x26 => self.sel.saturating_sub(1),
            0x28 => (self.sel + 1).min(last),
            0x24 => 0,
            0x23 => last,
            _ => return false,
        };
        let changed = sel != self.sel;
        self.sel = sel;
        changed
    }

    /// Height of the page below its origin, for scrolling.
    pub fn height(&self, m: &Model, w: f32) -> f32 {
        let l = self.layout(w, m.routes().len(), m.tunnels.len());
        l.why.1 + l.why.3
    }

    /// Draw at logical origin (`ox`, `oy`), `w` wide. `f` is (text, bold).
    pub fn draw(&self, g: &mut Gfx, f: (usize, usize), pal: &Pal, m: &Model, s: f32, ox: f32, oy: f32, w: f32) {
        let routes = m.routes();
        let l = self.layout(w, routes.len(), m.tunnels.len());
        let order = self.order(m.tunnels.len());
        let at = |r: R| (ox * s + r.0 * s, oy * s + r.1 * s, r.2 * s, r.3 * s);
        let held = routes.iter().filter(|r| r.holder.is_some()).count();

        g.text(f.1, 28.0 * s, ox * s, oy * s + 14.0 * s, "Routing", pal.ink);
        let sub = format!("{} of {} routes are held by a tunnel", held, routes.len());
        g.text(f.0, 14.0 * s, ox * s, oy * s + 50.0 * s, &sub, pal.muted);

        if !self.compact {
            self.draw_lines(g, pal, m, &routes, &order, s, ox, oy, &l);
        }

        for (d, row) in routes.iter().enumerate() {
            let (x, y, rw, rh) = at(l.routes[d]);
            g.rect(x, y, rw, rh, 8.0 * s, pal.card);
            if self.sel == d {
                g.outline(x, y, rw, rh, 8.0 * s, 2.0 * s, pal.ink);
            } else {
                let mut c = pal.line;
                if self.hover == Hover::Route(d) {
                    c[3] *= 2.5;
                }
                g.outline(x, y, rw, rh, 8.0 * s, 1.0, c);
            }
            let (name, mut desc) = title(&row.prefix);
            if desc.is_empty() {
                let names: Vec<&str> = row.requests.iter().map(|r| m.tunnels[r.0].name.as_str()).collect();
                desc = format!("Asked by {}", names.join(", "));
            }
            let room = rw - 28.0 * s - if self.compact { 0.0 } else { 24.0 * s };
            g.text_fit(f.1, 15.0 * s, x + 14.0 * s, y + 20.0 * s, &name, pal.ink, room);
            g.text_fit(f.0, 12.0 * s, x + 14.0 * s, y + 40.0 * s, &desc, pal.muted, room);
            if self.compact {
                // "Home holds" and "Office waits" chips, in priority order.
                let mut cx = x + 14.0 * s;
                let cy = y + 62.0 * s;
                for &(t, standing) in &row.requests {
                    let label = format!("{} {}", m.tunnels[t].name, verb(standing));
                    let cw = g.measure(f.0, 12.0 * s, &label) + 18.0 * s;
                    let c = hue(m, t, pal.dark);
                    if standing == Standing::Holds {
                        g.rect(cx, cy - 10.0 * s, cw, 20.0 * s, 10.0 * s, c_alpha(c, 0.18));
                        g.outline(cx, cy - 10.0 * s, cw, 20.0 * s, 10.0 * s, 1.0, c_alpha(c, 0.5));
                    } else {
                        g.outline(cx, cy - 10.0 * s, cw, 20.0 * s, 10.0 * s, 1.0, pal.line);
                    }
                    g.text(f.0, 12.0 * s, cx + 9.0 * s, cy, &label, if standing == Standing::Holds { pal.ink } else { pal.muted });
                    cx += cw + 6.0 * s;
                }
            } else if let Some(t) = row.holder {
                g.rect(x + rw - 22.0 * s, y + rh / 2.0 - 5.0 * s, 10.0 * s, 10.0 * s, 5.0 * s, hue(m, t, pal.dark));
            }
        }

        for (k, &t) in order.iter().enumerate() {
            self.draw_tunnel(g, f, pal, m, t, k, at(l.tuns[k]), at(l.btns[k]), s);
        }

        // Why strip for the selected route.
        let (x, y, ww, wh) = at(l.why);
        g.rect(x, y, ww, wh, 8.0 * s, pal.card);
        g.outline(x, y, ww, wh, 8.0 * s, 1.0, pal.line);
        let text = routes.get(self.sel).map_or_else(|| "No route is asked for yet".to_string(), |row| why(m, row));
        g.text_fit(f.1, 14.0 * s, x + 16.0 * s, y + wh / 2.0, &text, pal.ink, ww - 32.0 * s);
    }

    #[allow(clippy::too_many_arguments)]
    fn draw_tunnel(&self, g: &mut Gfx, f: (usize, usize), pal: &Pal, m: &Model, t: usize, slot: usize, card: R, btn: R, s: f32) {
        let tun = &m.tunnels[t];
        let c = hue(m, t, pal.dark);
        let on = tun.enabled;
        let (x, y, tw, th) = card;
        if self.compact {
            g.rect(x, y, tw, th, 8.0 * s, if on { c_alpha(c, 0.13) } else { pal.card });
            g.outline(x, y, tw, th, 8.0 * s, 1.0, if on { c_alpha(c, 0.28) } else { pal.line });
            g.text_fit(f.1, 13.0 * s, x + 14.0 * s, y + th / 2.0, &format!("{} · {}", tun.name, if on { "On" } else { "Off" }), pal.ink, tw - 28.0 * s);
            return;
        }
        let lifted = matches!(self.drag, Some((d, _)) if d == t);
        if on {
            g.rect(x, y, tw, th, 10.0 * s, c_alpha(c, if lifted { 0.22 } else { 0.13 }));
            g.outline(x, y, tw, th, 10.0 * s, 1.0, c_alpha(c, 0.28));
            g.rect(x + 16.0 * s, y + 22.0 * s - 5.0 * s, 10.0 * s, 10.0 * s, 5.0 * s, c);
        } else {
            g.outline(x, y, tw, th, 10.0 * s, 1.0, pal.line);
            g.outline(x + 16.0 * s, y + 22.0 * s - 5.0 * s, 10.0 * s, 10.0 * s, 5.0 * s, 2.0 * s, pal.muted);
        }
        let kind = kind_name(tun);
        let pw = g.measure(f.1, 11.0 * s, kind) + 14.0 * s;
        g.text_fit(f.1, 16.0 * s, x + 34.0 * s, y + 22.0 * s, &tun.name, pal.ink, tw - 34.0 * s - pw - 28.0 * s);
        g.outline(x + tw - 16.0 * s - pw, y + 13.0 * s, pw, 18.0 * s, 9.0 * s, 1.0, pal.line);
        g.text(f.1, 11.0 * s, x + tw - 16.0 * s - pw + 7.0 * s, y + 22.0 * s, kind, pal.muted);
        g.text_fit(f.0, 13.0 * s, x + 16.0 * s, y + 50.0 * s, &state_line(tun, slot), pal.muted, tw - 32.0 * s);

        let (bx, by, bw, bh) = btn;
        g.rect(bx, by, bw, bh, 6.0 * s, if self.btn_hover == Some(t) { c_alpha(pal.ink, 0.1) } else { pal.card });
        g.outline(bx, by, bw, bh, 6.0 * s, 1.0, pal.line);
        let label = if on { "Disconnect" } else { "Connect" };
        let lw = g.measure(f.0, 13.0 * s, label);
        g.text(f.0, 13.0 * s, bx + (bw - lw) / 2.0, by + bh / 2.0, label, pal.ink);
    }

    #[allow(clippy::too_many_arguments)]
    fn draw_lines(&self, g: &mut Gfx, pal: &Pal, m: &Model, routes: &[plaitway_core::RouteRow], order: &[usize], s: f32, ox: f32, oy: f32, l: &Layout) {
        let progress = self.progress();
        for (k, &t) in order.iter().enumerate() {
            let mut n = 0;
            for (d, row) in routes.iter().enumerate() {
                let Some(&(_, standing)) = row.requests.iter().find(|r| r.0 == t) else { continue };
                let lit = match self.hover {
                    Hover::None => true,
                    Hover::Route(r) => r == d,
                    Hover::Tun(h) => h == t,
                };
                let mut c = hue(m, t, pal.dark);
                c[3] = if lit { if standing == Standing::Holds { 1.0 } else { 0.7 } } else { 0.12 };
                let (x0, y0) = (ox * s + (l.routes[d].0 + ROUTE_W) * s, oy * s + (l.routes[d].1 + ROUTE_H / 2.0) * s);
                let (x1, y1) = (ox * s + l.tuns[k].0 * s, oy * s + (l.tuns[k].1 + 26.0 + n as f32 * 14.0) * s);
                n += 1;
                let pts = bezier(x0, y0, x1, y1);
                let shown = ((pts.len() - 1) as f32 * progress).ceil() as usize;
                let part = &pts[..=shown.min(pts.len() - 1)];
                if standing == Standing::Holds {
                    for seg in part.windows(2) {
                        g.line_flat(seg[0].0, seg[0].1, seg[1].0, seg[1].1, 3.0 * s, c);
                    }
                } else {
                    dotted(g, part, 2.5 * s, c);
                    if progress >= 0.85 {
                        let p = pts[pts.len() * 4 / 5];
                        let a = 4.5 * s;
                        let x = c_alpha(pal.ink, c[3]);
                        g.line(p.0 - a, p.1 - a, p.0 + a, p.1 + a, 2.0 * s, x);
                        g.line(p.0 - a, p.1 + a, p.0 + a, p.1 - a, 2.0 * s, x);
                    }
                }
            }
        }
    }
}

fn verb(s: Standing) -> &'static str {
    match s {
        Standing::Holds => "holds",
        Standing::Waits => "waits",
        Standing::Blocked => "is blocked",
        Standing::Failed => "failed",
        Standing::Pending => "is pending",
    }
}

pub fn kind_name(t: &Tunnel) -> &'static str {
    match t.kind {
        plaitway_core::Kind::WireGuard => "WireGuard",
        plaitway_core::Kind::OpenVpn => "OpenVPN",
    }
}

fn state_line(t: &Tunnel, slot: usize) -> String {
    match t.state {
        State::Connected => {
            let (h, w) = t.holds();
            format!("Priority {} · Holds {} of {} routes", slot + 1, h, w)
        }
        State::Connecting | State::Reconnecting => format!("Priority {} · Connecting", slot + 1),
        State::Disconnecting => format!("Priority {} · Disconnecting", slot + 1),
        State::AwaitingCredentials => format!("Priority {} · Needs credentials", slot + 1),
        State::Failed if !t.error.is_empty() => t.error.clone(),
        State::Failed => format!("Priority {} · Failed", slot + 1),
        State::Disconnected => format!("Priority {} · Not connected", slot + 1),
    }
}

/// The sentence under the page: who holds the route and who waits behind.
pub fn why(m: &Model, row: &plaitway_core::RouteRow) -> String {
    let name = title(&row.prefix).0;
    let Some(h) = row.holder else {
        return format!("{name}: no tunnel holds it");
    };
    let waiting: Vec<&str> = row.requests.iter().filter(|r| r.0 != h && r.1 == Standing::Waits).map(|r| m.tunnels[r.0].name.as_str()).collect();
    if waiting.is_empty() {
        format!("{name}: {} holds it", m.tunnels[h].name)
    } else {
        format!("{name}: {} holds it, {} waits behind", m.tunnels[h].name, waiting.join(" and "))
    }
}

/// A cubic with horizontal tangents at both ends, flattened.
fn bezier(x0: f32, y0: f32, x1: f32, y1: f32) -> Vec<(f32, f32)> {
    let dx = (x1 - x0) * 0.5;
    let (c0, c1) = ((x0 + dx, y0), (x1 - dx, y1));
    (0..=28)
        .map(|i| {
            let t = i as f32 / 28.0;
            let u = 1.0 - t;
            let (a, b, c, d) = (u * u * u, 3.0 * u * u * t, 3.0 * u * t * t, t * t * t);
            (a * x0 + b * c0.0 + c * c1.0 + d * x1, a * y0 + b * c0.1 + c * c1.1 + d * y1)
        })
        .collect()
}

/// Round dots along a polyline: a dash of 0.2 widths every 3 widths.
fn dotted(g: &mut Gfx, pts: &[(f32, f32)], width: f32, c: [f32; 4]) {
    let (dash, gap) = (0.2 * width, 2.8 * width);
    let mut carry = 0.0; // distance already walked into the current dash+gap period
    for seg in pts.windows(2) {
        let (dx, dy) = (seg[1].0 - seg[0].0, seg[1].1 - seg[0].1);
        let len = (dx * dx + dy * dy).sqrt();
        let mut t = -carry;
        while t < len {
            let (a, b) = (t.max(0.0), (t + dash).min(len));
            if b > a {
                g.line(seg[0].0 + dx * a / len, seg[0].1 + dy * a / len, seg[0].0 + dx * b / len, seg[0].1 + dy * b / len, width, c);
            }
            t += dash + gap;
        }
        carry = (carry + len) % (dash + gap);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use plaitway_core::{Kind, Request};

    const W: f32 = 968.0;

    fn tun(id: &str, priority: i32, asks: &[(&str, Standing)]) -> Tunnel {
        Tunnel {
            id: id.into(),
            name: id.to_uppercase(),
            kind: Kind::WireGuard,
            enabled: !asks.is_empty(),
            state: if asks.is_empty() { State::Disconnected } else { State::Connected },
            priority,
            error: String::new(),
            wanted: asks.len(),
            since: None,
            requests: asks.iter().map(|(p, s)| Request { prefix: p.to_string(), standing: *s, shadowed_by: None }).collect(),
        }
    }

    /// Home holds the default route and 192.168.1.0/24; Office waits on both and holds 10.20.0.0/16; Lab holds its own.
    fn model() -> Model {
        use Standing::*;
        Model { tunnels: vec![tun("home", 0, &[("0.0.0.0/0", Holds), ("192.168.1.0/24", Holds)]), tun("office", 1, &[("0.0.0.0/0", Waits), ("192.168.1.0/24", Waits), ("10.20.0.0/16", Holds)]), tun("lab", 2, &[("172.16.8.0/24", Holds)])] }
    }

    fn bay() -> Bay {
        let mut b = Bay::new();
        b.reduced_motion = true;
        b
    }

    fn centre(r: R) -> (f32, f32) {
        (r.0 + r.2 / 2.0, r.1 + r.3 / 2.0)
    }

    #[test]
    fn pressing_a_button_asks_for_the_opposite_state() {
        let (mut b, m) = (bay(), model());
        let l = b.layout(W, 4, 3);
        let (x, y) = centre(l.btns[0]);
        assert_eq!(b.press(&m, x, y, W), Out::SetEnabled("home".into(), false));
        let (x, y) = centre(l.btns[2]);
        let mut m2 = m.clone();
        m2.tunnels[2].enabled = false;
        assert_eq!(b.press(&m2, x, y, W), Out::SetEnabled("lab".into(), true));
    }

    #[test]
    fn dragging_a_tunnel_up_asks_for_a_new_priority_order() {
        let (mut b, m) = (bay(), model());
        let l = b.layout(W, 4, 3);
        let t = l.tuns[1];
        assert_eq!(b.press(&m, t.0 + 20.0, t.1 + 20.0, W), Out::Redraw);
        assert!(b.dragging());
        assert!(b.mouse_move(&m, t.0 + 20.0, l.tuns[0].1 + 20.0, W));
        assert_eq!(b.release(&m), Out::Reorder(vec!["office".into(), "home".into(), "lab".into()]));
        assert!(!b.dragging());
    }

    #[test]
    fn a_drag_that_ends_where_it_began_asks_for_nothing() {
        let (mut b, m) = (bay(), model());
        let t = b.layout(W, 4, 3).tuns[1];
        b.press(&m, t.0 + 20.0, t.1 + 20.0, W);
        b.mouse_move(&m, t.0 + 20.0, t.1 + 30.0, W);
        assert_eq!(b.release(&m), Out::Redraw);
    }

    #[test]
    fn dragging_past_the_ends_stays_in_range() {
        let (mut b, m) = (bay(), model());
        let t = b.layout(W, 4, 3).tuns[0];
        b.press(&m, t.0 + 20.0, t.1 + 20.0, W);
        b.mouse_move(&m, t.0 + 20.0, 5000.0, W);
        assert_eq!(b.release(&m), Out::Reorder(vec!["office".into(), "lab".into(), "home".into()]));
    }

    #[test]
    fn clicking_a_route_selects_it_and_keys_walk_the_list() {
        let (mut b, m) = (bay(), model());
        let r = b.layout(W, 4, 3).routes[2];
        assert_eq!(b.press(&m, r.0 + 10.0, r.1 + 10.0, W), Out::Redraw);
        assert!(!b.dragging());
        assert_eq!(b.sel, 2);
        assert!(b.key(&m, 0x28));
        assert_eq!(b.sel, 3);
        assert!(!b.key(&m, 0x28), "stops at the last route");
        assert!(b.key(&m, 0x24));
        assert_eq!(b.sel, 0);
        assert!(!b.key(&m, 0x41));
    }

    #[test]
    fn compact_layout_toggles_from_the_chips_and_never_drags() {
        let (mut b, m) = (bay(), model());
        b.compact = true;
        let t = b.layout(W, 4, 3).tuns[2];
        assert_eq!(b.press(&m, t.0 + 10.0, t.1 + 10.0, W), Out::SetEnabled("lab".into(), false));
        assert!(!b.dragging());
    }

    #[test]
    fn hover_changes_are_reported_once() {
        let (mut b, m) = (bay(), model());
        let r = b.layout(W, 4, 3).routes[1];
        assert!(b.mouse_move(&m, r.0 + 5.0, r.1 + 5.0, W));
        assert!(!b.mouse_move(&m, r.0 + 6.0, r.1 + 6.0, W));
        assert_eq!(b.hover, Hover::Route(1));
        assert!(b.leave());
        assert!(!b.leave());
    }

    #[test]
    fn the_page_copes_with_any_number_of_tunnels_and_routes() {
        let (mut b, empty) = (bay(), Model::default());
        assert_eq!(b.press(&empty, 10.0, 10.0, W), Out::Nothing);
        assert!(!b.key(&empty, 0x28));
        b.sync(&empty);
        let many = Model { tunnels: (0..6).map(|i| tun(&format!("p{i}"), i, &[("0.0.0.0/0", if i == 0 { Standing::Holds } else { Standing::Waits })])).collect() };
        let l = b.layout(W, 1, 6);
        assert_eq!(l.tuns.len(), 6);
        let t = l.tuns[5];
        assert!(matches!(b.press(&many, t.0 + 5.0, t.1 + 5.0, W), Out::Redraw));
    }

    #[test]
    fn colours_stay_with_the_profile_when_the_priorities_change() {
        let m = model();
        let mut swapped = m.clone();
        swapped.tunnels.swap(0, 2);
        assert_eq!(hue(&m, 0, true), hue(&swapped, 2, true));
        assert_ne!(hue(&m, 0, true), hue(&m, 1, true));
    }

    #[test]
    fn the_sentence_names_who_holds_and_who_waits() {
        let m = model();
        let routes = m.routes();
        assert_eq!(why(&m, &routes[0]), "Default route: HOME holds it, OFFICE waits behind");
        assert_eq!(why(&m, &routes[2]), "10.20.0.0/16: OFFICE holds it");
    }

    #[test]
    fn the_lines_draw_in_again_only_when_something_changed() {
        let mut b = Bay::new();
        let m = model();
        b.sync(&m);
        b.set_reduced_motion(true);
        assert!(!b.animating());
        b.reduced_motion = false;
        b.sync(&m);
        assert!(!b.animating(), "same model, no new animation");
        let mut m2 = m.clone();
        m2.tunnels[0].requests[0].standing = Standing::Waits;
        b.sync(&m2);
        assert!(b.animating());
    }
}



