"""图片 / 扫描版 PDF → Word、Excel。

流程：RapidOCR 识别文字行 → RapidLayout（PP 版面分析，CDLA 中文文档模型）找出标题 / 正文 / 表格 / 图片区域 →
表格区域用 RapidTable（SLANet-plus）识别行列结构 → 按原位置顺序写入 docx（python-docx）或 xlsx（openpyxl）。
“排版不变”只能尽量接近：保留阅读顺序、标题、段落缩进、字号大小关系、表格（含合并单元格）、图片。
"""
import html.parser
import io
import logging
import os
import threading

from . import ocr as ocrmod
from .common import require, model_file, imread, UserError, unique_path

_lock = threading.Lock()
_layout = None
_table = None


def _layout_engine():
    global _layout
    with _lock:
        if _layout is None:
            from rapid_layout import RapidLayout
            _layout = RapidLayout()
            for n in ("RapidLayout", "rapid_layout"):
                logging.getLogger(n).setLevel(logging.ERROR)
        return _layout


def _table_engine():
    global _table
    with _lock:
        if _table is None:
            from rapid_table import RapidTable, RapidTableInput
            path = model_file("m-table", "slanet-plus.onnx")
            _table = RapidTable(RapidTableInput(model_dir_or_path=path, use_ocr=False))
            for n in ("RapidTable", "rapid_table"):
                logging.getLogger(n).setLevel(logging.ERROR)
        return _table


# ---------------------------------------------------------------- 表格 HTML → 网格
class _TableParser(html.parser.HTMLParser):
    def __init__(self):
        super().__init__()
        self.rows, self.cur, self.cell = [], None, None

    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        if tag == "tr":
            self.cur = []
            self.rows.append(self.cur)
        elif tag in ("td", "th"):
            if self.cur is None:
                self.cur = []
                self.rows.append(self.cur)
            self.cell = {"text": "", "colspan": int(a.get("colspan") or 1), "rowspan": int(a.get("rowspan") or 1)}
            self.cur.append(self.cell)

    def handle_endtag(self, tag):
        if tag in ("td", "th"):
            self.cell = None

    def handle_data(self, data):
        if self.cell is not None:
            self.cell["text"] += data


def html_to_grid(html_text):
    """返回 (nrows, ncols, cells)，cells = [{r, c, rs, cs, text}]。"""
    p = _TableParser()
    p.feed(html_text or "")
    occupied = set()
    cells = []
    for r, row in enumerate(p.rows):
        c = 0
        for cell in row:
            while (r, c) in occupied:
                c += 1
            rs, cs = max(1, cell["rowspan"]), max(1, cell["colspan"])
            for i in range(rs):
                for j in range(cs):
                    occupied.add((r + i, c + j))
            cells.append({"r": r, "c": c, "rs": rs, "cs": cs, "text": cell["text"].strip()})
            c += cs
    nrows = max([k[0] for k in occupied], default=-1) + 1
    ncols = max([k[1] for k in occupied], default=-1) + 1
    return nrows, ncols, cells


# ---------------------------------------------------------------- 版面分析
def _ext(box):
    xs = [p[0] for p in box]
    ys = [p[1] for p in box]
    return min(xs), min(ys), max(xs), max(ys)


def _center_in(box, reg, pad=4):
    x0, y0, x1, y1 = _ext(box)
    cx, cy = (x0 + x1) / 2, (y0 + y1) / 2
    return reg[0] - pad <= cx <= reg[2] + pad and reg[1] - pad <= cy <= reg[3] + pad


def analyze(img, task=None, p0=0, p1=100, with_tables=True):
    """返回页面元素列表（按阅读顺序）：
    {"type":"heading"|"para", "lines":[...], "x":, "y":, "h": 行高}
    {"type":"table", "grid":(nrows,ncols,cells), "box":[x0,y0,x1,y1], "colw":[像素宽]}
    {"type":"figure", "png": bytes, "box":[...]}
    """
    import cv2
    import numpy as np  # noqa: F401

    def prog(f, msg):
        if task:
            task.check()
            task.progress(p0 + (p1 - p0) * f, msg)

    H, W = img.shape[:2]
    prog(0.05, "正在识别文字…")
    lines = ocrmod.run(img)
    prog(0.45, "正在分析版面…")
    regions = []
    try:
        lay = _layout_engine()(img)
        if lay.boxes is not None:
            for b, c, s in zip(lay.boxes, lay.class_names, lay.scores):
                regions.append({"cls": str(c), "box": [float(v) for v in b], "score": float(s)})
    except Exception as e:  # 版面分析失败时退化为纯文字行
        logging.getLogger("dtai").warning("layout failed: %s", e)

    tables = [r for r in regions if r["cls"] == "table"] if with_tables else []
    figures = [r for r in regions if r["cls"] in ("figure", "image")]
    titles = [r for r in regions if r["cls"] in ("title", "doc_title", "paragraph_title")]
    used = set()
    elements = []

    for ti, reg in enumerate(tables):
        prog(0.5 + 0.4 * ti / max(1, len(tables)), "正在识别表格结构…")
        x0, y0, x1, y1 = [int(round(v)) for v in reg["box"]]
        pad = 6
        x0, y0 = max(0, x0 - pad), max(0, y0 - pad)
        x1, y1 = min(W, x1 + pad), min(H, y1 + pad)
        crop = img[y0:y1, x0:x1]
        idx = [i for i, it in enumerate(lines) if _center_in(it[0], (x0, y0, x1, y1))]
        if not idx:
            continue
        boxes = np.array([[[p[0] - x0, p[1] - y0] for p in lines[i][0]] for i in idx], dtype=np.float32)
        txts = tuple(lines[i][1] for i in idx)
        scores = tuple(lines[i][2] for i in idx)
        try:
            from rapid_table import RapidTableInput  # noqa: F401
            eng = _table_engine()
            eng.cfg.use_ocr = True
            with _lock:
                res = eng(crop, ocr_results=[(boxes, txts, scores)])
            grid = html_to_grid(res.pred_htmls[0])
            cb = res.cell_bboxes[0] if res.cell_bboxes else None
        except UserError:
            raise
        except Exception as e:
            logging.getLogger("dtai").warning("table failed: %s", e)
            continue
        if grid[0] == 0 or grid[1] == 0:
            continue
        used.update(idx)
        elements.append({"type": "table", "grid": grid, "box": [x0, y0, x1, y1],
                         "colw": _col_widths(grid, x1 - x0)})

    for reg in figures:
        x0, y0, x1, y1 = [int(round(v)) for v in reg["box"]]
        if (x1 - x0) < 20 or (y1 - y0) < 20:
            continue
        inside = [i for i, it in enumerate(lines) if _center_in(it[0], (x0, y0, x1, y1), pad=0) and i not in used]
        used.update(inside)
        ok, buf = cv2.imencode(".png", img[y0:y1, x0:x1])
        if ok:
            elements.append({"type": "figure", "png": buf.tobytes(), "box": [x0, y0, x1, y1]})

    # 剩余文字行 → 段落（同一行的多个框合并；行距小且左对齐的连续行合并为一段）
    rest = [lines[i] for i in range(len(lines)) if i not in used]
    rows = _group_rows(rest)
    right = max([max(_ext(b)[2] for b, _, _ in row) for row in rows] or [W])
    paras = []
    for row in rows:
        x0 = min(_ext(b)[0] for b, _, _ in row)
        y0 = min(_ext(b)[1] for b, _, _ in row)
        x1 = max(_ext(b)[2] for b, _, _ in row)
        y1 = max(_ext(b)[3] for b, _, _ in row)
        text = _join_row(row)
        is_title = any(_center_in(row[0][0], t["box"]) for t in titles)
        prev = paras[-1] if paras else None
        lh = y1 - y0
        if (prev and not is_title and prev["type"] == "para" and abs(prev["x"] - x0) < max(12, lh * 0.8)
                and 0 <= y0 - prev["y1"] < lh * 0.9 and abs(prev["h"] - lh) < lh * 0.35
                and prev["x1"] >= right - 2.5 * lh and not prev["lines"][-1].rstrip().endswith(_SENT_END)
                and not _in_any_table(y0, elements)):
            prev["lines"].append(text)
            prev["y1"], prev["x1"] = y1, max(prev["x1"], x1)
            continue
        paras.append({"type": "heading" if is_title else "para", "lines": [text], "x": x0, "y": y0,
                      "y1": y1, "x1": x1, "h": lh})
    elements.extend(paras)
    elements.sort(key=lambda e: (e["box"][1] if "box" in e else e["y"]))
    prog(1.0, "版面分析完成")
    return {"width": W, "height": H, "elements": elements, "line_count": len(lines)}


_SENT_END = tuple("。！？；：.!?;:")


def _in_any_table(y, elements):
    return any(e["type"] == "table" and e["box"][1] <= y <= e["box"][3] for e in elements)


def _group_rows(items):
    rows = []
    for it in items:
        x0, y0, x1, y1 = _ext(it[0])
        if rows:
            r = rows[-1]
            ry0 = min(_ext(b)[1] for b, _, _ in r)
            ry1 = max(_ext(b)[3] for b, _, _ in r)
            if min(y1, ry1) - max(y0, ry0) > 0.5 * min(y1 - y0, ry1 - ry0):
                r.append(it)
                continue
        rows.append([it])
    for r in rows:
        r.sort(key=lambda it: _ext(it[0])[0])
    return rows


def _join_row(row):
    out = ""
    for i, (b, t, _) in enumerate(row):
        if i:
            gap = _ext(b)[0] - _ext(row[i - 1][0])[2]
            h = _ext(b)[3] - _ext(b)[1]
            out += "\t" if gap > h * 2 else " "
        out += t
    return out


def _join_para(lines):
    out = ""
    for ln in lines:
        if out and out[-1].isascii() and out[-1].isalnum() and ln[:1].isascii() and ln[:1].isalnum():
            out += " "
        elif out and out[-1] == "-" and ln[:1].isascii():
            out = out[:-1]
        out += ln
    return out


def _col_widths(grid, total_px):
    nrows, ncols, cells = grid
    w = [1.0] * ncols
    for c in cells:
        if c["cs"] == 1:
            w[c["c"]] = max(w[c["c"]], _text_units(c["text"]))
    s = sum(w)
    return [total_px * x / s for x in w]


def _text_units(s):
    return sum(2 if ord(ch) > 0x2E80 else 1 for ch in s) + 2


# ---------------------------------------------------------------- 输出 Word
def _set_east_asia_font(run_or_style, name="微软雅黑"):
    from docx.oxml.ns import qn
    el = run_or_style.element if hasattr(run_or_style, "element") else run_or_style._element
    rpr = el.get_or_add_rPr()
    fonts = rpr.find(qn("w:rFonts"))
    if fonts is None:
        fonts = rpr.makeelement(qn("w:rFonts"), {})
        rpr.append(fonts)
    fonts.set(qn("w:eastAsia"), name)


def _new_document():
    from docx import Document
    from docx.shared import Cm, Pt
    doc = Document()
    sec = doc.sections[0]
    sec.page_width, sec.page_height = Cm(21), Cm(29.7)
    sec.left_margin = sec.right_margin = Cm(2)
    sec.top_margin = sec.bottom_margin = Cm(2)
    st = doc.styles["Normal"]
    st.font.name = "Calibri"
    st.font.size = Pt(10.5)
    _set_east_asia_font(st)
    return doc


def _write_page_docx(doc, page, first=True):
    from docx.shared import Cm, Pt
    from docx.enum.text import WD_BREAK
    from docx.enum.table import WD_TABLE_ALIGNMENT
    W = page["width"]
    content_cm = 17.0
    cm_per_px = content_cm / max(1, W)
    if not first:
        doc.add_paragraph().add_run().add_break(WD_BREAK.PAGE)
    body = [e["h"] for e in page["elements"] if e["type"] == "para"]
    body_h = sorted(body)[len(body) // 2] if body else 20
    # 正文字号：行高（像素）→ 厘米 → 磅，0.75 为字高 / 行框高的经验比例
    body_pt = max(8.0, min(14.0, body_h * cm_per_px * 28.35 * 0.72))
    margin_px = min([e["x"] for e in page["elements"] if e["type"] in ("para", "heading")] or [0])
    for e in page["elements"]:
        if e["type"] in ("para", "heading"):
            text = _join_para(e["lines"])
            pt = max(8.0, min(36.0, e["h"] * cm_per_px * 28.35 * 0.72))
            heading = e["type"] == "heading" or (pt > body_pt * 1.3 and len(text) < 60 and len(e["lines"]) == 1)
            p = doc.add_paragraph()
            indent = (e["x"] - margin_px) * cm_per_px
            if indent > 0.4:
                if indent > content_cm * 0.25 and e["x1"] < W * 0.75 and abs((e["x"] + e["x1"]) / 2 - W / 2) < W * 0.08:
                    p.alignment = 1  # 居中
                else:
                    p.paragraph_format.left_indent = Cm(min(indent, content_cm * 0.6))
            p.paragraph_format.space_after = Pt(4)
            run = p.add_run(text)
            run.font.size = Pt(round(max(pt, body_pt * 1.25) if heading else (body_pt if abs(pt - body_pt) < 1.5 else pt), 1))
            run.bold = bool(heading)
            _set_east_asia_font(run)
        elif e["type"] == "table":
            nrows, ncols, cells = e["grid"]
            t = doc.add_table(rows=nrows, cols=ncols)
            t.style = "Table Grid"
            t.alignment = WD_TABLE_ALIGNMENT.CENTER
            for c in cells:
                a = t.cell(c["r"], c["c"])
                if c["rs"] > 1 or c["cs"] > 1:
                    b = t.cell(min(nrows - 1, c["r"] + c["rs"] - 1), min(ncols - 1, c["c"] + c["cs"] - 1))
                    a = a.merge(b)
                a.text = c["text"]
                for par in a.paragraphs:
                    for r in par.runs:
                        r.font.size = Pt(round(body_pt, 1))
                        _set_east_asia_font(r)
            total_cm = min(content_cm, (e["box"][2] - e["box"][0]) * cm_per_px)
            colw = e["colw"]
            s = sum(colw) or 1
            for j, col in enumerate(t.columns):
                wcm = Cm(total_cm * colw[j] / s)
                for cell in col.cells:
                    cell.width = wcm
            doc.add_paragraph()
        elif e["type"] == "figure":
            x0, y0, x1, y1 = e["box"]
            wcm = min(content_cm, (x1 - x0) * cm_per_px)
            doc.add_picture(io.BytesIO(e["png"]), width=Cm(wcm))


def image_to_docx(path, out, task=None):
    import docx  # noqa: F401  （内置 Python 自带 python-docx）
    require("models", "m-table")
    img = imread(path)
    page = analyze(img, task, 0, 90)
    doc = _new_document()
    _write_page_docx(doc, page)
    out = unique_path(out)
    doc.save(out)
    if task:
        task.progress(100, "完成")
    return {"output": out, "elements": _summary(page)}


def _summary(page):
    s = {}
    for e in page["elements"]:
        s[e["type"]] = s.get(e["type"], 0) + 1
    return s


# ---------------------------------------------------------------- 输出 Excel
def image_to_xlsx(path, out, task=None):
    require("models", "m-table")
    try:
        import openpyxl  # noqa: F401
    except ImportError:
        raise UserError("缺少组件：文字识别（OCR / 版面分析），请先在“设置 → AI 模型”中下载", code="missing_component",
                        component="py-ocr")
    img = imread(path)
    page = analyze(img, task, 0, 90)
    wb = _pages_to_xlsx([page])
    out = unique_path(out)
    wb.save(out)
    if task:
        task.progress(100, "完成")
    return {"output": out, "elements": _summary(page)}


def _pages_to_xlsx(pages):
    from openpyxl import Workbook
    from openpyxl.styles import Alignment, Border, Font, Side
    from openpyxl.utils import get_column_letter
    wb = Workbook()
    ws = wb.active
    ws.title = "识别结果"
    thin = Side(style="thin", color="000000")
    border = Border(left=thin, right=thin, top=thin, bottom=thin)
    row = 1
    colw = {}
    for pi, page in enumerate(pages):
        if pi:
            row += 1
        for e in page["elements"]:
            if e["type"] in ("para", "heading"):
                text = _join_para(e["lines"])
                parts = text.split("\t")
                for j, ptxt in enumerate(parts):
                    c = ws.cell(row=row, column=1 + j, value=_cell_value(ptxt))
                    if e["type"] == "heading":
                        c.font = Font(bold=True, size=14)
                row += 1
            elif e["type"] == "table":
                nrows, ncols, cells = e["grid"]
                for c in cells:
                    r0, c0 = row + c["r"], 1 + c["c"]
                    cell = ws.cell(row=r0, column=c0, value=_cell_value(c["text"]))
                    cell.alignment = Alignment(wrap_text=True, vertical="center")
                    if c["rs"] > 1 or c["cs"] > 1:
                        ws.merge_cells(start_row=r0, start_column=c0, end_row=r0 + c["rs"] - 1,
                                       end_column=c0 + c["cs"] - 1)
                    if c["cs"] == 1:
                        colw[c0] = max(colw.get(c0, 8), min(60, _text_units(c["text"]) + 1))
                for r in range(nrows):
                    for j in range(ncols):
                        ws.cell(row=row + r, column=1 + j).border = border
                row += nrows + 1
            elif e["type"] == "figure":
                row += 1
    for c, w in colw.items():
        ws.column_dimensions[get_column_letter(c)].width = w
    return wb


def _cell_value(s):
    """数字单元格写成真正的数字（去掉千分位），其余保持文本。"""
    t = s.strip().replace(",", "").replace("，", "")
    if t and len(t) < 16:
        try:
            if t.endswith("%"):
                return s.strip()
            v = float(t)
            if t.lstrip("-").isdigit() and not (len(t) > 1 and t.startswith("0")):
                return int(t)
            if "." in t:
                return v
        except ValueError:
            pass
    return s


# ---------------------------------------------------------------- 扫描版 PDF → Word
def pdf_to_docx(path, out, task=None, dpi=200):
    import fitz  # PyMuPDF（主程序内置）
    import numpy as np
    import cv2
    require("models", "m-table")
    try:
        pdf = fitz.open(path)
    except Exception as e:
        raise UserError("无法打开 PDF：%s" % e, code="bad_input")
    if pdf.needs_pass:
        raise UserError("PDF 已加密，请先解除密码", code="encrypted")
    n = pdf.page_count
    doc = _new_document()
    for i in range(n):
        if task:
            task.check()
        page = pdf.load_page(i)
        pix = page.get_pixmap(matrix=fitz.Matrix(dpi / 72, dpi / 72), alpha=False)
        arr = np.frombuffer(pix.samples, dtype=np.uint8).reshape(pix.height, pix.width, pix.n)
        img = cv2.cvtColor(arr, cv2.COLOR_RGB2BGR) if pix.n == 3 else cv2.cvtColor(arr, cv2.COLOR_GRAY2BGR)
        p0, p1 = 100 * i / n, 100 * (i + 1) / n
        if task:
            task.progress(p0, "正在识别第 %d / %d 页…" % (i + 1, n))
        res = analyze(img, task, p0, p1)
        _write_page_docx(doc, res, first=(i == 0))
    out = unique_path(out)
    doc.save(out)
    if task:
        task.progress(100, "完成")
    return {"output": out, "pages": n}


def pdf_has_text(path):
    import fitz
    pdf = fitz.open(path)
    for i in range(min(pdf.page_count, 5)):
        if pdf.load_page(i).get_text().strip():
            return True
    return False
