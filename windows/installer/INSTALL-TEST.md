# Installing the MSI for real

`packaging\windows\Test-Installer.ps1` reads the packages and unpacks them without installing. This file is what
`Test-Installer.ps1` cannot do: install, upgrade, repair and remove the product with its service. **Run it in a
virtual machine** (the Windows 11 test VM, restored to a clean snapshot first), never on a PC that has Plaitway
installed or runs `PlaitwayHelper`: the installer registers a LocalSystem service, and an uninstall with `-purge`
deletes `%ProgramData%\Plaitway`, where the profiles with their private keys live.

Build the package on the development PC and copy it over:

```powershell
powershell -File packaging\windows\build-installer.ps1
powershell -File packaging\windows\Test-Installer.ps1        # the read-only checks
```

The commands below run in an elevated PowerShell in the VM. `$msi` is the package, `$log` a path for the verbose log:

```powershell
$msi = 'C:\Test\Plaitway-0.6.1-x64.msi'
$log = 'C:\Test\msi.log'
```

## 1. First installation

```powershell
msiexec /i $msi /qn /l*v $log
```

| Expect | Check |
|---|---|
| Exit code 0 | `$LASTEXITCODE` (use `Start-Process -Wait -PassThru` to get it) |
| Files in `C:\Program Files\Plaitway` (`plaitwayd.exe`, `plaitway.exe`, `wintun.dll`), `app\Plaitway.exe`, `licenses\` | `Get-ChildItem -Recurse` |
| The service exists, runs and starts automatically | `& 'C:\Program Files\Plaitway\plaitwayd.exe' status` prints `PlaitwayHelper: running`; `(Get-Service PlaitwayHelper).StartType` is `Automatic` |
| The service runs as LocalSystem, with failure actions | `sc.exe qc PlaitwayHelper`, `sc.exe qfailure PlaitwayHelper` |
| The install folder passes the daemon's own check | `layoutcheck.exe -path 'C:\Program Files\Plaitway\plaitwayd.exe'` prints `verdict=pass` (build it on the PC with `go build -o layoutcheck.exe .\packaging\windows\layoutcheck` and copy it) |
| The daemon answers on its pipe | `& 'C:\Program Files\Plaitway\plaitway.exe' status` |
| A Start menu entry **Plaitway** for all users | `Get-ChildItem "$env:ProgramData\Microsoft\Windows\Start Menu\Programs" -Filter Plaitway.lnk` |
| An entry in Apps with the version | `Get-ItemProperty HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\* \| Where DisplayName -eq Plaitway` |
| The app starts from the Start menu entry without a runtime installed, and shows the Helper as running | by eye |

Another folder is refused: `msiexec /i $msi /qn INSTALLFOLDER=D:\Plaitway` fails with "Plaitway installs only to its
folder in Program Files, because the Helper service must run from a folder that only administrators can change" (the
daemon refuses an executable a standard user could replace).

## 2. Repair

Delete `wintun.dll`, then `msiexec /fa $msi /qn`. Expect the file back and the service running again. Stop the service
first with `plaitwayd.exe stop` and repeat: expect it started.

## 3. Upgrade

Build a second package of the same version with a different payload (touch a license file) or of the next version, and
install it over the first:

```powershell
msiexec /i $newMsi /qn /l*v $log
```

| Expect | Why |
|---|---|
| Exit code 0, one entry in Apps | `MajorUpgrade` removes the old product after the new files are in (`afterInstallExecute`) |
| The service is running with the new executable; the `ImagePath` is unchanged | `HelperStop`, then the files, then `install -update -start` |
| `%ProgramData%\Plaitway` is untouched: a profile created before is still listed | the upgrade skips `HelperUninstall` (`UPGRADINGPRODUCTCODE`) |
| An older version over a newer one fails with "A newer version of Plaitway is installed" | `DowngradeErrorMessage` |

Roll-back: install the new package with a file locked by another process, or with `INSTALLFOLDER` pointing at a
read-only share, and expect the old version, with its service running, afterwards.

## 4. Removal

```powershell
msiexec /x $msi /qn /l*v $log
```

Expect: no service (`plaitwayd.exe` is gone, so use `sc.exe query PlaitwayHelper`: error 1060), no
`C:\Program Files\Plaitway`, no Start menu entry, no entry in Apps, **`%ProgramData%\Plaitway` still there**.

| Variant | Expect |
|---|---|
| `msiexec /x $msi /qn PLAITWAY_PURGE_DATA=1` | as above, and `%ProgramData%\Plaitway` deleted |
| A removal while a tunnel is up (connect a profile first) | the tunnel goes down, its routes and DNS rules are removed (`Get-DnsClientNrptRule`), no `Plaitway-*` adapter remains |
| A removal while `uninstall` fails (replace `plaitwayd.exe` by a file that exits with 1) | the removal fails and rolls back, the service is registered again; with `PLAITWAY_FORCE_UNINSTALL=1` it succeeds anyway |

## 5. What to keep from a run

The verbose log (`/l*v`) holds the standard error of every Helper action: `HelperStop`, `HelperInstall`,
`HelperUninstall`. Search for `Helper` and for `Return value 3`. A failed run's log goes into the issue with the output
of the checks above.
