. "$PSScriptRoot\ui-lib.ps1"
$root = (Join-Path $PSScriptRoot "..\target\release")
$pipe = '\\.\pipe\plaitway-real'
$imp = Join-Path $env:TEMP "pw-import"
$scratch = Join-Path $env:TEMP "pw-manage-test"; New-Item -ItemType Directory -Force $scratch | Out-Null
function Ctl { & "$root\pwctl.exe" $pipe @args }
function Profiles { @(Ctl list) }
function Find($name) { Profiles | Where-Object { $_.Split('|')[1] -eq $name } | Select-Object -First 1 }
function Field($line, $key) { ($line.Split('|') | Where-Object { $_ -like "$key=*" }).Split('=')[1] }
function Until($what, [scriptblock]$cond, $secs = 6) { $end = (Get-Date).AddSeconds($secs); while ((Get-Date) -lt $end) { if (& $cond) { return $true }; Start-Sleep -m 200 }; $false }

Ctl clear
$env:LOCALAPPDATA = $scratch; $env:PLAITWAY_PIPE = $pipe
$env:PLAITWAY_IMPORT_FILE = "$imp\Cafe.conf|$imp\Corp.ovpn"
Get-Process plaitway-ui -ea 0 | Stop-Process -Force; Start-Sleep 1
$ui = Start-Process "$root\plaitway-ui.exe" -PassThru; Start-Sleep 3
Ok "the window shows, with nothing imported yet" ([UI]::IsWindowVisible((Win)) -and (Profiles).Count -eq 0)

# Import: from the empty page, then from the plus beside PROFILES.
Click 340 162
Ok "Import a profile on the empty page imports Cafe" (Until "cafe" { Find "Cafe" })
Click 215 103
Ok "the plus imports Corp" (Until "corp" { Find "Corp" })

# Credentials.
Click 100 206; Click 1200 83
Ok "connecting Corp makes the daemon ask for credentials (state 7)" (Until "creds" { (Field (Find "Corp") "state") -eq "7" })
SetBox 2 "ann"; SetBox 3 "hunter2"
Ok "the typed username is in its box" ((BoxText 2) -eq "ann")
Enter 3
Ok "Enter in the password box connects (state 3)" (Until "connected" { (Field (Find "Corp") "state") -eq "3" })

# The text of the profile.
Click 572 154
Ok "the Configuration tab fills the editor from the daemon" (Until "text" { (BoxText 1) -like "*auth-user-pass*" })
$text = BoxText 1
SetBox 1 ($text.TrimEnd() + "`n# edited by the test`n")
Click 329 662
Ok "Save stores the edited text" ((Until "edited" { ((& "$root\pwctl.exe" $pipe content ((Find "Corp").Split('|')[0])) -join "`n") -like "*# edited by the test*" }))
SetBox 1 ($text.TrimEnd() + "`n# fake: reject`n")
Click 329 662; Start-Sleep 1
$stored = (& "$root\pwctl.exe" $pipe content ((Find "Corp").Split('|')[0])) -join "`n"
Ok "a refused edit leaves the stored text alone" (($stored -like "*# edited by the test*") -and ($stored -notlike "*fake: reject*"))
Shot "m-config"

# Settings of the profile.
Click 674 154
Click 600 213
Ok "the first switch turns on connect-at-start" (Until "auto" { (Field (Find "Corp") "auto") -eq "True" })
Click 480 291
Ok "'All traffic' sets the tunnel mode to full" (Until "mode" { (Field (Find "Corp") "mode") -eq "2" })
Ok "the priority is not lost by those changes" ((Field (Find "Corp") "priority") -eq "2")
Shot "m-settings"

# Rename.
Click 320 66; Start-Sleep 1
Ok "clicking the title opens the name box" ((Box 4) -ne [IntPtr]::Zero -and (BoxText 4) -eq "Corp")
SetBox 4 "Corp HQ"; Enter 4
Ok "Enter renames the profile" (Until "renamed" { Find "Corp HQ" })

# Delete asks twice.
Click 1080 83; Start-Sleep 1
Ok "the first Delete only asks" ((Find "Corp HQ") -ne $null)
Shot "m-delete-ask"
Click 1206 83
Ok "the second Delete removes it" (Until "gone" { -not (Find "Corp HQ") })
Shot "m-after-delete"
$ui.Kill()
Ctl clear


