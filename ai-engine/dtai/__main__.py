"""引擎入口。

  python -m dtai serve   --root <ai 目录> [--ffmpeg <ffmpeg>]   常驻：stdin 逐行读请求，stdout 逐行写事件
  python -m dtai run <cmd> --root ... --args '<json>'            单次执行（调试 / CI 冒烟测试）
  python -m dtai selftest --root ...                              检查已安装组件能否导入

请求：{"id": "1", "cmd": "ocr", "args": {...}}；取消：{"id": "1", "cmd": "cancel"}
事件：{"id": "1", "event": "progress", "pct": 30, "msg": "…"}
      {"id": "1", "event": "partial", "data": {...}}       （流式文字，如翻译 / 纪要）
      {"id": "1", "event": "result", "data": {...}}
      {"id": "1", "event": "error", "code": "missing_component", "message": "…", "component": "py-ocr"}
"""
import argparse
import importlib
import json
import io
import os
import sys
import threading
import traceback

os.environ.setdefault("TQDM_DISABLE", "1")
os.environ.setdefault("OMP_NUM_THREADS", "4")

_out_lock = threading.Lock()


def _write(obj):
    s = json.dumps(obj, ensure_ascii=False)
    with _out_lock:
        sys.stdout.write(s + "\n")
        sys.stdout.flush()


def handlers():
    from . import ocr, docs, inpaint, speech, llm, minutes, common

    def selftest(args, task):
        return selftest_report()

    return {
        "ping": lambda a, t: {"ok": True, "version": _version(), "python": sys.version.split()[0]},
        "selftest": selftest,
        "status": lambda a, t: status(),
        "ocr": lambda a, t: ocr.ocr_image(a["path"], t),
        "image2docx": lambda a, t: docs.image_to_docx(a["path"], a["output"], t),
        "image2xlsx": lambda a, t: docs.image_to_xlsx(a["path"], a["output"], t),
        "pdf2docx_ocr": lambda a, t: docs.pdf_to_docx(a["path"], a["output"], t, int(a.get("dpi") or 200)),
        "pdf_has_text": lambda a, t: {"hasText": docs.pdf_has_text(a["path"])},
        "inpaint": lambda a, t: inpaint.inpaint_file(a["path"], a["mask"], a.get("output"), t),
        "transcribe": lambda a, t: speech.transcribe(a["path"], int(a.get("speakers") or 0), t),
        "translate": lambda a, t: llm.translate(a.get("text", ""), a.get("direction", "auto"), a.get("model"), t),
        "rewrite": lambda a, t: llm.rewrite(a.get("text", ""), a.get("preset", "polish"), a.get("instruction", ""),
                                            a.get("model"), t),
        "summarize": lambda a, t: llm.summarize(a.get("transcript") or speech.transcript_text(a.get("segments") or [],
                                                                                               a.get("names")),
                                                a.get("model"), t),
        "export_minutes": lambda a, t: minutes.export(a.get("format", "docx"), a["output"], a.get("title") or "会议记录",
                                                      a.get("source", ""), a.get("duration", 0), a.get("segments") or [],
                                                      a.get("minutes", ""), a.get("names")),
        "llm_stop": lambda a, t: (llm.SERVER.stop(), {"ok": True})[1],
    }


def _version():
    from . import __version__
    return __version__


def status():
    from . import common
    out = {}
    for kind in ("site", "models", "bin"):
        d = os.path.join(common.ROOT, kind)
        if os.path.isdir(d):
            for c in os.listdir(d):
                if os.path.exists(os.path.join(d, c, ".installed.json")):
                    out[c] = True
    return {"installed": sorted(out), "root": common.ROOT}


def selftest_report():
    """尝试导入每个已安装组件的关键模块，返回 {组件: "ok" | 错误}。"""
    from . import common
    common.setup(common.ROOT)
    checks = {"py-ort": ["onnxruntime"], "py-ocr": ["rapidocr", "rapid_layout", "rapid_table", "openpyxl"],
              "py-speech": ["sherpa_onnx"]}
    rep = {"python": sys.version.split()[0], "base": {}}
    for m in ("numpy", "cv2", "docx", "fitz"):
        try:
            importlib.import_module(m)
            rep["base"][m] = "ok"
        except Exception as e:
            rep["base"][m] = "%s: %s" % (type(e).__name__, e)
    for comp, mods in checks.items():
        if not common.installed("site", comp):
            rep[comp] = "not installed"
            continue
        try:
            for m in mods:
                importlib.import_module(m)
            rep[comp] = "ok"
        except Exception as e:
            rep[comp] = "%s: %s" % (type(e).__name__, e)
    return rep


def _detach_stdin():
    """把命令管道移到私有文件描述符，标准输入改指 NUL。

    Windows 上主线程在管道上同步 ReadFile 时，其他线程对同一句柄的任何操作
    （导入扩展模块时查询标准句柄、启动 ffmpeg 子进程继承句柄等）都会被阻塞，
    直到下一行命令到来——表现为第一条命令要等很久才返回。
    """
    try:
        fd = os.dup(0)
        nul = os.open(os.devnull, os.O_RDONLY)
        os.dup2(nul, 0)
        os.close(nul)
        sys.stdin = open(os.devnull, "r", encoding="utf-8")
        return io.TextIOWrapper(io.FileIO(fd, "rb", closefd=True), encoding="utf-8", newline=None)
    except Exception:
        return sys.stdin


class Server:
    def __init__(self):
        self.tasks = {}
        self.lock = threading.Lock()
        self.handlers = handlers()

    def handle(self, req):
        from .common import Task, Canceled, UserError, setup, ROOT  # noqa: F401
        from . import common
        rid = str(req.get("id", ""))
        cmd = req.get("cmd", "")
        if cmd == "cancel":
            with self.lock:
                t = self.tasks.get(rid)
            if t:
                t.cancel_event.set()
            return
        if cmd == "shutdown":
            from . import llm
            llm.SERVER.stop()
            os._exit(0)
        task = Task(lambda ev: _write(dict(ev, id=rid)))
        with self.lock:
            self.tasks[rid] = task

        def work():
            try:
                common.setup(common.ROOT)  # 新下载的组件立即可用
                importlib.invalidate_caches()
                fn = self.handlers.get(cmd)
                if fn is None:
                    raise UserError("未知命令：%s" % cmd)
                res = fn(req.get("args") or {}, task)
                _write({"id": rid, "event": "result", "data": res})
            except Canceled:
                _write({"id": rid, "event": "error", "code": "canceled", "message": "已取消"})
            except UserError as e:
                ev = {"id": rid, "event": "error", "code": e.code, "message": str(e)}
                ev.update(e.extra)
                _write(ev)
            except MemoryError:
                _write({"id": rid, "event": "error", "code": "oom", "message": "内存不足"})
            except Exception as e:
                tb = traceback.format_exc()
                sys.stderr.write(tb)
                _write({"id": rid, "event": "error", "code": "internal",
                        "message": "%s: %s" % (type(e).__name__, e), "trace": tb[-1500:]})
            finally:
                with self.lock:
                    self.tasks.pop(rid, None)
        threading.Thread(target=work, daemon=True).start()

    def loop(self):
        reader = _detach_stdin()
        _write({"id": "", "event": "ready", "data": {"version": _version()}})
        for line in reader:
            line = line.strip()
            if not line:
                continue
            try:
                req = json.loads(line)
            except ValueError:
                continue
            self.handle(req)
        # 主程序退出（stdin 关闭）：顺带关掉 llama-server
        from . import llm
        llm.SERVER.stop()


def main(argv=None):
    ap = argparse.ArgumentParser(prog="dtai")
    ap.add_argument("mode", choices=["serve", "run", "selftest"])
    ap.add_argument("cmd", nargs="?")
    ap.add_argument("--root", required=True)
    ap.add_argument("--ffmpeg", default="")
    ap.add_argument("--args", default="{}")
    a = ap.parse_args(argv)
    try:
        sys.stdout.reconfigure(encoding="utf-8")
        sys.stdin.reconfigure(encoding="utf-8")
    except Exception:
        pass
    from . import common
    common.setup(a.root, a.ffmpeg or None)
    if a.mode == "serve":
        Server().loop()
        return 0
    if a.mode == "selftest":
        rep = selftest_report()
        print(json.dumps(rep, ensure_ascii=False, indent=1))
        bad = [k for k, v in rep.items() if k != "python" and k != "base" and v not in ("ok", "not installed")]
        bad += [k for k, v in rep["base"].items() if v != "ok"]
        return 1 if bad else 0
    # run：单次执行，事件逐行打印
    from .common import Task, UserError
    args = json.loads(a.args)
    task = Task(lambda ev: _write(ev) if ev.get("event") == "progress" else None)
    try:
        res = handlers()[a.cmd](args, task)
    except UserError as e:
        _write({"event": "error", "code": e.code, "message": str(e)})
        return 2
    finally:
        if a.cmd in ("translate", "rewrite", "summarize"):
            from . import llm
            llm.SERVER.stop()
    _write({"event": "result", "data": res})
    return 0


if __name__ == "__main__":
    sys.exit(main())
