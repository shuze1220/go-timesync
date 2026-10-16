package main

import (
	"fmt"
	"os"
	"strings"
)

// 进度条状态。Python 版在交互终端用 \r 重绘底部进度条，结果行实时上滚；
// 管道/重定向时必须静默，否则日志里会混入 \r 与控制字符。
var lastBarLen int

const barWidth = 28

// barActive reports whether stdout is an interactive terminal.
func barActive() bool {
	return isTerminal(os.Stdout)
}

// renderProgress renders a single-line ASCII progress bar.
// 纯 ASCII（'#'/'-'）以兼容 GBK/936 控制台，不用 Unicode 方块。
func renderProgress(done, total, ok, fail int) string {
	if total <= 0 {
		return ""
	}
	pct := float64(done) / float64(total)
	filled := int(float64(barWidth)*pct + 0.5)
	if filled > barWidth {
		filled = barWidth
	}
	if filled < 0 {
		filled = 0
	}
	return fmt.Sprintf("[%s%s] %d/%d (%3d%%)  成功 %d 失败 %d",
		strings.Repeat("#", filled), strings.Repeat("-", barWidth-filled),
		done, total, int(pct*100), ok, fail)
}

// barClear erases the bottom bar by overwriting it with spaces.
func barClear() {
	if !barActive() || lastBarLen <= 0 {
		return
	}
	fmt.Print("\r" + strings.Repeat(" ", lastBarLen) + "\r")
	lastBarLen = 0
}

// barDraw (re)draws the bar at the bottom without a trailing newline.
func barDraw(done, total, ok, fail int) {
	if !barActive() {
		return
	}
	text := renderProgress(done, total, ok, fail)
	fmt.Print("\r" + text)
	lastBarLen = len(text)
}

// barEnd finishes the bar: newline while keeping the final state visible.
func barEnd() {
	if !barActive() {
		return
	}
	fmt.Print("\n")
	lastBarLen = 0
}

// progressLine prints a result line above the bar, then redraws the bar.
// 对应 Python 的 _bar_clear() -> print(line) -> _bar_draw(...) 序列。
func progressLine(line string, done, total, ok, fail int) {
	if barActive() {
		barClear()
		fmt.Println(line)
		barDraw(done, total, ok, fail)
		return
	}
	fmt.Println(line)
}
