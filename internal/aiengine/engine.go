package aiengine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"desktoptoolbox/internal/proc"
)

// Event 引擎事件。
type Event struct {
	ID        string          `json:"id"`
	Event     string          `json:"event"` // progress | partial | result | error | ready
	Pct       int             `json:"pct,omitempty"`
	Msg       string          `json:"msg,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Code      string          `json:"code,omitempty"`
	Message   string          `json:"message,omitempty"`
	Component string          `json:"component,omitempty"`
}

// Error 引擎返回的错误。
type Error struct {
	Code      string
	Message   string
	Component string
}

func (e *Error) Error() string { return e.Message }

// ErrCanceled 任务被取消。
var ErrCanceled = errors.New("已取消")

type pending struct {
	ch      chan Event
	onEvent func(Event)
}

// Engine 一个常驻的 Python AI 引擎进程（stdin/stdout 逐行 JSON）。
type Engine struct {
	Python string // 内置 Python
	Script string // ai-engine/dtai_main.py
	Root   string // AI 数据目录
	FFmpeg string

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	calls   map[string]*pending
	next    int
	errTail []string
	dead    chan struct{}
}

// Check 检查引擎文件是否存在。
func (e *Engine) Check() error {
	if _, err := os.Stat(e.Python); err != nil {
		return fmt.Errorf("找不到内置 Python：%s，请重新安装本软件", e.Python)
	}
	if _, err := os.Stat(e.Script); err != nil {
		return fmt.Errorf("找不到 AI 引擎脚本：%s，请重新安装本软件", e.Script)
	}
	return nil
}

func (e *Engine) start() error {
	if e.cmd != nil {
		return nil
	}
	if err := e.Check(); err != nil {
		return err
	}
	script, _ := filepath.Abs(e.Script)
	root, _ := filepath.Abs(e.Root)
	args := []string{"-B", "-s", "-X", "utf8", script, "serve", "--root", root}
	if e.FFmpeg != "" {
		args = append(args, "--ffmpeg", e.FFmpeg)
	}
	cmd := exec.Command(e.Python, args...)
	proc.Hide(cmd)
	cmd.Dir = filepath.Dir(script)
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1", "PYTHONDONTWRITEBYTECODE=1",
		"TQDM_DISABLE=1", "DTAI_ROOT="+root)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("无法启动 AI 引擎：%w", err)
	}
	e.cmd, e.stdin = cmd, stdin
	e.calls = map[string]*pending{}
	e.errTail = nil
	dead := make(chan struct{})
	e.dead = dead
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			e.mu.Lock()
			e.errTail = append(e.errTail, sc.Text())
			if len(e.errTail) > 40 {
				e.errTail = e.errTail[len(e.errTail)-40:]
			}
			e.mu.Unlock()
		}
	}()
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64<<10), 64<<20)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 || line[0] != '{' {
				continue
			}
			var ev Event
			if json.Unmarshal(line, &ev) != nil {
				continue
			}
			e.mu.Lock()
			p := e.calls[ev.ID]
			e.mu.Unlock()
			if p == nil {
				continue
			}
			switch ev.Event {
			case "result", "error":
				p.ch <- ev
			default:
				if p.onEvent != nil {
					p.onEvent(ev)
				}
			}
		}
		werr := cmd.Wait()
		e.mu.Lock()
		tail := strings.Join(e.errTail, "\n")
		if len(tail) > 1500 {
			tail = "…" + tail[len(tail)-1500:]
		}
		msg := "AI 引擎意外退出"
		if werr != nil {
			msg += "（" + werr.Error() + "）"
		}
		if tail != "" {
			msg += "：" + tail
		}
		for id, p := range e.calls {
			p.ch <- Event{ID: id, Event: "error", Code: "engine_exit", Message: msg}
		}
		e.calls = map[string]*pending{}
		if e.cmd == cmd {
			e.cmd, e.stdin = nil, nil
		}
		e.mu.Unlock()
		close(dead)
	}()
	return nil
}

func (e *Engine) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if e.stdin == nil {
		return errors.New("AI 引擎未运行")
	}
	_, err = e.stdin.Write(append(b, '\n'))
	return err
}

// Call 执行一条命令，onEvent 接收进度 / 流式事件。ctx 取消时通知引擎中止该任务。
func (e *Engine) Call(ctx context.Context, cmd string, args any, onEvent func(Event)) (json.RawMessage, error) {
	e.mu.Lock()
	if err := e.start(); err != nil {
		e.mu.Unlock()
		return nil, err
	}
	e.next++
	id := strconv.Itoa(e.next)
	p := &pending{ch: make(chan Event, 1), onEvent: onEvent}
	e.calls[id] = p
	err := e.send(map[string]any{"id": id, "cmd": cmd, "args": args})
	dead := e.dead
	e.mu.Unlock()
	if err != nil {
		e.mu.Lock()
		delete(e.calls, id)
		e.mu.Unlock()
		return nil, fmt.Errorf("无法与 AI 引擎通信：%w", err)
	}
	finished := make(chan struct{})
	defer close(finished)
	canceled := false
	for {
		select {
		case ev := <-p.ch:
			e.mu.Lock()
			delete(e.calls, id)
			e.mu.Unlock()
			if ev.Event == "result" {
				return ev.Data, nil
			}
			if ev.Code == "canceled" || canceled {
				return nil, ErrCanceled
			}
			return nil, &Error{Code: ev.Code, Message: ev.Message, Component: ev.Component}
		case <-ctx.Done():
			if !canceled {
				canceled = true
				e.mu.Lock()
				_ = e.send(map[string]any{"id": id, "cmd": "cancel"})
				e.mu.Unlock()
				// 引擎在 15 秒内未响应取消时强制结束进程
				go func() {
					select {
					case <-finished:
					case <-time.After(15 * time.Second):
						e.Kill()
					case <-dead:
					}
				}()
				ctx = context.Background()
			}
		}
	}
}

// Kill 立即结束引擎进程（llama-server 子进程由引擎在 stdin 关闭时清理；被强杀时由 Windows 作业对象/下次启动清理）。
func (e *Engine) Kill() {
	e.mu.Lock()
	c := e.cmd
	e.mu.Unlock()
	if c != nil && c.Process != nil {
		_ = c.Process.Kill()
	}
}

// Stop 正常关闭引擎（同时关闭 llama-server）。
func (e *Engine) Stop() {
	e.mu.Lock()
	c, in, dead := e.cmd, e.stdin, e.dead
	if c != nil {
		_ = e.send(map[string]any{"id": "", "cmd": "shutdown"})
	}
	e.mu.Unlock()
	if c == nil {
		return
	}
	if in != nil {
		in.Close()
	}
	select {
	case <-dead:
	case <-time.After(5 * time.Second):
		_ = c.Process.Kill()
	}
}

// Running 引擎进程是否在运行。
func (e *Engine) Running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cmd != nil
}
