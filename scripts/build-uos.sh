#!/usr/bin/env bash
# 桌面工具箱 统信 UOS / Linux .deb 构建脚本。
#
# 在 debian:buster 容器里运行（UOS 20 桌面版基于 Debian 10：glibc 2.28、webkit2gtk-4.0），保证二进制兼容：
#   docker run --rm -v "$PWD:/w" -w /w debian:buster bash scripts/build-uos.sh
# （容器架构即目标架构：x86_64 → amd64，aarch64 → arm64）
#
# 产物：dist/DesktopToolbox-<版本>-UOS-<amd64|arm64>.deb
#   包名 desktop-toolbox，显示名「桌面工具箱」，按统信应用规范安装到 /opt/apps/desktop-toolbox/：
#     info                                   应用信息（JSON）
#     entries/applications/desktop-toolbox.desktop
#     entries/icons/hicolor/<尺寸>/apps/desktop-toolbox.png
#     files/bin/desktop-toolbox              主程序（Go + Wails v2 + WebKitGTK）
#     files/bin/resources/                   内置 Python 3.12（PDF/Markdown 转换 + 离线 AI 引擎）、脚本、图标
#   另在 /usr/share/applications 放同名 .desktop（兼容非 UOS 发行版），/usr/bin/desktop-toolbox 为命令行入口。
#   AI 模型不打进安装包，首次使用时下载到 ~/.config/DesktopToolbox/ai。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
export DEBIAN_FRONTEND=noninteractive
VERSION="$(sed -n 's/.*"productVersion": *"\([^"]*\)".*/\1/p' wails.json | sed -n '1p')"
PKG=desktop-toolbox
APPDIR="/opt/apps/$PKG"
case "$(uname -m)" in
  x86_64) DEB_ARCH=amd64; GO_ARCH=amd64; PBS_ARCH=x86_64 ;;
  aarch64) DEB_ARCH=arm64; GO_ARCH=arm64; PBS_ARCH=aarch64 ;;
  *) echo "不支持的架构：$(uname -m)" >&2; exit 2 ;;
esac
PBS_TAG="${PBS_TAG:-20261110}"
PY_VER="${PY_VER:-3.12.16}"
DL="${DL:-$ROOT/third_party/uos-dl}"
STAGE="$ROOT/build/uos/stage-$DEB_ARCH"
DEB="$ROOT/dist/DesktopToolbox-$VERSION-UOS-$DEB_ARCH.deb"
mkdir -p "$DL" "$ROOT/dist"

echo "==> 1/7 构建环境（Debian 10 buster，archive.debian.org）"
if grep -q '^10\.' /etc/debian_version 2>/dev/null; then
  cat > /etc/apt/sources.list <<'SRC'
deb http://archive.debian.org/debian buster main
deb http://archive.debian.org/debian-security buster/updates main
SRC
fi
apt-get -o Acquire::Check-Valid-Until=false update -q
apt-get install -y -q --no-install-recommends build-essential pkg-config ca-certificates curl xz-utils git file \
  libgtk-3-dev libwebkit2gtk-4.0-dev libx11-dev libxtst-dev dpkg-dev imagemagick
cat /etc/debian_version; ldd --version | sed -n '1p'
pkg-config --modversion gtk+-3.0 webkit2gtk-4.0

echo "==> 2/7 Go"
if ! command -v go >/dev/null; then
  GOV="${GO_VERSION_FULL:-$(curl -fsSL 'https://go.dev/VERSION?m=text' | sed -n '1p')}"
  [ -f "$DL/$GOV.linux-$GO_ARCH.tar.gz" ] || curl -fsSL --retry 3 -o "$DL/$GOV.linux-$GO_ARCH.tar.gz" "https://go.dev/dl/$GOV.linux-$GO_ARCH.tar.gz"
  rm -rf /usr/local/go && tar -C /usr/local -xzf "$DL/$GOV.linux-$GO_ARCH.tar.gz"
  export PATH=/usr/local/go/bin:$PATH
fi
go version
export GOFLAGS=-buildvcs=false GOTOOLCHAIN=local

echo "==> 3/7 内置 Python（python-build-standalone $PBS_TAG / CPython $PY_VER / $PBS_ARCH）"
rm -rf "$STAGE" && mkdir -p "$STAGE/resources/scripts"
for kind in install_only_stripped install_only; do
  PBS_FILE="cpython-$PY_VER+$PBS_TAG-$PBS_ARCH-unknown-linux-gnu-$kind.tar.gz"
  if [ -f "$DL/$PBS_FILE" ] || curl -fsSL --retry 3 -o "$DL/$PBS_FILE" "https://github.com/astral-sh/python-build-standalone/releases/download/$PBS_TAG/${PBS_FILE//+/%2B}"; then
    break
  fi
  rm -f "$DL/$PBS_FILE"
done
tar -xzf "$DL/$PBS_FILE" -C "$STAGE/resources"      # 解压出 python/
PYHOME="$STAGE/resources/python"
PY="$PYHOME/bin/python3"
"$PY" -c 'import sys, ssl; print(sys.version); print(ssl.OPENSSL_VERSION)'
rm -rf "$PYHOME/lib/python3.12/test" "$PYHOME/lib/python3.12/idlelib" "$PYHOME/lib/python3.12/tkinter" \
       "$PYHOME/lib/python3.12/turtledemo" "$PYHOME/lib/python3.12/lib2to3" "$PYHOME"/lib/tcl* "$PYHOME"/lib/tk* \
       "$PYHOME"/lib/itcl* "$PYHOME"/lib/thread* "$PYHOME/include" "$PYHOME/share" \
       "$PYHOME"/lib/python3.12/lib-dynload/_tkinter*.so "$PYHOME"/lib/python3.12/config-3.12-*

echo "==> 4/7 安装转换依赖（与 Windows / macOS 版相同的版本范围）"
"$PY" -m pip install -q --disable-pip-version-check --no-compile --only-binary=:all: \
  pdf2docx==0.5.13 python-docx markdown "PyMuPDF>=1.26.7,<1.27" "opencv-python-headless<4.13" "numpy<2.4"
SP="$PYHOME/lib/python3.12/site-packages"
"$PY" -m pip uninstall -q -y pip setuptools wheel 2>/dev/null || true
rm -rf "$PYHOME/bin/pip"* "$SP"/*.dist-info/RECORD
find "$SP" -type d \( -name "__pycache__" -o -name "tests" \) -prune -exec rm -rf {} + 2>/dev/null || true
cp resources/scripts/convert.py "$STAGE/resources/scripts/"
mkdir -p "$STAGE/resources/ai-engine"
cp -r ai-engine/dtai ai-engine/dtai_main.py "$STAGE/resources/ai-engine/"
find "$STAGE/resources/ai-engine" -name __pycache__ -prune -exec rm -rf {} +
# 预编译 .pyc（unchecked-hash：安装目录只读，运行时不写缓存；.pyc 属于软件包，卸载时一并删除）
"$PY" -m compileall -q -j 0 --invalidation-mode unchecked-hash "$PYHOME/lib/python3.12" "$STAGE/resources/scripts" "$STAGE/resources/ai-engine" >/dev/null || true
"$PY" -B -s -X utf8 "$STAGE/resources/scripts/convert.py" --mode selftest
TMPD="$(mktemp -d)"
"$PY" -B -s -X utf8 "$STAGE/resources/scripts/convert.py" --mode md2docx --input tests/samples/sample.md --output "$TMPD/a.docx" >/dev/null
"$PY" -B -s -X utf8 "$STAGE/resources/scripts/convert.py" --mode pdf2docx --input tests/samples/text.pdf --output "$TMPD/b.docx" >/dev/null
[ -s "$TMPD/a.docx" ] && [ -s "$TMPD/b.docx" ] || { echo "转换自检失败" >&2; exit 1; }
convert build/appicon.png -resize 128x128 "$STAGE/resources/icon.png"

echo "==> 5/7 编译主程序（Wails v2 / webkit2gtk-4.0）"
CGO_ENABLED=1 go build -tags desktop,production -trimpath -ldflags "-s -w" -o "$STAGE/$PKG" .
file "$STAGE/$PKG"
GLIBC_MAX="$(objdump -T "$STAGE/$PKG" | grep -o 'GLIBC_[0-9.]*' | sort -uV | tail -1)"
echo "主程序需要 $GLIBC_MAX"
go vet -tags desktop,production . ./internal/...
CGO_ENABLED=0 go test -count=1 ./internal/cliphist/ ./internal/converter/ ./internal/recorder/ ./internal/search/ ./internal/extract/ ./internal/dib/

echo "==> 6/7 组装 deb（统信应用目录规范 /opt/apps/$PKG）"
PKGROOT="$ROOT/build/uos/pkg-$DEB_ARCH"
rm -rf "$PKGROOT"
A="$PKGROOT$APPDIR"
mkdir -p "$A/files/bin" "$A/entries/applications" "$PKGROOT/usr/share/applications" "$PKGROOT/usr/bin" "$PKGROOT/DEBIAN"
cp "$STAGE/$PKG" "$A/files/bin/$PKG"
cp -a "$STAGE/resources" "$A/files/bin/resources"
ln -s "$APPDIR/files/bin/$PKG" "$PKGROOT/usr/bin/$PKG"
for s in 16 24 32 48 64 96 128 256 512; do
  d="$A/entries/icons/hicolor/${s}x${s}/apps"
  mkdir -p "$d"
  convert build/appicon.png -resize "${s}x${s}" -strip "$d/$PKG.png"
done
desktop_file() { # $1 = Icon 值
  cat <<DESK
[Desktop Entry]
Type=Application
Version=1.0
Name=桌面工具箱
Name[zh_CN]=桌面工具箱
Name[en_US]=Desktop Toolbox
GenericName=桌面工具箱
Comment=文件搜索、截图（识字/翻译/改写）、超级复制、格式转换、录屏、离线 AI
Comment[zh_CN]=文件搜索、截图（识字/翻译/改写）、超级复制、格式转换、录屏、离线 AI
Comment[en_US]=File search, screenshot, clipboard history, document conversion, screen recording and offline AI
Exec=$APPDIR/files/bin/$PKG
TryExec=$APPDIR/files/bin/$PKG
Icon=$1
Terminal=false
Categories=Utility;
Keywords=search;screenshot;clipboard;convert;record;OCR;搜索;截图;剪贴板;转换;录屏;识字;
StartupWMClass=$PKG
StartupNotify=true
DESK
}
desktop_file "$PKG" > "$A/entries/applications/$PKG.desktop"
desktop_file "$APPDIR/entries/icons/hicolor/256x256/apps/$PKG.png" > "$PKGROOT/usr/share/applications/$PKG.desktop"
cat > "$A/info" <<INFO
{
  "appid": "$PKG",
  "name": "桌面工具箱",
  "version": "$VERSION",
  "arch": ["$DEB_ARCH"],
  "permissions": {
    "autostart": true,
    "notification": false,
    "trayicon": true,
    "clipboard": true,
    "account": false,
    "bluetooth": false,
    "camera": false,
    "audio_record": true,
    "installed_apps": false
  }
}
INFO
"$PY" -c 'import json,sys; json.load(open(sys.argv[1]))' "$A/info"

# 依赖：主程序链接的库（dpkg-shlibdeps 按 buster 的符号版本计算最低版本）+ 运行时调用的程序
SHL="$(mktemp -d)"
mkdir -p "$SHL/debian" && echo "Source: $PKG" > "$SHL/debian/control"
SHLIBS="$(cd "$SHL" && dpkg-shlibdeps -O -e"$A/files/bin/$PKG" 2>/dev/null | sed -n 's/^shlibs:Depends=//p')"
echo "shlibs: $SHLIBS"
DEPENDS="$SHLIBS, ffmpeg"
INSTALLED_KB="$(du -sk --exclude=DEBIAN "$PKGROOT" | cut -f1)"
cat > "$PKGROOT/DEBIAN/control" <<CTRL
Package: $PKG
Version: $VERSION
Architecture: $DEB_ARCH
Maintainer: DesktopToolbox <355365448+dunkinrhym@users.noreply.github.com>
Installed-Size: $INSTALLED_KB
Depends: $DEPENDS
Recommends: pulseaudio | pipewire-pulse
Section: utils
Priority: optional
Homepage: https://github.com/dunkinrhym/desktop-toolbox
Description: 桌面工具箱（Desktop Toolbox）
 文件搜索、截图（存桌面 / 识字 / 翻译 / 改写）、超级复制（剪贴板历史）、
 PDF / Markdown 转 Word、录屏，以及完全离线的 AI 功能：图片转 Word / Excel、
 扫描 PDF 识别、去水印、会议记录转写与摘要。AI 模型首次使用时下载。
 适用于统信 UOS 20 桌面版（X11 会话）及 Debian 10+ 等发行版。
CTRL
cat > "$PKGROOT/DEBIAN/postinst" <<'SH'
#!/bin/sh
set -e
if command -v update-desktop-database >/dev/null 2>&1; then update-desktop-database -q /usr/share/applications || true; fi
if command -v gtk-update-icon-cache >/dev/null 2>&1 && [ -f /usr/share/icons/hicolor/index.theme ]; then gtk-update-icon-cache -q -t /usr/share/icons/hicolor || true; fi
exit 0
SH
cat > "$PKGROOT/DEBIAN/prerm" <<SH
#!/bin/sh
set -e
# 卸载（非升级）时结束正在运行的桌面工具箱
if [ "\$1" = remove ] && command -v pkill >/dev/null 2>&1; then
  pkill -x "$PKG" || true
fi
exit 0
SH
cat > "$PKGROOT/DEBIAN/postrm" <<SH
#!/bin/sh
set -e
if [ "\$1" = remove ] || [ "\$1" = purge ]; then
  rm -rf "$APPDIR"
fi
if command -v update-desktop-database >/dev/null 2>&1; then update-desktop-database -q /usr/share/applications || true; fi
exit 0
SH
chmod 0755 "$PKGROOT/DEBIAN/postinst" "$PKGROOT/DEBIAN/prerm" "$PKGROOT/DEBIAN/postrm"
find "$PKGROOT" -type d -exec chmod 0755 {} +
chmod -R u+rw,go+r,go-w "$PKGROOT$APPDIR"

echo "==> 7/7 打包"
rm -f "$DEB"
dpkg-deb --root-owner-group -Zxz -z9 --build "$PKGROOT" "$DEB"
dpkg-deb -I "$DEB"
# 只看前 60 行；不用 head（pipefail 下 head 提前退出会让 dpkg-deb 收到 SIGPIPE，脚本以 141 失败）
dpkg-deb -c "$DEB" | grep -v '/python/lib/' | sed -n '1,60p'
ls -la "$DEB"
du -sh "$PKGROOT$APPDIR"
sha256sum "$DEB"
