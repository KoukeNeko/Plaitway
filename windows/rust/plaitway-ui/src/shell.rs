//! The parts of the app that belong to the shell: the notification-area icon, one running copy, starting
//! with Windows, and the icon itself.

use std::ffi::c_void;

use windows::{
    core::*,
    Win32::{
        Foundation::*,
        Graphics::Gdi::*,
        System::{
            LibraryLoader::GetModuleFileNameW,
            Registry::*,
            Threading::CreateMutexW,
        },
        UI::{Shell::*, WindowsAndMessaging::*},
    },
};

/// The notification-area icon was clicked (`lparam` carries the mouse message).
pub const WM_TRAY: u32 = WM_APP + 1;
/// Another copy was started: show this one.
pub const WM_SHOW: u32 = WM_APP + 2;

const TRAY_ID: u32 = 1;
const CMD_SHOW: usize = 1;
const CMD_QUIT: usize = 2;
const RUN_KEY: PCWSTR = w!(r"Software\Microsoft\Windows\CurrentVersion\Run");
/// The name of the Run value; PLAITWAY_RUN_VALUE gives a test its own, so it cannot touch the real one.
fn run_value() -> Vec<u16> {
    std::env::var("PLAITWAY_RUN_VALUE").unwrap_or_else(|_| "Plaitway".into()).encode_utf16().chain(Some(0)).collect()
}

/// True when this is the first copy for `name`. The mutex lives as long as the process.
pub fn first_instance(name: &str) -> bool {
    let wide: Vec<u16> = format!("Local\\{name}").encode_utf16().chain(Some(0)).collect();
    unsafe {
        let Ok(h) = CreateMutexW(None, false, PCWSTR(wide.as_ptr())) else { return true };
        if GetLastError() == ERROR_ALREADY_EXISTS {
            let _ = CloseHandle(h);
            return false;
        }
        // Never closed on purpose: the mutex is the proof that this copy runs.
        true
    }
}

/// Become the one running copy, or hand over to it. A copy that is still shutting down has no window
/// any more but still holds the mutex, so for a few seconds the answer is to wait for it.
pub fn claim(name: &str, class: PCWSTR) -> bool {
    for _ in 0..40 {
        if first_instance(name) {
            return true;
        }
        let shown = unsafe { FindWindowW(class, PCWSTR::null()).is_ok() };
        if shown {
            show_running(class);
            return false;
        }
        std::thread::sleep(std::time::Duration::from_millis(100));
    }
    false
}

/// Ask the copy that is already running to show itself.
pub fn show_running(class: PCWSTR) {
    unsafe {
        if let Ok(h) = FindWindowW(class, PCWSTR::null()) {
            let _ = PostMessageW(Some(h), WM_SHOW, WPARAM(0), LPARAM(0));
        }
    }
}
/// A 32 by 32 icon: the teal square of the title bar, drawn here so the app needs no resource file.
pub fn icon() -> HICON {
    const N: i32 = 32;
    let (r, cx) = (7.0f32, N as f32 / 2.0);
    let mut px = vec![0u8; (N * N * 4) as usize];
    for y in 0..N {
        for x in 0..N {
            // Distance to a rounded square of half-size 14 and corner radius r.
            let (dx, dy) = ((x as f32 + 0.5 - cx).abs() - (14.0 - r), (y as f32 + 0.5 - cx).abs() - (14.0 - r));
            let d = (dx.max(0.0).powi(2) + dy.max(0.0).powi(2)).sqrt() + dx.max(dy).min(0.0) - r;
            let a = (0.5 - d).clamp(0.0, 1.0);
            let i = ((y * N + x) * 4) as usize;
            // BGRA, premultiplied: #0A9B94.
            px[i..i + 4].copy_from_slice(&[(0x94 as f32 * a) as u8, (0x9B as f32 * a) as u8, (0x0A as f32 * a) as u8, (255.0 * a) as u8]);
        }
    }
    unsafe {
        let color = CreateBitmap(N, N, 1, 32, Some(px.as_ptr() as *const c_void));
        let mask = CreateBitmap(N, N, 1, 1, None);
        let icon = CreateIconIndirect(&ICONINFO { fIcon: true.into(), xHotspot: 0, yHotspot: 0, hbmMask: mask, hbmColor: color }).unwrap_or_default();
        let _ = DeleteObject(color.into());
        let _ = DeleteObject(mask.into());
        icon
    }
}

fn tray_data(hwnd: HWND) -> NOTIFYICONDATAW {
    NOTIFYICONDATAW { cbSize: std::mem::size_of::<NOTIFYICONDATAW>() as u32, hWnd: hwnd, uID: TRAY_ID, ..Default::default() }
}

pub fn add_tray(hwnd: HWND, icon: HICON) {
    let mut d = tray_data(hwnd);
    d.uFlags = NIF_ICON | NIF_MESSAGE | NIF_TIP;
    d.uCallbackMessage = WM_TRAY;
    d.hIcon = icon;
    for (i, c) in "Plaitway".encode_utf16().enumerate() {
        d.szTip[i] = c;
    }
    unsafe {
        let _ = Shell_NotifyIconW(NIM_ADD, &d);
    }
}

pub fn remove_tray(hwnd: HWND) {
    unsafe {
        let _ = Shell_NotifyIconW(NIM_DELETE, &tray_data(hwnd));
    }
}

#[derive(Clone, Copy, PartialEq, Debug)]
pub enum TrayChoice {
    Show,
    Quit,
    Nothing,
}

/// The menu of the tray icon, at the pointer.
pub fn tray_menu(hwnd: HWND) -> TrayChoice {
    unsafe {
        let Ok(menu) = CreatePopupMenu() else { return TrayChoice::Nothing };
        let wide = |text: &str| -> Vec<u16> { plaitway_core::tr(text).encode_utf16().chain(Some(0)).collect() };
        let _ = AppendMenuW(menu, MF_STRING, CMD_SHOW, PCWSTR(wide("Show Plaitway").as_ptr()));
        let _ = AppendMenuW(menu, MF_SEPARATOR, 0, PCWSTR::null());
        let _ = AppendMenuW(menu, MF_STRING, CMD_QUIT, PCWSTR(wide("Quit").as_ptr()));
        let mut at = POINT::default();
        let _ = GetCursorPos(&mut at);
        // Without this the menu would not close when the user clicks elsewhere.
        let _ = SetForegroundWindow(hwnd);
        let cmd = TrackPopupMenu(menu, TPM_RETURNCMD | TPM_RIGHTBUTTON, at.x, at.y, None, hwnd, None);
        let _ = DestroyMenu(menu);
        match cmd.0 as usize {
            CMD_SHOW => TrayChoice::Show,
            CMD_QUIT => TrayChoice::Quit,
            _ => TrayChoice::Nothing,
        }
    }
}

pub fn exe_path() -> String {
    let mut buf = [0u16; 520];
    let n = unsafe { GetModuleFileNameW(None, &mut buf) } as usize;
    String::from_utf16_lossy(&buf[..n])
}

/// What the Run key holds for the app, if anything.
pub fn startup_command() -> Option<String> {
    let mut buf = [0u16; 1024];
    let mut bytes = (buf.len() * 2) as u32;
    let r = unsafe { RegGetValueW(HKEY_CURRENT_USER, RUN_KEY, PCWSTR(run_value().as_ptr()), RRF_RT_REG_SZ, None, Some(buf.as_mut_ptr() as *mut c_void), Some(&mut bytes)) };
    r.is_ok().then(|| String::from_utf16_lossy(&buf[..(bytes as usize / 2).saturating_sub(1)]))
}

/// Start the app, hidden, when the user signs in; or stop doing that.
pub fn set_startup(on: bool) {
    unsafe {
        if on {
            let cmd: Vec<u16> = format!("\"{}\" --hidden", exe_path()).encode_utf16().chain(Some(0)).collect();
            let _ = RegSetKeyValueW(HKEY_CURRENT_USER, RUN_KEY, PCWSTR(run_value().as_ptr()), REG_SZ.0, Some(cmd.as_ptr() as *const c_void), (cmd.len() * 2) as u32);
        } else {
            let _ = RegDeleteKeyValueW(HKEY_CURRENT_USER, RUN_KEY, PCWSTR(run_value().as_ptr()));
        }
    }
}

/// Where the window was, from `GetWindowPlacement`.
pub fn placement(hwnd: HWND) -> Option<plaitway_core::Placement> {
    let mut p = WINDOWPLACEMENT { length: std::mem::size_of::<WINDOWPLACEMENT>() as u32, ..Default::default() };
    unsafe { GetWindowPlacement(hwnd, &mut p).ok()? };
    let r = p.rcNormalPosition;
    Some(plaitway_core::Placement { x: r.left, y: r.top, w: r.right - r.left, h: r.bottom - r.top, maximized: p.showCmd == SW_SHOWMAXIMIZED.0 as u32 })
}

/// Put the window back where it was, unless that is now off every screen.
pub fn restore(hwnd: HWND, p: &plaitway_core::Placement) -> bool {
    let rect = RECT { left: p.x, top: p.y, right: p.x + p.w, bottom: p.y + p.h };
    unsafe {
        if MonitorFromRect(&rect, MONITOR_DEFAULTTONULL).is_invalid() || p.w < 400 || p.h < 300 {
            return false;
        }
        let wp = WINDOWPLACEMENT { length: std::mem::size_of::<WINDOWPLACEMENT>() as u32, rcNormalPosition: rect, showCmd: if p.maximized { SW_SHOWMAXIMIZED.0 as u32 } else { SW_SHOWNORMAL.0 as u32 }, ..Default::default() };
        SetWindowPlacement(hwnd, &wp).is_ok()
    }
}



