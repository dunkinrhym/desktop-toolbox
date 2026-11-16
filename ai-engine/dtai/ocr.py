"""文字识别：RapidOCR（PP-OCR ONNX，模型随 py-ocr 组件的 rapidocr 轮子一起下载）。"""
import logging
import re
import threading

from .common import require, imread, UserError

_lock = threading.Lock()
_engine = None

URL_RE = re.compile(r"(https?://[^\s，。；、）)\]】>\"']+|www\.[^\s，。；、）)\]】>\"']+|[\w.+-]+@[\w-]+(?:\.[\w-]+)+)", re.I)


def _quiet():
    for name in ("RapidOCR", "rapidocr", "RapidLayout", "rapid_layout", "RapidTable", "rapid_table"):
        lg = logging.getLogger(name)
        lg.setLevel(logging.ERROR)
        lg.propagate = False


def engine():
    global _engine
    require("site", "py-ort")
    require("site", "py-ocr")
    with _lock:
        if _engine is None:
            from rapidocr import RapidOCR
            _quiet()
            _engine = RapidOCR(params={"Global.log_level": "error"}) if _accepts_params() else RapidOCR()
            _quiet()
        return _engine


def _accepts_params():
    try:
        from rapidocr import RapidOCR  # noqa: F401
        import inspect
        return "params" in inspect.signature(RapidOCR.__init__).parameters
    except Exception:
        return False


def find_links(text):
    out = []
    for m in URL_RE.finditer(text):
        s = m.group(0).rstrip(".,;:")
        kind = "email" if "@" in s and not s.lower().startswith(("http", "www")) else "url"
        out.append({"text": s, "start": m.start(), "end": m.start() + len(s), "kind": kind})
    return out


def run(img):
    """img：BGR ndarray。返回 [(box(4点), text, score)]，按阅读顺序排列。"""
    eng = engine()
    with _lock:
        res = eng(img)
    if res is None or res.boxes is None or res.txts is None:
        return []
    items = []
    for b, t, s in zip(res.boxes, res.txts, res.scores):
        box = [[float(p[0]), float(p[1])] for p in b]
        items.append((box, str(t), float(s)))
    return sort_reading_order(items)


def sort_reading_order(items):
    """按行排序：先按 y 中心，同一行（垂直重叠过半）内按 x。"""
    def ext(b):
        xs = [p[0] for p in b]
        ys = [p[1] for p in b]
        return min(xs), min(ys), max(xs), max(ys)
    items = sorted(items, key=lambda it: (ext(it[0])[1] + ext(it[0])[3]) / 2)
    rows = []
    for it in items:
        x0, y0, x1, y1 = ext(it[0])
        if rows:
            r = rows[-1]
            ry0, ry1 = r["y0"], r["y1"]
            ov = min(y1, ry1) - max(y0, ry0)
            if ov > 0.5 * min(y1 - y0, ry1 - ry0):
                r["items"].append(it)
                r["y0"], r["y1"] = min(ry0, y0), max(ry1, y1)
                continue
        rows.append({"y0": y0, "y1": y1, "items": [it]})
    out = []
    for r in rows:
        out.extend(sorted(r["items"], key=lambda it: ext(it[0])[0]))
    return out


def ocr_image(path, task=None):
    """截图识字：返回行级结果（含坐标，供界面叠加可选中文字层）。"""
    img = imread(path)
    h, w = img.shape[:2]
    if task:
        task.progress(10, "正在识别文字…")
    items = run(img)
    lines = []
    for box, text, score in items:
        lines.append({"text": text, "box": box, "score": round(score, 4), "links": find_links(text)})
    full = join_lines(items)
    return {"width": w, "height": h, "lines": lines, "text": full, "links": find_links(full)}


def join_lines(items):
    """把同一行的多个文本框用空格连接，不同行用换行连接。"""
    if not items:
        return ""
    out, prev = [], None
    for box, text, _ in items:
        ys = [p[1] for p in box]
        y0, y1 = min(ys), max(ys)
        if prev is not None:
            ov = min(y1, prev[1]) - max(y0, prev[0])
            same = ov > 0.5 * min(y1 - y0, prev[1] - prev[0])
            out.append(" " if same else "\n")
            if same:
                prev = (min(y0, prev[0]), max(y1, prev[1]))
            else:
                prev = (y0, y1)
        else:
            prev = (y0, y1)
        out.append(text)
    return "".join(out)


def check():
    try:
        engine()
    except UserError:
        raise
    return True
