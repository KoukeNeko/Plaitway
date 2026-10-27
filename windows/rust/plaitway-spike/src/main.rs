//! Phase 0 spike: a window with Mica (Windows 11) or a solid Fluent background (Windows 10),
//! a transparent composition swap chain, and a caption drawn by us that keeps Snap Layouts.
#![windows_subsystem = "windows"]

mod gfx;

use std::{cell::RefCell, ffi::c_void};

use gfx::Gfx;
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
    build: u32,
    mica: bool,
    scale: f32,
    hot: Hot,
    tracking: bool,
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

        // Sidebar: Mica shows through; only the selected row and the profile dots are drawn.
        g.rect(8.0 * s, cap + 6.0 * s, (SIDEBAR_W - 18.0) * s, 40.0 * s, 6.0 * s, t.selected);
        g.outline(8.0 * s, cap + 6.0 * s, (SIDEBAR_W - 18.0) * s, 40.0 * s, 6.0 * s, 1.0, line);
        g.text(self.f_text, 14.0 * s, 44.0 * s, cap + 26.0 * s, "Routing", ink);
        g.text(self.f_bold, 12.0 * s, 22.0 * s, cap + 78.0 * s, "PROFILES", muted);
        let profiles = [("Home", "WireGuard · Holds 3 of 3", 0x0A9B94u32), ("Office", "OpenVPN · Holds 1 of 4", 0xD9680F), ("Lab", "WireGuard · Holds 1 of 2", 0xC0306B)];
        for (i, (name, sub, c)) in profiles.iter().enumerate() {
            let cy = cap + (118.0 + i as f32 * 56.0) * s;
            g.rect(22.0 * s, cy - 6.0 * s, 12.0 * s, 12.0 * s, 6.0 * s, rgba(*c, 1.0));
            g.text(self.f_text, 14.0 * s, 46.0 * s, cy - 8.0 * s, name, ink);
            g.text(self.f_text, 12.0 * s, 46.0 * s, cy + 10.0 * s, sub, muted);
        }

        // Content surface: a translucent layer over Mica. Its right and bottom corners are pushed off screen.
        let (cx, cy0) = (SIDEBAR_W * s, cap);
        g.rect(cx, cy0, w - cx + 20.0 * s, h - cy0 + 20.0 * s, 10.0 * s, t.surface);
        g.outline(cx, cy0, w - cx + 20.0 * s, h - cy0 + 20.0 * s, 10.0 * s, 1.0, line);

        let (px, mut y) = (cx + 32.0 * s, cy0 + 56.0 * s);
        g.text(self.f_bold, 28.0 * s, px, y, "Phase 0 spike", ink);
        y += 40.0 * s;
        let backdrop = if self.mica { format!("Backdrop: Mica (build {})", self.build) } else { format!("Backdrop: solid colour, no Mica (build {})", self.build) };
        let dpi = (96.0 * s).round() as u32;
        let rows = [backdrop, format!("Scale: {}% ({} dpi)", (s * 100.0).round(), dpi), format!("Theme: {}", if t.dark { "dark" } else { "light" }), format!("Frames drawn since start: {}", self.frames + 1)];
        for r in rows {
            g.text(self.f_text, 14.0 * s, px, y, &r, muted);
            y += 26.0 * s;
        }
        y += 18.0 * s;
        for (i, (name, c)) in [("Home", 0x0A9B94u32), ("Office", 0xD9680F), ("Lab", 0xC0306B)].iter().enumerate() {
            let x = px + i as f32 * 132.0 * s;
            g.rect(x, y, 120.0 * s, 64.0 * s, 10.0 * s, rgba(*c, if t.dark { 0.22 } else { 0.14 }));
            g.outline(x, y, 120.0 * s, 64.0 * s, 10.0 * s, 1.0, rgba(*c, 0.5));
            g.text(self.f_bold, 14.0 * s, x + 14.0 * s, y + 22.0 * s, name, ink);
            g.text(self.f_text, 12.0 * s, x + 14.0 * s, y + 44.0 * s, "Connected", muted);
        }

        self.frames += 1;
        let clear = if self.mica { [0.0; 4] } else { self.theme.solid };
        self.gfx.render(clear)
    }
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
            with_app(|a| {
                a.tracking = false;
                if a.hot != Hot::None {
                    a.hot = Hot::None;
                    let _ = InvalidateRect(Some(hwnd), None, false);
                }
            });
            DefWindowProcW(hwnd, msg, wparam, lparam)
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
        WM_DESTROY => {
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
        let class = w!("PlaitwaySpike");
        RegisterClassExW(&WNDCLASSEXW { cbSize: std::mem::size_of::<WNDCLASSEXW>() as u32, style: CS_HREDRAW | CS_VREDRAW, lpfnWndProc: Some(wndproc), hInstance: inst, hCursor: LoadCursorW(None, IDC_ARROW)?, lpszClassName: class, ..Default::default() });
        let hwnd = CreateWindowExW(WS_EX_NOREDIRECTIONBITMAP, class, w!("Plaitway"), WS_OVERLAPPEDWINDOW, CW_USEDEFAULT, CW_USEDEFAULT, 1280, 800, None, None, Some(inst), None)?;

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
        let f_icons = gfx.load_font(&["Segoe Fluent Icons", "Segoe MDL2 Assets"], DWRITE_FONT_WEIGHT_NORMAL)?;
        let mut app = App { gfx, theme: theme(false), build, mica, scale, hot: Hot::None, tracking: false, frames: 0, f_text, f_bold, f_icons };
        app.apply_theme(hwnd);
        APP.with(|a| *a.borrow_mut() = Some(app));

        let _ = ShowWindow(hwnd, SW_SHOW);
        let mut msg = MSG::default();
        while GetMessageW(&mut msg, None, 0, 0).as_bool() {
            let _ = TranslateMessage(&msg);
            DispatchMessageW(&msg);
        }
    }
    Ok(())
}
