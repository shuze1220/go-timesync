package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

type PoolResult struct {
	Server      string
	Offset      float64
	Delay       float64
	Kind        string // "ntp" or "http"
	DT          time.Time
	Success     []string    // successful NTP servers (for recheck reuse)
	Uncertainty Uncertainty // 多源一致性（交叉验证）
}

// Uncertainty 描述多个可用时间源之间的离散程度（交叉验证 / 不确定度）。
// Count 为参与中位数的源数；Spread = max-min；Std 为总体标准差。
type Uncertainty struct {
	Count  int     `json:"count"`
	Spread float64 `json:"spread"`
	Std    float64 `json:"std"`
}

// computeUncertainty 由一组保留源偏差估计不确定度。不足 2 个源时只报数量。
func computeUncertainty(offs []float64) Uncertainty {
	u := Uncertainty{Count: len(offs)}
	if len(offs) < 2 {
		return u
	}
	min, max := offs[0], offs[0]
	sum := 0.0
	for _, v := range offs {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
		sum += v
	}
	mean := sum / float64(len(offs))
	var varSum float64
	for _, v := range offs {
		d := v - mean
		varSum += d * d
	}
	u.Spread = max - min
	u.Std = math.Sqrt(varSum / float64(len(offs)))
	return u
}

const (
	madFactor = 3.0
	madMinGap = 0.05
	madAbsGap = 1.0
)

// queryBest concurrently queries NTP sources, applies MAD outlier filter,
// and returns the lowest-delay source with the median offset. If all NTP
// fail, falls back to HTTP. onProgress is called with human-readable lines.
func queryBest(ntp, httpList []string, timeoutSec float64, retries int,
	onProgress func(string), successOut *[]string, sourcesOut *[]DiagProbe,
	showBar bool) (*PoolResult, error) {
	if onProgress == nil {
		onProgress = func(string) {}
	}
	addSuccess := func(name string) {
		if successOut == nil {
			return
		}
		*successOut = append(*successOut, name)
	}
	// sourcesOut: 逐源健康度（面板要用，复用 diag.go 的 DiagProbe）
	addSource := func(p DiagProbe) {
		if sourcesOut == nil {
			return
		}
		*sourcesOut = append(*sourcesOut, p)
	}
	// 进度条只在交互终端且调用方要求时才画（--quiet / 管道自动静默）
	bar := showBar && barActive()

	// === NTP phase ===
	results := make([]NTPResult, 0, len(ntp))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 12)
	done, okCnt, failCnt := 0, 0, 0
	if len(ntp) > 0 {
		onProgress(fmt.Sprintf("查询 NTP 时间源（%d 个，并发）：", len(ntp)))
	}
	for _, s := range ntp {
		wg.Add(1)
		sem <- struct{}{}
		go func(server string) {
			defer wg.Done()
			defer func() { <-sem }()
			res, err := queryNTP(server, timeoutSec, retries)
			mu.Lock()
			defer mu.Unlock()
			done++
			var line string
			if err != nil {
				failCnt++
				line = fmt.Sprintf("  ✗ NTP  %-22s 失败/超时", server)
				addSource(DiagProbe{Server: server, Kind: "ntp", OK: false, Error: err.Error()})
			} else {
				okCnt++
				results = append(results, res)
				line = fmt.Sprintf("  ✓ NTP  %-22s 延迟 %.2f 秒", server, res.Delay)
				addSource(DiagProbe{Server: server, Kind: "ntp", OK: true, Offset: res.Offset, Delay: res.Delay})
			}
			if bar {
				progressLine(line, done, len(ntp), okCnt, failCnt)
			} else {
				onProgress(line)
			}
		}(s)
	}
	wg.Wait()
	if bar && len(ntp) > 0 {
		barEnd()
	}

	if len(results) > 0 {
		kept, dropped := filterOutliers(results, func(r NTPResult) float64 { return r.Offset })
		for _, d := range dropped {
			onProgress(fmt.Sprintf("  · 剔除离群源 %s（偏差 %+.3f 秒，与中位数偏离过大）", d.Server, d.Offset))
		}
		// pick lowest delay among kept
		best := kept[0]
		for _, k := range kept[1:] {
			if k.Delay < best.Delay {
				best = k
			}
		}
		offs := make([]float64, 0, len(kept))
		successList := make([]string, 0, len(kept))
		for _, k := range kept {
			offs = append(offs, k.Offset)
			successList = append(successList, k.Server)
			addSuccess(k.Server)
		}
		med := median(offs)
		onProgress(fmt.Sprintf("  → 采用 %s（%d/%d 个 NTP 成功，偏差中位数 %.3f 秒）",
			best.Server, len(kept), len(ntp), med))
		return &PoolResult{
			Server: best.Server, Offset: med, Delay: best.Delay,
			Kind: "ntp", DT: best.DT, Success: successList,
			Uncertainty: computeUncertainty(offs),
		}, nil
	}

	// === HTTP fallback ===
	if len(httpList) > 0 {
		onProgress(fmt.Sprintf("NTP 全部失败，启用 HTTP 兜底源（%d 个，并发，秒级精度）：", len(httpList)))
		hres := make([]HTTPResult, 0, len(httpList))
		hdone, hok, hfail := 0, 0, 0
		for _, s := range httpList {
			wg.Add(1)
			sem <- struct{}{}
			go func(server string) {
				defer wg.Done()
				defer func() { <-sem }()
				res, err := queryHTTP(server, timeoutSec, retries)
				mu.Lock()
				defer mu.Unlock()
				hdone++
				var line string
				if err != nil {
					hfail++
					line = fmt.Sprintf("  ✗ HTTP %s 失败/超时", server)
					addSource(DiagProbe{Server: server, Kind: "http", OK: false, Error: err.Error()})
				} else {
					hok++
					hres = append(hres, res)
					line = fmt.Sprintf("  ✓ HTTP %-22s 秒级精度 耗时 %.2f 秒", server, res.RTT)
					addSource(DiagProbe{Server: server, Kind: "http", OK: true, Offset: res.Offset, Delay: res.RTT})
				}
				if bar {
					progressLine(line, hdone, len(httpList), hok, hfail)
				} else {
					onProgress(line)
				}
			}(s)
		}
		wg.Wait()
		if bar {
			barEnd()
		}
		if len(hres) > 0 {
			kept, _ := filterOutliersHTTP(hres, func(r HTTPResult) float64 { return r.Offset })
			best := kept[0]
			for _, k := range kept[1:] {
				if k.RTT < best.RTT {
					best = k
				}
			}
			offs := make([]float64, 0, len(kept))
			for _, k := range kept {
				offs = append(offs, k.Offset)
			}
			med := median(offs)
			onProgress(fmt.Sprintf("  → 采用 %s（HTTP 兜底，%d/%d 成功，偏差中位数 %.3f 秒，秒级精度）",
				best.Server, len(kept), len(httpList), med))
			return &PoolResult{
				Server: best.Server, Offset: med, Delay: best.RTT,
				Kind: "http", DT: best.DT,
				Uncertainty: computeUncertainty(offs),
			}, nil
		}
	}
	return nil, errors.New("所有 NTP 与 HTTP 时间源均不可用")
}

func filterOutliers[T any](items []T, key func(T) float64) (kept, dropped []T) {
	if len(items) < 3 {
		return items, nil
	}
	offs := make([]float64, len(items))
	for i, it := range items {
		offs[i] = key(it)
	}
	med := median(offs)
	devs := make([]float64, len(offs))
	for i, v := range offs {
		devs[i] = math.Abs(v - med)
	}
	mad := median(devs)
	thr := madFactor * mad
	if thr < madMinGap {
		thr = madMinGap
	}
	if mad == 0 {
		thr = madAbsGap
	}
	for i, it := range items {
		if math.Abs(offs[i]-med) <= thr {
			kept = append(kept, it)
		} else {
			dropped = append(dropped, it)
		}
	}
	if len(kept) < 2 {
		return items, nil
	}
	return kept, dropped
}

func filterOutliersHTTP[T any](items []T, key func(T) float64) (kept, dropped []T) {
	return filterOutliers(items, key)
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sorted := append([]float64(nil), xs...)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
