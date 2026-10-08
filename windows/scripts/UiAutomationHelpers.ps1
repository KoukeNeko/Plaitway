# Functions for the scripts that look at the running app through UI Automation, which is what a screen reader sees.
# Dot-source it: . "$PSScriptRoot\UiAutomationHelpers.ps1"

Add-Type -AssemblyName UIAutomationClient, UIAutomationTypes, System.Drawing
if (-not ([System.Management.Automation.PSTypeName] 'UiNative').Type) {
    Add-Type -Path (Join-Path $PSScriptRoot 'UiNative.cs') -ReferencedAssemblies System.Drawing.Common, System.Drawing.Primitives, System.Private.Windows.GdiPlus, System.Private.Windows.Core
}
[UiNative]::MakeDpiAware()

# What has to have a name for a screen reader: everything the user can act on or move through, and the lists they sit in.
# Text and panes need none (a text says what it says; a pane only groups).
$script:ControlTypesThatNeedAName = @(
    'Button', 'CheckBox', 'ComboBox', 'Edit', 'Hyperlink', 'List', 'ListItem', 'MenuItem', 'RadioButton', 'Slider',
    'SplitButton', 'Tab', 'TabItem', 'Document', 'Image', 'ProgressBar', 'DataItem', 'TreeItem', 'Tree', 'Table')

function Get-ControlTypeName($Element) {
    return $Element.Current.ControlType.ProgrammaticName -replace '^ControlType\.', ''
}

function Get-ChildElements($Element) {
    $children = [System.Collections.Generic.List[object]]::new()
    $walker = [System.Windows.Automation.TreeWalker]::ControlViewWalker
    $child = $walker.GetFirstChild($Element)
    while ($null -ne $child) {
        $children.Add($child)
        $child = $walker.GetNextSibling($child)
    }
    return $children
}

# The tree as text, one control per line: type, name, automation id, class, whether the keyboard can focus it, patterns.
function Get-AutomationTreeText($Element, [int] $Depth = 0, [int] $MaxDepth = 40) {
    $current = $Element.Current
    $patterns = ($Element.GetSupportedPatterns() | ForEach-Object { $_.ProgrammaticName -replace 'Identifiers\.Pattern', '' }) -join ','
    $line = '{0}{1} name="{2}" id="{3}" class="{4}" focusable={5} patterns=[{6}]' -f ('  ' * $Depth), (Get-ControlTypeName $Element), $current.Name, $current.AutomationId, $current.ClassName, $current.IsKeyboardFocusable, $patterns
    $lines = [System.Collections.Generic.List[string]]::new()
    $lines.Add($line)
    if ($Depth -lt $MaxDepth) {
        foreach ($child in (Get-ChildElements $Element)) {
            foreach ($childLine in (Get-AutomationTreeText $child ($Depth + 1) $MaxDepth)) { $lines.Add($childLine) }
        }
    }
    return $lines
}

# The controls that have to have a name and have none, as "Window > Pane > Button" paths.
function Get-UnnamedControls($Element, [string] $Path = '') {
    $type = Get-ControlTypeName $Element
    $here = if ($Path) { "$Path > $type" } else { $type }
    $found = [System.Collections.Generic.List[string]]::new()
    if ($script:ControlTypesThatNeedAName -contains $type -and [string]::IsNullOrWhiteSpace($Element.Current.Name)) {
        $found.Add("$here (id=`"$($Element.Current.AutomationId)`" class=`"$($Element.Current.ClassName)`")")
    }
    foreach ($child in (Get-ChildElements $Element)) {
        foreach ($item in (Get-UnnamedControls $child $here)) { $found.Add($item) }
    }
    return $found
}

function Find-ByName($Root, [string] $Name, [string] $ControlType = '') {
    $condition = [System.Windows.Automation.PropertyCondition]::new([System.Windows.Automation.AutomationElement]::NameProperty, $Name)
    foreach ($element in $Root.FindAll([System.Windows.Automation.TreeScope]::Descendants, $condition)) {
        if (-not $ControlType -or (Get-ControlTypeName $element) -eq $ControlType) { return $element }
    }
    return $null
}

function Find-ByNamePrefix($Root, [string] $Prefix, [string] $ControlType = '') {
    foreach ($element in $Root.FindAll([System.Windows.Automation.TreeScope]::Descendants, [System.Windows.Automation.Condition]::TrueCondition)) {
        if ((-not $ControlType -or (Get-ControlTypeName $element) -eq $ControlType) -and $element.Current.Name.StartsWith($Prefix, [StringComparison]::Ordinal)) { return $element }
    }
    return $null
}

function Find-AllByType($Root, [string] $ControlType) {
    $all = $Root.FindAll([System.Windows.Automation.TreeScope]::Descendants, [System.Windows.Automation.Condition]::TrueCondition)
    return @($all | Where-Object { (Get-ControlTypeName $_) -eq $ControlType })
}

# Chooses a list item, a tab or a radio button the way a keyboard user would, without the mouse.
function Select-Element($Element) {
    $pattern = $Element.GetCurrentPattern([System.Windows.Automation.SelectionItemPattern]::Pattern)
    $pattern.Select()
}

function Invoke-Element($Element) {
    $pattern = $Element.GetCurrentPattern([System.Windows.Automation.InvokePattern]::Pattern)
    $pattern.Invoke()
}

function Wait-Until([string] $What, [scriptblock] $Condition, [int] $Seconds = 20) {
    $deadline = (Get-Date).AddSeconds($Seconds)
    while (-not (& $Condition)) {
        if ((Get-Date) -gt $deadline) { throw "timed out waiting for $What" }
        Start-Sleep -Milliseconds 50
    }
}

# A window as the app draws it. PrintWindow asks the window itself for its pixels, so nothing in front of it (another window,
# a tooltip) can get into the picture; the backdrop of the window (Mica) is the one thing it leaves out.
function Save-WindowScreenshot([IntPtr] $Window, [string] $Path) {
    [UiNative]::CaptureWindow($Window, $Path)
}