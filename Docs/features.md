# What Plaitway does

The details behind the [README](../README.md): the windows, several profiles at once, routes and DNS, editing, on-demand activation, the helper's permissions.

## What it looks like

The sections down to Compatibility describe the macOS app; [Linux](technical-reference.md#linux) says what differs there.

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
helper further; [Linux](technical-reference.md#linux) lists how.

## In your language

English and 繁體中文, following the system. On Windows, **Settings › Language** chooses one, and **Restart Plaitway**
loads it. Messages that come from the helper itself, such as why a connection is stuck, are in English.
