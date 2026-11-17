#!/usr/bin/env bash
# 冒烟测试（普通用户部分，由 tests/uos/smoke.sh 调用）：bash smoke-user.sh <输出目录> <样例目录>
set -uo pipefail
OUT="$1"
SAMPLES="$2"
PKG=desktop-toolbox
BIN=/usr/bin/$PKG
APPDIR=/opt/apps/$PKG
PY=$APPDIR/files/bin/resources/python/bin/python3
CONV=$APPDIR/files/bin/resources/scripts/convert.py
mkdir -p "$OUT"
LOG="$OUT/smoke-user.log"
: > "$LOG"
FAILS=0
log() { echo "$*" | tee -a "$LOG"; }
step() {
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
shot() { ffmpeg -loglevel error -y -f x11grab -video_size "$(xdpyinfo_size)" -i "$DISPLAY" -frames:v 1 "$OUT/$1.png" 2>/dev/null || true; }
xdpyinfo_size() { echo "1366x768"; }

# ---- 图形会话：Xvfb（1366×768）+ openbox 窗口管理器 + XEmbed 托盘 + PulseAudio（空设备）+ D-Bus 会话总线
export DISPLAY=:99
Xvfb :99 -screen 0 1366x768x24 -nolisten tcp >"$OUT/xvfb.log" 2>&1 &
for i in $(seq 50); do xdotool getdisplaygeometry >/dev/null 2>&1 && break; sleep 0.2; done
mkdir -p "$HOME/Desktop" "$HOME/.config"
pulseaudio -D --exit-idle-time=-1 --log-target=file:"$OUT/pulse.log" || true
pulseaudio --check && pactl load-module module-null-sink sink_name=dtsink >/dev/null 2>&1 || true

# openbox 不加任何快捷键（默认配置会占用 Alt+Space），模拟只有窗口管理的桌面
mkdir -p "$HOME/.config/openbox"
cat > "$HOME/.config/openbox/rc.xml" <<'XML'
<?xml version="1.0" encoding="UTF-8"?>
<openbox_config xmlns="http://openbox.org/3.4/rc"><focus><focusNew>yes</focusNew></focus><keyboard></keyboard><mouse></mouse></openbox_config>
XML

session() {
  openbox --config-file "$HOME/.config/openbox/rc.xml" >"$OUT/openbox.log" 2>&1 &
  stalonetray --geometry 4x1+0+0 -bg '#333333' >"$OUT/tray.log" 2>&1 &
  sleep 1.5

  st() {
    "$BIN" --selftest --selftest-out="$OUT/selftest" > "$OUT/selftest-console.log" 2>&1
    local rc=$?
    cat "$OUT/selftest/selftest.log"
    [ $rc -eq 0 ] || return 1
    grep -q 'embedded in system tray: true' "$OUT/selftest/selftest.log" || { echo "托盘图标没有嵌入 XEmbed 系统托盘"; return 1; }
    grep -q 'h264+aac' "$OUT/selftest/selftest.log" || { echo "录屏没有音轨（PulseAudio）"; return 1; }
    echo "自检全部通过（热键 / 剪贴板 / 截屏 / 框选遮罩 / 托盘 / 搜索 / Python / x11grab+pulse 录屏）"
  }
  step "程序自检 --selftest" st

  export DT_UI_PROBE="$OUT/ui-probe.txt"
  rm -f "$DT_UI_PROBE"
  launch() {
    "$BIN" > "$OUT/app.log" 2>&1 &
    echo $! > "$OUT/app.pid"
    for i in $(seq 60); do
      if xdotool search --onlyvisible --name '^桌面工具箱$' >/dev/null 2>&1; then
        sleep 2; shot "01-main"
        echo "主窗口已显示（pid $(cat "$OUT/app.pid")，$(xdotool search --onlyvisible --name '^桌面工具箱$' getwindowgeometry | tr '\n' ' ')）"
        return 0
      fi
      sleep 0.5
    done
    cat "$OUT/app.log"; return 1
  }
  step "启动并显示主窗口" launch

  pages() {
    local v n=0
    for v in search shot clipboard convert record ocr inpaint meeting settings; do
      n=$((n + 1))
      timeout 10 "$BIN" --show=$v || { echo "第二实例 --show=$v 返回 $?"; return 1; }
      sleep 1.5
      shot "page-$(printf %02d $n)-$v"
    done
    cat "$DT_UI_PROBE"
    for v in search shot clipboard convert record ocr inpaint meeting settings; do
      grep -q "^$v	true" "$DT_UI_PROBE" || { echo "页面 $v 没有正常渲染"; return 1; }
    done
    echo "9 个页面全部渲染（第二实例 --show=<页面> 切换）"
  }
  step "逐页打开所有工具" pages

  single() {
    local before after
    before=$(pgrep -u "$(id -u)" -xc $PKG || true)
    timeout 10 "$BIN"; local rc=$?
    sleep 1
    after=$(pgrep -u "$(id -u)" -xc $PKG || true)
    [ "$rc" = 0 ] && [ "$before" = 1 ] && [ "$after" = 1 ] || { echo "rc=$rc 进程数 $before → $after"; return 1; }
    echo "第二次启动立即退出（rc=0），仍只有 1 个进程"
  }
  step "单实例" single

  hotkey_shot() {
    "$BIN" --show=settings; sleep 1
    xdotool key --clearmodifiers ctrl+alt+a
    for i in $(seq 20); do xdotool search --name '桌面工具箱-截图' >/dev/null 2>&1 && break; sleep 0.25; done
    xdotool search --name '桌面工具箱-截图' >/dev/null 2>&1 || { echo "Ctrl+Alt+A 后没有出现截图遮罩"; return 1; }
    sleep 0.8
    xdotool mousemove 200 150 mousedown 1 mousemove 350 250 mousemove 520 380 mouseup 1
    sleep 0.6; shot "02-screenshot-toolbar"
    xdotool key Return; sleep 1.2
    xclip -selection clipboard -t TARGETS -o | grep -q 'image/png' || { echo "剪贴板里没有图片"; return 1; }
    xclip -selection clipboard -t image/png -o > "$OUT/clip-shot.png"
    local sz; sz=$("$PY" -c 'import struct,sys; d=open(sys.argv[1],"rb").read(24); print("%dx%d" % struct.unpack(">II", d[16:24]))' "$OUT/clip-shot.png")
    [ "$sz" = "320x230" ] || { echo "截图尺寸 $sz（期望 320x230）"; return 1; }
    grep -q '"kind": *"image"' "$HOME/.config/DesktopToolbox/clipboard/history.json" || { echo "截图没有记入剪贴板历史"; return 1; }
    # Esc 取消
    xdotool key --clearmodifiers ctrl+alt+a; sleep 1.2
    xdotool key Escape; sleep 0.6
    xdotool search --onlyvisible --name '桌面工具箱-截图' >/dev/null 2>&1 && { echo "Esc 后遮罩仍在"; return 1; }
    echo "Ctrl+Alt+A → 框选 320×230 → 回车复制（image/png）并记入历史；Esc 取消"
  }
  step "全局热键截图 + 截图工具栏" hotkey_shot

  clip_history() {
    local t="UOS 冒烟测试 剪贴板 $(date +%s)"
    printf '%s' "$t" | xclip -selection clipboard -i >/dev/null 2>&1  # xclip 会留在后台提供剪贴板内容，不能占用输出管道
    sleep 1.5
    grep -q "$t" "$HOME/.config/DesktopToolbox/clipboard/history.json" || { echo "其他程序复制的文本没有记入历史"; return 1; }
    xdotool key --clearmodifiers ctrl+shift+v; sleep 1.5
    local wid; wid=$(xdotool search --onlyvisible --name '^桌面工具箱$' | head -1)
    local geo; geo=$(xdotool getwindowgeometry "$wid" | sed -n 's/.*Geometry: //p')
    shot "03-clipboard-popup"
    local w="${geo%x*}"
    [ -n "$w" ] && [ "$w" -le 520 ] || { echo "小窗尺寸 $geo（期望约 440x640）"; return 1; }
    xdotool key --clearmodifiers ctrl+shift+v; sleep 1
    echo "xclip 写入的文本已记入超级复制；Ctrl+Shift+V 弹出小窗（$geo）"
  }
  step "超级复制（剪贴板历史）+ 小窗" clip_history

  hotkey_show() {
    "$BIN" --show=search; sleep 1
    xdotool key --clearmodifiers alt+space; sleep 1
    xdotool search --onlyvisible --name '^桌面工具箱$' >/dev/null 2>&1 && { echo "Alt+Space 没有隐藏主窗口"; return 1; }
    xdotool key --clearmodifiers alt+space; sleep 1.5
    xdotool search --onlyvisible --name '^桌面工具箱$' >/dev/null 2>&1 || { echo "Alt+Space 没有重新显示主窗口"; return 1; }
    echo "Alt+Space 隐藏 / 显示主窗口"
  }
  step "全局热键显示/隐藏" hotkey_show

  quit_app() {
    timeout 10 "$BIN" --quit
    for i in $(seq 20); do kill -0 "$(cat "$OUT/app.pid")" 2>/dev/null || { echo "已退出"; return 0; }; sleep 0.5; done
    echo "--quit 后进程仍在"; return 1
  }
  step "退出程序（--quit）" quit_app
}

conv() { # conv <模式> <输入> <输出>
  local out
  out=$(cd "$(dirname "$CONV")" && PYTHONUTF8=1 "$PY" -B -s -X utf8 "$CONV" --mode "$1" ${2:+--input "$2"} ${3:+--output "$3"} 2>&1)
  echo "$out" | tail -2
  echo "$out" | grep -q '"done"'
}
docx_ok() { "$PY" -c 'import docx,sys; d=docx.Document(sys.argv[1]); n=len([p for p in d.paragraphs if p.text.strip()]); assert n>0, "空文档"; print(sys.argv[1], n, "段")' "$1"; }
conv_docx() { conv "$1" "$2" "$3" && docx_ok "$3"; }
mkdir -p "$OUT/convert"
step "转换组件自检" conv selftest "" ""
step "Markdown → Word" conv_docx md2docx "$SAMPLES/sample.md" "$OUT/convert/sample.docx"
step "Markdown（GBK）→ Word" conv_docx md2docx "$SAMPLES/gbk.md" "$OUT/convert/gbk.docx"
step "PDF → Word" conv_docx pdf2docx "$SAMPLES/text.pdf" "$OUT/convert/text.docx"

export -f step log shot xdpyinfo_size
export OUT LOG BIN APPDIR PKG PY
dbus-run-session -- bash -c "$(declare -f session); FAILS=0; session; echo \$FAILS > '$OUT/.session-fails'"
FAILS=$((FAILS + $(cat "$OUT/.session-fails" 2>/dev/null || echo 1)))

pkill -u "$(id -u)" Xvfb 2>/dev/null; pulseaudio -k 2>/dev/null || true
if [ "$FAILS" -gt 0 ]; then
  log "RESULT: $FAILS step(s) FAILED"
  exit 1
fi
log "RESULT: ALL PASSED"
