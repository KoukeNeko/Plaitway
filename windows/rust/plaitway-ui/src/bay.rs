//! The Routing page on fake data: routes on the left, tunnels on the right, one line per request.
//! A line is solid where the tunnel holds the route and dotted with a cross where it waits behind a
//! higher priority. Below 1000 px of window width the page becomes a list of routes with chips.
//! All geometry is in logical pixels; `s` scales it when drawing.

use std::time::Instant;

use crate::gfx::Gfx;

pub const PROFILES: [(&str, &str, u32, u32); 3] = [("Home", "WireGuard", 0x0A9B94, 0x3AD0C6), ("Office", "OpenVPN", 0xD9680F, 0xFF9A4D), ("Lab", "WireGuard", 0xC0306B, 0xFF6B9D)];
const DESTS: [(&str, &str); 5] = [("Default route", "All internet traffic"), ("DNS", "Name lookups"), ("192.168.1.0/24", "Shared office network"), ("10.20.0.0/16", "Corporate servers"), ("172.16.8.0/24", "Lab network")];
const WANTS: [[bool; 5]; 3] = [[true, true, true, false, false], [true, true, true, true, false], [false, false, true, false, true]];

pub const COMPACT_BELOW: f32 = 1000.0;
const ROUTE_H: f32 = 56.0;
const ROUTE_GAP: f32 = 16.0;
const ROUTE_W: f32 = 260.0;
const TUN_H: f32 = 104.0;
const TUN_GAP: f32 = 14.0;
const CHIP_ROUTE_H: f32 = 78.0;
const DRAW_IN: f32 = 0.6;

pub struct Pal {
    pub dark: bool,
    pub ink: [f32; 4],
    pub muted: [f32; 4],
    pub line: [f32; 4],
    pub card: [f32; 4],
}

#[derive(Clone, Copy, PartialEq, Debug)]
pub enum Hover {
    None,
    Route(usize),
    Tun(usize),
}

pub struct Bay {
    pub on: [bool; 3],
    /// Priority order: `order[0]` is the profile that wins every contested route.
    pub order: [usize; 3],
    pub sel: usize,
    pub hover: Hover,
    pub compact: bool,
    pub reduced_motion: bool,
    btn_hover: Option<usize>,
    drag: Option<usize>,
    anim: Option<Instant>,
}

type R = (f32, f32, f32, f32);

struct Layout {
    routes: [R; 5],
    /// By priority slot, not by profile.
    tuns: [R; 3],
    btns: [R; 3],
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

impl Bay {
    pub fn new() -> Self {
        Bay { on: [true; 3], order: [0, 1, 2], sel: 0, hover: Hover::None, compact: false, reduced_motion: false, btn_hover: None, drag: None, anim: Some(Instant::now()) }
    }

    pub fn dest(&self, d: usize) -> (&'static str, &'static str) {
        DESTS[d]
    }

    /// How profile `p` stands on route `d`, or None when it does not ask for it.
    pub fn status(&self, p: usize, d: usize) -> Option<&'static str> {
        if !WANTS[p][d] {
            None
        } else if !self.on[p] {
            Some("Not connected")
        } else if self.holder(d) == Some(p) {
            Some("Holds")
        } else {
            Some("Waits")
        }
    }

    pub fn toggle(&mut self, p: usize) {
        self.on[p] = !self.on[p];
        self.restart_anim();
    }

    fn slot(&self, p: usize) -> usize {
        self.order.iter().position(|&q| q == p).unwrap()
    }

    /// The first connected profile in priority order that wants the route holds it.
    pub fn holder(&self, d: usize) -> Option<usize> {
        self.order.iter().copied().find(|&p| self.on[p] && WANTS[p][d])
    }

    /// (routes held, routes wanted) for a profile.
    pub fn holds(&self, p: usize) -> (usize, usize) {
        ((0..5).filter(|&d| self.holder(d) == Some(p)).count(), WANTS[p].iter().filter(|w| **w).count())
    }

    fn layout(&self, w: f32) -> Layout {
        let y0 = 84.0;
        if self.compact {
            let cw = (w - 16.0) / 3.0;
            let tuns = std::array::from_fn(|k| (k as f32 * (cw + 8.0), y0, cw, 44.0));
            let routes = std::array::from_fn(|i| (0.0, y0 + 44.0 + 20.0 + i as f32 * (CHIP_ROUTE_H + 10.0), w, CHIP_ROUTE_H));
            let bottom = routes[4].1 + CHIP_ROUTE_H;
            return Layout { routes, tuns, btns: tuns, why: (0.0, bottom + 20.0, w, 56.0) };
        }
        let tw = (w * 0.28).clamp(250.0, 320.0);
        let routes = std::array::from_fn(|i| (0.0, y0 + i as f32 * (ROUTE_H + ROUTE_GAP), ROUTE_W, ROUTE_H));
        let tuns: [R; 3] = std::array::from_fn(|k| (w - tw, y0 + k as f32 * (TUN_H + TUN_GAP), tw, TUN_H));
        let btns = std::array::from_fn(|k| (tuns[k].0 + tw - 16.0 - 96.0, tuns[k].1 + TUN_H - 14.0 - 28.0, 96.0, 28.0));
        let bottom = (routes[4].1 + ROUTE_H).max(tuns[2].1 + TUN_H);
        Layout { routes, tuns, btns, why: (0.0, bottom + 24.0, w, 56.0) }
    }

    /// What is under (`x`, `y`), in logical pixels from the content origin, and whether it is a
    /// connect button.
    fn hit(&self, x: f32, y: f32, w: f32) -> (Hover, Option<usize>) {
        let l = self.layout(w);
        if let Some(k) = (0..3).find(|&k| inside(l.btns[k], x, y)) {
            return (Hover::Tun(self.order[k]), Some(self.order[k]));
        }
        if let Some(k) = (0..3).find(|&k| inside(l.tuns[k], x, y)) {
            return (Hover::Tun(self.order[k]), None);
        }
        if let Some(d) = (0..5).find(|&d| inside(l.routes[d], x, y)) {
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

    /// Returns true when the picture changes.
    pub fn mouse_move(&mut self, x: f32, y: f32, w: f32) -> bool {
        if let Some(p) = self.drag {
            // The dragged tunnel takes whichever slot the pointer is over.
            let l = self.layout(w);
            let slot = (((y - l.tuns[0].1) / (TUN_H + TUN_GAP)).floor().max(0.0) as usize).min(2);
            if slot != self.slot(p) {
                let mut o: Vec<usize> = self.order.iter().copied().filter(|&q| q != p).collect();
                o.insert(slot, p);
                self.order = [o[0], o[1], o[2]];
                self.restart_anim();
                return true;
            }
            return false;
        }
        let (h, b) = self.hit(x, y, w);
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
    pub fn press(&mut self, x: f32, y: f32, w: f32) -> bool {
        match self.hit(x, y, w) {
            (_, Some(p)) => {
                self.on[p] = !self.on[p];
                self.restart_anim();
            }
            (Hover::Route(d), _) => self.sel = d,
            (Hover::Tun(p), _) if !self.compact => self.drag = Some(p),
            _ => return false,
        }
        true
    }

    /// Release: ends a drag. True when a drag was in progress.
    pub fn release(&mut self) -> bool {
        self.drag.take().is_some()
    }

    pub fn dragging(&self) -> bool {
        self.drag.is_some()
    }

    /// Up and Down walk the route list.
    pub fn key(&mut self, vk: u32) -> bool {
        let sel = match vk {
            0x26 => self.sel.saturating_sub(1),
            0x28 => (self.sel + 1).min(4),
            0x24 => 0,
            0x23 => 4,
            _ => return false,
        };
        let changed = sel != self.sel;
        self.sel = sel;
        changed
    }

    /// Draw at logical origin (`ox`, `oy`), `w` wide. `f` is (text, bold).
    pub fn draw(&self, g: &mut Gfx, f: (usize, usize), pal: &Pal, s: f32, ox: f32, oy: f32, w: f32) {
        let l = self.layout(w);
        let hue = |p: usize| color(if pal.dark { PROFILES[p].3 } else { PROFILES[p].2 }, 1.0);
        let at = |r: R| (ox * s + r.0 * s, oy * s + r.1 * s, r.2 * s, r.3 * s);
        let held_total = (0..5).filter(|&d| self.holder(d).is_some()).count();

        g.text(f.1, 28.0 * s, ox * s, oy * s + 14.0 * s, "Routing", pal.ink);
        let sub = format!("{} of 5 routes are held by a tunnel", held_total);
        g.text(f.0, 14.0 * s, ox * s, oy * s + 50.0 * s, &sub, pal.muted);

        if !self.compact {
            self.draw_lines(g, pal, s, ox, oy, &l, &hue);
        }

        for d in 0..5 {
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
            g.text(f.1, 15.0 * s, x + 14.0 * s, y + 20.0 * s, DESTS[d].0, pal.ink);
            g.text(f.0, 12.0 * s, x + 14.0 * s, y + 40.0 * s, DESTS[d].1, pal.muted);
            if self.compact {
                // "Home holds" and "Office waits" chips, in priority order.
                let mut cx = x + 14.0 * s;
                let cy = y + 62.0 * s;
                for &p in self.order.iter().filter(|&&p| self.on[p] && WANTS[p][d]) {
                    let held = self.holder(d) == Some(p);
                    let label = format!("{} {}", PROFILES[p].0, if held { "holds" } else { "waits" });
                    let cw = g.measure(f.0, 12.0 * s, &label) + 18.0 * s;
                    let c = hue(p);
                    if held {
                        g.rect(cx, cy - 10.0 * s, cw, 20.0 * s, 10.0 * s, c_alpha(c, 0.18));
                        g.outline(cx, cy - 10.0 * s, cw, 20.0 * s, 10.0 * s, 1.0, c_alpha(c, 0.5));
                    } else {
                        g.outline(cx, cy - 10.0 * s, cw, 20.0 * s, 10.0 * s, 1.0, pal.line);
                    }
                    g.text(f.0, 12.0 * s, cx + 9.0 * s, cy, &label, if held { pal.ink } else { pal.muted });
                    cx += cw + 6.0 * s;
                }
            } else if let Some(p) = self.holder(d) {
                g.rect(x + rw - 22.0 * s, y + rh / 2.0 - 5.0 * s, 10.0 * s, 10.0 * s, 5.0 * s, hue(p));
            }
        }

        for k in 0..3 {
            let p = self.order[k];
            let on = self.on[p];
            let c = hue(p);
            let (x, y, tw, th) = at(l.tuns[k]);
            if self.compact {
                g.rect(x, y, tw, th, 8.0 * s, if on { c_alpha(c, 0.13) } else { pal.card });
                g.outline(x, y, tw, th, 8.0 * s, 1.0, if on { c_alpha(c, 0.28) } else { pal.line });
                let label = format!("{} · {}", PROFILES[p].0, if on { "On" } else { "Off" });
                g.text(f.1, 13.0 * s, x + 14.0 * s, y + th / 2.0, &label, pal.ink);
                continue;
            }
            let lifted = self.drag == Some(p);
            if on {
                g.rect(x, y, tw, th, 10.0 * s, c_alpha(c, if lifted { 0.22 } else { 0.13 }));
                g.outline(x, y, tw, th, 10.0 * s, 1.0, c_alpha(c, 0.28));
                g.rect(x + 16.0 * s, y + 22.0 * s - 5.0 * s, 10.0 * s, 10.0 * s, 5.0 * s, c);
            } else {
                g.outline(x, y, tw, th, 10.0 * s, 1.0, pal.line);
                g.outline(x + 16.0 * s, y + 22.0 * s - 5.0 * s, 10.0 * s, 10.0 * s, 5.0 * s, 2.0 * s, pal.muted);
            }
            g.text(f.1, 16.0 * s, x + 34.0 * s, y + 22.0 * s, PROFILES[p].0, pal.ink);
            let proto = PROFILES[p].1;
            let pw = g.measure(f.1, 11.0 * s, proto) + 14.0 * s;
            g.outline(x + tw - 16.0 * s - pw, y + 13.0 * s, pw, 18.0 * s, 9.0 * s, 1.0, pal.line);
            g.text(f.1, 11.0 * s, x + tw - 16.0 * s - pw + 7.0 * s, y + 22.0 * s, proto, pal.muted);
            let (held, wanted) = self.holds(p);
            let line = if on { format!("Priority {} · Holds {} of {} routes", k + 1, held, wanted) } else { format!("Priority {} · Not connected", k + 1) };
            g.text(f.0, 13.0 * s, x + 16.0 * s, y + 50.0 * s, &line, pal.muted);

            let (bx, by, bw, bh) = at(l.btns[k]);
            g.rect(bx, by, bw, bh, 6.0 * s, if self.btn_hover == Some(p) { c_alpha(pal.ink, 0.1) } else { pal.card });
            g.outline(bx, by, bw, bh, 6.0 * s, 1.0, pal.line);
            let label = if on { "Disconnect" } else { "Connect" };
            let lw = g.measure(f.0, 13.0 * s, label);
            g.text(f.0, 13.0 * s, bx + (bw - lw) / 2.0, by + bh / 2.0, label, pal.ink);
        }

        // Why strip for the selected route.
        let (x, y, ww, wh) = at(l.why);
        g.rect(x, y, ww, wh, 8.0 * s, pal.card);
        g.outline(x, y, ww, wh, 8.0 * s, 1.0, pal.line);
        g.text(f.1, 14.0 * s, x + 16.0 * s, y + wh / 2.0, &self.why(self.sel), pal.ink);
    }

    pub fn why(&self, d: usize) -> String {
        match self.holder(d) {
            Some(h) => {
                let waiting: Vec<&str> = self.order.iter().copied().filter(|&p| p != h && self.on[p] && WANTS[p][d]).map(|p| PROFILES[p].0).collect();
                if waiting.is_empty() {
                    format!("{}: {} holds it", DESTS[d].0, PROFILES[h].0)
                } else {
                    format!("{}: {} holds it, {} waits behind", DESTS[d].0, PROFILES[h].0, waiting.join(" and "))
                }
            }
            None => format!("{}: no connected tunnel wants it", DESTS[d].0),
        }
    }

    fn draw_lines(&self, g: &mut Gfx, pal: &Pal, s: f32, ox: f32, oy: f32, l: &Layout, hue: &dyn Fn(usize) -> [f32; 4]) {
        let progress = self.progress();
        for k in 0..3 {
            let p = self.order[k];
            if !self.on[p] {
                continue;
            }
            let mut n = 0;
            for d in (0..5).filter(|&d| WANTS[p][d]) {
                let held = self.holder(d) == Some(p);
                let lit = match self.hover {
                    Hover::None => true,
                    Hover::Route(r) => r == d,
                    Hover::Tun(t) => t == p,
                };
                let mut c = hue(p);
                c[3] = if lit { if held { 1.0 } else { 0.7 } } else { 0.12 };
                let (x0, y0) = (ox * s + (l.routes[d].0 + ROUTE_W) * s, oy * s + (l.routes[d].1 + ROUTE_H / 2.0) * s);
                let (x1, y1) = (ox * s + l.tuns[k].0 * s, oy * s + (l.tuns[k].1 + 26.0 + n as f32 * 14.0) * s);
                n += 1;
                let pts = bezier(x0, y0, x1, y1);
                let shown = ((pts.len() - 1) as f32 * progress).ceil() as usize;
                let part = &pts[..=shown.min(pts.len() - 1)];
                if held {
                    for seg in part.windows(2) {
                        g.line_flat(seg[0].0, seg[0].1, seg[1].0, seg[1].1, 3.0 * s, c);
                    }
                } else {
                    dotted(g, part, 2.5 * s, c);
                    if progress >= 0.85 {
                        let m = pts[pts.len() * 4 / 5];
                        let a = 4.5 * s;
                        let x = c_alpha(pal.ink, c[3]);
                        g.line(m.0 - a, m.1 - a, m.0 + a, m.1 + a, 2.0 * s, x);
                        g.line(m.0 - a, m.1 + a, m.0 + a, m.1 - a, 2.0 * s, x);
                    }
                }
            }
        }
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

    const W: f32 = 968.0;

    fn bay() -> Bay {
        let mut b = Bay::new();
        b.reduced_motion = true;
        b
    }

    /// Centre of tunnel slot `k`'s button, in the wide layout.
    fn button(b: &Bay, k: usize) -> (f32, f32) {
        let r = b.layout(W).btns[k];
        (r.0 + r.2 / 2.0, r.1 + r.3 / 2.0)
    }

    #[test]
    fn highest_priority_connected_tunnel_holds_the_route() {
        let b = bay();
        assert_eq!((0..5).map(|d| b.holder(d)).collect::<Vec<_>>(), [Some(0), Some(0), Some(0), Some(1), Some(2)]);
        assert_eq!((b.holds(0), b.holds(1), b.holds(2)), ((3, 3), (1, 4), (1, 2)));
    }

    #[test]
    fn disconnecting_the_holder_hands_its_routes_to_the_next() {
        let mut b = bay();
        let (x, y) = button(&b, 0);
        assert!(b.press(x, y, W));
        assert!(!b.on[0]);
        assert_eq!(b.holder(0), Some(1));
        assert_eq!(b.holds(1), (4, 4));
        assert!(b.press(x, y, W));
        assert_eq!(b.holder(0), Some(0));
    }

    #[test]
    fn nobody_holds_a_route_no_connected_tunnel_wants() {
        let mut b = bay();
        b.on = [true, true, false];
        assert_eq!(b.holder(4), None);
        assert!(b.why(4).contains("no connected tunnel"));
    }

    #[test]
    fn dragging_a_tunnel_up_raises_its_priority() {
        let mut b = bay();
        let l = b.layout(W);
        let t = l.tuns[1];
        assert!(b.press(t.0 + 20.0, t.1 + 20.0, W));
        assert!(b.dragging());
        // Move the pointer over slot 0.
        assert!(b.mouse_move(t.0 + 20.0, l.tuns[0].1 + 20.0, W));
        assert!(b.release());
        assert_eq!(b.order, [1, 0, 2]);
        assert_eq!(b.holder(0), Some(1), "Office now wins the default route");
        assert_eq!(b.holds(1).0, 4);
    }

    #[test]
    fn dragging_past_the_ends_stays_in_range() {
        let mut b = bay();
        let t = b.layout(W).tuns[0];
        b.press(t.0 + 20.0, t.1 + 20.0, W);
        b.mouse_move(t.0 + 20.0, 5000.0, W);
        b.release();
        assert_eq!(b.order, [1, 2, 0]);
    }

    #[test]
    fn clicking_a_route_selects_it_and_keys_walk_the_list() {
        let mut b = bay();
        let r = b.layout(W).routes[3];
        b.press(r.0 + 10.0, r.1 + 10.0, W);
        assert!(!b.dragging());
        assert_eq!(b.sel, 3);
        assert!(b.key(0x28));
        assert_eq!(b.sel, 4);
        assert!(!b.key(0x28), "stops at the last route");
        assert!(b.key(0x24));
        assert_eq!(b.sel, 0);
        assert!(!b.key(0x41));
    }

    #[test]
    fn compact_layout_toggles_from_the_chips_and_never_drags() {
        let mut b = bay();
        b.compact = true;
        let t = b.layout(W).tuns[2];
        assert!(b.press(t.0 + 10.0, t.1 + 10.0, W));
        assert!(!b.on[2]);
        assert!(!b.dragging());
    }

    #[test]
    fn hover_changes_are_reported_once() {
        let mut b = bay();
        let r = b.layout(W).routes[1];
        assert!(b.mouse_move(r.0 + 5.0, r.1 + 5.0, W));
        assert!(!b.mouse_move(r.0 + 6.0, r.1 + 6.0, W));
        assert_eq!(b.hover, Hover::Route(1));
        assert!(b.leave());
        assert!(!b.leave());
    }

    #[test]
    fn the_draw_in_animation_ends() {
        let mut b = Bay::new();
        assert!(b.animating());
        b.reduced_motion = true;
        b.restart_anim();
        assert!(!b.animating());
        assert_eq!(b.progress(), 1.0);
    }
}
