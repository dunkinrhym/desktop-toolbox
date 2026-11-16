package aiengine

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestManifestValid(t *testing.T) {
	m, err := LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range m.Components {
		if len(c.FilesFor("windows-amd64")) == 0 {
			t.Errorf("%s：缺少 windows-amd64 文件", c.ID)
		}
		for _, fs := range c.Files {
			for _, f := range fs {
				if len(f.SHA256) != 64 || f.Size <= 0 || len(f.URLs) == 0 {
					t.Errorf("%s/%s：清单不完整", c.ID, f.Name)
				}
			}
		}
	}
	for _, id := range append(m.FeatureComponents("doc", ""), LLMIDs...) {
		if _, err := m.Component(id); err != nil {
			t.Error(err)
		}
	}
	if RecommendLLM(16<<30) != "llm-8b" || RecommendLLM(8<<30) != "llm-4b" || RecommendLLM(4<<30) != "llm-1.7b" {
		t.Error("RecommendLLM")
	}
}

func makeZip(t *testing.T) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{"pkg/__init__.py": "x = 1\n", "pkg-1.0.dist-info/METADATA": "m",
		"pkg-1.0.data/scripts/tool": "skip"} {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// 第一个下载源返回错误，第二个源中途断开，第三次（断点续传）完成；校验 SHA256 并解压。
func TestInstallResumeAndMirrors(t *testing.T) {
	data := makeZip(t)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer bad.Close()
	var hits int32
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if r.Method == "HEAD" {
			return
		}
		start := 0
		if rg := r.Header.Get("Range"); rg != "" {
			start, _ = strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rg, "bytes="), "-"))
			w.Header().Set("Content-Range", "bytes "+strconv.Itoa(start)+"-"+strconv.Itoa(len(data)-1)+"/"+strconv.Itoa(len(data)))
			w.WriteHeader(http.StatusPartialContent)
		}
		body := data[start:]
		if n <= 2 && start == 0 { // 第一次 GET：只发一半就断开
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.Write(body[:len(body)/2])
			if hj, ok := w.(http.Hijacker); ok {
				c, _, _ := hj.Hijack()
				c.Close()
			}
			return
		}
		w.Write(body)
	}))
	defer good.Close()
	root := t.TempDir()
	m := NewManager(root)
	c := &Component{ID: "py-test", Kind: "site", Name: "测试", Version: "1", Files: map[string][]File{
		"all": {{Name: "pkg-1.0-py3-none-any.whl", Size: int64(len(data)), SHA256: sum(data),
			URLs: []string{bad.URL + "/a.whl", good.URL + "/a.whl"}, Extract: "zip"}}}}
	var last Progress
	if err := m.Install(context.Background(), c, func(p Progress) { last = p }); err != nil {
		t.Fatal(err)
	}
	if last.Stage != "done" {
		t.Errorf("最后进度 %+v", last)
	}
	if b, err := os.ReadFile(filepath.Join(root, "site", "py-test", "pkg", "__init__.py")); err != nil || string(b) != "x = 1\n" {
		t.Fatalf("解压结果不对：%v %q", err, b)
	}
	if _, err := os.Stat(filepath.Join(root, "site", "py-test", "pkg-1.0.data")); err == nil {
		t.Error(".data 目录不应解压")
	}
	if !m.IsInstalled(c) || m.DiskSize(c) == 0 {
		t.Error("未记录为已安装")
	}
	if err := m.Remove(c); err != nil || m.IsInstalled(c) {
		t.Errorf("删除失败：%v", err)
	}
}

func TestInstallChecksumMismatch(t *testing.T) {
	data := []byte("model-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("tampered!!!")) }))
	defer srv.Close()
	m := NewManager(t.TempDir())
	c := &Component{ID: "m-x", Kind: "models", Files: map[string][]File{"all": {{Name: "a.onnx", Size: int64(len(data)),
		SHA256: sum(data), URLs: []string{srv.URL + "/a.onnx"}, Extract: "file"}}}}
	err := m.Install(context.Background(), c, nil)
	if err == nil || !strings.Contains(err.Error(), "SHA256") {
		t.Fatalf("应报告校验失败，得到 %v", err)
	}
	if m.IsInstalled(c) {
		t.Error("校验失败不应安装")
	}
}
