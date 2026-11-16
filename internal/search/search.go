// Package search 实现并发的文件名/文件内容搜索。纯 Go，可在 Linux 上测试。
package search

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"desktoptoolbox/internal/extract"
)

// Options 搜索参数。
type Options struct {
	Roots       []string // 搜索起点（盘符根目录或文件夹）
	Keyword     string   // 关键词，空格分隔多个词时须全部命中（文件名）
	Content     bool     // 同时搜索文件内容
	Exts        []string // 仅这些扩展名（小写含点），空为全部
	MaxResults  int      // 结果上限
	MaxFileSize int64    // 内容搜索单文件上限
	Workers     int      // 目录遍历并发数
}

// Result 单条结果。
type Result struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Dir     string `json:"dir"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"modTime"` // unix 毫秒
	Match   string `json:"match"`   // name | content
	Snippet string `json:"snippet"`
}

// Stats 搜索统计。
type Stats struct {
	Dirs      int64 `json:"dirs"`
	Files     int64 `json:"files"`
	Scanned   int64 `json:"scanned"` // 内容扫描过的文件数
	Results   int64 `json:"results"`
	Truncated bool  `json:"truncated"`
	Canceled  bool  `json:"canceled"`
}

var skipDirNames = map[string]bool{
	"windows": true, "$recycle.bin": true, "system volume information": true, "$windows.~bt": true,
	"$windows.~ws": true, "$winreagent": true, "recovery": true, "config.msi": true, "msocache": true,
	"node_modules": true, ".git": true, ".svn": true, ".hg": true, "__pycache__": true,
	"$sysreset": true, "perflogs": true, "windowsapps": true, "winsxs": true,
}

// SkipDir 判断是否跳过该目录（系统/隐藏/依赖目录）。
func SkipDir(name string, info fs.FileInfo) bool {
	l := strings.ToLower(name)
	if skipDirNames[l] {
		return true
	}
	if strings.HasPrefix(l, ".") && l != "." && l != ".." {
		return true
	}
	if info != nil && isHiddenOrSystem(info) {
		return true
	}
	return false
}

// Keywords 把输入拆分为小写关键词。
func Keywords(q string) []string {
	var out []string
	for _, f := range strings.Fields(strings.ToLower(q)) {
		out = append(out, f)
	}
	return out
}

// NameMatches 文件名是否包含全部关键词（不区分大小写）。
func NameMatches(name string, kws []string) bool {
	if len(kws) == 0 {
		return false
	}
	l := strings.ToLower(name)
	for _, k := range kws {
		if !strings.Contains(l, k) {
			return false
		}
	}
	return true
}

// Snippet 返回关键词附近的一段文字；找不到返回空串。
func Snippet(text, kw string, radius int) string {
	if kw == "" {
		return ""
	}
	idx := IndexFold(text, kw)
	if idx < 0 {
		return ""
	}
	start := idx
	for i := 0; i < radius && start > 0; i++ {
		_, sz := utf8.DecodeLastRuneInString(text[:start])
		start -= sz
	}
	end := idx + len(kw)
	if end > len(text) {
		end = len(text)
	}
	for i := 0; i < radius && end < len(text); i++ {
		_, sz := utf8.DecodeRuneInString(text[end:])
		end += sz
	}
	s := strings.Join(strings.Fields(text[start:end]), " ")
	if start > 0 {
		s = "…" + s
	}
	if end < len(text) {
		s += "…"
	}
	return s
}

// IndexFold 不区分大小写查找子串，返回 text 中的字节偏移。
func IndexFold(text, sub string) int {
	if sub == "" {
		return 0
	}
	if i := strings.Index(text, sub); i >= 0 {
		return i
	}
	ls := strings.ToLower(sub)
	// 快速路径：ASCII 文本时小写不改变长度
	lt := strings.ToLower(text)
	if len(lt) == len(text) {
		return strings.Index(lt, ls)
	}
	// 慢速路径：逐字符比较
	for i := range text {
		if hasPrefixFold(text[i:], sub) {
			return i
		}
	}
	return -1
}

func hasPrefixFold(s, prefix string) bool {
	for _, pr := range prefix {
		if s == "" {
			return false
		}
		r, sz := utf8.DecodeRuneInString(s)
		if !strings.EqualFold(string(r), string(pr)) {
			return false
		}
		s = s[sz:]
	}
	return true
}

// ContainsFold 不区分大小写包含。
func ContainsFold(text, sub string) bool { return IndexFold(text, sub) >= 0 }

// dirQueue 无界目录队列，避免递归 goroutine 爆炸。
type dirQueue struct {
	mu      sync.Mutex
	cond    *sync.Cond
	items   []string
	pending int
	closed  bool
}

func newDirQueue() *dirQueue {
	q := &dirQueue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *dirQueue) push(dirs ...string) {
	q.mu.Lock()
	q.items = append(q.items, dirs...)
	q.pending += len(dirs)
	q.mu.Unlock()
	q.cond.Broadcast()
}

func (q *dirQueue) pop() (string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 && q.pending > 0 && !q.closed {
		q.cond.Wait()
	}
	if q.closed || len(q.items) == 0 {
		return "", false
	}
	n := len(q.items) - 1
	d := q.items[n]
	q.items = q.items[:n]
	return d, true
}

func (q *dirQueue) done() {
	q.mu.Lock()
	q.pending--
	q.mu.Unlock()
	q.cond.Broadcast()
}

func (q *dirQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cond.Broadcast()
}

// Run 执行搜索；每个结果调用 emit（可能被多个 goroutine 并发调用）。ctx 取消时尽快返回。
func Run(ctx context.Context, opt Options, emit func(Result)) Stats {
	var st Stats
	kws := Keywords(opt.Keyword)
	phrase := strings.TrimSpace(opt.Keyword)
	if len(kws) == 0 {
		return st
	}
	if opt.MaxResults <= 0 {
		opt.MaxResults = 5000
	}
	if opt.MaxFileSize <= 0 {
		opt.MaxFileSize = extract.DefaultMaxFileSize
	}
	if opt.Workers <= 0 {
		opt.Workers = runtime.NumCPU() * 2
		if opt.Workers < 4 {
			opt.Workers = 4
		}
		if opt.Workers > 16 {
			opt.Workers = 16
		}
	}
	exts := map[string]bool{}
	for _, e := range opt.Exts {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if !strings.HasPrefix(e, ".") {
			e = "." + e
		}
		exts[e] = true
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var results int64
	var emitMu sync.Mutex
	seen := map[string]bool{}
	send := func(r Result) {
		emitMu.Lock()
		defer emitMu.Unlock()
		if seen[r.Path] || ctx.Err() != nil {
			return
		}
		if atomic.LoadInt64(&results) >= int64(opt.MaxResults) {
			st.Truncated = true
			cancel()
			return
		}
		seen[r.Path] = true
		atomic.AddInt64(&results, 1)
		emit(r)
	}

	contentCh := make(chan string, 256)
	var contentWG sync.WaitGroup
	if opt.Content {
		n := runtime.NumCPU()
		if n < 2 {
			n = 2
		}
		if n > 6 {
			n = 6
		}
		for i := 0; i < n; i++ {
			contentWG.Add(1)
			go func() {
				defer contentWG.Done()
				for p := range contentCh {
					if ctx.Err() != nil {
						continue // 排空
					}
					txt, err := extract.Text(p, opt.MaxFileSize)
					atomic.AddInt64(&st.Scanned, 1)
					if err != nil || txt == "" {
						continue
					}
					if ContainsFold(txt, phrase) {
						if r, ok := makeResult(p, "content"); ok {
							r.Snippet = Snippet(txt, phrase, 30)
							send(r)
						}
					}
				}
			}()
		}
	}

	q := newDirQueue()
	for _, r := range opt.Roots {
		if r == "" {
			continue
		}
		if fi, err := os.Stat(r); err == nil && fi.IsDir() {
			q.push(r)
		}
	}
	var wg sync.WaitGroup
	go func() {
		<-ctx.Done()
		q.close()
	}()
	for i := 0; i < opt.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				dir, ok := q.pop()
				if !ok {
					return
				}
				subs := scanDir(ctx, dir, kws, exts, opt, &st, send, contentCh)
				if len(subs) > 0 {
					q.push(subs...)
				}
				q.done()
			}
		}()
	}
	wg.Wait()
	close(contentCh)
	contentWG.Wait()
	st.Results = atomic.LoadInt64(&results)
	if ctx.Err() != nil && !st.Truncated {
		st.Canceled = true
	}
	return st
}

func scanDir(ctx context.Context, dir string, kws []string, exts map[string]bool, opt Options, st *Stats,
	send func(Result), contentCh chan<- string) []string {
	if ctx.Err() != nil {
		return nil
	}
	atomic.AddInt64(&st.Dirs, 1)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var subs []string
	for _, e := range entries {
		if ctx.Err() != nil {
			return nil
		}
		name := e.Name()
		full := filepath.Join(dir, name)
		if e.IsDir() {
			info, _ := e.Info()
			if SkipDir(name, info) {
				continue
			}
			pkg := isPackageDir(name) // macOS .app 等：可搜到名称，但不进入内部
			if !pkg {
				subs = append(subs, full)
			}
			if (len(exts) == 0 || (pkg && exts[strings.ToLower(filepath.Ext(name))])) && NameMatches(name, kws) {
				if r, ok := makeResultFromEntry(full, e, "name"); ok {
					send(r)
				}
			}
			continue
		}
		if !e.Type().IsRegular() {
			continue
		}
		atomic.AddInt64(&st.Files, 1)
		ext := strings.ToLower(filepath.Ext(name))
		if len(exts) > 0 && !exts[ext] {
			continue
		}
		if NameMatches(name, kws) {
			if r, ok := makeResultFromEntry(full, e, "name"); ok {
				send(r)
			}
			continue
		}
		if opt.Content && extract.Supported(name) {
			if info, err := e.Info(); err == nil && info.Size() > 0 && info.Size() <= opt.MaxFileSize {
				select {
				case contentCh <- full:
				case <-ctx.Done():
					return nil
				}
			}
		}
	}
	return subs
}

func makeResultFromEntry(full string, e fs.DirEntry, match string) (Result, bool) {
	info, err := e.Info()
	if err != nil {
		return Result{}, false
	}
	return fromInfo(full, info, match), true
}

func makeResult(full, match string) (Result, bool) {
	info, err := os.Stat(full)
	if err != nil {
		return Result{}, false
	}
	return fromInfo(full, info, match), true
}

func fromInfo(full string, info fs.FileInfo, match string) Result {
	return Result{
		Path:    full,
		Name:    info.Name(),
		Dir:     filepath.Dir(full),
		IsDir:   info.IsDir(),
		Size:    info.Size(),
		ModTime: info.ModTime().UnixMilli(),
		Match:   match,
	}
}
