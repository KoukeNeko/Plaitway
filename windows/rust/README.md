# Plaitway for Windows, in Rust

A rebuild of the WinUI app: drawn by us on Direct3D 11, with Mica on Windows 11 and a solid colour on Windows 10.
The design is in `design/ui-plan.md` and `design/a-full` (a clickable prototype).

| Crate | What it is |
|---|---|
| `plaitway-client` | gRPC client for `plaitwayd` over its named pipe. Opens the pipe with the access the daemon grants, and checks who owns it before sending anything |
| `plaitway-core` | What the UI knows: the model of profiles and routes, the session that keeps it current (and runs commands), the settings store, the translations |
| `plaitway-helper` | The helper service: its state, the rule for running its program elevated, and the commands `install`, `start`, `install -update` and `uninstall` |
| `plaitway-ui` | The window: renderer, pages, text boxes, tray, screen reader support |
| `plaitway-fake` | A daemon that follows the proto, for development and tests, and `pwctl` to look at a daemon from a script |

```powershell
cargo build --release --workspace
target\release\fake-daemon.exe \\.\pipe\plaitway-demo          # a daemon with three profiles
$env:PLAITWAY_PIPE = '\\.\pipe\plaitway-demo'; target\release\plaitway-ui.exe
cargo test --workspace
```

`PLAITWAY_PIPE` names the daemon; without it the app uses `\\.\pipe\plaitway`, the real helper.

## Tests

- `cargo test --workspace`: the model, the layout and hit testing, the session against the fake daemon over a
  real named pipe, the trust rules against real access lists and real signatures, translations.
- `PLAITWAY_REAL_PIPE=\\.\pipe\plaitway cargo test -p plaitway-core --test real_daemon`: reads the real
  helper. It changes nothing.
- `PLAITWAY_FAKE_PIPE=<pipe of a plaitwayd -fake> cargo test -p plaitway-core --test real_fake_daemon`: the
  real gRPC server and reconciler with in-memory tunnels. It deletes every profile of that daemon.
- `PLAITWAY_TEST_ELEVATE=1 cargo test -p plaitway-helper`: also runs a real program elevated.
- `scripts\`: drive the real window; see `scripts\README.md`.

## What is not here

- **The installer is built, not installed.** `packaging\windows\build-installer.ps1 -RustApp` makes the MSI around
  `app\Plaitway.exe` with the notices of its crates, and `Test-Installer.ps1 -RustApp` checks it (administrative unpack only).
  Installing it and upgrading over a WinUI install have not been tried.
- **Windows 10.** The solid-colour path is drawn when `PLAITWAY_SOLID=1`, on Windows 11. It has not run on Windows 10.
- **Real DPI changes.** 100% and 200% were checked; changing the display scale while running, and moving the window between
  monitors of different scale, were not (no second monitor).
- **A human screen reader.** The UI Automation tree is checked by script (6 checks); nobody has listened to Narrator or NVDA on it.
- **More languages.** The shared catalogs have one translation, Traditional Chinese.
- **A profile from wgcf.** A WARP-shaped WireGuard profile written by hand was checked against the real daemon; a profile
  made by `wgcf register` and `wgcf generate` was not.
- **The WinUI app is still in the repository.** Whether to delete it is not decided.
