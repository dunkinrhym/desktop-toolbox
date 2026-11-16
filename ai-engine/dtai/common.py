"""公共工具：数据目录、组件路径、取消、隐藏子进程窗口。"""
import os
import subprocess
import sys
import threading

ROOT = os.environ.get("DTAI_ROOT", "")
FFMPEG = os.environ.get("DTAI_FFMPEG", "ffmpeg")


class Canceled(Exception):
    """任务被用户取消。"""


class UserError(Exception):
    """可直接显示给用户的错误（中文）。code 供界面区分处理，例如 missing_component。"""

    def __init__(self, msg, code="error", **extra):
        super().__init__(msg)
        self.code = code
        self.extra = extra


class Task:
    """单个请求的上下文：进度回调 + 取消标记。"""

    def __init__(self, emit=None):
        self._emit = emit
        self.cancel_event = threading.Event()

    def progress(self, pct, msg=""):
        if self._emit:
            self._emit({"event": "progress", "pct": int(max(0, min(100, pct))), "msg": msg})

    def partial(self, data):
        if self._emit:
            self._emit({"event": "partial", "data": data})

    def check(self):
        if self.cancel_event.is_set():
            raise Canceled()


NULL_TASK = Task()


def setup(root, ffmpeg=None):
    """设置数据目录，并把已安装的 Python 组件加入 sys.path。

    Windows 嵌入式 Python 有 ._pth 文件，会忽略 PYTHONPATH，所以必须在这里手动加路径。
    组件目录追加在内置 site-packages 之后，numpy / opencv 等以主程序内置版本为准。
    """
    global ROOT, FFMPEG
    ROOT = os.path.abspath(root)
    if ffmpeg:
        FFMPEG = ffmpeg
    site = os.path.join(ROOT, "site")
    if os.path.isdir(site):
        for name in sorted(os.listdir(site)):
            p = os.path.join(site, name)
            if os.path.isdir(p) and os.path.exists(os.path.join(p, ".installed.json")) and p not in sys.path:
                sys.path.append(p)
                # Windows：组件内的 DLL（onnxruntime / sherpa-onnx）所在目录
                if os.name == "nt":
                    for sub in ("onnxruntime/capi", "sherpa_onnx/lib", "shapely.libs", "pillow.libs"):
                        d = os.path.join(p, sub)
                        if os.path.isdir(d):
                            try:
                                os.add_dll_directory(d)
                            except OSError:
                                pass


def comp_dir(kind, comp):
    return os.path.join(ROOT, kind, comp)


def installed(kind, comp):
    return os.path.exists(os.path.join(comp_dir(kind, comp), ".installed.json"))


# 组件 → 用户可读名称（与 internal/aiengine/manifest.json 一致）
COMP_NAMES = {
    "py-ort": "AI 运行库（ONNX Runtime）",
    "py-ocr": "文字识别（OCR / 版面分析）",
    "py-speech": "语音识别运行库（sherpa-onnx）",
    "m-table": "表格结构识别模型",
    "m-lama": "去水印模型（LaMa）",
    "m-asr": "语音识别模型（SenseVoice）",
    "m-diar": "说话人分离模型",
    "llama": "大模型运行程序（llama.cpp）",
}


def require(kind, comp):
    if not installed(kind, comp):
        raise UserError("缺少组件：%s，请先在“设置 → AI 模型”中下载" % COMP_NAMES.get(comp, comp),
                        code="missing_component", component=comp)
    return comp_dir(kind, comp)


def model_file(comp, name):
    d = require("models", comp)
    p = os.path.join(d, name)
    if not os.path.exists(p):
        raise UserError("模型文件缺失：%s（请在设置中删除后重新下载该组件）" % name, code="missing_component", component=comp)
    return p


def popen_kwargs():
    """子进程不弹出控制台窗口（Windows）。"""
    if os.name == "nt":
        si = subprocess.STARTUPINFO()
        si.dwFlags |= subprocess.STARTF_USESHOWWINDOW
        return {"creationflags": 0x08000000, "startupinfo": si}  # CREATE_NO_WINDOW
    return {}


def cpu_threads():
    n = os.cpu_count() or 4
    return max(1, min(8, n // 2 if n >= 8 else n - 1 if n > 2 else n))


def unique_path(path):
    if not os.path.exists(path):
        return path
    base, ext = os.path.splitext(path)
    i = 1
    while os.path.exists("%s (%d)%s" % (base, i, ext)):
        i += 1
    return "%s (%d)%s" % (base, i, ext)


def imread(path):
    """读取图片（支持中文路径）。返回 BGR ndarray。"""
    import cv2
    import numpy as np
    data = np.fromfile(path, dtype=np.uint8)
    img = cv2.imdecode(data, cv2.IMREAD_COLOR)
    if img is None:
        raise UserError("无法读取图片：%s" % os.path.basename(path), code="bad_input")
    return img


def imwrite(path, img):
    import cv2
    ext = os.path.splitext(path)[1].lower() or ".png"
    ok, buf = cv2.imencode(ext, img)
    if not ok:
        raise UserError("图片编码失败")
    buf.tofile(path)
