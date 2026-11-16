"""桌面工具箱 离线 AI 引擎（dtai）。

由主程序用内置 Python 3.12 启动（`python -m dtai serve --root <ai 数据目录>`），通过 stdin/stdout
逐行 JSON 通信。所有模型均在本机运行，首次使用时由主程序下载到 <root>，之后完全离线。

目录约定（<root> = %APPDATA%\\DesktopToolbox\\ai）：
  <root>/site/<组件>/      已解压的 Python 轮子（每个组件一个目录，便于删除）
  <root>/models/<组件>/    模型文件
  <root>/bin/<组件>/       可执行程序（llama.cpp 的 llama-server）
"""

__version__ = "0.2.0"
