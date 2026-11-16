"""生成测试用样例图片（文档 + 表格 + 水印）。需要 Pillow 与一款中文字体。"""
import sys, os
from PIL import Image, ImageDraw, ImageFont

FONT = sys.argv[1] if len(sys.argv) > 1 else "/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc"
OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "samples")


def font(sz):
    return ImageFont.truetype(FONT, sz)


def doc():
    im = Image.new("RGB", (1240, 1100), "white")
    d = ImageDraw.Draw(im)
    d.text((80, 60), "季度销售报告", font=font(48), fill="black")
    d.text((80, 150), "本季度公司整体销售额同比增长 23%，其中华东区表现最好。", font=font(28), fill="black")
    d.text((80, 200), "The quick brown fox jumps over the lazy dog.", font=font(28), fill="black")
    d.text((80, 250), "详情请访问 https://example.com/report 或发邮件 sales@example.com", font=font(28), fill="black")
    # 表格
    x0, y0, cw, rh = 80, 340, 270, 64
    rows = [["地区", "销售额（万元）", "增长率", "负责人"],
            ["华东", "1,250", "31%", "张伟"],
            ["华南", "980", "18%", "李娜"],
            ["华北", "860", "12%", "王强"],
            ["合计", "3,090", "23%", "—"]]
    for r, row in enumerate(rows):
        for c, txt in enumerate(row):
            x, y = x0 + c * cw, y0 + r * rh
            d.rectangle([x, y, x + cw, y + rh], outline="black", width=2)
            d.text((x + 20, y + 14), txt, font=font(28), fill="black")
    d.text((80, 720), "结论：下季度继续加大华东区投入，并在华北试点新渠道。", font=font(28), fill="black")
    d.text((80, 770), "Action items: review pricing by Friday.", font=font(28), fill="black")
    im.save(os.path.join(OUT, "doc_table.png"))


def shot():
    im = Image.new("RGB", (900, 260), (245, 246, 248))
    d = ImageDraw.Draw(im)
    d.text((30, 30), "Machine learning models can run fully offline on your laptop.", font=font(26), fill="black")
    d.text((30, 90), "离线运行的模型不会上传任何数据。", font=font(26), fill="black")
    d.text((30, 150), "Docs: https://github.com/RapidAI/RapidOCR", font=font(26), fill=(20, 80, 200))
    im.save(os.path.join(OUT, "screenshot.png"))


def watermark():
    # 渐变背景 + 半透明水印文字；mask 为水印区域
    im = Image.new("RGB", (640, 480))
    px = im.load()
    for y in range(480):
        for x in range(640):
            px[x, y] = (40 + x * 150 // 640, 90 + y * 100 // 480, 160)
    d = ImageDraw.Draw(im)
    for i in range(0, 640, 80):
        d.ellipse([i, 300, i + 60, 360], fill=(230, 200, 80))
    clean = im.copy()
    over = Image.new("RGBA", im.size, (0, 0, 0, 0))
    od = ImageDraw.Draw(over)
    od.text((150, 200), "SAMPLE 水印", font=font(64), fill=(255, 255, 255, 170))
    im = Image.alpha_composite(im.convert("RGBA"), over).convert("RGB")
    mask = Image.new("L", im.size, 0)
    md = ImageDraw.Draw(mask)
    bbox = od.textbbox((150, 200), "SAMPLE 水印", font=font(64))
    md.rectangle([bbox[0] - 8, bbox[1] - 8, bbox[2] + 8, bbox[3] + 8], fill=255)
    im.save(os.path.join(OUT, "watermark.png"))
    mask.save(os.path.join(OUT, "watermark_mask.png"))
    clean.save(os.path.join(OUT, "watermark_clean.png"))


if __name__ == "__main__":
    os.makedirs(OUT, exist_ok=True)
    doc(); shot(); watermark()
    print("ok", OUT)
