package search

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// 本文件实现 macOS 的“快速文件名搜索”：调用 Spotlight 命令行 mdfind 查询索引，
// 结果再用与实时遍历相同的规则过滤（全部关键词命中、扩展名、跳过隐藏/系统/依赖目录与包内部）。
// 纯 Go + os/exec，可在 Linux 上用假的 mdfind 脚本测试。

// SpotlightQuery 生成 mdfind 查询：每个关键词都要出现在文件名中（不区分大小写/变音符号）。
func SpotlightQuery(kws []string) string {
	var parts []string
	for _, k := range kws {
		var b strings.Builder
		for _, r := range k {
			switch r {
			case '\\', '"', '*', '?', '\'':
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
		parts = append(parts, `kMDItemFSName == "*`+b.String()+`*"cd`)
	}
	return strings.Join(parts, " && ")
}

// macSkipPrefixes 不应出现在结果中的系统位置。
var macSkipPrefixes = []string{"/System/", "/private/", "/Library/Caches/", "/usr/", "/bin/", "/sbin/", "/cores/", "/dev/", "/opt/homebrew/"}

// ExcludedMacPath 判断 Spotlight 返回的路径是否应被排除（与实时遍历的跳过规则保持一致）。
func ExcludedMacPath(path, home string) bool {
	for _, p := range macSkipPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	if home != "" && (strings.HasPrefix(path, home+"/Library/") || strings.HasPrefix(path, home+"/.Trash/")) {
		return true
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, c := range parts {
		if c == "" {
			continue
		}
		if strings.HasPrefix(c, ".") || skipDirNames[strings.ToLower(c)] {
			return true
		}
		// 位于 .app 等包内部的文件（包本身可以作为结果）
		if i < len(parts)-1 {
			if j := strings.LastIndexByte(c, '.'); j > 0 && macPackageExt(strings.ToLower(c[j:])) {
				return true
			}
		}
	}
	return false
}

func macPackageExt(ext string) bool {
	switch ext {
	case ".app", ".framework", ".bundle", ".plugin", ".kext", ".appex", ".photoslibrary", ".musiclibrary",
		".xcarchive", ".xcodeproj", ".xcworkspace", ".rtfd", ".pages", ".numbers", ".key", ".fcpbundle",
		".imovielibrary", ".sparsebundle", ".pkg", ".mpkg", ".logicx", ".band", ".tvlibrary", ".photolibrary", ".playground":
		return true
	}
	return false
}

// ErrNoIndex mdfind 不可用或执行失败（调用方应回退到实时遍历）。
var ErrNoIndex = errors.New("spotlight index unavailable")

// RunMdfind 使用 Spotlight 按文件名搜索。只有在一条结果都没产生且命令失败时返回 ErrNoIndex。
func RunMdfind(ctx context.Context, mdfind, home string, opt Options, emit func(Result)) (Stats, error) {
	var st Stats
	kws := Keywords(opt.Keyword)
	if len(kws) == 0 {
		return st, nil
	}
	if opt.MaxResults <= 0 {
		opt.MaxResults = 5000
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
	cmd := exec.CommandContext(ctx, mdfind, "-0", SpotlightQuery(kws))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return st, ErrNoIndex
	}
	if err := cmd.Start(); err != nil {
		return st, ErrNoIndex
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	sc.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if i := bytes.IndexByte(data, 0); i >= 0 {
			return i + 1, data[:i], nil
		}
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	})
	seen := map[string]bool{}
	var results int64
	for sc.Scan() {
		if ctx.Err() != nil {
			break
		}
		p := strings.TrimSpace(sc.Text())
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		atomic.AddInt64(&st.Files, 1)
		name := filepath.Base(p)
		if !NameMatches(name, kws) || ExcludedMacPath(p, home) {
			continue
		}
		if len(exts) > 0 && !exts[strings.ToLower(filepath.Ext(name))] {
			continue
		}
		r, ok := makeResult(p, "name")
		if !ok {
			continue
		}
		if r.IsDir && len(exts) > 0 && !macPackageExt(strings.ToLower(filepath.Ext(name))) {
			continue
		}
		if results >= int64(opt.MaxResults) {
			st.Truncated = true
			cancel()
			break
		}
		results++
		emit(r)
	}
	werr := cmd.Wait()
	st.Results = results
	if ctx.Err() != nil && !st.Truncated {
		st.Canceled = true
		return st, nil
	}
	if werr != nil && results == 0 && !st.Truncated {
		return st, ErrNoIndex
	}
	return st, nil
}
