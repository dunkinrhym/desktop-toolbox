// Package cliphist 剪贴板历史存储（文本与图片），持久化到数据目录。纯 Go。
package cliphist

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	DefaultLimit = 100
	MaxTextBytes = 2 << 20 // 超过 2MB 的文本不记录
	thumbMax     = 240
)

// Item 一条历史记录。
type Item struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"` // text | image
	Text    string `json:"text,omitempty"`
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
	Pinned  bool   `json:"pinned"`
	Created int64  `json:"created"` // unix 毫秒（最近一次复制时间）
	Hash    string `json:"hash"`
}

// View 返回给前端的条目（图片带缩略图 dataURL）。
type View struct {
	Item
	Thumb   string `json:"thumb,omitempty"`
	Preview string `json:"preview,omitempty"` // 文本前 300 字
	Chars   int    `json:"chars,omitempty"`
}

// Store 历史存储，并发安全。
type Store struct {
	mu     sync.Mutex
	dir    string
	limit  int
	items  []*Item
	thumbs map[string]string
	now    func() time.Time
}

// Open 打开（或创建）数据目录下的历史。
func Open(dir string, limit int) (*Store, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	s := &Store{dir: dir, limit: limit, thumbs: map[string]string{}, now: time.Now}
	if err := os.MkdirAll(filepath.Join(dir, "images"), 0o755); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(s.indexPath())
	if err == nil {
		var items []*Item
		if json.Unmarshal(b, &items) == nil {
			for _, it := range items {
				if it.Kind == "image" {
					if _, err := os.Stat(s.imagePath(it.ID)); err != nil {
						continue
					}
				}
				s.items = append(s.items, it)
			}
		}
	}
	return s, nil
}

func (s *Store) indexPath() string          { return filepath.Join(s.dir, "history.json") }
func (s *Store) imagePath(id string) string { return filepath.Join(s.dir, "images", id+".png") }
func (s *Store) thumbPath(id string) string {
	return filepath.Join(s.dir, "images", id+".thumb.png")
}

func hashOf(kind string, b []byte) string {
	h := sha1.New()
	h.Write([]byte(kind))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

func newID(t time.Time, hash string) string {
	return t.Format("20060102150405.000") + "-" + hash[:8]
}

// AddText 记录一段文本。返回是否有变化。
func (s *Store) AddText(text string) bool {
	if strings.TrimSpace(text) == "" || len(text) > MaxTextBytes {
		return false
	}
	hash := hashOf("text", []byte(text))
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bumpLocked(hash) {
		return true
	}
	now := s.now()
	s.items = append(s.items, &Item{ID: newID(now, hash), Kind: "text", Text: text, Created: now.UnixMilli(), Hash: hash})
	s.trimLocked()
	s.saveLocked()
	return true
}

// AddImage 记录一张 PNG 图片。
func (s *Store) AddImage(pngData []byte) (bool, error) {
	if len(pngData) == 0 {
		return false, errors.New("空图片")
	}
	hash := hashOf("image", pngData)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bumpLocked(hash) {
		return true, nil
	}
	img, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		return false, err
	}
	now := s.now()
	id := newID(now, hash)
	if err := os.WriteFile(s.imagePath(id), pngData, 0o644); err != nil {
		return false, err
	}
	var tb bytes.Buffer
	if err := png.Encode(&tb, Thumbnail(img, thumbMax)); err == nil {
		_ = os.WriteFile(s.thumbPath(id), tb.Bytes(), 0o644)
	}
	b := img.Bounds()
	s.items = append(s.items, &Item{ID: id, Kind: "image", Width: b.Dx(), Height: b.Dy(), Created: now.UnixMilli(), Hash: hash})
	s.trimLocked()
	s.saveLocked()
	return true, nil
}

// bumpLocked 若已存在相同内容，则更新时间移到最前。
func (s *Store) bumpLocked(hash string) bool {
	for _, it := range s.items {
		if it.Hash == hash {
			it.Created = s.now().UnixMilli()
			s.saveLocked()
			return true
		}
	}
	return false
}

// trimLocked 超出上限时删除最旧的未置顶条目。
func (s *Store) trimLocked() {
	for len(s.items) > s.limit {
		oldest := -1
		for i, it := range s.items {
			if it.Pinned {
				continue
			}
			if oldest < 0 || it.Created < s.items[oldest].Created {
				oldest = i
			}
		}
		if oldest < 0 {
			return
		}
		s.removeAtLocked(oldest)
	}
}

func (s *Store) removeAtLocked(i int) {
	it := s.items[i]
	if it.Kind == "image" {
		os.Remove(s.imagePath(it.ID))
		os.Remove(s.thumbPath(it.ID))
		delete(s.thumbs, it.ID)
	}
	s.items = append(s.items[:i], s.items[i+1:]...)
}

func (s *Store) saveLocked() {
	b, err := json.Marshal(s.items)
	if err != nil {
		return
	}
	tmp := s.indexPath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		if err := os.Rename(tmp, s.indexPath()); err != nil {
			_ = os.WriteFile(s.indexPath(), b, 0o600)
			os.Remove(tmp)
		}
	}
}

// List 按置顶优先、时间倒序列出，可按关键词过滤（仅文本）。
func (s *Store) List(query string) []View {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := strings.ToLower(strings.TrimSpace(query))
	var out []View
	for _, it := range s.items {
		if q != "" {
			if it.Kind != "text" || !strings.Contains(strings.ToLower(it.Text), q) {
				if !(it.Kind == "image" && (q == "图片" || q == "image")) {
					continue
				}
			}
		}
		v := View{Item: *it}
		if it.Kind == "text" {
			r := []rune(it.Text)
			v.Chars = len(r)
			if len(r) > 300 {
				r = r[:300]
			}
			v.Preview = string(r)
			v.Text = ""
		} else {
			v.Thumb = s.thumbLocked(it.ID)
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Pinned != out[j].Pinned {
			return out[i].Pinned
		}
		return out[i].Created > out[j].Created
	})
	return out
}

func (s *Store) thumbLocked(id string) string {
	if t, ok := s.thumbs[id]; ok {
		return t
	}
	b, err := os.ReadFile(s.thumbPath(id))
	if err != nil {
		b, err = os.ReadFile(s.imagePath(id))
		if err != nil {
			return ""
		}
	}
	t := "data:image/png;base64," + base64.StdEncoding.EncodeToString(b)
	s.thumbs[id] = t
	return t
}

// Get 取出完整条目；图片时返回 PNG 数据。
func (s *Store) Get(id string) (*Item, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range s.items {
		if it.ID == id {
			cp := *it
			if it.Kind == "image" {
				b, err := os.ReadFile(s.imagePath(id))
				return &cp, b, err
			}
			return &cp, nil, nil
		}
	}
	return nil, nil, errors.New("记录不存在")
}

// Touch 把条目移到最前（重新复制时）。
func (s *Store) Touch(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range s.items {
		if it.ID == id {
			it.Created = s.now().UnixMilli()
			s.saveLocked()
			return
		}
	}
}

// SetPinned 置顶/取消置顶。
func (s *Store) SetPinned(id string, pinned bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range s.items {
		if it.ID == id {
			it.Pinned = pinned
		}
	}
	s.trimLocked()
	s.saveLocked()
}

// Delete 删除条目。
func (s *Store) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, it := range s.items {
		if it.ID == id {
			s.removeAtLocked(i)
			break
		}
	}
	s.saveLocked()
}

// Clear 清空；keepPinned 为 true 时保留置顶条目。
func (s *Store) Clear(keepPinned bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.items) - 1; i >= 0; i-- {
		if keepPinned && s.items[i].Pinned {
			continue
		}
		s.removeAtLocked(i)
	}
	s.saveLocked()
}

// JoinTexts 把多个文本条目按给定顺序用换行连接（图片跳过）。
func (s *Store) JoinTexts(ids []string) (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	byID := map[string]*Item{}
	for _, it := range s.items {
		byID[it.ID] = it
	}
	var parts []string
	for _, id := range ids {
		if it, ok := byID[id]; ok && it.Kind == "text" {
			parts = append(parts, it.Text)
		}
	}
	return strings.Join(parts, "\r\n"), len(parts)
}

// Thumbnail 等比缩放（盒式滤波），最长边不超过 max。
func Thumbnail(src image.Image, max int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= max && h <= max {
		return src
	}
	var nw, nh int
	if w >= h {
		nw, nh = max, h*max/w
	} else {
		nw, nh = w*max/h, max
	}
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		y0 := b.Min.Y + y*h/nh
		y1 := b.Min.Y + (y+1)*h/nh
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < nw; x++ {
			x0 := b.Min.X + x*w/nw
			x1 := b.Min.X + (x+1)*w/nw
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, bl, a, n uint32
			// 采样步长，避免超大图过慢
			sy := (y1-y0)/8 + 1
			sx := (x1-x0)/8 + 1
			for yy := y0; yy < y1; yy += sy {
				for xx := x0; xx < x1; xx += sx {
					cr, cg, cb, ca := src.At(xx, yy).RGBA()
					r += cr
					g += cg
					bl += cb
					a += ca
					n++
				}
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i] = uint8(r / n >> 8)
			dst.Pix[i+1] = uint8(g / n >> 8)
			dst.Pix[i+2] = uint8(bl / n >> 8)
			dst.Pix[i+3] = uint8(a / n >> 8)
		}
	}
	return dst
}
