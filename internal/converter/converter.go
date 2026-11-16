// Package converter 调用随附的 Python 脚本完成 PDF→Word、Markdown→Word 转换。
package converter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"desktoptoolbox/internal/proc"
)

// Event 脚本输出的事件。
type Event struct {
	Event   string `json:"event"`
	Pct     int    `json:"pct"`
	Msg     string `json:"msg"`
	Output  string `json:"output"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Converter 持有 python 与脚本路径。
type Converter struct {
	Python string
	Script string
}

// ModeFor 根据扩展名返回转换模式。
func ModeFor(path string) (string, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf":
		return "pdf2docx", nil
	case ".md", ".markdown", ".mdown", ".mkd":
		return "md2docx", nil
	}
	return "", errors.New("仅支持 PDF 和 Markdown (.md) 文件")
}

// OutputPath 计算输出路径：outDir 为空时放在源文件旁；已存在则追加 (1)(2)...
func OutputPath(src, outDir string) string {
	dir := outDir
	if dir == "" {
		dir = filepath.Dir(src)
	}
	base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	p := filepath.Join(dir, base+".docx")
	for i := 1; ; i++ {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return p
		}
		p = filepath.Join(dir, fmt.Sprintf("%s (%d).docx", base, i))
	}
}

// Check 检查组件是否存在。
func (c *Converter) Check() error {
	if _, err := os.Stat(c.Python); err != nil {
		return fmt.Errorf("找不到转换组件（Python 运行时）：%s，请重新安装本软件", c.Python)
	}
	if _, err := os.Stat(c.Script); err != nil {
		return fmt.Errorf("找不到转换脚本：%s，请重新安装本软件", c.Script)
	}
	return nil
}

// Convert 转换单个文件，progress 回调进度。返回输出文件路径。
func (c *Converter) Convert(ctx context.Context, src, dst string, progress func(pct int, msg string)) (string, error) {
	if err := c.Check(); err != nil {
		return "", err
	}
	mode, err := ModeFor(src)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Python, "-B", "-s", "-X", "utf8", c.Script, "--mode", mode, "--input", src, "--output", dst)
	proc.Hide(cmd)
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1", "PYTHONDONTWRITEBYTECODE=1")
	cmd.Dir = filepath.Dir(c.Script)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("无法启动转换组件：%w", err)
	}
	var last Event
	var result string
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var ev Event
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		switch ev.Event {
		case "progress":
			if progress != nil {
				progress(ev.Pct, ev.Msg)
			}
		case "done":
			result = ev.Output
		case "error":
			last = ev
		}
	}
	werr := cmd.Wait()
	if ctx.Err() == context.Canceled {
		return "", errors.New("已取消")
	}
	if ctx.Err() == context.DeadlineExceeded {
		return "", errors.New("转换超时（超过 60 分钟）")
	}
	if last.Event == "error" {
		return "", &ConvertError{Code: last.Code, Message: last.Message}
	}
	if werr != nil || result == "" {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 800 {
			msg = "…" + msg[len(msg)-800:]
		}
		if msg == "" && werr != nil {
			msg = werr.Error()
		}
		return "", fmt.Errorf("转换失败：%s", msg)
	}
	return result, nil
}

// ConvertError 脚本报告的已知错误。
type ConvertError struct {
	Code    string
	Message string
}

func (e *ConvertError) Error() string { return e.Message }

// SelfTest 运行脚本自检（导入全部依赖），用于诊断内置 Python 组件是否可用。
func (c *Converter) SelfTest(ctx context.Context) error {
	if err := c.Check(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Python, "-B", "-s", "-X", "utf8", c.Script, "--mode", "selftest")
	proc.Hide(cmd)
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"done"`) {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 1200 {
			msg = "…" + msg[len(msg)-1200:]
		}
		if msg == "" && err != nil {
			msg = err.Error()
		}
		return fmt.Errorf("转换组件自检失败：%s", msg)
	}
	return nil
}
