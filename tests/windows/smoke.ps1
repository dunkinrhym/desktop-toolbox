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
  $p = Start-Process -FilePath $Installer -ArgumentList '/S' -Wait -PassThru
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
  $p = Start-Process -FilePath $Exe -ArgumentList '--selftest', "--selftest-out=$st" -Wait -PassThru
  $log = Join-Path $st 'selftest.log'
  Assert (Test-Path $log) "没有生成 selftest.log（退出码 $($p.ExitCode)）"
  Get-Content $log -Encoding utf8 | ForEach-Object { Log "  | $_" }
  Assert ($p.ExitCode -eq 0) "selftest 退出码 $($p.ExitCode)"
  "exit 0"
}

# ------------------------------------------------------------------ 3. 启动程序、主窗口、截图、单实例
Step 'launch + main window' {
  Get-Process DesktopToolbox -ErrorAction SilentlyContinue | Stop-Process -Force
  $p = Start-Process -FilePath $Exe -PassThru
  Start-Sleep -Seconds 15
  $p.Refresh()
  Assert (-not $p.HasExited) "进程已退出，退出码 $($p.ExitCode)"
  $title = $p.MainWindowTitle
  $hwnd = [W32]::FindWindow('DesktopToolboxMain', $null)
  $visible = ($hwnd -ne [IntPtr]::Zero) -and [W32]::IsWindowVisible($hwnd)
  $wv = Get-Process msedgewebview2 -ErrorAction SilentlyContinue
  $shot = Save-Screenshot 'ui-main.png'
  Assert ($title -eq '桌面工具箱') "MainWindowTitle='$title'"
  Assert $visible "主窗口（类 DesktopToolboxMain）不可见"
  Assert ($wv.Count -gt 0) "没有 msedgewebview2 进程（WebView2 未启动）"
  "pid=$($p.Id) title='$title' hwnd=$hwnd visible=$visible webview2 procs=$($wv.Count) ws=$([math]::Round($p.WorkingSet64/1MB))MB; screenshot $shot"
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
  $p = Start-Process -FilePath $un -ArgumentList '/S' -Wait -PassThru
  # NSIS 卸载程序会把自己复制到临时目录再运行，这里轮询直到安装目录被删除
  $deadline = (Get-Date).AddSeconds(90)
  while ((Test-Path $InstallDir) -and (Get-Date) -lt $deadline) { Start-Sleep -Milliseconds 500 }
  Start-Sleep -Seconds 2
  Assert (-not (Test-Path $InstallDir)) "安装目录仍存在：$((Get-ChildItem -Recurse $InstallDir | Select-Object -First 10 | ForEach-Object FullName) -join ', ')"
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
