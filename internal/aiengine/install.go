package aiengine

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Progress 下载进度。
type Progress struct {
	Component string `json:"component"`
	File      string `json:"file"`
	Done      int64  `json:"done"`  // 本组件已下载字节
	Total     int64  `json:"total"` // 本组件总字节
	Speed     int64  `json:"speed"` // 字节/秒
	Stage     string `json:"stage"` // download | verify | extract | done
	Host      string `json:"host"`
}

// Installed 组件安装记录（写在组件目录的 .installed.json）。
type Installed struct {
	ID          string `json:"id"`
	Version     string `json:"version"`
	Platform    string `json:"platform"`
	InstalledAt string `json:"installedAt"`
}

// Manager 管理 <root> 下的组件。
type Manager struct {
	Root   string
	Client *http.Client
	// MinSpeed 低于该速度（字节/秒）持续 StallWindow 时换下一个下载源。
	MinSpeed    int64
	StallWindow time.Duration

	mu     sync.Mutex
	probed map[string]time.Duration
}

// NewManager 创建组件管理器。
func NewManager(root string) *Manager {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 30 * time.Second
	tr.DialContext = (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	return &Manager{Root: root, Client: &http.Client{Transport: tr}, MinSpeed: 20 << 10, StallWindow: 30 * time.Second,
		probed: map[string]time.Duration{}}
}

// Dir 组件目录。
func (m *Manager) Dir(c *Component) string { return filepath.Join(m.Root, c.Kind, c.ID) }

func (m *Manager) downloadsDir() string { return filepath.Join(m.Root, ".downloads") }

// IsInstalled 组件是否已安装。
func (m *Manager) IsInstalled(c *Component) bool {
	_, err := os.Stat(filepath.Join(m.Dir(c), ".installed.json"))
	return err == nil
}

// InstalledInfo 读取安装记录。
func (m *Manager) InstalledInfo(c *Component) (*Installed, bool) {
	b, err := os.ReadFile(filepath.Join(m.Dir(c), ".installed.json"))
	if err != nil {
		return nil, false
	}
	var in Installed
	if json.Unmarshal(b, &in) != nil {
		return nil, false
	}
	return &in, true
}

// DiskSize 组件占用的磁盘空间。
func (m *Manager) DiskSize(c *Component) int64 {
	var n int64
	_ = filepath.Walk(m.Dir(c), func(_ string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			n += fi.Size()
		}
		return nil
	})
	return n
}

// Remove 删除组件（以及残留的未完成下载）。
func (m *Manager) Remove(c *Component) error {
	if err := os.RemoveAll(m.Dir(c)); err != nil {
		return fmt.Errorf("删除失败（文件可能正在使用，请先关闭相关功能）：%w", err)
	}
	for _, f := range c.Files {
		for _, x := range f {
			os.Remove(filepath.Join(m.downloadsDir(), x.SHA256+".part"))
		}
	}
	return nil
}

// Install 下载、校验并解压组件。已安装时直接返回。支持断点续传（.downloads/<sha256>.part）。
func (m *Manager) Install(ctx context.Context, c *Component, progress func(Progress)) error {
	if m.IsInstalled(c) {
		return nil
	}
	plat := Platform()
	files := c.FilesFor(plat)
	if len(files) == 0 {
		return fmt.Errorf("“%s”暂不支持当前系统（%s）", c.Name, plat)
	}
	if err := os.MkdirAll(m.downloadsDir(), 0o755); err != nil {
		return err
	}
	total := c.TotalSize(plat)
	if err := checkFreeSpace(m.Root, total*2+(64<<20)); err != nil {
		return err
	}
	var base int64
	emit := func(p Progress) {
		if progress != nil {
			p.Component = c.ID
			p.Total = total
			progress(p)
		}
	}
	paths := make([]string, len(files))
	for i, f := range files {
		p, err := m.fetch(ctx, f, func(done, speed int64, host string) {
			emit(Progress{File: f.Name, Done: base + done, Speed: speed, Stage: "download", Host: host})
		})
		if err != nil {
			return err
		}
		paths[i] = p
		base += f.Size
	}
	emit(Progress{Done: total, Stage: "extract"})
	final := m.Dir(c)
	tmp := final + ".tmp"
	os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	for i, f := range files {
		if err := ctx.Err(); err != nil {
			os.RemoveAll(tmp)
			return err
		}
		var err error
		switch f.Extract {
		case "zip":
			err = unzipTo(paths[i], tmp)
		case "tgz":
			err = untgzTo(paths[i], tmp)
		default:
			err = moveFile(paths[i], filepath.Join(tmp, filepath.Base(f.Name)))
		}
		if err != nil {
			os.RemoveAll(tmp)
			return fmt.Errorf("解压 %s 失败：%w", f.Name, err)
		}
	}
	info, _ := json.MarshalIndent(Installed{ID: c.ID, Version: c.Version, Platform: plat,
		InstalledAt: time.Now().Format(time.RFC3339)}, "", " ")
	if err := os.WriteFile(filepath.Join(tmp, ".installed.json"), info, 0o644); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	os.RemoveAll(final)
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	for i, f := range files {
		if f.Extract == "zip" || f.Extract == "tgz" {
			os.Remove(paths[i])
		}
	}
	emit(Progress{Done: total, Stage: "done"})
	return nil
}

// fetch 下载单个文件到 .downloads/<sha256>.part 并校验，返回完成后的路径。
func (m *Manager) fetch(ctx context.Context, f File, progress func(done, speed int64, host string)) (string, error) {
	done := filepath.Join(m.downloadsDir(), f.SHA256+filepath.Ext(f.Name))
	if st, err := os.Stat(done); err == nil && st.Size() == f.Size {
		return done, nil
	}
	part := filepath.Join(m.downloadsDir(), f.SHA256+".part")
	urls := m.orderURLs(ctx, f.URLs)
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		for _, u := range urls {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			err := m.fetchFrom(ctx, u, part, f.Size, progress)
			if err == nil {
				if err := verify(part, f.SHA256); err != nil {
					os.Remove(part)
					lastErr = err
					continue
				}
				if err := os.Rename(part, done); err != nil {
					return "", err
				}
				return done, nil
			}
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			lastErr = fmt.Errorf("%s：%w", hostOf(u), err)
		}
	}
	return "", fmt.Errorf("下载 %s 失败（已尝试全部 %d 个下载源）：%v", f.Name, len(urls), lastErr)
}

var errStalled = errors.New("下载速度过慢")

func (m *Manager) fetchFrom(ctx context.Context, u, part string, size int64, progress func(done, speed int64, host string)) error {
	var have int64
	if st, err := os.Stat(part); err == nil {
		have = st.Size()
		if have > size {
			os.Remove(part)
			have = 0
		}
	}
	if have == size {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "DesktopToolbox/0.2")
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
	}
	resp, err := m.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	flags := os.O_CREATE | os.O_WRONLY
	switch {
	case resp.StatusCode == http.StatusPartialContent && have > 0:
		flags |= os.O_APPEND
	case resp.StatusCode == http.StatusOK:
		have = 0
		flags |= os.O_TRUNC
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		os.Remove(part)
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	default:
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	out, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	host := hostOf(u)
	buf := make([]byte, 256<<10)
	start := time.Now()
	winStart, winBytes := time.Now(), int64(0)
	lastEmit := time.Time{}
	got := int64(0)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := out.Write(buf[:n]); err != nil {
				return fmt.Errorf("写入失败（磁盘空间不足？）：%w", err)
			}
			got += int64(n)
			winBytes += int64(n)
		}
		now := time.Now()
		if now.Sub(lastEmit) > 250*time.Millisecond || rerr == io.EOF {
			el := now.Sub(start).Seconds()
			speed := int64(0)
			if el > 0 {
				speed = int64(float64(got) / el)
			}
			progress(have+got, speed, host)
			lastEmit = now
		}
		if now.Sub(winStart) > m.StallWindow {
			if winBytes < m.MinSpeed*int64(m.StallWindow/time.Second) {
				return errStalled
			}
			winStart, winBytes = now, 0
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if have+got != size {
		return fmt.Errorf("文件大小不符（%d / %d）", have+got, size)
	}
	return nil
}

// orderURLs 按主机的响应速度排序（每个主机只探测一次）：国内网络下 hf-mirror / ModelScope / 阿里云镜像通常更快。
func (m *Manager) orderURLs(ctx context.Context, urls []string) []string {
	if len(urls) < 2 {
		return urls
	}
	type hv struct {
		u string
		d time.Duration
		i int
	}
	var wg sync.WaitGroup
	res := make([]hv, len(urls))
	for i, u := range urls {
		res[i] = hv{u, 0, i}
		h := hostOf(u)
		m.mu.Lock()
		d, ok := m.probed[h]
		m.mu.Unlock()
		if ok {
			res[i].d = d
			continue
		}
		wg.Add(1)
		go func(i int, u, h string) {
			defer wg.Done()
			d := probe(ctx, m.Client, u)
			m.mu.Lock()
			m.probed[h] = d
			m.mu.Unlock()
			res[i].d = d
		}(i, u, h)
	}
	wg.Wait()
	sort.SliceStable(res, func(a, b int) bool {
		// 相差不到 300ms 时保持清单顺序（主站优先）
		if res[a].d-res[b].d > 300*time.Millisecond || res[b].d-res[a].d > 300*time.Millisecond {
			return res[a].d < res[b].d
		}
		return res[a].i < res[b].i
	})
	out := make([]string, len(res))
	for i, r := range res {
		out[i] = r.u
	}
	return out
}

func probe(ctx context.Context, c *http.Client, u string) time.Duration {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	pu, err := url.Parse(u)
	if err != nil {
		return time.Hour
	}
	req, _ := http.NewRequestWithContext(ctx, "HEAD", pu.Scheme+"://"+pu.Host+"/", nil)
	t := time.Now()
	resp, err := c.Do(req)
	if err != nil {
		return time.Hour
	}
	resp.Body.Close()
	return time.Since(t)
}

func hostOf(u string) string {
	if pu, err := url.Parse(u); err == nil {
		return pu.Host
	}
	return u
}

func verify(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
		return fmt.Errorf("SHA256 校验失败（文件损坏或被篡改）")
	}
	return nil
}

func safeJoin(dir, name string) (string, error) {
	name = filepath.FromSlash(name)
	p := filepath.Join(dir, name)
	if !strings.HasPrefix(p, filepath.Clean(dir)+string(os.PathSeparator)) {
		return "", fmt.Errorf("非法路径：%s", name)
	}
	return p, nil
}

func unzipTo(src, dir string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		// 轮子的 .data/scripts 等不需要
		if i := strings.Index(f.Name, ".data/"); i > 0 && !strings.Contains(f.Name[:i], "/") {
			continue
		}
		p, err := safeJoin(dir, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(p, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		mode := f.Mode().Perm() | 0o600
		if mode&0o111 == 0 && (strings.HasSuffix(p, ".so") || !strings.Contains(filepath.Base(p), ".")) {
			mode |= 0o755
		}
		out, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// untgzTo 解压 .tar.gz，去掉顶层目录（llama.cpp 的 llama-bXXXX/）。保留符号链接（Linux .so 版本链接）。
func untgzTo(src, dir string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(h.Name, "./")
		if i := strings.Index(name, "/"); i >= 0 {
			name = name[i+1:]
		} else {
			continue
		}
		if name == "" {
			continue
		}
		p, err := safeJoin(dir, name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(p, 0o755)
		case tar.TypeSymlink:
			if strings.Contains(h.Linkname, "..") || filepath.IsAbs(h.Linkname) {
				continue
			}
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.Remove(p)
			if err := os.Symlink(h.Linkname, p); err != nil {
				return err
			}
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(p), 0o755)
			out, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode).Perm()|0o600)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			out.Close()
			if err != nil {
				return err
			}
		}
	}
}

func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	in.Close()
	return os.Remove(src)
}
