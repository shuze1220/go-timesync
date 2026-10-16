package main

import (
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"sync"
	"time"
)

// DiagProbe is the result of probing one time source during --diag.
type DiagProbe struct {
	Server string  `json:"server"`
	Kind   string  `json:"kind"` // "ntp" | "http"
	OK     bool    `json:"ok"`
	Offset float64 `json:"offset,omitempty"`
	Delay  float64 `json:"delay,omitempty"`
	Error  string  `json:"error,omitempty"`
}

// DiagReport is the structured form of a --diag run (used by --json).
type DiagReport struct {
	Version   string      `json:"version"`
	OS        string      `json:"os"`
	Arch      string      `json:"arch"`
	IsAdmin   bool        `json:"is_admin"`
	Timezone  string      `json:"timezone"`
	LocalTime string      `json:"local_time"`
	W32Time   string      `json:"w32time,omitempty"`
	Advice    string      `json:"advice,omitempty"`
	Probes    []DiagProbe `json:"probes"`
	OKCount   int         `json:"ok_count"`
	FailCount int         `json:"fail_count"`
}

// RunDiag probes environment + every configured time source WITHOUT touching
// the system clock. Results are printed in the original list order (so output
// is stable and diff-able) even though probing runs concurrently — otherwise
// 12 sources × timeout × retries can take minutes serially.
func RunDiag(ntp, httpList []string, timeoutSec float64, retries int, showBar bool) DiagReport {
	zone, _ := time.Now().Zone()
	rep := DiagReport{
		Version:   Version,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		IsAdmin:   isAdmin(),
		Timezone:  fmt.Sprintf("%s (UTC%+d)", zone, offsetHours()),
		LocalTime: time.Now().Format("2006-01-02 15:04:05.000"),
	}

	// w32time state + advice (Windows only)
	if runtime.GOOS == "windows" {
		st := queryW32TimeState()
		rep.W32Time = st
		switch st {
		case "running":
			rep.Advice = "w32time 正在运行，可能周期性覆盖本工具的校正结果；建议用 --disable-w32time 停用。"
		case "disabled":
			rep.Advice = "w32time 已禁用，本工具可独占校时。"
		}
	}

	// Probe concurrently, then emit in original order.
	type indexed struct {
		i     int
		p     DiagProbe
		isNtp bool
	}
	total := len(ntp) + len(httpList)
	results := make([]indexed, 0, total)
	var mu sync.Mutex
	var wg sync.WaitGroup
	// 进度条（--json 时必须关掉，否则 \r 会污染 JSON 输出）
	bar := showBar && barActive()
	done, okCnt, failCnt := 0, 0, 0

	probe := func(i int, isNtp bool, server string) {
		defer wg.Done()
		p := DiagProbe{Server: server}
		if isNtp {
			p.Kind = "ntp"
			r, err := queryNTP(server, timeoutSec, retries)
			if err != nil {
				p.OK = false
				p.Error = err.Error()
			} else {
				p.OK = true
				p.Offset = r.Offset
				p.Delay = r.Delay
			}
		} else {
			p.Kind = "http"
			r, err := queryHTTP(server, timeoutSec, retries)
			if err != nil {
				p.OK = false
				p.Error = err.Error()
			} else {
				p.OK = true
				p.Offset = r.Offset
				p.Delay = r.RTT
			}
		}
		mu.Lock()
		results = append(results, indexed{i: i, p: p, isNtp: isNtp})
		done++
		if p.OK {
			okCnt++
		} else {
			failCnt++
		}
		if bar {
			barDraw(done, total, okCnt, failCnt)
		}
		mu.Unlock()
	}

	for i, s := range ntp {
		wg.Add(1)
		go probe(i, true, s)
	}
	for i, s := range httpList {
		wg.Add(1)
		go probe(i, false, s)
	}
	wg.Wait()
	if bar {
		barEnd()
	}

	// Stable order: NTP first (original index), then HTTP (original index).
	sort.SliceStable(results, func(a, b int) bool {
		if results[a].isNtp != results[b].isNtp {
			return results[a].isNtp
		}
		return results[a].i < results[b].i
	})
	for _, r := range results {
		rep.Probes = append(rep.Probes, r.p)
		if r.p.OK {
			rep.OKCount++
		} else {
			rep.FailCount++
		}
	}
	return rep
}

// PrintDiag renders the human-readable self-check report.
func PrintDiag(rep DiagReport) {
	fmt.Printf("==== time_sync 自检 (diag) v%s ====\n", rep.Version)
	fmt.Printf("操作系统     : %s/%s\n", rep.OS, rep.Arch)
	if rep.IsAdmin {
		fmt.Println("管理员权限   : 是")
	} else {
		fmt.Println("管理员权限   : 否（校正系统时间需管理员/root）")
	}
	fmt.Printf("时区         : %s\n", rep.Timezone)
	fmt.Printf("本机时间     : %s\n", rep.LocalTime)
	if rep.W32Time != "" {
		fmt.Printf("系统时间服务 : w32time（%s）\n", rep.W32Time)
	}
	if rep.Advice != "" {
		fmt.Println("  ! " + rep.Advice)
	}

	fmt.Println("---- NTP 源 ----")
	for _, p := range rep.Probes {
		if p.Kind != "ntp" {
			continue
		}
		if p.OK {
			fmt.Printf("  [OK]   %-26s 偏差 %+.3fs  延迟 %.3fs\n", p.Server, p.Offset, p.Delay)
		} else {
			fmt.Printf("  [FAIL] %-26s %s\n", p.Server, p.Error)
		}
	}
	fmt.Println("---- HTTP 兜底源（秒级精度） ----")
	for _, p := range rep.Probes {
		if p.Kind != "http" {
			continue
		}
		if p.OK {
			fmt.Printf("  [OK]   %-26s 偏差 %+.3fs  往返 %.3fs\n", p.Server, p.Offset, p.Delay)
		} else {
			fmt.Printf("  [FAIL] %-26s %s\n", p.Server, p.Error)
		}
	}
	fmt.Printf("---- 合计：成功 %d / 失败 %d ----\n", rep.OKCount, rep.FailCount)
}

// PrintDiagJSON emits the same report as JSON (--json).
func PrintDiagJSON(rep DiagReport) error {
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

// offsetHours returns the local UTC offset in whole hours (for display).
func offsetHours() int {
	_, off := time.Now().Zone()
	return off / 3600
}
