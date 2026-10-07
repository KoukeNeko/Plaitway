<p align="center">
  <img src="Docs/app-icon.png" alt="Plaitway" width="160">
</p>

<h1 align="center">Plaitway</h1>

<p align="center">
  <strong>Several VPNs at once, from one menu bar item.</strong><br>
  Run OpenVPN and WireGuard profiles side by side on your Mac. One component
  owns the routes and DNS, so profiles do not fight over them and a network
  change leaves nothing stale behind.
</p>

<p align="center">
  <img alt="macOS 15+" src="https://img.shields.io/badge/MACOS-15%2B-000000?style=for-the-badge&logo=apple&logoColor=white">
  <img alt="Swift 6" src="https://img.shields.io/badge/SWIFT-6-F05138?style=for-the-badge&logo=swift&logoColor=white">
  <img alt="Go 1.27" src="https://img.shields.io/badge/GO-1.27-00ADD8?style=for-the-badge&logo=go&logoColor=white">
  <img alt="OpenVPN" src="https://img.shields.io/badge/OPENVPN-2.7-EA7E20?style=for-the-badge&logo=openvpn&logoColor=white">
  <img alt="WireGuard" src="https://img.shields.io/badge/WIREGUARD-88171A?style=for-the-badge&logo=wireguard&logoColor=white">
</p>

<p align="center">
  <a href="#getting-started"><strong>Getting started</strong></a>
  · <a href="#what-it-looks-like">What it looks like</a>
  · <a href="#compatibility">Compatibility</a>
  · <a href="#technical-reference">Technical reference</a>
</p>

Plaitway keeps your OpenVPN and WireGuard profiles in the macOS menu bar.
Switch an office network on while a personal WireGuard tunnel stays up, see
which one holds the internet and which one holds `192.168.1.0/24`, and open a
window for the details when something is not working.

It is built on a small root helper and Apple's own `utun` devices — no kernel
extension and no Network Extension. The official OpenVPN and the WireGuard Go
implementation run under the helper, and a single Reconciler decides what goes
in the routing table and in DNS, writes it down, and puts it right again after
a new Wi-Fi network or a changed gateway.

## What it looks like

The menu bar item is a menu. It says how many profiles are connected and lists
each one with its state; choosing a row connects, disconnects or retries it.

```
Menu bar
├── Connected: 2
├── ✓ Office          OpenVPN · Connected
├── ✓ Home Lab        WireGuard · Connected
│   Café              OpenVPN · Disconnected
├── ──────────────
├── Disconnect All
├── ──────────────
├── Open Plaitway
├── Import Profile…
├── Settings…
└── Quit Plaitway
```

The window is a sidebar of profiles and **Diagnostics**. Each profile has five
pages, also in the **View** menu (⌘1 to ⌘5):

| Page | What it shows |
|---|---|
| **Overview** | The state and why it is so, the uptime, the rate in and out with a two minute graph, what is not in effect, the remote, the interface and the addresses, and a WireGuard profile's public key |
| **Routes and DNS** | Every route and DNS entry the profile asked for, whether it is installed, and for the ones that are not, why: shadowed by a higher priority profile, blocked by the local network, failed |
| **Logs** | The profile's log, following the newest line, filtered by level (debug lines are hidden by default) and searchable |
| **Configuration** | The profile's text, to read and to change |
| **Settings** | The name, auto-connect, tunnel mode, priority, on-demand activation, and for WireGuard leaving the private ranges out |

Every state has a glyph of its own, a word and a colour — never a colour alone.

## Several profiles at the same time

Each enabled profile gets its own tunnel. Where they would collide, the
profile higher in the list wins: the sidebar order is the priority, and you
can drag it.

- A **full tunnel** takes the internet. Only one profile holds it at a time;
  the others stand by and say so.
- A **split tunnel** takes just the prefixes its profile names, so
  `192.168.1.0/24` can be the office while the internet goes through
  WireGuard.
- A prefix that **overlaps the network you are on** is blocked and marked as
  such, instead of cutting you off from your own router.
- The address of each VPN server keeps a route outside every tunnel, through
  the physical gateway, so a tunnel never tries to carry itself.

Quitting the app leaves the tunnels up: the helper keeps them, unless you
choose **Disconnect All and Quit**.

## Routes and DNS that clean up after themselves

The helper writes down every route and DNS entry it adds before it adds it. If
it is killed, the next start repairs the table from that journal instead of
trusting what it finds. When the network changes, it compares what the kernel
holds with what it should hold and fixes the difference:

- a new default gateway (Wi-Fi to Ethernet, a phone hotspot) re-points every
  server route within a second; OpenVPN reconnects over the interface it kept
  and WireGuard keeps running
- a wake from sleep is detected and handled the same way
- a route left behind by something else shows up under **Diagnostics ›
  Stale routes**, with a **Remove** button

**Diagnostics** also lists the network, the routes the helper owns, the
resolver entries and the recent changes, and **Copy Report** puts all of it on
the clipboard as plain text for a bug report — without log lines.

## Edit a profile without showing its keys

Both kinds of profile can be edited after they are imported, in the window or
with `plaitway edit`. Private keys and inline key blocks appear as
`‹secret 1›` placeholders until you ask to see them; leave a placeholder where
it is and the secret comes back at save time. The editor has line numbers and
colours for directives and values, and when the helper refuses a text it marks
the line.

A profile that is running keeps the text it started with. **Save** stores the
new text for the next connection; **Save and Reconnect** restarts the profile
with it.

## Connect when the network says so

- **Auto-connect** brings a profile up when the helper starts.
- **On demand** connects a profile while the Mac's primary network is
  Ethernet, Wi-Fi or either, and disconnects it on any other. A connect or a
  disconnect you did by hand holds until the network type changes.
- **Exclude private IPs** (WireGuard) takes the private ranges out of
  AllowedIPs, so the local network stays outside the tunnel while everything
  else goes through. The profile's own DNS servers stay in.

## From the command line

`plaitway` talks to the same helper as the app.

| Command | What it does |
|---|---|
| `status [profile]` | State, interface, addresses, uptime and traffic |
| `list` | Profiles with their ids |
| `connect profile` · `disconnect profile` | Switch a profile on or off; `connect` waits until it is up |
| `import file` | Add a profile from an `.ovpn` or a wg-quick `.conf` |
| `show profile` | The profile's text, secrets hidden unless `-secrets` is given |
| `edit profile` | Change the text in `$VISUAL` or `$EDITOR` |
| `set profile` | Change the name and the settings |
| `remove profile` | Disconnect and delete a profile |
| `logs [profile]` | A profile's log, or the helper's own |
| `diagnostics` · `resync` | The network and the owned routes · rebuild them |
| `watch` | Print profile changes as they happen |

## Bring your profiles

Import `.ovpn` and wg-quick `.conf` files from the window, by dropping them on
it, by opening them with Plaitway in Finder, or with `plaitway import`. The
picker accepts any file: the helper tells an OpenVPN profile from a WireGuard
one by its content.

Profile files are untrusted. Certificates and keys that an OpenVPN profile
names by file are read from the profile's own folder and put inline — any other
path is refused — and the helper rejects directives that read files or run
programs. Usernames and passwords go to the login Keychain.

## Know what the helper may do

The helper runs as root, so it is held to a short list:

- It listens on a Unix socket only, never on the network.
- Every call is checked against who is calling. Connecting and reading are
  open to the person at the console, administrators and root; importing,
  editing and deleting profiles, reading a profile's text (it holds the keys)
  and removing routes are for administrators and root.
- It runs the bundled OpenVPN from its own root-owned directory, after checking
  the binary against the hash baked in at build time, with no scripts.
- Credentials are kept in the helper's memory only and are never logged.

## In your language

English and 繁體中文, following the system. Messages that come from the helper
itself, such as why a connection is stuck, are in English.

## Getting started

1. Build the app (see [Release](#release)); there is no download yet
2. Copy `Plaitway.app` to `/Applications` and open it. The first run offers to
   install the helper: **Install Helper**, then allow Plaitway in **System
   Settings › General › Login Items & Extensions**. The app carries on by
   itself once macOS has approved it
3. Import a profile — the **+** in the toolbar, a dropped file, or
   `plaitway import`. If it needs a username and password, the first
   connection asks, and the login Keychain remembers them
4. Choose the profile and press **Connect**, or choose its row in the menu bar
5. Optional: link the command line tool,
   `ln -s /Applications/Plaitway.app/Contents/Resources/bin/plaitway /usr/local/bin/plaitway`

If SMAppService does not accept the helper, `sudo scripts/dev-install-daemon.sh`
installs it as a plain LaunchDaemon and `sudo scripts/dev-uninstall-daemon.sh`
removes it.

## Compatibility

- macOS 15 or later on Apple silicon; the window uses the macOS 26 and 27
  design where it exists
- **OpenVPN** profiles (`.ovpn`) over UDP or TCP, with the certificates and keys
  inline or beside the file; a username and password, and a key passphrase,
  are asked for when the profile needs them
- **WireGuard** profiles in wg-quick format (`.conf`), IPv4 and IPv6, with
  `Table = off` honoured
- Profiles that use `pkcs12` or `secret` are refused, and so are directives
  that run programs or read files outside the profile

Windows and Linux (Wails v3 windows on the same helper) are planned. Nothing
of them is built yet.

---

# Technical reference

## Architecture

```
plaitway/
├── macos/                  Swift package
│   ├── Sources/PlaitwayAPI       Generated from the proto (do not edit)
│   ├── Sources/PlaitwayClient    Daemon connection, observable profile state,
│   │                             Keychain credentials, profile import and
│   │                             editing (SecretMask, ConfigTokenizer), helper
│   │                             installation. No AppKit or SwiftUI
│   └── Sources/PlaitwayMenuBar   The app: menu bar item, window, settings
├── cmd/plaitwayd           The helper (root LaunchDaemon)
├── cmd/plaitway            The command line client
├── internal/
│   ├── manager             Profile lifecycle, settings, on-demand, logs, status
│   ├── profile             Profile store on disk and content checks
│   ├── ovpn                OpenVPN engine over the management interface
│   ├── wg                  WireGuard engine on embedded wireguard-go
│   ├── reconciler          The only code that changes routes and DNS
│   ├── osnet               Adapter interfaces; macos/ (PF_ROUTE, scutil) and fake/
│   ├── tunnel              The contracts between engines, adapters and Reconciler
│   ├── transport, peercred Unix socket serving and the caller's identity
│   └── gen                 Generated Go code
├── proto/plaitway/v1       plaitway.proto, the only hand-written API definition
└── packaging/, scripts/    The signed bundle, notarization, install scripts
```

**One owner for routes and DNS.** Engines announce what they want — routes,
nameservers, the endpoints that must stay reachable — and the Reconciler
computes one result: priorities, shadowing, conflicts with the local subnet,
the default route split into `0.0.0.0/1` and `128.0.0.0/1` so the system's own
default is never touched. It writes that through the macOS adapters and keeps a
write-ahead journal, so a crash or a network change is repaired from the
journal rather than leaked. No engine edits the routing table itself, and
OpenVPN runs with `route-noexec`.

**The kernel chooses the interface of a gateway route.** macOS accepts a route
through any gateway reachable by the default route, and binds it to an
interface of its own choosing: with Wi-Fi and Ethernet on one router the
interface may not be the one asked for. The Reconciler therefore compares
destination, gateway and flags, not the interface.

**A full tunnel without the private ranges is still the default holder.** With
the private ranges left out of AllowedIPs a WireGuard full tunnel announces a
list of prefixes and no default route. The Reconciler counts such a list as a
default route when it covers the whole unicast space except the private and
kernel ranges, checked exactly; any other hole does not count. Otherwise its
catch-all DNS would have been dropped.

**Network changes are watched through the routing socket**, with the sleep
time read from `kern.sleeptime` to notice a wake. After a gateway change the
Reconciler repairs the routes first and then asks the engines to rebind, so an
OpenVPN connection restarts over the interface it kept instead of tearing the
tunnel down.

**Profile text and secrets.** The helper stores the text it was given and
returns it only to administrators. `SecretMask` finds what is secret (WireGuard
`PrivateKey` and `PresharedKey`, the body of `<key>`, `<tls-auth>`,
`<tls-crypt>`, `<tls-crypt-v2>`, `<pkcs12>`, `<secret>` and credential blocks, a
PEM private key anywhere) and swaps each for a numbered placeholder; `restore`
puts them back and refuses a text in which one placeholder stands twice. The
helper names the line it refuses in the text it was sent, and
`displayLine(forRestoredLine:)` maps it back to what the editor shows. The CLI
masks the same things.

**On-demand follows the primary network.** The macOS network monitor reads the
kind of the default interface from `networksetup -listallhardwareports` (a
port named after a chip, such as a USB adapter, counts as Ethernet). A manual
connect or disconnect holds until the kind changes.

**The app is linked against the macOS 27 SDK** with
`-platform_version macos 15.0 27.0` in `macos/Package.swift`. SwiftPM stamps
the deployment target as the SDK version, and macOS draws an app linked that way
in the pre-Tahoe design. The binary still runs on macOS 15; everything newer is
behind availability checks.

**The menu bar item is a menu, not a popover.** The HIG asks for a menu unless
the content is too complex for one; macOS 27 changes how windows shown from a
status item behave and hides the images of menu items by default; Apple's own
VPN menu is a menu. Each row carries its state in text, and a mark.

**The API is gRPC over a Unix socket.** `proto/plaitway/v1/plaitway.proto`
generates the Go server, the Go client and the Swift client. The state stream
(`WatchProfiles`) is typed end to end, which an OpenAPI description over HTTP
could not do without a hand-written event type.

## Development

Requirements: Go 1.27.1, Xcode 27 (Swift 6.4), macOS 15 or later.

```bash
make test      # Go tests, then the Swift tests against a fake daemon
make build     # bin/plaitwayd and the Swift app
make generate  # only after changing proto/
```

The daemon runs without root on an in-memory backend, which is a complete
stand-in for UI work:

```bash
go build -o bin/plaitwayd ./cmd/plaitwayd
SOCK="$(getconf DARWIN_USER_TEMP_DIR)plaitway.sock"     # macOS limits socket paths to 104 bytes
./bin/plaitwayd -fake -socket "$SOCK" -state-dir /tmp/plaitway-dev &
PLAITWAY_SOCKET="$SOCK" swift run --package-path macos Plaitway     # the override works in debug builds only
go run ./cmd/plaitway -socket "$SOCK" status
```

Profile text with `# fake: needs-credentials`, `# fake: fail`,
`# fake: conflict` or `# fake: reject` steers the fake backend. The fake daemon
has a Wi-Fi primary network that never changes and a stand-in public key for
WireGuard profiles. Without `-fake` the daemon uses the real engines; without
root it can read the network and import profiles, but creating a tunnel fails
with a permission error.

Tests that need root change the routing table, so they are built first and run
by hand:

```bash
go test -c -tags rootintegration -o /tmp/reconciler.test ./internal/reconciler
sudo env PLAITWAY_ROOT_TESTS=1 /tmp/reconciler.test -test.v
```

### Release

```bash
make app                  # build/Plaitway.app, signed with PLAITWAY_SIGN_IDENTITY
make dist                 # build/Plaitway-<version>.zip and .dmg; the version is in VERSION
make verify               # structure, plists, linkage, signatures, a start of the packaged daemon
packaging/notarize.sh     # notarize and staple, then rebuild the zip and dmg
```

[packaging/README.md](packaging/README.md) describes the bundle, the signing
order, install, upgrade, uninstall and the licenses.

Files on a machine: profiles and the route journal in
`/Library/Application Support/Plaitway`, the helper's log in
`/Library/Logs/Plaitway/plaitwayd.log` (readable by root only), the socket in
`/var/run/plaitway`.

## Verified on real hardware, and what is not

Run as root on macOS 27 (`go test -tags rootintegration ./internal/...`): route
writes through PF_ROUTE (blackhole, interface-bound and scoped routes, IPv4
and IPv6), DNS entries through scutil and configd, the Reconciler's crash
recovery from its journal and its sweep by marker against the real routing
table, the WireGuard engine with two real `utun` devices (handshake, ping
through the tunnel), and the OpenVPN engine with a real `utun` against a
loopback server.

Used for real, from the notarized app in `/Applications`, with the helper run
by launchd: an OpenVPN profile to a router and a WireGuard full tunnel
connected at the same time. The routes the Reconciler wrote matched what the
kernel showed — a route to each server through the physical gateway, the
WireGuard default split in two halves, the OpenVPN prefix through its own
interface — and both the remote network and the internet stayed reachable.
Wi-Fi to Ethernet, Wi-Fi off, and the move to a phone hotspot (a gateway
change, the case that leaves a stale host route behind in other clients) were
repaired within a second with no stale route; OpenVPN took about 12 seconds to
reconnect.

Not verified: a sleep of several minutes, a restart or crash of the helper
under launchd while tunnels are up, auto-connect at boot, an upgrade over a
running helper, running beside OpenVPN Connect or the WireGuard app, and
on-demand activation and the private-range exclusion with real tunnels.

## Troubleshooting

### The window says "Helper not found"

The app was started from a folder other than `/Applications`, usually a
`build/` folder that has since moved. Run the copy in `/Applications`.

### The helper does not answer

The helper's log says why:

```bash
sudo tail -n 50 /Library/Logs/Plaitway/plaitwayd.log
sudo launchctl print system/io.github.koukeneko.plaitway.daemon
```

**Settings › Reinstall Helper** registers it again; connected profiles
disconnect while it does.

### A route is left behind

```bash
plaitway diagnostics
plaitway resync
```

A route the helper did not add is listed under stale routes and is removed only
when you ask: **Diagnostics › Stale routes › Remove**.

### The menu bar item is missing

macOS lets people switch an app's menu bar item off and hides items that do not
fit next to the notch. Turn it back on in **Settings › Show in Menu Bar** and in
**System Settings › Menu Bar › Allow in the Menu Bar**. The window, the Dock
menu and the command line work without it.

## Known limitations

- No kill switch, no IPsec, no per-profile DNS settings, no auto-update
- OpenVPN servers that ask for a second factor or a challenge are not supported
- Profiles with `pkcs12` or `secret` are refused
- Priority and reordering apply to running tunnels at once; the tunnel mode and
  auto-connect apply at the next connection
- Credentials are always remembered in the login Keychain; there is no opt-out
- The helper's log is rotated when it starts, not while it runs
- A corrupt `profiles.json` stops the helper instead of being recovered
- Windows named-pipe code compiles but has not been run and has no caller
  identity check

<p>
  <img alt="Go" src="https://img.shields.io/badge/GO-1.27-00ADD8?style=for-the-badge&logo=go&logoColor=white">
  <img alt="SwiftUI" src="https://img.shields.io/badge/SWIFTUI-0071E3?style=for-the-badge&logo=swift&logoColor=white">
  <a href="https://github.com/grpc/grpc-swift-2"><img alt="gRPC" src="https://img.shields.io/badge/GRPC-244C5A?style=for-the-badge&logo=grpc&logoColor=white"></a>
  <a href="https://git.zx2c4.com/wireguard-go/"><img alt="wireguard-go" src="https://img.shields.io/badge/WIREGUARD--GO-88171A?style=for-the-badge&logo=wireguard&logoColor=white"></a>
  <a href="https://openvpn.net/community/"><img alt="OpenVPN" src="https://img.shields.io/badge/OPENVPN-2.7-EA7E20?style=for-the-badge&logo=openvpn&logoColor=white"></a>
</p>

## License

The project has no license file yet. Third-party components keep their own
terms; the bundled ones, including the GPL source offer for OpenVPN, LZO and
LZ4, are listed in [packaging/THIRD_PARTY_NOTICES.md](packaging/THIRD_PARTY_NOTICES.md).

### Trademarks

OpenVPN is a registered trademark of OpenVPN Inc. WireGuard is a registered
trademark of Jason A. Donenfeld. Apple, macOS and Keychain are trademarks of
Apple Inc. Plaitway is not affiliated with or endorsed by any of them; the
names are used only to say which protocols and which system it works with.
