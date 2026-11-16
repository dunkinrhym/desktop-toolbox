// Package extract 从各种文档中提取纯文本，用于内容搜索和预览。
// 纯 Go 实现，不修改原文件。
package extract

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// DefaultMaxFileSize 内容搜索时的单文件大小上限。
const DefaultMaxFileSize = 50 << 20

// ErrUnsupported 表示该扩展名不支持内容提取。
var ErrUnsupported = errors.New("不支持的文件类型")

// ErrTooLarge 表示文件超过大小上限。
var ErrTooLarge = errors.New("文件过大")

// ErrBinary 表示文件看起来是二进制文件。
var ErrBinary = errors.New("二进制文件")

var plainExts = map[string]bool{}

func init() {
	for _, e := range strings.Fields(`.txt .md .markdown .csv .tsv .log .json .xml .html .htm .yaml .yml .ini .cfg .conf .toml
		.go .py .js .ts .jsx .tsx .vue .java .kt .c .h .cpp .hpp .cc .cs .rs .rb .php .swift .m .sh .bat .cmd .ps1
		.sql .css .scss .less .lua .r .tex .rst .properties .gradle .srt .vtt .env .gitignore .dart .scala .pl .vb`) {
		plainExts[e] = true
	}
}

// Supported 返回该文件是否支持内容提取。
func Supported(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if plainExts[ext] {
		return true
	}
	switch ext {
	case ".docx", ".xlsx", ".pptx", ".pdf":
		return true
	}
	return false
}

// IsPlain 返回是否为纯文本类文件。
func IsPlain(path string) bool { return plainExts[strings.ToLower(filepath.Ext(path))] }

// Text 提取文件的文本内容。maxSize<=0 时使用 DefaultMaxFileSize。
func Text(path string, maxSize int64) (text string, err error) {
	if maxSize <= 0 {
		maxSize = DefaultMaxFileSize
	}
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", ErrUnsupported
	}
	if st.Size() > maxSize {
		return "", ErrTooLarge
	}
	defer func() {
		// 第三方解析器遇到异常文件可能 panic，统一转成错误
		if r := recover(); r != nil {
			text = ""
			err = fmt.Errorf("解析失败: %v", r)
		}
	}()
	ext := strings.ToLower(filepath.Ext(path))
	switch {
	case plainExts[ext]:
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return DecodeText(b)
	case ext == ".docx":
		return docxText(path)
	case ext == ".xlsx":
		return xlsxText(path)
	case ext == ".pptx":
		return pptxText(path)
	case ext == ".pdf":
		return pdfText(path)
	}
	return "", ErrUnsupported
}

// DecodeText 把字节解码为字符串，支持 UTF-8(含BOM)、UTF-16 LE/BE(含BOM)、GB18030/GBK。
func DecodeText(b []byte) (string, error) {
	switch {
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		return string(b[3:]), nil
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}):
		return decodeUTF16(b[2:], false), nil
	case bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		return decodeUTF16(b[2:], true), nil
	}
	head := b
	if len(head) > 8192 {
		head = head[:8192]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return "", ErrBinary
	}
	if utf8.Valid(b) {
		return string(b), nil
	}
	// 截断处可能切断多字节字符，容忍末尾 3 字节
	if len(b) > 4 && utf8.Valid(b[:len(b)-3]) && !utf8.Valid(b[len(b)-3:]) {
		return strings.ToValidUTF8(string(b), ""), nil
	}
	out, err := simplifiedchinese.GB18030.NewDecoder().Bytes(b)
	if err == nil {
		return string(out), nil
	}
	return strings.ToValidUTF8(string(b), "\uFFFD"), nil
}

func decodeUTF16(b []byte, bigEndian bool) string {
	n := len(b) / 2
	u := make([]uint16, n)
	for i := 0; i < n; i++ {
		if bigEndian {
			u[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
		} else {
			u[i] = uint16(b[2*i+1])<<8 | uint16(b[2*i])
		}
	}
	return string(utf16.Decode(u))
}

// ---------------------------------------------------------------- OOXML

const maxZipEntry = 200 << 20

func readZipEntry(f *zip.File) ([]byte, error) {
	if f.UncompressedSize64 > maxZipEntry {
		return nil, ErrTooLarge
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, maxZipEntry))
}

// ooxmlText 解析 OOXML，提取所有 local name == textTag 的元素文本，在 paraTag 结束时换行。
func ooxmlText(data []byte, textTag, paraTag string, tabTags ...string) string {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	var sb strings.Builder
	inText := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := t.Name.Local
			if n == textTag {
				inText = true
			} else if n == "tab" {
				sb.WriteByte('\t')
			} else if n == "br" || n == "cr" {
				sb.WriteByte('\n')
			}
			for _, tt := range tabTags {
				if n == tt {
					sb.WriteByte('\t')
				}
			}
		case xml.EndElement:
			if t.Name.Local == textTag {
				inText = false
			} else if t.Name.Local == paraTag {
				sb.WriteByte('\n')
			}
		case xml.CharData:
			if inText {
				sb.Write(t)
			}
		}
	}
	return sb.String()
}

func docxText(path string) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("无法打开 docx: %w", err)
	}
	defer zr.Close()
	var parts []string
	var names []string
	for _, f := range zr.File {
		n := f.Name
		if n == "word/document.xml" || strings.HasPrefix(n, "word/header") || strings.HasPrefix(n, "word/footer") ||
			n == "word/footnotes.xml" || n == "word/endnotes.xml" {
			names = append(names, n)
		}
	}
	// document.xml 放最前
	sort.SliceStable(names, func(i, j int) bool {
		return names[i] == "word/document.xml" && names[j] != "word/document.xml"
	})
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	if _, ok := files["word/document.xml"]; !ok {
		return "", errors.New("docx 中没有 word/document.xml")
	}
	for _, n := range names {
		b, err := readZipEntry(files[n])
		if err != nil {
			continue
		}
		s := ooxmlText(b, "t", "p")
		if strings.TrimSpace(s) != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n"), nil
}

var numRe = regexp.MustCompile(`(\d+)\.xml$`)

func sortByNumber(names []string) {
	sort.Slice(names, func(i, j int) bool {
		a, b := numRe.FindStringSubmatch(names[i]), numRe.FindStringSubmatch(names[j])
		if a != nil && b != nil {
			x, _ := strconv.Atoi(a[1])
			y, _ := strconv.Atoi(b[1])
			if x != y {
				return x < y
			}
		}
		return names[i] < names[j]
	})
}

func pptxText(path string) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("无法打开 pptx: %w", err)
	}
	defer zr.Close()
	var slides []string
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
		if strings.HasPrefix(f.Name, "ppt/slides/slide") && strings.HasSuffix(f.Name, ".xml") {
			slides = append(slides, f.Name)
		}
	}
	sortByNumber(slides)
	var sb strings.Builder
	for i, n := range slides {
		b, err := readZipEntry(files[n])
		if err != nil {
			continue
		}
		fmt.Fprintf(&sb, "—— 第 %d 页 ——\n", i+1)
		sb.WriteString(ooxmlText(b, "t", "p"))
		// 备注
		notes := strings.Replace(n, "slides/slide", "notesSlides/notesSlide", 1)
		if nf, ok := files[notes]; ok {
			if nb, err := readZipEntry(nf); err == nil {
				if s := strings.TrimSpace(ooxmlText(nb, "t", "p")); s != "" {
					sb.WriteString("[备注] ")
					sb.WriteString(s)
					sb.WriteByte('\n')
				}
			}
		}
	}
	return sb.String(), nil
}

type xlsxSST struct {
	SI []struct {
		T string `xml:"t"`
		R []struct {
			T string `xml:"t"`
		} `xml:"r"`
	} `xml:"si"`
}

func xlsxText(path string) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("无法打开 xlsx: %w", err)
	}
	defer zr.Close()
	files := map[string]*zip.File{}
	var sheets []string
	for _, f := range zr.File {
		files[f.Name] = f
		if strings.HasPrefix(f.Name, "xl/worksheets/sheet") && strings.HasSuffix(f.Name, ".xml") {
			sheets = append(sheets, f.Name)
		}
	}
	sortByNumber(sheets)
	var shared []string
	if f, ok := files["xl/sharedStrings.xml"]; ok {
		if b, err := readZipEntry(f); err == nil {
			var sst xlsxSST
			dec := xml.NewDecoder(bytes.NewReader(b))
			dec.Strict = false
			if dec.Decode(&sst) == nil {
				for _, si := range sst.SI {
					s := si.T
					for _, r := range si.R {
						s += r.T
					}
					shared = append(shared, s)
				}
			}
		}
	}
	var sb strings.Builder
	for i, n := range sheets {
		b, err := readZipEntry(files[n])
		if err != nil {
			continue
		}
		fmt.Fprintf(&sb, "—— 工作表 %d ——\n", i+1)
		sheetText(&sb, b, shared)
	}
	return sb.String(), nil
}

func sheetText(sb *strings.Builder, data []byte, shared []string) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	var cellType string
	var inV, inT bool
	var cur strings.Builder
	firstInRow := true
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				firstInRow = true
			case "c":
				cellType = ""
				for _, a := range t.Attr {
					if a.Name.Local == "t" {
						cellType = a.Value
					}
				}
				cur.Reset()
			case "v":
				inV = true
			case "t":
				inT = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
			case "t":
				inT = false
			case "c":
				val := cur.String()
				if cellType == "s" {
					if idx, err := strconv.Atoi(strings.TrimSpace(val)); err == nil && idx >= 0 && idx < len(shared) {
						val = shared[idx]
					}
				}
				if val != "" {
					if !firstInRow {
						sb.WriteByte('\t')
					}
					sb.WriteString(val)
					firstInRow = false
				}
			case "row":
				if !firstInRow {
					sb.WriteByte('\n')
				}
			}
		case xml.CharData:
			if inV || inT {
				cur.Write(t)
			}
		}
	}
}

// ---------------------------------------------------------------- PDF

func pdfText(path string) (string, error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return "", fmt.Errorf("无法解析 PDF: %w", err)
	}
	defer f.Close()
	var sb strings.Builder
	n := r.NumPage()
	for i := 1; i <= n; i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		s, err := pageText(p)
		if err != nil {
			continue
		}
		if n > 1 {
			fmt.Fprintf(&sb, "—— 第 %d 页 ——\n", i)
		}
		sb.WriteString(s)
		sb.WriteByte('\n')
	}
	out := sb.String()
	return out, nil
}

func pageText(p pdf.Page) (s string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	rows, err := p.GetTextByRow()
	if err != nil || len(rows) == 0 {
		return p.GetPlainText(nil)
	}
	var sb strings.Builder
	for _, row := range rows {
		for _, w := range row.Content {
			sb.WriteString(w.S)
		}
		sb.WriteByte('\n')
	}
	return sb.String(), nil
}
