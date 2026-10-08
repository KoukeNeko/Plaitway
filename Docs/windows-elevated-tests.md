# Windows tests that need elevation

These tests change the machine they run on, so they are not part of `go test ./...`. They carry the
`rootintegration` build tag and refuse to run in a shell that is not elevated. This page is the only list of them:
`packaging/windows/dev/Run-ElevatedTests.ps1` reads the two tables below and runs what they say, so a row that is
added here is run by the script and a row that is wrong here fails the script's `-CheckTable`.

Each test touches scratch resources only: routes and addresses in the documentation ranges `192.0.2.0/24`,
`198.51.100.0/24`, `203.0.113.0/24` and `2001:db8::/32`, the loopback pseudo-interface, NRPT rules for names under
`.invalid`, adapters named `Plaitway-test-*`, and a service named `PlaitwayHelperTest`. None of them writes the default
route, the DNS servers of a real interface, a firewall rule, the hosts file or the proxy. None touches the service
`PlaitwayHelper` or `%ProgramData%\Plaitway`. Everything listens on `127.0.0.1` only.

## Run

From an elevated PowerShell in the repository root:

```powershell
.\packaging\windows\dev\Run-ElevatedTests.ps1 -WhatIf                      # the plan, nothing is changed, no elevation needed
.\packaging\windows\dev\Run-ElevatedTests.ps1                              # Routes and Dns, one typed confirmation per group
.\packaging\windows\dev\Run-ElevatedTests.ps1 -Include Routes,Dns,Wintun   # more groups, in the order of the table
.\packaging\windows\dev\Run-ElevatedTests.ps1 -Yes -TestMachine -Include Routes,Dns,Wintun,OpenVpn,Daemon,Service
```

| Switch | Effect |
|---|---|
| `-Include <groups>` | The groups to run, from the Groups table. Default: the groups marked `yes` in the Default column |
| `-Yes` | Skips the typed confirmation. For a virtual machine; never a default |
| `-TestMachine` | Needed for `Service`: the group creates and removes a real service |
| `-CatchAll` | Needed for the `.` rule test of `Dns`: for a moment every name lookup of the PC goes to a throw-away resolver |
| `-IAcceptOpenVpnInterruption` | Lets `OpenVpn` run while an `openvpn.exe` that is not the tests' runs; see unknown 7 |
| `-WintunDll <path>` | A signed `wintun.dll` other than `bin\wintun\wintun.dll` |
| `-WhatIf` | Prints the plan and the state of every precondition, changes nothing |
| `-SnapshotOnly` | Takes the before snapshot twice and compares them, to see what the diff reports on a quiet machine. Needs no elevation |
| `-CheckTable` | Builds the test binaries and checks that every row names a test that exists, and that every test that exists only with the tag is a row or is in the last table. Needs no elevation |
| `-SelfTest` | Checks the script's own parsing, verdicts and diff rules on made-up data. Needs no elevation |
| `-AllowUnelevated` | Runs the groups in a shell that is not elevated. Every test checks for elevation before it changes anything and fails or skips, so this exercises the script and cannot show that a test passes |

The script builds each test binary once, into `%TEMP%\plaitway-elevated-tests`, at a fixed path. It takes a snapshot
before every group (routes, NRPT rules, adapters, services, firewall rule names, DNS client settings, the loopback's
addresses and metrics, driver packages, the state of `PlaitwayHelper`), runs the group, and takes another. Anything
named `Plaitway-*` or inside the scratch ranges that is still there, a changed loopback, a changed `PlaitwayHelper`,
a firewall rule that names a test binary and a socket on an address that is not loopback are reported as left behind,
with the command that removes them by hand; the script then stops. Other differences (a DHCP renewal, a service that
started) are listed as changes and are not failures. A log of the whole run is written to
`%TEMP%\plaitway-elevated-tests\logs\elevated-tests-<time>.log`, with the logs of the guard processes next to it. It
holds route, adapter and service names of the machine; read it before sending it.

Exit codes: `0` every selected group passed and left nothing, `1` a test failed or was skipped or a group was declined,
`2` the script refused to start (not elevated, a precondition, a missing switch), `3` something is left on the machine.

A test that skips counts as not run, never as a pass: the script checks for the `--- PASS` line of the named test.

## Groups

The order is the dependency order: the route table and the loopback first, because every later group relies on them;
adapters before OpenVPN, which needs the TAP adapter tooling; the real daemon after the engines it starts; the service
last, because it is the only group that changes the Service Control Manager.

| Group | Default | Switch | Risk | What it checks |
|---|---|---|---|---|
| Routes | yes | - | none | The route table adapter and the interface helper on the loopback pseudo-interface |
| Dns | yes | - | registry only | NRPT rules in the registry, the DNS Client picking them up, the sweep that uninstall runs |
| Wintun | no | - | driver install | The WireGuard engine's adapters on wintun, and the removal of leftover adapters |
| OpenVpn | no | - | driver install | The OpenVPN engine on tap-windows6 against a loopback server |
| Daemon | no | - | none | The real daemon with its real engines, started and stopped in an elevated process |
| Service | no | `-TestMachine` | service | The service registration, the pipe as SYSTEM and the restart after a crash |

The Switch column names a switch that has to be given for the group, or for that one test, to run at all.

Preconditions the script checks before it changes anything, and refuses on:

| Group | Refuses when |
|---|---|
| Routes | `PlaitwayHelper` is installed and not stopped (every test of the package refuses too); a scratch destination already has a route |
| Dns | `PlaitwayHelper` is installed and not stopped; `127.0.0.1:53` on UDP cannot be bound (the script tries); a rule named `Plaitway-*` exists; a group policy delivers NRPT rules |
| Wintun | There is no `wintun.dll` at `bin\wintun\wintun.dll` or at `-WintunDll`; an adapter named `Plaitway-test-*` exists |
| OpenVpn | There is no `C:\Program Files\OpenVPN\bin\openvpn.exe`; an `openvpn.exe` runs and `-IAcceptOpenVpnInterruption` is not given; an adapter named `Plaitway-test-*` exists |
| Daemon | The `PlaitwayHelper` service exists and is not stopped; a rule named `Plaitway-*` exists |
| Service | `-TestMachine` is not given; `PlaitwayHelperTest` exists; `go` is not on the path |

All groups also need `go` on the path, and refuse to start when the tree does not build or has unmerged files.

## Tests

One row per test function. The Command column is the command to run it by hand from the repository root; the script
runs the same test from the binary it built. `$env:` assignments in it are applied to that one test only.

| Test | Group | Command | Changes | Expected | Cleanup | Risk | Switch |
|---|---|---|---|---|---|---|---|
| `TestRootRouteLifecycleOnLoopback` | Routes | `go test -tags rootintegration -count=1 -run '^TestRootRouteLifecycleOnLoopback$' -v ./internal/osnet/windows` | Adds and deletes the on-link route 192.0.2.0/24 on Loopback Pseudo-Interface 1, in the active store | PASS. The route reads back static, as a blackhole (a static on-link route on the loopback is one), no gateway, metric 0; a second add is ErrExists, a second delete is ErrNotFound | The test deletes the route; its guard does after 2 minutes; by hand `Remove-NetRoute -DestinationPrefix 192.0.2.0/24 -Confirm:$false` | none | - |
| `TestRootRouteKeyIgnoresTheMetric` | Routes | `go test -tags rootintegration -count=1 -run '^TestRootRouteKeyIgnoresTheMetric$' -v ./internal/osnet/windows` | Adds and deletes 192.0.2.128/25 on the loopback with metric 7 | PASS: an add with metric 9 is ErrExists; a delete by destination alone and with metric 99 finds the route. Settles unknown 2 | As the row above, for 192.0.2.128/25 | none | - |
| `TestRootBlackholeRoutes` | Routes | `go test -tags rootintegration -count=1 -run '^TestRootBlackholeRoutes$' -v ./internal/osnet/windows` | Adds and deletes 198.51.100.64/28, 198.51.100.77/32, 2001:db8:ffff:1::/64 and 2001:db8:ffff::77/128 as on-link routes on the loopback | PASS in all four subtests: each reads back as a blackhole, static, on the loopback. Settles unknown 3 | As above, for each prefix | none | - |
| `TestRootRouteErrors` | Routes | `go test -tags rootintegration -count=1 -run '^TestRootRouteErrors$' -v ./internal/osnet/windows` | Nothing: three adds that must fail (a gateway on no subnet with no interface named, an interface that does not exist by name and by index) and two deletes of routes that do not exist | PASS: ErrUnreachable for each add, ErrNotFound for each delete | None needed | none | - |
| `TestRootNotificationsFireOnRouteChanges` | Routes | `go test -tags rootintegration -count=1 -run '^TestRootNotificationsFireOnRouteChanges$' -v ./internal/osnet/windows` | Adds and deletes 192.0.2.0/25 on the loopback; registers four change notifications | PASS: a route Change within 10 s of each, and the epoch does not move for a loopback route | The notifications end with the test; routes as above | none | - |
| `TestRootHelperServiceCheck` | Routes | `go test -tags rootintegration -count=1 -run '^TestRootHelperServiceCheck$' -v ./internal/osnet/windows` | Nothing: reads the state of `PlaitwayHelper` with the right to read it and no other, and checks the rule that every test of this package refuses while the service is not stopped | PASS: the rule refuses in each state of the service but stopped and not installed. The log line says whether `PlaitwayHelper` is installed on this PC and in which state | None needed | none | - |
| `TestRootSetAddressesOnLoopback` | Routes | `go test -tags rootintegration -count=1 -run '^TestRootSetAddressesOnLoopback$' -v ./internal/winiface` | Adds 192.0.2.77/32, 2001:db8::77/128 and 192.0.2.78/32 to the loopback, then removes them | PASS: the addresses are preferred when the call returns; the loopback's own addresses stay | The test removes them; its guard does after 2 minutes; by hand `Remove-NetIPAddress -InterfaceAlias 'Loopback Pseudo-Interface 1' -IPAddress 192.0.2.77,192.0.2.78,2001:db8::77 -Confirm:$false` | none | - |
| `TestRootSetInterfaceMetricOnLoopback` | Routes | `go test -tags rootintegration -count=1 -run '^TestRootSetInterfaceMetricOnLoopback$' -v ./internal/winiface` | Sets the interface metric of the loopback to 76 for IPv4 and IPv6, then gives it back to the system | PASS: manual 76, then automatic | The test restores the saved values; its guard does after 2 minutes; by hand `Set-NetIPInterface -InterfaceAlias 'Loopback Pseudo-Interface 1' -AddressFamily IPv4 -AutomaticMetric Enabled`, and IPv6 likewise | none | - |
| `TestRootSetMTUWritesBackTheCurrentValue` | Routes | `go test -tags rootintegration -count=1 -run '^TestRootSetMTUWritesBackTheCurrentValue$' -v ./internal/winiface` | Writes the loopback's current MTU back to itself | PASS. A `DIAGNOSTIC: SetMTU` line says that Windows refused the write, which is no failure | None needed | none | - |
| `TestRootDNSRuleResolvesThroughTheSystemResolver` | Dns | `go test -tags rootintegration -count=1 -run '^TestRootDNSRuleResolvesThroughTheSystemResolver$' -v ./internal/osnet/windows` | Writes NRPT rules for plaitway-test.invalid and plaitway-test2.invalid below `HKLM\SYSTEM\CurrentControlSet\Services\Dnscache\Parameters\DnsPolicyConfig`, asks the DNS Client to reread its policy, serves 127.0.0.1:53 on UDP | PASS. The log line `the rule was picked up after <time>` is how long the Reconciler can expect to wait. Settles unknowns 1 and 9 | The test removes the rules and flushes the cache; its guard does after 5 minutes; by hand the sweep command under Recovery | registry only | - |
| `TestRootDNSRegistryWriteWithoutPolicyRefresh` | Dns | `go test -tags rootintegration -count=1 -run '^TestRootDNSRegistryWriteWithoutPolicyRefresh$' -v ./internal/osnet/windows` | Writes one NRPT rule for plaitway-norefresh.invalid and does not ask the DNS Client to reread its policy | PASS on either answer. The `DIAGNOSTIC:` line says whether a registry write alone was picked up and after how long. Settles unknown 1 | As the row above | registry only | - |
| `TestRootSweepDNSRemovesTheRulesOfADeadDaemon` | Dns | `go test -tags rootintegration -count=1 -run '^TestRootSweepDNSRemovesTheRulesOfADeadDaemon$' -v ./internal/osnet/windows` | Writes two NRPT rules (plaitway-sweep.invalid, plaitway-sweep2.invalid) and removes them with the sweep that `plaitwayd.exe uninstall` runs | PASS: the sweep removes both and none remains | As the row above | registry only | - |
| `TestRootDNSCatchAll` | Dns | `$env:PLAITWAY_ROOT_CATCHALL='1'; go test -tags rootintegration -count=1 -run '^TestRootDNSCatchAll$' -v ./internal/osnet/windows` | Writes the rule `.`: every name lookup of the PC goes to the throw-away resolver until the rule is removed, a few seconds | PASS: two names resolve to 192.0.2.53, and stop doing so when the rule is removed | The test removes the rule; its guard does after 90 seconds; by hand the sweep command under Recovery | registry only | `-CatchAll` |
| `TestRootTwoWintunTunnelsHandshakeAndCarryADatagram` | Wintun | `$env:PLAITWAY_ROOT_TESTS='1'; go test -tags rootintegration -count=1 -run '^TestRootTwoWintunTunnelsHandshakeAndCarryADatagram$' -v ./internal/wg` | Creates two wintun adapters named Plaitway-test-*, with 198.51.100.2/30 and 203.0.113.2/30 on them; UDP on 127.0.0.1. The first adapter installs the wintun driver into the driver store | PASS: both tunnels Up, a handshake, one 1000-byte datagram counted on the client's tx and the server's rx, the IPv4 interface MTU 1420 (read with GetIpInterfaceEntry: `net.Interface.MTU` reports the adapter's 65535), both adapters gone after Stop. Settles unknown 6 | The test removes the adapters; the driver stays; adapters by hand `Get-NetAdapter -Name 'Plaitway-test-*' -IncludeHidden \| ForEach-Object { pnputil.exe /remove-device $_.PnPDeviceID }` | driver install | - |
| `TestRootStaleAdaptersAreRemoved` | Wintun | `$env:PLAITWAY_ROOT_TESTS='1'; go test -tags rootintegration -count=1 -run '^TestRootStaleAdaptersAreRemoved$' -v ./internal/wg` | Creates Plaitway-test-stale-left and Plaitway-test-not-stale, and removes the first with DIF_REMOVE | PASS: only the adapter with the stale prefix is removed. Settles unknown 5 | The test removes both; adapters by hand as above | driver install | - |
| `TestRootEngineStartRemovesAdaptersLeftByACrash` | Wintun | `$env:PLAITWAY_ROOT_TESTS='1'; go test -tags rootintegration -count=1 -run '^TestRootEngineStartRemovesAdaptersLeftByACrash$' -v ./internal/wg` | Creates Plaitway-test-left-by-a-crash, starts an engine on its own adapter, which removes the leftover | PASS: the leftover is gone, the engine's own adapter stays until Stop | The test stops the engine and removes what is left; adapters by hand as above | driver install | - |
| `TestRootAdapterOfAKilledProcessIsGone` | Wintun | `$env:PLAITWAY_ROOT_TESTS='1'; go test -tags rootintegration -count=1 -run '^TestRootAdapterOfAKilledProcessIsGone$' -v ./internal/wg` | Starts this test binary again as a child that creates Plaitway-test-stale-crashed and ends itself with TerminateProcess | PASS. The log line `adapter ... after the process was killed: left behind = true` or `false` settles unknown 5 | The test removes the adapter if it is left; adapters by hand as above | driver install | - |
| `TestRootOpenVPNConnectsOnItsOwnAdapter` | OpenVpn | `$env:PLAITWAY_ROOT_TESTS='1'; go test -tags rootintegration -count=1 -run '^TestRootOpenVPNConnectsOnItsOwnAdapter$' -v ./internal/ovpn` | tapctl creates Plaitway-test-ovpn-<8 hex digits> (tap-windows6); the installed openvpn.exe connects to a loopback server over TCP; the engine sets 198.51.100.2 on the adapter. No route and no DNS server is written | PASS: Up on the scratch adapter, the pushed route and DNS server are in the Intent and not on the machine, a Rebind keeps the adapter, the adapter is gone after Stop. Settles unknown 7 | The test removes the adapter; its guard does after 10 minutes; by hand `Get-NetAdapter -Name 'Plaitway-test-ovpn-*' \| ForEach-Object { & 'C:\Program Files\OpenVPN\bin\tapctl.exe' delete $_.Name }` | driver install | - |
| `TestRootOpenVPNKilledLeavesNoAdapter` | OpenVpn | `$env:PLAITWAY_ROOT_TESTS='1'; go test -tags rootintegration -count=1 -run '^TestRootOpenVPNKilledLeavesNoAdapter$' -v ./internal/ovpn` | As the row above, then ends openvpn.exe from outside | PASS: the engine fails, withdraws its owner and removes the adapter | As the row above | driver install | - |
| `TestRootAdapterLifecycle` | OpenVpn | `$env:PLAITWAY_ROOT_TESTS='1'; go test -tags rootintegration -count=1 -run '^TestRootAdapterLifecycle$' -v ./internal/ovpn` | tapctl creates and removes one scratch adapter, without openvpn | PASS: the adapter is known to the IP stack by name, then gone | As the row above | driver install | - |
| `TestRootStaleAdaptersAreRemoved` | OpenVpn | `$env:PLAITWAY_ROOT_TESTS='1'; go test -tags rootintegration -count=1 -run '^TestRootStaleAdaptersAreRemoved$' -v ./internal/ovpn` | Creates two scratch adapters and starts a device for a third | PASS: only the adapter that looks like the engine's leftover is removed | As the row above | driver install | - |
| `TestRootRealDaemonStartsElevatedAndStopsLeavingTheMachineAsItWas` | Daemon | `go test -tags rootintegration -count=1 -run '^TestRootRealDaemonStartsElevatedAndStopsLeavingTheMachineAsItWas$' -v ./cmd/plaitwayd` | Runs the real daemon inside the test process on a scratch pipe and a scratch state directory. It creates no adapter and no route; it removes `Plaitway-*` NRPT rules at start, so the test skips when any exists | PASS: GetDaemonInfo says privileged, OpenVPN is unavailable without a binary, the routing table is the same before and after, no `Plaitway-*` rule remains | The pipe and the directories are the test's own and go with it | none | - |
| `TestRealServiceLifecycle` | Service | `go test -tags rootintegration -count=1 -run '^TestRealServiceLifecycle$' -v ./cmd/plaitwayd` | Builds plaitwayd.exe into `%ProgramFiles%\PlaitwayHelperTest-*`, registers the service PlaitwayHelperTest (start on demand), runs it as SYSTEM on the fake backend, stops it, uninstalls it | PASS: the registration is what `install` sets, an unelevated token can only read it, the pipe is served by the service process and owned by SYSTEM or Administrators, a stop is clean and the service does not come back 8 seconds later | The test removes the service and the directory; its guard does after 10 minutes; by hand `sc.exe stop PlaitwayHelperTest; sc.exe delete PlaitwayHelperTest; Remove-Item -Recurse -Force "$env:ProgramFiles\PlaitwayHelperTest-*"` | service | `-TestMachine` |
| `TestRealServiceRestartsAfterACrash` | Service | `go test -tags rootintegration -count=1 -run '^TestRealServiceRestartsAfterACrash$' -v ./cmd/plaitwayd` | As the row above, then ends the service process with TerminateProcess | PASS: a running service with another process id within 40 seconds. SKIP when Windows refuses to end a SYSTEM process; that counts as not run. Settles unknown 8 | As the row above | service | `-TestMachine` |
| `TestScratchDirectoryCheck` | Service | `go test -tags rootintegration -count=1 -run '^TestScratchDirectoryCheck$' -v ./cmd/plaitwayd` | Nothing: the check that the guard of the service tests makes before it deletes a directory below Program Files | PASS: only a direct child of Program Files whose name starts with `PlaitwayHelperTest-` is accepted | None needed | none | - |
| `TestRemoveScratchDirectoryTouchesOnlyTheDirectoryOfTheTests` | Service | `go test -tags rootintegration -count=1 -run '^TestRemoveScratchDirectoryTouchesOnlyTheDirectoryOfTheTests$' -v ./cmd/plaitwayd` | Nothing outside its own temporary folder: removes a directory of the tests there and refuses the others | PASS: what is refused is still there with its contents | The test's own temporary folder goes with the test | none | - |

## Tests of the tag that are not rows above

These are in the files of the tag and are not run by the script as tests of their own.

| Test | Package | Why |
|---|---|---|
| `TestRootGuardHelper` | `*` | The entry of the guard process, which is the test binary started again. It does nothing in a normal run |
| `TestRootCrashChild` | `./internal/wg` | The child of `TestRootAdapterOfAKilledProcessIsGone`; it skips on its own |

## Recovery

If a run is killed and the script's report is not there, every test arms a guard process first. The guard cleans up
when the test process ends without releasing it and writes `plaitway-rootguard-<action>-<pid>.log` in the folder of
temporary files. When both are gone, the commands in the Cleanup column do the same by hand. The rules of the Dns
group, with the DNS Client cache:

```powershell
Get-ChildItem 'HKLM:\SYSTEM\CurrentControlSet\Services\Dnscache\Parameters\DnsPolicyConfig' | Where-Object PSChildName -like 'Plaitway-*' | Remove-Item; Clear-DnsClientCache
```

Routes in the scratch ranges, wherever they are:

```powershell
Get-NetRoute -PolicyStore ActiveStore | Where-Object { $_.DestinationPrefix -match '^(192\.0\.2\.|198\.51\.100\.|203\.0\.113\.|2001:db8:)' } | Remove-NetRoute -Confirm:$false
```

## What these tests settle

Nine things were written down as unknown or provisional. The table says which test settles each, what the possible
answers are, and what to do with each; the script prints the lines that carry the answer under "Findings".

| # | Unknown | Settled by | Answer and what to do |
|---|---|---|---|
| 1 | Does the DNS Client pick up an NRPT rule written to the registry without being told to reread its policy? | `TestRootDNSRegistryWriteWithoutPolicyRefresh`, and the pickup time in `TestRootDNSRuleResolvesThroughTheSystemResolver` | `a registry write alone was picked up after <time>`: set `refreshPolicyAfterChange = false` in `internal/osnet/windows/dns.go` (one line); the refresh code can go later. `was NOT picked up within 10 s`: leave it `true`. The time in the other test is what the Reconciler waits for a rule to take effect |
| 2 | Is the metric part of the key of a route? | `TestRootRouteKeyIgnoresTheMetric` | PASS: the keying of `internal/reconciler/keying.go` is right. A failure on `Add with another metric`: the metric is part of the key after all, so `KeyByPrefixInterfaceNextHop`, the route table's delete and the journal identity have to change; there is no switch for it |
| 3 | Which form is a blackhole route? | `TestRootBlackholeRoutes`, in both families | SETTLED on Windows 11 build 26300: the form that worked in older Windows, the loopback address (127.0.0.1, ::1) as next hop, is refused with "the parameter is incorrect" (New-NetRoute too). A static on-link route on the loopback pseudo-interface works, in IPv4 and IPv6: ping reports "General failure" and sends nothing, where the same packet without the route goes out and times out. `route_windows.go` uses that form. A failure of the test on another Windows: look at `loopbackTarget` and `routeFromRow` |
| 4 | Does an IPv6 gateway work on a tap-windows6 adapter? | No test in the tables. `TestRootOpenVPNConnectsOnItsOwnAdapter` pushes IPv4 only | Open. The Reconciler installs IPv6 routes through the gateway the tunnel announced (`GatewayV6`). A tunnel that announces an IPv4 gateway and no IPv6 one gets no IPv6 routes except the halves of a default route, and says `no IPv6 gateway in the tunnel`. It needs an IPv6 push added to that test, which is not in the files of this task |
| 5 | Does DIF_REMOVE remove a wintun device, and does a killed process leave its adapter behind? | `TestRootStaleAdaptersAreRemoved` (wg) and `TestRootAdapterOfAKilledProcessIsGone` | PASS on the first: `removeStaleAdapters` works. `left behind = false` on the second: the adapter goes with the process and the removal at start is only a net. `left behind = true` and PASS: the removal at start is what cleans up. A failure with `remove adapter ...`: the adapter has to be removed by hand (command in the table) and `removeDevice` in `internal/wg/stale_windows.go` is where to look. SETTLED on Windows 11 build 26300: DIF_REMOVE removes the device, and the adapter of a killed process goes with the process (`left behind = false`); the removal at start is a net |
| 6 | What does the first wintun adapter leave on the machine? | `TestRootTwoWintunTunnelsHandshakeAndCarryADatagram` and the driver packages in the script's report | A driver package for wintun in the driver store, listed by the script as a documented change, not a failure. It stays; the daemon never calls `WintunDeleteDriver`. To remove it: `pnputil.exe /enum-drivers`, find the entry whose original name is `wintun.inf`, `pnputil.exe /delete-driver oemNN.inf /uninstall`. SETTLED on build 26300: the first adapter of the first run replaced wintun 0.8 (`wintun.inf_amd64_def3401515466414`, put there by other software) with the 0.14 of `wintun.dll` (`oem7.inf`, `wintun.inf_amd64_8ed20477a29aa8f7`). `wintun.dll` does that itself (`Removing existing driver 0.8`, `Installing driver 0.14`), so whatever else on the PC uses wintun now runs on 0.14 |
| 7 | Do the tap-windows6 adapters of the tests coexist with the OpenVPN GUI and services of the owner? | The `OpenVpn` group run with `-IAcceptOpenVpnInterruption` while an OpenVPN is connected | The script lists the `openvpn.exe` processes and `tap0901` adapters before and after. The same processes and adapters: they coexist, and the switch can stay for that machine. A process ended or an adapter gone: do not run the group next to a live connection; disconnect it first. No code switch |
| 8 | Does the service restart after a crash? | `TestRealServiceRestartsAfterACrash` | PASS: the failure actions work as `packaging/windows/service/README.md` says. `was not restarted within 40s`: compare `packaging\windows\service\Show-PlaitwayService.ps1` with the failure actions in that README. SKIP: end the process some other way and look at `sc.exe query PlaitwayHelperTest`. SETTLED: PASS on build 26300 |
| 9 | Does the DNS Client accept the rule layout (`ConfigOptions` 8, `GenericDNSServers`, both namespaces of a domain)? | `TestRootDNSRuleResolvesThroughTheSystemResolver` | PASS: the layout in `internal/osnet/windows/doc.go` is accepted and the domain means itself and everything below it. A failure at the first `resolvesTo`: the DNS Client ignores the rule; check with `Get-DnsClientNrptPolicy` and the layout against `Add-DnsClientNrptRule` on the same machine |
