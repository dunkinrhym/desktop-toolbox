"""本地大模型：llama.cpp 的 llama-server + Qwen3 GGUF（只监听 127.0.0.1，随机端口，空闲 10 分钟自动退出）。

用于：截图文字翻译（英⇄中）、改写（润色 / 精简 / 扩写 / 正式书面语 / 自定义）、会议纪要。
"""
import glob
import json
import os
import re
import socket
import subprocess
import threading
import time
import urllib.request

from .common import ROOT, require, UserError, Canceled, popen_kwargs, cpu_threads
from . import common

IDLE_SECONDS = 600
# 本机连接绝不走系统 / 环境变量代理（Windows 上 urllib 会读取 IE 代理设置）
_OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))
LLM_COMPONENTS = ["llm-8b", "llm-4b", "llm-1.7b", "llm-0.6b"]  # 从大到小


class Server:
    def __init__(self):
        self.lock = threading.RLock()
        self.proc = None
        self.port = 0
        self.model = None
        self.ctx = 0
        self.last = 0.0
        self.log = []
        threading.Thread(target=self._idle_loop, daemon=True).start()

    def _idle_loop(self):
        while True:
            time.sleep(20)
            with self.lock:
                if self.proc and time.time() - self.last > IDLE_SECONDS:
                    self.stop()

    def stop(self):
        with self.lock:
            if self.proc:
                try:
                    self.proc.terminate()
                    self.proc.wait(5)
                except Exception:
                    try:
                        self.proc.kill()
                    except Exception:
                        pass
            self.proc = None
            self.model = None

    def ensure(self, comp, task=None):
        model = gguf_path(comp)
        with self.lock:
            self.last = time.time()
            if self.proc and self.proc.poll() is None and self.model == model:
                return
            self.stop()
            exe = server_exe()
            _kill_stale()
            self.port = _free_port()
            self.ctx = 8192 if comp in ("llm-8b", "llm-4b") else 4096
            args = [exe, "-m", model, "--host", "127.0.0.1", "--port", str(self.port), "-c", str(self.ctx),
                    "-t", str(cpu_threads()), "-np", "1", "--jinja"]
            self.log = []
            self.proc = subprocess.Popen(args, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE,
                                         stdin=subprocess.DEVNULL, cwd=os.path.dirname(exe), **popen_kwargs())
            threading.Thread(target=self._drain, args=(self.proc,), daemon=True).start()
            try:
                with open(_pid_file(), "w") as f:
                    f.write(str(self.proc.pid))
            except OSError:
                pass
            self.model = model
            t0 = time.time()
            while True:
                if task and task.cancel_event.is_set():
                    self.stop()
                    raise Canceled()
                if self.proc.poll() is not None:
                    tail = "\n".join(self.log[-6:])
                    self.proc = None
                    self.model = None
                    if "out of memory" in tail.lower() or "failed to allocate" in tail.lower():
                        raise UserError("内存不足，无法加载大模型。请在设置中改用更小的模型，或关闭其他程序后重试", code="oom")
                    raise UserError("大模型启动失败：%s" % (tail[-400:] or "未知错误"), code="llm_failed")
                try:
                    with _OPENER.open("http://127.0.0.1:%d/health" % self.port, timeout=2) as r:
                        if r.status == 200 and b"ok" in r.read():
                            break
                except Exception:
                    pass
                if task:
                    task.progress(min(30, 2 + (time.time() - t0) * 1.5), "正在加载本地大模型…")
                if time.time() - t0 > 300:
                    self.stop()
                    raise UserError("大模型加载超时", code="llm_failed")
                time.sleep(0.4)

    def _drain(self, p):
        for ln in iter(p.stderr.readline, b""):
            s = ln.decode("utf-8", "replace").rstrip()
            self.log.append(s)
            del self.log[:-50]

    def chat(self, comp, messages, task=None, max_tokens=1024, temperature=0.3, stream_cb=None):
        self.ensure(comp, task)
        body = {"messages": messages, "temperature": temperature, "top_p": 0.8, "max_tokens": max_tokens,
                "stream": True, "chat_template_kwargs": {"enable_thinking": False}}
        req = urllib.request.Request("http://127.0.0.1:%d/v1/chat/completions" % self.port,
                                     data=json.dumps(body).encode("utf-8"),
                                     headers={"Content-Type": "application/json"})
        out = []
        try:
            with _OPENER.open(req, timeout=600) as r:
                for raw in r:
                    self.last = time.time()
                    if task and task.cancel_event.is_set():
                        raise Canceled()
                    ln = raw.decode("utf-8", "replace").strip()
                    if not ln.startswith("data:"):
                        continue
                    data = ln[5:].strip()
                    if data == "[DONE]":
                        break
                    try:
                        d = json.loads(data)
                    except ValueError:
                        continue
                    ch = (d.get("choices") or [{}])[0]
                    delta = (ch.get("delta") or {}).get("content") or ""
                    if delta:
                        out.append(delta)
                        if stream_cb:
                            stream_cb(clean("".join(out)))
        except Canceled:
            # 关闭连接即可让 llama-server 停止生成
            raise
        except urllib.error.HTTPError as e:
            msg = e.read().decode("utf-8", "replace")[:300]
            if "exceed" in msg and "context" in msg:
                raise UserError("文字太长，超出了模型一次能处理的长度", code="too_long")
            raise UserError("大模型出错：%s" % msg, code="llm_failed")
        except OSError as e:
            raise UserError("大模型连接中断：%s" % e, code="llm_failed")
        finally:
            self.last = time.time()
        return clean("".join(out))


def _pid_file():
    return os.path.join(common.ROOT, "llama-server.pid")


def _kill_stale():
    """上次引擎被强制结束时遗留的 llama-server（只结束进程名确实是 llama-server 的）。"""
    try:
        with open(_pid_file()) as f:
            pid = int(f.read().strip())
    except (OSError, ValueError):
        return
    try:
        if os.name == "nt":
            out = subprocess.run(["tasklist", "/FI", "PID eq %d" % pid, "/NH", "/FO", "CSV"], capture_output=True,
                                 timeout=10, **popen_kwargs()).stdout.decode("utf-8", "replace").lower()
            if "llama-server" in out:
                subprocess.run(["taskkill", "/F", "/PID", str(pid)], capture_output=True, timeout=10, **popen_kwargs())
        else:
            with open("/proc/%d/cmdline" % pid, "rb") as f:
                if b"llama-server" in f.read():
                    os.kill(pid, 9)
    except Exception:
        pass
    try:
        os.remove(_pid_file())
    except OSError:
        pass


SERVER = Server()


def clean(s):
    s = re.sub(r"<think>.*?</think>", "", s, flags=re.S)
    s = re.sub(r"^<think>.*", "", s, flags=re.S) if s.lstrip().startswith("<think>") else s
    return s.strip()


def _free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    p = s.getsockname()[1]
    s.close()
    return p


def server_exe():
    d = require("bin", "llama")
    names = ["llama-server.exe", "llama-server"]
    for n in names:
        hits = glob.glob(os.path.join(d, "**", n), recursive=True)
        if hits:
            exe = hits[0]
            if os.name != "nt":
                os.chmod(exe, 0o755)
            return exe
    raise UserError("llama.cpp 程序缺失，请在设置中删除后重新下载“大模型运行程序”", code="missing_component", component="llama")


def gguf_path(comp):
    if comp not in LLM_COMPONENTS:
        raise UserError("未知的大模型：%s" % comp)
    d = require("models", comp)
    files = sorted(glob.glob(os.path.join(d, "*.gguf")))
    if not files:
        raise UserError("模型文件缺失，请在设置中重新下载", code="missing_component", component=comp)
    return files[0]


def pick(comp=None):
    """comp 为空时选已安装的最大模型。"""
    if comp:
        return comp
    for c in LLM_COMPONENTS:
        if os.path.exists(os.path.join(common.ROOT or ROOT, "models", c, ".installed.json")):
            return c
    raise UserError("还没有下载本地大模型，请先在“设置 → AI 模型”中下载", code="missing_component", component="llm")


# ---------------------------------------------------------------- 任务
def _is_cjk_major(text):
    cjk = sum(1 for ch in text if "\u4e00" <= ch <= "\u9fff")
    latin = sum(1 for ch in text if ch.isascii() and ch.isalpha())
    return cjk >= latin / 4


def translate(text, direction="auto", comp=None, task=None):
    text = (text or "").strip()
    if not text:
        raise UserError("没有要翻译的文字", code="bad_input")
    if direction == "auto":
        direction = "zh2en" if _is_cjk_major(text) else "en2zh"
    if direction == "en2zh":
        sys_p = ("你是专业的英译中翻译。把用户给出的文字翻译成通顺、准确的简体中文。"
                 "保留原文的换行和列表结构；网址、邮箱、代码、专有名词保持原样。只输出译文，不要解释。")
    else:
        sys_p = ("You are a professional Chinese-to-English translator. Translate the user's text into natural, "
                 "accurate English. Keep line breaks and list structure; keep URLs, emails and code unchanged. "
                 "Output only the translation.")
    out = _run(comp, sys_p, text, task, max_tokens=min(4096, 200 + len(text) * 3))
    return {"text": out, "direction": direction}


REWRITE_PRESETS = {
    "polish": "润色下面的文字：修正错别字和语病，让表达更通顺自然，保持原意和原有语言，长度大致不变。",
    "concise": "精简下面的文字：去掉重复和冗余，保留全部关键信息，篇幅压缩到原来的一半左右，保持原有语言。",
    "expand": "扩写下面的文字：在不改变原意的前提下补充细节、论据或过渡，使内容更充实，篇幅约为原来的两倍，保持原有语言。",
    "formal": "把下面的文字改写成正式的书面语：用词规范、语气得体，适合用于公文、邮件或报告，保持原意和原有语言。",
}


def rewrite(text, preset="polish", instruction="", comp=None, task=None):
    text = (text or "").strip()
    if not text:
        raise UserError("没有要改写的文字", code="bad_input")
    if instruction.strip():
        req = "按照以下要求改写文字：%s" % instruction.strip()
    else:
        req = REWRITE_PRESETS.get(preset) or REWRITE_PRESETS["polish"]
    sys_p = "你是中文写作助手。" + req + "只输出改写后的文字，不要任何解释、标题或引号。"
    out = _run(comp, sys_p, text, task, max_tokens=min(4096, 300 + len(text) * 3), temperature=0.5)
    return {"text": out, "preset": preset}


def _run(comp, sys_p, user, task, max_tokens=1024, temperature=0.3):
    comp = pick(comp)
    if task:
        task.progress(3, "正在加载本地大模型…")

    def cb(s):
        if task:
            task.partial({"text": s})
    out = SERVER.chat(comp, [{"role": "system", "content": sys_p}, {"role": "user", "content": user}],
                      task, max_tokens=max_tokens, temperature=temperature, stream_cb=cb)
    if task:
        task.progress(100, "完成")
    return out


MINUTES_SYS = (
    "你是专业的会议秘书。根据会议逐字稿撰写会议纪要，使用简体中文，内容必须忠于原文，不要编造。"
    "严格按下面的 Markdown 格式输出，不要输出其他内容：\n"
    "## 会议摘要\n（3～6 句话概括会议目的、主要讨论内容和结论）\n"
    "## 关键要点\n- 要点（可注明是哪位说话人提出的）\n"
    "## 决定事项\n- 已经达成的决定；没有就写“无”\n"
    "## 待办事项\n- [ ] 事项 —— 负责人（逐字稿中没有明确负责人就写“待定”）—— 截止时间（没有就省略）"
)

CHUNK_SYS = ("你是会议秘书。下面是一场长会议逐字稿中的一段。用简体中文提炼这一段的要点、决定和待办事项（含负责人），"
             "用 Markdown 列表输出，忠于原文，不要编造，不超过 300 字。")


def summarize(transcript, comp=None, task=None):
    """长会议分段提炼（map），再合并成纪要（reduce）。"""
    comp = pick(comp)
    transcript = (transcript or "").strip()
    if not transcript:
        raise UserError("逐字稿为空", code="bad_input")
    if task:
        task.progress(2, "正在加载本地大模型…")
    SERVER.ensure(comp, task)
    budget = 9000 if SERVER.ctx >= 8192 else 3500   # 每段字数（中文约 1.3 字/词元）
    chunks = _split(transcript, budget)
    notes = transcript
    if len(chunks) > 1:
        parts = []
        for i, ch in enumerate(chunks):
            if task:
                task.check()
                task.progress(5 + 70 * i / len(chunks), "正在提炼第 %d / %d 段…" % (i + 1, len(chunks)))
            parts.append(SERVER.chat(comp, [{"role": "system", "content": CHUNK_SYS}, {"role": "user", "content": ch}],
                                     task, max_tokens=600))
        notes = "\n\n".join("【第 %d 段】\n%s" % (i + 1, p) for i, p in enumerate(parts))
        while len(notes) > budget:  # 极长的会议：再合并一轮
            notes = notes[:budget]
        user = "以下是会议各段的要点记录，请据此写出整场会议的纪要：\n\n" + notes
    else:
        user = "会议逐字稿：\n\n" + transcript
    if task:
        task.progress(78, "正在生成会议纪要…")

    def cb(s):
        if task:
            task.partial({"text": s})
    out = SERVER.chat(comp, [{"role": "system", "content": MINUTES_SYS}, {"role": "user", "content": user}],
                      task, max_tokens=1500, temperature=0.3, stream_cb=cb)
    if task:
        task.progress(100, "纪要已生成")
    return {"markdown": out, "chunks": len(chunks), "model": comp}


def _split(text, budget):
    lines = text.split("\n")
    out, cur = [], ""
    for ln in lines:
        if cur and len(cur) + len(ln) + 1 > budget:
            out.append(cur)
            cur = ""
        while len(ln) > budget:
            out.append(ln[:budget])
            ln = ln[budget:]
        cur += ("\n" if cur else "") + ln
    if cur:
        out.append(cur)
    return out
