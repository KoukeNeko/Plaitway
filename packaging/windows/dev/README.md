# Running the daemon and the elevated tests by hand

| Script | Purpose |
|---|---|
| `Run-DaemonElevated.ps1` | Runs `plaitwayd.exe` in the foreground of an elevated shell with the real engines, on a scratch pipe |
| `Run-ElevatedTests.ps1` | Runs the tests that need elevation, group by group, and checks what they left behind |

## The daemon

`Run-DaemonElevated.ps1` starts `bin\plaitwayd.exe` in the foreground of an
elevated PowerShell with the real engines, on a pipe named
`\\.\pipe\plaitway-dev-<random>` and a state directory below `%TEMP%`. It never
uses the default pipe, `%ProgramData%\Plaitway` or the `PlaitwayHelper` service,
and it refuses to start while that service or another real `plaitwayd` runs: a
daemon removes every `Plaitway-*` DNS rule and adapter when it starts.

```powershell
go build -o bin\plaitwayd.exe .\cmd\plaitwayd
go build -o bin\plaitway.exe .\cmd\plaitway
powershell -File packaging\windows\fetch-wintun.ps1 -OutputDirectory bin   # once, for WireGuard
.\packaging\windows\dev\Run-DaemonElevated.ps1
```

The script prints how to point `plaitway.exe` at the daemon. Ctrl+C stops the
daemon; the Reconciler then removes its routes and DNS rules. The script lists
every `Plaitway-*` adapter, `Plaitway-*` DNS rule and route that is there and was
not there before, and the journal records never marked removed, and exits with 1
when anything is left. Start it again with `-ScratchDirectory` on the kept
directory to let a new daemon replay the journal.

## The elevated tests

The tests that change routes, DNS, adapters or the service are tagged
`rootintegration` in their packages and use scratch resources only.
`Run-ElevatedTests.ps1` runs them from binaries it builds once, in the groups
and the order of the tables in
[Docs/windows-elevated-tests.md](../../../Docs/windows-elevated-tests.md), which
is also where each test, its changes, its expected result and its cleanup are
written down. It reads those tables; there is no second list.

```powershell
.\packaging\windows\dev\Run-ElevatedTests.ps1 -WhatIf     # the plan and the preconditions, from any shell
.\packaging\windows\dev\Run-ElevatedTests.ps1             # from an elevated shell: Routes and Dns
```

The default groups change the routing table only for documentation ranges on
the loopback pseudo-interface and write NRPT rules for `.invalid` names. The
other groups (`Wintun`, `OpenVpn`, `Daemon`, `Service`) are named with
`-Include`; `Service` also needs `-TestMachine`. Each group asks for its name to
be typed before it runs; `-Yes` answers for a virtual machine.

Before each group the script snapshots routes, NRPT rules, adapters, services,
firewall rule names, DNS client settings, the loopback and the driver store, and
compares afterwards. A leftover is printed with the command that removes it, and
the run stops. The log of the run and the logs of the test guards are in
`%TEMP%\plaitway-elevated-tests\logs`.

| Exit code | Meaning |
|---|---|
| 0 | Every selected group passed and left nothing |
| 1 | A test failed or was skipped, or a group was declined |
| 2 | The script refused to start |
| 3 | Something is left on the machine |

`-CheckTable`, `-SelfTest` and `-SnapshotOnly` need no elevation and change
nothing; `-CheckTable` builds the test binaries and fails when the tables and
the tests disagree in either direction.
