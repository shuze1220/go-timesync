package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestAbsFloat(t *testing.T) {
	if absFloat(-1.5) != 1.5 {
		t.Errorf("absFloat(-1.5)=%v", absFloat(-1.5))
	}
	if absFloat(2.0) != 2.0 {
		t.Errorf("absFloat(2.0)=%v", absFloat(2.0))
	}
}

func TestRenderProgress(t *testing.T) {
	// 空/非法：不渲染
	if s := renderProgress(0, 0, 0, 0); s != "" {
		t.Errorf("total=0 should render empty, got %q", s)
	}
	// 进度条"图形"必须纯 ASCII：GBK/936 控制台显示不了 Unicode 方块(█░)。
	// 中文标签（成功/失败）GBK 本身支持，不受此限。
	s := renderProgress(5, 11, 5, 0)
	bar := s[:strings.Index(s, "]")+1]
	for _, r := range bar {
		if r > 127 {
			t.Errorf("non-ASCII rune %q in bar graphics: %q", r, bar)
		}
	}
	if strings.ContainsAny(bar, "█░▓") {
		t.Errorf("bar uses Unicode blocks (broken on GBK console): %q", bar)
	}
	// 进度 0% 与 100% 的填充长度
	p0 := renderProgress(0, 10, 0, 0)
	p100 := renderProgress(10, 10, 10, 0)
	if strings.Count(p0, "#") != 0 {
		t.Errorf("0%% should have no '#': %q", p0)
	}
	if strings.Count(p100, "#") != barWidth {
		t.Errorf("100%% should be full '#': %q", p100)
	}
	if strings.Count(p0, "-") != barWidth {
		t.Errorf("0%% should be full '-': %q", p0)
	}
	// 格式含 done/total/百分比/成功/失败
	for _, want := range []string{"5/11", "成功 5", "失败 0"} {
		if !strings.Contains(s, want) {
			t.Errorf("bar %q missing %q", s, want)
		}
	}
}

func TestBarSilentWhenNotTTY(t *testing.T) {
	// 测试环境 stdout 是管道，barActive() 应为 false，
	// 保证进度条不会把 \r 写进重定向/计划任务日志。
	if barActive() {
		t.Skip("stdout is a TTY in this environment; skipping")
	}
	barDraw(3, 10, 3, 0)
	if lastBarLen != 0 {
		t.Errorf("bar drew while not a TTY (lastBarLen=%d)", lastBarLen)
	}
}

func TestMedian(t *testing.T) {
	if m := median([]float64{1, 3, 5}); m != 3 {
		t.Errorf("odd median=%v", m)
	}
	if m := median([]float64{1, 2, 3, 4}); m != 2.5 {
		t.Errorf("even median=%v", m)
	}
	if m := median(nil); m != 0 {
		t.Errorf("empty median=%v", m)
	}
}

func TestSplitJoinListRoundtrip(t *testing.T) {
	in := []string{"a", "b", "c"}
	j := joinList(in)
	out := splitList(j)
	if len(out) != len(in) {
		t.Fatalf("len=%d", len(out))
	}
	for i := range in {
		if out[i] != in[i] {
			t.Fatalf("[%d] %v vs %v", i, out[i], in[i])
		}
	}
	// Split tolerates spaces, commas, semicolons
	if got := splitList("x, y ; z"); len(got) != 3 {
		t.Errorf("split: %v", got)
	}
	// Dedup
	if got := splitList("a, a, b"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("dedup: %v", got)
	}
}

func TestUpdateIniPreservesComments(t *testing.T) {
	src := `; 头部注释
[default]
keep = 1

[servers]
; 旧 NTP 注释
ntp = old.example.com, another.example.com
http = https://a.example.com

[sync]
timeout = 5
`
	out := updateIniText(src, []string{"new.example.com"}, []string{"https://new.example.com"})
	// 注释和未改键应当保留
	for _, must := range []string{
		"; 头部注释",
		"[default]",
		"keep = 1",
		"[sync]",
		"timeout = 5",
		"; 旧 NTP 注释",
	} {
		if !strings.Contains(out, must) {
			t.Errorf("missing %q in:\n%s", must, out)
		}
	}
	if !strings.Contains(out, "ntp = new.example.com") {
		t.Errorf("ntp not updated:\n%s", out)
	}
	if !strings.Contains(out, "http = https://new.example.com") {
		t.Errorf("http not updated:\n%s", out)
	}
	if strings.Contains(out, "old.example.com") {
		t.Errorf("old ntp leaked:\n%s", out)
	}
}

func TestParseBool(t *testing.T) {
	for _, v := range []string{"true", "yes", "1", "on", "TRUE"} {
		if !parseBool(v) {
			t.Errorf("parseBool(%q) false", v)
		}
	}
	for _, v := range []string{"false", "no", "0", "", "random"} {
		if parseBool(v) {
			t.Errorf("parseBool(%q) true", v)
		}
	}
}

func TestDriftReportEmpty(t *testing.T) {
	if !strings.Contains(ReportDrift("", 5), "无日志") {
		t.Error("expected 无日志 for empty path")
	}
	// 缺失文件与空日志在重构后统一提示「暂无校正记录」（parseDriftRows 对读取失败返回 nil）
	if !strings.Contains(ReportDrift("nonexistent-file-xyz.log", 5), "暂无校正记录") {
		t.Error("expected 暂无校正记录 for missing/empty file")
	}
}

func TestDriftReportParsesRows(t *testing.T) {
	// Synthesize a log with 2 well-formed correction rows 1 hour apart
	log := `2026-01-01 12:00:00 +0800  校正 源=ntp.aliyun.com 类型=ntp 偏差=+0.100 延迟=0.020
2026-01-01 13:00:00 +0800  校正 源=ntp.aliyun.com 类型=ntp 偏差=+0.500 延迟=0.020
`
	tmp := t.TempDir() + "/drift.log"
	if err := writeFile(tmp, log); err != nil {
		t.Fatal(err)
	}
	out := ReportDrift(tmp, 10)
	if !strings.Contains(out, "ntp  偏差") {
		t.Errorf("row format missing:\n%s", out)
	}
	if !strings.Contains(out, "+111.11 ppm") {
		t.Errorf("ppm calculation wrong:\n%s", out)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}

// --- P1 回归测试：BUG-2 日志路径 / 提权标签分派 / 配置回写 / 历史解析 ---

func TestResolveLogPath(t *testing.T) {
	// 相对路径 → 解析为 exe 同目录下的绝对路径
	cfg := Config{Log: "校正日志.txt"}
	resolveLogPath(&cfg)
	if !filepath.IsAbs(cfg.Log) {
		t.Fatalf("relative log not resolved to absolute: %q", cfg.Log)
	}
	if !strings.HasSuffix(cfg.Log, "校正日志.txt") {
		t.Fatalf("basename lost: %q", cfg.Log)
	}
	// 绝对路径 → 原样保留
	abs := `C:\abs\a.log`
	if runtime.GOOS == "windows" {
		c2 := Config{Log: abs}
		resolveLogPath(&c2)
		if c2.Log != abs {
			t.Fatalf("absolute path mutated: %q", c2.Log)
		}
	}
	// 空值 → 不变
	c3 := Config{}
	resolveLogPath(&c3)
	if c3.Log != "" {
		t.Fatalf("empty log mutated: %q", c3.Log)
	}
}

func TestParseWebTag(t *testing.T) {
	cases := []struct {
		tag      string
		wantKind string
		wantSub  string
	}{
		{"scheduler:install", "scheduler", "install"},
		{"scheduler:remove", "scheduler", "remove"},
		{"startup:install", "startup", "install"},
		{"w32time:disable", "w32time", "disable"},
		{"w32time:enable", "w32time", "enable"},
		{"scheduler", "scheduler", ""},
	}
	for _, c := range cases {
		k, s := parseWebTag(c.tag)
		if k != c.wantKind || s != c.wantSub {
			t.Errorf("parseWebTag(%q) = (%q,%q), want (%q,%q)", c.tag, k, s, c.wantKind, c.wantSub)
		}
	}
}

func TestSaveServersRoundtrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "配置.ini")
	src := `[servers]
ntp = old.a.com, old.b.com
http = https://old.h.com

[sync]
timeout = 5
`
	if err := writeFile(p, src); err != nil {
		t.Fatal(err)
	}
	got, err := SaveServers(p, []string{"new.a.com"}, []string{"https://new.h.com"})
	if err != nil {
		t.Fatalf("SaveServers failed: %v", err)
	}
	if got != p {
		t.Errorf("returned path %q != %q", got, p)
	}
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if !equalList(cfg.NTP, []string{"new.a.com"}) {
		t.Errorf("NTP not persisted: %v", cfg.NTP)
	}
	if !equalList(cfg.HTTP, []string{"https://new.h.com"}) {
		t.Errorf("HTTP not persisted: %v", cfg.HTTP)
	}
	// 未改的键应保留
	if cfg.Timeout != 5 {
		t.Errorf("timeout not preserved: %v", cfg.Timeout)
	}
}

func TestParseHistory(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "log.txt")
	log := `2026-01-01 12:00:00 +0800  校正 源=ntp.aliyun.com 类型=ntp 偏差=+0.100 延迟=0.020
2026-01-01 13:00:00 +0800  校正 源=ntp.tencent.com 类型=ntp 偏差=-0.300 延迟=0.040
garbage line
`
	if err := writeFile(p, log); err != nil {
		t.Fatal(err)
	}
	pts := ParseHistory(p, 0)
	if len(pts) != 2 {
		t.Fatalf("expected 2 points, got %d", len(pts))
	}
	if pts[0].Server != "ntp.aliyun.com" || pts[0].Offset != 0.1 {
		t.Errorf("point0 wrong: %+v", pts[0])
	}
	if pts[1].Offset != -0.3 {
		t.Errorf("point1 offset wrong: %+v", pts[1])
	}
	// n 限制
	if len(ParseHistory(p, 1)) != 1 {
		t.Errorf("n limit not applied")
	}
	// 空路径 / 不存在
	if ParseHistory("", 5) != nil {
		t.Error("empty path should return nil")
	}
	if ParseHistory("nope.log", 5) != nil {
		t.Error("missing file should return nil")
	}
}

// TestDefaultLogPathIsExeDir 回归：开箱默认日志应落在 exe 同目录，
// 保证无 配置.ini / 无手动填写时也能记录（趋势图/漂移报告/实时日志可用）。
// 这是用户要求"默认为 exe 的路径"的验收点。
func TestDefaultLogPathIsExeDir(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Log != "校正日志.txt" {
		t.Fatalf("DefaultConfig().Log = %q, want 校正日志.txt", cfg.Log)
	}
	resolveLogPath(&cfg)
	if !filepath.IsAbs(cfg.Log) {
		t.Fatalf("default log not resolved to absolute: %q", cfg.Log)
	}
	exe, err := os.Executable()
	if err == nil {
		if filepath.Dir(cfg.Log) != filepath.Dir(exe) {
			t.Errorf("default log dir %q != exe dir %q", filepath.Dir(cfg.Log), filepath.Dir(exe))
		}
	}
	if !strings.HasSuffix(cfg.Log, "校正日志.txt") {
		t.Errorf("basename lost: %q", cfg.Log)
	}
}

// TestSlewConfigRoundtrip 回归：平滑校时的 slew / slew_max 必须能被写进 ini 并读回，
// 否则面板开关看起来生效、重启后又变回默认值。
func TestSlewConfigRoundtrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "配置.ini")
	src := "[servers]\nntp = a.example.com\n\n[sync]\ntimeout = 5\nthreshold = 0.5\nlog = 校正日志.txt\n"
	if err := writeFile(p, src); err != nil {
		t.Fatal(err)
	}
	c := DefaultConfig()
	c.Slew = true
	c.SlewMaxSec = 30
	if err := saveSyncSection(p, c); err != nil {
		t.Fatalf("saveSyncSection failed: %v", err)
	}
	got, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Slew {
		t.Errorf("slew not persisted: %v", got.Slew)
	}
	if got.SlewMaxSec != 30 {
		t.Errorf("slew_max = %v, want 30", got.SlewMaxSec)
	}
	// 关掉也要能持久化
	c.Slew = false
	if err := saveSyncSection(p, c); err != nil {
		t.Fatal(err)
	}
	got2, _ := LoadConfig(p)
	if got2.Slew {
		t.Errorf("slew should be false after turning off: %v", got2.Slew)
	}
}

// TestToSystemtimeUTC 回归：Windows SetSystemTime 把 SYSTEMTIME 当 UTC 解读，
// toSystemtime 必须先转 UTC 再取字段。否则 UTC+8 环境下会把 12:44 当成 UTC 写入，
// 系统时钟被拨快 8 小时（复测偏差约 -28800 秒）。
func TestToSystemtimeUTC(t *testing.T) {
	// 构造一个"本地 12:44:06 但处于 UTC+8 时区"的时刻 → 其真实 UTC 为 04:44:06。
	loc := time.FixedZone("UTC+8", 8*3600)
	tm := time.Date(2026, 9, 24, 12, 44, 6, 0, loc)
	st := toSystemtime(tm)
	// 期望按 UTC 取字段：小时必须是 4，而不是本地 12；其余字段对应 UTC 04:44:06。
	if st.wYear != 2026 || st.wMonth != 9 || st.wDay != 24 {
		t.Errorf("UTC date wrong: %d-%d-%d", st.wYear, st.wMonth, st.wDay)
	}
	if st.wHour != 4 {
		t.Errorf("wHour=%d, want 4 (UTC); 若为 12 说明误用了本地时区", st.wHour)
	}
	if st.wMinute != 44 || st.wSecond != 6 || st.wMilliseconds != 0 {
		t.Errorf("UTC time-of-day wrong: %02d:%02d:%02d.%03d",
			st.wHour, st.wMinute, st.wSecond, st.wMilliseconds)
	}
}
