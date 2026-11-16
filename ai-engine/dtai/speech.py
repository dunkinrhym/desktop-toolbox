"""会议转写：FFmpeg 提取音频 → Silero VAD 切句 → SenseVoice（中/英/粤/日/韩，带标点）识别
→ pyannote 分割 + 3D-Speaker CAM++ 声纹聚类（说话人分离）→ 按时间重叠给每句分配“说话人N”。全部基于 sherpa-onnx，离线运行。
"""
import os
import re
import subprocess
import threading

from .common import FFMPEG, require, model_file, UserError, Canceled, popen_kwargs, cpu_threads
from . import common

SR = 16000
_lock = threading.Lock()
_rec = None


def recognizer():
    global _rec
    require("site", "py-speech")
    with _lock:
        if _rec is None:
            import sherpa_onnx as so
            _rec = so.OfflineRecognizer.from_sense_voice(
                model=model_file("m-asr", "model.int8.onnx"), tokens=model_file("m-asr", "tokens.txt"),
                language="auto", use_itn=True, num_threads=cpu_threads())
        return _rec


def load_audio(path, task=None, p0=0, p1=10):
    """用内置 FFmpeg 把任意音视频转成 16kHz 单声道 float32。"""
    import numpy as np
    if not os.path.exists(path):
        raise UserError("文件不存在：%s" % path, code="bad_input")
    cmd = [common.FFMPEG or FFMPEG, "-nostdin", "-hide_banner", "-i", path, "-vn", "-ac", "1", "-ar", str(SR),
           "-f", "s16le", "-acodec", "pcm_s16le", "-"]
    try:
        p = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, **popen_kwargs())
    except OSError as e:
        raise UserError("无法启动 FFmpeg：%s" % e)
    state = {"dur": 0.0, "err": []}

    def read_err():
        buf = b""
        while True:
            ch = p.stderr.read(256)
            if not ch:
                break
            buf += ch
            parts = re.split(rb"[\r\n]", buf)
            buf = parts[-1]
            for ln in parts[:-1]:
                s = ln.decode("utf-8", "replace")
                state["err"].append(s)
                del state["err"][:-20]
                m = re.search(r"Duration: (\d+):(\d+):([\d.]+)", s)
                if m:
                    state["dur"] = int(m.group(1)) * 3600 + int(m.group(2)) * 60 + float(m.group(3))
                m = re.search(r"time=(\d+):(\d+):([\d.]+)", s)
                if m and state["dur"] > 0 and task:
                    t = int(m.group(1)) * 3600 + int(m.group(2)) * 60 + float(m.group(3))
                    task.progress(p0 + (p1 - p0) * min(1, t / state["dur"]), "正在提取音频…")
    th = threading.Thread(target=read_err, daemon=True)
    th.start()
    chunks = []
    while True:
        if task and task.cancel_event.is_set():
            p.kill()
            raise Canceled()
        b = p.stdout.read(1 << 20)
        if not b:
            break
        chunks.append(b)
    p.wait()
    th.join(2)
    raw = b"".join(chunks)
    if len(raw) < SR // 5:
        msg = "\n".join(state["err"][-3:])
        if "does not contain any stream" in msg or "Output file #0 does not contain" in msg or not raw:
            raise UserError("文件中没有音频（录屏时可能没有录制麦克风声音）", code="no_audio")
        raise UserError("提取音频失败：%s" % msg, code="bad_input")
    if len(raw) % 2:
        raw = raw[:-1]
    return np.frombuffer(raw, dtype=np.int16).astype(np.float32) / 32768.0


def vad_segments(audio, task=None, p0=10, p1=20):
    """返回 [(start_sample, samples)]，每段 ≤ 20 秒。"""
    import sherpa_onnx as so
    cfg = so.VadModelConfig()
    cfg.silero_vad.model = model_file("m-diar", "silero_vad.onnx")
    cfg.silero_vad.min_silence_duration = 0.35
    cfg.silero_vad.min_speech_duration = 0.25
    cfg.silero_vad.max_speech_duration = 20
    cfg.silero_vad.threshold = 0.45
    cfg.sample_rate = SR
    cfg.num_threads = 1
    win = cfg.silero_vad.window_size
    vad = so.VoiceActivityDetector(cfg, buffer_size_in_seconds=60)
    out = []
    n = len(audio)
    step = win * 200
    for i in range(0, n, win):
        vad.accept_waveform(audio[i:i + win])
        while not vad.empty():
            seg = vad.front
            out.append((seg.start, seg.samples))
            vad.pop()
        if task and i % step == 0:
            task.check()
            task.progress(p0 + (p1 - p0) * i / max(1, n), "正在检测语音段…")
    vad.flush()
    while not vad.empty():
        seg = vad.front
        out.append((seg.start, seg.samples))
        vad.pop()
    res = []
    for st, samples in out:
        samples = list(samples) if not hasattr(samples, "shape") else samples
        res.append((int(st), len(samples)))
    return res


def diarize(audio, num_speakers=0, task=None, p0=60, p1=95):
    import sherpa_onnx as so
    cfg = so.OfflineSpeakerDiarizationConfig(
        segmentation=so.OfflineSpeakerSegmentationModelConfig(
            pyannote=so.OfflineSpeakerSegmentationPyannoteModelConfig(model=model_file("m-diar", "segmentation.onnx")),
            num_threads=cpu_threads()),
        embedding=so.SpeakerEmbeddingExtractorConfig(model=model_file("m-diar", "embedding.onnx"),
                                                     num_threads=cpu_threads()),
        clustering=so.FastClusteringConfig(num_clusters=num_speakers if num_speakers > 0 else -1, threshold=0.6),
        min_duration_on=0.3, min_duration_off=0.5)
    if not cfg.validate():
        raise UserError("说话人分离模型配置无效，请在设置中重新下载“说话人分离模型”", code="missing_component",
                        component="m-diar")
    sd = so.OfflineSpeakerDiarization(cfg)

    def cb(done, total, *_):
        if task:
            if task.cancel_event.is_set():
                return 1  # 非 0 表示中止
            task.progress(p0 + (p1 - p0) * done / max(1, total), "正在区分说话人…")
        return 0
    res = sd.process(audio, callback=cb).sort_by_start_time()
    if task:
        task.check()
    return [(s.start, s.end, s.speaker) for s in res]


def _turn_segments(audio, turns):
    """按说话人轮次切分音频（重叠部分归前一位），超过 20 秒的轮次再用 VAD 细分。返回 [(start, n, spk)]。"""
    out = []
    prev_end = 0.0
    for st, en, spk in turns:
        st = max(st, prev_end)
        if en - st < 0.3:
            continue
        prev_end = en
        a, b = int(st * SR), int(en * SR)
        if en - st <= 20:
            out.append((a, b - a, spk))
            continue
        subs = vad_segments(audio[a:b])
        if not subs:
            subs = [(i, min(20 * SR, b - a - i)) for i in range(0, b - a, 20 * SR)]
        for s0, n in subs:
            out.append((a + s0, n, spk))
    return out


def transcribe(path, num_speakers=0, task=None):
    """num_speakers：0 = 自动判断人数；1 = 不区分说话人；N = 指定人数。"""
    rec = recognizer()
    model_file("m-diar", "silero_vad.onnx")
    if task:
        task.progress(1, "正在提取音频…")
    audio = load_audio(path, task, 1, 10)
    dur = len(audio) / SR
    # 1) 切分：区分说话人时先做说话人分离，再按轮次切句；否则直接用 VAD 切句
    segs = []
    diarized = False
    if num_speakers != 1:
        try:
            turns = diarize(audio, num_speakers, task, 10, 55)
            segs = _turn_segments(audio, turns)
            diarized = bool(segs)
        except (Canceled, UserError):
            raise
        except Exception as e:  # 分离失败不影响逐字稿
            if task:
                task.progress(55, "说话人分离失败，改为不区分说话人：%s" % e)
    if not segs:
        segs = [(st, n, 0) for st, n in vad_segments(audio, task, 10, 20)]
    if not segs:
        raise UserError("没有检测到人声", code="no_speech")
    # 2) 识别
    p0 = 55 if diarized else 20
    results = []
    B = 8
    for bi in range(0, len(segs), B):
        if task:
            task.check()
            task.progress(p0 + (99 - p0) * bi / len(segs), "正在识别语音（%d / %d 句）…" % (bi, len(segs)))
        batch = segs[bi:bi + B]
        streams = []
        for st, n, _ in batch:
            s = rec.create_stream()
            s.accept_waveform(SR, audio[st:st + n])
            streams.append(s)
        rec.decode_streams(streams)
        for (st, n, spk), s in zip(batch, streams):
            text = s.result.text.strip()
            if text and not re.fullmatch(r"[\s。，、,.!?！？]*", text):
                results.append({"start": round(st / SR, 2), "end": round((st + n) / SR, 2), "text": text, "spk": spk})
    order = {}
    for r in results:
        if r["spk"] not in order:
            order[r["spk"]] = len(order) + 1
        r["speaker"] = order[r["spk"]]
    merged = _merge(results)
    if task:
        task.progress(100, "转写完成")
    return {"duration": round(dur, 2), "speakers": len(order), "segments": merged}


def _best_speaker(st, en, turns):
    best, bo = None, 0.0
    for ts, te, spk in turns:
        if te <= st:
            continue
        if ts >= en:
            break
        o = min(en, te) - max(st, ts)
        if o > bo:
            best, bo = spk, o
    if best is None and turns:  # 没有重叠时取最近的
        best = min(turns, key=lambda t: min(abs(t[0] - en), abs(t[1] - st)))[2]
    return best


def _merge(results, max_gap=2.0, max_len=90.0):
    out = []
    for r in results:
        if out and out[-1]["speaker"] == r["speaker"] and r["start"] - out[-1]["end"] <= max_gap \
                and r["end"] - out[-1]["start"] <= max_len:
            prev = out[-1]
            sep = " " if (prev["text"][-1:].isascii() and r["text"][:1].isascii()) else ""
            prev["text"] += sep + r["text"]
            prev["end"] = r["end"]
        else:
            out.append({k: r[k] for k in ("start", "end", "speaker", "text")})
    return out


def fmt_ts(sec):
    sec = int(sec)
    h, m, s = sec // 3600, sec % 3600 // 60, sec % 60
    return "%02d:%02d:%02d" % (h, m, s) if h else "%02d:%02d" % (m, s)


def transcript_text(segments, names=None):
    names = names or {}
    lines = []
    for s in segments:
        who = names.get(str(s["speaker"])) or "说话人%d" % s["speaker"]
        lines.append("%s %s：%s" % (fmt_ts(s["start"]), who, s["text"]))
    return "\n".join(lines)
