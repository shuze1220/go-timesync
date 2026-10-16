package main

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// 时钟回拨（把时间往回调）的告警/确认阈值，单位秒。
//
// 时钟倒退会影响计划任务触发、证书有效期校验与日志时序，所以：
//   - 超过 backwardWarnSec  → 打印一句提醒
//   - 超过 backwardConfirmSec → 交互式场景必须让用户明确确认
//
// Web 面板是无人值守的 goroutine，不能阻塞等输入，因此它用同一阈值改成
// "先返回 409 让前端弹窗确认"，保证两条路径判定标准一致（见 apiSync）。
const (
	backwardWarnSec    = 5.0
	backwardConfirmSec = 60.0
)

type Status int

const (
	StatusOK Status = iota
	StatusSkip
	StatusFail
)

func (s Status) ExitCode() int {
	switch s {
	case StatusOK:
		return ExitOK
	case StatusSkip:
		return ExitSkip
	default:
		return ExitFail
	}
}

type RunResult struct {
	Status      Status
	Server      string
	Kind        string // "ntp"/"http"
	Offset      float64
	Delay       float64
	AfterOff    float64 // after recheck (0 if not synced)
	DidSync     bool    // 本次是否执行了写时钟（用于面板区分"查询"与"校时"）
	RecheckOK   bool    // 校正后复测是否真正执行成功（网络抖动会导致复测失败）
	DT          time.Time
	Error       string
	Uncertainty Uncertainty // 多源交叉验证不确定度
	Warning     string      // 非致命告警（如 w32time 冲突），不影响本次结果判定
}

type RunOptions struct {
	NTP       []string
	HTTP      []string
	Timeout   float64
	Retries   int
	Threshold float64
	Force     bool
	DoSync    bool
	Quiet     bool
	LogPath   string
	ReuseNTP  []string // for recheck (after sync)
	AllowBack bool
	// Slew 平滑校时：偏差在 SlewMaxSec 以内时分段逼近，而非一次跳变。
	Slew       bool
	SlewMaxSec float64
}

// RunOnce performs one query (+ optional sync). Returns RunResult with status.
func RunOnce(opt RunOptions) RunResult {
	prog := func(string) {}
	if !opt.Quiet {
		prog = func(s string) { fmt.Println(s) }
	}
	if opt.DoSync && !opt.Quiet {
		fmt.Println("▶ 开始校时：先获取网络标准时间...")
	}
	successList := []string{}
	var sources []DiagProbe
	// 进度条：非静默时才画（barActive() 内部再判一次 TTY，管道/重定向自动静默）
	res, err := queryBest(opt.NTP, opt.HTTP, opt.Timeout, opt.Retries, prog, &successList, &sources, !opt.Quiet)
	// 逐源健康度存入全局，供 Web 面板 /api/status 读取
	appState.mu.Lock()
	appState.LastSources = sources
	appState.mu.Unlock()
	if err != nil {
		msg := "所有时间源均不可用：" + err.Error()
		LogEvent(opt.LogPath, "失败(无可用源) err="+err.Error())
		if !opt.Quiet {
			fmt.Println("✗ " + msg)
		}
		return RunResult{Status: StatusFail, Error: msg}
	}

	ntpLocal := res.DT
	rr := RunResult{
		Status:  StatusOK,
		Server:  res.Server,
		Kind:    res.Kind,
		Offset:  res.Offset,
		Delay:   res.Delay,
		DidSync: opt.DoSync,
		DT:      ntpLocal,
	}
	if !opt.Quiet {
		kindLabel := "NTP"
		if res.Kind == "http" {
			kindLabel = "HTTP兜底"
		}
		fmt.Printf("时间源       : %s (%s)\n", res.Server, kindLabel)
		fmt.Printf("网络标准时间 : %s (本地 %s)\n",
			ntpLocal.UTC().Format("2006-01-02 15:04:05.000 UTC"),
			ntpLocal.Local().Format("2006-01-02 15:04:05.000"))
		fmt.Printf("本机当前时间 : %s\n", time.Now().Format("2006-01-02 15:04:05.000"))
		fmt.Printf("时间偏差     : %+.3f 秒\n", res.Offset)
		if res.Kind == "ntp" {
			fmt.Printf("往返延迟     : %.3f 秒\n", res.Delay)
		}
	}

	if !opt.DoSync {
		return rr
	}

	region := "HTTP兜底"
	if res.Kind == "ntp" {
		region = serverRegionLabel(res.Server)
	}
	if res.Kind == "http" && !opt.Quiet {
		fmt.Println("· 注意：当前走 HTTP 兜底源，精度仅秒级（±1s），校正可能有误差。")
	}
	if !opt.Force && absFloat(res.Offset) < opt.Threshold {
		if !opt.Quiet {
			fmt.Printf("· 偏差 %.3f 秒 < 阈值 %.2f 秒，跳过写时钟（--force 可强制）。\n", res.Offset, opt.Threshold)
		}
		LogEvent(opt.LogPath, fmt.Sprintf("跳过 源=%s(%s) 偏差=%.3f 阈值=%.2f",
			res.Server, region, res.Offset, opt.Threshold))
		rr.Status = StatusSkip
		return rr
	}
	if !isAdmin() {
		msg := "校正系统时间需要管理员/root 权限"
		if !opt.Quiet {
			fmt.Println("✗ " + msg)
		}
		rr.Error = msg
		LogEvent(opt.LogPath, fmt.Sprintf("失败(无权限) 源=%s(%s)", res.Server, region))
		rr.Status = StatusFail
		return rr
	}
	// w32time 冲突提醒：系统时间服务在跑的话会周期性把时钟改回去，校了也白校。
	// 这条建议原先只在 --diag 里给出，正式校时路径一直没提示，导致"明明校正成功、
	// 过一会儿又偏了"却查不到原因。
	if queryW32TimeState() == "running" {
		rr.Warning = "w32time 系统时间服务正在运行，可能周期性覆盖本次校正结果；建议停用（菜单 11 / --disable-w32time）"
		if !opt.Quiet {
			fmt.Println("⚠ " + rr.Warning)
		}
	}
	if confirmBackward(res.Offset, opt.LogPath, opt.Quiet, opt.AllowBack) {
		rr.Error = "已取消（用户拒绝回拨时钟）"
		rr.Status = StatusFail
		return rr
	}
	target := time.Now().Add(time.Duration(res.Offset * float64(time.Second)))
	if !opt.Quiet {
		fmt.Printf("写入系统时钟：%s ...\n", target.Format("2006-01-02 15:04:05"))
	}
	if err := applyCorrection(target, opt); err != nil {
		if !opt.Quiet {
			fmt.Println("✗ 校正失败：" + err.Error())
		}
		rr.Error = err.Error()
		LogEvent(opt.LogPath, fmt.Sprintf("失败 源=%s(%s) err=%s", res.Server, region, err.Error()))
		rr.Status = StatusFail
		return rr
	}
	if !opt.Quiet {
		fmt.Println("✓ 已校正系统时间（毫秒级）。")
	}
	LogEvent(opt.LogPath, fmt.Sprintf("校正 源=%s(%s) 类型=%s 偏差=%.3f 延迟=%.3f",
		res.Server, region, res.Kind, res.Offset, res.Delay))
	// Recheck
	if !opt.Quiet {
		fmt.Println("校正后复测：重新查询网络标准时间...")
	}
	reuse := opt.ReuseNTP
	if len(reuse) == 0 && res.Kind == "ntp" {
		reuse = successList
	}
	if len(reuse) == 0 {
		reuse = opt.NTP
	}
	if r2, err2 := queryBest(reuse, opt.HTTP, opt.Timeout, opt.Retries, prog, nil, nil, !opt.Quiet); err2 == nil {
		if !opt.Quiet {
			fmt.Printf("校正后偏差   : %+.3f 秒\n", r2.Offset)
		}
		rr.AfterOff = r2.Offset
		rr.RecheckOK = true
		LogEvent(opt.LogPath, fmt.Sprintf("复测 偏差=%.3f", r2.Offset))
	} else if !opt.Quiet {
		fmt.Printf("校正后复测失败（%v），无法确认最终偏差。\n", err2)
	}
	return rr
}

// applyCorrection 写入系统时钟。
//
// 默认（也是绝大多数场景）是一次性跳变到目标时刻。开启平滑（Slew）且偏差在
// SlewMaxSec 以内时，改成把这段差距拆成 slewSteps 个小步进逐步逼近，时钟不再
// "啪"地跳一下——对日志时序、证书有效期校验、计划任务触发更友好。
//
// 这里是"分段逼近"而不是内核级 slew（SetSystemTimeAdjustment 需要进程长期存活
// 来维持调整量，一旦被杀会残留被改过的时钟调整值，风险不划算）。分段逼近没有
// 残留状态：中途被打断也只是停在"比原来更接近正确"的位置。
func applyCorrection(target time.Time, opt RunOptions) error {
	if !opt.Slew || opt.SlewMaxSec <= 0 {
		return syncSystemTime(target)
	}
	gap := target.Sub(time.Now())
	if gap < 0 {
		gap = -gap
	}
	// 太小没必要分段；太大则平滑耗时过长，直接跳变更实际
	if gap < 50*time.Millisecond || gap > time.Duration(opt.SlewMaxSec*float64(time.Second)) {
		return syncSystemTime(target)
	}
	// 自适应步数：偏差越大步数越多，但每一步的跨度控制在 ~0.5s 以内，
	// 且总逼近时长封顶 3s——既避免"啪"地大跳，也不至于小偏差时拖太久。
	gapSec := gap.Seconds()
	steps := int(math.Round(gapSec / 0.5))
	if steps < 5 {
		steps = 5
	}
	if steps > 60 {
		steps = 60
	}
	stepDelay := 120 * time.Millisecond
	if time.Duration(steps)*stepDelay > 3*time.Second {
		stepDelay = 3 * time.Second / time.Duration(steps)
	}
	if !opt.Quiet {
		fmt.Printf("· 平滑校时：%.3f 秒的偏差分 %d 步逼近（每步约 %.0fms）…\n",
			gapSec, steps, float64(stepDelay)/float64(time.Millisecond))
	}
	start := time.Now()
	for i := 1; i < steps; i++ {
		frac := float64(i) / float64(steps)
		want := start.Add(time.Duration(frac * float64(target.Sub(start))))
		if err := syncSystemTime(want); err != nil {
			return err
		}
		time.Sleep(stepDelay)
	}
	return syncSystemTime(target) // 最后一步精确落到目标
}

func serverRegionLabel(server string) string {
	s := strings.ToLower(server)
	switch {
	case strings.Contains(s, "aliyun"):
		return "中国 · 阿里云"
	case strings.Contains(s, "tencent"):
		return "中国 · 腾讯云"
	case strings.Contains(s, "ntp.ntsc"), strings.Contains(s, "ntsc.ac"):
		return "中国 · 国家授时中心"
	case strings.Contains(s, "edu.cn"), strings.Contains(s, "cernet"):
		return "中国 · 教育网"
	case strings.Contains(s, "cn.pool"), s == "cn.ntp.org.cn":
		return "中国 · pool"
	case strings.Contains(s, "apple"):
		return "全球 · Apple"
	case strings.Contains(s, "google"):
		return "全球 · Google"
	case strings.Contains(s, "cloudflare"):
		return "全球 · Cloudflare"
	case strings.Contains(s, "microsoft"), strings.Contains(s, "windows"):
		return "全球 · Windows"
	case strings.Contains(s, "nist"):
		return "美国 · NIST"
	case strings.Contains(s, "pool.ntp.org"):
		return "全球 · pool.ntp.org"
	}
	return "自定义"
}

// confirmBackward prompts the user when offset would rewind the clock significantly.
func confirmBackward(offset float64, logPath string, quiet, allowBack bool) bool {
	if offset >= 0 || absFloat(offset) < backwardWarnSec {
		return false
	}
	if allowBack || quiet {
		return false
	}
	fmt.Printf("注意：本次需要把时钟往回拨 %.3f 秒（系统时间将倒退，可能影响计划任务触发、证书有效期校验与日志时序）\n", absFloat(offset))
	LogEvent(logPath, fmt.Sprintf("回拨提醒 offset=%.3f", offset))
	if absFloat(offset) < backwardConfirmSec {
		return false
	}
	fmt.Print("确认回拨？[y/N]: ")
	var s string
	fmt.Scanln(&s)
	s = strings.ToLower(strings.TrimSpace(s))
	if s != "y" && s != "yes" {
		LogEvent(logPath, fmt.Sprintf("已取消(用户拒绝回拨) offset=%.3f", offset))
		fmt.Println("已取消，未修改系统时间。")
		return true
	}
	return false
}
