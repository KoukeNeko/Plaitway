<#
.SYNOPSIS
    Prints what the Service Control Manager holds for the Plaitway Helper service.
.DESCRIPTION
    Read only, no elevation. It compares nothing: README.md lists the values the
    installer sets.
#>
[CmdletBinding()]
param(
    [string]$Name = 'PlaitwayHelper'
)

$ErrorActionPreference = 'Stop'

$ServiceDoesNotExist = 1060
$ExitNotInstalled = 4

$queries = @(
    @{ Title = 'State'; Arguments = @('query', $Name) },
    @{ Title = 'Configuration'; Arguments = @('qc', $Name) },
    @{ Title = 'Description'; Arguments = @('qdescription', $Name) },
    @{ Title = 'Failure actions'; Arguments = @('qfailure', $Name) },
    @{ Title = 'Failure actions on non-crash failures'; Arguments = @('qfailureflag', $Name) },
    @{ Title = 'Service SID type'; Arguments = @('qsidtype', $Name) },
    @{ Title = 'Access list'; Arguments = @('sdshow', $Name) }
)

foreach ($query in $queries) {
    Write-Output "== $($query.Title)"
    & sc.exe @($query.Arguments)
    if ($LASTEXITCODE -eq $ServiceDoesNotExist) {
        Write-Output "$Name is not installed"
        exit $ExitNotInstalled
    }
    Write-Output ''
}
