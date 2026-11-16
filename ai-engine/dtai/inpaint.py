"""去水印：用户用画笔涂出区域（mask），LaMa（big-lama ONNX，512×512 固定输入）按周围画面修补。

大图不会整体缩放到 512：只裁出每块涂抹区域周围的画面（留足上下文）送进模型，结果按蒙版羽化贴回原图，
其余像素保持原样、原分辨率。
"""
import os
import threading

from .common import model_file, imread, imwrite, UserError, cpu_threads, unique_path

_lock = threading.Lock()
_sess = None
SIZE = 512


def session():
    global _sess
    with _lock:
        if _sess is None:
            import onnxruntime as ort
            so = ort.SessionOptions()
            so.intra_op_num_threads = cpu_threads()
            _sess = ort.InferenceSession(model_file("m-lama", "lama_fp32.onnx"), so,
                                         providers=["CPUExecutionProvider"])
        return _sess


def _boxes(mask):
    """把蒙版的连通区域合并成若干裁剪框（框之间留出上下文，重叠的合并）。"""
    import cv2
    n, _, stats, _ = cv2.connectedComponentsWithStats((mask > 0).astype("uint8"), 8)
    H, W = mask.shape[:2]
    boxes = []
    for i in range(1, n):
        x, y, w, h, area = stats[i]
        if area < 4:
            continue
        side = max(w, h)
        ctx = max(48, int(side * 0.6))
        boxes.append([max(0, x - ctx), max(0, y - ctx), min(W, x + w + ctx), min(H, y + h + ctx)])
    merged = True
    while merged and len(boxes) > 1:
        merged = False
        out = []
        while boxes:
            b = boxes.pop()
            for o in boxes:
                if b[0] < o[2] and o[0] < b[2] and b[1] < o[3] and o[1] < b[3]:
                    boxes.remove(o)
                    b = [min(b[0], o[0]), min(b[1], o[1]), max(b[2], o[2]), max(b[3], o[3])]
                    merged = True
            out.append(b)
        boxes = out
    return boxes


def _square(b, W, H):
    """尽量把框扩成正方形（模型输入是正方形，避免拉伸变形）。"""
    x0, y0, x1, y1 = b
    w, h = x1 - x0, y1 - y0
    side = min(max(w, h, 64), max(W, H))
    cx, cy = (x0 + x1) / 2, (y0 + y1) / 2
    nx0 = int(max(0, min(W - min(side, W), cx - side / 2)))
    ny0 = int(max(0, min(H - min(side, H), cy - side / 2)))
    return nx0, ny0, int(min(W, nx0 + side)), int(min(H, ny0 + side))


def _run(img, mask):
    import cv2
    import numpy as np
    h, w = img.shape[:2]
    im = cv2.resize(img, (SIZE, SIZE), interpolation=cv2.INTER_AREA if max(h, w) > SIZE else cv2.INTER_CUBIC)
    mk = cv2.resize(mask, (SIZE, SIZE), interpolation=cv2.INTER_NEAREST)
    rgb = cv2.cvtColor(im, cv2.COLOR_BGR2RGB).astype(np.float32) / 255.0
    x = np.transpose(rgb, (2, 0, 1))[None]
    m = (mk > 127).astype(np.float32)[None, None]
    out = session().run(None, {"image": x, "mask": m})[0][0]
    out = np.transpose(out, (1, 2, 0))
    if out.max() <= 1.5:
        out = out * 255.0
    out = cv2.cvtColor(np.clip(out, 0, 255).astype(np.uint8), cv2.COLOR_RGB2BGR)
    return cv2.resize(out, (w, h), interpolation=cv2.INTER_CUBIC if max(h, w) > SIZE else cv2.INTER_AREA)


def inpaint(img, mask, task=None):
    """img：BGR；mask：单通道，>0 表示要去除的区域。返回修补后的 BGR 图。"""
    import cv2
    import numpy as np
    H, W = img.shape[:2]
    if mask.shape[:2] != (H, W):
        mask = cv2.resize(mask, (W, H), interpolation=cv2.INTER_NEAREST)
    mask = np.where(mask > 127, 255, 0).astype(np.uint8)
    if not mask.any():
        raise UserError("请先用画笔涂抹要去除的水印区域", code="bad_input")
    # 稍微外扩，盖住水印边缘的半透明/抗锯齿像素
    mask = cv2.dilate(mask, np.ones((7, 7), np.uint8), iterations=1)
    out = img.copy()
    boxes = [_square(b, W, H) for b in _boxes(mask)]
    for i, (x0, y0, x1, y1) in enumerate(boxes):
        if task:
            task.check()
            task.progress(10 + 85 * i / max(1, len(boxes)), "正在修补第 %d / %d 处…" % (i + 1, len(boxes)))
        crop, cm = img[y0:y1, x0:x1], mask[y0:y1, x0:x1]
        res = _run(crop, cm)
        # 羽化贴回：只替换蒙版内的像素，边缘 3px 渐变
        alpha = cv2.GaussianBlur(cm.astype(np.float32) / 255.0, (0, 0), 1.5)
        alpha = np.maximum(alpha, (cm > 0).astype(np.float32))[..., None]
        blended = res.astype(np.float32) * alpha + out[y0:y1, x0:x1].astype(np.float32) * (1 - alpha)
        out[y0:y1, x0:x1] = np.clip(blended, 0, 255).astype(np.uint8)
    return out


def inpaint_file(path, mask_path, out=None, task=None):
    import cv2
    import numpy as np
    img = imread(path)
    data = np.fromfile(mask_path, dtype=np.uint8)
    mask = cv2.imdecode(data, cv2.IMREAD_UNCHANGED)
    if mask is None:
        raise UserError("蒙版无效", code="bad_input")
    if mask.ndim == 3:
        # 画笔图层：带透明通道时取 alpha，否则取亮度
        mask = mask[..., 3] if mask.shape[2] == 4 else cv2.cvtColor(mask, cv2.COLOR_BGR2GRAY)
    if task:
        task.progress(5, "正在加载去水印模型…")
    global _sess
    try:
        res = inpaint(img, mask, task)
    finally:
        with _lock:  # LaMa 占用约 1GB 内存，用完即释放（重新加载只需 1～2 秒）
            _sess = None
    if not out:
        base, _ = os.path.splitext(path)
        out = base + "_去水印.png"
    out = unique_path(out)
    imwrite(out, res)
    if task:
        task.progress(100, "完成")
    return {"output": out, "width": int(img.shape[1]), "height": int(img.shape[0])}
