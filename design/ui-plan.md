# Plaitway for Windows: Patch Bay UI, layout and self-draw plan

Direction: **A, Patch Bay**. The prototype is `design/a-full/index.html` (open it in a browser; resize below 1000 px to see the compact layout). Screenshots are in `design/a-full/shots/`.

North star: the first thing the window answers is *who holds what*. Routes sit on the left, tunnels on the right, one line each. A request that waits behind a higher priority is a dashed line with a ×.

## 1. Layout plan

Pages are the ones the C# app already has (`Navigation.cs`), so the logic of `AppCore` carries over.

| Page | Content | Prototype |
|---|---|---|
| Routing (home) | patch bay, "why" strip for the selected route, traffic | `#/routing` |
| Profile › Overview, Routes, Logs, Configuration, Settings | state and uptime, traffic, waiting routes, details | `#/profile/office/overview` and its tabs |
| Diagnostics › Overview, Daemon log | network, owned routes, recent changes, stale routes | `#/diagnostics` |
| Settings | language, start with Windows, close to tray, helper, version | `#/settings` |
| Empty and Setup | no profile yet, helper not installed | `#/empty`, `#/setup` |

| Window width | Sidebar | Routing page |
|---|---|---|
| 1000 px and up (default 1280×800) | 248 px, name, protocol, "Holds 3 of 3", priority number | three columns: routes, lines, tunnels |
| under 1000 px (minimum 900×600) | 64 px rail, one colour dot per profile, name in a tooltip | list of routes, each with a "Home holds" chip and "Office waits" chips; no lines |

Behaviour worth keeping:
- **Priority is the order in the sidebar** and the number on each row. Real app: drag to reorder (as today). Prototype: the "Move Office above Home" button in the why strip.
- Hovering a route or a tunnel dims every other line. Selecting a route fills the why strip.
- Connecting or disconnecting a tunnel recomputes who holds each route, so the lines move at once (try *Disconnect* on Home).
- One colour per profile (teal, orange, magenta; lighter variants in dark mode). Everything else is neutral. Colour means "which tunnel", never "good or bad"; state uses a filled or hollow dot and wording.

Motion (three, all ≤ 600 ms, off under reduced motion): lines draw in when the page opens, page change is a 180 ms fade and 6 px rise, hover dims the other lines in 200 ms.

## 2. Mica and the self-drawn scope

### What "Mica plus self-drawn" means in Win32 terms
1. A plain Win32 window (`windows` crate), extended style `WS_EX_NOREDIRECTIONBITMAP`.
2. `DwmSetWindowAttribute(DWMWA_SYSTEMBACKDROP_TYPE, DWMSBT_MAINWINDOW)` for Mica (`DWMSBT_TABBEDWINDOW` is Mica Alt). `DwmExtendFrameIntoClientArea` with margins of −1. `DWMWA_USE_IMMERSIVE_DARK_MODE` follows the app theme.
3. A **composition swap chain**: `CreateSwapChainForComposition`, `DXGI_ALPHA_MODE_PREMULTIPLIED`, attached to a DirectComposition visual. Any pixel the renderer leaves at alpha 0 shows Mica. The sidebar is therefore drawn with a low-alpha tint, and the content surface with higher alpha, exactly as in the prototype.
4. Custom title bar: handle `WM_NCCALCSIZE` (no system caption) and `WM_NCHITTEST` (drag area; return `HTMAXBUTTON` over the maximise button so **Snap Layouts** still appear).
5. Needs Windows 11 build 22621 or later for the backdrop attribute. **Decided: Windows 10 is supported.** There the window keeps the same layout and Fluent look (rounded corners drawn by us, same tints) over a **solid colour taken from the system theme**. No blur, no undocumented `SetWindowCompositionAttribute`, and no DWM blur cost. Acrylic on Windows 10 can be added later if wanted; it is known to lag while dragging and costs GPU.

### Three tiers

| Tier | What | How |
|---|---|---|
| **Self-draw on D3D11** | sidebar, buttons, switches, tabs, tables, chips, panels, why strip, **the patch bay**, traffic chart, scrolling, focus rings, animation | one renderer: rounded-rect SDF, strokes, bezier lines, clip, alpha; text through DirectWrite into a glyph atlas |
| **Keep native, hosted inside the window** | the **Configuration editor** (multi-line text, selection, undo, IME), the **credentials** password field | a child `EDIT`/RichEdit window placed over the canvas. This keeps IME, undo, spell and screen-reader behaviour for free, and it is where self-drawn text boxes usually go wrong |
| **Keep native, outside the window** | notification-area icon and its menu, file open dialog (`IFileOpenDialog`), UAC prompt for the helper, window placement and single-instance code | the existing C# code is the reference; port as is |

### Costs that come with self-drawing (none optional)
- **Text:** on a transparent surface there is no ClearType, so text is grayscale-antialiased. Fine at 150 % DPI and above; check at 100 %.
- **Accessibility:** UI Automation provider for every control. Use `accesskit` and `accesskit_windows` rather than writing it by hand. The existing `Plaitway.App.UiTests` (every control has a name) becomes the acceptance test.
- **DPI and scaling:** per-monitor v2, `WM_DPICHANGED`, text scale factor from `UISettings`.
- **System integration:** high contrast (switch to system colours), reduced motion (`SPI_GETCLIENTAREAANIMATION`), light/dark change at run time.
- **Robustness:** recover from `DXGI_ERROR_DEVICE_REMOVED`; render on demand (no 60 fps loop while idle), so the tray-resident app stays near zero GPU.
- **Keyboard:** tab order, arrow keys in lists and tabs, Enter and Space, access keys.

### Renderer decision: pure D3D11 (decided, lowest resource use)
Chosen because it is the lighter one at run time: no Direct2D device context or its caches, one small vertex buffer for all shapes, and text from a DirectWrite-rasterised glyph atlas (a few MB). The price is more code, almost all of it in text.
- **Render on demand.** Draw a frame only after input, an animation tick or new data; the swap chain is created at the window size, not larger. A tray-resident app then sits at about 0 % GPU.
- **Fallback:** if text layout (wrapping, trimming, bidi) turns out to be the schedule risk, switch the text path alone to Direct2D/DirectWrite on the same surface. The rest of the renderer does not change.

## 3. Rust layout and sizes

| Crate | Does | Replaces (C# lines today) |
|---|---|---|
| `plaitway-client` | gRPC client over the named pipe, generated from `proto/plaitway/v1/plaitway.proto` (`tonic`, `prost`, a named-pipe connector), the two watches | `Plaitway.Client` (≈2 800) |
| `plaitway-core` | app model, profile store, helper setup, editor model, navigation, strings | `Plaitway.AppCore` (≈6 300) |
| `plaitway-ui` | window, renderer, widgets, pages, tray | `Plaitway.App` (≈2 700) |

Strings come from the macOS catalog through a generator today (`Tools/Plaitway.Localization`); the Rust side needs the same step in `build.rs`.

## 4. Phases, each with a gate

| Phase | Deliverable | Gate |
|---|---|---|
| 0 Spike | one window: Mica, transparent swap chain, custom caption, Snap Layouts, DPI change, dark/light switch, one rounded rectangle and one line of text | Mica visible; resize without flicker; idle GPU near 0; works on 100 % and 150 % |
| 1 Renderer and bay | widget kit and the Routing page on fake data | matches the prototype screenshots; hover, select, reorder work; 60 fps while animating |
| 2 Client and core | `plaitway-client` and `plaitway-core`; the Routing page on the real daemon (`plaitwayd -fake`) | the client and model tests of the C# projects have Rust equivalents and pass |
| 3 Pages and native parts | profile tabs, Diagnostics, Settings, Setup, Empty; hosted editor, credentials, tray, file dialog | every page in the prototype exists; import, edit, connect, quit-to-tray work |
| 4 Accessibility and polish | UIA tree, high contrast, reduced motion, languages | the UI Automation tests pass against the Rust app |
| 5 Switch | Rust app replaces WinUI in the installer | parity checklist signed off; C# app removed |

### Phase 0 result (`windows/rust/plaitway-spike`)
Built and run on Windows 11 build 26300 at 125 % scale. The Windows 10 path was run with `PLAITWAY_SOLID=1` on the same machine, not on Windows 10.

| Gate | Result |
|---|---|
| Mica visible | yes: dark Mica is a neutral grey, the solid path is bluish; transparent pixels show the backdrop |
| Transparent composition swap chain, custom caption | yes; the system caption is gone, the borders and shadow stay |
| Caption buttons | hover (close turns red), maximise, restore, minimise and close all work by mouse |
| Maximised | no overhang past the screen; caption stays 32 px |
| Dark and light | both drawn (light pinned with `PLAITWAY_THEME=light`); the live switch on `WM_SETTINGCHANGE` is coded but was not triggered |
| Idle cost | 0 ms CPU over 5 s; 46 MB working set (debug build); release exe 181 KB |
| Snap Layouts | yes: hovering the maximise button for 2 s shows the layout popup (`design/verify-snap.png`) |
| Live light/dark switch | yes: flipping `AppsUseLightTheme` and broadcasting `ImmersiveColorSet` redraws the window in place, both directions (`design/verify-theme-light*.png`); the setting was restored |
| 100 % and 200 % scale | drawn correctly, text and caption scale together (`design/verify-scale-*.png`). Simulated by sending `WM_DPICHANGED` to the window, not by changing the display scale. At the simulated scales the system's own thin caption buttons show faintly beside ours (the real DPI stays 125 %); they are not visible at the real one. Cause not confirmed |
| Not checked | a real Windows 10 machine, a real DPI change between monitors, resize flicker by eye |

## 5. Not covered by the prototype
- Mica is simulated with a gradient; the real backdrop follows the wallpaper.
- No real drag-to-reorder, no keyboard handling, no localisation. All text is English.
- Numbers are sample data (Home, Office, Lab).

## 6. Decisions
1. Windows 10 (1809 or later) is supported: Mica on Windows 11, solid Fluent-style background on Windows 10.
2. Renderer: pure D3D11, drawn on demand.
3. Still open, default kept: the configuration editor and the password field are native child windows. This is also the cheapest option.

### Progress after phase 0 (`windows/rust`)
A Cargo workspace with `plaitway-ui` (window, D3D11 renderer, pages) and `plaitway-client` (gRPC over the named pipe).

| Phase | State | Evidence |
|---|---|---|
| 1 Renderer and bay | Routing page on fake data: lines, hover dimming, selection, drag to reorder, draw-in animation (off under reduced motion), compact layout under 1000 px, keyboard Up/Down/Home/End | 9 tests on the model; screenshots checked by sending messages to the window |
| 2 Client | generated from `proto/plaitway/v1/plaitway.proto` with a vendored `protoc`; a call, a failing call, a stream and a refused remote pipe, all over a real named pipe against an in-process server | 4 tests |
| 3 Pages | navigation, profile tabs (Overview, Routes, Logs, Configuration, Settings), Diagnostics, Settings with switches, on fake data | 4 tests on navigation; screenshots |
| Not done | the UI does not use the client yet (no `plaitwayd` here: Go is not installed); `plaitway-core` port of `AppCore`; hosted editor and password field, tray, file dialog, empty and setup pages, scrolling, text trimming, accessibility (UIA), high contrast, localisation, a real Windows 10 run | |
