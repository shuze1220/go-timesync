package main

import (
	"fmt"
	"os"
	"time"
)

const MaxLogBytes = 1 << 20 // 1 MB
const MaxLogGen = 3         // 保留最近 3 份轮转历史（.1 / .2 / .3）

// rotateLog 在日志超过上限时做多代轮转：最老一代丢弃，其余依次后移，当前日志→.1。
// 依赖 os.Rename 在 Windows 上会覆盖目标，因此无需先删除目标文件。
func rotateLog(path string) {
	for g := MaxLogGen; g > 1; g-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", path, g-1), fmt.Sprintf("%s.%d", path, g))
	}
	_ = os.Rename(path, path+".1")
}

// LogEvent appends a timestamped line to the log file. Rotates when > MaxLogBytes.
func LogEvent(path, line string) {
	if path == "" {
		return
	}
	if info, err := os.Stat(path); err == nil && info.Size() > MaxLogBytes {
		rotateLog(path)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	ts := time.Now().Format("2006-01-02 15:04:05 -0700")
	_, _ = f.WriteString(ts + "  " + line + "\n")
}
