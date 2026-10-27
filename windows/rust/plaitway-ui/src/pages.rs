//! Navigation and the pages other than Routing. Every clickable thing registers a region while it is
//! drawn; a click looks the region up, so drawing and hit-testing cannot drift apart.

use plaitway_core::{Link, Model, Standing, State, Tunnel, View};

use crate::{
    bay::{hue, kind_name, Pal},
    gfx::Gfx,
};

#[derive(Clone, PartialEq, Debug)]
pub enum Page {
    Routing,
    /// By profile id, so the page stays on the same profile when the priorities change.
    Profile(String),
    Diagnostics,
    Settings,
}

#[derive(Clone, PartialEq, Debug)]
pub enum Act {
    Go(Page),
    Tab(usize),
    /// A switch: 0 start with Windows, 1 close to tray, 2 and up per profile.
    Switch(usize),
    /// Connect or disconnect the profile with this id.
    Toggle(String),
}

pub const PROFILE_TABS: [&str; 5] = ["Overview", "Routes", "Logs", "Configuration", "Settings"];
pub const DIAG_TABS: [&str; 2] = ["Overview", "Daemon log"];
type R = (f32, f32, f32, f32);

pub struct Ui {
    pub page: Page,
    pub tab: usize,
    pub switches: [bool; 8],
    hits: Vec<(R, Act)>,
    hover: Option<Act>,
}

pub struct Fonts {
    pub text: usize,
    pub bold: usize,
    pub mono: usize,
}

impl Ui {
    pub fn new() -> Self {
        Ui { page: Page::Routing, tab: 0, switches: [true, true, false, true, true, false, true, false], hits: Vec::new(), hover: None }
    }

    pub fn clear(&mut self) {
        self.hits.clear();
    }

    /// Register a clickable region in pixels.
    pub fn region(&mut self, r: R, act: Act) {
        self.hits.push((r, act));
    }

    fn at(&self, x: f32, y: f32) -> Option<Act> {
        self.hits.iter().rev().find(|(r, _)| x >= r.0 && x < r.0 + r.2 && y >= r.1 && y < r.1 + r.3).map(|(_, a)| a.clone())
    }

    pub fn is_hover(&self, act: &Act) -> bool {
        self.hover.as_ref() == Some(act)
    }

    /// Returns true when the highlighted region changed.
    pub fn mouse_move(&mut self, x: f32, y: f32) -> bool {
        let h = self.at(x, y);
        let changed = h != self.hover;
        self.hover = h;
        changed
    }

    /// Apply a click. `Toggle` is returned to the caller, which owns the session.
    pub fn click(&mut self, x: f32, y: f32) -> Option<Act> {
        let act = self.at(x, y)?;
        match &act {
            Act::Go(p) => {
                self.page = p.clone();
                self.tab = 0;
            }
            Act::Tab(t) => self.tab = *t,
            Act::Switch(i) => self.switches[*i] = !self.switches[*i],
            Act::Toggle(_) => {}
        }
        Some(act)
    }

    /// Left and right arrows walk the tabs of a page that has them.
    pub fn key(&mut self, vk: u32) -> bool {
        let n = match self.page {
            Page::Profile(_) => PROFILE_TABS.len(),
            Page::Diagnostics => DIAG_TABS.len(),
            _ => return false,
        };
        let tab = match vk {
            0x25 => self.tab.saturating_sub(1),
            0x27 => (self.tab + 1).min(n - 1),
            _ => return false,
        };
        let changed = tab != self.tab;
        self.tab = tab;
        changed
    }

    /// Draw every page but Routing at logical origin (`ox`, `oy`), `w` wide.
    #[allow(clippy::too_many_arguments)]
    pub fn draw(&mut self, g: &mut Gfx, f: &Fonts, pal: &Pal, view: &View, s: f32, ox: f32, oy: f32, w: f32) {
        match self.page.clone() {
            Page::Routing => {}
            Page::Profile(id) => match view.model.index(&id) {
                Some(i) => self.profile(g, f, pal, &view.model, i, s, ox, oy, w),
                None => self.header(g, f, pal, s, ox, oy, "Profile not found", "It may have been deleted"),
            },
            Page::Diagnostics => self.diagnostics(g, f, pal, view, s, ox, oy, w),
            Page::Settings => self.settings(g, f, pal, s, ox, oy, w),
        }
    }

    #[allow(clippy::too_many_arguments)]
    fn header(&self, g: &mut Gfx, f: &Fonts, pal: &Pal, s: f32, ox: f32, oy: f32, title: &str, sub: &str) {
        g.text(f.bold, 28.0 * s, ox * s, oy * s + 14.0 * s, title, pal.ink);
        g.text(f.text, 14.0 * s, ox * s, oy * s + 50.0 * s, sub, pal.muted);
    }

    /// A row of tabs; returns the y just below it, in logical pixels.
    #[allow(clippy::too_many_arguments)]
    fn tabs(&mut self, g: &mut Gfx, f: &Fonts, pal: &Pal, s: f32, ox: f32, y: f32, w: f32, names: &[&str], accent: [f32; 4]) -> f32 {
        g.rect(ox * s, (y + 36.0) * s, w * s, 1.0, 0.0, pal.line);
        let mut x = ox;
        for (i, n) in names.iter().enumerate() {
            let tw = g.measure(f.bold, 14.0 * s, n) / s + 28.0;
            let sel = self.tab == i;
            g.text(if sel { f.bold } else { f.text }, 14.0 * s, (x + 14.0) * s, (y + 18.0) * s, n, if sel { pal.ink } else { pal.muted });
            if sel {
                g.rect(x * s, (y + 34.0) * s, tw * s, 2.0 * s, 1.0, accent);
            }
            self.region((x * s, y * s, tw * s, 36.0 * s), Act::Tab(i));
            x += tw + 4.0;
        }
        y + 56.0
    }

    /// One card holding `rows` of (label, value); returns its bottom.
    #[allow(clippy::too_many_arguments)]
    fn rows(&self, g: &mut Gfx, f: &Fonts, pal: &Pal, s: f32, ox: f32, y: f32, w: f32, rows: &[(&str, String)]) -> f32 {
        let h = rows.len() as f32 * 46.0;
        g.rect(ox * s, y * s, w * s, h * s, 10.0 * s, pal.card);
        g.outline(ox * s, y * s, w * s, h * s, 10.0 * s, 1.0, pal.line);
        for (i, (k, v)) in rows.iter().enumerate() {
            let cy = (y + 23.0 + i as f32 * 46.0) * s;
            if i > 0 {
                g.rect((ox + 1.0) * s, cy - 23.0 * s, (w - 2.0) * s, 1.0, 0.0, pal.line);
            }
            g.text(f.text, 14.0 * s, (ox + 18.0) * s, cy, k, pal.muted);
            g.text(f.text, 14.0 * s, (ox + 190.0) * s, cy, v, pal.ink);
        }
        y + h + 14.0
    }

    #[allow(clippy::too_many_arguments)]
    fn switch_row(&mut self, g: &mut Gfx, f: &Fonts, pal: &Pal, s: f32, ox: f32, y: f32, w: f32, rows: &[(&str, usize)]) -> f32 {
        let h = rows.len() as f32 * 46.0;
        g.rect(ox * s, y * s, w * s, h * s, 10.0 * s, pal.card);
        g.outline(ox * s, y * s, w * s, h * s, 10.0 * s, 1.0, pal.line);
        for (i, (k, idx)) in rows.iter().enumerate() {
            let cy = (y + 23.0 + i as f32 * 46.0) * s;
            if i > 0 {
                g.rect((ox + 1.0) * s, cy - 23.0 * s, (w - 2.0) * s, 1.0, 0.0, pal.line);
            }
            g.text(f.text, 14.0 * s, (ox + 18.0) * s, cy, k, pal.ink);
            let (sx, sy) = ((ox + w - 18.0 - 40.0) * s, cy - 10.0 * s);
            if self.switches[*idx] {
                g.rect(sx, sy, 40.0 * s, 20.0 * s, 10.0 * s, pal.ink);
                g.rect(sx + 23.0 * s, sy + 3.0 * s, 14.0 * s, 14.0 * s, 7.0 * s, if pal.dark { [0.12, 0.13, 0.15, 1.0] } else { [1.0; 4] });
            } else {
                g.outline(sx, sy, 40.0 * s, 20.0 * s, 10.0 * s, 1.0, pal.muted);
                g.rect(sx + 4.0 * s, sy + 4.0 * s, 12.0 * s, 12.0 * s, 6.0 * s, pal.muted);
            }
            self.region((ox * s, cy - 23.0 * s, w * s, 46.0 * s), Act::Switch(*idx));
        }
        y + h + 14.0
    }

    #[allow(clippy::too_many_arguments)]
    fn lines(&self, g: &mut Gfx, f: &Fonts, pal: &Pal, s: f32, ox: f32, y: f32, w: f32, lines: &[String], mono: bool) -> f32 {
        let h = lines.len().max(1) as f32 * 24.0 + 24.0;
        g.rect(ox * s, y * s, w * s, h * s, 10.0 * s, pal.card);
        g.outline(ox * s, y * s, w * s, h * s, 10.0 * s, 1.0, pal.line);
        for (i, l) in lines.iter().enumerate() {
            g.text(if mono { f.mono } else { f.text }, 13.0 * s, (ox + 18.0) * s, (y + 24.0 + i as f32 * 24.0) * s, l, pal.ink);
        }
        y + h + 14.0
    }

    #[allow(clippy::too_many_arguments)]
    fn profile(&mut self, g: &mut Gfx, f: &Fonts, pal: &Pal, m: &Model, i: usize, s: f32, ox: f32, oy: f32, w: f32) {
        let t: &Tunnel = &m.tunnels[i];
        let c = hue(m, i, pal.dark);
        self.header(g, f, pal, s, ox, oy, &t.name, &format!("{} · priority {}", kind_name(t), i + 1));
        // Connect / Disconnect at the right of the header.
        let (bx, by, bw, bh) = ((ox + w - 112.0) * s, (oy + 6.0) * s, 112.0 * s, 34.0 * s);
        let act = Act::Toggle(t.id.clone());
        g.rect(bx, by, bw, bh, 6.0 * s, if self.is_hover(&act) { [pal.ink[0], pal.ink[1], pal.ink[2], 0.12] } else { pal.card });
        g.outline(bx, by, bw, bh, 6.0 * s, 1.0, pal.line);
        let label = if t.enabled { "Disconnect" } else { "Connect" };
        let lw = g.measure(f.text, 14.0 * s, label);
        g.text(f.text, 14.0 * s, bx + (bw - lw) / 2.0, by + bh / 2.0, label, pal.ink);
        self.region((bx, by, bw, bh), act);

        let y = self.tabs(g, f, pal, s, ox, oy + 74.0, w, &PROFILE_TABS, c);
        match self.tab {
            0 => {
                let (held, wanted) = t.holds();
                g.rect(ox * s, y * s, w * s, 76.0 * s, 10.0 * s, pal.card);
                g.outline(ox * s, y * s, w * s, 76.0 * s, 10.0 * s, 1.0, pal.line);
                if t.connected() {
                    g.rect((ox + 18.0) * s, (y + 24.0) * s, 28.0 * s, 28.0 * s, 14.0 * s, c);
                } else {
                    g.outline((ox + 18.0) * s, (y + 24.0) * s, 28.0 * s, 28.0 * s, 14.0 * s, 2.0 * s, pal.muted);
                }
                g.text(f.bold, 18.0 * s, (ox + 60.0) * s, (y + 28.0) * s, state_name(t.state), pal.ink);
                let sub = if t.connected() {
                    format!("Holds {held} of {wanted} routes")
                } else if !t.error.is_empty() {
                    t.error.clone()
                } else {
                    "No routes held".to_string()
                };
                g.text(f.text, 13.0 * s, (ox + 60.0) * s, (y + 52.0) * s, &sub, pal.muted);
                if let Some(up) = t.since.and_then(uptime) {
                    g.text(f.bold, 18.0 * s, (ox + w - 150.0) * s, (y + 28.0) * s, &up, pal.ink);
                    g.text(f.text, 13.0 * s, (ox + w - 150.0) * s, (y + 52.0) * s, "Uptime", pal.muted);
                }
                let rows = [("Protocol", kind_name(t).to_string()), ("Priority", (i + 1).to_string()), ("Routes it names", t.wanted.to_string()), ("Routes it holds", held.to_string())];
                self.rows(g, f, pal, s, ox, y + 90.0, w, &rows);
            }
            1 => {
                let lines: Vec<String> = t.requests.iter().map(|r| format!("{:<20}{}", r.prefix, standing_name(r.standing, r.shadowed_by.as_deref().and_then(|id| m.index(id)).map(|j| m.tunnels[j].name.as_str())))).collect();
                let lines = if lines.is_empty() { vec!["Not connected, so nothing is asked for.".to_string()] } else { lines };
                self.lines(g, f, pal, s, ox, y, w, &lines, true);
            }
            2 | 3 => {
                let what = if self.tab == 2 { "Logs" } else { "The configuration editor" };
                self.lines(g, f, pal, s, ox, y, w, &[format!("{what} arrive in a later step.")], false);
            }
            _ => {
                let base = 2 + 2 * i.min(2);
                self.switch_row(g, f, pal, s, ox, y, w, &[("Connect when Plaitway starts", base), ("Use for the default route", base + 1)]);
            }
        }
    }

    #[allow(clippy::too_many_arguments)]
    fn diagnostics(&mut self, g: &mut Gfx, f: &Fonts, pal: &Pal, view: &View, s: f32, ox: f32, oy: f32, w: f32) {
        self.header(g, f, pal, s, ox, oy, "Diagnostics", "What the daemon owns on this machine");
        let y = self.tabs(g, f, pal, s, ox, oy + 74.0, w, &DIAG_TABS, pal.ink);
        if self.tab == 0 {
            let routes = view.model.routes();
            let held = routes.iter().filter(|r| r.holder.is_some()).count();
            let daemon = match &view.link {
                Link::Online => "Running".to_string(),
                Link::Connecting => "Connecting".to_string(),
                Link::Offline(why) => format!("Not reachable: {why}"),
            };
            let rows = [
                ("Daemon", daemon),
                ("Profiles", view.model.tunnels.len().to_string()),
                ("Connected", view.model.tunnels.iter().filter(|t| t.connected()).count().to_string()),
                ("Routes asked for", routes.len().to_string()),
                ("Routes held", held.to_string()),
            ];
            self.rows(g, f, pal, s, ox, y, w, &rows);
        } else {
            self.lines(g, f, pal, s, ox, y, w, &["The daemon log arrives in a later step.".to_string()], false);
        }
    }

    fn settings(&mut self, g: &mut Gfx, f: &Fonts, pal: &Pal, s: f32, ox: f32, oy: f32, w: f32) {
        self.header(g, f, pal, s, ox, oy, "Settings", "Plaitway 0.6.1");
        let y = oy + 84.0;
        let y = self.rows(g, f, pal, s, ox, y, w, &[("Language", "English".to_string())]);
        self.switch_row(g, f, pal, s, ox, y, w, &[("Start with Windows", 0), ("Keep running in the tray when closed", 1)]);
    }
}

pub fn state_name(s: State) -> &'static str {
    match s {
        State::Disconnected => "Not connected",
        State::Connecting => "Connecting",
        State::Connected => "Connected",
        State::Disconnecting => "Disconnecting",
        State::Failed => "Failed",
        State::Reconnecting => "Reconnecting",
        State::AwaitingCredentials => "Needs credentials",
    }
}

fn standing_name(s: Standing, holder: Option<&str>) -> String {
    match (s, holder) {
        (Standing::Holds, _) => "holds".into(),
        (Standing::Waits, Some(h)) => format!("waits behind {h}"),
        (Standing::Waits, None) => "waits".into(),
        (Standing::Blocked, _) => "blocked by a local subnet".into(),
        (Standing::Failed, _) => "failed".into(),
        (Standing::Pending, _) => "pending".into(),
    }
}

/// "2 h 14 min" from the epoch second a tunnel came up.
fn uptime(since: i64) -> Option<String> {
    let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).ok()?.as_secs() as i64;
    let secs = (now - since).max(0);
    let (h, m) = (secs / 3600, secs % 3600 / 60);
    Some(if h > 0 { format!("{h} h {m} min") } else if m > 0 { format!("{m} min") } else { format!("{secs} s") })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn clicking_a_region_runs_its_action_and_resets_the_tab() {
        let mut ui = Ui::new();
        ui.region((0.0, 0.0, 100.0, 40.0), Act::Go(Page::Profile("home".into())));
        ui.region((0.0, 50.0, 100.0, 40.0), Act::Tab(3));
        ui.region((0.0, 100.0, 100.0, 40.0), Act::Switch(0));
        assert_eq!(ui.click(500.0, 5.0), None);
        ui.tab = 2;
        assert_eq!(ui.click(10.0, 10.0), Some(Act::Go(Page::Profile("home".into()))));
        assert_eq!((ui.page.clone(), ui.tab), (Page::Profile("home".into()), 0));
        ui.click(10.0, 60.0);
        assert_eq!(ui.tab, 3);
        let before = ui.switches[0];
        ui.click(10.0, 110.0);
        assert_ne!(ui.switches[0], before);
    }

    #[test]
    fn a_toggle_is_handed_back_to_the_caller() {
        let mut ui = Ui::new();
        ui.region((0.0, 0.0, 10.0, 10.0), Act::Toggle("lab".into()));
        assert_eq!(ui.click(5.0, 5.0), Some(Act::Toggle("lab".into())));
        assert_eq!(ui.page, Page::Routing);
    }

    #[test]
    fn the_region_drawn_last_wins_where_they_overlap() {
        let mut ui = Ui::new();
        ui.region((0.0, 0.0, 100.0, 100.0), Act::Go(Page::Settings));
        ui.region((0.0, 0.0, 50.0, 50.0), Act::Tab(1));
        assert_eq!(ui.click(10.0, 10.0), Some(Act::Tab(1)));
        ui.clear();
        assert_eq!(ui.click(10.0, 10.0), None);
    }

    #[test]
    fn hover_is_reported_only_when_it_changes() {
        let mut ui = Ui::new();
        ui.region((0.0, 0.0, 10.0, 10.0), Act::Tab(0));
        assert!(ui.mouse_move(5.0, 5.0));
        assert!(!ui.mouse_move(6.0, 6.0));
        assert!(ui.is_hover(&Act::Tab(0)));
        assert!(ui.mouse_move(50.0, 50.0));
    }

    #[test]
    fn arrow_keys_walk_the_tabs_only_where_there_are_tabs() {
        let mut ui = Ui::new();
        assert!(!ui.key(0x27), "Routing has no tabs");
        ui.page = Page::Diagnostics;
        assert!(ui.key(0x27));
        assert!(!ui.key(0x27), "stops at the last tab");
        assert!(ui.key(0x25));
        ui.page = Page::Profile("x".into());
        ui.tab = 4;
        assert!(!ui.key(0x27));
    }

    #[test]
    fn uptime_reads_naturally() {
        let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).unwrap().as_secs() as i64;
        assert_eq!(uptime(now - 8040).unwrap(), "2 h 14 min");
        assert_eq!(uptime(now - 300).unwrap(), "5 min");
        assert!(uptime(now + 100).unwrap().ends_with(" s"));
    }
}
