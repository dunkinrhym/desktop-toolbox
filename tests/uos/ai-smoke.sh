#!/usr/bin/env bash
# 离线 AI 冒烟测试（UOS / Debian 10 容器，root）：
#   docker run --rm -v "$PWD:$PWD" -w "$PWD" -v "$T:$T" debian:buster bash tests/uos/ai-smoke.sh <deb> <AI 数据目录> <输出目录> <dtai 程序> <aiengine.test> [大模型组件]
# 安装 deb（使用包内的 Python 3.12 与 AI 引擎副本）→ 按内嵌清单真实下载 linux-<架构> 组件（断点续传 + SHA256 校验）→
# 引擎自检 → TestEngineSmoke：截图识字、图片转 Word / Excel、扫描 PDF 转 Word、去水印、会议转写 + 说话人、翻译、改写、纪要。
set -euo pipefail
DEB="$(readlink -f "$1")"; ROOT="$2"; OUT="$3"; DTAI="$(readlink -f "$4")"; TEST="$(readlink -f "$5")"; LLM="${6:-llm-0.6b}"
REPO="$(cd "$(dirname "$0")/../.." && pwd)"
APPDIR=/opt/apps/desktop-toolbox
PY=$APPDIR/files/bin/resources/python/bin/python3
ENGINE=$APPDIR/files/bin/resources/ai-engine/dtai_main.py
export DEBIAN_FRONTEND=noninteractive
mkdir -p "$ROOT" "$OUT"
if grep -q '^10\.' /etc/debian_version 2>/dev/null && grep -q buster /etc/apt/sources.list 2>/dev/null; then
  printf 'deb http://archive.debian.org/debian buster main\ndeb http://archive.debian.org/debian-security buster/updates main\n' > /etc/apt/sources.list
fi
apt-get -o Acquire::Check-Valid-Until=false update -q >/dev/null
apt-get install -y -q --no-install-recommends ca-certificates curl >/dev/null
apt-get install -y -q "$DEB" >/dev/null
echo "== $(grep PRETTY_NAME /etc/os-release) / $(uname -m) / $(ldd --version | sed -n '1p') / $(nproc) CPU / $(awk '/MemTotal/{printf "%.1f", $2/1048576}' /proc/meminfo) GB"
"$PY" -c 'import sys; print(sys.version)'

echo "== 1. 下载 AI 组件（linux-$(uname -m)，真实清单）"
"$DTAI" list
t0=$SECONDS
"$DTAI" install -root "$ROOT" py-ort py-ocr py-speech m-table m-lama m-asr m-diar llama "$LLM"
echo "下载 + 安装用时 $((SECONDS - t0)) 秒"
du -sh "$ROOT"/*/* 2>/dev/null | sort -h | tail -20 || true
ls -la "$ROOT/bin/llama"/* | sed -n '1,5p'

echo "== 2. 准备测试素材（多说话人中文录音 → 录屏 MP4、扫描版 PDF）"
mkdir -p "$OUT/media"
[ -s "$OUT/media/4spk.wav" ] || curl -fsSL --retry 3 -o "$OUT/media/4spk.wav" \
  https://huggingface.co/csukuangfj/speaker-embedding-models/resolve/main/0-four-speakers-zh.wav \
  || curl -fsSL --retry 3 -o "$OUT/media/4spk.wav" https://hf-mirror.com/csukuangfj/speaker-embedding-models/resolve/main/0-four-speakers-zh.wav
ffmpeg -v error -y -f lavfi -i color=c=black:s=320x240:d=57 -i "$OUT/media/4spk.wav" -shortest -c:v libx264 -c:a aac "$OUT/media/会议录屏.mp4"
"$PY" -c "import fitz,sys; d=fitz.open(); [d.new_page(width=595,height=842).insert_image(fitz.Rect(20,20,575,520), filename=sys.argv[1]) for i in range(2)]; d.save(sys.argv[2])" \
  "$REPO/ai-engine/tests/samples/doc_table.png" "$OUT/media/扫描件.pdf"
ls -la "$OUT/media"

echo "== 3. 引擎自检（安装包内的 Python + AI 引擎）"
PYTHONUTF8=1 "$PY" -B -s "$ENGINE" selftest --root "$ROOT"

echo "== 4. TestEngineSmoke（真实模型，大模型 $LLM）"
cd "$REPO/internal/aiengine"
DT_AI_ROOT="$ROOT" DT_TEST_PYTHON="$PY" DT_AI_SCRIPT="$ENGINE" DT_FFMPEG=/usr/bin/ffmpeg DT_AI_LLM="$LLM" DT_AI_OUT="$OUT" \
DT_AI_AUDIO="$OUT/media/会议录屏.mp4" DT_AI_SCANNED_PDF="$OUT/media/扫描件.pdf" PYTHONUTF8=1 \
  "$TEST" -test.v -test.count=1 -test.timeout 55m -test.run TestEngineSmoke 2>&1 | tee "$OUT/engine-smoke.log"
rc=${PIPESTATUS[0]}
grep -E '^\s*--- (PASS|FAIL|SKIP)' "$OUT/engine-smoke.log" || true
if grep -q -- '--- SKIP' "$OUT/engine-smoke.log"; then echo "有测试被跳过" >&2; exit 1; fi
exit "$rc"
