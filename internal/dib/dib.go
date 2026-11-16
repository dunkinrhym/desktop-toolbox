// Package dib 在 Windows 剪贴板 DIB 格式(CF_DIB/CF_DIBV5)与 image.Image 之间转换。纯 Go。
package dib

import (
	"encoding/binary"
	"errors"
	"image"
)

const (
	biRGB       = 0
	biBitfields = 3
)

// Decode 解析 BITMAPINFOHEADER/V4/V5 开头的 DIB 数据（24/32 位，以及 8 位调色板）。
func Decode(b []byte) (*image.RGBA, error) {
	if len(b) < 40 {
		return nil, errors.New("DIB 数据过短")
	}
	le := binary.LittleEndian
	hsize := int(le.Uint32(b[0:]))
	w := int(int32(le.Uint32(b[4:])))
	h := int(int32(le.Uint32(b[8:])))
	bpp := int(le.Uint16(b[14:]))
	comp := le.Uint32(b[16:])
	clrUsed := int(le.Uint32(b[32:]))
	if hsize < 40 || hsize > len(b) || w <= 0 || h == 0 || w > 1<<15 {
		return nil, errors.New("无效的 DIB 头")
	}
	topDown := h < 0
	if topDown {
		h = -h
	}
	if h > 1<<15 {
		return nil, errors.New("图片过大")
	}
	off := hsize
	rMask, gMask, bMask, aMask := uint32(0x00FF0000), uint32(0x0000FF00), uint32(0x000000FF), uint32(0xFF000000)
	if comp == biBitfields {
		if hsize == 40 {
			if len(b) < 52 {
				return nil, errors.New("DIB 位域缺失")
			}
			rMask, gMask, bMask = le.Uint32(b[40:]), le.Uint32(b[44:]), le.Uint32(b[48:])
			aMask = 0
			off += 12
		} else if hsize >= 56 {
			rMask, gMask, bMask, aMask = le.Uint32(b[40:]), le.Uint32(b[44:]), le.Uint32(b[48:]), le.Uint32(b[52:])
		}
	} else if comp != biRGB {
		return nil, errors.New("不支持压缩的 DIB")
	}
	var palette []uint32
	if bpp <= 8 {
		n := clrUsed
		if n == 0 {
			n = 1 << bpp
		}
		if off+n*4 > len(b) {
			return nil, errors.New("DIB 调色板不完整")
		}
		for i := 0; i < n; i++ {
			palette = append(palette, le.Uint32(b[off+i*4:]))
		}
		off += n * 4
	}
	stride := ((w*bpp + 31) / 32) * 4
	if off+stride*h > len(b) {
		return nil, errors.New("DIB 像素数据不完整")
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	allZeroAlpha := true
	shift := func(m uint32) uint {
		var s uint
		for m != 0 && m&1 == 0 {
			m >>= 1
			s++
		}
		return s
	}
	rs, gs, bs, as := shift(rMask), shift(gMask), shift(bMask), shift(aMask)
	for y := 0; y < h; y++ {
		sy := y
		if !topDown {
			sy = h - 1 - y
		}
		row := b[off+sy*stride:]
		for x := 0; x < w; x++ {
			var r, g, bl, a uint8
			switch bpp {
			case 32:
				p := le.Uint32(row[x*4:])
				r, g, bl = uint8((p&rMask)>>rs), uint8((p&gMask)>>gs), uint8((p&bMask)>>bs)
				if aMask != 0 {
					a = uint8((p & aMask) >> as)
				}
			case 24:
				bl, g, r, a = row[x*3], row[x*3+1], row[x*3+2], 255
			case 16:
				p := uint32(le.Uint16(row[x*2:]))
				if comp != biBitfields {
					rMask, gMask, bMask = 0x7C00, 0x03E0, 0x001F
					rs, gs, bs = 10, 5, 0
				}
				r8 := func(v, m uint32, s uint) uint8 {
					mx := m >> s
					if mx == 0 {
						return 0
					}
					return uint8(((v & m) >> s) * 255 / mx)
				}
				r, g, bl, a = r8(p, rMask, rs), r8(p, gMask, gs), r8(p, bMask, bs), 255
			case 8, 4, 1:
				var idx int
				switch bpp {
				case 8:
					idx = int(row[x])
				case 4:
					idx = int(row[x/2]>>(4*(1-uint(x%2)))) & 0xF
				case 1:
					idx = int(row[x/8]>>(7-uint(x%8))) & 1
				}
				if idx < len(palette) {
					c := palette[idx]
					r, g, bl = uint8(c>>16), uint8(c>>8), uint8(c)
				}
				a = 255
			default:
				return nil, errors.New("不支持的色深")
			}
			if a != 0 {
				allZeroAlpha = false
			}
			i := img.PixOffset(x, y)
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = r, g, bl, a
		}
	}
	if allZeroAlpha { // 很多程序 32 位 DIB 的 alpha 全为 0，视为不透明
		for i := 3; i < len(img.Pix); i += 4 {
			img.Pix[i] = 255
		}
	} else if bpp == 32 {
		// 剪贴板 32 位 DIB 一般为预乘 alpha，image.RGBA 也是预乘，无需转换
	}
	return img, nil
}

// Encode24 生成 24 位自底向上的 CF_DIB 数据（BITMAPINFOHEADER + 像素），兼容性最好。
func Encode24(img image.Image) []byte {
	bd := img.Bounds()
	w, h := bd.Dx(), bd.Dy()
	stride := ((w*24 + 31) / 32) * 4
	out := make([]byte, 40+stride*h)
	le := binary.LittleEndian
	le.PutUint32(out[0:], 40)
	le.PutUint32(out[4:], uint32(int32(w)))
	le.PutUint32(out[8:], uint32(int32(h)))
	le.PutUint16(out[12:], 1)
	le.PutUint16(out[14:], 24)
	le.PutUint32(out[16:], biRGB)
	le.PutUint32(out[20:], uint32(stride*h))
	le.PutUint32(out[24:], 3780) // 96 DPI
	le.PutUint32(out[28:], 3780)
	rgba, _ := img.(*image.RGBA)
	for y := 0; y < h; y++ {
		row := out[40+(h-1-y)*stride:]
		for x := 0; x < w; x++ {
			var r, g, b, a uint8
			if rgba != nil {
				i := rgba.PixOffset(bd.Min.X+x, bd.Min.Y+y)
				r, g, b, a = rgba.Pix[i], rgba.Pix[i+1], rgba.Pix[i+2], rgba.Pix[i+3]
			} else {
				cr, cg, cb, ca := img.At(bd.Min.X+x, bd.Min.Y+y).RGBA()
				r, g, b, a = uint8(cr>>8), uint8(cg>>8), uint8(cb>>8), uint8(ca>>8)
			}
			if a != 255 { // 预乘 alpha 合成到白色背景
				inv := 255 - uint16(a)
				r = uint8(uint16(r) + inv)
				g = uint8(uint16(g) + inv)
				b = uint8(uint16(b) + inv)
			}
			row[x*3], row[x*3+1], row[x*3+2] = b, g, r
		}
	}
	return out
}
