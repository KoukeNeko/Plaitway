# Makes the icons of the Windows app from Docs\app-icon.png, so that they can be made again and nobody draws them twice:
#
#   pwsh windows\scripts\generate-icons.ps1
#
# Plaitway.ico            the window's and the file's icon, 16 to 256 px
# Tray\tray-<state>-<taskbar>.ico
#                         the notification-area icon: the app's icon with a badge for how the profiles stand, at 16, 20,
#                         24, 32 and 48 px (100 % to 300 % scaling), once for a dark taskbar and once for a light one.
#                         The shell picks the size that fits its scale; each size is drawn, not scaled from another.
#
# States: idle (grey, no badge), connecting (amber badge with a bar), connected (green badge with a check),
# attention (red badge with an exclamation mark). A badge differs in shape as well as in colour.

[CmdletBinding()]
param(
    [string] $OutputDirectory = (Join-Path $PSScriptRoot '..\Sources\Plaitway.App\Assets')
)

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Drawing

$RepositoryRoot = Resolve-Path (Join-Path $PSScriptRoot '..\..')
$SourcePath = Join-Path $RepositoryRoot 'Docs\app-icon.png'
$OutputDirectory = [System.IO.Path]::GetFullPath($OutputDirectory)
$TrayDirectory = Join-Path $OutputDirectory 'Tray'

$AppSizes = 16, 20, 24, 32, 40, 48, 64, 256
$TraySizes = 16, 20, 24, 32, 48
$Supersample = 8
$AlphaThreshold = 8

# Taskbar theme -> what has to stand out against it.
$Taskbars = @{
    dark  = @{ BadgeRing = [System.Drawing.Color]::FromArgb(235, 255, 255, 255); Grey = 190 }
    light = @{ BadgeRing = [System.Drawing.Color]::FromArgb(235, 28, 28, 28); Grey = 105 }
}
$Badges = @{
    connecting = @{ Fill = [System.Drawing.Color]::FromArgb(255, 245, 158, 11); Mark = 'bar' }
    connected  = @{ Fill = [System.Drawing.Color]::FromArgb(255, 22, 163, 74); Mark = 'check' }
    attention  = @{ Fill = [System.Drawing.Color]::FromArgb(255, 220, 38, 38); Mark = 'exclamation' }
}

function Get-ContentBounds([System.Drawing.Bitmap] $bitmap) {
    $minX = $bitmap.Width; $minY = $bitmap.Height; $maxX = -1; $maxY = -1
    for ($y = 0; $y -lt $bitmap.Height; $y++) {
        for ($x = 0; $x -lt $bitmap.Width; $x++) {
            if ($bitmap.GetPixel($x, $y).A -gt $AlphaThreshold) {
                if ($x -lt $minX) { $minX = $x }; if ($x -gt $maxX) { $maxX = $x }
                if ($y -lt $minY) { $minY = $y }; if ($y -gt $maxY) { $maxY = $y }
            }
        }
    }
    return [System.Drawing.Rectangle]::new($minX, $minY, $maxX - $minX + 1, $maxY - $minY + 1)
}

function New-Graphics([System.Drawing.Bitmap] $bitmap) {
    $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
    $graphics.SmoothingMode = 'AntiAlias'
    $graphics.InterpolationMode = 'HighQualityBicubic'
    $graphics.PixelOffsetMode = 'HighQuality'
    $graphics.CompositingQuality = 'HighQuality'
    return $graphics
}

# The app's icon, cropped to its rounded square, at size x size.
function Get-BaseIcon([System.Drawing.Bitmap] $source, [System.Drawing.Rectangle] $bounds, [int] $size) {
    $big = $size * $Supersample
    $large = [System.Drawing.Bitmap]::new($big, $big, [System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
    $graphics = New-Graphics $large
    $graphics.DrawImage($source, [System.Drawing.Rectangle]::new(0, 0, $big, $big), $bounds, [System.Drawing.GraphicsUnit]::Pixel)
    $graphics.Dispose()
    return $large
}

function Convert-ToGrey([System.Drawing.Bitmap] $bitmap, [int] $level) {
    for ($y = 0; $y -lt $bitmap.Height; $y++) {
        for ($x = 0; $x -lt $bitmap.Width; $x++) {
            $pixel = $bitmap.GetPixel($x, $y)
            if ($pixel.A -eq 0) { continue }
            # Keep the shield lighter than the plate, as in the colour icon.
            $luma = [int](0.299 * $pixel.R + 0.587 * $pixel.G + 0.114 * $pixel.B)
            $shade = [math]::Min(255, [int]($level * 0.55 + $luma * 0.45))
            $bitmap.SetPixel($x, $y, [System.Drawing.Color]::FromArgb($pixel.A, $shade, $shade, $shade))
        }
    }
}

function Add-Badge([System.Drawing.Bitmap] $large, [hashtable] $badge, [hashtable] $taskbar) {
    $size = $large.Width
    $diameter = [int]($size * 0.56)
    $left = $size - $diameter
    $top = $size - $diameter
    $graphics = New-Graphics $large
    $ringWidth = [single]($size * 0.045)
    $ringBrush = [System.Drawing.SolidBrush]::new($taskbar.BadgeRing)
    $graphics.FillEllipse($ringBrush, $left - $ringWidth, $top - $ringWidth, $diameter + 2 * $ringWidth, $diameter + 2 * $ringWidth)
    $graphics.FillEllipse([System.Drawing.SolidBrush]::new($badge.Fill), $left, $top, $diameter, $diameter)

    $pen = [System.Drawing.Pen]::new([System.Drawing.Color]::White, [single]($diameter * 0.17))
    $pen.StartCap = 'Round'; $pen.EndCap = 'Round'; $pen.LineJoin = 'Round'
    $cx = $left + $diameter / 2.0; $cy = $top + $diameter / 2.0; $u = $diameter / 10.0
    switch ($badge.Mark) {
        'check' {
            $graphics.DrawLines($pen, [System.Drawing.PointF[]]@(
                [System.Drawing.PointF]::new($cx - 2.6 * $u, $cy + 0.1 * $u),
                [System.Drawing.PointF]::new($cx - 0.7 * $u, $cy + 2.2 * $u),
                [System.Drawing.PointF]::new($cx + 2.8 * $u, $cy - 2.4 * $u)))
        }
        'bar' {
            $graphics.DrawLine($pen, [single]($cx - 2.4 * $u), [single]$cy, [single]($cx + 2.4 * $u), [single]$cy)
        }
        'exclamation' {
            $graphics.DrawLine($pen, [single]$cx, [single]($cy - 2.8 * $u), [single]$cx, [single]($cy + 0.7 * $u))
            $graphics.FillEllipse([System.Drawing.Brushes]::White, [single]($cx - 0.95 * $u), [single]($cy + 1.7 * $u), [single](1.9 * $u), [single](1.9 * $u))
        }
    }
    $graphics.Dispose()
}

function Get-Png([System.Drawing.Bitmap] $large, [int] $size) {
    $small = [System.Drawing.Bitmap]::new($size, $size, [System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
    $graphics = New-Graphics $small
    $graphics.DrawImage($large, 0, 0, $size, $size)
    $graphics.Dispose()
    $stream = [System.IO.MemoryStream]::new()
    $small.Save($stream, [System.Drawing.Imaging.ImageFormat]::Png)
    $small.Dispose()
    return , $stream.ToArray()
}

# An .ico file with one PNG frame per size; Windows has read PNG frames since Vista.
function Save-Ico([string] $path, [object[]] $frames) {
    $stream = [System.IO.MemoryStream]::new()
    $writer = [System.IO.BinaryWriter]::new($stream)
    $writer.Write([uint16]0); $writer.Write([uint16]1); $writer.Write([uint16]$frames.Count)
    $offset = 6 + 16 * $frames.Count
    foreach ($frame in $frames) {
        $dimension = if ($frame.Size -ge 256) { 0 } else { $frame.Size }
        $writer.Write([byte]$dimension); $writer.Write([byte]$dimension); $writer.Write([byte]0); $writer.Write([byte]0)
        $writer.Write([uint16]1); $writer.Write([uint16]32)
        $writer.Write([uint32]$frame.Png.Length); $writer.Write([uint32]$offset)
        $offset += $frame.Png.Length
    }
    foreach ($frame in $frames) { $writer.Write($frame.Png) }
    $writer.Flush()
    New-Item -ItemType Directory -Force (Split-Path $path) | Out-Null
    [System.IO.File]::WriteAllBytes($path, $stream.ToArray())
    Write-Host "wrote $path ($($frames.Count) sizes)"
}

$source = [System.Drawing.Bitmap]::new($SourcePath)
$bounds = Get-ContentBounds $source

$appFrames = foreach ($size in $AppSizes) {
    $base = Get-BaseIcon $source $bounds $size
    [pscustomobject]@{ Size = $size; Png = (Get-Png $base $size) }
    $base.Dispose()
}
Save-Ico (Join-Path $OutputDirectory 'Plaitway.ico') $appFrames

foreach ($taskbar in $Taskbars.Keys) {
    foreach ($state in 'idle', 'connecting', 'connected', 'attention') {
        $frames = foreach ($size in $TraySizes) {
            $large = Get-BaseIcon $source $bounds $size
            if ($state -eq 'idle') { Convert-ToGrey $large $Taskbars[$taskbar].Grey } else { Add-Badge $large $Badges[$state] $Taskbars[$taskbar] }
            [pscustomobject]@{ Size = $size; Png = (Get-Png $large $size) }
            $large.Dispose()
        }
        Save-Ico (Join-Path $TrayDirectory "tray-$state-$taskbar.ico") $frames
    }
}
$source.Dispose()
