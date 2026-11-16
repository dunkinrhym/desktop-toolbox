# 桌面工具箱 v0.1.0

一个简洁的 Windows 桌面效率工具（Go + Wails v2，界面为中文、浅色、类 Apple 风格），完全离线运行。
安装后常驻系统托盘，通过全局快捷键一键调用。

## 功能

| 工具 | 说明 |
| --- | --- |
| **文件搜索** | 按文件名搜索（多个关键词用空格分隔，需全部命中，不区分大小写），范围可选“所有磁盘”（固定盘 + 可移动盘）或指定文件夹（也可把文件夹拖进窗口）。可选“搜索文件内容”：支持 txt / md / csv / 代码 / 日志 / json / xml / html 等文本（自动识别 UTF-8、UTF-16、GBK/GB18030），以及 .docx、.xlsx、.pptx、.pdf。并发遍历，自动跳过 Windows、$Recycle.Bin、System Volume Information、node_modules、.git 等系统/隐藏/依赖目录；内容搜索单文件上限 50MB；结果实时流式显示（上限 5000 条），可随时停止；可按名称/大小/时间排序；可按扩展名筛选。双击打开文件（系统默认程序）；右键菜单：打开 / 打开所在文件夹（资源管理器中选中）/ 复制路径。右侧**预览面板**显示提取出的文字，所有匹配处以**黄色高亮**，可“上一处/下一处”（F3 / Shift+F3）跳转——只读预览，**绝不修改原文件**。 |
| **截图** | 快捷键或按钮触发，冻结所有显示器画面，拖动框选区域（实时显示尺寸，可拖动移动选区，单击选中整个屏幕）。工具栏：**复制**（同时写入 CF_DIB 和 PNG 格式，可直接粘贴到 Word / 微信 / 邮件，Word 会自动缩放到页面宽度）、**保存**（PNG，默认桌面，可选路径）、**取消**。Esc 取消，右键重选，回车/双击/Ctrl+C 复制，Ctrl+S 保存。Per-Monitor V2 DPI 感知，按物理像素截图，支持多显示器/不同缩放比例。截图同时记入剪贴板历史。 |
| **超级复制** | 后台记录剪贴板历史（文字和图片，最近 100 条，置顶的不计入淘汰），保存在 `%APPDATA%\DesktopToolbox\clipboard`。支持搜索、置顶、删除、清空（保留置顶）、暂停记录；点击条目即复制回剪贴板；“多选”（或 Ctrl+点击 / 空格）按选择顺序把多条文本用换行合并复制。`Ctrl+Shift+V` 弹出小窗口列表：↑↓ 选择、Enter 复制并隐藏、Esc 关闭。会遵守密码管理器设置的“不记录”标记（ExcludeClipboardContentFromMonitorProcessing / CanIncludeInClipboardHistory）。 |
| **格式转换** | PDF → Word（pdf2docx）与 Markdown → Word（Python-Markdown + python-docx，自写转换器：标题、粗体/斜体/删除线、行内代码、代码块、有序/无序/嵌套列表、引用、表格（含对齐）、超链接、本地图片（自动限制为页面宽度）、分隔线；中文字体设为微软雅黑；Markdown 自动识别 UTF-8/GBK）。支持拖放或选择多个文件批量转换，输出到原文件旁（重名自动加 (1)(2)…）或指定文件夹，显示进度，完成后可“打开文件 / 所在文件夹”。扫描件（纯图片）PDF 会明确提示“需要 OCR，本工具不包含 OCR”；加密 PDF 也会提示。使用随附的嵌入式 Python 3.12 运行，后台无控制台窗口。 |
| **录屏** | 内置 FFmpeg（gdigrab）。模式：全屏（鼠标所在显示器）/ 选择区域（复用截图选框）/ 预设 1920×1080、1280×720（固定大小框跟随鼠标，单击放置）。H.264（libx264 ultrafast，yuv420p，宽高自动取偶数），默认 30fps（可选 15/60），可选是否录制鼠标指针；可选麦克风（dshow，默认不录音）。录制时屏幕上方显示悬浮计时条和“停止”按钮（可拖动），区域录制时显示红色边框；两者都设置了“不被捕获”（Win10 2004+），不会出现在视频中。停止时向 FFmpeg 发送 `q` 正常收尾，保存为 `桌面\录屏_日期_时间.mp4`。 |

不包含（按需求明确排除）：OCR、翻译、去水印、语音/会议记录。

## 全局快捷键

| 快捷键 | 功能 |
| --- | --- |
| `Alt + Space` | 显示 / 隐藏主窗口 |
| `Ctrl + Alt + A` | 截图 |
| `Ctrl + Shift + V` | 剪贴板历史（小窗口） |

若快捷键被其他程序占用，启动时会提示，“设置与快捷键”页面会显示状态。
关闭主窗口只是隐藏到托盘；托盘图标左键显示主窗口，右键菜单可截图、打开剪贴板历史、录屏、退出。
程序为单实例：重复启动会唤出已运行的窗口。可在设置中开启“开机自动启动（后台运行）”。

## 安装

运行 `dist/桌面工具箱-Setup-x64.exe`（需要管理员权限，默认安装到 `C:\Program Files\DesktopToolbox`，创建开始菜单和桌面快捷方式）。
也可以使用便携版 `dist/桌面工具箱-便携版-x64.zip`，解压后运行 `DesktopToolbox.exe`。

系统要求：Windows 10 / 11（x64），Microsoft Edge WebView2 运行时（Win11 自带；Win10 一般已随 Edge 安装。若缺失，安装程序会尝试在线安装，此步骤需要联网）。

卸载：控制面板 / 设置 → 应用 → 桌面工具箱，卸载时会询问是否删除剪贴板历史与设置。

## 从源码构建（Linux 交叉编译）

已在 Debian 13 aarch64 上验证。需要：Go ≥ 1.26、Wails CLI v2.17、NSIS（makensis）、Python 3.12（用于下载 Windows 轮子、预编译 .pyc、运行测试）、zip/unzip、curl。

```bash
# 一次性准备
curl -sSL https://go.dev/dl/go1.27.4.linux-arm64.tar.gz | tar xz -C /workspace/tools   # 或 amd64 版本
source /workspace/tools/env.sh            # 设置 GOROOT/GOPATH/PATH
go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.1
sudo apt-get install -y nsis zip unzip
python3 -m venv /workspace/tools/pyenv && /workspace/tools/pyenv/bin/pip install pdf2docx==0.5.13 python-docx markdown "PyMuPDF<1.27" "opencv-python-headless<4.13" "numpy<2.4"   # 仅用于测试

# 一键构建（下载组件 → 准备内置 Python/FFmpeg → 测试 → 编译 → NSIS 安装包 → 便携版 zip）
bash scripts/build.sh
```

构建脚本步骤：

1. 下载 `python-3.12.10-embed-amd64.zip`、gyan.dev FFmpeg essentials（只取 `ffmpeg.exe`）、用
   `pip download --platform win_amd64 --python-version 3.12 --only-binary=:all:` 下载 pdf2docx、python-docx、markdown、PyMuPDF 1.26、opencv-python-headless、numpy 等 Windows 轮子，以及 `msvc-runtime`（提供 msvcp140.dll，PyMuPDF 需要）。
2. 解压嵌入式 Python，改写 `python312._pth` 启用 `Lib\site-packages` 与 `import site`，把轮子解压进 site-packages，复制 VC++ 运行库，预编译 .pyc（unchecked-hash）。
3. `go vet`（linux 与 windows）+ `go test ./internal/...`（含调用本机 Python 测试转换脚本、调用本机 ffmpeg 测试录屏收尾逻辑）。
4. `wails build -platform windows/amd64 -nsis -skipbindings -trimpath -ldflags "-s -w"`（CGO_ENABLED=0，纯 Go）。
5. 安装包复制为 `dist/桌面工具箱-Setup-x64.exe`。
6. 生成便携版 `dist/桌面工具箱-便携版-x64.zip`。

单独运行测试：

```bash
go test ./internal/...                                   # 搜索、内容提取、剪贴板存储、DIB 编解码、录屏参数、转换
/workspace/tools/pyenv/bin/python resources/scripts/convert.py --mode md2docx --input tests/samples/sample.md --output /tmp/a.docx
```

界面预览（无需 Windows）：`chromium --headless --screenshot=ui.png "file://$PWD/tests/ui/preview.html?view=search"`（view 可为 search / shot / clipboard / convert / record / settings / popup），使用 `tests/ui/mock.js` 模拟后端。

## 目录结构

```
main.go, app*.go          Wails 应用与前端绑定（仅 Windows）
internal/search           并发文件名/内容搜索（纯 Go，可在 Linux 测试）
internal/extract          txt/docx/xlsx/pptx/pdf 文本提取与编码识别
internal/cliphist         剪贴板历史存储
internal/dib              剪贴板 DIB ⇄ 图像转换
internal/recorder         FFmpeg 录屏（参数构建、设备列表解析、启动/停止）
internal/converter        调用内置 Python 转换脚本
internal/win              Win32 封装（纯 syscall）：原生 UI 线程、全局热键、剪贴板、托盘、截屏、选区遮罩、录屏悬浮条、Shell
frontend/dist             前端（原生 HTML/CSS/JS，无需构建）
resources/scripts         convert.py（PDF/Markdown → Word）
build/windows             图标、清单（Per-Monitor V2 DPI）、NSIS 脚本
scripts/build.sh          一键构建
tests/samples, tests/ui   测试样例与界面预览
```

## 已知限制

- 文件内容搜索是实时遍历（不建索引），全盘内容搜索会比较慢；不支持旧版 .doc/.xls/.ppt。
- PDF 文本提取（搜索/预览）使用纯 Go 库，对部分使用特殊编码字体的中文 PDF 可能提取不全；PDF→Word 转换使用 PyMuPDF，不受此影响。
- PDF→Word 的版式还原取决于 pdf2docx，复杂排版可能需要手动调整；扫描件需要 OCR（不包含）。
- 录屏不录制系统声音（仅可选麦克风）；悬浮条“不被录入”需要 Win10 2004 及以上。
- WebView2 运行时缺失且无网络时无法启动（可预先安装 WebView2 离线包）。
