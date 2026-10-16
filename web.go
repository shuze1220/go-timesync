package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web
var webFS embed.FS

// DefaultListen 是 Web 控制面板的默认监听地址（仅本机回环，外部无法访问）。
const DefaultListen = "127.0.0.1:8765"

// listenSpan 端口被占用时向后顺延探测的个数（8765..8765+listenSpan-1）。
const listenSpan = 10

// AppState holds runtime state shared by HTTP handlers and the sync trigger.
type AppState struct {
	mu           sync.Mutex
	Cfg          Config
	CfgPath      string
	Last         *RunResult
	LastAt       time.Time
	LastSources  []DiagProbe // 最近一次查询的逐源健康度（面板用）
	LogPath      string
	Running      bool
	WebRunning   bool   // web panel started from interactive menu (background)
	WebURL       string // 面板真实地址（端口可能顺延，不能硬编码 8765）
	Standalone   bool   // 由 --web 拉起的独立面板进程，允许自助关闭
	SchedulerOK  bool
	StartupOK    bool
	W32TimeState string // "auto"|"disabled"|"unknown"
}

var appState = &AppState{}

func init() {
	// Best-effort initial load (main() will overwrite)
	cfg, _ := LoadConfig("")
	appState.Cfg = cfg
	appState.CfgPath = cfg.Path
	appState.LogPath = cfg.Log
	appState.W32TimeState = queryW32TimeState()
	appState.SchedulerOK = checkSchedulerInstalled()
	appState.StartupOK = checkStartupInstalled(false)
}

// buildPanelMux 组装面板的全部路由。
func buildPanelMux() (*http.ServeMux, error) {
	mux := http.NewServeMux()

	// Static files: serve the embedded web/ directory at /static/
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		return nil, err
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(sub))))

	// API routes
	mux.HandleFunc("GET /api/status", apiStatus)
	mux.HandleFunc("POST /api/query", apiQuery)
	mux.HandleFunc("POST /api/sync", apiSync)
	mux.HandleFunc("GET /api/config", apiGetConfig)
	mux.HandleFunc("POST /api/config", apiSetConfig)
	mux.HandleFunc("GET /api/log", apiLog)
	mux.HandleFunc("GET /api/log/stream", apiLogStream)
	mux.HandleFunc("GET /api/history", apiHistory)
	mux.HandleFunc("POST /api/diag", apiDiag)
	mux.HandleFunc("GET /api/drift", apiDrift)
	mux.HandleFunc("GET /api/integrations", apiIntegrations)
	mux.HandleFunc("GET /api/presets", apiPresets)
	mux.HandleFunc("POST /api/scheduler/toggle", apiSchedulerToggle)
	mux.HandleFunc("POST /api/startup/toggle", apiStartupToggle)
	mux.HandleFunc("POST /api/w32time/toggle", apiW32Toggle)
	mux.HandleFunc("POST /api/shutdown", apiShutdown)

	// Index
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		data, err := webFS.ReadFile("web/index.html")
		if err != nil {
			http.Error(w, "index missing", 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			return
		}
		http.NotFound(w, r)
	})
	return mux, nil
}

// serveOn 在已建立的监听器上开始服务（后台 goroutine），返回可访问 URL。
func serveOn(ln net.Listener) string {
	mux, err := buildPanelMux()
	if err != nil {
		_ = ln.Close()
		return ""
	}
	srv := &http.Server{
		Handler:           authWrap(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		_ = srv.Serve(ln)
	}()
	return "http://" + ln.Addr().String()
}

// authWrap 在配置了 api_token 且请求来自非本机回环时，要求请求携带令牌。
// 本机 127.0.0.1 / ::1 访问始终免 token，保证开箱即用；局域网 / 公网访问则强制校验，
// 防止同网段其他进程未授权调用校时、改配置等敏感操作。
// 令牌可放在 Authorization: Bearer <t>、X-API-Token 头，或 URL ?token=<t>（方便浏览器直开面板）。
func authWrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		appState.mu.Lock()
		want := appState.Cfg.APIToken
		appState.mu.Unlock()
		if want == "" {
			next.ServeHTTP(w, r)
			return
		}
		// 本机回环不校验：RemoteAddr 形如 "127.0.0.1:54321" 或 "[::1]:54321"
		ra := r.RemoteAddr
		if strings.Contains(ra, "127.0.0.1") || strings.Contains(ra, "::1") {
			next.ServeHTTP(w, r)
			return
		}
		if validToken(r, want) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "需要 API token：在 URL 加 ?token=<令牌>，或请求头 Authorization: Bearer <令牌> / X-API-Token: <令牌>",
		})
	})
}

func validToken(r *http.Request, want string) bool {
	if ah := r.Header.Get("Authorization"); strings.HasPrefix(ah, "Bearer ") {
		if ah[len("Bearer "):] == want {
			return true
		}
	}
	if r.Header.Get("X-API-Token") == want {
		return true
	}
	if r.URL.Query().Get("token") == want {
		return true
	}
	return false
}

// ServeWebPanel 在 listen 上启动面板；若端口被占用则自动向后顺延。
// 返回真实可访问的 URL —— 调用方必须用这个 URL，而不是入参。
func ServeWebPanel(listen string) (string, error) {
	if listen == "" {
		listen = DefaultListen
	}
	host, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		// 非法地址：原样试一次，让错误信息更直观
		ln, lerr := net.Listen("tcp", listen)
		if lerr != nil {
			return "", lerr
		}
		return serveOn(ln), nil
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		ln, lerr := net.Listen("tcp", listen)
		if lerr != nil {
			return "", lerr
		}
		return serveOn(ln), nil
	}
	var lastErr error
	for i := 0; i < listenSpan; i++ {
		addr := net.JoinHostPort(host, strconv.Itoa(port+i))
		ln, lerr := net.Listen("tcp", addr)
		if lerr != nil {
			lastErr = lerr
			continue
		}
		if i > 0 {
			fmt.Printf("  注意：端口 %d 已被占用，面板自动改用 %d。\n", port, port+i)
		}
		return serveOn(ln), nil
	}
	return "", lastErr
}

// panelIsOurs 判断该地址上跑的是不是本工具的面板。
//
// 只探测 TCP 端口是不够的：8765 可能被别的程序占着，那样把浏览器指过去会打开
// 一个毫不相干的服务。所以这里真的发一次 /api/status，并校验返回里有 version 字段。
func panelIsOurs(listen string) bool {
	c := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := c.Get("http://" + listen + "/api/status")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return false
	}
	return strings.Contains(string(body), `"version"`)
}

// StartWebServer starts the HTTP control panel on the given listen address
// (e.g. "127.0.0.1:8765"). It returns the actual URL it's serving.
func StartWebServer(listen string) (string, error) {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return "", err
	}
	return serveOn(ln), nil
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func apiStatus(w http.ResponseWriter, r *http.Request) {
	appState.mu.Lock()
	defer appState.mu.Unlock()
	now := time.Now()
	resp := map[string]interface{}{
		"version":          Version,
		"os":               runtime.GOOS,
		"is_admin":         isAdmin(),
		"local_time":       now.Local().Format("2006-01-02 15:04:05.000"),
		"utc_time":         now.UTC().Format("2006-01-02 15:04:05 UTC"),
		"reference_time":   "",
		"reference_source": "",
		"offset_seconds":   0.0,
		"need_sync":        false,
		"last_sync":        "",
		"last_result":      "",
		"warning":          "",
		"drift_ppm":        (*float64)(nil),
		"did_sync":         false,
		"recheck_ok":       false,
		"after_off":        (*float64)(nil), // 校正后复测偏差；仅在本次为校时且复测成功时回填
		"log_path":         appState.LogPath,
		"config_path":      appState.CfgPath,
		"running":          appState.Running,
		"sources":          appState.LastSources,
		"uncertainty":      Uncertainty{},
	}
	if appState.Last != nil {
		last := *appState.Last
		resp["uncertainty"] = last.Uncertainty
		resp["last_sync"] = appState.LastAt.Format("2006-01-02 15:04:05")
		resp["last_result"] = statusString(last.Status)
		resp["warning"] = last.Warning
		resp["reference_time"] = last.DT.Local().Format("2006-01-02 15:04:05")
		resp["reference_source"] = last.Server
		resp["offset_seconds"] = last.Offset
		// 校正后复测偏差：只有"校时"动作才有意义（查询动作不会写时钟，复测值恒为 0 会误导）。
		// DidSync 由 RunOnce 按 DoSync 标记，菜单/面板/定时循环三类校时路径都覆盖。
		// RecheckOK 标记复测是否真的执行成功——网络抖动导致复测失败时不应显示 0.000s，
		// 否则会误判成"校准到位"。此时面板改为提示"复测未执行"。
		if last.DidSync {
			resp["did_sync"] = true
			if last.RecheckOK {
				ao := last.AfterOff
				resp["after_off"] = &ao
				resp["recheck_ok"] = true
			} else {
				resp["recheck_ok"] = false
			}
		}
		// 结构化估计漂移：取代早先对 ReportDrift 文本做 strings.Contains("ppm") 的硬解析，
		// 日志文案怎么改都不会再影响面板拿到的 ppm 数值。
		if appState.LogPath != "" {
			if ppm, ok := EstimateDrift(appState.LogPath, 2); ok {
				resp["drift_ppm"] = ppm
			}
		}
		if last.Offset != 0 {
			resp["need_sync"] = absFloat(last.Offset) > 0.5
		}
	}
	writeJSON(w, 200, resp)
}

func statusString(s Status) string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusSkip:
		return "skip"
	}
	return "fail"
}

// apiQuery performs a query-only NTP/HTTP check (no system time change)
// and caches the result so the status endpoint can serve it.
func apiQuery(w http.ResponseWriter, r *http.Request) {
	appState.mu.Lock()
	if appState.Running {
		appState.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "已有任务在运行"})
		return
	}
	appState.Running = true
	cfg := appState.Cfg
	appState.mu.Unlock()
	go func() {
		res := RunOnce(RunOptions{
			NTP:       cfg.NTP,
			HTTP:      cfg.HTTP,
			Timeout:   cfg.Timeout,
			Retries:   cfg.Retries,
			Threshold: cfg.Threshold,
			DoSync:    false, // query only
			Quiet:     true,
			LogPath:   cfg.Log,
			AllowBack: true,
		})
		appState.mu.Lock()
		appState.Last = &res
		appState.LastAt = time.Now()
		appState.Running = false
		appState.mu.Unlock()
	}()
	writeJSON(w, 202, map[string]string{"status": "querying"})
}

// probeOffset 做一次只读查询拿当前偏差，供面板"回拨确认"判定。
func probeOffset(cfg Config) (float64, error) {
	res, err := queryBest(cfg.NTP, cfg.HTTP, cfg.Timeout, cfg.Retries,
		func(string) {}, nil, nil, false)
	if err != nil {
		return 0, err
	}
	if res == nil {
		return 0, fmt.Errorf("无可用时间源")
	}
	return res.Offset, nil
}

func apiSync(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AllowBack bool `json:"allow_back"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	appState.mu.Lock()
	if appState.Running {
		appState.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "已有校准任务在运行"})
		return
	}
	appState.Running = true
	cfg := appState.Cfg
	appState.mu.Unlock()

	// 干跑：先探一次偏差。
	//
	// 面板路径跑在无人值守的 goroutine 里，绝不能出现 fmt.Scanln 这类阻塞等输入；
	// 而原先直接 AllowBack=true 又把"大幅回拨"的确认整个跳过了——用户点一下
	// 「立即校准」，时钟可能被静默拨回好几个小时。这里改成：达到回拨确认阈值
	// 且前端尚未确认时，返回 409，把"要回拨多少秒"明确交给前端弹窗。
	if !req.AllowBack {
		if off, err := probeOffset(cfg); err == nil && off <= -backwardConfirmSec {
			appState.mu.Lock()
			appState.Running = false
			appState.mu.Unlock()
			// 用 200 + status 而不是 409：前端 api() 对非 2xx 会直接 toast 并抛异常，
			// 那样就拿不到 offset 去把"回拨多少秒"讲清楚了。
			writeJSON(w, 200, map[string]interface{}{
				"status":  "need_confirm",
				"offset":  off,
				"message": fmt.Sprintf("本次需要把时钟回拨 %.3f 秒（系统时间将倒退，可能影响计划任务触发、证书有效期校验与日志时序）", -off),
			})
			return
		}
	}

	// 非管理员：与集成开关一致，提权重开一个带标签的子进程去校时（弹 UAC）。
	if !isAdmin() {
		appState.mu.Lock()
		appState.Running = false
		appState.mu.Unlock()
		if relaunchElevatedForWeb("sync") {
			writeJSON(w, 202, map[string]string{"status": "elevating"})
		} else {
			writeJSON(w, 403, map[string]string{"error": "校时需要管理员权限，且 UAC 提权被取消"})
		}
		return
	}
	go func() {
		res := RunOnce(RunOptions{
			NTP:        cfg.NTP,
			HTTP:       cfg.HTTP,
			Timeout:    cfg.Timeout,
			Retries:    cfg.Retries,
			Threshold:  cfg.Threshold,
			Force:      true,
			DoSync:     true,
			Quiet:      true, // 无人值守：不打印，也绝不会阻塞等输入
			LogPath:    cfg.Log,
			AllowBack:  true, // 大幅回拨已由上面的 409 交给前端确认过
			Slew:       cfg.Slew,
			SlewMaxSec: cfg.SlewMaxSec,
		})
		appState.mu.Lock()
		appState.Last = &res
		appState.LastAt = time.Now()
		appState.Running = false
		appState.mu.Unlock()
	}()
	writeJSON(w, 202, map[string]string{"status": "started"})
}

func apiGetConfig(w http.ResponseWriter, r *http.Request) {
	appState.mu.Lock()
	defer appState.mu.Unlock()
	writeJSON(w, 200, appState.Cfg)
}

func apiSetConfig(w http.ResponseWriter, r *http.Request) {
	var req Config
	body, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "JSON 解析失败: " + err.Error()})
		return
	}
	// 字段"是否出现"必须与"值是多少"分开判断。
	//
	// 反例：applyPreset 只 POST {ntp:[...]}，若直接 cur.Log = req.Log，
	// 缺失的 Log 会被当成空串写回——日志被静默关掉，Force/Slew 同理被清零。
	// 所以下面凡是"省略即保持原值"的字段，都先查原始 JSON 里有没有这个键。
	var raw map[string]interface{}
	_ = json.Unmarshal(body, &raw)
	has := func(k string) bool { _, ok := raw[k]; return ok }

	// Empty/missing fields -> keep current
	appState.mu.Lock()
	cur := appState.Cfg
	if req.NTP != nil {
		cur.NTP = req.NTP
	}
	if req.HTTP != nil {
		cur.HTTP = req.HTTP
	}
	if req.Timeout > 0 {
		cur.Timeout = req.Timeout
	}
	if req.Threshold > 0 {
		cur.Threshold = req.Threshold
	}
	if has("force") {
		cur.Force = req.Force
	}
	if req.Retries >= 0 {
		cur.Retries = req.Retries
	}
	if req.Interval > 0 {
		cur.Interval = req.Interval
	}
	if has("log") {
		cur.Log = req.Log
	}
	if has("slew") {
		cur.Slew = req.Slew
	}
	if has("slew_max") && req.SlewMaxSec > 0 {
		cur.SlewMaxSec = req.SlewMaxSec
	}
	if has("api_token") {
		// 允许显式置空（清空令牌 = 关闭校验，回到本机免 token 的默认行为）
		cur.APIToken = req.APIToken
	}
	path := appState.CfgPath
	appState.Cfg = cur
	appState.LogPath = cur.Log
	appState.mu.Unlock()

	// Persist server list to disk; keep file comments intact.
	if _, err := SaveServers(path, cur.NTP, cur.HTTP); err != nil {
		writeJSON(w, 500, map[string]string{"error": "保存失败: " + err.Error()})
		return
	}
	// Write full cfg (sync section) too: simple approach — overwrite entire ini
	// (we want to preserve comments, so re-read and replace only known keys)
	if err := saveSyncSection(path, cur); err != nil {
		writeJSON(w, 500, map[string]string{"error": "保存 sync 段失败: " + err.Error()})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func saveSyncSection(path string, c Config) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	// Drop trailing empty
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	syncStart := -1
	syncEnd := len(lines)
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			name := strings.ToLower(strings.TrimSpace(t[1 : len(t)-1]))
			if name == "sync" {
				syncStart = i
			} else if syncStart >= 0 {
				syncEnd = i
				break
			}
		}
	}
	// Build replacement
	var newSection []string
	if syncStart < 0 {
		newSection = append(newSection, "", "[sync]")
	} else {
		// keep [sync] line
		newSection = append(newSection, lines[syncStart])
	}
	forceStr := "false"
	if c.Force {
		forceStr = "true"
	}
	newSection = append(newSection,
		fmt.Sprintf("timeout = %.2f", c.Timeout),
		fmt.Sprintf("threshold = %.2f", c.Threshold),
		"force = "+forceStr,
		fmt.Sprintf("interval = %d", c.Interval),
		fmt.Sprintf("retries = %d", c.Retries),
	)
	if c.Log != "" {
		newSection = append(newSection, "log = "+c.Log)
	}
	slewStr := "false"
	if c.Slew {
		slewStr = "true"
	}
	newSection = append(newSection,
		"slew = "+slewStr,
		fmt.Sprintf("slew_max = %.0f", c.SlewMaxSec),
	)
	if c.APIToken != "" {
		// 令牌含随机字符，单独成行写回；置空时（默认）不写入任何 api_token 行
		newSection = append(newSection, "api_token = "+c.APIToken)
	}
	if syncStart < 0 {
		// append
		lines = append(lines, newSection...)
	} else {
		// splice
		out := append([]string{}, lines[:syncStart]...)
		out = append(out, newSection...)
		lines = append(out, lines[syncEnd:]...)
	}
	text := strings.Join(lines, "\n") + "\n"
	return os.WriteFile(path, []byte(text), 0644)
}

func apiLog(w http.ResponseWriter, r *http.Request) {
	tail := 200
	if v := r.URL.Query().Get("tail"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			tail = n
		}
	}
	appState.mu.Lock()
	path := appState.LogPath
	appState.mu.Unlock()
	if path == "" {
		writeJSON(w, 200, map[string]interface{}{"lines": []string{}, "path": ""})
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		writeJSON(w, 200, map[string]interface{}{"lines": []string{}, "path": path, "error": err.Error()})
		return
	}
	all := strings.Split(string(data), "\n")
	if tail > 0 && len(all) > tail {
		all = all[len(all)-tail:]
	}
	writeJSON(w, 200, map[string]interface{}{"lines": all, "path": path})
}

// apiLogStream streams the log tail over SSE (Server-Sent Events).
// 前端 EventSource 连的就是这个端点；此前它未注册导致 404、面板反复显示"断连"。
func apiLogStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // 禁用 nginx 缓冲

	send := func(event, data string) {
		// SSE 帧内不能出现裸换行，逐行加 data: 前缀
		for _, ln := range strings.Split(data, "\n") {
			fmt.Fprintf(w, "data: %s\n", ln)
		}
		fmt.Fprint(w, "\n")
		flusher.Flush()
	}

	appState.mu.Lock()
	path := appState.LogPath
	appState.mu.Unlock()

	if path == "" {
		send("info", "（未配置日志文件，实时日志不可用）")
	} else if _, err := os.Stat(path); err != nil {
		send("info", "（日志文件尚不存在，有校正记录后自动出现）")
	}

	// 先补发已有尾部，避免打开面板时日志区空白
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
			if len(lines) > 50 {
				lines = lines[len(lines)-50:]
			}
			for _, ln := range lines {
				if strings.TrimSpace(ln) != "" {
					send("log", ln)
				}
			}
		}
	}

	lastSize := int64(0)
	if path != "" {
		if fi, err := os.Stat(path); err == nil {
			lastSize = fi.Size()
		}
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	hb := 0
	for {
		select {
		case <-r.Context().Done():
			return // 浏览器关掉页面/刷新，立即退出，不泄漏 goroutine
		case <-ticker.C:
			hb++
			if hb%15 == 0 {
				fmt.Fprint(w, ": ping\n\n") // 心跳，防代理掐连接
				flusher.Flush()
			}
			if path == "" {
				continue
			}
			fi, err := os.Stat(path)
			if err != nil {
				continue
			}
			if fi.Size() < lastSize {
				lastSize = fi.Size() // 日志被轮转/清空
				continue
			}
			if fi.Size() == lastSize {
				continue
			}
			f, err := os.Open(path)
			if err != nil {
				continue
			}
			if _, err := f.Seek(lastSize, 0); err == nil {
				chunk := make([]byte, fi.Size()-lastSize)
				if n, _ := f.Read(chunk); n > 0 {
					for _, ln := range strings.Split(strings.TrimRight(string(chunk[:n]), "\n"), "\n") {
						if strings.TrimSpace(ln) != "" {
							send("log", ln)
						}
					}
				}
			}
			f.Close()
			lastSize = fi.Size()
		}
	}
}

// apiHistory 返回最近 N 次校正记录（供面板趋势图）。
func apiHistory(w http.ResponseWriter, r *http.Request) {
	n := 30
	if v := r.URL.Query().Get("n"); v != "" {
		if i, err := strconv.Atoi(v); err == nil && i > 0 {
			n = i
		}
	}
	appState.mu.Lock()
	path := appState.LogPath
	appState.mu.Unlock()
	rows := ParseHistory(path, n)
	if rows == nil {
		rows = []HistoryPoint{}
	}
	writeJSON(w, 200, map[string]interface{}{"points": rows, "path": path})
}

// apiDiag 全量探测所有已配置时间源（不改系统时钟），供面板"时间源健康度"按需扫描。
// 与 /api/query 的区别：query 只要 NTP 成功就直接返回、不再探测 HTTP；
// diag 则 NTP + HTTP 一律全测，方便判断兜底源是否还活着。
func apiDiag(w http.ResponseWriter, r *http.Request) {
	appState.mu.Lock()
	if appState.Running {
		appState.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "已有任务在运行"})
		return
	}
	appState.Running = true
	cfg := appState.Cfg
	appState.mu.Unlock()
	go func() {
		rep := RunDiag(cfg.NTP, cfg.HTTP, cfg.Timeout, cfg.Retries, false)
		appState.mu.Lock()
		appState.LastSources = rep.Probes
		appState.Running = false
		appState.mu.Unlock()
	}()
	writeJSON(w, 202, map[string]string{"status": "probing"})
}

// apiShutdown 让独立运行的面板进程自助退出（页面上"关闭面板"按钮）。
//
// 只有 --web 拉起的独立进程才允许自杀：如果面板是跑在交互菜单进程里的，
// 一个 HTTP 请求把整个菜单进程干掉会非常意外，所以那种情况一律拒绝。
//
// 关于正在运行的任务：面板一打开前端就会自动做一次时间源检测，所以"刚打开就点
// 关闭"必然撞上 Running=true。早先直接返回 409，用户会觉得按钮坏了。现在改为
// 给任务最多 8 秒收尾时间（避免在改写系统时钟的半途硬退），超时也照样退出。
func apiShutdown(w http.ResponseWriter, r *http.Request) {
	appState.mu.Lock()
	standalone := appState.Standalone
	appState.mu.Unlock()
	if !standalone {
		writeJSON(w, 400, map[string]string{"error": "面板当前依附于控制台菜单进程，请在菜单里按 0/q 退出"})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "shutting-down"})
	go func() {
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			appState.mu.Lock()
			busy := appState.Running
			appState.mu.Unlock()
			if !busy {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		time.Sleep(250 * time.Millisecond) // 留出响应送达浏览器的时间
		os.Exit(0)
	}()
}

func apiDrift(w http.ResponseWriter, r *http.Request) {
	tail := 10
	if v := r.URL.Query().Get("tail"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			tail = n
		}
	}
	appState.mu.Lock()
	path := appState.LogPath
	appState.mu.Unlock()
	writeJSON(w, 200, map[string]interface{}{"text": ReportDrift(path, tail), "path": path})
}

func apiIntegrations(w http.ResponseWriter, r *http.Request) {
	appState.mu.Lock()
	defer appState.mu.Unlock()
	writeJSON(w, 200, map[string]interface{}{
		"scheduler": appState.SchedulerOK,
		"startup":   appState.StartupOK,
		"w32time":   appState.W32TimeState,
	})
}

func apiPresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, Presets)
}

func apiSchedulerToggle(w http.ResponseWriter, r *http.Request) {
	if runtime.GOOS != "windows" {
		writeJSON(w, 400, map[string]string{"error": "仅 Windows 支持"})
		return
	}
	appState.mu.Lock()
	installed := appState.SchedulerOK
	appState.mu.Unlock()
	if installed {
		removeScheduler()
	} else {
		if !isAdmin() {
			if relaunchElevatedForWeb("scheduler:install") {
				writeJSON(w, 202, map[string]string{"status": "elevating"})
				return
			}
			writeJSON(w, 403, map[string]string{"error": "需要管理员权限"})
			return
		}
		installScheduler(60)
	}
	appState.mu.Lock()
	appState.SchedulerOK = checkSchedulerInstalled()
	appState.mu.Unlock()
	writeJSON(w, 200, map[string]string{"status": "ok", "installed": strconv.FormatBool(appState.SchedulerOK)})
}

func apiStartupToggle(w http.ResponseWriter, r *http.Request) {
	if runtime.GOOS != "windows" {
		writeJSON(w, 400, map[string]string{"error": "仅 Windows 支持"})
		return
	}
	appState.mu.Lock()
	installed := appState.StartupOK
	appState.mu.Unlock()
	if installed {
		removeStartup(false)
	} else {
		if !isAdmin() {
			if relaunchElevatedForWeb("startup") {
				writeJSON(w, 202, map[string]string{"status": "elevating"})
				return
			}
			writeJSON(w, 403, map[string]string{"error": "需要管理员权限"})
			return
		}
		installStartup(false)
	}
	appState.mu.Lock()
	appState.StartupOK = checkStartupInstalled(false)
	appState.mu.Unlock()
	writeJSON(w, 200, map[string]string{"status": "ok", "installed": strconv.FormatBool(appState.StartupOK)})
}

func apiW32Toggle(w http.ResponseWriter, r *http.Request) {
	if runtime.GOOS != "windows" {
		writeJSON(w, 400, map[string]string{"error": "仅 Windows 支持"})
		return
	}
	if !isAdmin() {
		if relaunchElevatedForWeb("w32time") {
			writeJSON(w, 202, map[string]string{"status": "elevating"})
			return
		}
		writeJSON(w, 403, map[string]string{"error": "需要管理员权限"})
		return
	}
	appState.mu.Lock()
	st := appState.W32TimeState
	appState.mu.Unlock()
	disable := (st != "disabled")
	if !setSystemTimeService(disable) {
		writeJSON(w, 500, map[string]string{"error": "操作失败"})
		return
	}
	appState.mu.Lock()
	appState.W32TimeState = queryW32TimeState()
	appState.mu.Unlock()
	writeJSON(w, 200, map[string]string{"status": "ok", "w32time": appState.W32TimeState})
}

func checkSchedulerInstalled() bool {
	out, err := exec.Command("schtasks", "/Query", "/TN", "TimeSyncAuto").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "TimeSyncAuto")
}

func checkStartupInstalled(machine bool) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	// machine=true -> 查 HKLM（所有用户）；否则查 HKCU（当前用户）
	hive := `HKCU`
	if machine {
		hive = `HKLM`
	}
	out, err := exec.Command("reg", "query",
		hive+`\Software\Microsoft\Windows\CurrentVersion\Run`,
		"/v", "TimeSyncAuto").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "TimeSyncAuto")
}

func queryW32TimeState() string {
	if runtime.GOOS != "windows" {
		return "unsupported"
	}
	out, err := exec.Command("sc", "query", "w32time").CombinedOutput()
	if err != nil {
		return "unknown"
	}
	s := string(out)
	if strings.Contains(s, "RUNNING") {
		return "running"
	}
	if strings.Contains(s, "STOPPED") {
		// check start type
		out2, _ := exec.Command("reg", "query",
			`HKLM\SYSTEM\CurrentControlSet\Services\W32Time`,
			"/v", "Start").CombinedOutput()
		if strings.Contains(string(out2), "0x4") {
			return "disabled"
		}
		return "stopped"
	}
	return "unknown"
}

// openBrowser launches the default browser to the given URL (best effort).
//
// Windows 上按三种方式依次尝试：部分 Win11 / 安全软件策略会拦掉 rundll32 这一路，
// 早先只试 rundll32 就一直"没反应"。失败也不致命——调用方总会把 URL 打印出来。
func openBrowser(url string) {
	switch runtime.GOOS {
	case "windows":
		for _, argv := range [][]string{
			{"rundll32", "url.dll,FileProtocolHandler", url},
			{"explorer.exe", url},
			{"cmd", "/c", "start", "", url},
		} {
			if tryRun(argv) {
				return
			}
		}
		fmt.Println("  （未能自动打开浏览器，请手动复制上面的地址到浏览器）")
	case "darwin":
		if !tryRun([]string{"open", url}) {
			fmt.Println("  （未能自动打开浏览器，请手动访问上面的地址）")
		}
	default:
		if !tryRun([]string{"xdg-open", url}) {
			fmt.Println("  （未能自动打开浏览器，请手动访问上面的地址）")
		}
	}
}

// tryRun 启动命令并在极短时间内判断它是否"起来且没立刻报错"。
// 不等待命令真正结束（浏览器会一直开着），只区分"启动失败/秒退"和"正常拉起"。
func tryRun(argv []string) bool {
	cmd := exec.Command(argv[0], argv[1:]...)
	// `cmd /c start ...` 这条兜底路线会闪一下控制台窗口，必须隐藏
	if argv[0] == "cmd" {
		setHidden(cmd)
	}
	if err := cmd.Start(); err != nil {
		return false
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err == nil // 立刻结束且无错 → 认成功；有错 → 换下一种方式
	case <-time.After(800 * time.Millisecond):
		return true // 还在跑，说明浏览器已被拉起
	}
}

// goSelfPath returns the executable path (or a friendly fallback for dev).
func goSelfPath() string {
	p, err := os.Executable()
	if err == nil {
		return p
	}
	return filepath.Join(".", "timesync")
}

// relaunchElevatedForWeb tries to elevate the current process and re-launch it with
// the given tag (so the elevated window knows what to do). Returns true if the
// relaunch was dispatched. Currently a placeholder: the real implementation
// lives in platform_windows.go via relaunchElevatedTag.
func relaunchElevatedForWeb(tag string) bool {
	return relaunchElevatedTag(tag)
}
