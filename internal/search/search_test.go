package search

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func collect(opt Options) ([]Result, Stats) {
	var mu sync.Mutex
	var out []Result
	st := Run(context.Background(), opt, func(r Result) { mu.Lock(); out = append(out, r); mu.Unlock() })
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, st
}

func TestNameAndContent(t *testing.T) {
	d := t.TempDir()
	write(t, filepath.Join(d, "a", "Report_2026.txt"), "nothing")
	write(t, filepath.Join(d, "a", "b", "notes.md"), "会议纪要：讨论 Budget 预算")
	write(t, filepath.Join(d, "node_modules", "report.txt"), "budget")
	write(t, filepath.Join(d, ".hidden", "report.txt"), "budget")
	write(t, filepath.Join(d, "x.bin"), "budget\x00")
	samples, _ := filepath.Abs("../../tests/samples")

	res, st := collect(Options{Roots: []string{d}, Keyword: "report"})
	if len(res) != 1 || res[0].Name != "Report_2026.txt" || res[0].Match != "name" {
		t.Fatalf("name search: %+v", res)
	}
	if st.Dirs < 3 {
		t.Errorf("dirs %d", st.Dirs)
	}
	res, _ = collect(Options{Roots: []string{d}, Keyword: "budget", Content: true})
	if len(res) != 1 || res[0].Name != "notes.md" || res[0].Match != "content" || res[0].Snippet == "" {
		t.Fatalf("content search: %+v", res)
	}
	// 多关键词
	res, _ = collect(Options{Roots: []string{d}, Keyword: "report 2026"})
	if len(res) != 1 {
		t.Fatalf("multi kw %+v", res)
	}
	// 内容 - office/pdf 样本
	for kw, want := range map[string]string{"星辰大海": "doc.docx", "隐藏的宝藏": "book.xlsx", "火星计划": "deck.pptx", "keyword alpha": "text.pdf", "关键字测试": "gbk.txt"} {
		res, _ = collect(Options{Roots: []string{samples}, Keyword: kw, Content: true})
		if len(res) != 1 || res[0].Name != want {
			t.Errorf("%s: %+v", kw, res)
		}
	}
	// 扩展名过滤
	res, _ = collect(Options{Roots: []string{samples}, Keyword: "pdf", Exts: []string{"PDF"}})
	if len(res) != 2 {
		t.Errorf("ext filter %+v", res)
	}
}

func TestMaxResultsAndCancel(t *testing.T) {
	d := t.TempDir()
	for i := 0; i < 50; i++ {
		write(t, filepath.Join(d, "sub", string(rune('a'+i%26))+"file"+string(rune('0'+i%10))+filepath.Base(t.Name())+".txt"), "x")
		write(t, filepath.Join(d, "f", "dup", "file_"+time.Now().Format("150405.000000000")+".txt"), "x")
	}
	res, st := collect(Options{Roots: []string{d}, Keyword: "file", MaxResults: 10})
	if len(res) != 10 || !st.Truncated {
		t.Fatalf("got %d truncated=%v", len(res), st.Truncated)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	st = Run(ctx, Options{Roots: []string{d}, Keyword: "file"}, func(Result) {})
	if !st.Canceled {
		t.Errorf("expected canceled")
	}
}

func TestSnippetFold(t *testing.T) {
	s := Snippet("前面的一些文字 Hello World 后面的文字", "hello", 4)
	if s != "…些文字 Hello Wor…" {
		t.Errorf("snippet %q", s)
	}
	if IndexFold("ÄBC straße", "STRASSE") != -1 && IndexFold("ÄBC Straße", "straße") < 0 {
		t.Errorf("fold")
	}
	if IndexFold("ÄBC Straße", "äbc") != 0 {
		t.Errorf("fold unicode")
	}
}
