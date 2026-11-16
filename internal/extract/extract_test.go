package extract

import (
	"path/filepath"
	"strings"
	"testing"
)

func sample(n string) string { return filepath.Join("..", "..", "tests", "samples", n) }

func TestExtract(t *testing.T) {
	cases := []struct {
		file string
		want []string
	}{
		{"doc.docx", []string{"报告标题", "星辰大海", "English Keyword", "表格单元", "页眉内容"}},
		{"book.xlsx", []string{"姓名", "张三", "1234.5", "隐藏的宝藏"}},
		{"deck.pptx", []string{"幻灯片标题", "火星计划", "备注里的秘密", "第11张"}},
		{"text.pdf", []string{"searchable keyword alpha", "page 3"}},
		{"gbk.txt", []string{"中文GBK文本", "第二行"}},
		{"utf16.txt", []string{"UTF16 文本 hello"}},
		{"sample.md", []string{"桌面工具箱 测试文档"}},
		{"gbk.md", []string{"中文内容测试"}},
	}
	for _, c := range cases {
		txt, err := Text(sample(c.file), 0)
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		for _, w := range c.want {
			if !strings.Contains(txt, w) {
				t.Errorf("%s: missing %q in %q", c.file, w, txt)
			}
		}
	}
	// pptx 顺序：第2张应在第11张之前
	txt, _ := Text(sample("deck.pptx"), 0)
	if strings.Index(txt, "第2张") > strings.Index(txt, "第11张") {
		t.Errorf("slides out of order")
	}
}

func TestBinaryAndLimits(t *testing.T) {
	if _, err := Text(sample("pic.png"), 0); err != ErrUnsupported {
		t.Errorf("png should be unsupported, got %v", err)
	}
	if _, err := Text(sample("doc.docx"), 10); err != ErrTooLarge {
		t.Errorf("want too large, got %v", err)
	}
	if _, err := DecodeText([]byte("abc\x00def")); err != ErrBinary {
		t.Errorf("want binary")
	}
	if !Supported("A.DOCX") || Supported("x.exe") {
		t.Errorf("Supported wrong")
	}
}

func TestScannedPDF(t *testing.T) {
	txt, err := Text(sample("scanned.pdf"), 0)
	if err != nil {
		t.Logf("scanned pdf error (acceptable): %v", err)
	}
	if strings.TrimSpace(txt) != "" {
		t.Errorf("scanned pdf should have no text: %q", txt)
	}
}
