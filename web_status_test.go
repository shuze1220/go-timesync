package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestApiStatusAfterOff 锁定"校正后复测偏差"的输出约定：
//   - 查询动作（DidSync=false）→ after_off 必须为空（null），避免把 0 误显示成"已校准"
//   - 校时动作（DidSync=true）→ after_off 必须等于 RunResult.AfterOff
//
// 该测试不依赖网络：直接构造 appState.Last 并驱动 apiStatus 读取它。
func TestApiStatusAfterOff(t *testing.T) {
	// 只保存/恢复被测字段，避免整体复制 AppState 连带拷贝 sync.Mutex（vet 会报错）。
	oldLast := appState.Last
	oldLastAt := appState.LastAt
	oldLog := appState.LogPath
	oldSrc := appState.LastSources
	defer func() {
		appState.mu.Lock()
		appState.Last = oldLast
		appState.LastAt = oldLastAt
		appState.LogPath = oldLog
		appState.LastSources = oldSrc
		appState.mu.Unlock()
	}()

	// 基础环境：清空可能触发磁盘读写的字段
	appState.mu.Lock()
	appState.LogPath = ""
	appState.LastSources = nil
	appState.mu.Unlock()

	// 场景 1：查询（不写时钟）
	appState.mu.Lock()
	appState.Last = &RunResult{
		Status:  StatusOK,
		Server:  "ntp.aliyun.com",
		Offset:  -0.210,
		AfterOff: 0.0, // 查询不会跑复测，恒为 0
		DidSync: false,
		DT:      time.Now(),
	}
	appState.LastAt = time.Now()
	appState.mu.Unlock()

	rec := httptest.NewRecorder()
	apiStatus(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d", rec.Code)
	}
	var q map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &q); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if q["after_off"] != nil {
		t.Errorf("query run: after_off should be null, got %v", q["after_off"])
	}

	// 场景 2：校时（写时钟 + 复测成功 +0.002）
	appState.mu.Lock()
	appState.Last = &RunResult{
		Status:    StatusOK,
		Server:    "ntp.aliyun.com",
		Offset:    -0.210,
		AfterOff:  0.002,
		DidSync:   true,
		RecheckOK: true,
		DT:        time.Now(),
	}
	appState.LastAt = time.Now()
	appState.mu.Unlock()

	rec2 := httptest.NewRecorder()
	apiStatus(rec2, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("status code = %d", rec2.Code)
	}
	var s map[string]interface{}
	if err := json.Unmarshal(rec2.Body.Bytes(), &s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ao, ok := s["after_off"].(float64)
	if !ok {
		t.Fatalf("sync run: after_off missing or wrong type: %v", s["after_off"])
	}
	if ao != 0.002 {
		t.Errorf("sync run: after_off = %v, want 0.002", ao)
	}

	// 场景 3：校时成功但复测因网络抖动失败（RecheckOK=false）→ after_off 必须为空，
	// 否则会误显 "0.000s 校准到位"。复测是否成功用 recheck_ok 标记。
	appState.mu.Lock()
	appState.Last = &RunResult{
		Status:   StatusOK,
		Server:   "ntp.aliyun.com",
		Offset:   -0.210,
		AfterOff: 0.0, // 复测失败，值无意义
		DidSync:  true,
		RecheckOK: false,
		DT:       time.Now(),
	}
	appState.LastAt = time.Now()
	appState.mu.Unlock()

	rec3 := httptest.NewRecorder()
	apiStatus(rec3, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rec3.Code != http.StatusOK {
		t.Fatalf("status code = %d", rec3.Code)
	}
	var u map[string]interface{}
	if err := json.Unmarshal(rec3.Body.Bytes(), &u); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if u["after_off"] != nil {
		t.Errorf("recheck-failed run: after_off should be null, got %v", u["after_off"])
	}
	if u["did_sync"] != true {
		t.Errorf("recheck-failed run: did_sync should be true, got %v", u["did_sync"])
	}
	if u["recheck_ok"] != false {
		t.Errorf("recheck-failed run: recheck_ok should be false, got %v", u["recheck_ok"])
	}
}
