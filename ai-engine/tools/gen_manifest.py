#!/usr/bin/env python3
"""生成 internal/aiengine/manifest.json：AI 组件的下载地址（主站 + 镜像）、大小与 SHA256。

  python3 ai-engine/tools/gen_manifest.py > internal/aiengine/manifest.json

数据来源：PyPI JSON API（轮子的 sha256 / 大小）、Hugging Face API（LFS 文件的 sha256）、
ModelScope API、GitHub Releases API（llama.cpp 资产的 sha256 digest）。改版本号只需改下面的常量。
"""
import hashlib
import json
import sys
import urllib.request

PLATFORMS = {
    # 平台键 = GOOS-GOARCH；值 = 可接受的轮子平台标签（按优先级）
    "windows-amd64": ["win_amd64"],
    "linux-arm64": ["manylinux_2_28_aarch64", "manylinux_2_27_aarch64", "manylinux_2_24_aarch64",
                    "manylinux_2_17_aarch64", "manylinux2014_aarch64"],
    "linux-amd64": ["manylinux_2_28_x86_64", "manylinux_2_27_x86_64", "manylinux_2_24_x86_64",
                    "manylinux_2_17_x86_64", "manylinux2014_x86_64"],
}

PYPI_MIRRORS = ["https://files.pythonhosted.org/packages/",
                "https://mirrors.aliyun.com/pypi/packages/",
                "https://pypi.tuna.tsinghua.edu.cn/packages/"]

# 每个 Python 组件的固定版本（--no-deps；依赖已手工列全。numpy / opencv / python-docx / PyMuPDF 由主程序内置）
PY_COMPONENTS = {
    "py-ort": ["onnxruntime==1.23.2"],
    "py-ocr": ["rapidocr==3.10.0", "rapid-layout==1.2.1", "rapid-table==3.0.2", "pyclipper==1.4.0",
               "shapely==2.2.0", "PyYAML==6.0.3", "pillow==13.0.0", "tqdm==4.70.1", "omegaconf==2.4.0",
               "requests==2.34.2", "charset-normalizer==3.5.2", "idna==3.20", "urllib3==2.8.0",
               "certifi==2026.10.23", "colorlog==6.12.0", "six==1.17.0", "openpyxl==3.1.5", "et-xmlfile==2.0.0",
               "colorama==0.4.6"],
    "py-speech": ["sherpa-onnx==2.0.1", "sherpa-onnx-core==2.0.1"],
}

HF = "https://huggingface.co/"
HFM = "https://hf-mirror.com/"
MS = "https://www.modelscope.cn/models/"

LLAMA_TAG = "b12438"
# 平台键 → (GitHub 仓库, Release 标签, 资产名)。官方 ubuntu 预编译包需要 glibc 2.35+，统信 UOS 20（Debian 10，glibc 2.28）
# 无法运行，Linux 使用 .github/workflows/build-llama-linux.yml 在 manylinux_2_28 中自行编译、发布到本仓库预发布 Release 的版本。
LLAMA_LINUX_REPO = "dunkinrhym/desktop-toolbox"
LLAMA_ASSETS = {
    "windows-amd64": ("ggml-org/llama.cpp", LLAMA_TAG, "llama-%s-bin-win-cpu-x64.zip" % LLAMA_TAG),
    "linux-amd64": (LLAMA_LINUX_REPO, "llama-linux-" + LLAMA_TAG, "llama-%s-bin-linux-glibc2.28-x64.tar.gz" % LLAMA_TAG),
    "linux-arm64": (LLAMA_LINUX_REPO, "llama-linux-" + LLAMA_TAG, "llama-%s-bin-linux-glibc2.28-arm64.tar.gz" % LLAMA_TAG),
}


def get(url):
    req = urllib.request.Request(url, headers={"User-Agent": "dt-gen-manifest"})
    with urllib.request.urlopen(req, timeout=60) as r:
        return r.read()


def getj(url):
    return json.loads(get(url))


def wheel_for(spec, tags):
    name, ver = spec.split("==")
    d = getj("https://pypi.org/pypi/%s/%s/json" % (name, ver))
    files = [u for u in d["urls"] if u["packagetype"] == "bdist_wheel"]
    cands = []
    for u in files:
        fn = u["filename"]
        parts = fn[:-4].split("-")
        py, abi, plat = parts[-3], parts[-2], parts[-1]
        pys = py.split(".")
        if not any(p in ("py3", "py2.py3", "cp312") or p.startswith("py3") for p in pys):
            if "cp312" not in pys and not (abi == "abi3" and any(p in ("cp310", "cp311", "cp39", "cp38") for p in pys)):
                continue
        plats = plat.split(".")
        if "any" in plats:
            cands.append((100, u))
            continue
        for i, t in enumerate(tags):
            if t in plats:
                cands.append((i, u))
                break
    if not cands:
        raise SystemExit("找不到 %s 的轮子：%s" % (spec, tags))
    cands.sort(key=lambda x: x[0])
    u = cands[0][1]
    path = u["url"].split("/packages/", 1)[1]
    return {"name": u["filename"], "size": u["size"], "sha256": u["digests"]["sha256"],
            "urls": [m + path for m in PYPI_MIRRORS], "extract": "zip"}


def hf_file(repo, path, dest, modelscope=None):
    tree = getj("https://huggingface.co/api/models/%s/tree/main?recursive=1" % repo)
    meta = next(f for f in tree if f["path"] == path)
    sha = (meta.get("lfs") or {}).get("oid")
    size = meta["size"]
    if not sha:
        data = get(HF + "%s/resolve/main/%s" % (repo, path))
        sha, size = hashlib.sha256(data).hexdigest(), len(data)
    urls = [HF + "%s/resolve/main/%s" % (repo, path), HFM + "%s/resolve/main/%s" % (repo, path)]
    if modelscope:
        urls.append(MS + "%s/resolve/master/%s" % modelscope)
    return {"name": dest, "size": size, "sha256": sha, "urls": urls, "extract": "file"}


def ms_file(repo, path, dest, rev="master"):
    d = getj("https://www.modelscope.cn/api/v1/models/%s/repo/files?Recursive=true&Revision=%s" % (repo, rev))
    meta = next(f for f in d["Data"]["Files"] if f["Path"] == path)
    return {"name": dest, "size": meta["Size"], "sha256": meta["Sha256"],
            "urls": [MS + "%s/resolve/%s/%s" % (repo, rev, path)], "extract": "file"}


def llama_files():
    rels = {}
    out = {}
    for plat, (repo, tag, name) in LLAMA_ASSETS.items():
        if (repo, tag) not in rels:
            rels[(repo, tag)] = getj("https://api.github.com/repos/%s/releases/tags/%s" % (repo, tag))
        assets = {a["name"]: a for a in rels[(repo, tag)]["assets"]}
        a = assets[name]
        sha = (a.get("digest") or "").replace("sha256:", "")
        if not sha:
            sha = hashlib.sha256(get(a["browser_download_url"])).hexdigest()
        url = a["browser_download_url"]
        out[plat] = [{"name": a["name"], "size": a["size"], "sha256": sha,
                      "urls": [url, "https://ghfast.top/" + url, "https://gh-proxy.com/" + url],
                      "extract": "zip" if a["name"].endswith(".zip") else "tgz"}]
    return out


def main():
    comps = []
    descs = {
        "py-ort": ("AI 运行库（ONNX Runtime）", "文字识别、去水印共用的推理引擎"),
        "py-ocr": ("文字识别（OCR / 版面分析）", "RapidOCR（PP-OCR 中英文模型）、版面分析、表格识别程序，Excel 输出"),
        "py-speech": ("语音识别运行库（sherpa-onnx）", "会议记录的语音识别与说话人分离引擎"),
    }
    for cid, specs in PY_COMPONENTS.items():
        files = {}
        for plat, tags in PLATFORMS.items():
            files[plat] = [wheel_for(s, tags) for s in specs]
        name, desc = descs[cid]
        comps.append({"id": cid, "kind": "site", "name": name, "desc": desc, "version": specs[0].split("==")[1],
                      "files": files})
    comps.append({"id": "m-table", "kind": "models", "name": "表格结构识别模型（SLANet-plus）",
                  "desc": "图片转 Word / Excel 时识别表格的行列结构", "version": "2.0.0",
                  "files": {"all": [ms_file("RapidAI/RapidTable", "slanet-plus.onnx", "slanet-plus.onnx", "v2.0.0")]}})
    comps.append({"id": "m-lama", "kind": "models", "name": "去水印模型（LaMa）", "desc": "big-lama 图像修补，Apache-2.0",
                  "version": "1", "files": {"all": [hf_file("Carve/LaMa-ONNX", "lama_fp32.onnx", "lama_fp32.onnx")]}})
    sv = "csukuangfj/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17"
    comps.append({"id": "m-asr", "kind": "models", "name": "语音识别模型（SenseVoice）",
                  "desc": "中文 / 英文 / 粤语 / 日语 / 韩语识别，自动加标点", "version": "2024-07-17",
                  "files": {"all": [hf_file(sv, "model.int8.onnx", "model.int8.onnx"),
                                    hf_file(sv, "tokens.txt", "tokens.txt")]}})
    comps.append({"id": "m-diar", "kind": "models", "name": "说话人分离模型",
                  "desc": "pyannote 分割 + 3D-Speaker CAM++ 声纹 + Silero 语音检测", "version": "1",
                  "files": {"all": [
                      hf_file("csukuangfj/sherpa-onnx-pyannote-segmentation-3-0", "model.int8.onnx", "segmentation.onnx"),
                      hf_file("csukuangfj/speaker-embedding-models",
                              "3dspeaker_speech_campplus_sv_zh_en_16k-common_advanced.onnx", "embedding.onnx"),
                      hf_file("csukuangfj/vad", "silero_vad.onnx", "silero_vad.onnx")]}})
    comps.append({"id": "llama", "kind": "bin", "name": "大模型运行程序（llama.cpp %s）" % LLAMA_TAG,
                  "desc": "在本机运行 Qwen3 大模型（翻译、改写、会议纪要）", "version": LLAMA_TAG, "files": llama_files()})
    llms = [("llm-8b", "Qwen/Qwen3-8B-GGUF", "Qwen3-8B-Q4_K_M.gguf", "Qwen3-8B 大模型（推荐 16GB 及以上内存）", 16),
            ("llm-4b", "Qwen/Qwen3-4B-GGUF", "Qwen3-4B-Q4_K_M.gguf", "Qwen3-4B 大模型（8GB 内存）", 8),
            ("llm-1.7b", "Qwen/Qwen3-1.7B-GGUF", "Qwen3-1.7B-Q8_0.gguf", "Qwen3-1.7B 小模型（速度快，质量一般）", 4),
            ("llm-0.6b", "Qwen/Qwen3-0.6B-GGUF", "Qwen3-0.6B-Q8_0.gguf", "Qwen3-0.6B 微型模型（仅供测试）", 2)]
    for cid, repo, fn, name, ram in llms:
        f = hf_file(repo, fn, fn, modelscope=(repo, fn))
        comps.append({"id": cid, "kind": "models", "name": name, "desc": "通义千问 Qwen3，Apache-2.0", "version": "1",
                      "minRamGB": ram, "hidden": cid == "llm-0.6b", "files": {"all": [f]}})
    features = {
        "ocr": ["py-ort", "py-ocr"],
        "doc": ["py-ort", "py-ocr", "m-table"],
        "inpaint": ["py-ort", "m-lama"],
        "asr": ["py-speech", "m-asr", "m-diar"],
        "llm": ["llama"],  # + 按内存选择的 llm-*
    }
    json.dump({"version": 1, "components": comps, "features": features}, sys.stdout, ensure_ascii=False, indent=1)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
