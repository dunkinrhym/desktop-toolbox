"""AI 引擎启动脚本：python dtai_main.py serve --root <ai 目录> --ffmpeg <ffmpeg>

Windows 嵌入式 Python 存在 ._pth 文件时不会把脚本目录加入 sys.path，这里手动加入。
"""
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from dtai.__main__ import main  # noqa: E402

if __name__ == "__main__":
    sys.exit(main())
