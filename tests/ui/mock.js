// 浏览器预览用的模拟后端（仅用于在 Linux 上检查界面，不会打包进程序）
(function () {
  const handlers = {};
  window.__mockRuntime = {
    EventsOn(n, cb) { (handlers[n] = handlers[n] || []).push(cb); },
    emit(n, d) { (handlers[n] || []).forEach(cb => cb(d)); },
  };
  const now = Date.now();
  const results = [
    { path: 'D:\\工作\\2026 年度报告.docx', name: '2026 年度报告.docx', dir: 'D:\\工作', size: 482133, modTime: now - 86400e3, match: 'name', isDir: false },
    { path: 'C:\\Users\\me\\Documents\\会议纪要-预算.md', name: '会议纪要-预算.md', dir: 'C:\\Users\\me\\Documents', size: 3120, modTime: now - 3600e3, match: 'content', snippet: '…本季度的预算调整方案已经确定，报告需在周五前提交…', isDir: false },
    { path: 'D:\\工作\\报告模板', name: '报告模板', dir: 'D:\\工作', size: 0, modTime: now - 7 * 86400e3, match: 'name', isDir: true },
    { path: 'E:\\资料\\销售报告.xlsx', name: '销售报告.xlsx', dir: 'E:\\资料', size: 98213, modTime: now - 30 * 86400e3, match: 'name', isDir: false },
    { path: 'E:\\资料\\产品手册.pdf', name: '产品手册.pdf', dir: 'E:\\资料', size: 5213133, modTime: now - 90 * 86400e3, match: 'content', snippet: '…详见第三章测试报告与附录…', isDir: false },
  ];
  const clips = [
    { id: '1', kind: 'text', preview: '这是置顶的常用地址：北京市朝阳区某某路 88 号', chars: 22, pinned: true, created: now - 86400e3 },
    { id: '2', kind: 'text', preview: 'https://example.com/docs/getting-started', chars: 40, pinned: false, created: now - 60e3 },
    { id: '3', kind: 'image', thumb: '../../frontend/dist/logo.png', width: 1280, height: 720, pinned: false, created: now - 600e3 },
    { id: '4', kind: 'text', preview: 'func main() {\n    fmt.Println("你好，世界")\n}', chars: 45, pinned: false, created: now - 7200e3 },
  ];
  const ok = v => Promise.resolve(v);
  window.__mockApi = {
    GetInfo: () => ok(/[?&]mac=1/.test(location.search) ? { name: '桌面工具箱', version: '0.1.0', platform: 'darwin', screenOk: false, hotkeys: [
      { name: '⌥ Option+Space', keys: '⌥ Option+Space', ok: true, action: '显示/隐藏主窗口' },
      { name: '⌃ Control+⌥ Option+A', keys: '⌃ Control+⌥ Option+A', ok: true, action: '截图' },
      { name: '⌘ Command+⇧ Shift+V', keys: '⌘ Command+⇧ Shift+V', ok: true, action: '剪贴板历史' }],
      dataDir: '/Users/me/Library/Application Support/DesktopToolbox', autostart: false, hasPython: true, hasFfmpeg: true, clipOn: true, initErrors: [] } : { name: '桌面工具箱', version: '0.1.0', platform: 'windows', screenOk: true, hotkeys: [
      { name: 'Alt+Space', keys: 'Alt+Space', ok: true, action: '显示/隐藏主窗口' },
      { name: 'Ctrl+Alt+A', keys: 'Ctrl+Alt+A', ok: true, action: '截图' },
      { name: 'Ctrl+Shift+V', keys: 'Ctrl+Shift+V', ok: false, action: '剪贴板历史' }],
      dataDir: 'C:\\Users\\me\\AppData\\Roaming\\DesktopToolbox', autostart: false, hasPython: true, hasFfmpeg: true, clipOn: true, initErrors: [] }),
    IsRecording: () => ok(false),
    StartSearch: () => { setTimeout(() => { window.__mockRuntime.emit('search:batch', { id: 1, items: results }); window.__mockRuntime.emit('search:done', { id: 1, stats: { dirs: 5231, files: 81234, scanned: 1320, results: 5 }, elapsedMs: 2310 }); }, 50); return ok(1); },
    CancelSearch: () => ok(),
    Preview: () => ok({ text: '会议纪要\n\n一、本季度的预算调整方案已经确定，报告需在周五前提交。\n二、市场部提交了新的推广报告，需要财务审核。\n\n' + '其他内容 '.repeat(60) + '\n\n三、下次会议讨论年度报告的格式。', truncated: false }),
    ClipList: () => ok(clips),
    ListMicrophones: () => ok(['麦克风阵列 (Realtek(R) Audio)']),
    PickFolder: () => ok('D:\\工作'),
  };
  window.__mockRuntime.EventsOn = window.__mockRuntime.EventsOn.bind(window.__mockRuntime);
})();
