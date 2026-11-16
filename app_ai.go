//go:build windows || darwin

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"desktoptoolbox/internal/aiengine"
)

// 离线 AI 功能：组件下载 / 删除，以及调用 AI 引擎（ai-engine/dtai）。
// 语音识别使用独立的引擎进程（sherpa-onnx 自带的 onnxruntime 与 py-ort 版本不同，避免同进程 DLL 冲突）。

type aiState struct {
	once    sync.Once
	mgr     *aiengine.Manager
	vision  *aiengine.Engine
	speech  *aiengine.Engine
	mu      sync.Mutex
	dlBusy  map[string]context.CancelFunc
	jobs    map[string]context.CancelFunc
	llmPref string
}

func (a *App) aiInit() {
	a.ai.once.Do(func() {
		root := filepath.Join(a.dataDir, "ai")
		res := filepath.Dir(filepath.Dir(a.conv.Script)) // resources
		script := filepath.Join(res, "ai-engine", "dtai_main.py")
		a.ai.mgr = aiengine.NewManager(root)
		a.ai.vision = &aiengine.Engine{Python: a.conv.Python, Script: script, Root: root, FFmpeg: a.ffmpeg}
		a.ai.speech = &aiengine.Engine{Python: a.conv.Python, Script: script, Root: root, FFmpeg: a.ffmpeg}
		a.ai.dlBusy = map[string]context.CancelFunc{}
		a.ai.jobs = map[string]context.CancelFunc{}
		if b, err := os.ReadFile(filepath.Join(root, "llm.txt")); err == nil {
			a.ai.llmPref = strings.TrimSpace(string(b))
		}
	})
}

func (a *App) aiShutdown() {
	if a.ai.vision != nil {
		a.ai.vision.Stop()
		a.ai.speech.Stop()
	}
}

// AIComponent 设置页显示的组件信息。
type AIComponent struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Desc      string `json:"desc"`
	Size      int64  `json:"size"`
	Installed bool   `json:"installed"`
	DiskSize  int64  `json:"diskSize"`
	Busy      bool   `json:"busy"`
	LLM       bool   `json:"llm"`
}

// AIStatus 组件列表、当前大模型、内存。
type AIStatus struct {
	Components []AIComponent `json:"components"`
	LLM        string        `json:"llm"`
	Recommend  string        `json:"recommend"`
	RAMGB      float64       `json:"ramGB"`
	Root       string        `json:"root"`
}

// GetAIStatus 返回 AI 组件状态。
func (a *App) GetAIStatus() (AIStatus, error) {
	a.aiInit()
	m, err := aiengine.LoadManifest()
	if err != nil {
		return AIStatus{}, err
	}
	ram := aiengine.TotalRAM()
	st := AIStatus{LLM: a.currentLLM(), Recommend: aiengine.RecommendLLM(ram), RAMGB: float64(ram) / (1 << 30), Root: a.ai.mgr.Root}
	plat := aiengine.Platform()
	a.ai.mu.Lock()
	defer a.ai.mu.Unlock()
	for i := range m.Components {
		c := &m.Components[i]
		if c.Hidden || len(c.FilesFor(plat)) == 0 {
			continue
		}
		inst := a.ai.mgr.IsInstalled(c)
		ac := AIComponent{ID: c.ID, Name: c.Name, Desc: c.Desc, Size: c.TotalSize(plat), Installed: inst,
			Busy: a.ai.dlBusy[c.ID] != nil, LLM: aiengine.IsLLM(c.ID)}
		if inst {
			ac.DiskSize = a.ai.mgr.DiskSize(c)
		}
		st.Components = append(st.Components, ac)
	}
	return st, nil
}

// currentLLM 用户选择的大模型；未选择时用已安装的最大模型，否则按内存推荐。
func (a *App) currentLLM() string {
	a.aiInit()
	if a.ai.llmPref != "" {
		return a.ai.llmPref
	}
	m, _ := aiengine.LoadManifest()
	for _, id := range aiengine.LLMIDs {
		if c, err := m.Component(id); err == nil && a.ai.mgr.IsInstalled(c) {
			return id
		}
	}
	return aiengine.RecommendLLM(aiengine.TotalRAM())
}

// SetAILLM 选择翻译 / 改写 / 纪要使用的大模型。
func (a *App) SetAILLM(id string) error {
	a.aiInit()
	if !aiengine.IsLLM(id) {
		return errors.New("未知的模型")
	}
	a.ai.llmPref = id
	os.MkdirAll(a.ai.mgr.Root, 0o755)
	return os.WriteFile(filepath.Join(a.ai.mgr.Root, "llm.txt"), []byte(id), 0o644)
}

// AIMissing 返回某功能还缺少的组件 ID（feature：ocr | doc | inpaint | meeting | translate | rewrite | summary）。
func (a *App) AIMissing(feature string) []string {
	a.aiInit()
	m, _ := aiengine.LoadManifest()
	var out []string
	for _, id := range m.FeatureComponents(feature, a.currentLLM()) {
		if c, err := m.Component(id); err == nil && !a.ai.mgr.IsInstalled(c) {
			out = append(out, id)
		}
	}
	return out
}

// AIInstall 后台下载组件，推送 ai:dl 事件（进度 / 完成 / 错误）。
func (a *App) AIInstall(ids []string) error {
	a.aiInit()
	m, err := aiengine.LoadManifest()
	if err != nil {
		return err
	}
	for _, id := range ids {
		c, err := m.Component(id)
		if err != nil {
			return err
		}
		a.ai.mu.Lock()
		if a.ai.dlBusy[id] != nil || a.ai.mgr.IsInstalled(c) {
			a.ai.mu.Unlock()
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		a.ai.dlBusy[id] = cancel
		a.ai.mu.Unlock()
		go func(c *aiengine.Component) {
			err := a.ai.mgr.Install(ctx, c, func(p aiengine.Progress) { runtime.EventsEmit(a.ctx, "ai:dl", p) })
			a.ai.mu.Lock()
			delete(a.ai.dlBusy, c.ID)
			a.ai.mu.Unlock()
			cancel()
			ev := map[string]any{"component": c.ID, "stage": "done"}
			if err != nil {
				ev["stage"] = "error"
				ev["error"] = err.Error()
				if ctx.Err() != nil {
					ev["error"] = "已取消"
				}
			}
			runtime.EventsEmit(a.ctx, "ai:dl", ev)
		}(c)
	}
	return nil
}

// AICancelInstall 取消下载（已下载部分保留，下次继续）。
func (a *App) AICancelInstall(id string) {
	a.aiInit()
	a.ai.mu.Lock()
	defer a.ai.mu.Unlock()
	if c := a.ai.dlBusy[id]; c != nil {
		c()
	}
}

// AIRemove 删除组件。
func (a *App) AIRemove(id string) error {
	a.aiInit()
	m, _ := aiengine.LoadManifest()
	c, err := m.Component(id)
	if err != nil {
		return err
	}
	// 先停掉引擎，释放被占用的模型 / DLL 文件
	a.ai.vision.Stop()
	a.ai.speech.Stop()
	return a.ai.mgr.Remove(c)
}

// AIRun 执行一条 AI 命令（job 为前端生成的任务 ID，用于进度事件 ai:job 与取消）。
func (a *App) AIRun(job, cmd string, args map[string]any) (json.RawMessage, error) {
	a.aiInit()
	if args == nil {
		args = map[string]any{}
	}
	switch cmd {
	case "translate", "rewrite", "summarize":
		if _, ok := args["model"]; !ok {
			args["model"] = a.currentLLM()
		}
	}
	eng := a.ai.vision
	if cmd == "transcribe" {
		eng = a.ai.speech
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.ai.mu.Lock()
	a.ai.jobs[job] = cancel
	a.ai.mu.Unlock()
	defer func() {
		a.ai.mu.Lock()
		delete(a.ai.jobs, job)
		a.ai.mu.Unlock()
		cancel()
	}()
	res, err := eng.Call(ctx, cmd, args, func(ev aiengine.Event) {
		runtime.EventsEmit(a.ctx, "ai:job", map[string]any{"job": job, "event": ev.Event, "pct": ev.Pct, "msg": ev.Msg, "data": ev.Data})
	})
	if err != nil {
		var ee *aiengine.Error
		if errors.As(err, &ee) && ee.Code == "missing_component" {
			return nil, errors.New(ee.Message)
		}
		return nil, err
	}
	return res, nil
}

// AICancel 取消 AI 任务。
func (a *App) AICancel(job string) {
	a.aiInit()
	a.ai.mu.Lock()
	defer a.ai.mu.Unlock()
	if c := a.ai.jobs[job]; c != nil {
		c()
	}
}

// PickImage 选择图片。
func (a *App) PickImage() (string, error) {
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{Title: "选择图片",
		Filters: []runtime.FileFilter{{DisplayName: "图片 (*.png;*.jpg;*.jpeg;*.bmp;*.webp)", Pattern: "*.png;*.jpg;*.jpeg;*.bmp;*.webp"}}})
}

// PickMedia 选择音频 / 视频。
func (a *App) PickMedia() (string, error) {
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{Title: "选择会议录音或录屏",
		Filters: []runtime.FileFilter{{DisplayName: "音频 / 视频", Pattern: "*.mp4;*.mov;*.mkv;*.avi;*.mp3;*.wav;*.m4a;*.aac;*.flac;*.ogg;*.wma;*.webm"}}})
}

// SaveAs 选择保存路径。
func (a *App) SaveAs(title, name, pattern string) (string, error) {
	return runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{Title: title, DefaultFilename: name,
		DefaultDirectory: aiDesktop(), Filters: []runtime.FileFilter{{DisplayName: pattern, Pattern: pattern}}})
}

// ReadImageDataURL 读取图片为 data URL（界面预览）。
func (a *App) ReadImageDataURL(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	mime := "image/png"
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		mime = "image/jpeg"
	case ".webp":
		mime = "image/webp"
	case ".bmp":
		mime = "image/bmp"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b), nil
}

// WriteTempPNG 把前端画好的蒙版（data URL）写入临时文件，返回路径。
func (a *App) WriteTempPNG(dataURL string) (string, error) {
	i := strings.Index(dataURL, ",")
	if i < 0 {
		return "", errors.New("数据无效")
	}
	b, err := base64.StdEncoding.DecodeString(dataURL[i+1:])
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "dt-mask-*.png")
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = f.Write(b)
	return f.Name(), err
}

// ---------------------------------------------------------------- 截图 + AI

// LastShot 最近一次截图：临时文件路径 + data URL。
type LastShot struct {
	Path string `json:"path"`
	URL  string `json:"url"`
}

// GetLastShot 返回最近一次截图（写入临时文件供 OCR 使用）。
func (a *App) GetLastShot() (LastShot, error) {
	a.shot.mu.Lock()
	data := a.shot.last
	a.shot.mu.Unlock()
	if len(data) == 0 {
		return LastShot{}, errors.New("还没有截图")
	}
	p := filepath.Join(os.TempDir(), "dt-last-shot.png")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return LastShot{}, err
	}
	return LastShot{Path: p, URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)}, nil
}

// SaveShotToDesktop 一键把最近一次截图保存到桌面。
func (a *App) SaveShotToDesktop() (string, error) {
	a.shot.mu.Lock()
	data := a.shot.last
	a.shot.mu.Unlock()
	if len(data) == 0 {
		return "", errors.New("还没有截图")
	}
	p := filepath.Join(aiDesktop(), timestampName("截图", ".png"))
	return p, os.WriteFile(p, data, 0o644)
}
