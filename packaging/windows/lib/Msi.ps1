# Reads a Windows Installer package through the Windows Installer COM API, read-only: the package is opened as a
# database and nothing is installed, so no elevation is needed. Dot-source this file.

Set-StrictMode -Version Latest

$script:MsiOpenDatabaseModeReadOnly = 0
$script:ComBindingFlags = [System.Reflection.BindingFlags]

function Invoke-ComMember {
    param([object] $Target, [string] $Name, [System.Reflection.BindingFlags] $Kind, [object[]] $Arguments = @())
    return $Target.GetType().InvokeMember($Name, $Kind, $null, $Target, $Arguments)
}

# Opens the package. The handle must go to Close-MsiDatabase: an open database keeps the file locked, which stops a
# build from replacing it.
function Open-MsiDatabase {
    param([Parameter(Mandatory)] [string] $Path)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "The package $Path does not exist."
    }
    $installer = New-Object -ComObject WindowsInstaller.Installer
    $fullPath = (Resolve-Path -LiteralPath $Path).Path
    $database = Invoke-ComMember $installer 'OpenDatabase' InvokeMethod @($fullPath, $script:MsiOpenDatabaseModeReadOnly)
    return [pscustomobject]@{ Installer = $installer; Database = $database; Path = $fullPath }
}

function Close-MsiDatabase {
    param([Parameter(Mandatory)] $Handle)
    foreach ($com in @($Handle.Database, $Handle.Installer)) {
        if ($null -ne $com) {
            [void][System.Runtime.InteropServices.Marshal]::ReleaseComObject($com)
        }
    }
    [GC]::Collect()
    [GC]::WaitForPendingFinalizers()
}

# Runs a SELECT and returns its rows, each an array of the fields as strings (an empty field is an empty string).
function Invoke-MsiQuery {
    param([Parameter(Mandatory)] $Handle, [Parameter(Mandatory)] [string] $Sql)
    $view = Invoke-ComMember $Handle.Database 'OpenView' InvokeMethod @($Sql)
    try {
        [void](Invoke-ComMember $view 'Execute' InvokeMethod)
        $rows = [System.Collections.Generic.List[object]]::new()
        while ($true) {
            $record = Invoke-ComMember $view 'Fetch' InvokeMethod
            if ($null -eq $record) {
                break
            }
            $fieldCount = [int](Invoke-ComMember $record 'FieldCount' GetProperty)
            $fields = for ($index = 1; $index -le $fieldCount; $index++) {
                [string](Invoke-ComMember $record 'StringData' GetProperty @([int]$index))
            }
            $rows.Add(@($fields))
            [void][System.Runtime.InteropServices.Marshal]::ReleaseComObject($record)
        }
        return , $rows.ToArray()
    }
    finally {
        [void](Invoke-ComMember $view 'Close' InvokeMethod)
        [void][System.Runtime.InteropServices.Marshal]::ReleaseComObject($view)
    }
}

function Test-MsiTable {
    param([Parameter(Mandatory)] $Handle, [Parameter(Mandatory)] [string] $Name)
    $tables = Invoke-MsiQuery -Handle $Handle -Sql 'SELECT `Name` FROM `_Tables`'
    return [bool]($tables | Where-Object { $_[0] -eq $Name })
}

# The Property table as a dictionary.
function Get-MsiProperties {
    param([Parameter(Mandatory)] $Handle)
    $properties = @{}
    foreach ($row in (Invoke-MsiQuery -Handle $Handle -Sql 'SELECT `Property`, `Value` FROM `Property`')) {
        $properties[$row[0]] = $row[1]
    }
    return $properties
}

# Writes the named stream of the package (a cabinet embedded in it, for instance) to a file.
function Export-MsiStream {
    param([Parameter(Mandatory)] $Handle, [Parameter(Mandatory)] [string] $StreamName, [Parameter(Mandatory)] [string] $Destination)
    $view = Invoke-ComMember $Handle.Database 'OpenView' InvokeMethod @('SELECT `Name`, `Data` FROM `_Streams` WHERE `Name` = ''' + $StreamName + '''')
    try {
        [void](Invoke-ComMember $view 'Execute' InvokeMethod)
        $record = Invoke-ComMember $view 'Fetch' InvokeMethod
        if ($null -eq $record) {
            throw "The package has no stream $StreamName."
        }
        $chunkSize = 1MB
        $output = [System.IO.File]::Create($Destination)
        try {
            while ($true) {
                $chunk = Invoke-ComMember $record 'ReadStream' InvokeMethod @([int]2, [int]$chunkSize, [int]2)
                if ([string]::IsNullOrEmpty($chunk)) {
                    break
                }
                # ReadStream with msiReadStreamBytes (2) gives the bytes as a string of one character per byte.
                $bytes = [System.Text.Encoding]::Latin1.GetBytes($chunk)
                $output.Write($bytes, 0, $bytes.Length)
            }
        }
        finally {
            $output.Dispose()
        }
        [void][System.Runtime.InteropServices.Marshal]::ReleaseComObject($record)
    }
    finally {
        [void](Invoke-ComMember $view 'Close' InvokeMethod)
        [void][System.Runtime.InteropServices.Marshal]::ReleaseComObject($view)
    }
}

# Summary information property by its number (PID_TEMPLATE 7, PID_REVNUMBER 9, PID_WORDCOUNT 15, ...).
function Get-MsiSummaryProperty {
    param([Parameter(Mandatory)] $Handle, [Parameter(Mandatory)] [int] $PropertyId)
    $summary = Invoke-ComMember $Handle.Database 'SummaryInformation' GetProperty @([int]0)
    try {
        return [string](Invoke-ComMember $summary 'Property' GetProperty @([int]$PropertyId))
    }
    finally {
        [void][System.Runtime.InteropServices.Marshal]::ReleaseComObject($summary)
    }
}
