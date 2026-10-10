<p align="center">
  <img src="Docs/app-icon.png" alt="Plaitway" width="160">
</p>

<h1 align="center">Plaitway</h1>

<p align="center">
  <strong>Several VPNs at once, without them fighting over your routes.</strong><br>
  Run OpenVPN and WireGuard profiles side by side on macOS and Linux. One
  component owns the routing table and DNS, so a new Wi-Fi network or a changed
  gateway leaves nothing stale behind.
</p>

<p align="center">
  <img alt="macOS 15+" src="https://img.shields.io/badge/MACOS-15%2B-000000?style=for-the-badge&logo=apple&logoColor=white">
  <img alt="Linux with systemd" src="https://img.shields.io/badge/LINUX-SYSTEMD-FCC624?style=for-the-badge&logo=linux&logoColor=black">
  <img alt="Swift 6" src="https://img.shields.io/badge/SWIFT-6-F05138?style=for-the-badge&logo=swift&logoColor=white">
  <img alt="Go 1.27" src="https://img.shields.io/badge/GO-1.27-00ADD8?style=for-the-badge&logo=go&logoColor=white">
  <img alt="OpenVPN" src="https://img.shields.io/badge/OPENVPN-2.7-EA7E20?style=for-the-badge&logo=openvpn&logoColor=white">
  <img alt="WireGuard" src="https://img.shields.io/badge/WIREGUARD-88171A?style=for-the-badge&logo=wireguard&logoColor=white">
  <a href="https://github.com/KoukeNeko/Plaitway/releases/latest"><img alt="Latest release" src="https://img.shields.io/github/v/release/KoukeNeko/Plaitway?style=for-the-badge&label=RELEASE&color=2196F3"></a>
</p>

<p align="center">
  <a href="#install"><strong>Install</strong></a>
  · <a href="#what-you-get">What you get</a>
  · <a href="#from-the-command-line">Command line</a>
  · <a href="#update-and-uninstall">Update and uninstall</a>
  · <a href="#compatibility">Compatibility</a>
  · <a href="#technical-reference">Technical reference</a>
</p>

<p align="center">
  <img width="1111" height="704" alt="image" src="https://github.com/user-attachments/assets/6d09589b-1ce9-4a36-9228-6980049001d0" />

</p>

Run an office OpenVPN and a personal WireGuard tunnel together, see which one
holds the internet and which one holds `192.168.1.0/24`, and open the window for
the details when something is not working. Every tunnel that is up wants the
default route and the DNS; one Reconciler decides who gets what, writes it down
before it changes anything, and puts it right again after a network change.

## Install

**macOS 15 or later, Apple silicon**

```sh
brew install --cask koukeneko/tap/plaitway
```

Or take the dmg from the [latest release](https://github.com/KoukeNeko/Plaitway/releases/latest).
Open **Plaitway**; the first run offers **Install Helper**, and macOS asks you to allow it in
**System Settings › General › Login Items & Extensions**.

**Linux with systemd** (Ubuntu 24.04 and 26.04, Debian 13)

```sh
sudo install -d -m 0755 /etc/apt/keyrings
sudo curl -fsSL https://koukeneko.github.io/Plaitway/key.asc -o /etc/apt/keyrings/plaitway.asc
echo "deb [signed-by=/etc/apt/keyrings/plaitway.asc] https://koukeneko.github.io/Plaitway stable main" | sudo tee /etc/apt/sources.list.d/plaitway.list
sudo chmod 0644 /etc/apt/keyrings/plaitway.asc /etc/apt/sources.list.d/plaitway.list
sudo apt update && sudo apt install plaitway
```

It installs OpenVPN and the GTK libraries and starts the helper, `plaitwayd.service`. Open
**Plaitway** from the application menu, or run `plaitway-app`. `sudo apt upgrade` brings the later versions.

**Linux with OpenRC** (Gentoo): the live ebuild of this repository builds it with the system's Go,
installs the OpenRC service, and pulls in openresolv for DNS settings and OpenVPN for OpenVPN profiles:

```sh
git clone https://github.com/KoukeNeko/Plaitway && cd Plaitway
printf '[plaitway]\nlocation = %s\n' "$PWD/packaging/linux/gentoo" | sudo tee /etc/portage/repos.conf/plaitway.conf
echo '=net-vpn/plaitway-9999 **' | sudo tee -a /etc/portage/package.accept_keywords/plaitway
sudo emerge net-vpn/plaitway
sudo rc-update add plaitwayd default && sudo rc-service plaitwayd start
```

Open **Plaitway** from the application menu, or run `plaitway-app`. With NetworkManager, set
`rc-manager=resolvconf` in the `[main]` section of a file in `/etc/NetworkManager/conf.d`, or it
replaces the DNS servers of a full tunnel; [Linux without systemd](#linux-without-systemd-gentoo-openrc) has the details.

**Windows 11, x64:** download `Plaitway-<version>-x64-en-US.msi` (or `zh-TW`) from the
[latest release](https://github.com/KoukeNeko/Plaitway/releases/latest) and run it. The package is not signed yet, so
SmartScreen asks for confirmation. WireGuard profiles need nothing else; OpenVPN profiles use the OpenVPN installation of
the PC. See [Windows](#windows).

**Then, on either system:**

1. Import a profile: the **+** in the toolbar, a file dropped on the window, or `plaitway import file.ovpn`
2. Press **Connect**, or choose the profile's row in the menu bar or tray. A profile that needs a username and
   password asks at the first connection; the login Keychain (macOS) or the Secret Service (Linux) remembers them
3. Add a second profile and connect it too. The sidebar order is the priority, and the Routes and DNS page says what each one holds

<details>
<summary><strong>Other ways to install</strong></summary>

- **macOS, without Homebrew:** copy `Plaitway.app` from the dmg or the zip to `/Applications`. To get the command
  line tool onto your path: `ln -s /Applications/Plaitway.app/Contents/Resources/bin/plaitway /usr/local/bin/plaitway`
  (Homebrew does it for you)
- **macOS, if SMAppService does not accept the helper:** `sudo scripts/dev-install-daemon.sh` installs it as a
  plain LaunchDaemon and `sudo scripts/dev-uninstall-daemon.sh` removes it
- **Linux, without the apt repository:** `sudo apt install ./plaitway_*.deb` with the `.deb` of the
  release page (it is attached a few minutes after the macOS files), or build it with `make deb`, which writes it
  to `build/linux`
- **Linux with OpenRC, from a checkout without portage:** `make linux-build`, then
  `sudo packaging/linux/install.sh --prefix /usr --openrc` with the `--python-dir` of
  [Linux without systemd](#linux-without-systemd-gentoo-openrc)
- **From source:** see [Development](#development) and [Release](#release)

</details>

## What you get

- **[Several profiles at the same time](#several-profiles-at-the-same-time).** Each enabled profile gets its own
  tunnel. A full tunnel takes the internet, a split tunnel takes only its prefixes, and where they would collide
  the profile higher in the list wins
- **[Routes and DNS that clean up after themselves](#routes-and-dns-that-clean-up-after-themselves).** Every route
  is written down before it is added, so a crash, a new gateway or a wake from sleep is repaired and nothing is
  left behind. **Diagnostics** shows what is owned and what is stale
- **[Edit a profile without showing its keys](#edit-a-profile-without-showing-its-keys).** Private keys appear as
  placeholders until you ask, and the editor marks the line the helper refuses
- **[Connect when the network says so](#connect-when-the-network-says-so).** Auto-connect, on-demand activation by
  Ethernet or Wi-Fi, and leaving the private ranges out of a WireGuard tunnel
- **[A window, a menu bar item and a command line](#from-the-command-line).** All three talk to the same helper, so
  what you start in one is what the other shows
- **[A helper that is held to a short list](#know-what-the-helper-may-do).** It listens on a local socket only,
  checks who is calling, and on macOS runs only the OpenVPN it has checked against a hash

## What it looks like

The sections down to Compatibility describe the macOS app; [Linux](#linux) says what differs there.

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

The window is a sidebar, as tall as the window: the profiles at the top and
**Diagnostics** and **Settings** (the app's own, and the helper's) at the bottom.
The page switcher and **+** (import) are at the right of the toolbar;
**Connect** or **Disconnect**, and **Retry** for a profile that failed, are in a
bar at the bottom. Each profile has five pages, also in the **View** menu (⌘1
to ⌘5):

| Page | What it shows |
|---|---|
| **Overview** | The state and why it is so, the uptime, the rate in and out with a two minute graph, what is not in effect, the remote, the interface and the addresses, and a WireGuard profile's public key |
| **Routes and DNS** | Every route and DNS entry the profile asked for, whether it is installed, and for the ones that are not, why: shadowed by a higher priority profile, blocked by the local network, failed |
| **Logs** | The profile's log, following the newest line, filtered by level (debug lines are hidden by default) and searchable |
| **Configuration** | The profile's text, to read and to change |
| **Settings** | The name, auto-connect, tunnel mode, priority, on-demand activation, and for WireGuard leaving the private ranges out |

What a page does that the others do not — the search and level of the log,
**Save** and **Show Secrets** of the configuration — is in a strip at its top,
not in the toolbar. **Diagnostics** has two pages: the overview and the helper's
own log.

Every state has a glyph of its own, a word and a colour — never a colour alone.
The sidebar shows it as a shield, the one the menu bar item uses.

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

On Linux the rules are the same, with these meanings: an administrator is root
or a member of `sudo`, `wheel` or `admin`; the person at the console is a user
with an active session on a seat of systemd-logind, so a session over SSH is
neither the console nor an administrator unless its user is one; without
logind's state only administrators get in. The helper runs the distribution's
`openvpn` where it is, and only when root owns the file and every directory
above it and nobody else can write to them. The systemd unit restricts the
helper further; [Linux](#linux) lists how.

## In your language

English and 繁體中文, following the system. Messages that come from the helper
itself, such as why a connection is stuck, are in English.

## Update and uninstall

**Update on macOS:** `brew upgrade --cask koukeneko/tap/plaitway`, or replace
`Plaitway.app` in `/Applications` with the new one. When the helper is older than the
app, the app offers **Reinstall Helper**, which disconnects running profiles.

**Update on Linux:** `sudo apt update && sudo apt upgrade` with the apt repository added,
or `sudo apt install ./plaitway_*.deb` over the old package. The upgrade restarts the
helper, which disconnects running profiles; the stored profiles stay. Quit Plaitway and
open it again to get the new window.

**Update on Gentoo:** `sudo emerge @live-rebuild`, then `sudo rc-service plaitwayd restart`, which
disconnects running profiles; the stored profiles stay. Installed by hand, build and run
`install.sh` again.

**Uninstall on macOS**, in this order:

1. In Plaitway, delete the profiles you do not want to keep. This also deletes their
   saved credentials from the Keychain
2. **Uninstall Helper…** in Settings. The helper removes its routes and DNS entries
3. Quit Plaitway and remove the app: `brew uninstall --cask koukeneko/tap/plaitway`, or
   move `Plaitway.app` to the Trash. Removing the app before step 2 can leave a root
   helper registered with nothing to stop it
4. The helper's data stays until you delete it, because the profiles hold private keys
   that cannot be made again:
   `sudo rm -rf "/Library/Application Support/Plaitway" /Library/Logs/Plaitway /var/run/plaitway`

[packaging/README.md](packaging/README.md#uninstall) lists what is left where, and has
the steps for a helper installed with `scripts/dev-install-daemon.sh`.

**Uninstall on Linux**, in this order:

1. In Plaitway, delete the profiles you do not want to keep. This also deletes their
   saved passwords from the Secret Service (GNOME Keyring, KWallet)
2. `sudo apt remove plaitway` stops the helper and removes the program. The profiles stay
   in `/var/lib/plaitway`; `sudo apt purge plaitway` deletes them too, and they hold
   private keys that cannot be made again
3. If you added the apt repository,
   `sudo rm /etc/apt/sources.list.d/plaitway.list /etc/apt/keyrings/plaitway.asc`, then
   `sudo apt update`
4. What the app keeps in your home: `~/.local/state/plaitway`, the window's own
   settings, and with **Launch at Login** on,
   `~/.config/autostart/io.github.koukeneko.Plaitway.desktop`

On Gentoo, `sudo rc-service plaitwayd stop && sudo rc-update del plaitwayd default`, then
`sudo emerge --unmerge net-vpn/plaitway`. Installed with `make install`, there is no uninstall
target: delete the files of the table in [Linux](#linux) from below `/usr/local`. Installed with
`install.sh --openrc`, stop and remove the service the same way and delete `/etc/init.d/plaitwayd`,
`/etc/logrotate.d/plaitwayd` and the files of the table. The profiles stay in `/var/lib/plaitway`;
to delete them with their private keys as well, `sudo rm -rf /var/lib/plaitway /var/log/plaitwayd.log*`. A helper installed with
`scripts/linux/dev-install-daemon.sh` is removed by
`sudo scripts/linux/dev-uninstall-daemon.sh`, and `--purge` deletes the profiles as well.

## Compatibility

- macOS 15 or later on Apple silicon; the window uses the macOS 26 and 27
  design where it exists
- **OpenVPN** profiles (`.ovpn`) over UDP or TCP, with the certificates and keys
  inline or beside the file; a username and password, and a key passphrase,
  are asked for when the profile needs them
- **WireGuard** profiles in wg-quick format (`.conf`), IPv4 and IPv6, with
  `Table = off` honoured. An address with a `/0` prefix, such as the
  `192.168.50.0/0` that ASUS routers write for their LAN, is the default route
  as far as WireGuard is concerned, and the import says so
- Profiles that use `pkcs12` or `secret` are refused, and so are directives
  that run programs or read files outside the profile
- Linux with systemd, Ubuntu 24.04 and 26.04 and Debian 13: a kernel with `/dev/net/tun`,
  `systemd-resolved` for DNS settings, and the distribution's `openvpn` (2.6 or
  later) for OpenVPN profiles; WireGuard profiles run on the embedded
  wireguard-go and need no kernel module. OpenVPN profiles with `dev tap` are
  refused. The app needs GTK 4.14 and libadwaita 1.5, the versions of Ubuntu
  24.04; [Linux](#linux) says what was run where. Gentoo with OpenRC installs from the
  live ebuild, with DNS settings for a full tunnel through openresolv:
  [Linux without systemd](#linux-without-systemd-gentoo-openrc)

Windows is described under [Windows](#windows).

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
├── linux/                  The Linux app: GTK 4 and libadwaita, Python (PyGObject)
├── windows/                C# solution: the WinUI 3 app, the client library, their tests
├── cmd/plaitwayd           The helper (root LaunchDaemon, systemd service on Linux, Windows service)
├── cmd/plaitway            The command line client
├── internal/
│   ├── manager             Profile lifecycle, settings, on-demand, logs, status
│   ├── profile             Profile store on disk and content checks
│   ├── fsperm              Private files and directories: mode bits, and on
│   │                       Windows an access list
│   ├── ovpn                OpenVPN engine over the management interface
│   ├── wg                  WireGuard engine on embedded wireguard-go
│   ├── reconciler          The only code that changes routes and DNS
│   ├── osnet               Adapter interfaces; macos/ (PF_ROUTE, scutil), linux/
│   │                       (netlink, resolvectl), windows/ (IP Helper API, NRPT rules
│   │                       in the registry) and fake/
│   ├── winiface            Windows only: addresses, MTU and metric of an adapter an engine made
│   ├── authenticode        Windows only: the signature check of wintun.dll and openvpn.exe
│   ├── tunnel              The contracts between engines, adapters and Reconciler
│   ├── transport, peercred Unix socket or named pipe serving and the caller's
│   │                       identity
│   └── gen                 Generated Go code
├── proto/plaitway/v1       plaitway.proto, the only hand-written API definition
└── packaging/, scripts/    The signed bundle, notarization, install scripts;
                            packaging/linux and scripts/linux for Linux
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

**Toolbar items are declared in `MainView`, and the page switcher is AppKit's.**
SwiftUI replaces a toolbar item, and the toolbar is seen to be built again,
whenever the view that declares the item is drawn again or the item's own
content changes. A profile's page is drawn again with every reading of its
traffic, and a SwiftUI picker whose selection changes inside an item is
replaced with each change, so the items declared by a page, a picker and an
explicit `ToolbarItem(id:)` were all replaced, at each reading of a profile's
traffic and at each choice of a page. The page switcher is an
`NSSegmentedControl` that follows `AppModel` itself, declared next to **+** in
`MainView`, where neither happens.
The toolbar background is set to visible for every page: left to the system it
follows what is under it, and changed between a page that starts with a scroll
view and a page that starts with a strip of controls.

**The log backlog is shown in one update.** The helper sends the last 200 lines
of a log as a burst. Applied line by line, the page was drawn 487 times over
2.3 seconds when Logs was opened; lines that arrive within 50 ms of each other
are now shown together.

**The menu bar item is a menu, not a popover.** The HIG asks for a menu unless
the content is too complex for one; macOS 27 changes how windows shown from a
status item behave and hides the images of menu items by default; Apple's own
VPN menu is a menu. Each row carries its state in text, and a mark.

**The API is gRPC over a Unix socket.** `proto/plaitway/v1/plaitway.proto`
generates the Go server, the Go client and the Swift client. The state stream
(`WatchProfiles`) is typed end to end, which an OpenAPI description over HTTP
could not do without a hand-written event type.

## Development

Requirements: Go 1.27.1, Xcode 27 (Swift 6.4), macOS 15 or later. The Go
packages also build and test on Windows, see [Windows](#windows).

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

GitHub Actions (`.github/workflows`) runs `gofmt`, `go mod tidy` and
`shellcheck`; builds, vets and tests the Go code on macOS, Linux and Windows;
runs the Swift tests on the Xcode 27 image; and, when `proto/` or the generated
code changes, runs `make lint-proto` and `make generate` and fails if the
committed files differ. Three Linux jobs run the tests of the app, the root tests
and the build and check of the package, see [Linux](#linux).

### Release

```bash
make app                  # build/Plaitway.app, signed with PLAITWAY_SIGN_IDENTITY
make dist                 # build/Plaitway-<version>.zip and .dmg; the version is in VERSION
make verify               # structure, plists, linkage, signatures, a start of the packaged daemon
packaging/notarize.sh     # notarize and staple, then rebuild the zip and dmg
```

Pushing a tag `vX.Y.Z` that matches `VERSION`, with `releases/X.Y.Z.md` written,
does all of this in GitHub Actions and then publishes the release and the
Homebrew cask;
[packaging/README.md](packaging/README.md#releasing) says how.
[packaging/README.md](packaging/README.md) also describes the bundle, the
signing order, install, upgrade, uninstall and the licenses.

Files on a machine: profiles and the route journal in
`/Library/Application Support/Plaitway`, the helper's log in
`/Library/Logs/Plaitway/plaitwayd.log` (readable by root only), the socket in
`/var/run/plaitway`.

## Windows

`plaitwayd` runs on Windows with the real OpenVPN and WireGuard engines, as the service `PlaitwayHelper` or in a
console, and `plaitway` is the same command line as on macOS. The app is native: C#, .NET 10, WinUI 3, unpackaged,
in `windows/` ([windows/README.md](windows/README.md)). `packaging/windows/build-installer.ps1` builds the MSI
([packaging/windows/README.md](packaging/windows/README.md)); the release attaches it, unsigned.
[Docs/windows-status.md](Docs/windows-status.md) lists what is verified, what waits for a machine or a decision and what is
not done; [Docs/windows-architecture.md](Docs/windows-architecture.md) says what is where, who is trusted with what and
why the choices were made.

| | OpenVPN | WireGuard |
|---|---|---|
| Tunnel device | A TAP-Windows6 adapter that the engine makes with `tapctl`, named `Plaitway-ovpn-` and eight hex digits | A wintun adapter named `Plaitway-` and eight hex digits |
| Needs | `openvpn.exe` and `tapctl.exe` of an OpenVPN installation (tested with 2.7.1) that has the TAP-Windows6 driver, signed by OpenVPN Inc., in a folder that only administrators can change: `<folder of plaitwayd.exe>\openvpn\bin`, else `C:\Program Files\OpenVPN\bin`; `-openvpn` names another | `wintun.dll` signed by WireGuard LLC, next to `plaitwayd.exe` |
| Routes | The Reconciler, through the tunnel's own gateway: the adapter answers only for it | The Reconciler, on-link |
| DNS | NRPT rules written by the Reconciler | NRPT rules written by the Reconciler |

An engine that cannot run does not stop the daemon: it logs `engine unavailable` with the reason, and the reason is
shown with the profile.

### Build and run

Go 1.27.1 builds the daemon and the command line client. .NET SDK 10 (`windows/global.json` pins 10.0.401) builds the
app; no Visual Studio workload is needed. The app targets Windows 10 1809 or later, x64; development and tests are on
Windows 11, and Windows 10 and arm64 are not tested.

```powershell
go build -o bin\plaitwayd.exe .\cmd\plaitwayd
go build -o bin\plaitway.exe .\cmd\plaitway
powershell -File packaging\windows\fetch-wintun.ps1 -OutputDirectory bin       # wintun.dll, checked by hash and signature
dotnet build windows\Plaitway.sln -c Release
```

Without elevation the daemon runs on the in-memory backend, which is a complete stand-in for UI work:

```powershell
$daemon = Start-Process bin\plaitwayd.exe -ArgumentList '-fake','-socket','\\.\pipe\plaitway-dev' -WindowStyle Hidden -PassThru
bin\plaitway.exe -socket '\\.\pipe\plaitway-dev' list
Stop-Process -Id $daemon.Id
```

With the real engines, in an elevated PowerShell, `packaging\windows\dev\Run-DaemonElevated.ps1` runs the daemon in
the foreground on a scratch pipe and a scratch state directory, and lists what it left behind when it ends
([packaging/windows/dev/README.md](packaging/windows/dev/README.md)). The service is registered with
`plaitwayd.exe install -start` from an elevated shell, for a copy that is below Program Files: `install` refuses an
executable, or a folder above it, that a standard user can change. The app offers the same through **Install Helper**,
and Windows asks for confirmation once. `plaitwayd.exe status` prints `PlaitwayHelper: running`, `stopped` or `not
installed` and needs no elevation; its exit code is 0, 3 or 4. The registration, the commands, the update and the
uninstall are described in [packaging/windows/service/README.md](packaging/windows/service/README.md). An uninstall
keeps `%ProgramData%\Plaitway`, because the profiles hold private keys; `uninstall -purge` deletes it.

| | Windows |
|---|---|
| Control pipe | `\\.\pipe\plaitway`. `-socket` and `PLAITWAY_SOCKET` take another name under `\\.\pipe\`; the daemon and the client refuse any other value |
| Privileged daemon | State in `%ProgramData%\Plaitway`, run directory in `%ProgramData%\Plaitway\run`, log in `%ProgramData%\Plaitway\Logs\plaitwayd.log` |
| Development daemon | State in `plaitway-state` and run directory in `plaitway-run`, both below `%TEMP%`; stderr only |

**Privilege is the elevation of the process token.** LocalSystem and an
elevated administrator are privileged and use the `%ProgramData%` locations; an
administrator's ordinary shell has a filtered token, is not privileged and gets
the development locations. `%ProgramData%` is asked of the shell, not read from
the environment.

**Files are private by access list.** The state, run and log directories and
the log file have a protected access list (it takes nothing from the parent;
the entries of a directory pass to the files created in it) for SYSTEM and
Administrators, plus the daemon's own user when the process is not elevated,
so that a development daemon can use its directories. `-socket-mode` is
accepted and ignored.

**The pipe has its own access list.** It denies network logons first, gives
SYSTEM, Administrators and the daemon's user full control, and gives
interactive users read and write, without the right to create another instance
of the pipe. Remote clients are rejected. The client connects at identification
level and checks the owner of the pipe before it sends anything: SYSTEM,
Administrators or the calling user, so that a pipe squatted by another user
receives no request.

**The caller is identified by the token of the pipe client, not by its pid.**
The daemon reads the user SID, the Administrators group and the logon session
from it; a client that connects anonymously is refused. The authorization
follows the macOS one:

| Level | Calls | Caller |
|---|---|---|
| Modify | Import, edit, delete and reorder profiles, read a profile's text, remove a route | Administrator: the Administrators group is enabled in the token, or deny-only, which is how an administrator's unelevated shell carries it |
| Connect | Read state and logs, connect and disconnect, answer credential requests, resync | An administrator, or a caller in the active console session; a machine without one refuses non-administrators |

**The command line differs in two places.** `plaitway edit` runs `$VISUAL`,
else `$EDITOR`, else `notepad` (`vi` elsewhere). The variable is read as a
Windows command line, where double quotes group and a backslash belongs to a
path; a value that is the path of an existing file is not split, so an unquoted
path with spaces works. `-socket` and `PLAITWAY_SOCKET` must start with
`\\.\pipe\`.

### Tests

`go test ./...` runs on Windows without a tag; files for one system
carry a `_windows` suffix or a `//go:build` line (`unix`, `!windows`). The
engine tests that run a fake `openvpn`, and the tests of the Unix socket, run
only on Unix. A test that needs what the session lacks (a console, permission
to create symbolic links, a real `openvpn`) skips and says why.
To run the Unix tests from a Windows machine, cross-compile a package's
test binary and run it in WSL from the package directory:

```powershell
$env:GOOS = 'linux'; $env:GOARCH = 'amd64'
go test -c -o bin\ovpn.linux.test .\internal\ovpn
Remove-Item Env:GOOS, Env:GOARCH
wsl --cd /mnt/e/dev/plaitway/internal/ovpn -- ../../bin/ovpn.linux.test
```

The tests of `cmd/plaitway` start `go build` for both programs. A WSL
distribution without Go needs an executable named `go` first on `PATH` that
copies Linux builds of `plaitway` and `plaitwayd` to the `-o` path it is given.

A Go test binary is built to a new temporary path on every run, and Windows Defender Firewall asks about each one that
listens on an address other than loopback, leaving two permanent allow rules behind for a file that is gone a minute
later. Tests listen on `127.0.0.0/8` and `::1` only.
`scripts\windows\Test-LoopbackOnly.ps1` runs the tests of every package, or of the packages named in `-Packages`, from
fixed paths, with a watcher that reads the socket tables without pausing, and fails with the name of a package that
opens another address (`-PerTest <package>` names the test). The WireGuard engine's tests go through `newBind`, never
through wireguard-go's default bind.

The C# tests run from the `windows` directory, because `global.json` selects the runner for it:
`dotnet test --project Tests\Plaitway.Client.Tests -c Release`, and the same for `Plaitway.AppCore.Tests`. Both start
`plaitwayd -fake` on pipes of their own. The UI tests open the app window on the screen of whoever runs them.

The tests that change the routing table, DNS, adapters or the service carry the `rootintegration` tag and need an
elevated shell. They use scratch resources only, and `packaging\windows\dev\Run-ElevatedTests.ps1` runs them in groups
with a snapshot and a comparison around each; `-WhatIf` prints the plan from any shell.
[Docs/windows-elevated-tests.md](Docs/windows-elevated-tests.md) lists every test, what it changes, what is expected
and how to undo it.

### Troubleshooting on Windows

**The app says "Helper not installed" or "Helper stopped".** The service is not registered or is not running.
`plaitwayd.exe status` says which (`PlaitwayHelper: not installed`, exit code 4; `PlaitwayHelper: stopped`, exit code
3). **Install Helper** and **Start Helper** run `install -start` and `start` with the consent prompt. The command line
client says `the daemon is not running: \\.\pipe\plaitway does not exist`. From an elevated shell, `plaitwayd.exe
install -start` registers it; from a copy in a folder that a standard user can change it fails with
`plaitwayd install: <path> cannot be a service: ...; use an administrator-only location such as Program Files`, and
without elevation with `plaitwayd install: this needs administrator rights: run from an elevated shell`.

**The helper's log.** `%ProgramData%\Plaitway\Logs\plaitwayd.log`, readable by SYSTEM and Administrators. A service
that ends at start shows the reason as the last `ERROR` line.

**The command line client or the app refuses the pipe.** The command line client says `refusing to use
\\.\pipe\plaitway: it is owned by <account>, not by SYSTEM, Administrators or <your account>`; other clients see the same
text after `pipe server refused: `. Something other than the service serves the pipe. The app words it as "The helper's
pipe belongs to an unexpected account. Plaitway does not use it." Start the service, or stop the other program.

**WireGuard is unavailable: `wintun.dll`.** The profile shows `<folder>\wintun.dll is missing; the WireGuard engine
needs wintun.dll in the folder of the program`. A file that is there but is not signed by WireGuard LLC is reported
with its signature problem (`has no Authenticode signature`, `is signed by "<name>", not by "WireGuard LLC"`, `was
changed after it was signed`). Put the checked copy next to `plaitwayd.exe`:
`powershell -File packaging\windows\fetch-wintun.ps1 -OutputDirectory <folder of plaitwayd.exe>`. The first adapter
installs the wintun driver into the driver store.

**OpenVPN is unavailable: `openvpn is not trusted`.** The daemon runs `openvpn.exe` as LocalSystem, so it runs only a
file that a standard user could not have replaced. The reason follows the message: `openvpn is not trusted: <path>:
can be changed by an account that is not SYSTEM, Administrators or TrustedInstaller`, `... is not on a local fixed
disk`, `... has no Authenticode signature`, `... is signed by "<name>", not by "OpenVPN Inc."`. Install OpenVPN below
`C:\Program Files\OpenVPN`, or copy the installation to `<folder of plaitwayd.exe>\openvpn\bin`. Other reasons:
`openvpn not found at <path>`, `the TAP-Windows6 driver is not installed (<system>\drivers\tap0901.sys is missing);
install OpenVPN with its TAP-Windows6 driver`, `tapctl is not trusted: ...`. A trusted binary is checked again each
time a profile starts. `packaging\windows\openvpn\check-openvpn.ps1` prints what the checks look at, and changes
nothing.

**DNS is not in effect: group policy.** The Routes and DNS page shows `DNS is not in effect: a group policy defines
name resolution (NRPT) rules, and Windows applies those instead of the rules of local programs`. A group policy or
DirectAccess on the PC delivers NRPT rules, and Windows then ignores the rules of local programs. The daemon does not
write its rules in that case; rules written before the policy arrived are still removed. Only the administrator of
the policy can change this.

**A route is not in effect: another VPN.** A route of another program with a lower effective metric (route metric plus
interface metric), or one that is more specific, decides where the traffic goes. The route of Plaitway stays in the
table, is shown as failed with `overridden by <route>: effective metric <n>, ours <m>` or `overridden by more specific
routes, such as <route>`, and is judged again at every network change. Plaitway never lowers a metric to win and never
deletes a route it did not add. Disconnecting the other VPN puts the route in use at the next network event.

**No network after a route or adapter is left behind.** The Reconciler repairs its journal at the next start, and
`plaitwayd.exe uninstall` removes the NRPT rules left in the registry (`removed <n> DNS rules left in the registry`).
Adapters named `Plaitway-` are removed when the first one of the next run is made. `Run-DaemonElevated.ps1` and
`Run-ElevatedTests.ps1` list what they find and print the commands that remove it.

**A firewall prompt appears while the tests run.** A test binary listens on an address other than loopback.
`Test-LoopbackOnly.ps1` finds the package. Each **Allow** leaves rules for a binary that is deleted; they are listed
by `Get-NetFirewallApplicationFilter | Where-Object Program -like '*.test.exe'` and removed with `Remove-NetFirewallRule`
after a look at the list.

## Linux

The helper runs as root, as the systemd service `plaitwayd.service`. The app is a
GTK 4 and libadwaita window with a tray item ([linux/README.md](linux/README.md));
`plaitway` is the command line client. Tunnels are `tun` devices, routes go
through netlink, DNS through `resolvectl` (or `resolvconf` where there is no systemd-resolved),
and OpenVPN is the distribution's.

| Path | What |
|---|---|
| `/usr/libexec/plaitway/plaitwayd` | the helper; it is not a command, so it is not on `PATH` |
| `/usr/bin/plaitway`, `/usr/bin/plaitway-app` | the client and the app |
| `/usr/lib/systemd/system/plaitwayd.service` | the unit |
| `/var/lib/plaitway` | the profiles, with their keys, and the route journal; root only (0700) |
| `/run/plaitway` | `plaitwayd.sock` (mode 0666; each call is authorized by who makes it), the management sockets of openvpn and the configurations it is started with; removed when the service stops |
| the journal | the helper's log: `journalctl -u plaitwayd`. There is no log file |
| the Secret Service | the credentials the app saves (the user's keyring) |

`make deb` builds `build/linux/plaitway_<version>_<arch>.deb` and `make verify-deb`
checks it; [packaging/README.md](packaging/README.md#linux) describes the package, what it
depends on, and what installing, upgrading, removing and purging it do. An upgrade
restarts the service, which disconnects the connected profiles; removing the package
keeps `/var/lib/plaitway`, purging it deletes the profiles. Without the package,
`make linux-build && sudo make install` lays out the same files below `/usr/local`, and
`sudo scripts/linux/dev-install-daemon.sh` installs the helper you built as the
service (`dev-uninstall-daemon.sh` removes it). Both refuse to touch a unit that the
package owns.

**Routes.** Every route the helper adds is in the main table and has `proto 199`:
`ip route show proto 199` lists them. A tunnel's routes have metric 5; the route that
keeps a VPN server reachable outside the tunnels (through the physical router) has
metric 1; a full tunnel is `0.0.0.0/1` and `128.0.0.0/1` (`::/1` and `8000::/1`), so
the system's default route stays as it is. The helper writes each route to
`/var/lib/plaitway/journal` before it adds it, and a start after a crash repairs the
table from that record. On Linux the metric is part of a route's identity: the
kernel refuses a second route with the same destination and metric, so a route of
another program with the metric of ours is a conflict, and one with another metric
is a neighbour that may be the one in use. Then the route of ours stays, is marked
overridden with the winner named, and is looked at again at every network event. The
helper never lowers its metric to win and never deletes another program's route on
its own.

**DNS** is set per tunnel interface through `resolvectl` (servers, a routing domain,
the default-route flag), which systemd-resolved forgets when the interface goes away.
The helper writes nothing to `/etc/resolv.conf` and looks at it only to warn, once, when
it does not lead to systemd-resolved. Where there is no `resolvectl` and openresolv is
installed, it adds the servers of a full tunnel with `resolvconf -x -a plaitway:<owner>:<interface>`
and resolvconf writes the file: [Linux without systemd](#linux-without-systemd-gentoo-openrc).

**Other VPNs.** A VPN that routes by policy (wg-quick with a table and a fwmark,
Cloudflare WARP: a rule that sends all traffic without its mark to a table of its own)
is invisible to the helper, which neither reads nor changes `ip rule` or any table but
the main one. Its interface counts as a tunnel and the main table's default route stays
the physical one. A route of ours with a prefix length above zero is not suppressed by
its rule and wins against its catch-all, the halves of a default route included. A
tunnel's server that none of our routes captures gets no route of its own: its traffic
follows the other VPN's catch-all.

**Authorization** is described under Know what the helper may do. The socket is open
(0666) so that the app, run by any user, can reach it; the helper decides per call from
the caller's uid and groups.

### Linux without systemd (Gentoo, OpenRC)

The helper does not need systemd to run: it sends its readiness notice only when systemd
gives it a socket, and it takes its paths and its `openvpn` on the command line.
`packaging/linux/openrc/plaitwayd` is an OpenRC service with the arguments of the unit,
run by `supervise-daemon`. A crash or a kill is followed by a start three seconds later that
repairs the routes and the DNS entry, and the restarts have no limit: the DNS entry of a full
tunnel outlives the daemon that made it (see below), and only a daemon that runs again
deletes it. The service sets no new privileges, keeps the log in `/var/log/plaitwayd.log`
where only root reads it, and removes the run directory and the DNS entries of the daemon
when it stops. `--openrc` also installs `/etc/logrotate.d/plaitwayd`, which rotates the log
weekly, or at 20 MB, and keeps four compressed copies; the file is copied and emptied in
place, since a restart would end the tunnels. Build and install it from the source, with the Python directory of the
system's Python (Gentoo's does not look below `/usr/local`):

```sh
make linux-build
```

```sh
sudo packaging/linux/install.sh --prefix /usr --openrc --python-dir "$(python3 -c 'import sysconfig; print(sysconfig.get_path("purelib"))')"
```

```sh
sudo rc-update add plaitwayd default && sudo rc-service plaitwayd start
```

On Gentoo, `packaging/linux/gentoo` is a repository with a live ebuild of the head of `main`,
which builds the programs with the Go of the system, installs the OpenRC service (or the unit,
with the `systemd` flag), the logrotate file and the app, and depends on the packages below.
It fetches the Go modules while it unpacks, so it needs the network then:

```sh
printf '[plaitway]\nlocation = %s\n' "$PWD/packaging/linux/gentoo" | sudo tee /etc/portage/repos.conf/plaitway.conf
echo '=net-vpn/plaitway-9999 **' | sudo tee -a /etc/portage/package.accept_keywords/plaitway
sudo emerge net-vpn/plaitway
```

Building needs Go 1.27.1 (`dev-lang/go`) and a C compiler. The helper needs `/dev/net/tun`, the
distribution's `openvpn` 2.6 or later for OpenVPN profiles, and openresolv (`resolvconf`;
`net-dns/openresolv` on Gentoo) for DNS settings; the app needs GTK 4.14, libadwaita 1.5,
PyGObject, grpcio, protobuf and libsecret. What is not as with systemd:

- **DNS goes through `resolvconf` (openresolv), for a full tunnel only.** The entry of a
  full tunnel is added as the exclusive one: while it is up its servers are the only ones in
  `/etc/resolv.conf`, deleting it brings the others back, and a renewed DHCP lease does not
  take it away. A profile whose DNS is for some domains only (a split tunnel with search
  domains) cannot be set, because `resolv.conf` has one list of servers for every name: the
  profile's Routes and DNS page says "DNS for corp.example needs systemd-resolved", and
  those names go to the resolver the network gave. With neither systemd-resolved nor
  openresolv there are no DNS settings, and the log says so at start. The entries do not go
  away with the tunnel, as the links of systemd-resolved do: a daemon that was killed leaves
  its entry in `/run/resolvconf` until a daemon starts again, which deletes it, or the
  service is stopped, which does too. If a host has lost its DNS that way,
  `sudo resolvconf -i 'plaitway:*'` lists the entries and `sudo resolvconf -f -d NAME`
  deletes one. **NetworkManager** writes `/etc/resolv.conf` itself unless it is told to go
  through resolvconf, and then puts its own servers back whenever a connection changes;
  set `rc-manager=resolvconf` in the `[main]` section of a file in
  `/etc/NetworkManager/conf.d`, and the helper says at start when the file says
  NetworkManager made it
- **Console users.** The people at the console are read from the seats of systemd-logind
  in `/run/systemd/seats`. `elogind` keeps `seat0` there in the same format, with the user
  of the active session as `ACTIVE_UID`. Without the directory only administrators (root and the
  members of `sudo`, `wheel` or `admin`) can use the helper
- **No sandbox of the unit's kind.** The helper runs as root with no new privileges, and
  without the capability bounding set, the system call filter and the read-only file
  system that the unit adds. `supervise-daemon` could drop capabilities only by listing
  every other one
- **The app cannot start the helper.** On a system that does not run systemd it shows
  "Helper not running" with "Start plaitwayd, for example with rc-service plaitwayd start.",
  and no restart control. Start the helper with `rc-service plaitwayd start`, or at boot with
  `rc-update`

Run on Debian 13 in a booted container with OpenRC 0.56 as its init, openresolv 3.13.2 and
OpenVPN 2.6.14 (Debian's OpenRC has no `localmount` service, so a stand-in was used), a DHCP
client stood in for by `resolvconf -a lan0.dhcp`, and a peer namespace with a kernel WireGuard
peer, an OpenVPN server that pushes `redirect-gateway` and a DNS server. A WireGuard full
tunnel with `DNS =` put its server alone in `resolv.conf`, answered the names of a domain and
a name that does not exist at once, kept it through a renewed DHCP lease, and disconnecting
brought the DHCP server back. A split tunnel with a search domain was connected with its route
and its DNS entry reported as failed, with the reason above, and `resolv.conf` untouched. An
OpenVPN full tunnel did the same with the server it pushed. With two full tunnels there was
one entry, and it moved to the other when the holder went. Twenty rounds of a SIGKILL of the
helper with a full tunnel up: each time OpenRC started a new helper, which deleted the entry
the old one left, within 53 ms of `resolv.conf` being the DHCP server's again, with no
interface and no route; the `openvpn` child of a killed helper was gone. Stopping the service
with a tunnel up removed the entry and left no process, and with the supervisor and the
helper both killed, `rc-service plaitwayd stop` deleted the entry that was left. The rotation
of the log was run there too, with logrotate 3.22 and a stand-in for the daemon that writes a
line every 20 ms: the supervisor opens the log for appending, so a rotation while the daemon
runs leaves a compressed copy of everything before it, a live log that starts again small with
no zero bytes in it, the same daemon process, mode 0600 on both, a rotation at 21 MB without
waiting for the week, four copies kept, and no rotation of an empty or a missing log.

On Gentoo's own stage3 with Gentoo's own packages (see the next paragraphs for the first run
there), the ebuild was built and installed, and everything below was run against that
installation: OpenRC 0.63.3, openresolv 3.16.5, OpenVPN 2.7.5, dhcpcd 10.5.2, NetworkManager
1.56.1, logrotate 3.22.0, elogind 255.24, Go 1.27.1, Python 3.14.7, GTK 4.20.4, libadwaita 1.8,
grpcio 1.83.1 and protobuf 7.35.1 (binary packages of Gentoo where it has them, GTK and grpcio
compiled). The lab above passed all 49 checks, OpenVPN included, and the helper deleted the entry
within 2 ms after each of twenty SIGKILLs. It found that openresolv 3.16 says "No resolv.conf for
key" where 3.13 says "... for interface", so every listing with no match was an error and a full
tunnel got no DNS entry; the Debian run used 3.13 and could not have seen it. Gentoo's Go also
deletes `LICENSE` from `GOROOT`, which the generator of the third-party notices needed. A real
dhcpcd took a lease from a real DHCP server (dnsmasq); a rebind that changed its DNS server, a
new request and the release of the lease each left the tunnel's server alone in `resolv.conf`,
and disconnecting brought the renewed lease's server back. NetworkManager with its default
settings writes `resolv.conf` itself and replaced the tunnel's server when the connection went
down and up; with `rc-manager=resolvconf` the tunnel kept it through the same steps. The
rotation of the log was run on the real helper: a compressed copy of the lines before, a log that
starts small again with no zero bytes, the same process, mode 0600 on both. The app's 369 tests
pass under Xvfb on Gentoo's Python and GTK (the daemon of the tests is built as the development
version, as in the CI), and the installed app, run headless, shows "Helper not running" with
"Start plaitwayd, for example with rc-service plaitwayd start." while the service is stopped and
its profiles while it runs. With an `elogind` session of a user on `seat0` (made with the
`CreateSession` call of logind's bus API, as `pam_elogind` makes it; `seat0` then holds
`ACTIVE_UID`), that user could disconnect a profile and a user without a session could not,
and the user lost it again when the session ended.

The first run on Gentoo's stage3 (`amd64-openrc` of 2026-10-04, OpenRC 0.63.3, glibc 2.43), booted with
Gentoo's init and with the files of openresolv 3.13.2 added (the stage3 has neither it nor
`openvpn`), the WireGuard checks above gave the same results: the service starts at boot under
one supervisor, a full tunnel's server is alone in `resolv.conf` and survives a renewed lease,
a split tunnel's DNS entry is reported as failed, twenty SIGKILLs were each followed by a new
helper that deleted the entry (`resolv.conf` was the DHCP server's within 3 ms), and stopping
the service with the supervisor and the helper both killed deleted the entry. Without
openresolv the helper says at start that neither `resolvectl` nor `resolvconf` is installed
and what to install, and the DNS entry of a full tunnel is reported as failed with the same
words. `install.sh --openrc` laid out the files there and Gentoo's Python imported the
package.

### Troubleshooting on Linux

```bash
systemctl status plaitwayd
journalctl -u plaitwayd -b            # the helper's log since boot
plaitway diagnostics                  # the network, the routes it owns, the journal
plaitway resync                       # rebuild the routes and DNS entries it owns
ip route show proto 199               # the routes it added
resolvectl status                     # per-link DNS settings
```

- `plaitway` says `the daemon is not running: … does not exist; run "systemctl status plaitwayd"`
  when the socket is missing (`rc-service plaitwayd status` on OpenRC). The app shows a screen for each state (not installed, not
  running, not answering, refused, not trusted); **Start Helper** asks polkit.
- A profile that fails says why in its log and on its Overview. The helper's log at
  start lists which engines it can run and why not: without `/dev/net/tun` (`modprobe tun`)
  nothing can connect, and without an `openvpn` that root owns (with every directory
  above it) OpenVPN profiles are refused.
- `DNS settings of tunnels cannot be applied` in the log means `resolvectl` is missing
  or systemd-resolved does not answer; the routes still work.
- A route left behind by something else is listed under Diagnostics, Stale routes, and is
  removed only when you ask.

### What was run on Linux

Run on one machine, Ubuntu 26.04 (kernel 7.0, systemd 259, OpenVPN 2.7.0), in private user,
network and mount namespaces as the fake root of the namespace
(`scripts/linux/root-tests.sh`, `make linux-root-test`), with real tun devices, the
kernel's routing table and netlink, the kernel's WireGuard as the peer, a real `openvpn`
server and client, and, for the DNS test, a systemd-resolved of its own on a private bus:

- `internal/osnet/linux`: route writes and reads (IPv4 and IPv6, several routes to
  a prefix, deleting whatever the protocol, errors, events, lost messages), interface and
  default-route detection, link configuration, and DNS through a real systemd-resolved
  and `resolvectl`
- `internal/reconciler`: 28 kernel tests of the Reconciler on the real table (full and
  split tunnels, competition between tunnels, foreign routes that outrank or lose to
  ours, a foreign VPN that routes by policy, a gateway change, crash recovery from the
  journal, the table with many routes, announcements during network changes)
- `internal/wg`: 19 tests of the WireGuard engine with real tun devices against the
  kernel's WireGuard (handshake, traffic through the tunnel, rebind, IPv6, a missing
  `/dev/net/tun`, a tunnel device removed from outside)
- `internal/ovpn`: 13 tests of the OpenVPN engine against a real openvpn server (split
  and full tunnels, IPv6, two tunnels, credentials, a server restart, a changed tunnel
  network, a network change)
- `cmd/plaitwayd`: 4 tests of the daemon as a process (a WireGuard tunnel from start to
  SIGTERM, recovery after a SIGKILL, a failed DNS entry, no tun device)

The CI workflow (`.github/workflows/ci.yml`) runs the tests above on an Ubuntu runner,
builds and checks the package for amd64 and arm64, and runs the Python tests of the app
under a virtual display on Ubuntu 24.04 and 26.04. It ran on Actions for 0.4.0 and 0.4.1,
and its failures there found the OpenVPN 2.6 problem that 0.4.1 fixes.

The unit's sandbox settings were first checked in pieces, before the service could be run as root (the next section is the run as root). Three
of the four daemon tests (the fourth mounts a file system itself and needs a capability
of its own) and eight of the OpenVPN tests, with openvpn as server and as client, passed
with the daemon and openvpn started under the capability bounding set of the unit
(`CAP_NET_ADMIN` and `CAP_NET_BIND_SERVICE`; without `CAP_NET_ADMIN` the tunnel cannot be
made), `NoNewPrivileges`, the kernel's memory-deny-write-execute and a seccomp filter of
the system calls in `@system-service` that killed the process on any other call (the unit
makes such a call fail with EPERM instead). In the traced tests (the daemon's WireGuard
tunnel and two OpenVPN ones) the sockets opened were of the four families the unit allows,
and the paths written were the run and state directories and `/dev/net/tun`. The packaged
daemon (`-fake`) ran as a transient service of the user's
systemd with the unit's settings that a user manager can apply: systemd reported it started
after READY=1, its run and state directories had the modes of the unit, and it stopped
cleanly. A stand-in process showed what `KillMode=mixed` and `Restart=on-failure` do after
a kill, and that a process ignoring SIGTERM is killed when `TimeoutStopSec` runs out.
`ProtectKernelTunables` is not set because the helper writes
`/proc/sys/net/ipv6/conf/<tunnel>/disable_ipv6`.

### Run on the machine itself

Once, on the same Ubuntu 26.04 machine (a virtual machine, with NetworkManager,
systemd-resolved and a Cloudflare WARP tunnel of `wg-quick` running on it), as real root
in the initial namespaces:

- The package was installed with `apt` (it pulled `python3-grpcio` 1.51 and
  `python3-protobuf` 3.21), and `plaitwayd.service` ran under the system's systemd with the
  unit's sandbox: `NoNewPrivs`, the seccomp filter, the capability bounding set, the closed
  device policy and memory-deny-write-execute were all in effect, a WireGuard and an OpenVPN
  profile connected at once, and the journal and the kernel log show no denial. Installing
  again, removing, installing again and purging behaved as the maintainer scripts say: the
  profiles stayed until the purge. The Python tests of the app, and its client against the
  real socket (with the check that root owns it), pass on that grpcio and protobuf
- With the WARP tunnel up, the helper connected a WireGuard profile to the kernel's WireGuard
  and an OpenVPN profile to a real `openvpn` server, both in a namespace behind a veth pair.
  The routes had `proto 199` and metric 5, the DNS settings of both were written to
  systemd-resolved (a routing domain, answered through the tunnel), NetworkManager listed the
  device as `connected (externally)`, `ip rule` and the WARP routes did not change and the
  machine's own traffic kept going through WARP. Disconnecting, SIGTERM and SIGKILL left no
  interface, route or DNS setting, and openvpn did not outlive the helper
- With the WARP tunnel stopped for the time of the test, the WireGuard engine connected to
  Cloudflare with the WARP profile as a split tunnel to one address, and as a full tunnel:
  IPv4 and IPv6 traffic, name resolution through the tunnel's catch-all DNS entry and the
  server's bypass route behaved as described above. A SIGKILL during the full tunnel left only
  the bypass route, and the next start removed it from the journal
- In booted containers, the package under the systemd of Debian 13 (257, OpenVPN 2.6.14) and
  of Ubuntu 24.04 (255, OpenVPN 2.6.9 and 2.6.19) ran the same lab: a kernel WireGuard peer and
  an OpenVPN server in a namespace behind a veth pair, DNS answered through systemd-resolved,
  disconnecting and SIGKILL of the helper. On Ubuntu 24.04 the released package also ran a full
  tunnel from a server-pushed `redirect-gateway`, which OpenVPN 2.6 does not report to the
  helper in its environment (the engine reads it from the PUSH_REPLY since 0.4.1)

### Not verified on Linux

- arm64: `verify-deb`, which starts the packaged daemon with `-fake`, passes on the CI's
  arm64 runner; no tunnel, installed service or app has run on arm64
- The app in a desktop session: **Start Helper** answered by a person at the polkit prompt
  (it ran with a temporary rule), and the tray icon of the installed package (a checkout's
  icon was missing in a GNOME session and is fixed; the installed one is looked up by name in
  the system's icon theme and has not been looked at). The tests use a private bus with a
  watcher of their own, see [linux/README.md](linux/README.md)
- Suspend and resume, systemd-networkd, a profile with a server behind a captive portal,
  auto-connect at boot, and running beside the distribution's own WireGuard and OpenVPN
  clients other than `wg-quick`

### Known limitations on Linux

- DNS settings for some domains only need systemd-resolved and `resolvectl`. Without it,
  as on OpenRC, openresolv's `resolvconf` sets the servers of a full tunnel and nothing
  else: a profile whose DNS is for a search domain only gets its DNS entry reported as
  failed, and there is no backend that writes `/etc/resolv.conf` itself
- Only the main routing table is read and written. Policy routing (`ip rule`) and other
  tables are neither read nor changed, so a program that sends traffic around the main
  table cannot be seen
- OpenVPN profiles with `dev tap` are refused: the helper binds every tunnel route to its
  device without a next hop, and over a tap device (an Ethernet link) a destination behind
  the server would be looked for with ARP and go nowhere
- IPv6 through a tunnel needs IPv6 on the tunnel device. The helper turns it on for a
  device that the host's default created with it off, and logs when it cannot
- Another VPN's catch-all DNS routing domain (`~.`) on its own link competes with ours;
  the helper does not look at links it does not own, and which of them answers is up to
  systemd-resolved
- The tray item needs a StatusNotifierWatcher; stock GNOME has none (an AppIndicator
  extension provides one: Ubuntu's session ships it, on Debian it is
  `gnome-shell-extension-appindicator`). Without it closing the window quits the app,
  and the tunnels stay up

### Development on Linux

The helper runs without root on the in-memory backend, as on macOS
([linux/README.md](linux/README.md) has the commands for the app). Tests:

```bash
make linux-test           # Go tests, then the Python tests of linux/
make linux-root-test      # the tests that change routes, links and DNS, in private namespaces
make test-packaging-linux # the packaging scripts against fakes
make deb && make verify-deb
```

`make linux-root-test` runs `scripts/linux/root-tests.sh`. By hand, for one package:

```bash
go test -c -tags rootintegration -o /tmp/ovpn.test ./internal/ovpn
unshare -Urnm sh -c 'mount -t tmpfs none /run; ip link set lo up; PLAITWAY_ROOT_TESTS=1 /tmp/ovpn.test -test.v -test.run ^TestRoot'
```

The tests refuse to run outside a user namespace of their own and a network namespace with
nothing in it but loopback, so they cannot reach the machine's network. They need
`ip`, `ping`, `wg` and the distribution's `openvpn`; on Ubuntu 24.04 and later,
`sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0` first. The DNS test of
`internal/osnet/linux` runs alone (`-test.run ^TestResolvedEndToEnd$`), and the tests of
`internal/reconciler` are `^TestKernel`.

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
- Windows: unsigned, the MSI has not been installed on a clean machine, and the OpenVPN engine's tests
  that need an elevated shell have not been run; see [Docs/windows-status.md](Docs/windows-status.md) for the full list
- Linux: the limits are listed under [Known limitations on Linux](#known-limitations-on-linux)

<p>
  <img alt="Go" src="https://img.shields.io/badge/GO-1.27-00ADD8?style=for-the-badge&logo=go&logoColor=white">
  <img alt="SwiftUI" src="https://img.shields.io/badge/SWIFTUI-0071E3?style=for-the-badge&logo=swift&logoColor=white">
  <a href="https://github.com/grpc/grpc-swift-2"><img alt="gRPC" src="https://img.shields.io/badge/GRPC-244C5A?style=for-the-badge&logo=grpc&logoColor=white"></a>
  <a href="https://git.zx2c4.com/wireguard-go/"><img alt="wireguard-go" src="https://img.shields.io/badge/WIREGUARD--GO-88171A?style=for-the-badge&logo=wireguard&logoColor=white"></a>
  <a href="https://openvpn.net/community/"><img alt="OpenVPN" src="https://img.shields.io/badge/OPENVPN-2.7-EA7E20?style=for-the-badge&logo=openvpn&logoColor=white"></a>
  <a href="LICENSE"><img alt="License: MIT" src="https://img.shields.io/badge/LICENSE-MIT-2196F3?style=for-the-badge&logo=github"></a>
</p>

## License

[MIT](LICENSE) © KoukeNeko

Third-party components keep their own terms. The bundled ones, including the
GPL source offer for OpenVPN, LZO and LZ4, are listed in
[packaging/THIRD_PARTY_NOTICES.md](packaging/THIRD_PARTY_NOTICES.md). The Linux
package bundles no OpenVPN; its notices list the Go modules in the two programs
(`go run ./packaging/notices -platform linux`).

### Trademarks

OpenVPN is a registered trademark of OpenVPN Inc. WireGuard is a registered
trademark of Jason A. Donenfeld. Apple, macOS and Keychain are trademarks of
Apple Inc. Linux is the registered trademark of Linus Torvalds. Plaitway is not
affiliated with or endorsed by any of them; the names are used only to say which
protocols and which systems it works with.
