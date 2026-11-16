# 离线 AI 引擎（dtai）

主程序用内置的 Python 3.12 启动：`python dtai_main.py serve --root <AI 数据目录> --ffmpeg <ffmpeg>`，
stdin/stdout 逐行 JSON 通信（协议见 `dtai/__main__.py`）。模型与 Python 轮子按 `internal/aiengine/manifest.json`
首次使用时下载到 `%APPDATA%\DesktopToolbox\ai`，之后完全离线。

| 模块 | 功能 | 组件 |
| --- | --- | --- |
| `ocr.py` | 截图识字（行坐标 + 链接识别） | py-ort, py-ocr（RapidOCR 3.10，PP-OCRv6 模型随轮子） |
| `docs.py` | 图片 → Word / Excel，扫描版 PDF → Word | + m-table（SLANet-plus），版面分析 rapid_layout（CDLA） |
| `inpaint.py` | 去水印（画笔蒙版 + LaMa） | py-ort, m-lama |
| `speech.py` | 会议转写 + 说话人分离 | py-speech（sherpa-onnx 2.0.1）, m-asr（SenseVoice）, m-diar |
| `llm.py` | 翻译（英⇄中）、改写、会议纪要 | llama（llama.cpp llama-server）, llm-8b / llm-4b / llm-1.7b（Qwen3 GGUF） |
| `minutes.py` | 逐字稿 + 纪要导出 DOCX / MD | 内置 python-docx |

- 重新生成组件清单：`python3 ai-engine/tools/gen_manifest.py > internal/aiengine/manifest.json`
- 下载组件：`go run ./internal/aiengine/cmd/dtai install -root /tmp/ai py-ort py-ocr ...`
- 单条命令：`go run ./internal/aiengine/cmd/dtai call -root /tmp/ai -python python3 ocr '{"path":"x.png"}'`
- 冒烟测试：`DT_AI_ROOT=/tmp/ai DT_TEST_PYTHON=python3 go test -v -run TestEngineSmoke ./internal/aiengine/`
- 测试样例：`tests/make_samples.py` 生成 `tests/samples/*.png`。
