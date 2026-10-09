# Plaitway for Linux

The Linux app: GTK 4 and libadwaita, Python 3 (PyGObject). It is the window and the tray
item of the `plaitwayd` helper and talks to it over gRPC on a Unix socket. The macOS app
(`macos/`) is the reference; this app does what it does, with the system's own parts in
place of Apple's. [docs/ui-architecture.md](docs/ui-architecture.md) describes the layers.

## Requirements

| What | For | Version |
|---|---|---|
| Python | everything | 3.10 or newer (tested on 3.14) |
| `python3-gi`, `gir1.2-gtk-4.0`, `gir1.2-adw-1` | the window | GTK 4.14, libadwaita 1.7 |
| `gir1.2-secret-1` | saved passwords | libsecret 0.20 |
| `python3-grpc`, `python3-protobuf` | the connection to the helper | the distribution's: written for grpcio 1.51 and protobuf 3.21, tested with 1.84 and 7.36 |
| systemd, polkit, a system D-Bus | starting and restarting the helper | |
| A Secret Service (GNOME Keyring, KWallet) | remembering credentials | |
| A StatusNotifierWatcher | the tray item; stock GNOME has none (an AppIndicator extension provides one) | |
| `plaitwayd.service` | the helper | the same version as the app |

Development adds `grpcio-tools` (to regenerate the API descriptor) and a Go toolchain (the
tests start the daemon). Nothing else is needed: the tests use `unittest`.

## Run from source against a fake daemon

The helper runs as root. The development daemon runs as you, with an in-memory backend that
needs no VPN server and changes nothing on the host:

```bash
go build -o /tmp/plaitwayd ./cmd/plaitwayd
SOCK_DIR="$(mktemp -d /tmp/pw-XXXXXX)"        # a socket path is limited to 107 bytes
/tmp/plaitwayd -fake -socket "$SOCK_DIR/d.sock" -state-dir "$SOCK_DIR/state" -run-dir "$SOCK_DIR/run" &
PLAITWAY_DEV=1 PLAITWAY_SOCKET="$SOCK_DIR/d.sock" linux/bin/plaitway-app
```

If the distribution's Python lacks grpcio or protobuf, use a virtual environment that can
still see PyGObject: `python3 -m venv --system-site-packages .venv && .venv/bin/pip install
grpcio protobuf`, and start the app with `.venv/bin/python linux/bin/plaitway-app`.

Profile text with `# fake: needs-credentials`, `# fake: fail`, `# fake: conflict` or
`# fake: reject` steers the fake backend (see `internal/manager/fake`).

| Variable | Effect |
|---|---|
| `PLAITWAY_SOCKET` | The socket to use. Followed only together with `PLAITWAY_DEV=1`. |
| `PLAITWAY_DEV` | `1` makes the app treat the daemon as a development daemon: it does not manage it, and keeps the passwords it is given in memory instead of the Secret Service. |
| `PLAITWAY_LANGUAGE` | `en` or `zh-Hant`, instead of the desktop's language. |
| `PLAITWAY_DEBUG` | Set it for debug logging. |

The app answers the credential requests of the daemon behind its socket from the keyring.
A normal run therefore never follows an environment variable to another socket: that would
hand every saved password to whatever listens there. The production socket
(`/run/plaitway/plaitwayd.sock`) is also checked before anything is sent to it: the socket
and its directory must be owned by root, the directory must not be writable by others, and
neither may be a symbolic link. Otherwise the app refuses it (`Helper not trusted`).

`plaitway-app --background` starts without a window when a tray shows the app (the
autostart entry of **Launch at Login** uses it); `plaitway-app file.ovpn …` imports the
files into the instance that runs, which is how a file manager opens them.

## Tests

```bash
PYTHONPATH=linux/src python3 -m unittest discover -s linux/tests
```

`PLAITWAY_DAEMON` names a prebuilt `plaitwayd`; without it the tests build
`./cmd/plaitwayd`. Each test class starts its own daemon on a private socket and stops it.
Tests that need what the machine lacks skip and say why: no display (the GTK and
application tests open real windows), no `dbus-daemon` (the tray tests use a private bus
with a StatusNotifierWatcher of their own), a daemon that refuses the user running the
tests, a daemon without real engines (the tests of the real profile parser), no
`grpcio-tools` (the check of the API descriptor; `tools/gen_descriptor.py --check` does it).

| Swift tests of `macos/Tests` | Here |
|---|---|
| ConfigTokenizerTests, ConfigDiagnosticTests, SecretMaskTests, ProfileImporterTests | `test_config_tokenizer`, `test_config_diagnostic`, `test_secret_mask`, `test_profile_importer` |
| ProfileStoreTests, CredentialFlowTests | `test_profile_store` |
| ProfileManagementTests, ProfileContentTests, LogsAndDiagnosticsTests | `test_daemon_calls`, `test_app_model` |
| AppModelTests, ProfileEditorTests, ModelTests | `test_app_model`, `test_profile_editor`, `test_models` |
| LocalizationTests | `test_localization` |
| DaemonInstallerTests | `test_app_model` (`HelperTests`) with a fake of systemd |
| (new) | `test_client` (production socket check, outages, cancellation), `test_descriptor`, `test_tray`, `test_gtk_smoke`, `test_application` |

## Install map

The package assembles from these paths:

| In the repository | Installed as |
|---|---|
| `linux/src/plaitway/` (including `client/descriptor.binpb`) | `/usr/lib/python3/dist-packages/plaitway/` |
| `linux/bin/plaitway-app` | `/usr/bin/plaitway-app` |
| `linux/data/share/` | `/usr/share/` |

`linux/data/share` holds:

- `applications/io.github.koukeneko.Plaitway.desktop`, which opens `.ovpn` files
  (`application/x-openvpn-profile`, known to shared-mime-info) and wg-quick files
- `metainfo/io.github.koukeneko.Plaitway.metainfo.xml`
- `mime/packages/io.github.koukeneko.Plaitway.xml`: `application/x-wireguard-profile`,
  recognised by `[Interface]` near the start of the file, with no `*.conf` glob (that would
  make every configuration file a WireGuard profile)
- `icons/hicolor/{16,32,48,64,128,256,512}x…/apps/io.github.koukeneko.Plaitway.png`
- `icons/hicolor/symbolic/apps/plaitway-state-*-symbolic.svg`: the seven shields of the
  profile states and of the tray item (idle, connecting, connected, attention, and
  reconnecting, disconnecting, credentials)

`linux/src/plaitway/version.py` holds the version of the app; the package stamps the
release version there. A helper of another version is shown as `Helper out of date`.

After installing, the package refreshes the caches: `update-mime-database /usr/share/mime`,
`gtk-update-icon-cache -t /usr/share/icons/hicolor`, `update-desktop-database`.

Checks: `desktop-file-validate` and `appstreamcli validate --no-net` pass on the two files.
The metainfo carries no screenshots, which need a URL.

## Regenerating

| What | Command | After |
|---|---|---|
| The API descriptor `client/descriptor.binpb` | `python tools/gen_descriptor.py` | changing `proto/` |
| The string accessors `l10n/strings.py` | `python tools/localize.py` | changing a catalog or `Resources/strings-manifest.json` |
| The PNG icons | `python tools/make_icons.py` | changing `Docs/app-icon.png` |
| Pictures of every page | `python tools/capture_ui.py --language zh-Hant --out /tmp/shots` | a change of the UI |

The generated files are committed. No generated protobuf module is: `*_pb2.py` only loads
with the protobuf version that made it, and the app runs on the distribution's. The client
builds its classes at run time from the committed `FileDescriptorSet`, and a test fails
when `proto/plaitway/v1/plaitway.proto` and the descriptor disagree.

## Strings

English and Traditional Chinese (Taiwan terms), following `PLAITWAY_LANGUAGE`, then
`LANGUAGE`, `LC_ALL`, `LC_MESSAGES` and `LANG`. The source of truth is the macOS catalog
(`macos/Sources/PlaitwayMenuBar/Resources/Localizable.xcstrings`) for strings that have the
same role on Linux, and `Resources/Linux.xcstrings` for the ones only Linux has.
`Resources/strings-manifest.json` lists the macOS strings Linux does not use and names the
accessors of strings with placeholders. UI code has no string literals: it calls
`strings.cancel`, `strings.connected_count(n)`. A test fails for a string no code uses, a
string the code uses and no catalog has, a literal text given to a control, a missing or
mismatched translation, a Mainland term, and a generated module that is out of date.

## What differs from the macOS app

| macOS | Linux | Why |
|---|---|---|
| Menu bar item | StatusNotifierItem with a `com.canonical.dbusmenu` menu, rows as in the macOS menu | The system's tray protocol. Stock GNOME has no tray host; then there is no tray, and closing the window quits the app (tunnels stay up, the helper keeps them). With a tray, closing hides the window. |
| Install Helper, approval in System Settings, Reinstall, Uninstall | `Start Helper` and `Restart Helper` through `org.freedesktop.systemd1.Manager` with interactive authorization (polkit asks); a screen for each state: not installed, not running, running and not answering, refused (permission denied), not trusted | The helper is a package's systemd unit; the app does not install it. |
| Show in Menu Bar | Show Tray Icon | There is no menu bar. |
| Keychain | Secret Service through libsecret, schema `io.github.koukeneko.plaitway.credentials`, attributes `profile_id` and `kind`, the same JSON value. A credential that cannot be saved is reported (`Credentials not saved`). | macOS only logs it. |
| Launch at Login | An autostart entry that starts `plaitway-app --background` | |
| Debug builds follow `PLAITWAY_SOCKET` | Any run follows it, but only with `PLAITWAY_DEV=1` too | One binary, no debug build. |
| Window frame remembered | Size and maximized state in `$XDG_STATE_HOME/plaitway/state.json` | GTK 4 cannot place a window. |
| Page switcher and + in the toolbar | The page switcher in the header (a bar of pages at the bottom of a narrow window); + and the main menu in the sidebar header | GNOME conventions; the pane collapses below 640 sp. |
| ⌘F finds in the editor too | Ctrl+F finds in the logs | |
| A restarted log stream replaces the lines shown | The client does not deliver again the lines it delivered | |
| Quit asks even at power off | SIGTERM and SIGINT quit at once | |

## Not done

- A scalable SVG of the app icon (the PNG set is made from `Docs/app-icon.png`).
- Screenshots in the metainfo.
