# End-to-end checks of the window

These PowerShell scripts drive the real window with messages and read the real daemon, so what they
check is what a user would see. They need a release build (`cargo build --release --workspace`) and a
Windows desktop session.

| Script | What it checks | Daemon it uses |
|---|---|---|
| `shell-test.ps1` | one running copy, hide to the tray and come back, window placement, `--hidden`, start with Windows | `fake-daemon.exe` (made here) |
| `uia-test.ps1` | the window through UI Automation, as a screen reader sees it: names, roles, pressing a button | `fake-daemon.exe` |
| `lang-test.ps1` | choosing a language and the Chinese text and fonts | `fake-daemon.exe` |
| `manage-test.ps1` | import, credentials, editing the text, settings, rename, delete | a real `plaitwayd -fake` on `\\.\pipe\plaitway-real` |

`manage-test.ps1` needs that daemon running with administrator rights, because the Windows build of
`plaitwayd` insists on `C:\ProgramData\Plaitway`:

```powershell
Start-Process 'C:\Program Files\Plaitway\plaitwayd.exe' -Verb RunAs -WindowStyle Hidden -ArgumentList `
  '-fake -socket \\.\pipe\plaitway-real -state-dir "$env:TEMP\pw-state" -run-dir "$env:TEMP\pw-run" -log-file ""'
```

It deletes every profile of that daemon first. Never point it at the real service's pipe.

`ui-lib.ps1` is the shared part: finding the window, sending clicks, reading the text boxes, taking a
picture of the window (`Shot`).

The windows are 1280 by 800 at 125 % scale and the scripts click at logical positions, so a change of
layout means new numbers.
