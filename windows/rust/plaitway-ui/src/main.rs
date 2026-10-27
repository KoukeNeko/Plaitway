//! Phase 0 spike: a window with Mica (Windows 11) or a solid Fluent background (Windows 10),
//! a transparent composition swap chain, and a caption drawn by us that keeps Snap Layouts.
#![windows_subsystem = "windows"]

mod bay;
mod gfx;
mod pages;
mod shell;

use std::{cell::RefCell, ffi::c_void};

use bay::{Bay, Out, Pal};
use plaitway_core::{Link, Session, Store, UiPrefs, View};
use gfx::Gfx;
use pages::{Act, Fonts, Page, Ui};
use windows::{
    core::*,
    Wdk::System::SystemServices::RtlGetVersion,
    Win32::{
        Foundation::*,
        Graphics::{Dwm::*, DirectWrite::*, Gdi::*},
        System::{LibraryLoader::GetModuleHandleW, Registry::*, SystemInformation::OSVERSIONINFOW},
        UI::{Controls::MARGINS, HiDpi::*, Input::KeyboardAndMouse::*, WindowsAndMessaging::*},
    },
};

const CAPTION_H: f32 = 32.0;
const BUTTON_W: f32 = 46.0;
const SIDEBAR_W: f32 = 248.0;
const MICA_BUILD: u32 = 22621;
const WM_MOUSELEAVE: u32 = 0x02A3;

#[derive(Clone, Copy, PartialEq)]
enum Hot {
    None,
    Min,
    Max,
    Close,
}

struct Theme {
    dark: bool,
    ink: [f32; 4],
    muted: [f32; 4],
    line: [f32; 4],
    surface: [f32; 4],
    selected: [f32; 4],
    hover: [f32; 4],
    solid: [f32; 4],
}

fn rgba(hex: u32, a: f32) -> [f32; 4] {
    [((hex >> 16) & 255) as f32 / 255.0, ((hex >> 8) & 255) as f32 / 255.0, (hex & 255) as f32 / 255.0, a]
}

fn theme(dark: bool) -> Theme {
    if dark {
        Theme { dark, ink: rgba(0xEEF3F5, 1.0), muted: rgba(0xA3B0B7, 1.0), line: rgba(0xFFFFFF, 0.12), surface: rgba(0xFFFFFF, 0.055), selected: rgba(0xFFFFFF, 0.11), hover: rgba(0xFFFFFF, 0.08), solid: rgba(0x202427, 1.0) }
    } else {
        Theme { dark, ink: rgba(0x16222A, 1.0), muted: rgba(0x5B6A72, 1.0), line: rgba(0x16222A, 0.12), surface: rgba(0xFFFFFF, 0.68), selected: rgba(0xFFFFFF, 0.75), hover: rgba(0x16222A, 0.06), solid: rgba(0xE9EEF0, 1.0) }
    }
}

fn apps_use_dark() -> bool {
    // PLAITWAY_THEME=light|dark pins the theme, to look at both without changing the system setting.
    match std::env::var("PLAITWAY_THEME").as_deref() {
        Ok("light") => return false,
        Ok("dark") => return true,
        _ => {}
    }
    let mut v = 1u32;
    let mut n = 4u32;
    let r = unsafe { RegGetValueW(HKEY_CURRENT_USER, w!(r"Software\Microsoft\Windows\CurrentVersion\Themes\Personalize"), w!("AppsUseLightTheme"), RRF_RT_REG_DWORD, None, Some(&mut v as *mut u32 as *mut c_void), Some(&mut n)) };
    r.is_ok() && v == 0
}

fn build_number() -> u32 {
    let mut v = OSVERSIONINFOW { dwOSVersionInfoSize: std::mem::size_of::<OSVERSIONINFOW>() as u32, ..Default::default() };
    unsafe {
        let _ = RtlGetVersion(&mut v);
    }
    v.dwBuildNumber
}

struct App {
    gfx: Gfx,
    theme: Theme,
    mica: bool,
    scale: f32,
    hot: Hot,
    tracking: bool,
    client_tracking: bool,
    bay: Bay,
    session: Session,
    view: View,
    ui: Ui,
    /// What the session was last asked to watch: the log (a profile id, empty for the daemon's) and whether diagnostics.
    watching: (Option<String>, bool),
    store: Store,
    prefs: UiPrefs,
    /// Set by the tray menu: the next close really closes.
    quitting: bool,
    /// How far the page is scrolled, in logical pixels, and how far it can be.
    scroll: f32,
    max_scroll: f32,
    /// The page and tab the scroll position belongs to; a different one starts at the top.
    scroll_key: (Page, usize),
    f_mono: usize,
    frames: u32,
    f_text: usize,
    f_bold: usize,
    f_icons: usize,
}

thread_local! {
    static APP: RefCell<Option<App>> = const { RefCell::new(None) };
}

fn with_app<R>(f: impl FnOnce(&mut App) -> R) -> Option<R> {
    APP.with(|a| a.try_borrow_mut().ok().and_then(|mut a| a.as_mut().map(f)))
}

fn set_attr(hwnd: HWND, attr: i32, v: i32) {
    unsafe {
        let _ = DwmSetWindowAttribute(hwnd, DWMWINDOWATTRIBUTE(attr), &v as *const i32 as *const c_void, 4);
    }
}

impl App {
    /// Keep where the window is for the next start.
    fn remember_window(&mut self, hwnd: HWND) {
        if let Some(p) = shell::placement(hwnd) {
            self.prefs.window = Some(p);
            let _ = self.store.save_ui(&self.prefs);
        }
    }

    fn apply_theme(&mut self, hwnd: HWND) {
        self.theme = theme(apps_use_dark());
        set_attr(hwnd, 20, self.theme.dark as i32); // DWMWA_USE_IMMERSIVE_DARK_MODE
    }

    fn draw(&mut self, hwnd: HWND) -> Result<()> {
        let s = self.scale;
        let (w, h) = (self.gfx.size.0 as f32, self.gfx.size.1 as f32);
        let t = &self.theme;
        let (ink, muted, line) = (t.ink, t.muted, t.line);
        let g = &mut self.gfx;
        let maximized = unsafe { IsZoomed(hwnd).as_bool() };
        let cap = CAPTION_H * s;

        // Title bar: icon, name, caption buttons.
        g.rect(14.0 * s, (cap - 14.0 * s) / 2.0, 14.0 * s, 14.0 * s, 4.0 * s, rgba(0x0A9B94, 1.0));
        g.text(self.f_text, 12.0 * s, 36.0 * s, cap / 2.0, "Plaitway", ink);
        let glyphs = [(Hot::Min, "\u{E921}"), (Hot::Max, if maximized { "\u{E923}" } else { "\u{E922}" }), (Hot::Close, "\u{E8BB}")];
        for (hot, glyph) in glyphs {
            let (x, bw) = caption_rect(hot, w, s);
            let mut color = ink;
            if self.hot == hot {
                if hot == Hot::Close {
                    g.rect(x, 0.0, bw, cap, 0.0, rgba(0xC42B1C, 1.0));
                    color = [1.0, 1.0, 1.0, 1.0];
                } else {
                    g.rect(x, 0.0, bw, cap, 0.0, t.hover);
                }
            }
            let gw = g.measure(self.f_icons, 10.0 * s, glyph);
            g.text(self.f_icons, 10.0 * s, (x + (bw - gw) / 2.0).round(), cap / 2.0, glyph, color);
        }

        // Sidebar: Mica shows through; only the current row and the profile dots are drawn.
        let (side, _, compact) = frame(w, s);
        let ui = &mut self.ui;
        ui.clear();
        let rw = (side - 18.0) * s;
        let model = &self.view.model;
        // One navigation row: highlight when current, lighter when hovered, and a click region.
        let nav = |g: &mut Gfx, ui: &mut Ui, y: f32, h: f32, act: Act| {
            let current = matches!(&act, Act::Go(p) if *p == ui.page);
            if current || ui.is_hover(&act) {
                g.rect(8.0 * s, y, rw, h, 6.0 * s, if current { t.selected } else { t.hover });
            }
            if current {
                g.outline(8.0 * s, y, rw, h, 6.0 * s, 1.0, line);
            }
            ui.region((8.0 * s, y, rw, h), act);
        };
        nav(g, ui, cap + 6.0 * s, 40.0 * s, Act::Go(Page::Routing));
        g.text(self.f_icons, 14.0 * s, if compact { 8.0 * s + (rw - 14.0 * s) / 2.0 } else { 22.0 * s }, cap + 26.0 * s, "\u{E968}", ink);
        if !compact {
            g.text(self.f_text, 14.0 * s, 52.0 * s, cap + 26.0 * s, "Routing", ink);
            g.text(self.f_bold, 12.0 * s, 22.0 * s, cap + 78.0 * s, "PROFILES", muted);
        }
        for (k, tun) in model.tunnels.iter().enumerate() {
            let cy = cap + (if compact { 92.0 + k as f32 * 36.0 } else { 118.0 + k as f32 * 56.0 }) * s;
            nav(g, ui, cy - (if compact { 16.0 } else { 24.0 }) * s, (if compact { 32.0 } else { 48.0 }) * s, Act::Go(Page::Profile(tun.id.clone())));
            let (held, wanted) = tun.holds();
            let kind = bay::kind_name(tun);
            let sub = if tun.connected() { format!("{kind} · Holds {held} of {wanted}") } else { format!("{kind} · {}", pages::state_name(tun.state)) };
            let c = bay::hue(model, k, t.dark);
            let dx = if compact { (side - 18.0) / 2.0 + 8.0 - 6.0 } else { 22.0 } * s;
            if tun.enabled {
                g.rect(dx, cy - 6.0 * s, 12.0 * s, 12.0 * s, 6.0 * s, c);
            } else {
                g.outline(dx, cy - 6.0 * s, 12.0 * s, 12.0 * s, 6.0 * s, 2.0 * s, muted);
            }
            if !compact {
                let room = (side - 46.0 - 20.0) * s;
                g.text_fit(self.f_text, 14.0 * s, 46.0 * s, cy - 8.0 * s, &tun.name, ink, room);
                g.text_fit(self.f_text, 12.0 * s, 46.0 * s, cy + 10.0 * s, &sub, muted, room);
            }
        }

        // Diagnostics and Settings sit at the bottom of the sidebar.
        for (k, (label, glyph, page)) in [("Diagnostics", "\u{E9D9}", Page::Diagnostics), ("Settings", "\u{E713}", Page::Settings)].into_iter().enumerate() {
            let y = h - (100.0 - k as f32 * 46.0) * s;
            nav(g, ui, y, 40.0 * s, Act::Go(page));
            g.text(self.f_icons, 14.0 * s, if compact { 8.0 * s + (rw - 14.0 * s) / 2.0 } else { 22.0 * s }, y + 20.0 * s, glyph, ink);
            if !compact {
                g.text(self.f_text, 14.0 * s, 52.0 * s, y + 20.0 * s, label, ink);
            }
        }

        // Content surface: a translucent layer over Mica. Its right and bottom corners are pushed off screen.
        let (cx, cy0) = (side * s, cap);
        g.rect(cx, cy0, w - cx + 20.0 * s, h - cy0 + 20.0 * s, 10.0 * s, t.surface);
        g.outline(cx, cy0, w - cx + 20.0 * s, h - cy0 + 20.0 * s, 10.0 * s, 1.0, line);

        let pal = Pal { dark: t.dark, ink, muted, line, card: if t.dark { rgba(0xFFFFFF, 0.07) } else { rgba(0xFFFFFF, 0.9) } };
        let (ox, oy, cw) = content(w, s);
        self.bay.compact = compact;
        if self.scroll_key != (self.ui.page.clone(), self.ui.tab) {
            self.scroll_key = (self.ui.page.clone(), self.ui.tab);
            self.scroll = 0.0;
        }
        let top = oy - self.scroll;
        let viewport = h / s - oy - 16.0;
        g.clip(cx, cy0 + 1.0, w - cx, h - cy0);
        let fonts = Fonts { text: self.f_text, bold: self.f_bold, mono: self.f_mono };
        match (&self.view.link, self.ui.page == Page::Routing) {
            // Without the daemon there is nothing to draw but why.
            (Link::Offline(why), true) => {
                g.text(self.f_bold, 28.0 * s, ox * s, (oy + 14.0) * s, "plaitwayd is not running", ink);
                g.text(self.f_text, 14.0 * s, ox * s, (oy + 50.0) * s, why, muted);
                g.text(self.f_text, 14.0 * s, ox * s, (oy + 78.0) * s, "Trying again every second.", muted);
            }
            (Link::Connecting, true) => {
                g.text(self.f_bold, 28.0 * s, ox * s, (oy + 14.0) * s, "Connecting to the daemon", ink);
            }
            (_, true) if self.view.model.tunnels.is_empty() => {
                g.text(self.f_bold, 28.0 * s, ox * s, (oy + 14.0) * s, "No profiles yet", ink);
                g.text(self.f_text, 14.0 * s, ox * s, (oy + 50.0) * s, "Import a WireGuard or OpenVPN profile to see its routes here.", muted);
            }
            (_, true) => {
                self.bay.draw(g, (self.f_text, self.f_bold), &pal, &self.view.model, s, ox, top, cw);
                self.max_scroll = (self.bay.height(&self.view.model, cw) + 24.0 - viewport).max(0.0);
            }
            _ => {
                self.ui.draw(g, &fonts, &pal, &self.view, s, ox, top, cw, h / s - oy - 24.0);
                self.max_scroll = (self.ui.bottom - top + 24.0 - viewport).max(0.0);
            }
        }
        self.scroll = self.scroll.clamp(0.0, self.max_scroll);
        g.unclip();
        if self.max_scroll > 0.0 {
            // A thin bar at the right edge: the thumb is the visible share of the page.
            let (track_y, track_h) = ((oy - 8.0) * s, (viewport + 8.0) * s);
            let thumb = (viewport / (viewport + self.max_scroll) * track_h).max(24.0 * s);
            let at = track_y + (track_h - thumb) * self.scroll / self.max_scroll;
            g.rect(w - 8.0 * s, at, 4.0 * s, thumb, 2.0 * s, rgba(if t.dark { 0xFFFFFF } else { 0x16222A }, 0.35));
        }
        if let Some(e) = &self.view.error {
            g.text(self.f_text, 13.0 * s, ox * s, h - 24.0 * s, e, rgba(0xFF8DB4, 1.0));
        }
        self.frames += 1;
        let clear = if self.mica { [0.0; 4] } else { self.theme.solid };
        self.gfx.render(clear)
    }
}

/// Bring the window back from the notification area, or from behind others.
fn show_window(hwnd: HWND) {
    unsafe {
        let _ = ShowWindow(hwnd, if IsIconic(hwnd).as_bool() { SW_RESTORE } else { SW_SHOW });
        let _ = SetForegroundWindow(hwnd);
    }
}

/// Scroll by `delta` logical pixels (positive is down) and redraw if that moved the page.
fn scroll_by(hwnd: HWND, a: &mut App, delta: f32) {
    let to = (a.scroll + delta).clamp(0.0, a.max_scroll);
    if to != a.scroll {
        a.scroll = to;
        redraw(hwnd, a);
    }
}

/// Logical sidebar width, content padding and whether the window is narrow enough for the compact layout.
fn frame(w_px: f32, s: f32) -> (f32, f32, bool) {
    if w_px / s < bay::COMPACT_BELOW {
        (64.0, 20.0, true)
    } else {
        (SIDEBAR_W, 32.0, false)
    }
}

/// A client-area point in logical pixels from the content origin, plus the content width.
fn local(a: &mut App, x: f32, y: f32, w_px: f32) -> (f32, f32, f32) {
    let (s, (ox, oy, cw)) = (a.scale, content(w_px, a.scale));
    a.bay.compact = frame(w_px, s).2;
    (x / s - ox, y / s - oy + a.scroll, cw)
}

/// Tell the session what the current page needs: a log on the Logs tabs, diagnostics on the Diagnostics ones.
fn sync_watch(a: &mut App) {
    let logs = match (&a.ui.page, a.ui.tab) {
        (Page::Profile(id), 2) => Some(id.clone()),
        (Page::Diagnostics, 2) => Some(String::new()),
        _ => None,
    };
    let diag = a.ui.page == Page::Diagnostics && a.ui.tab < 2;
    if logs != a.watching.0 {
        a.session.watch_logs(logs.as_deref());
        a.watching.0 = logs;
    }
    if diag != a.watching.1 {
        a.session.watch_diagnostics(diag);
        a.watching.1 = diag;
    }
}

/// Carry out what the Routing page asked for.
fn run(hwnd: HWND, a: &mut App, out: Out) {
    match out {
        Out::Nothing => {}
        Out::Redraw => redraw(hwnd, a),
        Out::SetEnabled(id, on) => a.session.set_enabled(&id, on),
        Out::Reorder(ids) => {
            a.session.reorder(ids);
            redraw(hwnd, a);
        }
    }
}

/// Ask for a frame, and keep a timer running while the lines are drawing in.
fn redraw(hwnd: HWND, a: &App) {
    unsafe {
        let _ = InvalidateRect(Some(hwnd), None, false);
        if a.bay.animating() {
            SetTimer(Some(hwnd), 1, 16, None);
        }
    }
}

/// Logical origin and width of the page content for a client area `w_px` wide.
fn content(w_px: f32, s: f32) -> (f32, f32, f32) {
    let (side, pad, _) = frame(w_px, s);
    (side + pad, CAPTION_H + 28.0, w_px / s - side - 2.0 * pad)
}

/// Left edge and width of a caption button, in pixels.
fn caption_rect(hot: Hot, w: f32, s: f32) -> (f32, f32) {
    let i = match hot {
        Hot::Min => 0.0,
        Hot::Max => 1.0,
        _ => 2.0,
    };
    (w - (3.0 - i) * BUTTON_W * s, BUTTON_W * s)
}

fn client_size(hwnd: HWND) -> (u32, u32) {
    let mut r = RECT::default();
    unsafe {
        let _ = GetClientRect(hwnd, &mut r);
    }
    ((r.right - r.left).max(1) as u32, (r.bottom - r.top).max(1) as u32)
}

fn hit_button(app: &App, x: f32, y: f32, w: f32) -> Hot {
    if y < 0.0 || y >= CAPTION_H * app.scale {
        return Hot::None;
    }
    for hot in [Hot::Min, Hot::Max, Hot::Close] {
        let (bx, bw) = caption_rect(hot, w, app.scale);
        if x >= bx && x < bx + bw {
            return hot;
        }
    }
    Hot::None
}

unsafe extern "system" fn wndproc(hwnd: HWND, msg: u32, wparam: WPARAM, lparam: LPARAM) -> LRESULT {
    match msg {
        // No system caption: the client area starts at the top of the window. The other three
        // borders keep their default size, so resizing and the shadow stay as they are.
        WM_NCCALCSIZE if wparam.0 != 0 => {
            let p = lparam.0 as *mut NCCALCSIZE_PARAMS;
            let top = (*p).rgrc[0].top;
            let r = DefWindowProcW(hwnd, msg, wparam, lparam);
            if r.0 != 0 {
                return r;
            }
            (*p).rgrc[0].top = top;
            if IsZoomed(hwnd).as_bool() {
                let dpi = GetDpiForWindow(hwnd);
                (*p).rgrc[0].top += GetSystemMetricsForDpi(SM_CYFRAME, dpi) + GetSystemMetricsForDpi(SM_CXPADDEDBORDER, dpi);
            }
            LRESULT(0)
        }
        WM_NCHITTEST => {
            let r = DefWindowProcW(hwnd, msg, wparam, lparam);
            if r.0 as u32 != HTCLIENT {
                return r;
            }
            let mut pt = POINT { x: (lparam.0 & 0xffff) as i16 as i32, y: ((lparam.0 >> 16) & 0xffff) as i16 as i32 };
            let _ = ScreenToClient(hwnd, &mut pt);
            let (w, _) = client_size(hwnd);
            let hit = with_app(|a| {
                let b = hit_button(a, pt.x as f32, pt.y as f32, w as f32);
                let edge = (5.0 * a.scale) as i32;
                (b, edge)
            });
            let Some((b, edge)) = hit else { return r };
            match b {
                Hot::Min => return LRESULT(HTMINBUTTON as isize),
                Hot::Max => return LRESULT(HTMAXBUTTON as isize), // this is what brings up Snap Layouts
                Hot::Close => return LRESULT(HTCLOSE as isize),
                Hot::None => {}
            }
            if !IsZoomed(hwnd).as_bool() && pt.y < edge {
                let (l, rt) = (pt.x < edge * 2, pt.x >= w as i32 - edge * 2);
                return LRESULT(if l { HTTOPLEFT } else if rt { HTTOPRIGHT } else { HTTOP } as isize);
            }
            let cap = with_app(|a| (CAPTION_H * a.scale) as i32).unwrap_or(32);
            if pt.y < cap {
                return LRESULT(HTCAPTION as isize);
            }
            LRESULT(HTCLIENT as isize)
        }
        WM_NCMOUSEMOVE => {
            let hot = match wparam.0 as u32 {
                HTMINBUTTON => Hot::Min,
                HTMAXBUTTON => Hot::Max,
                HTCLOSE => Hot::Close,
                _ => Hot::None,
            };
            with_app(|a| {
                if !a.tracking {
                    let mut e = TRACKMOUSEEVENT { cbSize: std::mem::size_of::<TRACKMOUSEEVENT>() as u32, dwFlags: TME_LEAVE | TME_NONCLIENT, hwndTrack: hwnd, dwHoverTime: 0 };
                    let _ = TrackMouseEvent(&mut e);
                    a.tracking = true;
                }
                if a.hot != hot {
                    a.hot = hot;
                    let _ = InvalidateRect(Some(hwnd), None, false);
                }
            });
            DefWindowProcW(hwnd, msg, wparam, lparam)
        }
        WM_NCMOUSELEAVE | WM_MOUSEMOVE => {
            let (mx, my) = ((lparam.0 & 0xffff) as i16 as f32, ((lparam.0 >> 16) & 0xffff) as i16 as f32);
            let (cw, _) = client_size(hwnd);
            with_app(|a| {
                let mut dirty = false;
                if msg == WM_NCMOUSELEAVE {
                    a.tracking = false;
                } else {
                    if !a.client_tracking {
                        let mut e = TRACKMOUSEEVENT { cbSize: std::mem::size_of::<TRACKMOUSEEVENT>() as u32, dwFlags: TME_LEAVE, hwndTrack: hwnd, dwHoverTime: 0 };
                        let _ = TrackMouseEvent(&mut e);
                        a.client_tracking = true;
                    }
                    let (x, y, w) = local(a, mx, my, cw as f32);
                    dirty |= a.ui.mouse_move(mx, my);
                    if a.ui.page == Page::Routing {
                        dirty |= a.bay.mouse_move(&a.view.model, x, y, w);
                    }
                }
                if a.hot != Hot::None {
                    a.hot = Hot::None;
                    dirty = true;
                }
                if dirty {
                    redraw(hwnd, a);
                }
            });
            DefWindowProcW(hwnd, msg, wparam, lparam)
        }
        WM_MOUSELEAVE => {
            with_app(|a| {
                a.client_tracking = false;
                if !a.bay.dragging() && a.bay.leave() {
                    redraw(hwnd, a);
                }
            });
            LRESULT(0)
        }
        WM_LBUTTONDOWN => {
            let (mx, my) = ((lparam.0 & 0xffff) as i16 as f32, ((lparam.0 >> 16) & 0xffff) as i16 as f32);
            let (cw, _) = client_size(hwnd);
            with_app(|a| {
                let (x, y, w) = local(a, mx, my, cw as f32);
                if let Some(act) = a.ui.click(mx, my) {
                    match act {
                        Act::Toggle(id) => {
                            let on = a.view.model.tunnels.iter().find(|t| t.id == id).is_some_and(|t| t.enabled);
                            a.session.set_enabled(&id, !on);
                        }
                        Act::Switch(0) => shell::set_startup(a.ui.switches[0]),
                        Act::Switch(1) => {
                            a.prefs.close_to_tray = a.ui.switches[1];
                            let _ = a.store.save_ui(&a.prefs);
                        }
                        Act::Resync => a.session.resync(),
                        Act::RemoveStale(key) => a.session.remove_stale(&key),
                        _ => {}
                    }
                    sync_watch(a);
                    redraw(hwnd, a);
                } else if a.ui.page == Page::Routing {
                    let out = a.bay.press(&a.view.model, x, y, w);
                    if a.bay.dragging() {
                        SetCapture(hwnd);
                    }
                    run(hwnd, a, out);
                }
            });
            LRESULT(0)
        }
        WM_LBUTTONUP => {
            with_app(|a| {
                if a.bay.dragging() {
                    let _ = ReleaseCapture();
                    let out = a.bay.release(&a.view.model);
                    run(hwnd, a, out);
                }
            });
            LRESULT(0)
        }
        WM_MOUSEWHEEL => {
            let delta = ((wparam.0 >> 16) & 0xffff) as i16 as f32 / 120.0;
            with_app(|a| scroll_by(hwnd, a, -delta * 56.0));
            LRESULT(0)
        }
        WM_KEYDOWN => {
            with_app(|a| {
                let vk = wparam.0 as u32;
                let page = (a.gfx.size.1 as f32 / a.scale) * 0.8;
                match vk {
                    0x21 => scroll_by(hwnd, a, -page),
                    0x22 => scroll_by(hwnd, a, page),
                    _ => {}
                }
                let tab = a.ui.key(vk);
                sync_watch(a);
                if tab || (a.ui.page == Page::Routing && a.bay.key(&a.view.model, vk)) {
                    redraw(hwnd, a);
                }
            });
            LRESULT(0)
        }
        // The session thread changed the view.
        WM_APP => {
            with_app(|a| {
                a.view = a.session.view();
                a.bay.sync(&a.view.model);
                redraw(hwnd, a);
            });
            LRESULT(0)
        }
        // Drives the line draw-in; it is killed as soon as the animation is over.
        WM_TIMER => {
            with_app(|a| {
                if !a.bay.animating() {
                    let _ = KillTimer(Some(hwnd), 1);
                }
                let _ = InvalidateRect(Some(hwnd), None, false);
            });
            LRESULT(0)
        }
        // The default handler would draw its own caption buttons; the click is turned into the command here.
        WM_NCLBUTTONDOWN if matches!(wparam.0 as u32, HTMINBUTTON | HTMAXBUTTON | HTCLOSE) => LRESULT(0),
        WM_NCLBUTTONUP if matches!(wparam.0 as u32, HTMINBUTTON | HTMAXBUTTON | HTCLOSE) => {
            let cmd = match wparam.0 as u32 {
                HTMINBUTTON => SC_MINIMIZE,
                HTMAXBUTTON if IsZoomed(hwnd).as_bool() => SC_RESTORE,
                HTMAXBUTTON => SC_MAXIMIZE,
                _ => SC_CLOSE,
            };
            let _ = PostMessageW(Some(hwnd), WM_SYSCOMMAND, WPARAM(cmd as usize), LPARAM(0));
            LRESULT(0)
        }
        WM_GETMINMAXINFO => {
            let dpi = GetDpiForWindow(hwnd) as f32 / 96.0;
            let m = lparam.0 as *mut MINMAXINFO;
            (*m).ptMinTrackSize = POINT { x: (900.0 * dpi) as i32, y: (600.0 * dpi) as i32 };
            LRESULT(0)
        }
        WM_SIZE => {
            if wparam.0 as u32 != SIZE_MINIMIZED {
                let (w, h) = client_size(hwnd);
                with_app(|a| {
                    let _ = a.gfx.resize(w, h);
                });
                let _ = InvalidateRect(Some(hwnd), None, false);
            }
            LRESULT(0)
        }
        WM_DPICHANGED => {
            let r = &*(lparam.0 as *const RECT);
            let _ = SetWindowPos(hwnd, None, r.left, r.top, r.right - r.left, r.bottom - r.top, SWP_NOZORDER | SWP_NOACTIVATE);
            with_app(|a| {
                a.scale = (wparam.0 & 0xffff) as f32 / 96.0;
                a.gfx.reset_glyphs();
            });
            let _ = InvalidateRect(Some(hwnd), None, false);
            LRESULT(0)
        }
        // "ImmersiveColorSet" arrives when the user switches between light and dark.
        WM_SETTINGCHANGE => {
            with_app(|a| a.apply_theme(hwnd));
            let _ = InvalidateRect(Some(hwnd), None, false);
            DefWindowProcW(hwnd, msg, wparam, lparam)
        }
        WM_ERASEBKGND => LRESULT(1),
        WM_PAINT => {
            let _ = ValidateRect(Some(hwnd), None);
            with_app(|a| {
                if let Err(e) = a.draw(hwnd) {
                    eprintln!("draw failed: {e}");
                }
            });
            LRESULT(0)
        }
        WM_CLOSE => {
            let to_tray = with_app(|a| {
                a.remember_window(hwnd);
                a.prefs.close_to_tray && !a.quitting
            });
            if to_tray == Some(true) {
                let _ = ShowWindow(hwnd, SW_HIDE);
                LRESULT(0)
            } else {
                DefWindowProcW(hwnd, msg, wparam, lparam)
            }
        }
        shell::WM_TRAY => {
            match (lparam.0 & 0xffff) as u32 {
                WM_LBUTTONUP => show_window(hwnd),
                WM_RBUTTONUP | WM_CONTEXTMENU => match shell::tray_menu(hwnd) {
                    shell::TrayChoice::Show => show_window(hwnd),
                    shell::TrayChoice::Quit => {
                        with_app(|a| a.quitting = true);
                        let _ = DestroyWindow(hwnd);
                    }
                    shell::TrayChoice::Nothing => {}
                },
                _ => {}
            }
            LRESULT(0)
        }
        shell::WM_SHOW => {
            show_window(hwnd);
            LRESULT(0)
        }
        WM_DESTROY => {
            with_app(|a| a.remember_window(hwnd));
            shell::remove_tray(hwnd);
            PostQuitMessage(0);
            LRESULT(0)
        }
        _ => DefWindowProcW(hwnd, msg, wparam, lparam),
    }
}

fn main() -> Result<()> {
    unsafe {
        let _ = SetProcessDpiAwarenessContext(DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2);
        let inst: HINSTANCE = GetModuleHandleW(None)?.into();
        let class = w!("PlaitwayUi");
        // One copy per daemon pipe; a second one asks the first to show itself and leaves.
        let pipe = std::env::var("PLAITWAY_PIPE").unwrap_or_else(|_| plaitway_core::DEFAULT_PIPE.to_string());
        if !shell::claim(&format!("PlaitwayUi-{}", pipe.replace('\\', "_")), class) {
            return Ok(());
        }
        let icon = shell::icon();
        RegisterClassExW(&WNDCLASSEXW { cbSize: std::mem::size_of::<WNDCLASSEXW>() as u32, style: CS_HREDRAW | CS_VREDRAW, lpfnWndProc: Some(wndproc), hInstance: inst, hCursor: LoadCursorW(None, IDC_ARROW)?, hIcon: icon, hIconSm: icon, lpszClassName: class, ..Default::default() });
        let hwnd = CreateWindowExW(WS_EX_NOREDIRECTIONBITMAP, class, w!("Plaitway"), WS_OVERLAPPEDWINDOW & !WS_SYSMENU, // no WS_SYSMENU: DWM then draws no caption buttons of its own beside ours
CW_USEDEFAULT, CW_USEDEFAULT, 1280, 800, None, None, Some(inst), None)?;

        let scale = GetDpiForWindow(hwnd) as f32 / 96.0;
        let _ = SetWindowPos(hwnd, None, 0, 0, (1280.0 * scale) as i32, (800.0 * scale) as i32, SWP_NOMOVE | SWP_NOZORDER | SWP_NOACTIVATE);
        let (w, h) = client_size(hwnd);

        let build = build_number();
        // PLAITWAY_SOLID=1 takes the Windows 10 path on any build, to look at it without a Windows 10 machine.
        let mica = build >= MICA_BUILD && std::env::var_os("PLAITWAY_SOLID").is_none();
        if mica {
            // The glass sheet and the backdrop type are what let the transparent pixels show Mica.
            let m = MARGINS { cxLeftWidth: -1, cxRightWidth: -1, cyTopHeight: -1, cyBottomHeight: -1 };
            let _ = DwmExtendFrameIntoClientArea(hwnd, &m);
            set_attr(hwnd, 38, 2); // DWMWA_SYSTEMBACKDROP_TYPE = DWMSBT_MAINWINDOW
        }

        let mut gfx = Gfx::new(hwnd, w, h)?;
        let f_text = gfx.load_font(&["Segoe UI Variable Text", "Segoe UI"], DWRITE_FONT_WEIGHT_NORMAL)?;
        let f_bold = gfx.load_font(&["Segoe UI Variable Text", "Segoe UI"], DWRITE_FONT_WEIGHT_SEMI_BOLD)?;
        let f_mono = gfx.load_font(&["Cascadia Mono", "Consolas"], DWRITE_FONT_WEIGHT_NORMAL)?;
        let f_icons = gfx.load_font(&["Segoe Fluent Icons", "Segoe MDL2 Assets"], DWRITE_FONT_WEIGHT_NORMAL)?;
        // The session thread wakes the window with WM_APP whenever the daemon tells it something.
        let handle = hwnd.0 as isize;
        let session = Session::start(&pipe, move || {
            let _ = PostMessageW(Some(HWND(handle as *mut c_void)), WM_APP, WPARAM(0), LPARAM(0));
        });
        let view = session.view();
        let store = Store::new(Store::default_dir());
        let prefs = store.ui();
        let mut app = App { gfx, theme: theme(false), mica, scale, hot: Hot::None, tracking: false, client_tracking: false, bay: Bay::new(), session, view, ui: Ui::new(), watching: (None, false), store, prefs, quitting: false, scroll: 0.0, max_scroll: 0.0, scroll_key: (Page::Routing, 0), f_mono, frames: 0, f_text, f_bold, f_icons };
        app.apply_theme(hwnd);
        APP.with(|a| *a.borrow_mut() = Some(app));

        let mut animate = BOOL(1);
        let _ = SystemParametersInfoW(SPI_GETCLIENTAREAANIMATION, 0, Some(&mut animate as *mut BOOL as *mut c_void), SYSTEM_PARAMETERS_INFO_UPDATE_FLAGS(0));
        with_app(|a| {
            a.bay.set_reduced_motion(!animate.as_bool());
            a.ui.switches[0] = shell::startup_command().is_some();
            a.ui.switches[1] = a.prefs.close_to_tray;
        });
        shell::add_tray(hwnd, icon);
        let placed = with_app(|a| a.prefs.window).flatten().is_some_and(|p| shell::restore(hwnd, &p));
        // `--hidden` (what the Run key starts) leaves the window in the notification area.
        if std::env::args().any(|a| a == "--hidden") {
            let _ = ShowWindow(hwnd, SW_HIDE);
        } else if !placed {
            let _ = ShowWindow(hwnd, SW_SHOW);
        }
        with_app(|a| redraw(hwnd, a));
        let mut msg = MSG::default();
        while GetMessageW(&mut msg, None, 0, 0).as_bool() {
            let _ = TranslateMessage(&msg);
            DispatchMessageW(&msg);
        }
    }
    Ok(())
}















