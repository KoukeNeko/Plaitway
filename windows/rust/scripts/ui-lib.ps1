Add-Type @'
using System; using System.Text; using System.Runtime.InteropServices;
public static class UI {
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern IntPtr FindWindow(string c, string t);
  [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr h);
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out R r);
  [DllImport("user32.dll")] public static extern bool MoveWindow(IntPtr h, int x, int y, int w, int hh, bool r);
  [DllImport("user32.dll")] public static extern IntPtr SendMessage(IntPtr h, uint m, IntPtr w, IntPtr l);
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern IntPtr SendMessage(IntPtr h, uint m, IntPtr w, string l);
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern int GetWindowText(IntPtr h, StringBuilder s, int n);
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern IntPtr SendMessage(IntPtr h, uint m, IntPtr w, StringBuilder l);

  [DllImport("user32.dll")] public static extern IntPtr GetDlgItem(IntPtr h, int id);
  [DllImport("user32.dll")] public static extern bool SetProcessDPIAware();
  [DllImport("user32.dll")] public static extern uint GetDpiForWindow(IntPtr h);
  [DllImport("user32.dll")] public static extern bool SetWindowPos(IntPtr h, IntPtr a, int x, int y, int cx, int cy, uint f);
  [StructLayout(LayoutKind.Sequential)] public struct R { public int L, T, Rt, B; }
}
'@
Add-Type -AssemblyName System.Drawing
[void][UI]::SetProcessDPIAware()
$script:OutDir = $env:TEMP
function Win { [UI]::FindWindow("PlaitwayUi", "Plaitway") }
function Ok($name, $cond) { "{0,-66} {1}" -f $name, $(if ($cond) { "ok" } else { "FAILED" }) }
function Scale { [UI]::GetDpiForWindow((Win)) / 96.0 }
function Msg($m, $lx, $ly) { $s = Scale; $l = ([int]($ly*$s) -shl 16) -bor ([int]($lx*$s) -band 0xffff); [void][UI]::SendMessage((Win), $m, [IntPtr]0, [IntPtr]$l); Start-Sleep -m 350 }
function Click($x, $y) { Msg 0x200 $x $y; Msg 0x201 $x $y; Msg 0x202 $x $y; Start-Sleep -m 250 }
function Box($id) { $f = [UI]::FindWindow("PlaitwayField", "$id"); if ($f -eq [IntPtr]::Zero) { return [IntPtr]::Zero }; [UI]::GetDlgItem($f, $id) }
function SetBox($id, $text) { $b = Box $id; [void][UI]::SendMessage($b, 0x0C, [IntPtr]0, $text); Start-Sleep -m 300 }
function BoxText($id) { $b = Box $id; $sb = New-Object System.Text.StringBuilder 70000; [void][UI]::SendMessage($b, 0x0D, [IntPtr]70000, $sb); $sb.ToString() }
function Enter($id) { [void][UI]::SendMessage((Box $id), 0x100, [IntPtr]0x0D, [IntPtr]0); Start-Sleep -m 500 }
function Shot($name) {
  $h = Win; [void][UI]::SetWindowPos($h, [IntPtr](-1), 0, 0, 0, 0, 0x43)
  foreach ($i in 1..4) { $p = [UI]::FindWindow("PlaitwayField", "$i"); if ($p -ne [IntPtr]::Zero) { [void][UI]::SetWindowPos($p, [IntPtr](-1), 0, 0, 0, 0, 0x53) } }
  Start-Sleep -m 400
  $r = New-Object UI+R; [void][UI]::GetWindowRect($h, [ref]$r)
  $w = $r.Rt - $r.L; $hh = $r.B - $r.T
  $b = New-Object System.Drawing.Bitmap $w, $hh
  $g = [System.Drawing.Graphics]::FromImage($b); $g.CopyFromScreen($r.L, $r.T, 0, 0, $b.Size); $g.Dispose()
  $b.Save("$script:OutDir\$name.png"); $b.Dispose()
  foreach ($i in 1..4) { $p = [UI]::FindWindow("PlaitwayField", "$i"); if ($p -ne [IntPtr]::Zero) { [void][UI]::SetWindowPos($p, [IntPtr](-2), 0, 0, 0, 0, 0x53) } }
  [void][UI]::SetWindowPos($h, [IntPtr](-2), 0, 0, 0, 0, 0x43)
}




