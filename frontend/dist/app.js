'use strict';
/* 桌面工具箱 前端。无构建步骤，直接调用 Wails 注入的 window.go.main.App 与 window.runtime。 */

const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => Array.from(el.querySelectorAll(s));
const API = () => (window.go && window.go.main && window.go.main.App) || window.__mockApi;
const RT = () => window.runtime || window.__mockRuntime;
const on = (ev, cb) => RT().EventsOn(ev, cb);

function esc(s) {
  return String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
function errMsg(e) { return typeof e === 'string' ? e : (e && e.message) || String(e); }
function toast(msg, isErr = false, ms = 2600) {
  const t = document.createElement('div');
  t.className = 'toast' + (isErr ? ' error' : '');
  t.textContent = msg;
  $('#toasts').appendChild(t);
  setTimeout(() => t.remove(), isErr ? Math.max(ms, 5000) : ms);
}
async function call(name, ...args) {
  try {
    return await API()[name](...args);
  } catch (e) {
    toast(errMsg(e), true);
    throw e;
  }
}
function fmtSize(n) {
  if (n == null) return '';
  if (n < 1024) return n + ' B';
  if (n < 1048576) return (n / 1024).toFixed(1) + ' KB';
  if (n < 1073741824) return (n / 1048576).toFixed(1) + ' MB';
  return (n / 1073741824).toFixed(2) + ' GB';
}
function fmtTime(ms) {
  const d = new Date(ms), p = n => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}
function ago(ms) {
  const s = (Date.now() - ms) / 1000;
  if (s < 60) return '刚刚';
  if (s < 3600) return Math.floor(s / 60) + ' 分钟前';
  if (s < 86400) return Math.floor(s / 3600) + ' 小时前';
  if (s < 86400 * 7) return Math.floor(s / 86400) + ' 天前';
  return fmtTime(ms);
}
function baseName(p) { return String(p).split(/[\\/]/).pop(); }
function escRe(s) { return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'); }
function fileIcon(name, isDir) {
  if (isDir) return '📁';
  const e = (name.split('.').pop() || '').toLowerCase();
  if (['doc', 'docx'].includes(e)) return '📘';
  if (['xls', 'xlsx', 'csv'].includes(e)) return '📗';
  if (['ppt', 'pptx'].includes(e)) return '📙';
  if (e === 'pdf') return '📕';
  if (['png', 'jpg', 'jpeg', 'gif', 'bmp', 'webp', 'svg'].includes(e)) return '🖼️';
  if (['mp4', 'mov', 'avi', 'mkv', 'mp3', 'wav'].includes(e)) return '🎬';
  if (['zip', 'rar', '7z'].includes(e)) return '🗜️';
  if (['exe', 'msi', 'bat', 'cmd'].includes(e)) return '⚙️';
  return '📄';
}

/* ------------------------------------------------------------ navigation */
let currentView = 'search';
function showView(v) {
  if (!v) return;
  currentView = v;
  $$('.nav-item').forEach(a => a.classList.toggle('active', a.dataset.view === v));
  $$('.view').forEach(s => s.classList.toggle('active', s.id === 'view-' + v));
  if (v === 'clipboard') { loadClips(); setTimeout(() => $('#clip-q').focus(), 50); }
  if (v === 'search') setTimeout(() => $('#kw').focus(), 50);
  if (v === 'settings') loadInfo();
}
$$('.nav-item').forEach(a => a.addEventListener('click', () => showView(a.dataset.view)));

let popup = false;
on('ui:mode', m => {
  popup = !!m.popup;
  document.body.classList.toggle('popup', popup);
  if (popup) {
    showView('clipboard');
    $('#clip-q').value = '';
    clipFocus = 0;
    loadClips();
    setTimeout(() => $('#clip-q').focus(), 80);
  } else if (m.view) showView(m.view);
});

/* ------------------------------------------------------------ 文件搜索 */
const S = { id: 0, items: [], running: false, scope: 'all', folder: '', sort: null, asc: true, sel: -1, kws: [], phrase: '' };

$$('#scope .seg-btn').forEach(b => b.addEventListener('click', async () => {
  if (b.dataset.scope === 'folder') {
    const f = await call('PickFolder');
    if (!f) return;
    S.folder = f;
  }
  S.scope = b.dataset.scope;
  $$('#scope .seg-btn').forEach(x => x.classList.toggle('active', x === b));
  $('#folder-path').textContent = S.folder;
  $('#folder-path').classList.toggle('hidden', S.scope !== 'folder');
}));
$('#folder-path').addEventListener('click', async () => {
  const f = await call('PickFolder');
  if (f) { S.folder = f; $('#folder-path').textContent = f; }
});
$('#kw').addEventListener('keydown', e => { if (e.key === 'Enter') startSearch(); });
$('#btn-search').addEventListener('click', startSearch);
$('#btn-stop').addEventListener('click', () => API().CancelSearch());

function setSearching(b) {
  S.running = b;
  $('#btn-stop').classList.toggle('hidden', !b);
  $('#btn-search').textContent = b ? '重新搜索' : '搜索';
}

async function startSearch() {
  const kw = $('#kw').value.trim();
  if (!kw) { toast('请输入要搜索的关键词', true); return; }
  const content = $('#opt-content').checked;
  S.items = []; S.sel = -1; S.sort = null;
  S.phrase = kw;
  S.kws = kw.split(/\s+/).filter(Boolean);
  $$('.sortable').forEach(x => x.classList.remove('asc', 'desc'));
  $('#res-body').innerHTML = '';
  clearPreview();
  try {
    S.id = await call('StartSearch', { keyword: kw, folder: S.scope === 'folder' ? S.folder : '', content, exts: $('#opt-exts').value });
  } catch { return; }
  S.t0 = Date.now();
  setSearching(true);
  $('#search-status').textContent = (S.scope === 'folder' ? '正在搜索 ' + S.folder : '正在搜索所有磁盘') + (content ? '（含文件内容，可能较慢）' : '') + '…';
}

function highlightName(name) {
  if (!S.kws.length) return esc(name);
  const re = new RegExp(S.kws.map(escRe).join('|'), 'gi');
  let out = '', last = 0, m;
  while ((m = re.exec(name))) {
    if (!m[0]) { re.lastIndex++; continue; }
    out += esc(name.slice(last, m.index)) + '<mark>' + esc(m[0]) + '</mark>';
    last = m.index + m[0].length;
  }
  return out + esc(name.slice(last));
}

function rowHtml(r, i) {
  const snip = r.match === 'content' && r.snippet ? `<div class="snip">${highlightSnippet(r.snippet)}</div>` : '';
  return `<div class="res-row" data-i="${i}" title="${esc(r.path)}">
    <span class="c-name"><span class="ficon">${fileIcon(r.name, r.isDir)}</span><span class="nm">${highlightName(r.name)}</span>${r.match === 'content' ? '<span class="tag">内容</span>' : ''}</span>
    <span class="c-dir">${esc(r.dir)}</span>
    <span class="c-size">${r.isDir ? '' : fmtSize(r.size)}</span>
    <span class="c-time">${fmtTime(r.modTime)}</span>${snip}</div>`;
}
function highlightSnippet(s) {
  const re = new RegExp(escRe(S.phrase), 'gi');
  let out = '', last = 0, m;
  while ((m = re.exec(s))) {
    if (!m[0]) { re.lastIndex++; continue; }
    out += esc(s.slice(last, m.index)) + '<mark>' + esc(m[0]) + '</mark>';
    last = m.index + m[0].length;
  }
  return out + esc(s.slice(last));
}

function appendRows(items) {
  const start = S.items.length;
  S.items.push(...items);
  if (S.sort) { renderAllRows(); return; }
  const html = items.map((r, k) => rowHtml(r, start + k)).join('');
  $('#res-body').insertAdjacentHTML('beforeend', html);
}
function renderAllRows() {
  $('#res-body').innerHTML = S.items.map(rowHtml).join('');
  if (S.sel >= 0) { const el = $(`.res-row[data-i="${S.sel}"]`); if (el) el.classList.add('sel'); }
}
function updateStatus(done, stats, ms) {
  const n = S.items.length;
  let s = done ? `找到 ${n.toLocaleString()} 项` : `已找到 ${n.toLocaleString()} 项…`;
  if (stats) {
    s += ` · 扫描 ${stats.dirs.toLocaleString()} 个文件夹、${stats.files.toLocaleString()} 个文件`;
    if (stats.scanned) s += `，读取 ${stats.scanned.toLocaleString()} 个文档内容`;
  }
  if (ms != null) s += ` · 用时 ${(ms / 1000).toFixed(1)} 秒`;
  if (stats && stats.truncated) s += ' · 结果过多，只显示前 5000 项，请输入更精确的关键词';
  if (stats && stats.canceled) s += ' · 已停止';
  $('#search-status').textContent = s;
}
on('search:batch', b => {
  if (b.id !== S.id) return;
  appendRows(b.items || []);
  updateStatus(false);
});
on('search:done', d => {
  if (d.id !== S.id) return;
  setSearching(false);
  updateStatus(true, d.stats, d.elapsedMs);
  if (!S.items.length) {
    $('#res-body').innerHTML = `<div class="empty"><div class="empty-icon">🫥</div><div>没有找到匹配的文件</div><div class="muted">${$('#opt-content').checked ? '可以尝试更短的关键词' : '可以勾选“搜索文件内容”在文档内容中查找'}</div></div>`;
  }
});

$$('.sortable').forEach(h => h.addEventListener('click', () => {
  const k = h.dataset.sort;
  S.asc = S.sort === k ? !S.asc : true;
  S.sort = k;
  $$('.sortable').forEach(x => x.classList.remove('asc', 'desc'));
  h.classList.add(S.asc ? 'asc' : 'desc');
  const selItem = S.items[S.sel];
  const f = { name: r => r.name.toLowerCase(), size: r => r.size, time: r => r.modTime }[k];
  S.items.sort((a, b) => { const x = f(a), y = f(b); return (x < y ? -1 : x > y ? 1 : 0) * (S.asc ? 1 : -1); });
  S.sel = selItem ? S.items.indexOf(selItem) : -1;
  renderAllRows();
}));

const resBody = $('#res-body');
resBody.addEventListener('click', e => {
  const row = e.target.closest('.res-row');
  if (row) selectRow(+row.dataset.i);
});
resBody.addEventListener('dblclick', e => {
  const row = e.target.closest('.res-row');
  if (row) call('OpenPath', S.items[+row.dataset.i].path).catch(() => {});
});
resBody.addEventListener('contextmenu', e => {
  const row = e.target.closest('.res-row');
  if (!row) return;
  e.preventDefault();
  selectRow(+row.dataset.i);
  const m = $('#ctx-menu');
  m.classList.remove('hidden');
  const w = 180, h = 110;
  m.style.left = Math.min(e.clientX, innerWidth - w) + 'px';
  m.style.top = Math.min(e.clientY, innerHeight - h) + 'px';
});
document.addEventListener('click', () => $('#ctx-menu').classList.add('hidden'));
$('#ctx-menu').addEventListener('click', e => {
  const act = e.target.dataset.act, r = S.items[S.sel];
  if (!r) return;
  if (act === 'open') call('OpenPath', r.path).catch(() => {});
  if (act === 'folder') call('ShowInFolder', r.path).catch(() => {});
  if (act === 'copy') call('CopyText', r.path).then(() => toast('已复制路径')).catch(() => {});
});
resBody.addEventListener('keydown', e => {
  if (e.key === 'ArrowDown' && S.sel < S.items.length - 1) { selectRow(S.sel + 1); e.preventDefault(); }
  if (e.key === 'ArrowUp' && S.sel > 0) { selectRow(S.sel - 1); e.preventDefault(); }
  if (e.key === 'Enter' && S.items[S.sel]) call('OpenPath', S.items[S.sel].path).catch(() => {});
});
resBody.tabIndex = 0;

let pvTimer = null;
function selectRow(i) {
  $$('.res-row.sel').forEach(x => x.classList.remove('sel'));
  S.sel = i;
  const el = $(`.res-row[data-i="${i}"]`);
  if (el) { el.classList.add('sel'); el.scrollIntoView({ block: 'nearest' }); }
  clearTimeout(pvTimer);
  pvTimer = setTimeout(() => loadPreview(S.items[i]), 120);
}

const PV = { marks: [], cur: -1, path: '' };
function clearPreview() {
  PV.marks = []; PV.cur = -1; PV.path = '';
  $('#pv-title').textContent = '预览';
  $('#pv-count').textContent = '';
  $('#pv-tools').classList.add('hidden');
  $('#pv-body').innerHTML = '<div class="empty"><div class="muted">选择一个结果，在这里预览文字内容并以黄色标出关键词（不会修改原文件）</div></div>';
}
async function loadPreview(r) {
  if (!r) return;
  PV.path = r.path;
  $('#pv-title').textContent = r.name;
  $('#pv-title').title = r.path;
  $('#pv-tools').classList.remove('hidden');
  $('#pv-count').textContent = '';
  if (r.isDir) { $('#pv-body').innerHTML = '<div class="empty"><div class="muted">这是一个文件夹</div></div>'; return; }
  $('#pv-body').innerHTML = '<div class="empty"><div class="muted">正在读取…</div></div>';
  let res;
  try { res = await API().Preview(r.path); } catch (e) { res = { error: errMsg(e) }; }
  if (PV.path !== r.path) return;
  if (res.error) { $('#pv-body').innerHTML = `<div class="empty"><div class="muted">${esc(res.error)}</div></div>`; return; }
  renderPreview(res.text, res.truncated);
}
const MAX_MARKS = 5000;
function renderPreview(text, truncated) {
  const terms = [S.phrase, ...S.kws].filter((v, i, a) => v && a.indexOf(v) === i).sort((a, b) => b.length - a.length);
  const re = terms.length ? new RegExp(terms.map(escRe).join('|'), 'gi') : null;
  let out = '', last = 0, n = 0, m;
  if (re) {
    while ((m = re.exec(text)) && n < MAX_MARKS) {
      if (!m[0]) { re.lastIndex++; continue; }
      out += esc(text.slice(last, m.index)) + `<mark data-n="${n}">` + esc(m[0]) + '</mark>';
      last = m.index + m[0].length; n++;
    }
  }
  out += esc(text.slice(last));
  let warn = '';
  if (truncated) warn += '<div class="pv-warn">文件较大，仅显示前 3MB 文字</div>';
  if (n >= MAX_MARKS) warn += `<div class="pv-warn">匹配过多，仅高亮前 ${MAX_MARKS} 处</div>`;
  $('#pv-body').innerHTML = warn + '<pre>' + out + '</pre>';
  PV.marks = $$('#pv-body mark');
  PV.cur = -1;
  $('#pv-count').textContent = n ? `${n} 处匹配` : '内容中无匹配';
  if (n) gotoMark(0);
}
function gotoMark(i) {
  if (!PV.marks.length) return;
  i = (i + PV.marks.length) % PV.marks.length;
  if (PV.cur >= 0 && PV.marks[PV.cur]) PV.marks[PV.cur].classList.remove('cur');
  PV.cur = i;
  const mk = PV.marks[i];
  mk.classList.add('cur');
  mk.scrollIntoView({ block: 'center' });
  $('#pv-count').textContent = `第 ${i + 1} / ${PV.marks.length} 处`;
}
$('#pv-next').addEventListener('click', () => gotoMark(PV.cur + 1));
$('#pv-prev').addEventListener('click', () => gotoMark(PV.cur - 1));
$('#pv-open').addEventListener('click', () => PV.path && call('OpenPath', PV.path).catch(() => {}));
$('#pv-folder').addEventListener('click', () => PV.path && call('ShowInFolder', PV.path).catch(() => {}));
document.addEventListener('keydown', e => {
  if (currentView === 'search' && e.key === 'F3') { gotoMark(PV.cur + (e.shiftKey ? -1 : 1)); e.preventDefault(); }
});

/* ------------------------------------------------------------ 截图 */
$('#btn-shot').addEventListener('click', () => API().StartScreenshot());
on('shot:done', ev => {
  const box = $('#shot-result');
  if (ev.error) { toast(ev.error, true); return; }
  box.classList.remove('hidden');
  if (ev.action === 'copy') {
    box.innerHTML = `<span class="ok">✔</span><span class="grow">已复制 ${ev.width}×${ev.height} 截图到剪贴板，可直接在 Word 中粘贴（Ctrl+V）</span>`;
    toast('截图已复制到剪贴板');
  } else if (ev.action === 'save') {
    box.innerHTML = `<span class="ok">✔</span><span class="grow" title="${esc(ev.path)}">已保存：${esc(ev.path)}</span>
      <button class="btn small" data-open="${esc(ev.path)}">打开</button><button class="btn small" data-folder="${esc(ev.path)}">打开所在文件夹</button>`;
    toast('截图已保存');
  }
});
document.addEventListener('click', e => {
  const o = e.target.closest('[data-open]'), f = e.target.closest('[data-folder]');
  if (o) call('OpenPath', o.dataset.open).catch(() => {});
  if (f) call('ShowInFolder', f.dataset.folder).catch(() => {});
});

/* ------------------------------------------------------------ 超级复制 */
let clips = [], clipFocus = 0, selectMode = false, picked = [];
let clipTimer = null;
$('#clip-q').addEventListener('input', () => { clearTimeout(clipTimer); clipTimer = setTimeout(() => { clipFocus = 0; loadClips(); }, 150); });
on('clip:changed', () => { if (currentView === 'clipboard') loadClips(); });

async function loadClips() {
  try { clips = await API().ClipList($('#clip-q').value) || []; } catch (e) { toast(errMsg(e), true); clips = []; }
  picked = picked.filter(id => clips.some(c => c.id === id));
  renderClips();
}
function renderClips() {
  const list = $('#clip-list');
  list.classList.toggle('select-mode', selectMode);
  if (!clips.length) {
    list.innerHTML = `<div class="card empty"><div class="empty-icon">📋</div><div>${$('#clip-q').value ? '没有匹配的记录' : '还没有记录'}</div><div class="muted">复制任意文字或图片后会出现在这里</div></div>`;
    updatePickBtn();
    return;
  }
  if (clipFocus >= clips.length) clipFocus = clips.length - 1;
  list.innerHTML = clips.map((c, i) => {
    const body = c.kind === 'image'
      ? `<img class="clip-img" src="${c.thumb}" alt="">`
      : `<div class="clip-text">${esc(c.preview)}${c.chars > 300 ? '…' : ''}</div>`;
    const meta = c.kind === 'image' ? `图片 ${c.width}×${c.height}` : `${c.chars} 字`;
    const pi = picked.indexOf(c.id);
    return `<div class="clip-item${c.pinned ? ' pinned' : ''}${i === clipFocus ? ' focus' : ''}${pi >= 0 ? ' picked' : ''}" data-i="${i}">
      <input type="checkbox" class="pick" ${pi >= 0 ? 'checked' : ''} tabindex="-1">
      <div class="clip-main">${body}<div class="clip-meta"><span>${ago(c.created)}</span><span>${meta}</span>${c.pinned ? '<span class="pin-badge">已置顶</span>' : ''}${pi >= 0 ? `<span class="pin-badge">第 ${pi + 1} 个</span>` : ''}</div></div>
      <div class="clip-ops"><button class="icon-btn pin-btn" data-op="pin" title="${c.pinned ? '取消置顶' : '置顶'}">📌</button><button class="icon-btn" data-op="del" title="删除">✕</button></div>
    </div>`;
  }).join('');
  updatePickBtn();
}
function updatePickBtn() {
  const b = $('#clip-copy-sel');
  b.classList.toggle('hidden', !selectMode);
  b.textContent = `复制所选 (${picked.length})`;
  b.disabled = !picked.length;
  $('#clip-select-mode').textContent = selectMode ? '取消多选' : '多选';
}
function togglePick(c) {
  const i = picked.indexOf(c.id);
  if (i >= 0) picked.splice(i, 1); else picked.push(c.id);
  renderClips();
}
async function copyClip(c) {
  try {
    await API().ClipCopy(c.id);
    toast(c.kind === 'image' ? '图片已复制' : '已复制');
    if (popup) setTimeout(() => API().HideWindow(), 150);
  } catch (e) { toast(errMsg(e), true); }
}
$('#clip-list').addEventListener('click', async e => {
  const item = e.target.closest('.clip-item');
  if (!item) return;
  const c = clips[+item.dataset.i];
  clipFocus = +item.dataset.i;
  const op = e.target.closest('[data-op]');
  if (op) {
    e.stopPropagation();
    if (op.dataset.op === 'pin') await API().ClipPin(c.id, !c.pinned);
    if (op.dataset.op === 'del') await API().ClipDelete(c.id);
    loadClips();
    return;
  }
  if (selectMode || e.ctrlKey) {
    if (!selectMode) { selectMode = true; }
    togglePick(c);
    return;
  }
  copyClip(c);
});
$('#clip-select-mode').addEventListener('click', () => { selectMode = !selectMode; if (!selectMode) picked = []; renderClips(); });
$('#clip-copy-sel').addEventListener('click', async () => {
  if (!picked.length) return;
  try {
    const n = await API().ClipCopyMany(picked);
    toast(`已合并复制 ${n} 条文本（按选择顺序，换行分隔）`);
    picked = []; selectMode = false; loadClips();
    if (popup) setTimeout(() => API().HideWindow(), 200);
  } catch (e) { toast(errMsg(e), true); }
});
$('#clip-clear').addEventListener('click', async () => {
  if (!confirm('确定清空剪贴板历史吗？（置顶的记录会保留）')) return;
  await API().ClipClear(true);
  loadClips();
});
$('#clip-enabled').addEventListener('change', async e => {
  const onv = e.target.checked;
  try { await API().ClipSetEnabled(onv); } catch (err) { toast(errMsg(err), true); }
  $('.switch-label').textContent = onv ? '记录中' : '已暂停';
});
document.addEventListener('keydown', e => {
  if (currentView !== 'clipboard') return;
  if (e.key === 'Escape') {
    if (popup) API().HideWindow();
    else if (selectMode) { selectMode = false; picked = []; renderClips(); }
    return;
  }
  if (!clips.length) return;
  if (e.key === 'ArrowDown') { clipFocus = Math.min(clipFocus + 1, clips.length - 1); renderClips(); scrollFocus(); e.preventDefault(); }
  else if (e.key === 'ArrowUp') { clipFocus = Math.max(clipFocus - 1, 0); renderClips(); scrollFocus(); e.preventDefault(); }
  else if (e.key === 'Enter') {
    e.preventDefault();
    if (selectMode && picked.length) $('#clip-copy-sel').click(); else copyClip(clips[clipFocus]);
  } else if (e.key === ' ' && document.activeElement !== $('#clip-q')) {
    e.preventDefault(); selectMode = true; togglePick(clips[clipFocus]);
  }
});
function scrollFocus() { const el = $('.clip-item.focus'); if (el) el.scrollIntoView({ block: 'nearest' }); }

/* ------------------------------------------------------------ 格式转换 */
const C = { files: [], outMode: 'same', outDir: '', running: false };
function addConvFiles(paths) {
  let bad = 0;
  for (const p of paths || []) {
    if (!/\.(pdf|md|markdown|mdown|mkd)$/i.test(p)) { bad++; continue; }
    if (C.files.some(f => f.path === p)) continue;
    C.files.push({ path: p, status: 'wait', pct: 0, msg: '', output: '', error: '' });
  }
  if (bad) toast(`已忽略 ${bad} 个不支持的文件（仅支持 PDF 和 Markdown）`, true);
  renderConv();
}
function renderConv() {
  const list = $('#conv-list');
  if (!C.files.length) { list.innerHTML = '<div class="empty"><div class="muted">还没有添加文件</div></div>'; return; }
  list.innerHTML = C.files.map((f, i) => {
    const kind = /\.pdf$/i.test(f.path) ? 'PDF → Word' : 'Markdown → Word';
    let badge = '<span class="badge">等待</span>', sub = esc(f.path), extra = '';
    if (f.status === 'running') { badge = '<span class="badge run">转换中</span>'; sub = esc(f.msg || '处理中…'); extra = `<div class="bar"><i style="width:${f.pct}%"></i></div>`; }
    if (f.status === 'done') { badge = '<span class="badge ok">完成</span>'; sub = '已生成：' + esc(f.output); }
    if (f.status === 'error') { badge = '<span class="badge err">失败</span>'; sub = esc(f.error); }
    if (f.status === 'canceled') { badge = '<span class="badge">已取消</span>'; }
    const btns = f.status === 'done'
      ? `<button class="btn small primary" data-open="${esc(f.output)}">打开文件</button><button class="btn small" data-folder="${esc(f.output)}">所在文件夹</button>`
      : (C.running ? '' : `<button class="icon-btn" data-rm="${i}" title="移除">✕</button>`);
    return `<div class="conv-row"><div style="font-size:22px">${/\.pdf$/i.test(f.path) ? '📕' : '📝'}</div>
      <div class="conv-info"><div class="conv-name">${esc(baseName(f.path))} <span class="muted" style="font-weight:400;font-size:12px">${kind}</span></div>
      <div class="conv-sub ${f.status === 'error' ? 'err' : ''}" title="${esc(f.path)}">${sub}</div>${extra}</div>${badge}${btns}</div>`;
  }).join('');
}
$('#conv-list').addEventListener('click', e => {
  const rm = e.target.closest('[data-rm]');
  if (rm) { C.files.splice(+rm.dataset.rm, 1); renderConv(); }
});
$('#pick-files').addEventListener('click', async () => addConvFiles(await call('PickConvertFiles')));
$('#drop').addEventListener('click', async e => { if (e.target.id !== 'pick-files') addConvFiles(await call('PickConvertFiles')); });
function onFilesDropped(paths) {
  if (currentView === 'convert') { addConvFiles(paths); return; }
  if (currentView === 'search' && paths && paths.length) {
    // 拖入文件夹到搜索页：设为搜索范围
    S.folder = paths[0]; S.scope = 'folder';
    $$('#scope .seg-btn').forEach(x => x.classList.toggle('active', x.dataset.scope === 'folder'));
    $('#folder-path').textContent = S.folder; $('#folder-path').classList.remove('hidden');
    return;
  }
  showView('convert'); addConvFiles(paths);
}
// Wails 原生拖放：返回真实文件路径（WebView2 postMessageWithAdditionalObjects）
window.addEventListener('load', () => {
  if (window.runtime && window.runtime.OnFileDrop) window.runtime.OnFileDrop((x, y, paths) => onFilesDropped(paths), false);
});
['dragenter', 'dragover'].forEach(t => document.addEventListener(t, e => { e.preventDefault(); if (currentView === 'convert') $('#drop').classList.add('over'); }));
['dragleave', 'drop'].forEach(t => document.addEventListener(t, e => { e.preventDefault(); $('#drop').classList.remove('over'); }));

$$('#out-mode .seg-btn').forEach(b => b.addEventListener('click', async () => {
  if (b.dataset.out === 'dir') {
    const d = await call('PickOutputDir');
    if (!d) return;
    C.outDir = d;
  }
  C.outMode = b.dataset.out;
  $$('#out-mode .seg-btn').forEach(x => x.classList.toggle('active', x === b));
  $('#out-path').textContent = C.outDir;
  $('#out-path').classList.toggle('hidden', C.outMode !== 'dir');
}));
$('#out-path').addEventListener('click', async () => { const d = await call('PickOutputDir'); if (d) { C.outDir = d; $('#out-path').textContent = d; } });
$('#conv-clear').addEventListener('click', () => { if (!C.running) { C.files = []; renderConv(); } });
$('#conv-cancel').addEventListener('click', () => API().CancelConvert());
$('#conv-start').addEventListener('click', async () => {
  const todo = C.files.filter(f => f.status !== 'done');
  if (!todo.length) { toast(C.files.length ? '所有文件都已转换完成' : '请先添加 PDF 或 Markdown 文件', !C.files.length); return; }
  todo.forEach(f => { f.status = 'wait'; f.pct = 0; f.error = ''; });
  try {
    await call('StartConvert', todo.map(f => f.path), C.outMode === 'dir' ? C.outDir : '');
  } catch { return; }
  setConvRunning(true);
});
function setConvRunning(b) {
  C.running = b;
  $('#conv-cancel').classList.toggle('hidden', !b);
  $('#conv-start').disabled = b;
  $('#conv-start').textContent = b ? '转换中…' : '开始转换';
  renderConv();
}
on('convert:file', ev => {
  const f = C.files.find(x => x.path === ev.file);
  if (!f) return;
  Object.assign(f, { status: ev.status, pct: ev.pct || f.pct, msg: ev.msg || '', output: ev.output || f.output, error: ev.error || '' });
  if (ev.status === 'error' && ev.code === 'scanned') f.error = '⚠ ' + ev.error;
  renderConv();
});
on('convert:done', () => {
  setConvRunning(false);
  const ok = C.files.filter(f => f.status === 'done').length, bad = C.files.filter(f => f.status === 'error').length;
  toast(bad ? `转换结束：成功 ${ok} 个，失败 ${bad} 个` : `转换完成：${ok} 个文件`, bad > 0);
});

/* ------------------------------------------------------------ 录屏 */
const R = { mode: 'full', w: 0, h: 0, started: 0, timer: null, micLoaded: false };
const modeNotes = { full: '录制鼠标所在的整个显示器', region: '开始后拖动鼠标选择要录制的区域', preset: '开始后移动鼠标放置固定大小的录制框，单击确定' };
$$('#rec-mode .seg-btn').forEach(b => b.addEventListener('click', () => {
  R.mode = b.dataset.mode; R.w = +(b.dataset.w || 0); R.h = +(b.dataset.h || 0);
  $$('#rec-mode .seg-btn').forEach(x => x.classList.toggle('active', x === b));
  $('#rec-mode-note').textContent = modeNotes[R.mode];
}));
async function loadMics() {
  const sel = $('#rec-mic'), cur = sel.value;
  $('#rec-mic-refresh').disabled = true;
  try {
    const devs = await API().ListMicrophones();
    sel.innerHTML = '<option value="">不录制声音</option>' + devs.map(d => `<option value="${esc(d)}">${esc(d)}</option>`).join('');
    if (devs.includes(cur)) sel.value = cur;
    if (!devs.length) toast('没有检测到麦克风');
    R.micLoaded = true;
  } catch (e) { toast(errMsg(e), true); }
  $('#rec-mic-refresh').disabled = false;
}
$('#rec-mic-refresh').addEventListener('click', loadMics);
$('#rec-mic').addEventListener('focus', () => { if (!R.micLoaded) loadMics(); });
$('#rec-start').addEventListener('click', async () => {
  try {
    await API().StartRecording({ mode: R.mode, width: R.w, height: R.h, microphone: $('#rec-mic').value, cursor: $('#rec-cursor').checked, fps: +$('#rec-fps').value });
  } catch (e) { toast(errMsg(e), true); }
});
$('#rec-stop').addEventListener('click', () => API().StopRecording().catch(e => toast(errMsg(e), true)));
function setRecUI(rec, started) {
  $('#rec-start').classList.toggle('hidden', rec);
  $('#rec-live').classList.toggle('hidden', !rec);
  clearInterval(R.timer);
  if (rec) {
    R.started = started || Date.now();
    const tick = () => {
      const s = Math.floor((Date.now() - R.started) / 1000), p = n => String(n).padStart(2, '0');
      $('#rec-timer').textContent = `${p(Math.floor(s / 3600))}:${p(Math.floor(s / 60) % 60)}:${p(s % 60)}`;
    };
    tick(); R.timer = setInterval(tick, 500);
  }
}
on('rec:state', ev => {
  setRecUI(ev.recording, ev.started);
  if (ev.error) toast(ev.error, true);
});
on('rec:saved', ev => {
  setRecUI(false);
  const box = $('#rec-result');
  box.classList.remove('hidden');
  if (ev.error) {
    box.innerHTML = `<span class="err">✕</span><span class="grow" style="white-space:normal">${esc(ev.error)}</span>`;
    toast(ev.error, true);
    return;
  }
  box.innerHTML = `<span class="ok">✔</span><span class="grow" title="${esc(ev.output)}">已保存：${esc(ev.output)}</span>
    <button class="btn small primary" data-open="${esc(ev.output)}">播放</button><button class="btn small" data-folder="${esc(ev.output)}">打开所在文件夹</button>`;
  toast('录屏已保存到桌面');
});

/* ------------------------------------------------------------ 设置 */
async function loadInfo() {
  let info;
  try { info = await API().GetInfo(); } catch (e) { toast(errMsg(e), true); return; }
  $('#ver').textContent = 'v' + info.version;
  $('#hk-list').innerHTML = info.hotkeys.map(h => `<div class="hk-row"><span>${esc(h.action)}</span><span>${h.keys.split('+').map(k => `<kbd>${esc(k)}</kbd>`).join(' + ')}
    ${h.ok ? '<span class="ok" style="margin-left:8px">已启用</span>' : '<span class="err" style="margin-left:8px">被其他程序占用</span>'}</span></div>`).join('');
  $('#set-autostart').checked = info.autostart;
  $('#clip-enabled').checked = info.clipOn;
  $('.switch-label').textContent = info.clipOn ? '记录中' : '已暂停';
  $('#comp-list').innerHTML =
    `<div class="comp-row"><span>格式转换组件（内置 Python + pdf2docx）</span>${info.hasPython ? '<span class="ok">正常</span>' : '<span class="err">缺失，请重新安装</span>'}</div>` +
    `<div class="comp-row"><span>转换组件自检（首次约需数秒）</span><button class="btn small" id="comp-test">检测</button></div>` +
    `<div class="comp-row"><span>录屏组件（内置 FFmpeg）</span>${info.hasFfmpeg ? '<span class="ok">正常</span>' : '<span class="err">缺失，请重新安装</span>'}</div>`;
  $('#init-errors').innerHTML = (info.initErrors || []).map(e => `<div class="pv-warn">${esc(e)}</div>`).join('');
  if ((info.initErrors || []).length && !loadInfo.warned) { loadInfo.warned = true; toast(info.initErrors.join('\n'), true); }
  const failed = info.hotkeys.filter(h => !h.ok);
  if (failed.length && !loadInfo.hkWarned) { loadInfo.hkWarned = true; toast('以下快捷键被其他程序占用：' + failed.map(h => h.keys).join('、'), true); }
}
$('#set-autostart').addEventListener('change', async e => {
  try { await API().SetAutostart(e.target.checked); toast(e.target.checked ? '已设置开机自启' : '已取消开机自启'); }
  catch (err) { toast(errMsg(err), true); e.target.checked = !e.target.checked; }
});
document.addEventListener('click', async e => {
  if (e.target.id !== 'comp-test') return;
  e.target.disabled = true; e.target.textContent = '检测中…';
  try { await API().TestConverter(); toast('转换组件工作正常'); } catch (err) { toast(errMsg(err), true); }
  e.target.disabled = false; e.target.textContent = '检测';
});
$('#set-datadir').addEventListener('click', () => call('OpenDataDir').catch(() => {}));
$('#set-quit').addEventListener('click', () => { if (confirm('退出后快捷键和剪贴板记录将停止，确定退出吗？')) API().Quit(); });

/* ------------------------------------------------------------ init */
document.addEventListener('contextmenu', e => { if (!e.target.closest('input, textarea, pre')) e.preventDefault(); });
window.addEventListener('load', () => {
  loadInfo();
  API().IsRecording().then(r => r && setRecUI(true)).catch(() => {});
  $('#kw').focus();
});
