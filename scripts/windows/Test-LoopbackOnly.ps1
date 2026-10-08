# Runs the Go tests of every package and fails when a test process opens a socket on an address that is not
# loopback.
#
# Why: a Go test binary is built into a new temporary path every time. When it listens on a non-loopback
# address, Windows Defender Firewall asks the person at the keyboard whether to allow it, and each "Allow"
# leaves two permanent inbound rules for a file that is deleted a minute later. A test only ever needs
# loopback (127.0.0.0/8, ::1). A socket that lives for a few milliseconds is enough to raise the prompt, so
# the watcher (scripts\windows\loopbackwatch) reads the kernel's socket tables in a tight loop instead of
# sampling every few tens of milliseconds, which missed exactly such a socket once.
#
# The test binaries are built to fixed paths below -OutputDirectory, so a regression raises at most one
# prompt per package and is then reported here with the name of the package.
#
# Usage: scripts\windows\Test-LoopbackOnly.ps1 [-Packages ./internal/wg,./cmd/plaitwayd]
#        scripts\windows\Test-LoopbackOnly.ps1 -PerTest ./internal/wg    (one process per test: names the test)

[CmdletBinding()]
param(
    [string[]] $Packages = @(),
    [string] $PerTest = '',
    [string] $OutputDirectory = (Join-Path $env:TEMP 'plaitway-loopback-check')
)

$ErrorActionPreference = 'Stop'
$repository = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$settleMilliseconds = 400

function Get-TestedPackages {
    $lines = go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}|{{.Dir}}{{end}}' ./... 2>&1
    foreach ($line in $lines) {
        if ($line -match '\|') { $import, $dir = $line -split '\|', 2; [pscustomobject]@{ Import = $import; Directory = $dir } }
    }
}

function Start-Watcher([string] $LogFile) {
    $watcher = Join-Path $OutputDirectory 'loopbackwatch.exe'
    go build -o $watcher ./scripts/windows/loopbackwatch
    if ($LASTEXITCODE -ne 0) { throw 'the socket watcher did not build' }
    if (Test-Path $LogFile) { [IO.File]::Delete($LogFile) }
    $process = Start-Process -FilePath $watcher -PassThru -WindowStyle Hidden -RedirectStandardOutput $LogFile
    Start-Sleep -Milliseconds $settleMilliseconds
    return $process
}

function Stop-Watcher($Process, [string] $LogFile) {
    Start-Sleep -Milliseconds $settleMilliseconds
    Stop-Process -Id $Process.Id -Force
    return @(Get-Content $LogFile)
}

function Invoke-Test([string] $Executable, [string] $WorkingDirectory, [string[]] $Arguments) {
    $process = Start-Process -FilePath $Executable -ArgumentList $Arguments -WorkingDirectory $WorkingDirectory `
        -PassThru -WindowStyle Hidden -RedirectStandardOutput "$Executable.out" -RedirectStandardError "$Executable.err"
    [void] $process.WaitForExit()
    return $process.ExitCode
}

Push-Location $repository
try {
    New-Item -ItemType Directory -Force $OutputDirectory | Out-Null
    $log = Join-Path $OutputDirectory 'sockets.log'
    $targets = Get-TestedPackages
    $filter = if ($PerTest) { @($PerTest) } else { $Packages }
    if ($filter.Count -gt 0) {
        $wanted = $filter | ForEach-Object { (Resolve-Path (Join-Path $repository $_)).Path }
        $targets = $targets | Where-Object { $wanted -contains (Resolve-Path $_.Directory).Path }
    }
    $failed = 0
    foreach ($target in $targets) {
        $name = ($target.Import -replace '[^A-Za-z0-9]+', '_') + '.test.exe'
        $executable = Join-Path $OutputDirectory $name
        go test -c -o $executable $target.Import 2>$null
        if (-not (Test-Path $executable)) { continue }   # a package whose tests are tagged out here

        $watcher = Start-Watcher $log
        $exitCode = 0
        if ($PerTest) {
            # '-test.list' is quoted: PowerShell would split it at the dot.
            foreach ($test in @(& $executable '-test.list' '.*' | Where-Object { $_ -match '^Test' })) {
                $code = Invoke-Test $executable $target.Directory @('-test.run', "^$test`$", '-test.count=1')
                if ($code -ne 0) { $exitCode = $code }
                $before = @(Get-Content $log).Count
                Start-Sleep -Milliseconds 50
                if (@(Get-Content $log).Count -gt $before) { Write-Warning "a socket on a non-loopback address appeared during $test" }
            }
        }
        else {
            $exitCode = Invoke-Test $executable $target.Directory @('-test.count=1')
        }
        $sockets = Stop-Watcher $watcher $log

        $verdict = if ($sockets.Count -gt 0) { $failed++; 'NOT LOOPBACK' } elseif ($exitCode -ne 0) { 'tests failed' } else { 'ok' }
        '{0,-14} {1}' -f $verdict, $target.Import
        $sockets | ForEach-Object { "               $_" }
    }
    if ($failed -gt 0) { throw "$failed package(s) open a socket on a non-loopback address" }
}
finally {
    Pop-Location
}
