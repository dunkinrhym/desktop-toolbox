# 桌面工具箱 Windows 冒烟测试（在 GitHub Actions windows-latest 上运行，需要 PowerShell 7 / pwsh 与管理员权限）
# 用法：pwsh tests/windows/smoke.ps1 -Installer <安装包.exe> -OutDir <输出目录> [-Samples tests/samples]
# 每一步记录 PASS / FAIL 并继续，最后有失败则退出码 1。输出：OutDir 下的 smoke.log、截图、selftest 日志、转换结果、录屏文件。
param(
  [Parameter(Mandatory = $true)][string]$Installer,
  [Parameter(Mandatory = $true)][string]$OutDir,
  [string]$Samples = (Join-Path $PSScriptRoot '..\samples'),
  [string]$FFprobe = ''
)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
$OutDir = (Resolve-Path $OutDir).Path
$Samples = (Resolve-Path $Samples).Path
$Installer = (Resolve-Path $Installer).Path
$LogFile = Join-Path $OutDir 'smoke.log'
$script:Failures = @()
$InstallDir = Join-Path $env:ProgramFiles 'DesktopToolbox'
$Exe = Join-Path $InstallDir 'DesktopToolbox.exe'
$Python = Join-Path $InstallDir 'resources\python\python.exe'
$Script = Join-Path $InstallDir 'resources\scripts\convert.py'
$FFmpeg = Join-Path $InstallDir 'resources\ffmpeg\ffmpeg.exe'

function Log([string]$msg) {
  $line = "[{0}] {1}" -f (Get-Date -Format 'HH:mm:ss'), $msg
  Write-Host $line
  Add-Content -Path $LogFile -Value $line -Encoding utf8
}
function Step([string]$name, [scriptblock]$body) {
  Log "==== $name"
  try {
    $detail = & $body
    Log "PASS  $name  $detail"
  } catch {
    $script:Failures += $name
    Log "FAIL  $name  $($_.Exception.Message)"
    Log ($_.ScriptStackTrace)
  }
}
function Assert($cond, [string]$msg) { if (-not $cond) { throw $msg } }
# 启动进程并限时等待（Start-Process -Wait 会等待所有子进程，且没有超时，容易卡住整个任务）
function Run-Timed([string]$file, [string[]]$argList, [int]$sec) {
  $p = if ($argList) { Start-Process -FilePath $file -ArgumentList $argList -PassThru } else { Start-Process -FilePath $file -PassThru }
  if (-not $p.WaitForExit($sec * 1000)) {
    Save-Screenshot ("timeout-" + [IO.Path]::GetFileNameWithoutExtension($file) + ".png") | Out-Null
    try { Stop-Process -Id $p.Id -Force } catch {}
    throw "$file 在 $sec 秒内没有退出（已强制结束）"
  }
  $p.WaitForExit()
  return $p
}

Add-Type -AssemblyName System.Drawing
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.IO.Compression.FileSystem
Add-Type @'
using System;
using System.Runtime.InteropServices;
public static class W32 {
  [DllImport("user32.dll")] public static extern bool SetProcessDPIAware();
  [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr h);
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern IntPtr FindWindow(string cls, string title);
  [DllImport("user32.dll")] public static extern int GetSystemMetrics(int i);
  [DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out RECT r);
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int Left, Top, Right, Bottom; }
  [StructLayout(LayoutKind.Sequential)] struct KEYBDINPUT { public ushort wVk; public ushort wScan; public uint dwFlags; public uint time; public IntPtr dwExtraInfo; }
  [StructLayout(LayoutKind.Explicit, Size=40)] struct INPUT { [FieldOffset(0)] public uint type; [FieldOffset(8)] public KEYBDINPUT ki; }
  [DllImport("user32.dll", SetLastError=true)] static extern uint SendInput(uint n, INPUT[] inputs, int size);
  // 依次按下 vks，再逆序松开；返回成功注入的事件数
  public static uint Keys(params ushort[] vks) {
    var list = new System.Collections.Generic.List<INPUT>();
    foreach (var v in vks) { var i = new INPUT(); i.type = 1; i.ki.wVk = v; list.Add(i); }
    for (int k = vks.Length - 1; k >= 0; k--) { var i = new INPUT(); i.type = 1; i.ki.wVk = vks[k]; i.ki.dwFlags = 2; list.Add(i); }
    return SendInput((uint)list.Count, list.ToArray(), Marshal.SizeOf(typeof(INPUT)));
  }
}
'@
[W32]::SetProcessDPIAware() | Out-Null

function Save-Screenshot([string]$name) {
  $x = [W32]::GetSystemMetrics(76); $y = [W32]::GetSystemMetrics(77)
  $w = [W32]::GetSystemMetrics(78); $h = [W32]::GetSystemMetrics(79)
  $bmp = New-Object System.Drawing.Bitmap $w, $h
  $g = [System.Drawing.Graphics]::FromImage($bmp)
  $g.CopyFromScreen($x, $y, 0, 0, $bmp.Size)
  $path = Join-Path $OutDir $name
  $bmp.Save($path, [System.Drawing.Imaging.ImageFormat]::Png)
  $g.Dispose(); $bmp.Dispose()
  return "$path (${w}x${h})"
}

function Test-Docx([string]$path) {
  Assert (Test-Path $path) "输出不存在：$path"
  $size = (Get-Item $path).Length
  $zip = [System.IO.Compression.ZipFile]::OpenRead($path)
  try {
    $entry = $zip.GetEntry('word/document.xml')
    Assert ($null -ne $entry) "$path 中没有 word/document.xml"
    $sr = New-Object System.IO.StreamReader($entry.Open())
    $xml = $sr.ReadToEnd(); $sr.Close()
    Assert ($xml.Length -gt 200) "document.xml 太短"
    return @{ Size = $size; Xml = $xml; Entries = $zip.Entries.Count }
  } finally { $zip.Dispose() }
}

# 与 internal/converter 完全一致的调用方式
function Invoke-Convert([string]$mode, [string]$in, [string]$out) {
  $psi = New-Object System.Diagnostics.ProcessStartInfo
  $psi.FileName = $Python
  foreach ($a in @('-B', '-s', '-X', 'utf8', $Script, '--mode', $mode)) { $psi.ArgumentList.Add($a) }
  if ($in) { $psi.ArgumentList.Add('--input'); $psi.ArgumentList.Add($in); $psi.ArgumentList.Add('--output'); $psi.ArgumentList.Add($out) }
  $psi.WorkingDirectory = Split-Path $Script
  $psi.UseShellExecute = $false; $psi.CreateNoWindow = $true
  $psi.RedirectStandardOutput = $true; $psi.RedirectStandardError = $true
  $psi.StandardOutputEncoding = [System.Text.Encoding]::UTF8; $psi.StandardErrorEncoding = [System.Text.Encoding]::UTF8
  $psi.Environment['PYTHONIOENCODING'] = 'utf-8'; $psi.Environment['PYTHONUTF8'] = '1'; $psi.Environment['PYTHONDONTWRITEBYTECODE'] = '1'
  $sw = [Diagnostics.Stopwatch]::StartNew()
  $p = [System.Diagnostics.Process]::Start($psi)
  $errTask = $p.StandardError.ReadToEndAsync()
  $stdout = $p.StandardOutput.ReadToEnd()
  $p.WaitForExit()
  $stderr = $errTask.Result
  return @{ Code = $p.ExitCode; Out = $stdout; Err = $stderr; Ms = $sw.ElapsedMilliseconds }
}

Log "Installer: $Installer ($([math]::Round((Get-Item $Installer).Length/1MB,1)) MB)"
Log ("OS: " + [Environment]::OSVersion.VersionString + "  user=" + $env:USERNAME + "  session=" + [System.Diagnostics.Process]::GetCurrentProcess().SessionId)

# ------------------------------------------------------------------ 1. 静默安装
Step 'install (/S)' {
  $sw = [Diagnostics.Stopwatch]::StartNew()
  $p = Run-Timed $Installer @('/S') 300
  Assert ($p.ExitCode -eq 0) "安装程序退出码 $($p.ExitCode)"
  foreach ($f in @($Exe, $Python, $Script, $FFmpeg, (Join-Path $InstallDir 'uninstall.exe'),
                   (Join-Path $InstallDir 'resources\python\python312.dll'),
                   (Join-Path $InstallDir 'resources\python\msvcp140.dll'),
                   (Join-Path $InstallDir 'resources\python\Lib\site-packages\fitz'),
                   (Join-Path $InstallDir 'resources\python\Lib\site-packages\pdf2docx'))) {
    Assert (Test-Path $f) "缺少 $f"
  }
  $files = (Get-ChildItem -Recurse -File $InstallDir | Measure-Object -Property Length -Sum)
  $lnk = Test-Path (Join-Path ([Environment]::GetFolderPath('CommonDesktopDirectory')) '桌面工具箱.lnk')
  $uk = Get-ChildItem 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall' | Where-Object { $_.GetValue('DisplayName') -eq '桌面工具箱' }
  Assert ($null -ne $uk) "没有卸载注册表项"
  "exit 0 in $($sw.ElapsedMilliseconds)ms; $($files.Count) files, $([math]::Round($files.Sum/1MB,1)) MB; desktop shortcut=$lnk; uninstall key=$($uk.PSChildName)"
}

# ------------------------------------------------------------------ 2. 隐藏自检（热键 / 剪贴板 / 截屏 / 搜索 / 转换组件）
Step 'app --selftest' {
  $st = Join-Path $OutDir 'selftest'
  $log = Join-Path $st 'selftest.log'
  try { $p = Run-Timed $Exe @('--selftest', "--selftest-out=$st") 240 }
  catch { if (Test-Path $log) { Get-Content $log -Encoding utf8 | ForEach-Object { Log "  | $_" } }; throw }
  Assert (Test-Path $log) "没有生成 selftest.log（退出码 $($p.ExitCode)）"
  Get-Content $log -Encoding utf8 | ForEach-Object { Log "  | $_" }
  Assert ($p.ExitCode -eq 0) "selftest 退出码 $($p.ExitCode)"
  "exit 0"
}

# ------------------------------------------------------------------ 3. 启动程序、主窗口、截图、单实例
Step 'launch + main window' {
  Get-Process DesktopToolbox -ErrorAction SilentlyContinue | Stop-Process -Force
  $env:DT_UI_PROBE = Join-Path $OutDir 'ui-probe.txt'
  Remove-Item $env:DT_UI_PROBE -ErrorAction SilentlyContinue
  $p = Start-Process -FilePath $Exe -PassThru
  Start-Sleep -Seconds 15
  $p.Refresh()
  Assert (-not $p.HasExited) "进程已退出，退出码 $($p.ExitCode)"
  $title = $p.MainWindowTitle
  $hwnd = [W32]::FindWindow('DesktopToolboxMain', [NullString]::Value)
  $visible = ($hwnd -ne [IntPtr]::Zero) -and [W32]::IsWindowVisible($hwnd)
  $wv = Get-Process msedgewebview2 -ErrorAction SilentlyContinue
  $shot = Save-Screenshot 'ui-main.png'
  Assert ($title -eq '桌面工具箱') "MainWindowTitle='$title'"
  Assert $visible "主窗口（类 DesktopToolboxMain）不可见"
  Assert ($wv.Count -gt 0) "没有 msedgewebview2 进程（WebView2 未启动）"
  Assert ($p.MainWindowHandle -eq $hwnd) "MainWindowHandle=$($p.MainWindowHandle) 与 FindWindow(DesktopToolboxMain)=$hwnd 不一致"
  "pid=$($p.Id) title='$title' hwnd=$hwnd visible=$visible webview2 procs=$($wv.Count) ws=$([math]::Round($p.WorkingSet64/1MB))MB; screenshot $shot"
}
Step 'global hotkeys (SendInput)' {
  $hwnd = [W32]::FindWindow('DesktopToolboxMain', [NullString]::Value)
  Assert ($hwnd -ne [IntPtr]::Zero) "找不到主窗口"
  $state = { "visible=$([W32]::IsWindowVisible($hwnd)) fg=$([W32]::GetForegroundWindow() -eq $hwnd)" }
  $notes = @()
  # Alt+Space：可见且在前台 → 隐藏；否则 → 显示并置前
  $seenHide = $false; $seenShow = $false
  for ($i = 0; $i -lt 4 -and -not ($seenHide -and $seenShow); $i++) {
    $wasShown = [W32]::IsWindowVisible($hwnd) -and ([W32]::GetForegroundWindow() -eq $hwnd)
    [W32]::Keys(0x12, 0x20) | Out-Null   # Alt+Space
    Start-Sleep -Milliseconds 1500
    $vis = [W32]::IsWindowVisible($hwnd); $fg = [W32]::GetForegroundWindow() -eq $hwnd
    $notes += "Alt+Space#$($i+1): before shown=$wasShown -> $(& $state)"
    if ($wasShown -and -not $vis) { $seenHide = $true }
    if (-not $wasShown -and $vis -and $fg) { $seenShow = $true }
  }
  $notes | ForEach-Object { Log "  $_" }
  Assert ($seenHide -and $seenShow) "Alt+Space 未能切换显示/隐藏（hide=$seenHide show=$seenShow）"
  # 确保主窗口可见，然后 Ctrl+Shift+V → 剪贴板小窗口（440x640）
  if (-not [W32]::IsWindowVisible($hwnd)) { [W32]::Keys(0x12, 0x20) | Out-Null; Start-Sleep -Milliseconds 1500 }
  [W32]::Keys(0x11, 0x10, 0x56) | Out-Null   # Ctrl+Shift+V
  Start-Sleep -Milliseconds 2000
  $r = New-Object W32+RECT; [W32]::GetWindowRect($hwnd, [ref]$r) | Out-Null
  $pw = $r.Right - $r.Left; $ph = $r.Bottom - $r.Top
  Save-Screenshot 'ui-clip-popup.png' | Out-Null
  Log "  Ctrl+Shift+V: $(& $state) size=${pw}x${ph}"
  Assert ([W32]::IsWindowVisible($hwnd) -and $pw -lt 700) "剪贴板小窗口未出现（size=${pw}x${ph}）"
  [W32]::Keys(0x11, 0x10, 0x56) | Out-Null   # 再按一次：小窗口在前台时隐藏
  Start-Sleep -Milliseconds 1500
  Log "  Ctrl+Shift+V again: $(& $state)"
  $popupHidden = -not [W32]::IsWindowVisible($hwnd)
  # 恢复主窗口
  [W32]::Keys(0x12, 0x20) | Out-Null; Start-Sleep -Milliseconds 1500
  [W32]::GetWindowRect($hwnd, [ref]$r) | Out-Null
  $mw = $r.Right - $r.Left
  Log "  Alt+Space restore: $(& $state) width=$mw"
  # Ctrl+Alt+A → 截图遮罩（类 DesktopToolboxOverlay），Esc 取消
  [W32]::Keys(0x11, 0x12, 0x41) | Out-Null   # Ctrl+Alt+A
  $ov = [IntPtr]::Zero
  for ($k = 0; $k -lt 20 -and $ov -eq [IntPtr]::Zero; $k++) { Start-Sleep -Milliseconds 200; $ov = [W32]::FindWindow('DesktopToolboxOverlay', [NullString]::Value) }
  Assert ($ov -ne [IntPtr]::Zero) "Ctrl+Alt+A 后 4 秒内没有出现截图遮罩"
  Start-Sleep -Milliseconds 500
  Save-Screenshot 'ui-screenshot-overlay.png' | Out-Null
  $ovFg = [W32]::GetForegroundWindow() -eq $ov
  [W32]::Keys(0x1B) | Out-Null   # Esc
  $gone = $false
  for ($k = 0; $k -lt 15 -and -not $gone; $k++) { Start-Sleep -Milliseconds 200; $gone = -not [W32]::IsWindowVisible($ov) }
  Assert $gone "按 Esc 后截图遮罩没有关闭（遮罩在前台=$ovFg）"
  Assert ($mw -gt 800) "Alt+Space 后主窗口未恢复正常尺寸（宽 $mw）"
  "Alt+Space hide+show OK; Ctrl+Shift+V popup ${pw}x${ph} (second press hides=$popupHidden); main restored width=$mw; Ctrl+Alt+A overlay shown (foreground=$ovFg) and closed by Esc"
}
Step 'open every tool page' {
  # 通过第二实例 --show=<页面> 让主程序逐页切换，前端回报渲染结果到 DT_UI_PROBE，并逐页截图
  $views = 'search','shot','clipboard','convert','record','ocr','inpaint','meeting','settings'
  foreach ($v in $views) {
    $p2 = Start-Process -FilePath $Exe -ArgumentList "--show=$v" -PassThru
    $p2.WaitForExit(15000) | Out-Null
    Start-Sleep -Milliseconds 1800
    Save-Screenshot "ui-view-$v.png" | Out-Null
  }
  Assert (Test-Path $env:DT_UI_PROBE) "前端没有回报任何页面（ui-probe.txt 不存在）"
  $lines = Get-Content $env:DT_UI_PROBE -Encoding utf8
  $lines | ForEach-Object { Log "  $_" }
  $bad = @()
  foreach ($v in $views) {
    $hit = $lines | Where-Object { $_ -like "$v`t*" } | Select-Object -Last 1
    if (-not $hit -or ($hit -split "`t")[1] -ne 'true') { $bad += $v }
  }
  Assert ($bad.Count -eq 0) "以下页面未正常打开：$($bad -join ', ')"
  "all $($views.Count) pages rendered"
}
Step 'app icon (exe resource)' {
  Add-Type -AssemblyName System.Drawing
  $ico = [System.Drawing.Icon]::ExtractAssociatedIcon($Exe)
  $png = Join-Path $OutDir 'exe-icon.png'
  $ico.ToBitmap().Save($png, [System.Drawing.Imaging.ImageFormat]::Png)
  $bmp = $ico.ToBitmap()
  $orange = 0
  for ($x = 0; $x -lt $bmp.Width; $x++) { for ($y = 0; $y -lt $bmp.Height; $y++) { $c = $bmp.GetPixel($x, $y); if ($c.A -gt 200 -and $c.R -gt 200 -and $c.G -gt 90 -and $c.G -lt 190 -and $c.B -lt 120) { $orange++ } } }
  Assert ($orange -gt 0) "exe 图标中没有橙色像素，可能仍是旧图标"
  "icon $($bmp.Width)x$($bmp.Height) orange px=$orange -> $png"
}
Step 'single instance' {
  $first = @(Get-Process DesktopToolbox -ErrorAction SilentlyContinue)
  Assert ($first.Count -eq 1) "启动第二个实例前有 $($first.Count) 个进程"
  $p2 = Start-Process -FilePath $Exe -PassThru
  $exited = $p2.WaitForExit(15000)
  Start-Sleep -Seconds 2
  $now = @(Get-Process DesktopToolbox -ErrorAction SilentlyContinue)
  Save-Screenshot 'ui-after-second-launch.png' | Out-Null
  Assert $exited "第二个实例 15 秒内没有退出"
  Assert ($now.Count -eq 1 -and $now[0].Id -eq $first[0].Id) "现在有 $($now.Count) 个进程"
  "second instance exited (code $($p2.ExitCode)); original pid $($first[0].Id) still running"
}
Step 'stop app' {
  $procs = @(Get-Process DesktopToolbox -ErrorAction SilentlyContinue)
  foreach ($p in $procs) { $p.CloseMainWindow() | Out-Null }
  Start-Sleep -Seconds 2
  # 关闭窗口只会隐藏到托盘，所以仍应在运行
  $still = @(Get-Process DesktopToolbox -ErrorAction SilentlyContinue)
  $hidden = $still.Count -eq 1
  $still | Stop-Process -Force
  Start-Sleep -Seconds 1
  "close-to-tray kept process alive=$hidden; killed"
}

# ------------------------------------------------------------------ 4. 内置 Python 转换
Step 'python selftest' {
  $r = Invoke-Convert 'selftest' $null $null
  Log "  stdout: $($r.Out.Trim())"
  if ($r.Err) { Log "  stderr: $($r.Err.Trim())" }
  Assert ($r.Code -eq 0 -and $r.Out -match '"done"') "退出码 $($r.Code)"
  "ok in $($r.Ms)ms"
}
$convDir = Join-Path $OutDir 'convert'
New-Item -ItemType Directory -Force -Path $convDir | Out-Null
# 输入放到含中文与空格的路径，模拟真实用户
$inDir = Join-Path $env:TEMP '转换 测试'
New-Item -ItemType Directory -Force -Path $inDir | Out-Null
Copy-Item (Join-Path $Samples '*') $inDir -Recurse -Force
Step 'md -> docx' {
  $out = Join-Path $convDir 'sample-md.docx'
  $r = Invoke-Convert 'md2docx' (Join-Path $inDir 'sample.md') $out
  if ($r.Code -ne 0) { Log "  stdout: $($r.Out)"; Log "  stderr: $($r.Err)" }
  Assert ($r.Code -eq 0 -and $r.Out -match '"done"') "退出码 $($r.Code)"
  $d = Test-Docx $out
  Assert ($d.Xml -match '<w:tbl>') "没有表格"
  "$($d.Size) bytes, $($d.Entries) zip entries, has table; $($r.Ms)ms"
}
Step 'md (GBK) -> docx' {
  $out = Join-Path $convDir 'gbk-md.docx'
  $r = Invoke-Convert 'md2docx' (Join-Path $inDir 'gbk.md') $out
  Assert ($r.Code -eq 0) "退出码 $($r.Code) $($r.Out) $($r.Err)"
  $d = Test-Docx $out
  "$($d.Size) bytes"
}
Step 'pdf -> docx' {
  $out = Join-Path $convDir 'text-pdf.docx'
  $r = Invoke-Convert 'pdf2docx' (Join-Path $inDir 'text.pdf') $out
  if ($r.Code -ne 0) { Log "  stdout: $($r.Out)"; Log "  stderr: $($r.Err)" }
  Assert ($r.Code -eq 0 -and $r.Out -match '"done"') "退出码 $($r.Code)"
  $d = Test-Docx $out
  "$($d.Size) bytes, $($d.Entries) zip entries; $($r.Ms)ms"
}
Step 'scanned pdf -> clear error' {
  $r = Invoke-Convert 'pdf2docx' (Join-Path $inDir 'scanned.pdf') (Join-Path $convDir 'scanned.docx')
  Assert ($r.Out -match '"code":\s*"scanned"') "未返回 scanned 错误：$($r.Out)"
  "reported code=scanned"
}

# ------------------------------------------------------------------ 5. FFmpeg gdigrab 录屏（与程序相同的编码参数）
Step 'ffmpeg gdigrab 3s' {
  $desk = [Environment]::GetFolderPath('Desktop')
  $mp4 = Join-Path $desk '录屏_CI测试.mp4'
  Remove-Item $mp4 -ErrorAction SilentlyContinue
  # 与 internal/recorder.BuildArgs 相同的参数（全屏 = 主显示器，宽高取偶数）
  $b = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
  $w = $b.Width - ($b.Width % 2); $h = $b.Height - ($b.Height % 2)
  $ffArgs = @('-hide_banner', '-loglevel', 'warning', '-y', '-thread_queue_size', '512',
              '-f', 'gdigrab', '-framerate', '30', '-draw_mouse', '1',
              '-offset_x', "$($b.X)", '-offset_y', "$($b.Y)", '-video_size', "${w}x${h}", '-i', 'desktop',
              '-map', '0:v:0', '-c:v', 'libx264', '-preset', 'ultrafast', '-tune', 'zerolatency', '-crf', '23',
              '-pix_fmt', 'yuv420p', '-r', '30', '-t', '3', '-movflags', '+faststart', $mp4)
  $sw = [Diagnostics.Stopwatch]::StartNew()
  $ErrorActionPreference = 'Continue'
  & $FFmpeg @ffArgs 2> (Join-Path $OutDir 'ffmpeg-stderr.txt')
  $code = $LASTEXITCODE
  $ErrorActionPreference = 'Stop'
  Get-Content (Join-Path $OutDir 'ffmpeg-stderr.txt') -ErrorAction SilentlyContinue | ForEach-Object { Log "  ffmpeg: $_" }
  Assert ($code -eq 0) "ffmpeg 退出码 $code"
  Assert (Test-Path $mp4) "没有输出文件"
  $size = (Get-Item $mp4).Length
  Assert ($size -gt 0) "文件大小为 0"
  $ErrorActionPreference = 'Continue'
  if ($FFprobe -and (Test-Path $FFprobe)) {
    $info = & $FFprobe -v error -show_entries 'format=duration:stream=codec_name,width,height,pix_fmt' -of 'default=nw=1' $mp4
    Log "  ffprobe: $($info -join ' ')"
    $dur = [double](($info | Where-Object { $_ -like 'duration=*' }) -replace 'duration=', '')
  } else {
    $probe = (& $FFmpeg -hide_banner -i $mp4 2>&1 | ForEach-Object { "$_" }) -join "`n"
    $m = [regex]::Match($probe, 'Duration: (\d+):(\d+):([\d.]+)')
    $dur = [int]$m.Groups[1].Value * 3600 + [int]$m.Groups[2].Value * 60 + [double]$m.Groups[3].Value
  }
  $ErrorActionPreference = 'Stop'
  Copy-Item $mp4 (Join-Path $OutDir 'gdigrab-3s.mp4')
  Assert ($dur -ge 2.5 -and $dur -le 3.6) "时长 $dur 秒"
  "$mp4 ${w}x${h} $size bytes, duration ${dur}s, wall $($sw.ElapsedMilliseconds)ms"
}

# ------------------------------------------------------------------ 6. 静默卸载
Step 'uninstall (/S)' {
  $un = Join-Path $InstallDir 'uninstall.exe'
  $p = Run-Timed $un @('/S') 120
  # NSIS 卸载程序会把自己复制到临时目录再运行，这里轮询直到安装目录被删除
  $deadline = (Get-Date).AddSeconds(90)
  while ((Test-Path $InstallDir) -and (Get-Date) -lt $deadline) { Start-Sleep -Milliseconds 500 }
  Start-Sleep -Seconds 2
  if (Test-Path -LiteralPath $InstallDir) {
    throw "安装目录仍存在：$((Get-ChildItem -LiteralPath $InstallDir -Recurse | Select-Object -First 10 | ForEach-Object FullName) -join ', ')"
  }
  $uk = Get-ChildItem 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall' | Where-Object { $_.GetValue('DisplayName') -eq '桌面工具箱' }
  Assert ($null -eq $uk) "卸载注册表项仍存在"
  Assert (-not (Test-Path 'HKLM:\SOFTWARE\DesktopToolbox')) "HKLM\SOFTWARE\DesktopToolbox 仍存在"
  $lnk = Join-Path ([Environment]::GetFolderPath('CommonDesktopDirectory')) '桌面工具箱.lnk'
  Assert (-not (Test-Path $lnk)) "桌面快捷方式仍存在"
  $data = Join-Path $env:APPDATA 'DesktopToolbox'
  "removed install dir, uninstall key, HKLM key, shortcut (uninstaller exit $($p.ExitCode)); %APPDATA%\DesktopToolbox exists=$(Test-Path $data)"
}

Log ""
if ($script:Failures.Count -gt 0) {
  Log "SMOKE RESULT: $($script:Failures.Count) FAILED: $($script:Failures -join '; ')"
  exit 1
}
Log "SMOKE RESULT: ALL PASSED"
exit 0
