//! The Routing page on fake data: routes on the left, tunnels on the right, one line per request.
//! A line is solid where the tunnel holds the route and dotted with a cross where it waits behind a
//! higher priority. All geometry is in logical pixels; `s` scales it when drawing.

use crate::gfx::Gfx;

pub const PROFILES: [(&str, &str, u32, u32); 3] = [("Home", "WireGuard", 0x0A9B94, 0x3AD0C6), ("Office", "OpenVPN", 0xD9680F, 0xFF9A4D), ("Lab", "WireGuard", 0xC0306B, 0xFF6B9D)];
const DESTS: [(&str, &str); 5] = [("Default route", "All internet traffic"), ("DNS", "Name lookups"), ("192.168.1.0/24", "Shared office network"), ("10.20.0.0/16", "Corporate servers"), ("172.16.8.0/24", "Lab network")];
const WANTS: [[bool; 5]; 3] = [[true, true, true, false, false], [true, true, true, true, false], [false, false, true, false, true]];

const ROUTE_H: f32 = 56.0;
const ROUTE_GAP: f32 = 16.0;
const ROUTE_W: f32 = 260.0;
const TUN_H: f32 = 104.0;
const TUN_GAP: f32 = 14.0;

pub struct Pal {
    pub dark: bool,
    pub ink: [f32; 4],
    pub muted: [f32; 4],
    pub line: [f32; 4],
    pub card: [f32; 4],
}

#[derive(Clone, Copy, PartialEq)]
pub enum Hover {
    None,
    Route(usize),
    Tun(usize),
}

pub struct Bay {
    pub on: [bool; 3],
    pub sel: usize,
    pub hover: Hover,
    btn_hover: Option<usize>,
}

type R = (f32, f32, f32, f32);

struct Layout {
    routes: [R; 5],
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

impl Bay {
    pub fn new() -> Self {
        Bay { on: [true; 3], sel: 0, hover: Hover::None, btn_hover: None }
    }

    /// The first connected profile that wants the route holds it (priority is the list order).
    fn holder(&self, d: usize) -> Option<usize> {
        (0..3).find(|&p| self.on[p] && WANTS[p][d])
    }

    /// (routes held, routes wanted) for a profile.
    pub fn holds(&self, p: usize) -> (usize, usize) {
        ((0..5).filter(|&d| self.holder(d) == Some(p)).count(), WANTS[p].iter().filter(|w| **w).count())
    }

    fn layout(w: f32, oy: f32) -> Layout {
        let tw = (w * 0.28).clamp(250.0, 320.0);
        let y0 = oy + 84.0;
        let routes = std::array::from_fn(|i| (0.0, y0 + i as f32 * (ROUTE_H + ROUTE_GAP), ROUTE_W, ROUTE_H));
        let tuns: [R; 3] = std::array::from_fn(|i| (w - tw, y0 + i as f32 * (TUN_H + TUN_GAP), tw, TUN_H));
        let btns = std::array::from_fn(|i| (tuns[i].0 + tw - 16.0 - 96.0, tuns[i].1 + TUN_H - 14.0 - 28.0, 96.0, 28.0));
        let bottom = routes[4].1 + ROUTE_H;
        let bottom = bottom.max(tuns[2].1 + TUN_H);
        Layout { routes, tuns, btns, why: (0.0, bottom + 24.0, w, 56.0) }
    }

    /// `x`, `y` are logical pixels from the content origin.
    fn hit(&self, x: f32, y: f32, w: f32) -> (Hover, Option<usize>) {
        let l = Self::layout(w, 0.0);
        if let Some(p) = (0..3).find(|&p| inside(l.btns[p], x, y)) {
            return (Hover::Tun(p), Some(p));
        }
        if let Some(p) = (0..3).find(|&p| inside(l.tuns[p], x, y)) {
            return (Hover::Tun(p), None);
        }
        if let Some(d) = (0..5).find(|&d| inside(l.routes[d], x, y)) {
            return (Hover::Route(d), None);
        }
        (Hover::None, None)
    }

    /// Returns true when the picture changes.
    pub fn mouse_move(&mut self, x: f32, y: f32, w: f32) -> bool {
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

    pub fn click(&mut self, x: f32, y: f32, w: f32) -> bool {
        match self.hit(x, y, w) {
            (_, Some(p)) => self.on[p] = !self.on[p],
            (Hover::Route(d), _) => self.sel = d,
            _ => return false,
        }
        true
    }

    /// Draw at logical origin (`ox`, `oy`), `w` wide. `f` is (text, bold).
    pub fn draw(&self, g: &mut Gfx, f: (usize, usize), pal: &Pal, s: f32, ox: f32, oy: f32, w: f32) {
        let l = Self::layout(w, 0.0);
        let hue = |p: usize| color(if pal.dark { PROFILES[p].3 } else { PROFILES[p].2 }, 1.0);
        let at = |r: R| (ox * s + r.0 * s, oy * s + r.1 * s, r.2 * s, r.3 * s);
        let held_total = (0..5).filter(|&d| self.holder(d).is_some()).count();

        g.text(f.1, 28.0 * s, ox * s, oy * s + 14.0 * s, "Routing", pal.ink);
        let sub = format!("{} of 5 routes are held by a tunnel", held_total);
        g.text(f.0, 14.0 * s, ox * s, oy * s + 50.0 * s, &sub, pal.muted);

        // Lines go under the cards.
        for p in (0..3).filter(|&p| self.on[p]) {
            let mut k = 0;
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
                let (x1, y1) = (ox * s + l.tuns[p].0 * s, oy * s + (l.tuns[p].1 + 26.0 + k as f32 * 14.0) * s);
                k += 1;
                let pts = bezier(x0, y0, x1, y1);
                if held {
                    for seg in pts.windows(2) {
                        g.line_flat(seg[0].0, seg[0].1, seg[1].0, seg[1].1, 3.0 * s, c);
                    }
                } else {
                    dotted(g, &pts, 2.5 * s, c);
                    let m = pts[pts.len() * 4 / 5];
                    let a = 4.5 * s;
                    let mut x = pal.ink;
                    x[3] = c[3];
                    g.line(m.0 - a, m.1 - a, m.0 + a, m.1 + a, 2.0 * s, x);
                    g.line(m.0 - a, m.1 + a, m.0 + a, m.1 - a, 2.0 * s, x);
                }
            }
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
            if let Some(p) = self.holder(d) {
                g.rect(x + rw - 22.0 * s, y + rh / 2.0 - 5.0 * s, 10.0 * s, 10.0 * s, 5.0 * s, hue(p));
            }
        }

        for p in 0..3 {
            let (x, y, tw, th) = at(l.tuns[p]);
            let c = hue(p);
            let on = self.on[p];
            if on {
                g.rect(x, y, tw, th, 10.0 * s, [c[0], c[1], c[2], 0.13]);
                g.outline(x, y, tw, th, 10.0 * s, 1.0, [c[0], c[1], c[2], 0.28]);
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
            let line = if on { format!("Holds {} of {} routes", held, wanted) } else { "Not connected".to_string() };
            g.text(f.0, 13.0 * s, x + 16.0 * s, y + 50.0 * s, &line, pal.muted);

            let (bx, by, bw, bh) = at(l.btns[p]);
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
        let d = self.sel;
        let text = match self.holder(d) {
            Some(h) => {
                let waiting: Vec<&str> = (0..3).filter(|&p| p != h && self.on[p] && WANTS[p][d]).map(|p| PROFILES[p].0).collect();
                if waiting.is_empty() {
                    format!("{}: {} holds it", DESTS[d].0, PROFILES[h].0)
                } else {
                    format!("{}: {} holds it, {} waits behind", DESTS[d].0, PROFILES[h].0, waiting.join(" and "))
                }
            }
            None => format!("{}: no connected tunnel wants it", DESTS[d].0),
        };
        g.text(f.1, 14.0 * s, x + 16.0 * s, y + wh / 2.0, &text, pal.ink);
    }
}

fn c_alpha(c: [f32; 4], a: f32) -> [f32; 4] {
    [c[0], c[1], c[2], a]
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

/// Round dots along a polyline, 2 on and 7 off at width 2.5, scaled with `width`.
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
