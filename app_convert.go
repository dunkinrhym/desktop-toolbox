//go:build windows || darwin || linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"desktoptoolbox/internal/aiengine"
	"desktoptoolbox/internal/converter"
)

type convertState struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	busy   bool
}

// ConvertEvent 单个文件的转换状态。
type ConvertEvent struct {
	File   string `json:"file"`
	Status string `json:"status"` // running | done | error | canceled
	Pct    int    `json:"pct"`
	Msg    string `json:"msg"`
	Output string `json:"output"`
	Error  string `json:"error"`
	Code   string `json:"code"`
}

// PickConvertFiles 选择要转换的文件（PDF / Markdown）。
func (a *App) PickConvertFiles() ([]string, error) {
	return runtime.OpenMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择 PDF 或 Markdown 文件",
		Filters: []runtime.FileFilter{
			{DisplayName: "PDF / Markdown (*.pdf;*.md)", Pattern: "*.pdf;*.md;*.markdown"},
			{DisplayName: "PDF (*.pdf)", Pattern: "*.pdf"},
			{DisplayName: "Markdown (*.md)", Pattern: "*.md;*.markdown"},
		},
	})
}

// PickOutputDir 选择输出文件夹。
func (a *App) PickOutputDir() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "选择输出文件夹"})
}

// ConvertCheck 检查文件是否可转换，返回错误说明（空表示可以）。
func (a *App) ConvertCheck(path string) string {
	if _, err := converter.ModeFor(path); err != nil {
		return err.Error()
	}
	if st, err := os.Stat(path); err != nil || st.IsDir() {
		return "文件不存在"
	}
	return ""
}

// StartConvert 批量转换，逐个推送 convert:file 事件，最后推送 convert:done。
func (a *App) StartConvert(files []string, outDir string) error {
	if len(files) == 0 {
		return errors.New("请先添加要转换的文件")
	}
	if err := a.conv.Check(); err != nil {
		return err
	}
	if outDir != "" {
		if st, err := os.Stat(outDir); err != nil || !st.IsDir() {
			return errors.New("输出文件夹不存在：" + outDir)
		}
	}
	a.convs.mu.Lock()
	if a.convs.busy {
		a.convs.mu.Unlock()
		return errors.New("正在转换中，请等待完成或先取消")
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.convs.cancel = cancel
	a.convs.busy = true
	a.convs.mu.Unlock()

	go func() {
		defer func() {
			a.convs.mu.Lock()
			a.convs.busy = false
			a.convs.cancel = nil
			a.convs.mu.Unlock()
			cancel()
			runtime.EventsEmit(a.ctx, "convert:done")
		}()
		for _, f := range files {
			if ctx.Err() != nil {
				runtime.EventsEmit(a.ctx, "convert:file", ConvertEvent{File: f, Status: "canceled", Error: "已取消"})
				continue
			}
			emit := func(ev ConvertEvent) { ev.File = f; runtime.EventsEmit(a.ctx, "convert:file", ev) }
			if msg := a.ConvertCheck(f); msg != "" {
				emit(ConvertEvent{Status: "error", Error: msg})
				continue
			}
			dir := outDir
			if dir == "" {
				dir = filepath.Dir(f)
			}
			dst := converter.OutputPath(f, dir)
			emit(ConvertEvent{Status: "running", Pct: 1, Msg: "正在启动转换组件…"})
			out, err := a.conv.Convert(ctx, f, dst, func(p int, m string) {
				emit(ConvertEvent{Status: "running", Pct: p, Msg: m})
			})
			var sce *converter.ConvertError
			if err != nil && errors.As(err, &sce) && sce.Code == "scanned" && len(a.AIMissing("doc")) == 0 && ctx.Err() == nil {
				// 扫描件：改用离线 OCR 识别后生成 Word
				emit(ConvertEvent{Status: "running", Pct: 2, Msg: "扫描版 PDF，正在用 OCR 识别…"})
				var res json.RawMessage
				res, err = a.ai.vision.Call(ctx, "pdf2docx_ocr", map[string]any{"path": f, "output": dst}, func(ev aiengine.Event) {
					if ev.Event == "progress" {
						emit(ConvertEvent{Status: "running", Pct: ev.Pct, Msg: ev.Msg})
					}
				})
				if err == nil {
					var r struct{ Output string }
					_ = json.Unmarshal(res, &r)
					out = r.Output
				}
			}
			if err != nil {
				ev := ConvertEvent{Status: "error", Error: err.Error()}
				var ce *converter.ConvertError
				if errors.As(err, &ce) {
					ev.Code = ce.Code
				}
				if ctx.Err() != nil {
					ev.Status = "canceled"
					ev.Error = "已取消"
					os.Remove(dst)
				}
				emit(ev)
				continue
			}
			emit(ConvertEvent{Status: "done", Pct: 100, Output: out})
		}
	}()
	return nil
}

// CancelConvert 取消批量转换。
func (a *App) CancelConvert() {
	a.convs.mu.Lock()
	defer a.convs.mu.Unlock()
	if a.convs.cancel != nil {
		a.convs.cancel()
	}
}

// TestConverter 自检转换组件（设置页“检测”按钮）。
func (a *App) TestConverter() error {
	return a.conv.SelfTest(context.Background())
}
