package search

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSpotlightQuery(t *testing.T) {
	q := SpotlightQuery([]string{"报告", `a"b*`})
	want := `kMDItemFSName == "*报告*"cd && kMDItemFSName == "*a\"b\**"cd`
	if q != want {
		t.Fatalf("got  %s\nwant %s", q, want)
	}
}

func TestExcludedMacPath(t *testing.T) {
	home := "/Users/me"
	cases := map[string]bool{
		"/Users/me/Documents/报告.docx":                     false,
		"/Users/me/Library/Caches/x/报告.txt":               true,
		"/Users/me/.Trash/报告.docx":                        true,
		"/Users/me/proj/node_modules/a/报告.js":             true,
		"/Applications/Foo.app":                           false,
		"/Applications/Foo.app/Contents/Resources/报告.txt": true,
		"/System/Library/报告":                              true,
		"/Volumes/U盘/资料/报告.pdf":                           false,
		"/Users/me/code/.git/报告":                          true,
		"/private/var/folders/x/报告":                       true,
	}
	for p, want := range cases {
		if got := ExcludedMacPath(p, home); got != want {
			t.Errorf("%s: got %v want %v", p, got, want)
		}
	}
}

// 用一个假的 mdfind 脚本验证结果解析与过滤。
func TestRunMdfindFake(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	dir := t.TempDir()
	keep := filepath.Join(dir, "年度 报告.docx")
	other := filepath.Join(dir, "年度总结.pdf")
	hidden := filepath.Join(dir, ".git", "年度 报告.txt")
	for _, f := range []string{keep, other, hidden} {
		os.MkdirAll(filepath.Dir(f), 0o755)
		os.WriteFile(f, []byte("x"), 0o644)
	}
	script := filepath.Join(dir, "mdfind")
	body := "#!/bin/sh\nprintf '%s\\0%s\\0%s\\0%s\\0' '" + keep + "' '" + other + "' '" + hidden + "' '" + keep + "'\n"
	os.WriteFile(script, []byte(body), 0o755)
	var got []Result
	st, err := RunMdfind(context.Background(), script, "", Options{Keyword: "年度 报告"}, func(r Result) { got = append(got, r) })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != keep || st.Results != 1 {
		t.Fatalf("unexpected %+v %+v", got, st)
	}
	// 扩展名过滤
	got = nil
	RunMdfind(context.Background(), script, "", Options{Keyword: "年度", Exts: []string{"pdf"}}, func(r Result) { got = append(got, r) })
	if len(got) != 1 || !strings.HasSuffix(got[0].Path, ".pdf") {
		t.Fatalf("ext filter %+v", got)
	}
	// 命令失败 -> ErrNoIndex
	bad := filepath.Join(dir, "bad")
	os.WriteFile(bad, []byte("#!/bin/sh\nexit 3\n"), 0o755)
	if _, err := RunMdfind(context.Background(), bad, "", Options{Keyword: "x"}, func(Result) {}); err != ErrNoIndex {
		t.Fatalf("want ErrNoIndex, got %v", err)
	}
	if _, err := RunMdfind(context.Background(), filepath.Join(dir, "missing"), "", Options{Keyword: "x"}, func(Result) {}); err != ErrNoIndex {
		t.Fatalf("want ErrNoIndex for missing binary, got %v", err)
	}
}
