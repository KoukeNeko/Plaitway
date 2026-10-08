# UI toolkit

Decision: **WinUI 3 (Windows App SDK 2.5), unpackaged**, in `windows/Sources/Plaitway.App`. WPF was not needed as a
fallback: WinUI 3 builds with `dotnet build`, runs, and passed every check below on this machine. A minimal WPF
window was built next to it for the numbers in the comparison (not committed).

Machine: Windows 11 Pro 10.0.26300, x64, .NET SDK 10.0.401, no Visual Studio workload used, not elevated. Monitor at
150 % (144 DPI). System language zh-TW, apps in dark mode, Windows mode light.

## Spike

`Plaitway.App` opens a window that shows the daemon's version and its profiles (read through `Plaitway.Client` from
`plaitwayd -fake`), creates a notification-area icon with a menu, hides to the tray when closed, puts the icon back on
`TaskbarCreated`, and exits from the menu. `scripts/run-spike.ps1` ran it end to end (the script is gone: `capture-ui.ps1` and `Tests/Plaitway.App.UiTests` do its work for the app that replaced the spike). Result of the last run:

| Check | Result |
|---|---|
| Build with `dotnet build`, 0 warnings | yes; clean build of the solution (restore, protoc, XAML) 8.5 s with a warm package cache |
| Window with the daemon's version | `0.0.0-dev (protocol 1)`, read from the daemon over the pipe |
| Screenshot, read back | [dark, zh-TW](images/spike-window-dark-zh-TW.png), [light, en-US](images/spike-window-light-en-US.png), [tray menu](images/spike-tray-menu.png) (`PrintWindow` with `PW_RENDERFULLCONTENT`; it does not draw the Mica backdrop, which the compositor adds) |
| Process alive for 10 s, no crash | yes; 0 error events about the app in the Application log |
| Memory after 10 s | 162 MB working set, 103 MB private, 68 threads, 1259 handles (Debug build, connected, three profiles) |
| Tray icon accepted by the shell | `Shell_NotifyIcon(NIM_ADD)` returned true; the shell created an entry under `HKCU\Control Panel\NotifyIconSettings` for the exe |
| Explorer restart | the icon was removed from outside the process, then `TaskbarCreated` was posted to the app's window: `NIM_ADD` again, true (`tray_adds 1,True ; 2,True`). A real restart of Explorer was not done, because it closes the user's shell |
| Hide to tray | closing the window hides it and the process stays; a click on the icon (`NIN_SELECT`) shows it again |
| Exit | the menu entry, chosen with the keyboard, ends the process with exit code 0 after the tray window, the client and the watch are disposed |

## Measurements

| | WinUI 3 framework-dependent | WinUI 3 self-contained | WPF framework-dependent | WPF self-contained |
|---|---|---|---|---|
| Publish size | 37.7 MB, 44 files | 170.2 MB, 461 files | 0.2 MB, 5 files | 139.6 MB, 400 files |
| Needs on the target machine | .NET 10 runtime, Windows App Runtime 2.5 | nothing | .NET 10 Desktop runtime | nothing |
| First window, Release publish | 405 to 424 ms (activated) | 409 to 465 ms | about 1000 ms (content rendered) | about 950 ms |
| Working set, idle, 8 s | 137 MB | 137 MB | 164 MB | 145 MB |
| Private bytes | 92 MB | 93 MB | 136 MB | 120 MB |

The WinUI numbers are from the real app (gRPC client, three event sources, no daemon: the Release build ignores
`PLAITWAY_SOCKET`). The WPF numbers are from a window with a list and a text box. First window is the time from the
process's start time to the first activation (WinUI) or content rendering (WPF): different events on different
content, so the columns are indicative only and do not rank the two toolkits. Clean build of the WinUI app alone:
9 to 17 s.

The framework-dependent WinUI folder is large because of `Microsoft.Windows.SDK.NET.dll` (23.7 MB, the .NET projection
of the whole Windows SDK) and `Microsoft.WinUI.dll` (7 MB). Referencing the Windows App SDK components the app needs
(Base, Foundation, InteractiveExperiences, WinUI, Runtime) instead of the `Microsoft.WindowsAppSDK` metapackage
removed `onnxruntime.dll`, `DirectML.dll` and the machine learning projections: 78.4 MB to 37.7 MB framework-
dependent, 227.8 MB to 170.2 MB self-contained. `Microsoft.Web.WebView2.Core.dll` (the projection, 0.8 MB) is still
copied as a dependency of WinUI; the app has no WebView and loads none.

## Packages

| Package | Version | Used by |
|---|---|---|
| Microsoft.WindowsAppSDK.Base | 2.0.4 | App |
| Microsoft.WindowsAppSDK.Foundation | 2.3.12 | App |
| Microsoft.WindowsAppSDK.InteractiveExperiences | 2.1.9 | App |
| Microsoft.WindowsAppSDK.WinUI | 2.3.9 | App |
| Microsoft.WindowsAppSDK.Runtime | 2.5.1 | App (the runtime of the self-contained build) |
| Microsoft.Windows.SDK.BuildTools | 10.0.26100.4654 | App (transitive) |
| Microsoft.Windows.SDK.BuildTools.MSIX | 1.7.251221100 | App (transitive) |
| Microsoft.Web.WebView2 | 1.0.3719.77 | App (transitive, not used) |
| Google.Protobuf | 3.36.2 | Client |
| Grpc.Net.Client | 2.84.0 (Grpc.Net.Common, Grpc.Core.Api 2.84.0) | Client |
| Grpc.Tools | 2.84.0 | Client (build only) |
| Microsoft.NET.Test.Sdk | 18.10.1 | Tests |
| xunit.v3 | 4.0.1 | Tests |
| xunit.runner.visualstudio | 3.1.5 | Tests |

Windows App Runtime packages installed on this machine: 1.6, 1.7, 1.8 and 2.2 to 2.5.1.

## Decision record

### Unpackaged, framework-dependent or self-contained

Unpackaged (`WindowsPackageType=None`): the app starts like any exe, needs no MSIX signing, identity or sideloading
setting, and `dotnet build` produces something that runs. The cost is that the Windows App Runtime is not
registered by the app: a framework-dependent build needs it installed (the bootstrapper finds an installed 2.x), and
the self-contained build carries it (+132 MB). Recommended: self-contained in the installer, framework-dependent for
development. The installer is the place to decide; the service installer will need to be shipped anyway.

Without package identity: no `AppInstance` redirection by key from the shell, no toast activation by package, no
startup task from the manifest (a registry Run key or the Startup folder instead), no MSIX auto-update.

### Notification-area icon

`Shell_NotifyIcon` through P/Invoke (`Tray/`, about 400 lines with the declarations), not a NuGet package. H.NotifyIcon 2.4.1 is maintained and
works with WinUI 3, but it brings a window for XAML flyouts and its own handling of `TaskbarCreated`, and what the
app needs is small. The menu is a native menu (`TrackPopupMenuEx`): the system draws it, screen readers and the
keyboard work without extra code, it opens at the click, and rows of profiles with a check mark or a bitmap fit it.
Findings:

- The window that receives the shell's messages has to be a hidden top-level window. A message-only window gets no
  broadcast, and `TaskbarCreated` is one.
- A native menu is light unless the process opts in to dark mode. Windows has no documented call for it; the two
  uxtheme ordinals 135 (`SetPreferredAppMode`) and 136 (`FlushMenuThemes`) are used and fall back to a light menu when
  missing. With the system in dark mode the menu was dark (see the screenshot). If richer rows are needed (a coloured
  state glyph per profile), the next step is a XAML flyout in a small popup window, which costs a window and the
  focus handling.
- `NIM_ADD` for an icon that exists fails. The handler counts adds and reports the result.

### Accessibility (UI Automation)

The tree of the running window (`automation-tree*.txt` of the script, read through `System.Windows.Automation`)
shows real controls: a Window with `WindowPattern`, a List with `SelectionPattern` and `ItemContainerPattern`,
ListItems with `SelectionItemPattern`, an Edit with `ValuePattern` and `TextPattern`, labelled by its header. Items take
their name from `ToString()` of the row (`Office, OpenVPN, Connected`); the status is a Group named by its text, so the
state is announced in words, not by colour. The list had no name until `AutomationProperties.Name` was set. The status
glyph is hidden from the tree. Not tested with Narrator.

Keyboard: `ListView` with `CanReorderItems` reorders by dragging. Reordering from the keyboard was not tried
(Alt+Shift changes the keyboard layout on this machine), and the app should offer Move up and Move down commands
regardless: a drag has no keyboard equivalent that can be relied on.

### High DPI

`PerMonitorV2` in the manifest. The window reported 144 DPI (150 %) and drew sharp at that scale. WinUI redraws and
rescales when the window moves between monitors with different scales (this machine has five monitors; a move
between them was not tried).

### Dark mode and theme

The content follows the system's app mode (dark here), and a theme can be forced (`--theme light`), which gave the
light screenshot. Mica comes from `MicaBackdrop`. Gap: the native title bar follows the Windows mode, not the app
mode, so a dark app wore a light title bar; `DwmSetWindowAttribute(DWMWA_USE_IMMERSIVE_DARK_MODE)` from
`ActualTheme` fixes it (`Win32Window.UseDarkTitleBar`). The tray menu follows the system, not the forced theme.
Status colours are theme resources (`SystemFillColorSuccessBrush` and the others) in a visual-state control, so
they follow the theme; every state also has an icon and a word.

### Text input and IME for Traditional Chinese

`測試中文 Plaitway` was sent as Unicode key events (`SendInput` with `KEYEVENTF_UNICODE`) into the focused
`TextBox` and read back through UI Automation unchanged. The strings render in zh-TW (the `.resw` files are
generated from `Localizable.xcstrings`, so labels match the macOS app) with the system's CJK font fallback.
IME composition (Microsoft New Phonetic is installed here) was not automated: it needs the input method of the
app's thread switched, which, with the system's "same input method for all windows" setting, changes it for the user.
`TextBox` uses the Text Services Framework itself, so composition and the candidate window are the system's;
manual check: Win+Space to New Phonetic, type `su3cl3` in the Name box, see the composition underline and the
candidate window at the caret.

### List reorder

Drag a profile below the last one with real mouse input (`SendInput`, 25 moves): the list changed on the first
attempt, `ReorderProfiles` reached the daemon and `plaitway list` showed the new order (Office last). The ordering
the daemon reports back (one profile at a time) is applied without flicker by `ProfileSet` and `MainViewModel.Show`.

### Startup cost

First window about 0.4 s for the WinUI app (to activation) and about 1.0 s for the WPF window (to content rendered), in
both framework-dependent and self-contained forms. These are not the same event on the same content, so they are
indicative only. Process start to a running window includes the .NET runtime, the Windows App Runtime
bootstrapper and, for WinUI, the XAML load. No ReadyToRun, trimming or NativeAOT was tried; WPF cannot be trimmed,
WinUI trimming is limited.

### Workarounds that were needed

| Problem | Workaround |
|---|---|
| `dotnet publish` leaves the `.pri` files out of an unpackaged WinUI app, and it dies at the first resource or XAML lookup (`0x80070490`) | `CopyPriFilesToPublishDirectory` in `Plaitway.App.csproj` copies the build's `.pri` files after publish; adding them to `ResolvedFileToPublish` fails with NETSDK1152 |
| `GenerateDocumentationFile` on the app made the XAML compiler fail with `WMC9999` after CS1591 | documentation is generated only for the client library |
| Light native title bar on a dark app | `Win32Window.UseDarkTitleBar` |
| Native menus stay light | `MenuTheme.FollowSystem` (undocumented ordinals) |
| `TaskbarCreated` never arrived at a message-only window | hidden top-level window |
| xUnit v3 on the .NET 10 SDK refuses the VSTest target | `global.json` selects Microsoft.Testing.Platform; `dotnet test --project` |
| The output path gained `x64\` when built from the solution | `AppendPlatformToOutputPath=false` |

### WinUI 3 or WPF

| | WinUI 3 | WPF |
|---|---|---|
| Look | Fluent, Mica, Windows 11 controls, matches Settings | .NET 10 has a Fluent theme (`ThemeMode`), experimental, controls are older |
| Dark mode | follows the system; title bar needs a call | `ThemeMode.System` |
| DPI | per-monitor v2 native | per-monitor v2 supported, fonts and bitmaps need care |
| Accessibility | UI Automation peers for all controls; list, edit, groups as above | UI Automation, mature |
| Drag reorder | `ListView.CanReorderItems`, worked | not built in; needs code |
| Tray | P/Invoke in both | P/Invoke or WinForms `NotifyIcon` in both |
| Packaging unpackaged | works; runtime install or +132 MB; one publish workaround | framework-dependent is 0.2 MB, self-contained 140 MB |
| Startup, memory | 0.4 s, 137 MB (indicative, see Startup cost) | 1.0 s, 145 to 164 MB (indicative) |
| Risk | younger, fewer answers, version churn (1.8 to 2.5 within the machine's installed runtimes), `.pri` and publish quirks | stable, in maintenance, no new controls |

WinUI 3 gives the native Windows 11 appearance the product asks for and a drag-reorder list without custom code. The
decision does not rest on startup time or memory, which were not measured comparably. WPF remains the fallback if the Windows App Runtime distribution
becomes a problem.

## Not verified

- IME composition, Narrator, moving the window between monitors of different scale, a real Explorer restart, the
  light menu with a light system.
- Windows 10 and ARM64 (the project builds for `win-x64`; `TargetPlatformMinVersion` is 10.0.17763.0).
- Self-contained and framework-dependent builds were run from `dotnet publish` output only on this machine.
