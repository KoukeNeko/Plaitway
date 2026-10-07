# Packaging

Build, sign, verify and install Plaitway.app. Everything here targets Apple silicon (arm64 only) and macOS 15
or later.

| Path | Purpose |
|---|---|
| `build-openvpn.sh`, `openvpn-deps.env` | builds the bundled OpenVPN and its static libraries from pinned, checksummed sources |
| `package-app.sh` | assembles and signs `build/Plaitway.app` |
| `make-dist.sh` | `build/Plaitway-<version>.zip` and `.dmg` from the app, each checked after it is made |
| `notarize.sh` | notarizes and staples the app, then rebuilds the zip and dmg around it |
| `notices/` | generates `THIRD_PARTY_NOTICES.md`, including the GPL source offer |
| `lib.sh` | names, paths and helpers shared by the scripts here and in `scripts/` |
| `lib_test.sh` | tests of the scripts against fakes (`make test-packaging`) |
| `smoketest/` | calls `GetDaemonInfo` on a daemon socket; used by `scripts/verify-bundle.sh` |
| `../scripts/verify-bundle.sh` | acceptance test of the finished bundle and its zip and dmg (`make verify`) |
| `../scripts/dev-install-daemon.sh`, `dev-uninstall-daemon.sh` | the helper as a plain LaunchDaemon, without SMAppService |

## Build

```sh
make app      # openvpn (only when its recipe changed), the Swift app, plaitwayd, plaitway, signing
make dist     # a fresh `make app`, then the zip and the dmg
make verify   # scripts/verify-bundle.sh
make test-packaging
```

`make app` and `make dist` always assemble and sign the bundle again. `package-app.sh` deletes the zip and dmg of
the app it replaces, so a dist file never outlives its build.

| Variable | Effect |
|---|---|
| `PLAITWAY_SIGN_IDENTITY` | signing identity; default is the Developer ID certificate, by SHA-1 because two certificates share its name; `-` signs ad hoc |
| `PLAITWAY_ALLOW_NO_TIMESTAMP=1` | sign without a secure timestamp when Apple's timestamp server is unreachable; without it that is an error, because notarization rejects such a signature |
| `PLAITWAY_OPENVPN_WORK` | work directory of `build-openvpn.sh` (default `build/openvpn-work`); the result does not depend on it |
| `PLAITWAY_SOURCE_CONTACT` | where the written source offer says to ask (default: the web address of the `go.mod` module path) |
| `PLAITWAY_NOTARY_PROFILE` | notarytool keychain profile for `notarize.sh` (default `plaitway-notary`) |

`make dist` output is not notarized. To notarize: `make app`, then `packaging/notarize.sh`, which builds the zip and
dmg from the stapled app. Build 0.1.0 was notarized on 2026-10-07: the app and the dmg are stapled and `spctl` reports
"Notarized Developer ID"; the zip holds the stapled app but is not itself notarized.

### Bundle

```
Plaitway.app/Contents
  MacOS/Plaitway            the app
  MacOS/plaitwayd           the daemon, run by launchd as root
  Resources/bin/openvpn     the bundled OpenVPN
  Resources/bin/plaitway    the command line client
  Resources/Source/         source archives and build scripts of OpenVPN, LZO and LZ4
  Resources/THIRD_PARTY_NOTICES.md, Credits.rtf
  Library/LaunchDaemons/io.github.koukeneko.plaitway.daemon.plist
  Frameworks/               Swift libraries the OS does not ship
```

`plaitway` is not installed anywhere. Run it by its full path, or link it:
`ln -s /Applications/Plaitway.app/Contents/Resources/bin/plaitway /usr/local/bin/plaitway`.

### Signing order and the openvpn hash

The daemon runs only an openvpn whose SHA-256 it was built with: it copies the file into its root-owned run
directory, hashes the bytes while copying, and keeps the copy only on a match. So the hash is of the final
signed bytes:

1. `build-openvpn.sh` builds and signs `build/openvpn/bin/openvpn`.
2. `package-app.sh` copies it into the bundle and signs it again; this is the last write to the file.
3. The SHA-256 of that file goes into the daemon: `-X main.openvpnSHA256=<hex>`.
4. The daemon and the client are built and signed, then the bundle is sealed.

`scripts/verify-bundle.sh` checks that the daemon carries the hash of the bundled file. A daemon built without it
runs openvpn from the configured path as it is (development builds).

### Bundled OpenVPN

OpenVPN 2.7.7 with OpenSSL, LZO and LZ4 linked statically. It runs as root on untrusted profiles, so:

- every library and openvpn are configured for the prefix `/var/empty` and installed with `DESTDIR` into the work
  directory. The binary names `/var/empty` as the place for helper scripts, plug-ins, OpenSSL modules and
  `openssl.cnf`, which nobody but root can create; no path of the build machine is in it. `build-openvpn.sh` and
  `verify-bundle.sh` fail when a binary contains `/Users/`, `/private/tmp` or `/var/folders`.
- OpenSSL is built with `no-dso`, which removes loading providers from shared libraries; `no-module` and `no-engine`
  only cover engines. The hardened runtime's library validation is the second layer.
- `--disable-plugins` removes the `plugin` directive; `--disable-dns-updown-by-default` stops the dns-updown helper
  from running unless a profile asks for it. The daemon also passes `--dns-updown disable`.

`make openvpn` rebuilds only when `build-openvpn.sh`, `lib.sh` or `openvpn-deps.env` changed (about 90 s). The source
archives are cached in `build/openvpn-work/src` and verified against `openvpn-deps.env` on every run.

### launchd job

The daemon plist sets `ExitTimeOut` to 20 seconds (`DAEMON_EXIT_TIMEOUT` in `lib.sh`). Where the key is missing
launchd waits only 5 seconds before SIGKILL, which is less than the daemon needs to remove its routes and DNS
entries. The daemon's own budget (`serverStopTimeout` plus `shutdownTimeout` in `cmd/plaitwayd/daemon.go`, 17
seconds) must stay below it; `make test-packaging` fails otherwise. There is no `StandardErrorPath`: launchd fails a
job whose log directory is missing, so the daemon creates its log directory itself.

## Install

SMAppService (the app's Install Helper) needs the app in `/Applications` and the approval in System Settings, General,
Login Items & Extensions. Without that approval, `sudo scripts/dev-install-daemon.sh` installs the daemon as a
LaunchDaemon from a root-owned copy of the bundle in `/Library/PrivilegedHelperTools`, then waits until the daemon
runs and its socket exists, and prints the end of the log when it does not. It refuses to replace a job that the app
registered.

## Upgrade

- Registered by the app: replace the app, then Reinstall Helper. Connected profiles disconnect.
- Installed by the script: replace the app, then run `sudo scripts/dev-install-daemon.sh` again. The app does not see
  this install, so it offers no Reinstall for it.

## Logs

The daemon logs to `/Library/Logs/Plaitway/plaitwayd.log`. The file is readable by root only because it names
endpoints and users; at start-up a file above 10 MB moves to `plaitwayd.log.1`.

```sh
sudo tail -n 100 /Library/Logs/Plaitway/plaitwayd.log
launchctl print system/io.github.koukeneko.plaitway.daemon      # state, last exit status, exit timeout
```

A helper that is registered but never answers shows as Helper unavailable. launchd restarts a daemon that exits at
start-up every 10 seconds, and the cause is in this log. Failures before the daemon's `main` runs (a rejected
signature, a missing binary) are not in it; `launchctl print` shows them.

## Uninstall

What exists after an install:

| Location | Content | Owner |
|---|---|---|
| `/Library/Application Support/Plaitway` | stored profiles with their inline private keys and certificates, the route journal | root, mode 0700 |
| `/Library/Logs/Plaitway` | daemon log | root |
| `/var/run/plaitway` | socket, openvpn's management sockets, the verified openvpn copy, generated configs | root; recreated at every start, cleared by a reboot |
| login Keychain, service `io.github.koukeneko.plaitway.credentials` | saved profile credentials | your user |
| `/Library/LaunchDaemons/io.github.koukeneko.plaitway.daemon.plist`, `/Library/PrivilegedHelperTools/Plaitway.app` | the script install only | root |

Remove everything, in this order:

1. In Plaitway, delete the profiles you do not want to keep. This also deletes their saved credentials from the
   Keychain.
2. Uninstall Helper (General). The daemon removes its routes and DNS entries and macOS removes the registration.
   Installed by the script instead: `sudo scripts/dev-uninstall-daemon.sh`.
3. Quit Plaitway and move `Plaitway.app` to the Trash. Trashing the app before step 2 can leave a root daemon
   registered with no app to stop it.
4. Delete the data the helper left: `sudo scripts/dev-uninstall-daemon.sh --purge`, or without the script
   `sudo rm -rf "/Library/Application Support/Plaitway" /Library/Logs/Plaitway /var/run/plaitway`.
5. Delete any remaining Keychain items of the service above in Keychain Access.

The uninstall script keeps the profiles and logs unless `--purge` is given: the profiles hold private keys that
cannot be recovered, and the daemon cannot tell an uninstall from an upgrade. `--purge` refuses to run while a job
that the app registered is loaded, because it would remove the state of a running daemon. It does not touch the
Keychain or the app's preferences, which belong to the user, not to root.

Deleted files are not overwritten, and Time Machine backups made earlier keep their copies. If the keys must not
outlive the machine, revoke them on the VPN server.

## Licenses

OpenVPN and LZO are GPLv2. The source archives of OpenVPN, LZO and LZ4 and the scripts that build the bundled
openvpn (`build-openvpn.sh`, `lib.sh`, `openvpn-deps.env`) ship in `Contents/Resources/Source`, which is GPLv2
section 3(a). `THIRD_PARTY_NOTICES.md` repeats this as a written offer (section 3(b)), lists every bundled component with
its license text, and carries the OpenVPN and WireGuard trademark notice. `Credits.rtf` makes the About panel point to
both. `notices/` generates the file during `make app`.

Plaitway's own code has no license: the repository has no `LICENSE` file, and the notices say nothing about it. That
is the owner's decision and must be made before anyone else receives the app. The same goes for the copyright line in
`Info.plist` (a GitHub handle today) and for the address in the written offer, which is derived from the `go.mod`
module path and must be a place that serves the source for three years.

BoringSSL, which swift-nio-ssl and swift-crypto compile into the app, has no license file in either package, so its
license text is not in the notices. Add it from the BoringSSL repository at the commit named in each package's
`hash.txt`.

## Checks and limits

`make verify` checks structure, plists (including `ExitTimeOut`), every Mach-O (arm64, minimum OS, signature, hardened
runtime, no entitlement that weakens it, no build path), the notices and the shipped source, the bundled openvpn
against a profile in the shape of the ASUS router's, the hash the daemon carries, the packaged daemon (`-fake`) with the
packaged `plaitway`, and the zip and dmg against the app.

Not covered because it needs root, an approval or a service: running as a registered helper (SMAppService,
BTM, launchd honouring `ExitTimeOut` for that registration), Gatekeeper on a quarantined, downloaded copy,
the real engines on a real network, the daemon's start of openvpn from its verified copy, and the About panel.
`verify-bundle.sh` starts the daemon with `-fake` only: the real daemon removes resolver entries that carry its
marker when it starts, which is not something to do on a machine in use. A bundle for release should be run once on
a spare Mac or a macOS 15 VM.

The development and the production build share the bundle identifier and the daemon label. Two copies of the app on one
Mac, or a script install next to an app registration, compete for one launchd label.
