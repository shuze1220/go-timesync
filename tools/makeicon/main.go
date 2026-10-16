package main

// 生成 time_sync 校时工具的图标：蓝色渐变圆盘 + 白色刻度 + 10:10 指针。
// 4x 超采样后降采样，保证边缘平滑（Go 标准库无抗锯齿绘图，只能这样）。

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

const (
	N  = 256 // 输出尺寸
	SS = 4   // 超采样倍数
	S  = N * SS
)

type rgb [3]float64

var (
	cTop = rgb{0x1d / 255.0, 0x4e / 255.0, 0xd8 / 255.0} // #1d4ed8 深蓝
	cBot = rgb{0x38 / 255.0, 0xbd / 255.0, 0xf8 / 255.0} // #38bdf8 天蓝
)

// distToSeg 点到线段距离（画圆头指针/刻度用）
func distToSeg(px, py, ax, ay, bx, by float64) float64 {
	vx, vy := bx-ax, by-ay
	wx, wy := px-ax, py-ay
	l2 := vx*vx + vy*vy
	var t float64
	if l2 > 0 {
		t = (wx*vx + wy*vy) / l2
	}
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	qx, qy := ax+t*vx, ay+t*vy
	return math.Hypot(px-qx, py-qy)
}

// capsule 判断点是否落在以 (ax,ay)-(bx,by) 为轴、半径 w 的胶囊内
func inCapsule(px, py, ax, ay, bx, by, w float64) bool {
	return distToSeg(px, py, ax, ay, bx, by) <= w
}

func main() {
	img := image.NewRGBA(image.Rect(0, 0, S, S))
	c := float64(S) / 2
	R := c * 0.94       // 外圆半径
	ringW := R * 0.075  // 白色外环宽度
	ringIn := R - ringW // 内圈半径（渐变区）

	// --- 指针角度：10:10 经典姿态 ---
	// 屏幕坐标 y 向下，角度以 12 点为 -90°
	hourAng := -math.Pi/2 + (10.0/12.0)*2*math.Pi // 10 点
	minAng := -math.Pi/2 + (10.0/60.0)*2*math.Pi  // 10 分 -> 指向 2
	hourLen := R * 0.46
	minLen := R * 0.68
	hourW := R * 0.075
	minW := R * 0.055
	hcx, hcy := c, c // 指针起点（圆心）

	// --- 12 个刻度 ---
	type tick struct{ ax, ay, bx, by float64 }
	var ticks []tick
	for i := 0; i < 12; i++ {
		a := -math.Pi/2 + float64(i)/12.0*2*math.Pi
		r1 := ringIn * 0.70
		r2 := ringIn * 0.86
		if i%3 == 0 {
			r1 = ringIn * 0.62 // 整点更长
		}
		ticks = append(ticks, tick{
			c + math.Cos(a)*r1, c + math.Sin(a)*r1,
			c + math.Cos(a)*r2, c + math.Sin(a)*r2,
		})
	}
	tickW := R * 0.028

	for y := 0; y < S; y++ {
		for x := 0; x < S; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			dx, dy := px-c, py-c
			d := math.Hypot(dx, dy)
			if d > R {
				continue // 圆外透明
			}

			var r, g, b float64
			switch {
			case d > ringIn:
				// 白色外环
				r, g, b = 1, 1, 1
			default:
				// 蓝渐变
				t := (py) / float64(S)
				r = cTop[0] + (cBot[0]-cTop[0])*t
				g = cTop[1] + (cBot[1]-cTop[1])*t
				b = cTop[2] + (cBot[2]-cTop[2])*t
			}

			a := 1.0 // alpha（超采样时按覆盖比例累积，这里简化为二值后降采样）

			// 刻度（白）
			for _, tk := range ticks {
				if inCapsule(px, py, tk.ax, tk.ay, tk.bx, tk.by, tickW) {
					r, g, b = 1, 1, 1
					break
				}
			}
			// 指针（白）
			if inCapsule(px, py, hcx, hcy,
				hcx+math.Cos(hourAng)*hourLen, hcy+math.Sin(hourAng)*hourLen, hourW) ||
				inCapsule(px, py, hcx, hcy,
					hcx+math.Cos(minAng)*minLen, hcy+math.Sin(minAng)*minLen, minW) {
				r, g, b = 1, 1, 1
			}
			// 中心圆点（深蓝，让指针根部干净）
			if d < R*0.075 {
				r, g, b = cTop[0], cTop[1], cTop[2]
			}

			if a > 0 {
				img.Set(x, y, color.RGBA{
					R: uint8(r * 255), G: uint8(g * 255), B: uint8(b * 255), A: 255,
				})
			}
		}
	}

	out := downsample(img, N)
	f, err := os.Create(os.Args[1])
	if err != nil {
		panic(err)
	}
	if err := png.Encode(f, out); err != nil {
		panic(err)
	}
	f.Close()

	// 多尺寸 ICO（Windows 资源管理器/任务栏/Alt-Tab 各取所需）
	if len(os.Args) > 2 {
		ico, err := encodeICO(img, S, []int{16, 24, 32, 48, 64, 128, 256})
		if err != nil {
			panic(err)
		}
		if err := os.WriteFile(os.Args[2], ico, 0644); err != nil {
			panic(err)
		}
	}
}

// downsample 面积平均降采样（等价抗锯齿）
func downsample(src *image.RGBA, n int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, n, n))
	f := S / n
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			var r, g, b, a, cnt float64
			for dy := 0; dy < f; dy++ {
				for dx := 0; dx < f; dx++ {
					c := src.RGBAAt(x*f+dx, y*f+dy)
					r += float64(c.R)
					g += float64(c.G)
					b += float64(c.B)
					a += float64(c.A)
					cnt++
				}
			}
			dst.Set(x, y, color.RGBA{
				R: uint8(r / cnt), G: uint8(g / cnt),
				B: uint8(b / cnt), A: uint8(a / cnt),
			})
		}
	}
	return dst
}
