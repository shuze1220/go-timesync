package main

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var driftRe = regexp.MustCompile(
	`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}(?:\s+\S+)?\s+校正\s+源=(.*?)\s+类型=(\w+)\s+偏差=([-+0-9.]+)\s+延迟=([-+0-9.]+)`,
)

type DriftRow struct {
	TS    string
	Kind  string
	Off   float64
	Delay float64
}

// HistoryPoint 单条校正记录（面板趋势图用）。
type HistoryPoint struct {
	TS     string  `json:"ts"`
	Server string  `json:"server"`
	Kind   string  `json:"kind"`
	Offset float64 `json:"offset"`
	Delay  float64 `json:"delay"`
}

// ParseHistory 解析日志中最近 n 条校正记录（复用 driftRe，含源名）。
func ParseHistory(path string, n int) []HistoryPoint {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil
	}
	var rows []HistoryPoint
	for _, line := range strings.Split(string(data), "\n") {
		m := driftRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ts := ""
		if len(line) >= 19 {
			ts = line[:19]
		}
		off, _ := strconv.ParseFloat(m[3], 64)
		dly, _ := strconv.ParseFloat(m[4], 64)
		rows = append(rows, HistoryPoint{TS: ts, Server: m[1], Kind: m[2], Offset: off, Delay: dly})
	}
	if n > 0 && len(rows) > n {
		rows = rows[len(rows)-n:]
	}
	return rows
}

// parseDriftRows 解析日志中最近 n 条校正记录（复用 driftRe）。
// n<=0 表示全部。供 ReportDrift 与 EstimateDrift 共用。
func parseDriftRows(path string, n int) []DriftRow {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil
	}
	var rows []DriftRow
	for _, line := range strings.Split(string(data), "\n") {
		m := driftRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ts := ""
		if len(line) >= 19 {
			ts = line[:19]
		}
		off, _ := strconv.ParseFloat(m[3], 64)
		dly, _ := strconv.ParseFloat(m[4], 64)
		rows = append(rows, DriftRow{TS: ts, Kind: m[2], Off: off, Delay: dly})
	}
	if n > 0 && len(rows) > n {
		rows = rows[len(rows)-n:]
	}
	return rows
}

// EstimateDrift 结构化估计本机晶振漂移（ppm）。
// 与早期「把 ReportDrift 的文本再用字符串硬解析」不同，这里直接算数值返回，
// 日志文案怎么改都不会影响面板拿到的 ppm。记录不足 2 条或时间跨度过小则返回 ok=false。
func EstimateDrift(path string, n int) (ppm float64, ok bool) {
	rows := parseDriftRows(path, n)
	if len(rows) < 2 {
		return 0, false
	}
	t0, e0 := time.Parse("2006-01-02 15:04:05", rows[0].TS)
	t1, e1 := time.Parse("2006-01-02 15:04:05", rows[len(rows)-1].TS)
	if e0 != nil || e1 != nil {
		return 0, false
	}
	dt := t1.Sub(t0).Seconds()
	if dt <= 1 {
		return 0, false
	}
	ppm = (rows[len(rows)-1].Off - rows[0].Off) / dt * 1e6
	return ppm, true
}

// ReportDrift parses the last n correction rows from the log, lists them,
// and estimates crystal drift (ppm) from the first/last row if >= 2.
func ReportDrift(path string, n int) string {
	var b strings.Builder
	if path == "" {
		b.WriteString("无日志文件（用 --log 指定/开启，或从配置读 log=）。\n")
		return b.String()
	}
	rows := parseDriftRows(path, n)
	if len(rows) == 0 {
		b.WriteString("日志中暂无校正记录。\n")
		return b.String()
	}
	fmt.Fprintf(&b, "最近 %d 次校正记录：\n", len(rows))
	for _, r := range rows {
		fmt.Fprintf(&b, "  %s  %-3s  偏差 %+.3f 秒  延迟 %.3f 秒\n", r.TS, r.Kind, r.Off, r.Delay)
	}
	if ppm, ok := EstimateDrift(path, n); ok {
		fmt.Fprintf(&b, "估计本机晶振漂移约 %+.2f ppm（相邻记录偏差变化 / 时长）。\n", ppm)
	}
	return b.String()
}
