"""会议记录导出：Markdown / Word（逐字稿 + 纪要）。"""
import os
import re
import time

from .common import unique_path
from .speech import fmt_ts


def _name(names, spk):
    return (names or {}).get(str(spk)) or "说话人%d" % spk


def to_markdown(title, source, duration, segments, minutes_md="", names=None):
    out = ["# %s" % title, ""]
    meta = "- 来源：%s\n- 时长：%s\n- 生成时间：%s" % (os.path.basename(source or "") or "—", fmt_ts(duration or 0),
                                                time.strftime("%Y-%m-%d %H:%M"))
    out += [meta, ""]
    if minutes_md.strip():
        out += ["# 会议纪要", "", _shift_headings(minutes_md.strip()), ""]
    out += ["# 逐字稿", ""]
    for s in segments:
        out.append("**%s** %s  " % (_name(names, s["speaker"]), fmt_ts(s["start"])))
        out.append(s["text"])
        out.append("")
    return "\n".join(out)


def _shift_headings(md):
    return re.sub(r"^(#{1,5}) ", lambda m: "#" + m.group(1) + " ", md, flags=re.M)


def export(fmt, out, title, source, duration, segments, minutes_md="", names=None):
    out = unique_path(out)
    if fmt == "md":
        with open(out, "w", encoding="utf-8") as f:
            f.write(to_markdown(title, source, duration, segments, minutes_md, names))
        return {"output": out}
    from docx import Document
    from docx.shared import Pt, RGBColor
    from docx.oxml.ns import qn
    doc = Document()
    st = doc.styles["Normal"]
    st.font.name = "Calibri"
    st.font.size = Pt(10.5)
    rpr = st.element.get_or_add_rPr()
    fonts = rpr.find(qn("w:rFonts"))
    if fonts is None:
        fonts = rpr.makeelement(qn("w:rFonts"), {})
        rpr.append(fonts)
    fonts.set(qn("w:eastAsia"), "微软雅黑")
    for hs in ("Title", "Heading 1", "Heading 2", "Heading 3"):
        try:
            h = doc.styles[hs]
            hr = h.element.get_or_add_rPr()
            hf = hr.find(qn("w:rFonts"))
            if hf is None:
                hf = hr.makeelement(qn("w:rFonts"), {})
                hr.append(hf)
            hf.set(qn("w:eastAsia"), "微软雅黑")
        except KeyError:
            pass
    doc.add_heading(title, 0)
    p = doc.add_paragraph()
    p.add_run("来源：%s    时长：%s    生成时间：%s" % (os.path.basename(source or "") or "—", fmt_ts(duration or 0),
                                               time.strftime("%Y-%m-%d %H:%M"))).font.color.rgb = RGBColor(0x66, 0x66, 0x66)
    if minutes_md.strip():
        doc.add_heading("会议纪要", 1)
        _md_to_docx(doc, minutes_md)
    doc.add_heading("逐字稿", 1)
    for s in segments:
        p = doc.add_paragraph()
        r = p.add_run(_name(names, s["speaker"]))
        r.bold = True
        r2 = p.add_run("  " + fmt_ts(s["start"]))
        r2.font.color.rgb = RGBColor(0x88, 0x88, 0x88)
        r2.font.size = Pt(9)
        p.paragraph_format.space_after = Pt(0)
        q = doc.add_paragraph(s["text"])
        q.paragraph_format.space_after = Pt(8)
    doc.save(out)
    return {"output": out}


def _md_to_docx(doc, md):
    for ln in md.splitlines():
        s = ln.rstrip()
        if not s.strip():
            continue
        m = re.match(r"^(#{1,6})\s+(.*)", s)
        if m:
            doc.add_heading(m.group(2).strip(), min(3, len(m.group(1)) + 1 if len(m.group(1)) < 2 else 2))
            continue
        m = re.match(r"^\s*[-*]\s+\[( |x|X)\]\s+(.*)", s)
        if m:
            doc.add_paragraph(("☑ " if m.group(1).lower() == "x" else "☐ ") + _strip_md(m.group(2)), style="List Bullet")
            continue
        m = re.match(r"^\s*(?:[-*]|\d+[.、])\s+(.*)", s)
        if m:
            doc.add_paragraph(_strip_md(m.group(1)), style="List Bullet")
            continue
        doc.add_paragraph(_strip_md(s))


def _strip_md(s):
    return re.sub(r"\*\*(.+?)\*\*", r"\1", s).replace("`", "")
