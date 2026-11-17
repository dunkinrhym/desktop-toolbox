package aiengine

// 端到端冒烟测试：通过常驻引擎（与主程序相同的 stdin/stdout 通道）运行每个 AI 功能，用真实模型。
//
//	DT_AI_ROOT    已安装组件的目录（dtai install 的 -root）
//	DT_TEST_PYTHON 内置 Python（Windows 便携版的 resources\python\python.exe）
//	DT_AI_OUT     输出目录（可选）
//	DT_FFMPEG     ffmpeg（可选，默认 PATH 中的 ffmpeg）
//	DT_AI_AUDIO   会议测试音频（可选，多说话人中文 wav）
//	DT_AI_LLM     大模型组件（可选，默认 llm-0.6b）
//
// 未设置 DT_AI_ROOT 时跳过。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func smokeEnv(t *testing.T) (*Engine, string, string) {
	root := os.Getenv("DT_AI_ROOT")
	py := os.Getenv("DT_TEST_PYTHON")
	if root == "" || py == "" {
		t.Skip("未设置 DT_AI_ROOT / DT_TEST_PYTHON")
	}
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Join(filepath.Dir(file), "..", "..")
	out := os.Getenv("DT_AI_OUT")
	if out == "" {
		out = t.TempDir()
	}
	os.MkdirAll(out, 0o755)
	ff := os.Getenv("DT_FFMPEG")
	if ff == "" {
		ff = "ffmpeg"
	}
	script := filepath.Join(repo, "ai-engine", "dtai_main.py")
	if s := os.Getenv("DT_AI_SCRIPT"); s != "" { // 测试安装包里的引擎副本（如 UOS deb 的 /opt/apps/.../ai-engine）
		script = s
	}
	e := &Engine{Python: py, Script: script, Root: root, FFmpeg: ff}
	t.Cleanup(e.Stop)
	return e, filepath.Join(repo, "ai-engine", "tests", "samples"), out
}

func call(t *testing.T, e *Engine, cmd string, args any, v any) time.Duration {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	st := time.Now()
	res, err := e.Call(ctx, cmd, args, nil)
	d := time.Since(st)
	if err != nil {
		t.Fatalf("%s 失败（%s）：%v", cmd, d, err)
	}
	if v != nil {
		if err := json.Unmarshal(res, v); err != nil {
			t.Fatalf("%s 结果无法解析：%v\n%s", cmd, err, res)
		}
	}
	t.Logf("%s 用时 %s", cmd, d.Round(time.Millisecond))
	return d
}

func TestEngineSmoke(t *testing.T) {
	e, samples, out := smokeEnv(t)
	// 与主程序一致：语音识别在独立的引擎进程中运行（sherpa-onnx 自带的 onnxruntime.dll 与 py-ort 的版本不同，避免同进程冲突）
	speech := &Engine{Python: e.Python, Script: e.Script, Root: e.Root, FFmpeg: e.FFmpeg}
	t.Cleanup(speech.Stop)
	llm := os.Getenv("DT_AI_LLM")
	if llm == "" {
		llm = "llm-0.6b"
	}

	t.Run("selftest", func(t *testing.T) {
		var rep map[string]any
		call(t, e, "selftest", nil, &rep)
		t.Logf("%v", rep)
		for _, k := range []string{"py-ort", "py-ocr", "py-speech"} {
			if rep[k] != "ok" {
				t.Errorf("%s：%v", k, rep[k])
			}
		}
	})

	var shotText string
	t.Run("截图识字", func(t *testing.T) {
		var r struct {
			Text  string `json:"text"`
			Lines []struct {
				Text string      `json:"text"`
				Box  [][]float64 `json:"box"`
			} `json:"lines"`
			Links []struct{ Text string } `json:"links"`
		}
		call(t, e, "ocr", map[string]any{"path": filepath.Join(samples, "screenshot.png")}, &r)
		call(t, e, "ocr", map[string]any{"path": filepath.Join(samples, "screenshot.png")}, &r) // 第二次（模型已加载）
		t.Logf("识别结果：%q", r.Text)
		shotText = r.Text
		for _, want := range []string{"offline", "离线运行的模型", "github.com"} {
			if !strings.Contains(r.Text, want) {
				t.Errorf("缺少 %q", want)
			}
		}
		if len(r.Lines) < 3 || len(r.Lines[0].Box) != 4 || len(r.Links) == 0 {
			t.Errorf("行 / 坐标 / 链接不完整：%d 行，%d 个链接", len(r.Lines), len(r.Links))
		}
	})

	t.Run("图片转Word", func(t *testing.T) {
		var r struct {
			Output   string         `json:"output"`
			Elements map[string]int `json:"elements"`
		}
		call(t, e, "image2docx", map[string]any{"path": filepath.Join(samples, "doc_table.png"),
			"output": filepath.Join(out, "图片转Word.docx")}, &r)
		if r.Elements["table"] != 1 || r.Elements["para"] < 3 {
			t.Errorf("版面元素：%v", r.Elements)
		}
		if st, err := os.Stat(r.Output); err != nil || st.Size() < 5000 {
			t.Errorf("输出文件：%v", err)
		}
	})

	t.Run("图片转Excel", func(t *testing.T) {
		var r struct {
			Output   string         `json:"output"`
			Elements map[string]int `json:"elements"`
		}
		call(t, e, "image2xlsx", map[string]any{"path": filepath.Join(samples, "doc_table.png"),
			"output": filepath.Join(out, "图片转Excel.xlsx")}, &r)
		if r.Elements["table"] != 1 {
			t.Errorf("版面元素：%v", r.Elements)
		}
	})

	t.Run("扫描PDF转Word", func(t *testing.T) {
		pdf := os.Getenv("DT_AI_SCANNED_PDF")
		if pdf == "" {
			t.Skip("未提供扫描版 PDF（DT_AI_SCANNED_PDF）")
		}
		var h struct {
			HasText bool `json:"hasText"`
		}
		call(t, e, "pdf_has_text", map[string]any{"path": pdf}, &h)
		if h.HasText {
			t.Error("扫描件应判断为无文字层")
		}
		var r struct {
			Pages int `json:"pages"`
		}
		call(t, e, "pdf2docx_ocr", map[string]any{"path": pdf, "output": filepath.Join(out, "扫描件.docx")}, &r)
		if r.Pages < 1 {
			t.Error("页数为 0")
		}
	})

	t.Run("去水印", func(t *testing.T) {
		var r struct {
			Output string `json:"output"`
		}
		call(t, e, "inpaint", map[string]any{"path": filepath.Join(samples, "watermark.png"),
			"mask": filepath.Join(samples, "watermark_mask.png"), "output": filepath.Join(out, "去水印.png")}, &r)
		if _, err := os.Stat(r.Output); err != nil {
			t.Error(err)
		}
	})

	var segments json.RawMessage
	t.Run("会议转写", func(t *testing.T) {
		audio := os.Getenv("DT_AI_AUDIO")
		if audio == "" {
			t.Skip("未提供测试音频（DT_AI_AUDIO）")
		}
		var r struct {
			Duration float64         `json:"duration"`
			Speakers int             `json:"speakers"`
			Segments json.RawMessage `json:"segments"`
		}
		d := call(t, speech, "transcribe", map[string]any{"path": audio}, &r)
		t.Logf("时长 %.1fs，%d 位说话人，实时率 %.2f\n%s", r.Duration, r.Speakers, d.Seconds()/r.Duration, r.Segments)
		if r.Speakers < 2 || !strings.Contains(string(r.Segments), "测试") {
			t.Errorf("转写结果异常")
		}
		segments = r.Segments
	})

	t.Run("翻译", func(t *testing.T) {
		var r struct{ Text, Direction string }
		call(t, e, "translate", map[string]any{"text": "Machine learning models can run fully offline on your laptop.", "model": llm}, &r)
		t.Logf("英→中：%s", r.Text)
		if r.Direction != "en2zh" || !hasCJK(r.Text) {
			t.Errorf("英译中失败：%+v", r)
		}
		call(t, e, "translate", map[string]any{"text": "离线运行的模型不会上传任何数据。", "model": llm}, &r)
		t.Logf("中→英：%s", r.Text)
		if r.Direction != "zh2en" || hasCJK(r.Text) {
			t.Errorf("中译英失败：%+v", r)
		}
		if shotText != "" {
			call(t, e, "translate", map[string]any{"text": shotText, "direction": "en2zh", "model": llm}, &r)
			t.Logf("截图文字→中文：%s", r.Text)
		}
	})

	t.Run("改写", func(t *testing.T) {
		src := "这个功能我们做的挺好的，用户用了都说好，下次继续这么干。"
		for _, p := range []string{"polish", "concise", "expand", "formal"} {
			var r struct{ Text string }
			call(t, e, "rewrite", map[string]any{"text": src, "preset": p, "model": llm}, &r)
			t.Logf("%s：%s", p, r.Text)
			if strings.TrimSpace(r.Text) == "" {
				t.Errorf("%s 结果为空", p)
			}
		}
		var r struct{ Text string }
		call(t, e, "rewrite", map[string]any{"text": src, "instruction": "改写成一句英文", "model": llm}, &r)
		t.Logf("自定义：%s", r.Text)
	})

	t.Run("会议纪要", func(t *testing.T) {
		args := map[string]any{"model": llm, "transcript": "00:00 说话人1：我们下周三之前要把新版本发布出去。\n00:08 说话人2：测试还差安装包这一项，我明天负责完成。\n00:15 说话人1：好，那就定在周二发布，市场部周三发公告。"}
		if segments != nil {
			args = map[string]any{"model": llm, "segments": segments}
		}
		var r struct {
			Markdown string `json:"markdown"`
		}
		call(t, e, "summarize", args, &r)
		t.Logf("纪要：\n%s", r.Markdown)
		if !strings.Contains(r.Markdown, "摘要") {
			t.Errorf("纪要格式不对")
		}
		var segs []map[string]any
		_ = json.Unmarshal(segments, &segs)
		if segs == nil {
			segs = []map[string]any{{"start": 0, "end": 3, "speaker": 1, "text": "测试"}}
		}
		for _, f := range []string{"docx", "md"} {
			var o struct{ Output string }
			call(t, e, "export_minutes", map[string]any{"format": f, "output": filepath.Join(out, "会议记录."+f),
				"title": "测试会议", "segments": segs, "minutes": r.Markdown, "duration": 57}, &o)
			if _, err := os.Stat(o.Output); err != nil {
				t.Error(err)
			}
		}
	})

	t.Run("取消", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		go func() { time.Sleep(300 * time.Millisecond); cancel() }()
		_, err := e.Call(ctx, "rewrite", map[string]any{"text": strings.Repeat("会议记录测试。", 200), "preset": "expand", "model": llm}, nil)
		if err != ErrCanceled {
			t.Errorf("应返回已取消，得到 %v", err)
		}
		var p map[string]any
		call(t, e, "ping", nil, &p) // 取消后引擎仍可用
	})
}

func hasCJK(s string) bool {
	for _, r := range s {
		if r >= 0x4e00 && r <= 0x9fff {
			return true
		}
	}
	return false
}
