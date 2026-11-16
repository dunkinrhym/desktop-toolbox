package converter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func conv(t *testing.T) *Converter {
	py := "/workspace/tools/pyenv/bin/python"
	if env := os.Getenv("DT_TEST_PYTHON"); env != "" { // CI：测试打包进 .app 的 Python
		py = env
	}
	if _, err := os.Stat(py); err != nil {
		t.Skip("python venv not available")
	}
	s, _ := filepath.Abs("../../resources/scripts/convert.py")
	return &Converter{Python: py, Script: s}
}

func TestConvert(t *testing.T) {
	c := conv(t)
	dir := t.TempDir()
	for _, f := range []string{"sample.md", "text.pdf"} {
		src, _ := filepath.Abs(filepath.Join("../../tests/samples", f))
		dst := OutputPath(src, dir)
		var last int
		out, err := c.Convert(context.Background(), src, dst, func(p int, m string) { last = p })
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if st, err := os.Stat(out); err != nil || st.Size() < 1000 || last != 100 {
			t.Fatalf("%s: bad output %v %d", f, err, last)
		}
		if OutputPath(src, dir) == dst {
			t.Errorf("OutputPath should avoid existing file")
		}
	}
	src, _ := filepath.Abs("../../tests/samples/scanned.pdf")
	_, err := c.Convert(context.Background(), src, filepath.Join(dir, "s.docx"), nil)
	var ce *ConvertError
	if !errors.As(err, &ce) || ce.Code != "scanned" {
		t.Fatalf("want scanned error, got %v", err)
	}
	if err := c.SelfTest(context.Background()); err != nil {
		t.Fatalf("selftest: %v", err)
	}
	if _, err := ModeFor("a.txt"); err == nil {
		t.Errorf("txt unsupported")
	}
}
