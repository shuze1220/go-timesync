package main

// 把超采样母图按任意比例面积重采样到多尺寸，并打包成 ICO（内嵌 PNG，Vista+ 支持）。

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
)

// areaResample 通用面积重采样：目标像素 = 对应源区域的平均值。
// 支持非整数倍缩放（如 1024 -> 48）。
func areaResample(src *image.RGBA, sb, n int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, n, n))
	scale := float64(sb) / float64(n)
	for y := 0; y < n; y++ {
		y0 := int(float64(y) * scale)
		y1 := int(float64(y+1) * scale)
		if y1 <= y0 {
			y1 = y0 + 1
		}
		if y1 > sb {
			y1 = sb
		}
		for x := 0; x < n; x++ {
			x0 := int(float64(x) * scale)
			x1 := int(float64(x+1) * scale)
			if x1 <= x0 {
				x1 = x0 + 1
			}
			if x1 > sb {
				x1 = sb
			}
			var r, g, b, a, cnt float64
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					c := src.RGBAAt(xx, yy)
					r += float64(c.R) * float64(c.A)
					g += float64(c.G) * float64(c.A)
					b += float64(c.B) * float64(c.A)
					a += float64(c.A)
					cnt++
				}
			}
			var rr, gg, bb, aa uint8
			if a > 0 {
				rr = uint8(r / a)
				gg = uint8(g / a)
				bb = uint8(b / a)
			}
			if cnt > 0 {
				aa = uint8(a / cnt)
			}
			dst.Set(x, y, color.RGBA{R: rr, G: gg, B: bb, A: aa})
		}
	}
	return dst
}

// encodeICO 把多个尺寸打包成 ICO（每个条目内嵌 PNG 数据）。
func encodeICO(src *image.RGBA, sb int, sizes []int) ([]byte, error) {
	type entry struct {
		size int
		data []byte
	}
	var entries []entry
	for _, s := range sizes {
		img := areaResample(src, sb, s)
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return nil, err
		}
		entries = append(entries, entry{size: s, data: buf.Bytes()})
	}

	var out bytes.Buffer
	// ICONDIR
	binary.Write(&out, binary.LittleEndian, uint16(0)) // reserved
	binary.Write(&out, binary.LittleEndian, uint16(1)) // type = icon
	binary.Write(&out, binary.LittleEndian, uint16(len(entries)))

	offset := 6 + 16*len(entries)
	for _, e := range entries {
		w := byte(e.size)
		if e.size >= 256 {
			w = 0 // 256 用 0 表示
		}
		out.WriteByte(w)                                    // width
		out.WriteByte(w)                                    // height
		out.WriteByte(0)                                    // colorCount
		out.WriteByte(0)                                    // reserved
		binary.Write(&out, binary.LittleEndian, uint16(1))  // planes
		binary.Write(&out, binary.LittleEndian, uint16(32)) // bitCount
		binary.Write(&out, binary.LittleEndian, uint32(len(e.data)))
		binary.Write(&out, binary.LittleEndian, uint32(offset))
		offset += len(e.data)
	}
	for _, e := range entries {
		out.Write(e.data)
	}
	return out.Bytes(), nil
}
