#!/usr/bin/env bash
# 统信 UOS / Debian 10 冒烟测试（在全新的 debian:buster 或 UOS/deepin 容器里以 root 运行）：
#   docker run --rm -v "$PWD:/w" -w /w debian:buster bash tests/uos/smoke.sh <deb 文件> <输出目录>
#
# 步骤：apt 安装 deb（自动拉取依赖）→ 检查安装布局与 .desktop / info → 以普通用户在 Xvfb + openbox + D-Bus +
# PulseAudio + XEmbed 托盘（stalonetray）中运行 tests/uos/smoke-user.sh（自检、启动、逐页打开、单实例、热键截图、
# 剪贴板历史、小窗、转换、退出）→ apt purge 卸载并检查没有残留。每步 PASS / FAIL，有失败则退出码 1。
set -uo pipefail
DEB="$(readlink -f "${1:?deb 文件}")"
OUT="$(readlink -m "${2:-/tmp/uos-smoke}")"
PKG=desktop-toolbox
APPDIR=/opt/apps/$PKG
HERE="$(cd "$(dirname "$0")" && pwd)"
mkdir -p "$OUT"
LOG="$OUT/smoke.log"
: > "$LOG"
FAILS=0
log() { echo "$*" | tee -a "$LOG"; }
step() { # step <名称> <命令...>
  local name="$1"; shift
  local t0=$SECONDS out
  if out="$("$@" 2>&1)"; then
    log "PASS  $name ($((SECONDS - t0))s) ${out:+— $(echo "$out" | tail -1)}"
  else
    FAILS=$((FAILS + 1))
    log "FAIL  $name ($((SECONDS - t0))s)"
    echo "$out" | tail -40 | sed 's/^/      /' | tee -a "$LOG"
  fi
}
export DEBIAN_FRONTEND=noninteractive

log "== 系统：$(cat /etc/os-release | grep PRETTY_NAME | cut -d= -f2) / $(uname -m) / $(ldd --version | sed -n '1p')"
if grep -q '^10\.' /etc/debian_version 2>/dev/null && grep -q buster /etc/apt/sources.list 2>/dev/null; then
  printf 'deb http://archive.debian.org/debian buster main\ndeb http://archive.debian.org/debian-security buster/updates main\n' > /etc/apt/sources.list
fi
apt-get -o Acquire::Check-Valid-Until=false update -q >/dev/null
apt-get install -y -q --no-install-recommends xvfb xauth xdotool xclip dbus dbus-x11 openbox stalonetray pulseaudio \
  procps desktop-file-utils fonts-wqy-microhei unzip ca-certificates >/dev/null || { log "FAIL  测试工具安装失败"; exit 1; }

install_deb() {
  apt-get install -y -q "$DEB" 2>&1 | tail -3
  dpkg -s $PKG | grep -E '^(Status|Version|Architecture|Depends|Installed-Size)'
}
step "apt 安装 deb（自动安装依赖）" install_deb

check_layout() {
  set -e
  for f in "$APPDIR/info" "$APPDIR/files/bin/$PKG" "$APPDIR/entries/applications/$PKG.desktop" \
           "$APPDIR/entries/icons/hicolor/256x256/apps/$PKG.png" "$APPDIR/entries/icons/hicolor/16x16/apps/$PKG.png" \
           "/usr/share/applications/$PKG.desktop" "$APPDIR/files/bin/resources/python/bin/python3" \
           "$APPDIR/files/bin/resources/scripts/convert.py" "$APPDIR/files/bin/resources/ai-engine/dtai_main.py"; do
    [ -e "$f" ] || { echo "缺少 $f"; exit 1; }
  done
  [ "$(readlink -f /usr/bin/$PKG)" = "$APPDIR/files/bin/$PKG" ] || { echo "/usr/bin/$PKG 链接错误"; exit 1; }
  desktop-file-validate "$APPDIR/entries/applications/$PKG.desktop"
  desktop-file-validate "/usr/share/applications/$PKG.desktop"
  grep -q '^Name=桌面工具箱$' "/usr/share/applications/$PKG.desktop"
  "$APPDIR/files/bin/resources/python/bin/python3" -c 'import json,sys; d=json.load(open(sys.argv[1])); assert d["appid"]=="desktop-toolbox" and d["name"]=="桌面工具箱", d; print(d["version"], d["arch"])' "$APPDIR/info"
  ldd "$APPDIR/files/bin/$PKG" | grep -E 'not found' && exit 1
  echo "布局正确，ldd 无缺失库，$(du -sh $APPDIR | cut -f1)"
}
step "安装布局 / .desktop / info / 动态库" check_layout

id tester >/dev/null 2>&1 || useradd -m -s /bin/bash tester
mkdir -p "$OUT/user" && chown -R tester "$OUT/user"
cp -r "$HERE" /tmp/uos-tests && chmod -R a+rX /tmp/uos-tests
SAMPLES="$(readlink -f "$HERE/../samples")"
cp -r "$SAMPLES" /tmp/uos-samples && chmod -R a+rX /tmp/uos-samples
log "== 以普通用户 tester 运行界面测试"
su - tester -c "bash /tmp/uos-tests/smoke-user.sh '$OUT/user' /tmp/uos-samples" 2>&1 | tee -a "$OUT/user-console.log"
if [ -f "$OUT/user/smoke-user.log" ]; then
  cat "$OUT/user/smoke-user.log" >> "$LOG"
  n=$(grep -c '^FAIL' "$OUT/user/smoke-user.log" || true)
  FAILS=$((FAILS + n))
  grep -q '^RESULT: ALL PASSED' "$OUT/user/smoke-user.log" || { [ "$n" -gt 0 ] || { FAILS=$((FAILS + 1)); log "FAIL  用户测试没有完成"; }; }
else
  FAILS=$((FAILS + 1)); log "FAIL  用户测试没有生成日志"
fi

uninstall() {
  set -e
  apt-get purge -y -q $PKG 2>&1 | tail -2
  if dpkg -s $PKG >/dev/null 2>&1; then echo "dpkg 仍显示已安装"; exit 1; fi
  for f in "$APPDIR" "/usr/share/applications/$PKG.desktop" "/usr/bin/$PKG" /opt/apps/$PKG; do
    [ ! -e "$f" ] || { echo "残留：$f"; ls -la "$f"; exit 1; }
  done
  pgrep -x $PKG && { echo "进程仍在运行"; exit 1; }
  echo "已卸载：/opt/apps/$PKG、/usr/share/applications、/usr/bin 均无残留（用户数据 ~/.config/DesktopToolbox 按惯例保留）"
}
step "apt purge 卸载干净" uninstall

if [ "$FAILS" -gt 0 ]; then
  log "RESULT: $FAILS step(s) FAILED"
  exit 1
fi
log "RESULT: ALL PASSED"
