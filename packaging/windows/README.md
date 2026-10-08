# Packaging for Windows

Build, check and sign the MSI of Plaitway. The package is per machine, x64 only (the app is not built for arm64
yet), Windows 10 version 1809 or later, one file per language (`en-US`, `zh-TW`).

| Path | Purpose |
|---|---|
| `build-installer.ps1` | Stages the payload and builds `build\windows\Plaitway-<version>-x64-<culture>.msi` |
| `Test-Installer.ps1` | Reads the packages and unpacks them with an administrative install (`msiexec /a`); installs nothing |
| `lib\Msi.ps1`, `lib\Sign.ps1` | Reading a package through the Windows Installer COM API; Authenticode signing |
| `fetch-wintun.ps1`, `wintun\` | `wintun.dll`, checked by pinned hashes and by its signature (WireGuard LLC) |
| `layoutcheck\` | Runs the check that `plaitwayd install` makes of its own location, for the installer's tests |
| `service\` | The contract of the `PlaitwayHelper` service and `plaitwayd install` / `uninstall` |
| `openvpn\` | What the daemon needs from the OpenVPN installation it finds |
| `dev\` | Running the daemon and the tests that need elevation, by hand |
| `..\..\windows\installer\` | The WiX project (`Package.wxs`, the languages) and `INSTALL-TEST.md`, the test in a virtual machine |

## Build

```powershell
powershell -File packaging\windows\build-installer.ps1                  # both languages, unsigned
powershell -File packaging\windows\build-installer.ps1 -Culture zh-TW
powershell -File packaging\windows\Test-Installer.ps1
```

The version is the `VERSION` file. The script builds `plaitwayd.exe` and `plaitway.exe` with it, fetches `wintun.dll`,
publishes the app self-contained (`-p:PlaitwaySelfContained=true`, so the machine needs no Windows App Runtime and no
.NET), writes the licence and `THIRD_PARTY_NOTICES.md` (`go run ./packaging/notices -windows`), and builds the package
once per language, because each has its own ProductCode. Everything goes below `build\windows`, which git ignores:
`payload\` is what the package holds, the finished packages are beside it. `-SkipAppPublish` reuses the app of the last
run, for trying out a change of `Package.wxs`.

The ProductCode is derived from the upgrade code, the version, the language and the hash of every payload file: the same
files give the same code, other files give another one, and the package replaces the installed one as an upgrade
(`AllowSameVersionUpgrades`). The upgrade code never changes.

## Signing

An unsigned package installs, with a SmartScreen warning. To sign, give a code signing certificate (current user or
machine store, with its private key):

```powershell
powershell -File packaging\windows\build-installer.ps1 -CertificateThumbprint <thumbprint>
```

The programs of Plaitway (`plaitwayd.exe`, `plaitway.exe`, `Plaitway.exe`, the app's own assemblies) are signed before
the packages are built, and the packages after, all with SHA-256 and a timestamp (`-TimestampServer`, default DigiCert).
`wintun.dll` and the libraries of others keep their signatures. A signature that is not `Valid` stops the build;
`-AllowUntrustedRoot` accepts a self-signed certificate, to try the script out.

## What the package does

Files in `C:\Program Files\Plaitway`: `plaitwayd.exe`, `plaitway.exe`, `wintun.dll`, `app\` and `licenses\`; a Start menu
shortcut for all users. The folder cannot be changed: `plaitwayd install` refuses an executable that a standard user
could replace.

The package has no ServiceInstall rows. Custom actions run the daemon's own `install`, `uninstall` and `stop` from the
installed executable, so what the service is stays in `packaging\windows\service`. `Package.wxs` has the order of the
actions and the rollback of each.

| Command line | Effect |
|---|---|
| `msiexec /i Plaitway-….msi /qn` | Install or upgrade; an upgrade stops the service, replaces the files and registers it again with `install -update -start` |
| `msiexec /x Plaitway-….msi /qn` | Remove the product and the service; `%ProgramData%\Plaitway` stays, because the profiles hold private keys |
| `… PLAITWAY_PURGE_DATA=1` | With a removal: delete `%ProgramData%\Plaitway` too |
| `… PLAITWAY_FORCE_UNINSTALL=1` | With a removal: go on when `plaitwayd uninstall` fails |

OpenVPN is not part of the package. The daemon uses the OpenVPN of the PC (see `openvpn\README.md`); WireGuard needs
nothing else.
