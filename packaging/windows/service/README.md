# Plaitway Helper service

`plaitwayd` runs on Windows as a service. The same binary is the service, the
installer of the service and the console daemon: `plaitwayd.exe` with the
daemon's flags runs in the console as before, the service manager starts it
without flags, and `plaitwayd.exe install` registers it.

## Registration

| Setting | Value | Why |
|---|---|---|
| Name | `PlaitwayHelper` | |
| Display name | `Plaitway Helper` | |
| Description | Runs the VPN tunnels of Plaitway and manages their routes and DNS entries with administrator rights. | The wording of the macOS helper |
| Account | `LocalSystem` | Adapters, routes and DNS need it |
| Service type | Own process | |
| Start type | Automatic, not delayed | A VPN has to be up before anybody logs on, and its auto-connect profiles with it. A delayed start waits about two minutes after boot |
| Command line | `"<path>\plaitwayd.exe"`, no flags | The defaults of the daemon are the production ones |
| Dependencies | `Nsi`, `Tcpip` | The network stack is up before the first tunnel |
| Service SID | Unrestricted (`NT SERVICE\PlaitwayHelper` is in the token) | Files can be granted to this service alone |
| Failure actions | Restart after 5 s, 10 s, 30 s; the last one repeats | |
| Failure count reset | After one day without a failure | |
| Failure actions on non-crash failures | Yes | The daemon exits with a non-zero code when it fails on its own |
| Preshutdown timeout | 30 s | Longer than the 17 s the daemon needs to remove routes and DNS entries (`serverStopTimeout` + `shutdownTimeout` in `cmd/plaitwayd/daemon.go`); the Windows default is 3 minutes |

Access list of the service
(SDDL `D:(A;;CCLCSWRPWPDTLOCRRC;;;SY)(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;BA)(A;;LCLORC;;;IU)`):

| Who | May |
|---|---|
| Interactive users | Read the state (`LC`), interrogate (`LO`), read this list (`RC`) |
| SYSTEM | Query, start, stop, pause and interrogate. Not change the configuration, delete, or change this list |
| Administrators | Everything |

So the app can show running, stopped or not installed without elevation, and
only SYSTEM and Administrators control the service.

## Commands

All of them are `plaitwayd.exe <command>`. Errors are one line on stderr,
`plaitwayd <command>: <reason>`.

| Command | Needs elevation | What it does |
|---|---|---|
| `install [-start] [-update]` | Yes | Registers the service. `-start` starts it. `-update` replaces the registration of an installed service |
| `uninstall [-purge]` | Yes | Stops the service, waits for it, deletes it. `-purge` also deletes `%ProgramData%\Plaitway` |
| `start` | Yes | Starts the service and waits until it runs and stays running |
| `stop` | Yes | Stops the service and waits |
| `status` | No | Prints `PlaitwayHelper: running`, `stopped`, `start pending`, `stop pending` or `not installed` |

| Exit code | Meaning |
|---|---|
| 0 | Done. For `status`: running |
| 1 | The operation failed |
| 2 | Wrong arguments |
| 3 | `status`: installed, not running |
| 4 | `status`: not installed |

`install` refuses, before it changes anything:

- when the shell is not elevated (`run from an elevated shell`);
- when `plaitwayd.exe`, or any directory above it, can be changed by an
  account other than SYSTEM, Administrators and TrustedInstaller: Program Files
  passes, a profile or a temporary directory does not. The directory of the
  executable is held to the same standard as the file, since a library next to
  it is loaded by it; the directories above it only have to stay where they
  are. A junction or symbolic link on the way, a network share and a removable
  drive are refused too;
- when the service is installed already and `-update` was not given.

`install` does not copy the executable. It registers the one it runs from, so
the installer puts the files in Program Files first.

A failed `install` removes the service it created, so the command can be run
again.

## Update

A running executable cannot be replaced on Windows, so an installer does:

1. `plaitwayd.exe stop`
2. replace the files
3. `plaitwayd.exe install -update -start` (from the new executable)

`install -update` on its own does the same inside: it stops the service if it
runs, sets the registration of the executable it runs from, and starts the
service again if it was running. If the new registration cannot be set, or the
service does not start and stay running for a second, the previous
registration is put back and the service is started again if it was running.
Only the registration is restored. The files belong to the installer.

## Uninstall

`plaitwayd.exe uninstall` leaves `%ProgramData%\Plaitway`: the profiles hold
private keys, and an uninstall cannot tell from an upgrade. `-purge` deletes it
after the service is gone, and refuses when a junction, a hard-linked file or
an object owned by a standard user is in the tree.

Whether or not the service was installed, and after it is gone, `uninstall`
removes the DNS rules that Plaitway left in the registry
(`osnet/windows.SweepDNS`: every rule under
`HKLM\SYSTEM\CurrentControlSet\Services\Dnscache\Parameters\DnsPolicyConfig`
whose key name starts with `Plaitway-`; rules of other programs are left). It
does not depend on the daemon or the Reconciler, because the rules are the one
thing that a daemon which cannot start leaves behind that outlives a reboot: a
service that fails its checks of `wintun.dll` or `openvpn.exe` after a power
loss with a full tunnel up would otherwise keep all DNS of the PC on the dead
tunnel's server. Routes and adapters are not swept: they live in the active
store and with the process, and are gone with the reboot.

It prints `removed N DNS rules left in the registry` when it removed some. When
a rule remains, the exit code is 1 and the message names the keys and the
command that removes them by hand:

```powershell
Get-ChildItem 'HKLM:\SYSTEM\CurrentControlSet\Services\Dnscache\Parameters\DnsPolicyConfig' | Where-Object PSChildName -like 'Plaitway-*' | Remove-Item; Clear-DnsClientCache
```

`-purge` deletes the data in that case too.

A group policy (or DirectAccess) that delivers name resolution rules to the PC
makes Windows ignore the local rules. The daemon's DNS configurator then refuses
to write: it returns `osnet/windows.ErrGroupPolicyNRPT`, so that DNS shows as not
in effect and not as installed. Removing rules works as before.

## What the service does

- Reports `StartPending` with a growing check point while the daemon starts.
  `Running` is reported when the daemon logs that it listens, so `Running`
  means the pipe answers. A daemon that fails on the way is a failed start, not
  a service that stopped a moment after it started.
- Accepts Stop, Shutdown, PreShutdown, PowerEvent and SessionChange. Stop,
  Shutdown and PreShutdown run the shutdown order of `daemon.go`: end the watch
  streams, stop the server, stop the engines, end the Reconciler, which removes
  its routes and DNS entries. Meanwhile the service reports `StopPending` with
  a growing check point and wait hint, and exits only after the daemon has
  returned. PreShutdown comes first at system shutdown, while the network still
  works.
- Treats the console control event that Windows sends a service at shutdown
  (SIGTERM in Go) as a stop request.
- Logs power events and session changes. The daemon watches the network itself.
- Exits with code 0 after a stop that was asked for, whatever the shutdown
  returned: a non-zero code would make the failure actions restart a service
  that somebody has just stopped.

An engine that cannot run does not stop the daemon. Without a trusted
`wintun.dll` next to `plaitwayd.exe`, or with an `openvpn.exe` that fails the
checks of `internal/ovpn`, the daemon starts, listens and logs `engine
unavailable` with the reason; `GetDaemonInfo` and the profile's last error show
the same reason. The verdict on OpenVPN is taken again when a profile starts, so
an OpenVPN installed after the service started is used without a restart. The
daemon looks for `openvpn.exe` in `<folder of plaitwayd.exe>\openvpn\bin`, then
in `C:\Program Files\OpenVPN\bin` (the folder comes from the shell, not from
`%ProgramFiles%`); `-openvpn` overrides both.

What does stop the daemon is a failure that no restart repairs and no engine
can be blamed for: the pipe is taken, the state directory belongs to an
untrusted account, the journal cannot be read, the network cannot be read. The
service ends with exit code 1 and the reason is the last `ERROR` line of the
log. The failure actions are the only backoff: restart after 5 s, 10 s, then
every 30 s. The service manager has no setting that skips a restart for one
exit code; ending with exit code 0 would skip it, but then `start` and the
installer would report a daemon that never ran as a clean stop. So a
persistent fault shows as a service that restarts every 30 s, with one reason
line in the log each time, until the fault is repaired (the failure count starts
over after a day without a failure).

Service-specific exit codes, shown by `start` when the service ends and by
`sc.exe query`:

| Code | Meaning |
|---|---|
| 1 | The daemon failed or ended on its own |
| 2 | The daemon did not listen within 25 s |
| 3 | The daemon did not stop within its budget (22 s) |
| 4 | The process could not be prepared (hardening, detecting the service) |

The log is `%ProgramData%\Plaitway\Logs\plaitwayd.log`, readable by SYSTEM and
Administrators. A panic trace lands in it. A failure before the log is open
(wrong flags, a refused log directory) shows only as an exit code of the
process.

## Process hardening

In service mode, before anything else:

- `SetDefaultDllDirectories(APPLICATION_DIR | SYSTEM32)`: libraries come from
  the directory of the executable (administrator-only, see `install`) and
  System32, never from the current directory or PATH. wintun loads the same way.
- The current directory is System32.
- Image load mitigation: no image from a share, none with a low integrity
  label, System32 first.

Not set, because each would break the daemon or software around it: a ban on
child processes (openvpn), on images that Microsoft did not sign (wintun,
openvpn), on dynamic code (security software hooks the process), Win32k
lockdown, and legacy extension point blocking (a Winsock layered provider on
an old machine).

## Not done

- Delayed automatic start: see the table.
- Restricted service SID: a restricted token cannot add adapters or routes.
- Virtual service account (`NT SERVICE\PlaitwayHelper`) instead of
  LocalSystem: it cannot create wintun adapters or write routes without
  privileges that LocalSystem has.
- Trigger start (on network arrival): the daemon watches the network itself and
  must be up for auto-connect profiles at boot.
- Event Log: the log file is the record.
- A localized description: the string is English. A localized one is a
  resource in the executable, `@path,-id`.
- A recovery command or a reboot action.

## Looking at the registration

`Show-PlaitwayService.ps1` prints what the service manager holds: the
configuration, the failure actions, the service SID type and the access list.
It changes nothing and needs no elevation.

The same with `sc.exe`:

```powershell
sc.exe qc PlaitwayHelper
sc.exe qdescription PlaitwayHelper
sc.exe qfailure PlaitwayHelper
sc.exe qfailureflag PlaitwayHelper
sc.exe qsidtype PlaitwayHelper
sc.exe sdshow PlaitwayHelper
```

## Tests

`go test ./cmd/plaitwayd ./internal/fsperm` runs the unit tests, which use a
fake Service Control Manager. `service_integration_windows_test.go` is built
with `-tags rootintegration` and creates a real service named
`PlaitwayHelperTest`; read its header before running it from an elevated shell.

The test service is registered to start on demand, never at boot: the real one
is the only one that starts with the PC. A test that is killed does not run its
clean-up, so each test starts a guard before the first change: this test binary
again, without a console and in a process group of its own
(`internal/winiface/rootguard`). The guard stops and deletes the service and
deletes `%ProgramFiles%\PlaitwayHelperTest-*` when the test process ends without
having released it, or after ten minutes, and writes what it did to
`plaitway-rootguard-test-service-<pid>.log` in the folder of temporary files. The
test prints the command that does the same by hand:

```powershell
sc.exe stop PlaitwayHelperTest; sc.exe delete PlaitwayHelperTest; Remove-Item -Recurse -Force '<the scratch directory the test printed>'
```
