# Plaitway

A VPN manager that runs several OpenVPN and WireGuard profiles at the same time.
It lives in the macOS menu bar, with a management window for profiles, routes, DNS,
logs and diagnostics, and a command line tool. One Reconciler owns the host's routes
and DNS, so profiles do not fight over them and a network change leaves no stale route behind.

Windows and Linux (Wails v3 UIs on the same daemon) are planned. This repository
ships the macOS product (Apple silicon, macOS 15 or later).

## How it works

- **App** (`macos/`): a native Swift menu bar app and an unprivileged client of the daemon.
- **Daemon** (`cmd/plaitwayd`): a root LaunchDaemon. It stores profiles, runs one engine per
  enabled profile and serves the API on a Unix socket only, never TCP.
- **Engines**: OpenVPN runs the bundled official binary under the daemon, controlled through its
  management interface (`internal/ovpn`). WireGuard runs embedded wireguard-go (`internal/wg`).
  Both retry transient failures themselves (no network yet, name does not resolve, server
  unreachable) and say why a connection is stuck.
- **Reconciler** (`internal/reconciler`): the only code that changes routes and DNS. Engines announce what
  they want; it computes one result (priorities, shadowing, local subnet conflicts), writes it
  through the macOS adapters (`internal/osnet/macos`: PF_ROUTE, scutil) and journals everything it adds,
  so a crash or a network change is repaired instead of leaked.
- **CLI** (`cmd/plaitway`): status, list, connect, disconnect, import, show, edit, set, remove, logs, diagnostics, resync, watch.
  `show` prints a profile's text with its secrets hidden (`-secrets` shows them), `edit` changes it in `$VISUAL` or `$EDITOR`,
  `set` changes the name and the settings.
- **Contract**: `proto/plaitway/v1/plaitway.proto` is the only hand-written API definition;
  Go and Swift code are generated from it.

## Requirements

Go 1.27.1, Xcode 27 (Swift 6.4), macOS 15 or later (grpc-swift 2.x minimum), Apple silicon.

## Build

```sh
make test      # Go tests, then the Swift tests against a fake daemon
make app       # build/Plaitway.app (builds the bundled OpenVPN first), signed with PLAITWAY_SIGN_IDENTITY
make dist      # build/Plaitway-<version>.zip and .dmg; the version is in VERSION
make verify    # scripts/verify-bundle.sh: structure, plists, linkage, signatures, a start of the packaged daemon
make generate  # only after changing proto/
```

[packaging/README.md](packaging/README.md) describes the bundle, the signing order, install, upgrade,
uninstall and the licenses. To notarize, run `make app` and then `packaging/notarize.sh` (notarytool keychain profile `plaitway-notary`); it staples
the tickets and rebuilds the zip and dmg. Build 0.1.0 was notarized this way on 2026-10-07 and Gatekeeper accepts it.

## Install

Copy `Plaitway.app` to `/Applications` and open it. The first run offers to install the helper: macOS asks
for approval in System Settings, Login Items, and the app continues from there. If SMAppService does not accept
the helper, `sudo scripts/dev-install-daemon.sh` installs it as a LaunchDaemon and
`sudo scripts/dev-uninstall-daemon.sh` removes it. The command line tool is in
`Plaitway.app/Contents/Resources/bin/plaitway`; it is not installed anywhere, link it yourself.

Profiles (`.ovpn`, wg-quick `.conf`) are imported from the window, by dropping the file on it, with
`plaitway import`, or by opening the file with Plaitway. Both kinds can be edited afterwards, in the window (Configuration)
or with `plaitway edit`: the editor shows private keys and inline key blocks as placeholders until asked to show them, marks the line
the daemon refuses, and a running profile keeps its old text until it is restarted (Save and Reconnect). Per profile the window also
sets auto-connect, the tunnel mode, on-demand activation by network type (Ethernet, Wi-Fi) and, for WireGuard, leaving the private
address ranges out of AllowedIPs; a WireGuard profile shows its public key. Profile files are untrusted: the app inlines
certificates and keys that lie inside the profile's own folder and refuses any other file, and the daemon
rejects directives that read files or run programs. Usernames and passwords go to the login Keychain.

Files: profiles and the route journal in `/Library/Application Support/Plaitway`, the daemon log in
`/Library/Logs/Plaitway` (readable by root only), the socket in `/var/run/plaitway`. Quitting the app does not
disconnect tunnels; the helper keeps them.

## The window and the menu bar

The management window is a sidebar of profiles and Diagnostics, and per profile five pages (Overview, Routes and DNS, Logs,
Configuration, Settings; also View, ⌘1 to ⌘5). Overview shows the state and its cause, the uptime, the rates and a two minute graph
(worked out in the app from the byte counters, the daemon keeps no history), and what is not in effect. Every state has a glyph of its
own, a word and a colour. Settings (⌘,) holds Launch at Login, whether the menu bar item is shown, and the helper.

The menu bar item is a menu, not a popover: the HIG asks for a menu unless the content is too complex for one, macOS 27 changes how
custom status item windows behave and hides the images of menu items, and Apple's own VPN menu is a menu. It shows how many
profiles are connected, one row per profile with its state (choose a row to connect, disconnect, retry or answer a password prompt),
Disconnect All, and the window commands. The Dock menu lists the same profiles while a window is open.

The app is linked against the macOS 27 SDK (`-platform_version macos 15.0 27.0` in `macos/Package.swift`): SwiftPM stamps the
deployment target as the SDK version, and macOS draws such an app in the pre-Tahoe design. It still runs on macOS 15.

## Develop

The daemon runs without root on an in-memory backend, which is a complete stand-in for UI work:

```sh
go build -o bin/plaitwayd ./cmd/plaitwayd
SOCK="$(getconf DARWIN_USER_TEMP_DIR)plaitway.sock"       # macOS limits socket paths to 104 bytes
./bin/plaitwayd -fake -socket "$SOCK" -state-dir /tmp/plaitway-dev &
PLAITWAY_SOCKET="$SOCK" swift run --package-path macos Plaitway     # the override works in debug builds only
go run ./cmd/plaitway -socket "$SOCK" status
```

Profile text with `# fake: needs-credentials`, `# fake: fail`, `# fake: conflict` or `# fake: reject`
steers the fake backend. The fake daemon has a Wi-Fi primary network that never changes, and a stand-in public key for WireGuard profiles. Without `-fake` the daemon uses the real engines; without root it can
read the network and import profiles, but creating a tunnel fails with a permission error.

## Security model

- The socket is mode 0666 inside a root-owned directory; every call is authorized from the caller's
  uid and groups: connect and read for the console user, administrators and root; importing, editing and
  deleting profiles, reading a profile's text (it holds the keys) and removing routes for administrators and root.
- Release builds of the app ignore `PLAITWAY_SOCKET`, so a process cannot point the app, which answers
  credential requests from the Keychain, at a daemon of its own.
- The daemon copies the bundled openvpn into its root-owned run directory and checks its SHA-256 against
  the hash baked in at build time before it executes it.
- OpenVPN runs with no scripts, and the Reconciler (not openvpn) owns routes and DNS.
- Credentials are held by the daemon in memory only and never logged.
- Windows named-pipe code compiles but has not been run and has no caller identity check yet.

## Verified on real hardware, and what is not

Run as root on macOS 27 (2026-10-07, `PLAITWAY_ROOT_TESTS=1 sudo -E go test -tags rootintegration ./internal/...`, built as test binaries first):
route writes through PF_ROUTE (blackhole, interface-bound and scoped routes, IPv4 and IPv6, EEXIST and ESRCH mapping), DNS entries through
scutil and configd, the Reconciler's crash recovery from its journal and its sweep by marker against the real routing table, the WireGuard
engine with two real utun devices (handshake, ping through the tunnel, interface removal), and the OpenVPN engine with a real utun against
a loopback server using AES-128-CBC, SHA1 and LZO.

Used for real the same day: the helper registered through SMAppService from the notarized app in `/Applications` and run by launchd as root,
with openvpn started from its hash-verified copy; an OpenVPN profile connected to an ASUS router (TCP, comp-lzo, AES-128-CBC, SHA1, the profile's
pull-filters honoured) and a WireGuard full tunnel connected at the same time. The Reconciler's result matched what the kernel showed: a
bypass route to each server through the physical gateway, the WireGuard default split into `0.0.0.0/1` and `128.0.0.0/1` with the system
default untouched, the OpenVPN routes for `192.168.1.0/24` through its own utun (so the remote LAN and the internet were both reachable),
one resolver entry, and a journal of what was added.

Network change, same evening, both tunnels up: Ethernet came up next to Wi-Fi (same gateway), then Wi-Fi was turned off. In the second step the
kernel dropped the interface the two bypass routes were bound to and WireGuard logged "can't assign requested address" once; within the same second the
Reconciler removed and re-added both routes (journal: removed, pending, applied), no stale route remained, and neither tunnel reconnected. Then
the gateway itself changed (the Mac moved to a phone hotspot, `192.168.51.1` to `172.20.10.1`), the case that left a stale host route behind in
OpenVPN Connect: the Reconciler removed and re-added all three bypass routes through the new gateway within a second, no route to the old
gateway remained, openvpn was restarted over the preserved interface, re-authenticated from memory and connected, WireGuard kept running, and
the remote LAN and the internet stayed reachable. Recovery of OpenVPN took about 12 seconds.

Not verified: sleep and wake, a restart or crash of the helper under launchd while tunnels are up, auto-connect at boot, and an upgrade over a
running helper.
The repository has no LICENSE file yet; the bundled third-party notices are in `packaging/THIRD_PARTY_NOTICES.md`.
