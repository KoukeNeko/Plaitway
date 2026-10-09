# Architecture of the Linux app

Three layers, each importing only the one below it.

```
plaitway.app     GTK 4 and libadwaita. Wiring: a view binds a widget to a model, and the
                 adapters of the ports (GLib, libsecret, systemd, Gio D-Bus).
plaitway.core    The models and what they present. No GTK. Runs under unittest.
plaitway.client  The daemon connection and the pure logic that belongs to the API. No GTK.
plaitway.l10n    The strings (generated accessors) and the language. Used by core and app.
```

`client` and `core` import nothing from GTK, so everything except the widgets is tested
with the real daemon on a private socket and no display.

## client

| Module | What it is |
|---|---|
| `proto`, `types`, `descriptor.binpb` | Message classes built at run time from a committed `FileDescriptorSet`; enums as `IntEnum` without their prefix (`ProfileState.AWAITING_CREDENTIALS`); an unknown number reads as `UNSPECIFIED`. |
| `daemon_client` | One blocking method per RPC, raising `DaemonFailure`; `watch_profiles` and `watch_logs`. |
| `watch` | A watch is a thread. It reports an outage once (again when the reason changes), retries with `BackoffPolicy` (200 ms × 1.6 up to 5 s, 20 % jitter), starts again with a snapshot, and does not deliver again the log lines it delivered. `cancel()` cancels the gRPC call, which ends it at the daemon. |
| `errors` | `DaemonFailure` with a `kind`: permission denied, unavailable, not found, rejected (input refused; `message` is the daemon's reason), server refused (this app refused the daemon), other. |
| `location` | The production socket, `PLAITWAY_SOCKET` (followed only with `PLAITWAY_DEV=1`), and `verify_socket_owner`: lstat of the socket and its directory, owned by root, directory not writable by others, no symbolic links. The client runs it before every call and every restart of a watch. |
| `secret_mask`, `config_tokenizer`, `config_text`, `config_diagnostic` | The editing logic: secrets as `‹secret n›` placeholders, tokens for highlighting, the daemon's rejection read as a line and a reason. Ranges are offsets in code points, which is what a `GtkTextBuffer` counts. |
| `profile_importer` | Reads a profile file. Certificates and keys named by file are read from the profile's own folder and put inline; any other path is refused; `auth-user-pass file` becomes a bare directive and its credentials are returned. |
| `profile_set` | Priority order and how a drag changes it. |

## core

- `ProfileStore` holds the daemon's profiles (`ProfileSet`), the connection state, the
  daemon's description and the credential prompts. It follows the watch, answers a
  credential request from the credential store before it asks, and keeps the credential
  store in step with profiles that are deleted.
- `AppModel` holds the rest of what the window shows: the selection, the page of a
  profile and of Diagnostics, the helper's registration, and the commands. `DaemonSetup`
  (`core.setup`) says what the window is while the helper is not ready.
- `ProfileEditor`, `LogTail`, `DiagnosticsModel`, `TrafficHistory` each own one page's
  state. `presentation`, `formatting`, `tray_model`, `aggregate` turn states into words,
  glyph names, tones and rows.
- Ports (`core.ports`): `UiScheduler`, `Clipboard`, `FilePicker`, `HelperService`,
  `CredentialStore`, `LoginItem`, `QuitPrompts`, `Dialogs`, `Localizer`. `core.fakes` and
  `InMemoryCredentialStore` are the doubles; `l10n.localizer` has both Localizers.

### Threads

```
watch threads ──► ProfileStore / LogTail      state under a lock, any thread
                       │ Notifier.notify()    coalesced; posts one call to the scheduler
                       ▼
UI thread  ◄── UiScheduler.post               GLib.idle_add in the app, a queue in tests
   view.update() reads the models
   view ─► AppModel.command() ─► TaskRunner (worker thread) ─► blocking store / client call
                                   └─► done(Outcome) posted back to the UI thread
```

- **Blocking** (any thread, raise): `DaemonClient`, `ProfileStore` commands, `CredentialStore`,
  `HelperService`. A view never calls them on the UI thread.
- **Not blocking**: `AppModel`, `ProfileEditor` and the other models start the work on a
  `TaskRunner` and report on the UI thread: through `done` callbacks, an alert (`Dialogs`),
  or a change that `Notifier` announces.
- A `Notifier` is the only way a model tells a view it changed. A burst of changes is one
  call; the listener reads the model again. `follow(widget, notifiers, update)` connects
  while the widget is on screen and calls `update` once when it appears.

## app

| Module | |
|---|---|
| `application` | One instance per user (the application id), actions and accelerators, the tray, quitting. Closing the window hides it while a tray shows the app, and quits otherwise. |
| `window` | `AdwNavigationSplitView` (collapses below 640 sp through a breakpoint), the helper setup page, the content stack of pages, the page switcher (toggle group in the header, `AdwViewSwitcherBar` below when narrow), the banner for the helper, the action bar, drag and drop of files, the window actions. |
| `sidebar` | The profile list (context menu, Delete key, drag to reorder) and the footer rows. |
| `pages/` | `overview`, `routes`, `logs`, `configuration` (with `config_editor`), `profile_settings`, `diagnostics`, `app_settings`, `setup`. A profile's five pages are one `AdwViewStack`, built when the profile is first selected. |
| `dialogs`, `platform`, `secret_service`, `systemd`, `login_item` | The adapters of the ports. |
| `tray/` | `status_notifier` (`org.kde.StatusNotifierItem`), `dbusmenu` (`com.canonical.dbusmenu`), `controller` (watches `org.kde.StatusNotifierWatcher`, keeps the item in step with the models). |
| `widgets`, `style`, `theme` | `StateGlyph`, `Sparkline`, `PageSwitcher`, `NoticeBar`, `name_widget`; the CSS; colours for text tags that follow light, dark and high contrast. |

## Conventions

- A view has no logic. It builds widgets, binds them to a model in one `update()` that
  reads everything it shows, and forwards what a person does to the model.
- Words come from `Strings` accessors only. A literal text given to a control fails
  `test_localization`. Product names (`OpenVPN`, `WireGuard`, `Plaitway`) are not strings.
- A state is a glyph, a word and a tone, never a colour alone: `presentation` maps a state
  to a symbolic icon name, a label and a `Tone`; `style.set_tone` maps a tone to a style
  class of libadwaita (`success`, `warning`, `error`, `dimmed`).
- Every control that has no text of its own is named with `name_widget` (an accessible
  label), and an icon-only button also gets a tooltip (`icon_button`). `test_gtk_smoke`
  fails for a control that has no name.
- Use libadwaita's widgets and style classes first; `style.CSS` is for what they lack.
  `AdwViewStack`, `AdwStatusPage` and most `Adw*` widgets are final and cannot be
  subclassed: compose them.
- Dialogs are `AdwAlertDialog` or `AdwDialog` through the `Dialogs` port, never made by a
  view or a model directly.
- Light, dark and high contrast follow `AdwStyleManager`. Colours that a text tag needs
  are in `theme.Palette` (light, dark, and a stronger pair for high contrast).
- Nothing blocks the UI thread: no `time.sleep`, no daemon call, no libsecret call.

## Adding a page

1. State that is not a profile's goes in a model in `core` with a `Notifier`, started and
   stopped by the view when it is mapped. Its commands run on the `TaskRunner`.
2. Words go into `linux/Resources/Linux.xcstrings` (both languages) or are taken from the
   macOS catalog; run `tools/localize.py`. A long or formatted string needs a name in
   `Resources/strings-manifest.json`.
3. The view is a `Gtk.Box` or an `Adw.PreferencesPage` in `app/pages/` whose `update()` is
   registered with `follow`. Add it to `build_profile_pages` (or the stack it belongs to),
   with an icon in `pages/icons.py` for the narrow bar, and a `ProfileSection` member in
   `core/app_model.py` whose `label` is a `Strings` accessor.
4. Add its keyboard shortcut in `application.ACCELERATORS` if it has a number.
5. Cover the model in `core` tests against the daemon, and build the page in
   `test_gtk_smoke` (the existing tests walk every page of every profile, so a page that is
   in the stack is already checked for warnings and unnamed controls).
6. Run `tools/capture_ui.py` in both languages and look at the pictures.

## The helper

`DaemonSetup` is resolved from the connection (`ProfileStore.connection` and the reason it
is unavailable), what systemd says about `plaitwayd.service` (`HelperRegistration`, asked
with `LoadUnit` and the unit's properties), the versions, and whether the app was just
told to start the helper (`is_settling`, eight seconds).

| State | Shown | Offers |
|---|---|---|
| ready, version mismatch | the profiles; a banner when the versions differ | Restart Helper |
| connecting | a spinner | |
| not installed | `Helper not installed` | Retry |
| not running | `Helper not running` | Start Helper |
| not responding | `Helper unavailable`, where its log is (`journalctl -u plaitwayd.service`) | Retry, Restart Helper |
| permission denied | `Permission denied` | Retry |
| server refused | `Helper not trusted` and why | Retry |
| development | `Helper unavailable` and how to start the development daemon | Retry |

Starting and restarting call `StartUnit` and `RestartUnit` on the system bus with
`ALLOW_INTERACTIVE_AUTHORIZATION`, so polkit asks the person. The app runs no command and
needs no sudo. Profiles stay in the window while the helper stops answering (a restart
would otherwise flash a setup page).
