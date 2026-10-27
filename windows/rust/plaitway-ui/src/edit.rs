//! Native text boxes over the window. A self-drawn text box has to redo selection, undo, the clipboard,
//! IME and screen-reader behaviour, and that is where such boxes usually go wrong; the system `EDIT`
//! control already does all of it. The pages say where a box goes while they draw, and `apply` moves the
//! real controls there after the frame.
//!
//! A control cannot be a child of the window: the swap chain is composed by DirectComposition above every
//! child window, whatever their order. So each box sits in a small borderless window owned by the main
//! one, which the desktop composes above it, and which follows it when it moves.

use std::ffi::c_void;

use windows::{
    core::*,
    Win32::{
        Foundation::*,
        Graphics::Gdi::*,
        UI::{
            Controls::{SetWindowTheme, EM_SETLIMITTEXT, EM_SETRECTNP, EM_SETSEL},
            Input::KeyboardAndMouse::*,
            Shell::*,
            WindowsAndMessaging::*,
        },
    },
};

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Field {
    /// The text of a profile.
    Config,
    Username,
    Password,
    /// The name of a profile, in place of its title.
    Rename,
}

impl Field {
    fn id(self) -> usize {
        self as usize + 1
    }

    fn from_id(id: usize) -> Option<Field> {
        [Field::Config, Field::Username, Field::Password, Field::Rename].into_iter().find(|f| f.id() == id)
    }

    fn multiline(self) -> bool {
        self == Field::Config
    }
}

/// Sent to the main window when Enter (or Ctrl+S in the text of a profile) is pressed in a box.
pub const WM_FIELD_ENTER: u32 = WM_APP + 3;
/// Sent to the main window when Escape is pressed in a box.
pub const WM_FIELD_ESCAPE: u32 = WM_APP + 4;

pub struct Hosted {
    parent: HWND,
    /// (field, the borderless window, the control inside it)
    boxes: Vec<(Field, HWND, HWND)>,
    last: Vec<(Field, Rect)>,
    fonts: Option<(HFONT, HFONT, u32)>,
    brush: HBRUSH,
    colors: (COLORREF, COLORREF),
}

/// Rectangle in client pixels.
pub type Rect = (f32, f32, f32, f32);

impl Hosted {
    pub fn new(parent: HWND) -> Self {
        Hosted { parent, boxes: Vec::new(), last: Vec::new(), fonts: None, brush: HBRUSH::default(), colors: (COLORREF(0), COLORREF(0)) }
    }

    fn rgb(c: [f32; 4]) -> COLORREF {
        COLORREF((c[0] * 255.0) as u32 | (((c[1] * 255.0) as u32) << 8) | (((c[2] * 255.0) as u32) << 16))
    }

    /// Text and background colours of the boxes; the background is opaque, since a control cannot be see-through.
    pub fn set_colors(&mut self, text: [f32; 4], back: [f32; 4], dark: bool) {
        let colors = (Self::rgb(text), Self::rgb(back));
        if colors != self.colors || self.brush.is_invalid() {
            self.colors = colors;
            unsafe {
                if !self.brush.is_invalid() {
                    let _ = DeleteObject(self.brush.into());
                }
                self.brush = CreateSolidBrush(colors.1);
                for (_, _, h) in &self.boxes {
                    // Dark scroll bars and menus follow the theme.
                    let _ = SetWindowTheme(*h, if dark { w!("DarkMode_Explorer") } else { w!("Explorer") }, PCWSTR::null());
                    let _ = InvalidateRect(Some(*h), None, true);
                }
            }
        }
    }

    fn fonts(&mut self, dpi: u32) -> (HFONT, HFONT) {
        if let Some((a, b, d)) = self.fonts {
            if d == dpi {
                return (a, b);
            }
            unsafe {
                let _ = DeleteObject(a.into());
                let _ = DeleteObject(b.into());
            }
        }
        let make = |face: PCWSTR, pt: i32| unsafe { CreateFontW(-(pt * dpi as i32 / 96), 0, 0, 0, 400, 0, 0, 0, DEFAULT_CHARSET, OUT_DEFAULT_PRECIS, CLIP_DEFAULT_PRECIS, CLEARTYPE_QUALITY, 0, face) };
        let (text, mono) = (make(w!("Segoe UI Variable Text"), 14), make(w!("Cascadia Mono"), 13));
        self.fonts = Some((text, mono, dpi));
        (text, mono)
    }

    fn find(&self, f: Field) -> Option<HWND> {
        self.boxes.iter().find(|b| b.0 == f).map(|b| b.2)
    }

    fn frame_of(&self, f: Field) -> Option<HWND> {
        self.boxes.iter().find(|b| b.0 == f).map(|b| b.1)
    }

    fn create(&mut self, f: Field) -> HWND {
        let class = frame_class();
        let name: Vec<u16> = f.id().to_string().encode_utf16().chain(Some(0)).collect();
        let frame = unsafe { CreateWindowExW(WS_EX_TOOLWINDOW, class, PCWSTR(name.as_ptr()), WS_POPUP | WS_CLIPCHILDREN, 0, 0, 10, 10, Some(self.parent), None, None, None) }.unwrap_or_default();
        let kind = if f.multiline() { (ES_MULTILINE | ES_AUTOVSCROLL | ES_WANTRETURN) as u32 | WS_VSCROLL.0 } else { ES_AUTOHSCROLL as u32 };
        let password = if f == Field::Password { ES_PASSWORD as u32 } else { 0 };
        let style = WS_CHILD | WS_VISIBLE | WINDOW_STYLE(kind | password);
        let h = unsafe { CreateWindowExW(WINDOW_EX_STYLE(0), w!("EDIT"), PCWSTR::null(), style, 0, 0, 10, 10, Some(frame), Some(HMENU(f.id() as *mut c_void)), None, None) }.unwrap_or_default();
        unsafe {
            let _ = SetWindowSubclass(h, Some(subclass), f.id(), 0);
            // The text of a profile is far below this, but the default limit of 32 KB is not a good one to hit.
            SendMessageW(h, EM_SETLIMITTEXT, Some(WPARAM(1 << 20)), None);
        }
        self.boxes.push((f, frame, h));
        h
    }

    /// Show the boxes in `want` at their places and hide every other one.
    pub fn apply(&mut self, want: &[(Field, Rect)], dpi: u32, dark: bool) {
        self.last = want.to_vec();
        self.place(dpi, dark);
    }

    /// Put the boxes where they were last asked to be; the window may have moved since.
    pub fn place(&mut self, dpi: u32, dark: bool) {
        let want = self.last.clone();
        let (text_font, mono_font) = self.fonts(dpi);
        let mut origin = POINT::default();
        unsafe {
            let _ = ClientToScreen(self.parent, &mut origin);
        }
        for (f, r) in &want {
            if self.find(*f).is_none() {
                self.create(*f);
            }
            let (frame, h) = (self.frame_of(*f).unwrap(), self.find(*f).unwrap());
            unsafe {
                let font = if *f == Field::Config { mono_font } else { text_font };
                SendMessageW(h, WM_SETFONT, Some(WPARAM(font.0 as usize)), Some(LPARAM(1)));
                let _ = SetWindowTheme(h, if dark { w!("DarkMode_Explorer") } else { w!("Explorer") }, PCWSTR::null());
                let (x, y, w, hh) = (r.0.round() as i32, r.1.round() as i32, r.2.round() as i32, r.3.round() as i32);
                let _ = SetWindowPos(frame, None, origin.x + x, origin.y + y, w, hh, SWP_NOZORDER | SWP_NOACTIVATE | SWP_SHOWWINDOW);
                let _ = SetWindowPos(h, None, 0, 0, w, hh, SWP_NOZORDER | SWP_NOACTIVATE);
                // A little room inside, so the text does not touch the edge of the card.
                let pad = (6 * dpi / 96) as i32;
                let rect = RECT { left: pad, top: pad / 2, right: w - pad, bottom: hh - pad / 2 };
                SendMessageW(h, EM_SETRECTNP, None, Some(LPARAM(&rect as *const RECT as isize)));
            }
        }
        for (f, frame, _) in &self.boxes {
            if !want.iter().any(|w| w.0 == *f) {
                unsafe {
                    let _ = ShowWindow(*frame, SW_HIDE);
                }
            }
        }
    }

    pub fn text(&self, f: Field) -> String {
        let Some(h) = self.find(f) else { return String::new() };
        unsafe {
            let n = GetWindowTextLengthW(h) as usize;
            let mut buf = vec![0u16; n + 1];
            let got = GetWindowTextW(h, &mut buf) as usize;
            // The control stores line breaks as CR LF; the daemon and the profile files use LF.
            String::from_utf16_lossy(&buf[..got]).replace("\r\n", "\n")
        }
    }

    pub fn set_text(&mut self, f: Field, text: &str) {
        if self.find(f).is_none() {
            self.create(f);
        }
        let h = self.find(f).unwrap();
        let wide: Vec<u16> = text.replace("\r\n", "\n").replace('\n', "\r\n").encode_utf16().chain(Some(0)).collect();
        unsafe {
            let _ = SetWindowTextW(h, PCWSTR(wide.as_ptr()));
        }
    }

    pub fn clear(&mut self, f: Field) {
        self.set_text(f, "");
    }

    pub fn focus(&self, f: Field) {
        if let Some(h) = self.find(f) {
            unsafe {
                let _ = SetFocus(Some(h));
                SendMessageW(h, EM_SETSEL, Some(WPARAM(0)), Some(LPARAM(-1)));
            }
        }
    }

    /// `WM_CTLCOLOREDIT`: the colours of a box.
    pub fn paint(&self, hdc: HDC) -> LRESULT {
        unsafe {
            SetTextColor(hdc, self.colors.0);
            SetBkColor(hdc, self.colors.1);
        }
        LRESULT(self.brush.0 as isize)
    }

    /// Which box a `WM_COMMAND` from a control belongs to.
    pub fn field_of(id: usize) -> Option<Field> {
        Field::from_id(id)
    }
}

/// Enter, Escape and Tab are the form's, not the box's.
unsafe extern "system" fn subclass(hwnd: HWND, msg: u32, wparam: WPARAM, lparam: LPARAM, id: usize, _: usize) -> LRESULT {
    let field = Field::from_id(id);
    let ctrl = GetKeyState(VK_CONTROL.0 as i32) < 0;
    // The box's own window forwards these to the main window.
    let frame = GetParent(hwnd).unwrap_or_default();
    match msg {
        WM_KEYDOWN => match VIRTUAL_KEY(wparam.0 as u16) {
            VK_RETURN if field.is_some_and(|f| !f.multiline()) => {
                let _ = PostMessageW(Some(frame), WM_FIELD_ENTER, WPARAM(id), LPARAM(0));
                return LRESULT(0);
            }
            VK_ESCAPE => {
                let _ = PostMessageW(Some(frame), WM_FIELD_ESCAPE, WPARAM(id), LPARAM(0));
                return LRESULT(0);
            }
            _ => {}
        },
        // Ctrl+S saves the text of a profile.
        WM_CHAR if ctrl && wparam.0 == 0x13 => {
            let _ = PostMessageW(Some(frame), WM_FIELD_ENTER, WPARAM(id), LPARAM(0));
            return LRESULT(0);
        }
        // The beep for a key the box does not take.
        WM_CHAR if matches!(wparam.0, 0x0D | 0x1B) && field.is_some_and(|f| !f.multiline()) => return LRESULT(0),
        // Ctrl+A is not built into a plain EDIT.
        WM_CHAR if ctrl && wparam.0 == 0x01 => {
            SendMessageW(hwnd, EM_SETSEL, Some(WPARAM(0)), Some(LPARAM(-1)));
            return LRESULT(0);
        }
        _ => {}
    }
    DefSubclassProc(hwnd, msg, wparam, lparam)
}

/// The class of the borderless window around a box; it hands what the box says to the main window.
fn frame_class() -> PCWSTR {
    static REGISTERED: std::sync::Once = std::sync::Once::new();
    REGISTERED.call_once(|| unsafe {
        let class = WNDCLASSEXW { cbSize: std::mem::size_of::<WNDCLASSEXW>() as u32, lpfnWndProc: Some(frame_proc), lpszClassName: w!("PlaitwayField"), hCursor: LoadCursorW(None, IDC_IBEAM).unwrap_or_default(), ..Default::default() };
        RegisterClassExW(&class);
    });
    w!("PlaitwayField")
}

unsafe extern "system" fn frame_proc(hwnd: HWND, msg: u32, wparam: WPARAM, lparam: LPARAM) -> LRESULT {
    let owner = GetWindow(hwnd, GW_OWNER).unwrap_or_default();
    match msg {
        WM_CTLCOLOREDIT | WM_COMMAND | WM_FIELD_ENTER | WM_FIELD_ESCAPE => SendMessageW(owner, msg, Some(wparam), Some(lparam)),
        _ => DefWindowProcW(hwnd, msg, wparam, lparam),
    }
}
