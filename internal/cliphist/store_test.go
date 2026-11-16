package cliphist

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"
)

func pngBytes(w, h int, c color.Color) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

func TestStore(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, 5)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(1700000000, 0)
	s.now = func() time.Time { clock = clock.Add(time.Second); return clock }
	for _, txt := range []string{"一", "二", "三", "  ", "四"} {
		s.AddText(txt)
	}
	if ok, err := s.AddImage(pngBytes(1000, 500, color.RGBA{255, 0, 0, 255})); !ok || err != nil {
		t.Fatal(err)
	}
	l := s.List("")
	if len(l) != 5 || l[0].Kind != "image" || l[0].Thumb == "" || l[0].Width != 1000 {
		t.Fatalf("list %+v", l)
	}
	// 重复内容只更新时间
	s.AddText("一")
	l = s.List("")
	if len(l) != 5 || l[0].Preview != "一" {
		t.Fatalf("bump %+v", l[0])
	}
	// 置顶后超出上限不被删除
	s.SetPinned(l[len(l)-1].ID, true)
	pinnedID := l[len(l)-1].ID
	for _, txt := range []string{"五", "六", "七"} {
		s.AddText(txt)
	}
	l = s.List("")
	if len(l) != 5 || l[0].ID != pinnedID {
		t.Fatalf("pin %+v", l)
	}
	// 持久化
	s2, _ := Open(dir, 5)
	if len(s2.List("")) != 5 {
		t.Fatalf("reload")
	}
	if got := s2.List("六"); len(got) != 1 {
		t.Fatalf("search %+v", got)
	}
	ids := []string{}
	for _, v := range s2.List("") {
		ids = append(ids, v.ID)
	}
	joined, n := s2.JoinTexts(ids)
	if n == 0 || !bytes.Contains([]byte(joined), []byte("\r\n")) {
		t.Fatalf("join %q", joined)
	}
	s2.Clear(true)
	if l := s2.List(""); len(l) != 1 || !l[0].Pinned {
		t.Fatalf("clear %+v", l)
	}
	th := Thumbnail(image.NewRGBA(image.Rect(0, 0, 3000, 100)), 240)
	if th.Bounds().Dx() != 240 || th.Bounds().Dy() != 8 {
		t.Fatalf("thumb %v", th.Bounds())
	}
}
