# Windows: what is verified, what waits, what is not done

State on 2026-10-11, at release 0.6.0. The machine of the verification is a Windows 11 Pro PC (build
26300) with an administrator session; "elevated" below means that session. The architecture is in
[windows-architecture.md](windows-architecture.md), the elevated tests in [windows-elevated-tests.md](windows-elevated-tests.md).

## Verified

| What | How | Result |
|---|---|---|
| Daemon, command line client, every Go package | `go test ./...` on Windows; the Unix-only tests as Linux binaries in WSL | pass |
| Routes: the route table adapter, the interface helper (addresses, metric, MTU) | `Run-ElevatedTests.ps1 -Include Routes` | 9 of 9 |
| DNS: NRPT rules, pickup by the DNS Client, the sweep of `uninstall`, the catch-all rule `.` | `-Include Dns -CatchAll` | 4 of 4 |
| WireGuard engine on wintun: two tunnels, handshake, a datagram through the adapter, the removal of leftover and of crashed adapters | `-Include Wintun` | 4 of 4 |
| The real daemon in an elevated process on a scratch pipe | `-Include Daemon` | 1 of 1 |
| The service: registration, the pipe served as SYSTEM, stop, restart after a crash, uninstall | `-Include Service -TestMachine` | 4 of 4 |
| C# client library and app model | `dotnet test` from `windows/` | Client 898 of 900 (2 need a daemon with real engines), AppCore 361 of 361 |
| UI tests: the window opened on the screen | `dotnet test Tests/Plaitway.App.UiTests` from `windows/`, after the drop and the trust check were wired and the branch was rebased onto v0.5.0 | 9 of 9; the new one chooses Traditional Chinese in Settings, restarts the app and reads the sidebar of the new window |
| The `windows` job of the release workflow on a GitHub runner (`windows-latest`) | Release run by hand: the Go tests, `build-installer.ps1`, `Test-Installer.ps1`; then `Test-Installer.ps1` again on the two MSIs of the artifact | pass. The first run failed two tests that assumed a process that is not elevated (`fsperm`: the owner of a new file; `transport`: the restricted token of an ordinary user kept the Administrators group); the tests are fixed |
| The MSI | `build-installer.ps1`, then `Test-Installer.ps1` (tables, the order of the Helper actions, the unpacked files, the signature of `wintun.dll`) | pass |
| Installing the MSI for real, on the development PC (the PC had no Plaitway, no `%ProgramData%\Plaitway`, and a restore timer was armed) | install, `plaitwayd status`, `plaitway status` through the pipe, `layoutcheck`, repair (also of a deleted `wintun.dll` and with the service stopped), the zh-TW package over the en-US one, removal with `PLAITWAY_PURGE_DATA=1` | pass; the service is LocalSystem, automatic, restarts on failure; after the removal nothing is left |

The first two attempts to install failed and rolled back cleanly, which found three defects that the read-only checks could not:
the launch condition used `WindowsBuild`, which the installer reports as 9600 on Windows 11 (now read from the registry); the
quotes of the Helper command lines reached `WixQuietExec` as the text `&quot;`; and `MajorUpgrade` gave the upgrade a language, so
the zh-TW package sat next to the en-US one in one folder (now an explicit upgrade without a language). `Test-Installer.ps1` checks
the last two.

Every elevated group ran with a restore timer (`packaging/windows/dev/NetworkGuard.ps1`) armed first and with a snapshot of
routes, DNS rules, adapters, services, firewall rules and drivers before and after; no group left a difference except the
driver below.

The unknowns that the elevated tests were written to settle are answered in the table "What these tests settle" of
[windows-elevated-tests.md](windows-elevated-tests.md): the metric is not part of a route's key, a blackhole is an on-link
route on the loopback pseudo-interface, the DNS Client picks up a rule from the registry by itself, the layout of a rule
is accepted, DIF_REMOVE removes a wintun device and the adapter of a killed process goes with the process, the service
restarts after a crash.

## Changes the verification left on the PC

- **The wintun driver was replaced.** The first adapter replaced wintun 0.8 (`wintun.inf_amd64_def3401515466414`, put there
  by other software) with the 0.14 of `wintun.dll` (`oem7.inf`). Software on the PC that uses wintun now runs on 0.14.
- Four allow rules of Windows Defender Firewall for deleted `wg.test.exe` paths remain from an earlier run, as a test
  binary once listened on all addresses (fixed since; the tests listen on loopback only). They are listed by
  `Get-NetFirewallApplicationFilter | Where-Object Program -like '*.test.exe'`.

## Waits for something this session did not have

| What | What it needs |
|---|---|
| What of the MSI is still untried: the rollback of a failed install, the block of a downgrade, `PLAITWAY_FORCE_UNINSTALL`, a removal while a tunnel is up, starting the app from the Start menu entry, an install over a machine that has the service from `plaitwayd install`, the single package over an installed 0.6.0 of either language | A virtual machine restored to a clean snapshot: [windows/installer/INSTALL-TEST.md](../windows/installer/INSTALL-TEST.md) |
| The `OpenVpn` group of the elevated tests (3 tests: the engine on a tap-windows6 adapter against a loopback server) | An OpenVPN installation with nothing connected, or a decision to run next to the owner's connection (`-IAcceptOpenVpnInterruption`). The PC of the verification had a connected OpenVPN, so the group was not run |
| An IPv6 gateway on a tap-windows6 adapter (unknown 4) | A test that pushes IPv6 from the loopback server |
| Dropping profile files on the window | The drop is wired in `ShellView`; a drag from Explorer has not been tried |
| Windows 10, arm64 | Machines |

## Not done

- A code signing certificate: the package, the programs and the app are unsigned (`build-installer.ps1 -CertificateThumbprint`
  signs them when there is one; tried with a throw-away certificate). The app's check of the helper then falls back to the
  folder rule, which Program Files passes.
- Update checking.
- OpenVPN is the user's own installation, not part of the package.
- Polish listed by the reviewers: tab stops of the sidebar, the columns of the log page, a test of the zh-TW strings through
  UI Automation.
