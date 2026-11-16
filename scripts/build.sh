#!/usr/bin/env bash
# 桌面工具箱 一键构建脚本（在 Linux 上交叉编译 Windows x64 安装包）
# 用法：bash scripts/build.sh
set -euo pipefail
ROOT="${ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
cd "$ROOT"
TOOLS="${TOOLS:-/workspace/tools}"
[ -f "$TOOLS/env.sh" ] && source "$TOOLS/env.sh"

VERSION=0.1.0
PY_VER=3.12.10
DL=third_party/dl
STAGE=build/stage/resources
DIST=dist

echo "==> 1/6 下载第三方组件（已存在则跳过）"
mkdir -p "$DL" third_party/wheels third_party/ffmpeg
[ -f "$DL/python-$PY_VER-embed-amd64.zip" ] || curl -fsSL -o "$DL/python-$PY_VER-embed-amd64.zip" "https://www.python.org/ftp/python/$PY_VER/python-$PY_VER-embed-amd64.zip"
if [ ! -f third_party/ffmpeg/ffmpeg.exe ]; then
  [ -f "$DL/ffmpeg-ess.zip" ] || curl -fsSL -o "$DL/ffmpeg-ess.zip" "https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip"
  unzip -o -j -q "$DL/ffmpeg-ess.zip" '*/bin/ffmpeg.exe' '*/LICENSE' -d third_party/ffmpeg
fi
if ! ls third_party/wheels/pdf2docx-*.whl >/dev/null 2>&1; then
  pip3 download --platform win_amd64 --python-version 3.12 --implementation cp --abi cp312 --only-binary=:all: \
    -d third_party/wheels pdf2docx==0.5.13 python-docx markdown "PyMuPDF>=1.26.7,<1.27" "opencv-python-headless<4.13" "numpy<2.4"
fi
ls "$DL"/msvc_runtime-*.whl >/dev/null 2>&1 || pip3 download --platform win_amd64 --only-binary=:all: --no-deps -d "$DL" msvc-runtime

echo "==> 2/6 准备内置 Python（嵌入版 + 依赖）"
rm -rf build/stage && mkdir -p "$STAGE/python/Lib/site-packages" "$STAGE/scripts" "$STAGE/ffmpeg"
unzip -q "$DL/python-$PY_VER-embed-amd64.zip" -d "$STAGE/python"
cat > "$STAGE/python/python312._pth" <<'PTH'
python312.zip
.
Lib\site-packages

# enable site-packages
import site
PTH
for w in third_party/wheels/*.whl; do unzip -q -o "$w" -d "$STAGE/python/Lib/site-packages"; done
SP="$STAGE/python/Lib/site-packages"
# 清理：wheel 的 .data 目录、测试、缓存、空占位文件
rm -rf "$SP"/*.data "$SP"/*.whl
find "$SP" -type d \( -name "__pycache__" -o -name "tests" \) -prune -exec rm -rf {} + 2>/dev/null || true
# VC++ 运行库（PyMuPDF 依赖 msvcp140.dll；未安装 VC++ 运行库的电脑也能运行）
TMPV=$(mktemp -d); unzip -q -o "$DL"/msvc_runtime-*.whl -d "$TMPV"
cp "$TMPV"/*.data/data/msvcp140.dll "$TMPV"/*.data/data/msvcp140_1.dll "$TMPV"/*.data/data/msvcp140_2.dll \
   "$TMPV"/*.data/data/msvcp140_atomic_wait.dll "$TMPV"/*.data/data/msvcp140_codecvt_ids.dll \
   "$TMPV"/*.data/data/Scripts/vcruntime140.dll "$TMPV"/*.data/data/Scripts/vcruntime140_1.dll "$STAGE/python/"
rm -rf "$TMPV"
cp resources/scripts/convert.py "$STAGE/scripts/"
# 离线 AI 引擎脚本（模型与 AI 依赖首次使用时下载）
mkdir -p "$STAGE/ai-engine" && cp -r ai-engine/dtai ai-engine/dtai_main.py "$STAGE/ai-engine/" && find "$STAGE/ai-engine" -name __pycache__ -prune -exec rm -rf {} +
# 预编译 .pyc（unchecked-hash：不校验源码时间戳）。安装到 Program Files 后 Python 无法写缓存，预编译可显著加快启动。
# 本机 Python 必须同为 3.12.x（字节码格式一致）
python3 -c 'import sys; assert sys.version_info[:2]==(3,12), sys.version'
python3 -m compileall -q -j 0 --invalidation-mode unchecked-hash "$SP" "$STAGE/scripts" >/dev/null || true
cp third_party/ffmpeg/ffmpeg.exe third_party/ffmpeg/LICENSE "$STAGE/ffmpeg/"
du -sh "$STAGE"/*

echo "==> 3/6 单元测试（Linux）"
go vet ./... && GOOS=windows GOARCH=amd64 go vet ./... && GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go vet ./...
go test ./internal/...

echo "==> 4/6 编译 Windows 程序并生成 NSIS 安装包"
wails build -platform windows/amd64 -nsis -skipbindings -trimpath -ldflags "-s -w" -webview2 download

echo "==> 5/6 输出安装包"
mkdir -p "$DIST"
cp build/bin/DesktopToolbox-amd64-installer.exe "$DIST/桌面工具箱-Setup-x64.exe"

echo "==> 6/6 生成便携版 zip"
PORT=build/portable/桌面工具箱
rm -rf build/portable && mkdir -p "$PORT"
cp build/bin/DesktopToolbox.exe "$PORT/"
cp -r "$STAGE" "$PORT/resources"
cat > "$PORT/使用说明.txt" <<'TXT'
桌面工具箱 便携版 v0.1.0
双击 DesktopToolbox.exe 运行，关闭窗口后驻留在系统托盘。
快捷键：Alt+Space 显示主窗口；Ctrl+Alt+A 截图；Ctrl+Shift+V 剪贴板历史。
需要 Windows 10/11 及 Microsoft Edge WebView2 运行时（Win11 自带，Win10 一般已随 Edge 安装）。
数据保存在 %APPDATA%\DesktopToolbox。
TXT
(cd build/portable && rm -f "../../$DIST/桌面工具箱-便携版-x64.zip" && zip -q -r -6 "../../$DIST/桌面工具箱-便携版-x64.zip" 桌面工具箱)
ls -la "$DIST"
