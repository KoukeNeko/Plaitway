# wintun

The WireGuard engine on Windows creates its tunnel adapters with
[Wintun](https://www.wintun.net/). `wintun.dll` is not part of this repository.

| File | Purpose |
|---|---|
| `wintun.json` | Pins the version, the archive and the DLL of each architecture by SHA-256, and the signer |
| `../fetch-wintun.ps1` | Downloads the archive into a new folder, checks the pins and the signature, and copies `wintun.dll` to a folder |
| `THIRD_PARTY.txt` | Notice and license text, for the installer to ship |
| `.gitignore` | Keeps `wintun.dll` and the archive out of the repository |

## Fetch

```powershell
powershell -File packaging\windows\fetch-wintun.ps1 -OutputDirectory bin\wintun
```

`-Architecture amd64|arm64|x86` defaults to the machine's. The script refuses to
write anything unless the archive, the DLL and the Authenticode signature
(issued to WireGuard LLC) all check out. To update, change `wintun.json`: the
new archive hash is the one published at <https://www.wintun.net/>, the DLL
hashes are those of the files in the archive.

## Where the daemon loads it from

Next to `plaitwayd.exe`, only. Before the engine uses the DLL it checks the
Authenticode signature of that file (valid, issued to WireGuard LLC) and loads
it by full path. `golang.zx2c4.com/wintun`, which wireguard-go calls, then finds
that module by name; its own search covers the application directory and
system32, never the working directory or `PATH`. `Probe` reports the engine as
unavailable, with the reason, when the file is missing, is not a plain file, is
not signed by WireGuard LLC or cannot be loaded.

## Driver

`wintun.dll` carries the signed driver (`wintun.sys`, `wintun.inf`,
`wintun.cat`) in its resources. The first `WintunCreateAdapter` installs it into
the driver store with `SetupCopyOEMInf`, which needs administrator rights: the
daemon runs as LocalSystem, so the installer ships `wintun.dll` and no driver
package. The DLL imports `SwDeviceCreate` and `SwDeviceClose`: an adapter is a
software device that Windows removes when the handle that created it closes, so
it should go away when the daemon closes it or ends. At the first adapter the
daemon removes leftovers with the `Plaitway-` prefix anyway, and
`TestRootAdapterOfAKilledProcessIsGone` shows on a real machine what a killed
process leaves. `WintunDeleteDriver` removes the driver; the daemon never calls
it.

## Tests that create adapters

The tests tagged `rootintegration` create scratch adapters named
`Plaitway-test-*` and need an elevated shell. They change nothing else. From
the repository root, in an elevated PowerShell:

```powershell
powershell -File packaging\windows\fetch-wintun.ps1 -OutputDirectory bin\wintun
$env:PLAITWAY_ROOT_TESTS = '1'
go test -count=1 -tags rootintegration -run 'TestRoot' -v ./internal/wg
```

`PLAITWAY_WINTUN_DLL` names another signed `wintun.dll`. Without the elevation
or the variable the tests skip and say why. The tests without the tag use the
same DLL when it exists, and skip the cases that need it otherwise.
