# -*- coding: utf-8 -*-
"""桌面工具箱 - 文档格式转换脚本 (由 Go 主程序调用，使用随附的嵌入式 Python 运行)

用法:
    python convert.py --mode pdf2docx --input a.pdf --output a.docx
    python convert.py --mode md2docx  --input a.md  --output a.docx

标准输出每行一个 JSON 事件:
    {"event": "progress", "pct": 0-100, "msg": "..."}
    {"event": "done", "output": "..."}
    {"event": "error", "code": "scanned|encrypted|not_found|failed", "message": "..."}
退出码: 0 成功, 2 已知错误, 1 未知错误
"""
import argparse
import json
import logging
import os
import re
import sys
import traceback

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    sys.stderr.reconfigure(encoding="utf-8", errors="replace")
except Exception:  # pragma: no cover
    pass


def emit(event, **kw):
    kw["event"] = event
    sys.stdout.write(json.dumps(kw, ensure_ascii=False) + "\n")
    sys.stdout.flush()


class KnownError(Exception):
    def __init__(self, code, message):
        super().__init__(message)
        self.code = code
        self.message = message


# ---------------------------------------------------------------- PDF -> DOCX

class _ProgressHandler(logging.Handler):
    """解析 pdf2docx 的日志，换算成总体进度。"""

    def __init__(self):
        super().__init__(logging.INFO)
        self.phase = 0  # 3 = parsing, 4 = creating

    def emit(self, record):
        try:
            msg = record.getMessage()
        except Exception:
            return
        if "[3/4]" in msg:
            self.phase = 3
            globals()["emit"]("progress", pct=10, msg="正在解析页面…")
            return
        if "[4/4]" in msg:
            self.phase = 4
            globals()["emit"]("progress", pct=60, msg="正在生成 Word 页面…")
            return
        m = re.match(r"\((\d+)/(\d+)\)", msg)
        if m and self.phase in (3, 4):
            i, n = int(m.group(1)), max(1, int(m.group(2)))
            base, span = (10, 50) if self.phase == 3 else (60, 38)
            globals()["emit"]("progress", pct=int(base + span * i / n),
                              msg="第 %d/%d 页" % (i, n))


def check_pdf(path):
    import fitz  # PyMuPDF

    try:
        doc = fitz.open(path)
    except Exception as e:
        raise KnownError("failed", "无法打开 PDF：%s" % e)
    try:
        if doc.needs_pass:
            raise KnownError("encrypted", "该 PDF 已加密（需要密码），无法转换。")
        n = doc.page_count
        if n == 0:
            raise KnownError("failed", "PDF 没有任何页面。")
        text_chars = 0
        image_pages = 0
        sample = range(n) if n <= 30 else list(range(15)) + list(range(n - 15, n))
        for i in sample:
            page = doc[i]
            text_chars += len(page.get_text("text").strip())
            if page.get_images(full=False):
                image_pages += 1
        per_page = text_chars / float(len(sample))
        if per_page < 10 and image_pages > 0:
            raise KnownError(
                "scanned",
                "该 PDF 是扫描件/图片型 PDF（几乎没有可提取的文字），需要 OCR 识别，本工具不包含 OCR 功能。",
            )
        return n
    finally:
        doc.close()


def pdf2docx(src, dst):
    check_pdf(src)
    from pdf2docx import Converter

    root = logging.getLogger()
    root.handlers[:] = []
    root.setLevel(logging.INFO)
    root.addHandler(_ProgressHandler())
    emit("progress", pct=5, msg="正在打开文档…")
    cv = Converter(src)
    try:
        cv.convert(dst, start=0, end=None)
    finally:
        cv.close()


# ----------------------------------------------------------- Markdown -> DOCX

VOID = {"br", "hr", "img", "input", "meta", "link", "col", "area", "base", "wbr", "source"}
INLINE = {"a", "abbr", "b", "strong", "i", "em", "code", "span", "del", "s", "strike",
          "u", "ins", "sup", "sub", "br", "img", "kbd", "mark", "small", "q", "cite", "input"}
EAST_ASIA_FONT = "Microsoft YaHei"
MONO_FONT = "Consolas"
MAX_IMG_WIDTH_IN = 6.0


class Node(object):
    __slots__ = ("tag", "attrs", "children", "parent")

    def __init__(self, tag, attrs=None, parent=None):
        self.tag = tag
        self.attrs = dict(attrs or [])
        self.children = []
        self.parent = parent


def parse_html(html):
    from html.parser import HTMLParser

    root = Node("root")

    class P(HTMLParser):
        def __init__(self):
            super().__init__(convert_charrefs=True)
            self.cur = root

        def handle_starttag(self, tag, attrs):
            n = Node(tag, attrs, self.cur)
            self.cur.children.append(n)
            if tag not in VOID:
                self.cur = n

        def handle_startendtag(self, tag, attrs):
            self.cur.children.append(Node(tag, attrs, self.cur))

        def handle_endtag(self, tag):
            n = self.cur
            while n is not None and n.tag != tag:
                n = n.parent
            if n is not None and n.parent is not None:
                self.cur = n.parent

        def handle_data(self, data):
            self.cur.children.append(data)

    p = P()
    p.feed(html)
    p.close()
    return root


def is_inline(n):
    return isinstance(n, str) or n.tag in INLINE


class DocxWriter(object):
    def __init__(self, base_dir):
        import docx
        from docx.shared import Pt

        self.docx = docx
        self.base_dir = base_dir
        self.doc = docx.Document()
        self.ol_counters = []
        st = self.doc.styles["Normal"]
        st.font.name = "Calibri"
        st.font.size = Pt(10.5)
        self._set_east_asia(st.element, EAST_ASIA_FONT)
        for name in ["Heading %d" % i for i in range(1, 7)] + ["Title"]:
            try:
                self._set_east_asia(self.doc.styles[name].element, EAST_ASIA_FONT)
            except KeyError:
                pass

    # ---- low level helpers
    def _set_east_asia(self, el, font):
        from docx.oxml.ns import qn
        from docx.oxml import OxmlElement

        rpr = el.find(qn("w:rPr"))
        if rpr is None:
            rpr = OxmlElement("w:rPr")
            el.append(rpr)
        fonts = rpr.find(qn("w:rFonts"))
        if fonts is None:
            fonts = OxmlElement("w:rFonts")
            rpr.insert(0, fonts)
        fonts.set(qn("w:eastAsia"), font)

    def _shade(self, el_pr, fill):
        from docx.oxml import OxmlElement
        from docx.oxml.ns import qn

        shd = OxmlElement("w:shd")
        shd.set(qn("w:val"), "clear")
        shd.set(qn("w:color"), "auto")
        shd.set(qn("w:fill"), fill)
        el_pr.append(shd)

    def _border(self, p, side, color="BFBFBF", size="12", space="4"):
        from docx.oxml import OxmlElement
        from docx.oxml.ns import qn

        ppr = p._p.get_or_add_pPr()
        bdr = ppr.find(qn("w:pBdr"))
        if bdr is None:
            bdr = OxmlElement("w:pBdr")
            ppr.append(bdr)
        b = OxmlElement("w:" + side)
        b.set(qn("w:val"), "single")
        b.set(qn("w:sz"), size)
        b.set(qn("w:space"), space)
        b.set(qn("w:color"), color)
        bdr.append(b)

    def _hyperlink(self, paragraph, url, fmt):
        from docx.oxml import OxmlElement
        from docx.oxml.ns import qn

        if url.startswith("#"):
            return None
        r_id = paragraph.part.relate_to(
            url, "http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink",
            is_external=True)
        h = OxmlElement("w:hyperlink")
        h.set(qn("r:id"), r_id)
        paragraph._p.append(h)
        return h

    def _run(self, paragraph, text, fmt, container=None):
        from docx.shared import RGBColor, Pt
        from docx.text.run import Run
        from docx.oxml import OxmlElement

        if container is not None:
            r_el = OxmlElement("w:r")
            container.append(r_el)
            run = Run(r_el, paragraph)
            run.text = text
        else:
            run = paragraph.add_run(text)
        if fmt.get("b"):
            run.bold = True
        if fmt.get("i"):
            run.italic = True
        if fmt.get("u") or fmt.get("link"):
            run.underline = True
        if fmt.get("strike"):
            run.font.strike = True
        if fmt.get("sup"):
            run.font.superscript = True
        if fmt.get("sub"):
            run.font.subscript = True
        if fmt.get("link"):
            run.font.color.rgb = RGBColor(0x05, 0x63, 0xC1)
        if fmt.get("color"):
            run.font.color.rgb = fmt["color"]
        if fmt.get("code"):
            run.font.name = MONO_FONT
            run.font.size = Pt(9.5)
            self._set_east_asia(run._r, EAST_ASIA_FONT)
            if not fmt.get("pre"):
                self._shade(run._r.get_or_add_rPr(), "F2F2F2")
        if fmt.get("mark"):
            from docx.enum.text import WD_COLOR_INDEX
            run.font.highlight_color = WD_COLOR_INDEX.YELLOW
        return run

    def _image(self, paragraph, src, alt):
        from docx.shared import Inches

        path = src
        if src.lower().startswith("file:///"):
            path = src[8:]
        if re.match(r"^[a-z]+://", path, re.I) or path.startswith("data:"):
            self._run(paragraph, "[图片：%s]" % (alt or src), {"i": True})
            return
        try:
            from urllib.parse import unquote
            path = unquote(path)
        except Exception:
            pass
        if not os.path.isabs(path):
            path = os.path.join(self.base_dir, path)
        if not os.path.isfile(path):
            self._run(paragraph, "[图片缺失：%s]" % (alt or src), {"i": True})
            return
        try:
            from docx.image.image import Image as DImage

            img = DImage.from_file(path)
            w_in = img.px_width / float(img.horz_dpi or 96)
            run = paragraph.add_run()
            if w_in > MAX_IMG_WIDTH_IN:
                run.add_picture(path, width=Inches(MAX_IMG_WIDTH_IN))
            else:
                run.add_picture(path)
        except Exception:
            self._run(paragraph, "[无法插入图片：%s]" % (alt or src), {"i": True})

    # ---- inline rendering
    def inline(self, paragraph, nodes, fmt, container=None, strip_lead=False):
        for idx, n in enumerate(nodes):
            if isinstance(n, str):
                text = n
                if not fmt.get("pre"):
                    text = re.sub(r"\s*\n\s*", " ", text)
                    text = re.sub(r"[ \t]+", " ", text)
                    if strip_lead and idx == 0:
                        text = text.lstrip()
                if text:
                    if fmt.get("pre") and "\n" in text:
                        lines = text.split("\n")
                        for li, line in enumerate(lines):
                            r = self._run(paragraph, line, fmt, container)
                            if li < len(lines) - 1:
                                r.add_break()
                    else:
                        self._run(paragraph, text, fmt, container)
                continue
            t = n.tag
            f = dict(fmt)
            if t in ("b", "strong"):
                f["b"] = True
            elif t in ("i", "em", "cite"):
                f["i"] = True
            elif t in ("del", "s", "strike"):
                f["strike"] = True
            elif t in ("u", "ins"):
                f["u"] = True
            elif t in ("code", "kbd"):
                f["code"] = True
            elif t == "sup":
                f["sup"] = True
            elif t == "sub":
                f["sub"] = True
            elif t == "mark":
                f["mark"] = True
            elif t == "br":
                r = self._run(paragraph, "", fmt, container)
                r.add_break()
                continue
            elif t == "img":
                self._image(paragraph, n.attrs.get("src", ""), n.attrs.get("alt", ""))
                continue
            elif t == "input":
                if n.attrs.get("type") == "checkbox":
                    self._run(paragraph, "☑ " if "checked" in n.attrs else "☐ ", fmt, container)
                continue
            elif t == "a":
                href = n.attrs.get("href", "")
                if href and container is None:
                    f["link"] = True
                    h = self._hyperlink(paragraph, href, f)
                    if h is not None:
                        self.inline(paragraph, n.children, f, container=h)
                        continue
            self.inline(paragraph, n.children, f, container)

    # ---- block rendering
    def para(self, style=None):
        p = self.doc.add_paragraph()
        if style:
            try:
                p.style = self.doc.styles[style]
            except KeyError:
                pass
        return p

    def flush_inline(self, buf, ctx):
        if not buf:
            return
        if all(isinstance(x, str) and not x.strip() for x in buf):
            del buf[:]
            return
        p = self.para(ctx.get("style"))
        self.apply_ctx(p, ctx)
        self.inline(p, buf, ctx.get("fmt", {}), strip_lead=True)
        del buf[:]

    def apply_ctx(self, p, ctx):
        from docx.shared import Pt, RGBColor

        if ctx.get("indent"):
            p.paragraph_format.left_indent = Pt(ctx["indent"])
        if ctx.get("quote"):
            self._border(p, "left", color="C8C8C8", size="18", space="8")

    def blocks(self, node, ctx):
        buf = []
        for n in node.children:
            if is_inline(n):
                buf.append(n)
                continue
            self.flush_inline(buf, ctx)
            self.block(n, ctx)
        self.flush_inline(buf, ctx)

    def block(self, n, ctx):
        from docx.shared import Pt, RGBColor

        t = n.tag
        if t in ("h1", "h2", "h3", "h4", "h5", "h6"):
            p = self.doc.add_heading(level=int(t[1]))
            self.inline(p, n.children, {}, strip_lead=True)
        elif t == "p":
            p = self.para(ctx.get("style"))
            self.apply_ctx(p, ctx)
            self.inline(p, n.children, ctx.get("fmt", {}), strip_lead=True)
        elif t in ("ul", "ol"):
            self.list_block(n, ctx, ordered=(t == "ol"))
        elif t == "pre":
            self.code_block(n, ctx)
        elif t == "blockquote":
            c = dict(ctx)
            c["indent"] = ctx.get("indent", 0) + 18
            c["quote"] = True
            f = dict(ctx.get("fmt", {}))
            f["i"] = True
            f["color"] = RGBColor(0x59, 0x59, 0x59)
            c["fmt"] = f
            self.blocks(n, c)
        elif t == "table":
            self.table(n)
        elif t == "hr":
            p = self.para()
            self._border(p, "bottom", color="BFBFBF", size="6", space="1")
        elif t in ("div", "section", "article", "details", "summary", "body", "html",
                   "dl", "dd", "dt", "figure", "figcaption", "header", "footer", "nav", "main"):
            c = dict(ctx)
            if t == "dt":
                f = dict(ctx.get("fmt", {}))
                f["b"] = True
                c["fmt"] = f
            if t == "dd":
                c["indent"] = ctx.get("indent", 0) + 18
            self.blocks(n, c)
        else:
            # 未知块元素：按普通段落处理
            self.blocks(n, ctx)

    def list_block(self, n, ctx, ordered):
        from docx.shared import Pt

        level = ctx.get("list_level", 0)
        number = 1
        try:
            number = int(n.attrs.get("start", "1"))
        except ValueError:
            pass
        for li in n.children:
            if isinstance(li, str) or li.tag != "li":
                continue
            indent = ctx.get("indent", 0) + 21 * (level + 1)
            if ordered:
                p = self.para()
                p.paragraph_format.left_indent = Pt(indent)
                p.paragraph_format.first_line_indent = Pt(-18)
                self._run(p, "%d.\t" % number, {})
                p.paragraph_format.tab_stops.add_tab_stop(Pt(indent))
                number += 1
            else:
                style = "List Bullet" if level == 0 else "List Bullet %d" % min(level + 1, 3)
                p = self.para(style)
                p.paragraph_format.left_indent = Pt(indent)
            if ctx.get("quote"):
                self._border(p, "left", color="C8C8C8", size="18", space="8")
            first = True
            buf = []
            sub = dict(ctx)
            sub["list_level"] = level + 1
            sub["indent"] = ctx.get("indent", 0)
            cont = dict(ctx)
            cont["indent"] = indent
            cont.pop("style", None)

            def flush():
                if not buf:
                    return
                if all(isinstance(x, str) and not x.strip() for x in buf):
                    del buf[:]
                    return
                nonlocal_first = first
                target = p if nonlocal_first else self.para()
                if not nonlocal_first:
                    self.apply_ctx(target, cont)
                self.inline(target, buf, ctx.get("fmt", {}), strip_lead=True)
                del buf[:]
                return True

            for c in li.children:
                if is_inline(c):
                    buf.append(c)
                    continue
                if buf:
                    if flush():
                        first = False
                if c.tag == "p":
                    if first:
                        self.inline(p, c.children, ctx.get("fmt", {}), strip_lead=True)
                        first = False
                    else:
                        q = self.para()
                        self.apply_ctx(q, cont)
                        self.inline(q, c.children, ctx.get("fmt", {}), strip_lead=True)
                elif c.tag in ("ul", "ol"):
                    self.list_block(c, sub, ordered=(c.tag == "ol"))
                    first = False
                else:
                    self.block(c, cont)
                    first = False
            if buf:
                flush()

    def code_block(self, n, ctx):
        from docx.shared import Pt

        def text_of(x):
            if isinstance(x, str):
                return x
            return "".join(text_of(c) for c in x.children)

        code = text_of(n).rstrip("\n")
        p = self.para()
        pf = p.paragraph_format
        pf.left_indent = Pt(ctx.get("indent", 0) + 6)
        pf.space_before = Pt(4)
        pf.space_after = Pt(8)
        self._shade(p._p.get_or_add_pPr(), "F5F5F7")
        for side in ("top", "left", "bottom", "right"):
            self._border(p, side, color="E0E0E0", size="4", space="4")
        self.inline(p, [code], {"code": True, "pre": True})

    def table(self, n):
        from docx.shared import Pt

        rows = []

        def collect(x):
            for c in x.children:
                if isinstance(c, str):
                    continue
                if c.tag == "tr":
                    cells = [cc for cc in c.children if not isinstance(cc, str) and cc.tag in ("td", "th")]
                    rows.append(cells)
                else:
                    collect(c)

        collect(n)
        if not rows:
            return
        ncols = max(len(r) for r in rows)
        if ncols == 0:
            return
        tbl = self.doc.add_table(rows=len(rows), cols=ncols)
        try:
            tbl.style = self.doc.styles["Table Grid"]
        except KeyError:
            pass
        for ri, r in enumerate(rows):
            for ci in range(ncols):
                cell = tbl.cell(ri, ci)
                p = cell.paragraphs[0]
                if ci >= len(r):
                    continue
                c = r[ci]
                fmt = {"b": True} if c.tag == "th" else {}
                align = (c.attrs.get("align") or "")
                style = c.attrs.get("style") or ""
                m = re.search(r"text-align:\s*(\w+)", style)
                if m:
                    align = m.group(1)
                from docx.enum.text import WD_ALIGN_PARAGRAPH
                if align == "center":
                    p.alignment = WD_ALIGN_PARAGRAPH.CENTER
                elif align == "right":
                    p.alignment = WD_ALIGN_PARAGRAPH.RIGHT
                if c.tag == "th":
                    self._shade(cell._tc.get_or_add_tcPr(), "F2F2F2")
                self.inline(p, c.children, fmt, strip_lead=True)
        self.doc.add_paragraph()

    def save(self, path):
        self.doc.save(path)


def read_text_file(path):
    with open(path, "rb") as f:
        data = f.read()
    for enc in ("utf-8-sig", "gb18030"):
        try:
            return data.decode(enc)
        except UnicodeDecodeError:
            continue
    return data.decode("utf-8", errors="replace")


def _strike_extension():
    import markdown
    from markdown.inlinepatterns import SimpleTagInlineProcessor

    class StrikeExt(markdown.extensions.Extension):
        def extendMarkdown(self, md):
            md.inlinePatterns.register(SimpleTagInlineProcessor(r"(~~)(.+?)~~", "del"), "del", 175)

    return StrikeExt()


def md2docx(src, dst):
    import markdown

    emit("progress", pct=10, msg="正在读取 Markdown…")
    text = read_text_file(src)
    html = markdown.markdown(
        text,
        extensions=["extra", "sane_lists", "toc", _strike_extension()],
        output_format="html",
    )
    emit("progress", pct=40, msg="正在生成 Word…")
    root = parse_html(html)
    w = DocxWriter(os.path.dirname(os.path.abspath(src)))
    w.blocks(root, {})
    w.save(dst)


def main(argv=None):
    ap = argparse.ArgumentParser()
    ap.add_argument("--mode", required=True, choices=["pdf2docx", "md2docx", "selftest"])
    ap.add_argument("--input")
    ap.add_argument("--output")
    a = ap.parse_args(argv)
    try:
        if a.mode == "selftest":
            import docx  # noqa: F401
            import markdown  # noqa: F401
            import fitz  # noqa: F401
            import pdf2docx as _p  # noqa: F401
            emit("done", output="ok")
            return 0
        if not a.input or not os.path.isfile(a.input):
            raise KnownError("not_found", "找不到输入文件：%s" % a.input)
        out_dir = os.path.dirname(os.path.abspath(a.output))
        if not os.path.isdir(out_dir):
            os.makedirs(out_dir)
        if a.mode == "pdf2docx":
            pdf2docx(a.input, a.output)
        else:
            md2docx(a.input, a.output)
        if not os.path.isfile(a.output):
            raise KnownError("failed", "转换结束但没有生成输出文件。")
        emit("progress", pct=100, msg="完成")
        emit("done", output=os.path.abspath(a.output))
        return 0
    except KnownError as e:
        emit("error", code=e.code, message=e.message)
        return 2
    except PermissionError as e:
        emit("error", code="permission", message="没有写入权限或文件被占用（请关闭已打开的 Word 文档）：%s" % e)
        return 2
    except Exception as e:  # noqa
        tb = traceback.format_exc()
        sys.stderr.write(tb)
        emit("error", code="failed", message="转换失败：%s" % e)
        return 1


if __name__ == "__main__":
    sys.exit(main())
