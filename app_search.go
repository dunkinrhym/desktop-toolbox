//go:build windows || darwin || linux

package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"desktoptoolbox/internal/extract"
	"desktoptoolbox/internal/search"
)

type searchState struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	id     int
}

// SearchRequest 前端搜索请求。
type SearchRequest struct {
	Keyword string `json:"keyword"`
	Folder  string `json:"folder"` // 空 = 所有磁盘
	Content bool   `json:"content"`
	Exts    string `json:"exts"` // 逗号/空格分隔
}

// SearchBatch 推送给前端的一批结果。
type SearchBatch struct {
	ID    int             `json:"id"`
	Items []search.Result `json:"items"`
	Stats search.Stats    `json:"stats"`
}

// SearchDone 搜索结束事件。
type SearchDone struct {
	ID        int          `json:"id"`
	Stats     search.Stats `json:"stats"`
	ElapsedMs int64        `json:"elapsedMs"`
}

// GetDrives 列出可搜索的磁盘。
func (a *App) GetDrives() []string { return searchRoots() }

// PickFolder 选择文件夹。
func (a *App) PickFolder() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "选择要搜索的文件夹"})
}

// StartSearch 开始搜索，结果通过 search:batch / search:done 事件推送。返回搜索编号。
func (a *App) StartSearch(req SearchRequest) (int, error) {
	kw := strings.TrimSpace(req.Keyword)
	if kw == "" {
		return 0, errors.New("请输入要搜索的关键词")
	}
	var roots []string
	if req.Folder != "" {
		st, err := os.Stat(req.Folder)
		if err != nil || !st.IsDir() {
			return 0, errors.New("所选文件夹不存在或无法访问：" + req.Folder)
		}
		roots = []string{req.Folder}
	} else {
		roots = searchRoots()
		if len(roots) == 0 {
			return 0, errors.New("没有找到可搜索的磁盘")
		}
	}
	exts := strings.FieldsFunc(req.Exts, func(r rune) bool { return r == ',' || r == ' ' || r == '，' || r == ';' })

	a.search.mu.Lock()
	if a.search.cancel != nil {
		a.search.cancel()
	}
	a.search.id++
	id := a.search.id
	ctx, cancel := context.WithCancel(context.Background())
	a.search.cancel = cancel
	a.search.mu.Unlock()

	go func() {
		start := time.Now()
		var mu sync.Mutex
		var pending []search.Result
		stop := make(chan struct{})
		flushDone := make(chan struct{})
		var stats search.Stats
		flush := func() {
			mu.Lock()
			items := pending
			pending = nil
			mu.Unlock()
			if len(items) > 0 {
				runtime.EventsEmit(a.ctx, "search:batch", SearchBatch{ID: id, Items: items})
			}
		}
		go func() {
			defer close(flushDone)
			t := time.NewTicker(150 * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					flush()
				case <-stop:
					flush()
					return
				}
			}
		}()
		opts := search.Options{Roots: roots, Keyword: kw, Content: req.Content, Exts: exts, MaxResults: 5000}
		collect := func(r search.Result) {
			mu.Lock()
			pending = append(pending, r)
			mu.Unlock()
		}
		// 平台快速通道（macOS：全盘按文件名搜索时使用 Spotlight 索引）；不可用时回退到实时遍历
		fast, ok := fastNameSearch(ctx, req.Folder == "", opts, collect)
		if ok {
			stats = fast
		} else {
			stats = search.Run(ctx, opts, collect)
		}
		close(stop)
		<-flushDone
		runtime.EventsEmit(a.ctx, "search:done", SearchDone{ID: id, Stats: stats, ElapsedMs: time.Since(start).Milliseconds()})
		a.search.mu.Lock()
		if a.search.id == id {
			a.search.cancel = nil
		}
		a.search.mu.Unlock()
		cancel()
	}()
	return id, nil
}

// CancelSearch 取消正在进行的搜索。
func (a *App) CancelSearch() {
	a.search.mu.Lock()
	defer a.search.mu.Unlock()
	if a.search.cancel != nil {
		a.search.cancel()
		a.search.cancel = nil
	}
}

// PreviewResult 预览内容。
type PreviewResult struct {
	Path      string `json:"path"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
	Error     string `json:"error"`
	Supported bool   `json:"supported"`
}

const maxPreviewBytes = 3 << 20

// Preview 提取文件文本用于预览（只读，不修改原文件）。
func (a *App) Preview(path string) PreviewResult {
	res := PreviewResult{Path: path, Supported: extract.Supported(path)}
	if !res.Supported {
		res.Error = "该类型文件不支持文本预览（支持 txt/md/csv/代码/日志/json/xml/html、docx、xlsx、pptx、pdf）"
		return res
	}
	txt, err := extract.Text(path, extract.DefaultMaxFileSize)
	if err != nil {
		switch {
		case errors.Is(err, extract.ErrTooLarge):
			res.Error = "文件超过 50MB，无法预览"
		case errors.Is(err, extract.ErrBinary):
			res.Error = "该文件看起来不是文本文件"
		default:
			res.Error = "无法读取文件内容：" + err.Error()
		}
		return res
	}
	if strings.TrimSpace(txt) == "" {
		res.Error = "没有提取到文字（可能是扫描件、图片型 PDF 或空文件）"
		return res
	}
	if len(txt) > maxPreviewBytes {
		cut := maxPreviewBytes
		for cut > 0 && !utf8.RuneStart(txt[cut]) {
			cut--
		}
		txt = txt[:cut]
		res.Truncated = true
	}
	res.Text = txt
	return res
}
