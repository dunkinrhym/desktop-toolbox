#!/usr/bin/env bash
# 桌面工具箱 macOS 构建脚本（在 macOS 上运行；GitHub Actions 的 macos-15 / macos-15-intel runner 使用它）
#
# 用法：bash scripts/build-macos.sh arm64|x64
#
# 产物：dist/DesktopToolbox-<版本>-macOS-<arm64|x64>.dmg
#   内含“桌面工具箱.app”（ad-hoc 签名，未公证）和“应用程序”快捷方式。
#
# 步骤：
#   1. 下载 python-build-standalone 的 CPython 3.12（install_only，可重定位）
#   2. 用 pip --platform 为目标架构安装 pdf2docx / PyMuPDF 1.26.x / python-docx / markdown 等轮子，预编译 .pyc
#   3. 下载静态 FFmpeg（ffmpeg.martin-riedl.de，带 libx264 与 avfoundation）
#   4. wails build -platform darwin/<arch>
#   5. 把 python / ffmpeg / scripts 放进 .app/Contents/Resources，改名为“桌面工具箱.app”
#   6. ad-hoc 签名（codesign -s -），自检，打包 DMG（hdiutil）
set -euo pipefail

ARCH="${1:-arm64}"
case "$ARCH" in
  arm64) GOARCH=arm64; PBS_ARCH=aarch64; PIP_PLAT=macosx_12_0_arm64; FF_ARCH=arm64 ;;
  x64|amd64|x86_64) ARCH=x64; GOARCH=amd64; PBS_ARCH=x86_64; PIP_PLAT=macosx_12_0_x86_64; FF_ARCH=amd64 ;;
  *) echo "未知架构：${ARCH}（应为 arm64 或 x64）" >&2; exit 2 ;;
esac

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
VERSION="$(sed -n 's/.*"productVersion": *"\([^"]*\)".*/\1/p' wails.json | head -1)"
PBS_TAG="${PBS_TAG:-20261110}"           # python-build-standalone 发布标签
PY_VER="${PY_VER:-3.12.16}"              # 该标签中的 CPython 3.12 版本
MIN_MACOS="${MIN_MACOS:-12.0}"           # 与 PIP_PLAT、Info.plist 的 LSMinimumSystemVersion 一致
DL="third_party/mac-dl"
STAGE="build/macos/stage-$ARCH"
APP_NAME="桌面工具箱"
DMG="dist/DesktopToolbox-$VERSION-macOS-$ARCH.dmg"
HOST_PY="${HOST_PY:-python3}"            # 宿主 Python 3.12（用于 pip 交叉安装与预编译 .pyc）

echo "==> 构建 macOS ${ARCH}（版本 ${VERSION}）"
"$HOST_PY" -c 'import sys; assert sys.version_info[:2]==(3,12), "需要宿主 Python 3.12，当前 "+sys.version'
mkdir -p "$DL" dist
rm -rf "$STAGE" && mkdir -p "$STAGE/scripts" "$STAGE/ffmpeg"

echo "==> 1/6 内置 Python（python-build-standalone $PBS_TAG / CPython $PY_VER / ${PBS_ARCH}）"
PBS_FILE="cpython-$PY_VER+$PBS_TAG-$PBS_ARCH-apple-darwin-install_only.tar.gz"
PBS_URL="https://github.com/astral-sh/python-build-standalone/releases/download/$PBS_TAG/${PBS_FILE//+/%2B}"
[ -f "$DL/$PBS_FILE" ] || curl -fL --retry 3 -o "$DL/$PBS_FILE" "$PBS_URL"
tar -xzf "$DL/$PBS_FILE" -C "$STAGE"          # 解压出 python/
PYHOME="$STAGE/python"
SP="$PYHOME/lib/python3.12/site-packages"
# 精简：测试、IDLE、Tk、头文件、文档等转换用不到
rm -rf "$PYHOME/lib/python3.12/test" "$PYHOME/lib/python3.12/idlelib" "$PYHOME/lib/python3.12/tkinter" \
       "$PYHOME/lib/python3.12/turtledemo" "$PYHOME/lib/python3.12/ensurepip" "$PYHOME/lib/python3.12/lib2to3" \
       "$PYHOME"/lib/tcl* "$PYHOME"/lib/tk* "$PYHOME"/lib/itcl* "$PYHOME"/lib/thread* \
       "$PYHOME/include" "$PYHOME/share" "$PYHOME"/lib/python3.12/config-3.12-darwin \
       "$PYHOME/lib/python3.12/lib-dynload/_tkinter"*.so
mkdir -p "$SP"

echo "==> 2/6 安装转换依赖（pip --platform ${PIP_PLAT}）"
"$HOST_PY" -m pip install --disable-pip-version-check --no-compile --upgrade \
  --target "$SP" --platform "$PIP_PLAT" --python-version 3.12 --implementation cp --only-binary=:all: \
  pdf2docx==0.5.13 python-docx markdown "PyMuPDF>=1.26.7,<1.27" "opencv-python-headless<4.13" "numpy<2.4"
rm -rf "$SP"/bin "$SP"/share "$SP"/*.dist-info/RECORD
find "$SP" -type d \( -name "__pycache__" -o -name "tests" \) -prune -exec rm -rf {} + 2>/dev/null || true
cp resources/scripts/convert.py "$STAGE/scripts/"
# 预编译 .pyc（unchecked-hash：不校验源码时间戳；.app 内目录不可写，预编译显著加快启动）
"$HOST_PY" -m compileall -q -j 0 --invalidation-mode unchecked-hash "$PYHOME/lib/python3.12" "$STAGE/scripts" >/dev/null || true

echo "==> 3/6 内置 FFmpeg（${FF_ARCH}）"
FF_ZIP="$DL/ffmpeg-$FF_ARCH.zip"
if [ ! -f "$FF_ZIP" ]; then
  curl -fL --retry 3 -o "$FF_ZIP" "https://ffmpeg.martin-riedl.de/redirect/latest/macos/$FF_ARCH/release/ffmpeg.zip" \
  || { [ "$FF_ARCH" = amd64 ] && curl -fL --retry 3 -o "$FF_ZIP" "https://evermeet.cx/ffmpeg/getrelease/zip"; }
fi
unzip -o -q "$FF_ZIP" -d "$STAGE/ffmpeg"
chmod +x "$STAGE/ffmpeg/ffmpeg"
lipo -archs "$STAGE/ffmpeg/ffmpeg" | grep -q "$( [ "$ARCH" = arm64 ] && echo arm64 || echo x86_64 )" \
  || { echo "FFmpeg 架构不符：$(lipo -archs "$STAGE/ffmpeg/ffmpeg")" >&2; exit 1; }
cat > "$STAGE/ffmpeg/LICENSE.txt" <<'TXT'
本目录中的 ffmpeg 为 FFmpeg 项目的静态构建（含 libx264，GPL v2+ 授权），
来源：https://ffmpeg.martin-riedl.de/ （构建脚本：https://git.martin-riedl.de/ffmpeg/build-script）
FFmpeg 源代码：https://ffmpeg.org/download.html
TXT

echo "==> 4/6 编译 macOS 程序（wails build darwin/${GOARCH}）"
export MACOSX_DEPLOYMENT_TARGET="$MIN_MACOS"
export CGO_CFLAGS="${CGO_CFLAGS:-} -mmacosx-version-min=$MIN_MACOS"
export CGO_LDFLAGS="${CGO_LDFLAGS:-} -mmacosx-version-min=$MIN_MACOS"
rm -rf build/bin
wails build -platform "darwin/$GOARCH" -skipbindings -trimpath -ldflags "-s -w" -clean
SRC_APP="$(ls -d build/bin/*.app | head -1)"
OUT_DIR="build/macos/out-$ARCH"
rm -rf "$OUT_DIR" && mkdir -p "$OUT_DIR"
APP="$OUT_DIR/$APP_NAME.app"
ditto "$SRC_APP" "$APP"

echo "==> 5/6 组装 .app 并签名"
RES="$APP/Contents/Resources"
ditto "$PYHOME" "$RES/python"
ditto "$STAGE/ffmpeg" "$RES/ffmpeg"
ditto "$STAGE/scripts" "$RES/scripts"
xattr -cr "$APP"
# 先逐个签名 Resources 中没有有效签名的 Mach-O（.so/.dylib/可执行文件）；已有签名（如 FFmpeg 的开发者签名）保持不变。
# Apple Silicon 要求所有 arm64 代码至少有 ad-hoc 签名。
while IFS= read -r -d '' f; do
  if file -b "$f" | grep -q "Mach-O"; then
    codesign -v "$f" >/dev/null 2>&1 || codesign --force --sign - "$f"
  fi
done < <(find "$RES" -type f \( -perm -u+x -o -name "*.so" -o -name "*.dylib" \) -print0)
codesign --force --deep --sign - "$APP"
codesign --verify --deep --strict --verbose=2 "$APP"
lipo -archs "$APP/Contents/MacOS/"*

echo "==> 自检"
"$RES/ffmpeg/ffmpeg" -hide_banner -version | head -1 || echo "（当前机器无法运行 $ARCH 的 FFmpeg，跳过）"
PYBIN="$RES/python/bin/python3"
CAN_RUN=0; RUN=()
if [ "$ARCH" = arm64 ] && [ "$(uname -m)" = arm64 ]; then
  CAN_RUN=1
elif [ "$ARCH" = x64 ]; then
  if [ "$(uname -m)" = x86_64 ]; then CAN_RUN=1
  elif arch -x86_64 /usr/bin/true 2>/dev/null; then CAN_RUN=1; RUN=(arch -x86_64)   # Rosetta 2
  fi
fi
if [ "$CAN_RUN" = 1 ]; then
  ${RUN[@]+"${RUN[@]}"} "$PYBIN" -B -s -X utf8 "$RES/scripts/convert.py" --mode selftest
  TMPD="$(mktemp -d)"
  ${RUN[@]+"${RUN[@]}"} "$PYBIN" -B -s -X utf8 "$RES/scripts/convert.py" --mode md2docx --input tests/samples/sample.md --output "$TMPD/a.docx" >/dev/null
  ${RUN[@]+"${RUN[@]}"} "$PYBIN" -B -s -X utf8 "$RES/scripts/convert.py" --mode pdf2docx --input tests/samples/text.pdf --output "$TMPD/b.docx" >/dev/null
  ls -la "$TMPD"
  [ -s "$TMPD/a.docx" ] && [ -s "$TMPD/b.docx" ] || { echo "转换自检失败" >&2; exit 1; }
  ${RUN[@]+"${RUN[@]}"} "$RES/ffmpeg/ffmpeg" -hide_banner -f avfoundation -list_devices true -i "" 2>&1 | head -20 || true
else
  echo "（当前机器无法运行 $ARCH 的程序，跳过转换自检）"
fi

echo "==> 6/6 生成 DMG"
DMG_SRC="build/macos/dmg-$ARCH"
rm -rf "$DMG_SRC" && mkdir -p "$DMG_SRC"
ditto "$APP" "$DMG_SRC/$APP_NAME.app"
ln -s /Applications "$DMG_SRC/应用程序"
cat > "$DMG_SRC/安装说明.txt" <<TXT
桌面工具箱 ${VERSION}（macOS $( [ "$ARCH" = arm64 ] && echo "Apple 芯片 M1/M2/M3/M4" || echo "Intel 芯片" ) 版）

1. 把“桌面工具箱”拖到“应用程序”文件夹。
2. 本程序未经 Apple 公证（没有开发者证书），第一次打开时：
   · macOS 14 及更早：在“应用程序”中按住 Control 点按（或右键）“桌面工具箱” → 打开 → 打开。
   · macOS 15 及更新：先双击一次（会提示无法打开），再到“系统设置 → 隐私与安全性”底部点“仍要打开”。
   · 或者在“终端”中执行：xattr -cr /Applications/桌面工具箱.app
3. 首次截图/录屏时，系统会请求“屏幕录制”权限：在“系统设置 → 隐私与安全性 → 屏幕录制”中打开“桌面工具箱”，然后重新打开本程序。
   录制声音还需要“麦克风”权限。

快捷键：⌥Option+空格 显示主窗口；⌃Control+⌥Option+A 截图；⌘Command+⇧Shift+V 剪贴板历史。
数据保存在 ~/Library/Application Support/DesktopToolbox。
TXT
rm -f "$DMG"
hdiutil create -volname "$APP_NAME" -srcfolder "$DMG_SRC" -fs HFS+ -format UDZO -imagekey zlib-level=9 -ov "$DMG"
hdiutil verify "$DMG"
ls -la "$DMG"
du -sh "$APP"
