# OpenVPN on Windows

The OpenVPN engine (`internal/ovpn`) runs the official `openvpn.exe` under
`plaitwayd`, which is LocalSystem. This page is what that asks of an
installation, what the engine does on the machine, and how it differs from the
macOS engine. The findings are from OpenVPN 2.7.1 on Windows 11 (build 26300);
`go test ./internal/ovpn` repeats the ones that need no adapter.

## What an installation needs

| | |
|---|---|
| `openvpn.exe` | An absolute path, signed by OpenVPN Inc. (Authenticode, embedded) |
| `tapctl.exe` | In the same folder, signed the same way. Makes and removes adapters |
| The libraries `openvpn.exe` loads | In the same folder (`libcrypto`, `libssl`, `vcruntime140`, `libpkcs11-helper`) |
| The TAP-Windows6 driver | `%SystemRoot%\System32\drivers\tap0901.sys` |

The folder, every folder above it and the files must be owned by SYSTEM,
Administrators or TrustedInstaller, and must not let any other account write,
delete or take ownership. The standard OpenVPN installer under
`C:\Program Files\OpenVPN\bin` satisfies this; a profile folder, a temporary
folder, a share or a removable drive does not.

`ovpn.Config.Binary` must be that absolute path. A bare `openvpn.exe` is refused:
it would be looked up in folders anyone may write.

## Where the daemon looks

`plaitwayd -openvpn <path>` names the binary. Without it the daemon takes the
first of these that exists, and otherwise the last, so that the reason it gives
names the place to install OpenVPN:

1. `<folder of plaitwayd.exe>\openvpn\bin\openvpn.exe`, the copy the installer of
   Plaitway ships;
2. `C:\Program Files\OpenVPN\bin\openvpn.exe`, the official installation (the
   Program Files folder is asked of the shell, not read from `%ProgramFiles%`).

The path is only a choice. The checks below decide whether the daemon runs it,
every time it probes or starts the binary, so an OpenVPN installed after the
daemon started is picked up without a restart. A binary the daemon does not
trust is reported as `openvpn is not trusted: <reason>` in `GetDaemonInfo`, in
the log (`engine unavailable`) and as the last error of the profiles; the
daemon starts regardless.

## What the daemon checks, and when

`ovpn.VerifyBinary(path, sha256)` and the engine itself, before every `--version`
and every start of the process:

1. the file is opened without write sharing and stays open until the process
   runs, so it cannot be swapped between the check and the start;
2. `fsperm.CheckAdminOnlyPath`: owner and access list of the file and of every
   folder above it (`internal/fsperm`);
3. a valid embedded Authenticode signature (`internal/authenticode`, shared with
   the WireGuard engine: `WinVerifyTrust`, no revocation lookup because the
   service may start before the network is up, an expired certificate accepted
   when the signature has a trusted timestamp) issued to `OpenVPN Inc.`;
4. when `Config.BinarySHA256` is set, the SHA-256 of the file.

A binary that fails is not run, and the engine reports why in `Probe().Detail`
(`openvpn is not trusted: ...`). The libraries beside the program are not signed
one by one: a folder that only administrators can write holds only what they
put there.

Unlike macOS the binary is not copied into the run directory. `openvpn.exe`
loads its libraries from its own folder, so a copy of the program alone does not
run, and a copy of all of them into a protected folder is the protected folder
that is already required.

### Pinning a binary

A daemon built for one OpenVPN carries its SHA-256:

```powershell
$hash = (Get-FileHash 'C:\Program Files\Plaitway\openvpn\openvpn.exe' -Algorithm SHA256).Hash.ToLower()
go build -ldflags "-X main.openvpnSHA256=$hash" -o bin\plaitwayd.exe .\cmd\plaitwayd
```

`ovpn.Config.BinarySHA256` takes the value. The check is in addition to the
others, not instead of them.

[check-openvpn.ps1](check-openvpn.ps1) prints what the checks look at for a
machine, without changing anything.

## The management interface

`openvpn.exe` for Windows accepts neither a Unix socket (`MANAGEMENT: this
platform does not support unix domain sockets`) nor a named pipe. The engine
uses a TCP port on `127.0.0.1`:

| | |
|---|---|
| Options | `--management 127.0.0.1 0 <password file>` |
| Port | Port `0` makes openvpn choose, and it says which on its output (`MANAGEMENT: TCP Socket listening on [AF_INET]127.0.0.1:N`). Nobody can be given the number beforehand |
| Password | 256 random bits in hex, in a file of the engine's private folder, created for the engine only. openvpn demands it on connect |
| Peer | Before the password is sent, `GetExtendedTcpTable` says which process owns the other end of the connection; it must be the child. A process that took the port first would otherwise be given the password and could report routes and DNS servers as if openvpn pushed them |
| Commands | One at a time, 50 ms after the last reply (see below) |

Every local user can connect to the port. The password keeps them out of the
interface; the peer check keeps the engine from handing it to them. What a local
user can still do is connect before the engine and so keep it waiting: the engine
then fails with an error for the missing prompt, and nothing leaks.

### openvpn loses commands that follow a reply too closely

Found with the real binary: of commands written one after the other, or each
sent as soon as the reply to the last was read, some are never answered, and the
log shows the earlier command again in their place. With a pause after the reply:

| Pause after the reply | Runs with all four of `state on`, `bytecount 2`, `log on`, `hold release` answered |
|---|---|
| 0 ms | 1 of 5 |
| 10 ms | 5 of 5 |
| 25 ms | 5 of 5 |
| 50 ms, 100 ms | 5 of 5 |

A lost `hold release` leaves the tunnel waiting for ever, and a lost `password`
leaves it waiting for the credential. The connection therefore keeps one command
in flight, waits 50 ms after each reply and gives up on a reply after 10 s
(`mgmtConn` in `internal/ovpn/mgmt.go`). The macOS connection is a plain
pipeline, as before.

The reply to the management password counts as a reply. Without a pause after it,
the 19th run of a series, on a machine with all its cores busy, got
`ERROR: unknown command [<the password>]` for its first command: openvpn took
the line it had just accepted for a command, and dropped the real one. The
engine then had no state notices and the tunnel never reached Up. The first
command now waits 50 ms too, and the engine ends the session when openvpn
refuses `state`, `log` or `hold` (`bytecount` only costs the byte counts), with
the password redacted from the error, which quotes it. With the pause, 600 runs in
a row on the same busy machine all came up.

openvpn also does not answer a second `hold release` while it asks for
credentials, so the engine sends one release at the start, not the two (one for
the command loop and one for the `>HOLD` notice) that the pipeline can afford.

## What openvpn is kept from doing

The engine owns routes and DNS (the Reconciler) and the address of the adapter
(`internal/winiface`). On top of the options of every system, the Windows command
line has:

| Option | Why |
|---|---|
| `--ifconfig-noexec`, `--ip-win32 manual` | No `netsh`, no IP Helper calls, no DHCP emulation by the driver. In 2.7.1 `--ifconfig-noexec` alone already turns into `ip_win32_type = 0` (manual); both are kept so that neither can be undone by the other |
| `--pull-filter ignore ip-win32` | A server may push `ip-win32`, which would pick another method |
| `--pull-filter ignore block-outside-dns` | It installs Windows Filtering Platform rules; the firewall is not openvpn's |
| `--pull-filter ignore "dns "`, `... dhcp-option` | openvpn applies pushed DNS options to the adapter through the OpenVPN Interactive Service (`Setting NRPT DNS ... using service`). Started by the daemon it finds no service and logs `DNS: could not talk to service`, but the daemon owns DNS and should not rely on that. The engine reads the options from the `PUSH_REPLY` line, which openvpn logs before any filter |
| | All four filters come before the profile, because the first filter that matches decides |
| `--route-noexec`, `--dns-updown disable` | As on macOS |
| `--disable-dco` | See the driver |
| `--dev-node <adapter>` | The adapter the engine made. Without it openvpn takes the first adapter it finds, which is one of the owner's |
| not passed: `--service`, `--msg-channel` | The OpenVPN Interactive Service is never involved |

`register-dns`, `block-outside-dns`, `ip-win32`, `dev-node`, `windows-driver`
and the like cannot be set by a profile: `directives.go` removes them.

## The driver

OpenVPN 2.7 removed Wintun (`DEPRECATED OPTION: windows-driver: In OpenVPN 2.7,
the default Windows driver is ovpn-dco. If incompatible options are used,
OpenVPN will fall back to tap-windows6. Wintun support has been removed`). The
engine therefore runs every tunnel on **tap-windows6** with `--disable-dco`, as
the macOS engine already passes `--disable-dco`.

ovpn-dco (data channel offload) was not chosen. openvpn takes it only when the
profile allows it (it logs "disabling data channel offload" for a profile with
compression, and the manual says it falls back to tap-windows6 when options are
incompatible), so the adapter the engine makes would have to match a choice it
cannot see beforehand: `tapctl create --hwid ovpn-dco` for one kind, the default
for the other. A profile like the ASUS one (`comp-lzo`, `AES-128-CBC`) never
qualifies. It is the one change that would make some tunnels faster. No test
that ran without administrator rights opened a DCO adapter or a tap adapter; the
tests that do are listed under Tests.

## The adapter

| | |
|---|---|
| Name | `Plaitway-ovpn-` and 8 hex digits of the SHA-256 of the profile id: the same profile has the same name every time |
| Made by | `tapctl create --name <name>` (default hardware id: tap-windows6), in the engine's `acquire`, before openvpn starts. Needs administrator rights |
| Configured by | `winiface`, when openvpn reports the tunnel up: MTU of each family the tunnel has an address in (IPv6 only from 1280), the addresses, interface metric 5 (as the WireGuard engine), then a wait for the link. With topology `net30` or `p2p` openvpn reports a peer and no netmask; the address gets the smallest subnet that holds both |
| Removed by | `tapctl delete <name>` after openvpn has exited and the owner is withdrawn |
| Leftovers | The first adapter of the process removes every interface named `Plaitway-ovpn-` + 8 hex digits, which a daemon that crashed left behind. Nothing else is removed |

The adapters of an OpenVPN installation (`OpenVPN TAP-Windows6`, `OpenVPN Data
Channel Offload`, the services and the GUI) are never named, opened or deleted.

## Process

`openvpn.exe` is started with `CREATE_NO_WINDOW | CREATE_SUSPENDED` in the
engine's private folder with the environment `SystemRoot`, `windir` and
`PATH=<System32>;<Windows>`, put in a job object with
`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` and then resumed. The child is in the job
before it runs an instruction, and a daemon that is killed without a chance to
clean up takes openvpn with it (the job's last handle closes with the process).

Stop asks for `signal SIGTERM` through the management interface and waits
`stopGrace`; then, or at once when the interface cannot be reached, the job is
terminated.

## Differences from macOS

| | macOS | Windows |
|---|---|---|
| Management | Unix socket in the engine's folder (mode 0700) | Loopback TCP, password, peer process check |
| Commands | Pipelined | One at a time, 50 ms apart |
| Start-up hold | Released twice | Released once |
| Tunnel interface | Made by openvpn (`utun`) | Made by the engine (`tapctl`), named, removed at the end |
| Address | Set by openvpn | Set by the engine, `ifconfig-noexec` |
| Pushed `dhcp-option` DNS and domains | In the environment of the up event | Not there: read from the `PUSH_REPLY` in the log, after the profile's filters |
| Binary trust | Hash checked, copy run from the root-owned run folder | Admin-only folders, Authenticode, optional hash; run where it is |
| Process | pid, `SIGTERM` | Job object, management `SIGTERM`, then terminate the job |
| Refused connection | `TCP: connect to ... failed: Connection refused` at once | None: Windows sends the refusal after about two seconds, and openvpn stays in `TCP_CONNECT` for its `connect-timeout`. The status says `no response from the server` |
| Driver | utun | tap-windows6 |

## Tests

Run in a normal session, they use the installed OpenVPN with `--dev null` (no
adapter) against a server started by the test: the management handshake, states,
credentials, a rejected password, the pushed routes and DNS, a network change,
Stop, and a server that does not answer. The trust checks run against the
installation itself and against copies of it.

Tests that make adapters are written for an elevated shell and skip otherwise.
They name only `Plaitway-test-ovpn-*` adapters, use 198.51.100.0/24,
203.0.113.0/24, 192.0.2.53 and `corp.invalid`, push no `redirect-gateway`, and
remove their adapters before, after and on failure.

**Disconnect your own OpenVPN before you run them.** The TAP driver is shared
with the OpenVPN GUI and its services, and a connection that is up has routes
and DNS of its own. A root test refuses to run while an `openvpn.exe` is
running whose command line is not the tests' (a `--dev-node Plaitway-test-ovpn-*`
client, or their loopback server), or whose command line cannot be read, and
says which process it is. `PLAITWAY_ROOT_ALLOW_OWNER_OPENVPN=1` runs them next
to it anyway.

A test that is killed (Ctrl+C, `-timeout`) does not run its clean-up. Before the
first adapter, each root test starts a guard: this test binary again, without a
console and in a process group of its own (`internal/winiface/rootguard`). The
guard removes every adapter named `Plaitway-test-ovpn-*` when the test process
ends without having released it, or after ten minutes, and writes what it did to
`plaitway-rootguard-tap-adapters-<pid>.log` in the folder of temporary files.
The test prints the command that does the same by hand:

```powershell
Get-NetAdapter -Name 'Plaitway-test-ovpn-*' | ForEach-Object { & 'C:\Program Files\OpenVPN\bin\tapctl.exe' delete $_.Name }
```

Run them:

```powershell
$env:PLAITWAY_ROOT_TESTS = '1'
go test -count=1 -tags rootintegration -run '^TestRoot' -v ./internal/ovpn
```
