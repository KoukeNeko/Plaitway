# Running the daemon by hand

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

The tests that change routes, DNS or adapters are not part of this script; they
are tagged `rootintegration` in their packages and use scratch resources only.
