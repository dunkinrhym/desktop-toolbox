#!/usr/bin/env bash
# 为统信 UOS 20 / Debian 10（glibc 2.28）编译 llama.cpp 的 llama-server（CPU 版）。
#
# 官方 ubuntu 预编译包要求 glibc 2.35+，在 UOS 20 上无法运行，所以在 manylinux_2_28（AlmaLinux 8，glibc 2.28，
# gcc-toolset 新版编译器、libstdc++ 新符号静态链接）容器里自行编译：
#   docker run --rm -v "$PWD:/w" -w /w quay.io/pypa/manylinux_2_28_x86_64 bash scripts/build-llama-linux.sh x64
#   docker run --rm -v "$PWD:/w" -w /w quay.io/pypa/manylinux_2_28_aarch64 bash scripts/build-llama-linux.sh arm64
#
# 产物：dist/llama-<标签>-bin-linux-glibc2.28-<x64|arm64>.tar.gz（llama-server + 共享库，RPATH=$ORIGIN）。
# CPU 后端按指令集编译多个变体（GGML_CPU_ALL_VARIANTS + GGML_BACKEND_DL），运行时自动选择当前 CPU 支持的最快版本，
# 兼容兆芯 / 海光 / 飞腾 / 鲲鹏等国产 CPU。不使用 OpenMP、不依赖 curl / OpenSSL。
set -euo pipefail
ARCH="${1:?x64|arm64}"
TAG="${LLAMA_TAG:-b12438}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="${WORK:-/tmp/llama-build}"
OUT="$ROOT/dist"
NAME="llama-$TAG-bin-linux-glibc2.28-$ARCH"
mkdir -p "$WORK" "$OUT"
cd "$WORK"
gcc --version | head -1
cmake --version | head -1
if [ ! -d "llama.cpp-$TAG" ]; then
  curl -fsSL --retry 3 "https://github.com/ggml-org/llama.cpp/archive/refs/tags/$TAG.tar.gz" | tar xz
fi
cmake -S "llama.cpp-$TAG" -B build -DCMAKE_BUILD_TYPE=Release \
  -DGGML_NATIVE=OFF -DGGML_BACKEND_DL=ON -DGGML_CPU_ALL_VARIANTS=ON -DBUILD_SHARED_LIBS=ON \
  -DGGML_OPENMP=OFF -DLLAMA_CURL=OFF -DLLAMA_OPENSSL=OFF \
  -DLLAMA_BUILD_TESTS=OFF -DLLAMA_BUILD_EXAMPLES=OFF -DLLAMA_BUILD_SERVER=ON \
  -DCMAKE_BUILD_RPATH='$ORIGIN' -DCMAKE_INSTALL_RPATH='$ORIGIN' -DCMAKE_BUILD_WITH_INSTALL_RPATH=ON
TARGETS="llama-server $(cmake --build build --target help | grep -o 'ggml-cpu[A-Za-z0-9_.-]*' | sort -u | tr '\n' ' ')"
echo "targets: $TARGETS"
# shellcheck disable=SC2086
cmake --build build -j"$(nproc)" --target $TARGETS
rm -rf "$NAME" && mkdir -p "$NAME"
cp -a build/bin/llama-server "$NAME/"
find build/bin -maxdepth 1 \( -name '*.so' -o -name '*.so.*' \) -exec cp -a {} "$NAME/" \;
cp "llama.cpp-$TAG/LICENSE" "$NAME/LICENSE-llama.cpp.txt"
strip --strip-unneeded "$NAME"/llama-server "$NAME"/*.so* 2>/dev/null || true
ls -la "$NAME"

echo "==> glibc / libstdc++ 符号版本检查（UOS 20：GLIBC ≤ 2.28，GLIBCXX ≤ 3.4.25，CXXABI ≤ 1.3.11）"
maxver() { { objdump -T "$NAME"/llama-server "$NAME"/*.so* 2>/dev/null || true; } | grep -o "$1_[0-9.]*" | sed "s/$1_//" | sort -uV | tail -1 || true; }
G="$(maxver GLIBC)"; X="$(maxver GLIBCXX)"; A="$(maxver CXXABI)"
echo "GLIBC_$G GLIBCXX_$X CXXABI_$A"
verle() { [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | tail -1)" = "$2" ]; }
verle "$G" 2.28 || { echo "需要的 glibc 太新：$G" >&2; exit 1; }
[ -z "$X" ] || verle "$X" 3.4.25 || { echo "需要的 libstdc++ 太新：$X" >&2; exit 1; }
[ -z "$A" ] || verle "$A" 1.3.11 || { echo "需要的 CXXABI 太新：$A" >&2; exit 1; }
readelf -d "$NAME/llama-server" | grep -E 'NEEDED|RPATH|RUNPATH'
"$NAME/llama-server" --version 2>&1 | tail -3 || true
tar -czf "$OUT/$NAME.tar.gz" "$NAME"
ls -la "$OUT/$NAME.tar.gz"
sha256sum "$OUT/$NAME.tar.gz"
