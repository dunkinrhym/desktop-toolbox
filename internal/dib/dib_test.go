package dib

import (
	"encoding/binary"
	"image"
	"image/color"
	"testing"
)

func TestRoundTrip24(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 5, 3))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	img.Set(4, 2, color.RGBA{0, 0, 255, 255})
	img.Set(2, 1, color.RGBA{10, 20, 30, 255})
	b := Encode24(img)
	if len(b) != 40+16*3 {
		t.Fatalf("len %d", len(b))
	}
	got, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range [][2]int{{0, 0}, {4, 2}, {2, 1}} {
		if got.RGBAAt(p[0], p[1]) != img.RGBAAt(p[0], p[1]) {
			t.Errorf("pixel %v: %v vs %v", p, got.RGBAAt(p[0], p[1]), img.RGBAAt(p[0], p[1]))
		}
	}
}

func TestDecode32TopDownZeroAlpha(t *testing.T) {
	w, h := 2, 2
	b := make([]byte, 40+w*h*4)
	le := binary.LittleEndian
	le.PutUint32(b[0:], 40)
	le.PutUint32(b[4:], uint32(w))
	v := int32(-h)
	le.PutUint32(b[8:], uint32(v))
	le.PutUint16(b[12:], 1)
	le.PutUint16(b[14:], 32)
	// 第一行第一个像素 = 红 (BGRA)，alpha 0
	b[40], b[41], b[42], b[43] = 0, 0, 255, 0
	img, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if c := img.RGBAAt(0, 0); c != (color.RGBA{255, 0, 0, 255}) {
		t.Fatalf("got %v", c)
	}
}

func TestDecodeErrors(t *testing.T) {
	if _, err := Decode([]byte{1, 2, 3}); err == nil {
		t.Error("short")
	}
}
