package main

import (
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Args holds the parsed CLI flags. We use the stdlib `flag` package directly
// rather than a third-party library to keep the binary self-contained.
type Args struct {
	Query        bool
	Sync         bool
	Force        bool
	AllowBack    bool
	Quiet        bool
	Menu         bool
	Web          bool
	NoOpen       bool
	Loop         bool
	Interval     int
	Retries      int
	Timeout      float64
	Threshold    float64
	Log          string
	Config       string
	Listen       string
	PrintVer     bool
	Help         bool
	InstallSched bool
	RemoveSched  bool
	IntervalSet  bool
	InstallStart bool
	RemoveStart  bool
	DisableW32   bool
	EnableW32    bool
	DriftReport  bool
	WebTag       string
	Elevated     bool
	Hidden       bool // (internal) 后台面板进程：隐藏自己的控制台窗口
	SetToken     bool // 生成面板访问令牌并写入配置后退出
	// --- 补齐 Python 版 / zdtime 参考实现中缺失的参数 ---
	Diag        bool       // 自检模式
	Machine     bool       // 配合 startup 写 HKLM（所有用户）
	SetServer   string     // 把 NTP 列表写入配置并退出
	SetHTTP     string     // 把 HTTP 列表写入配置并退出
	NoConfig    bool       // 忽略配置文件
	NoLog       bool       // 本次运行不写日志
	JSON        bool       // JSON 输出
	ReportLimit int        // 漂移报告展示条数
	Servers     stringList // --server（可多次）
	HTTPs       stringList // --http（可多次）
}

// stringList implements flag.Value so a flag can be repeated: --server a --server b
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		if t := strings.TrimSpace(part); t != "" {
			*s = append(*s, t)
		}
	}
	return nil
}

func parseArgs() Args {
	fs := flag.NewFlagSet("timesync", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	a := Args{}
	fs.BoolVar(&a.Query, "query", false, "查询时间偏差（不修改系统时间）")
	fs.BoolVar(&a.Sync, "sync", false, "立即校正系统时间")
	fs.BoolVar(&a.Force, "force", false, "force sync (ignore threshold)")
	fs.BoolVar(&a.AllowBack, "allow-back", false, "允许把系统时钟往回拨")
	fs.BoolVar(&a.Quiet, "quiet", false, "静默输出（适合计划任务/脚本）")
	fs.BoolVar(&a.Menu, "menu", false, "强制进入交互式菜单")
	fs.BoolVar(&a.Web, "web", false, "启动 HTML 控制面板并打开浏览器")
	fs.BoolVar(&a.NoOpen, "no-open", false, "起 web 但不自动打开浏览器")
	fs.BoolVar(&a.Loop, "loop", false, "循环模式：按 --interval 持续校时")
	fs.IntVar(&a.Interval, "interval", 0, "循环间隔（分钟，>0 启用）")
	fs.IntVar(&a.Retries, "retries", -1, "每源重试次数")
	fs.Float64Var(&a.Timeout, "timeout", 0, "单源超时（秒）")
	fs.Float64Var(&a.Threshold, "threshold", 0, "偏差阈值（秒），低于则跳过")
	fs.StringVar(&a.Log, "log", "", "日志文件路径")
	fs.StringVar(&a.Config, "config", "", "配置文件路径")
	fs.StringVar(&a.Listen, "listen", "127.0.0.1:8765", "Web 面板监听地址")
	fs.BoolVar(&a.PrintVer, "version", false, "打印版本并退出")
	fs.BoolVar(&a.Help, "help", false, "打印帮助")
	fs.BoolVar(&a.InstallSched, "install-scheduler", false, "注册计划任务")
	fs.BoolVar(&a.RemoveSched, "remove-scheduler", false, "删除计划任务")
	fs.IntVar(&a.Interval, "scheduler-interval", 0, "计划任务间隔（分钟）")
	fs.BoolVar(&a.InstallStart, "install-startup", false, "注册开机自启")
	fs.BoolVar(&a.RemoveStart, "remove-startup", false, "取消开机自启")
	fs.BoolVar(&a.DisableW32, "disable-w32time", false, "停用系统时间服务")
	fs.BoolVar(&a.EnableW32, "enable-w32time", false, "启用系统时间服务")
	fs.BoolVar(&a.DriftReport, "drift-report", false, "查看漂移报告")
	fs.StringVar(&a.WebTag, "_webtag", "", "(internal) elevated web action tag")
	fs.BoolVar(&a.Elevated, "_elevated", false, "(internal) elevated marker")
	fs.BoolVar(&a.Hidden, "_hidden", false, "(internal) 后台面板进程，隐藏控制台窗口")
	fs.BoolVar(&a.SetToken, "set-token", false, "生成面板访问令牌并写入配置（远程/局域网访问时防未授权调用），然后退出")
	// --- 补齐的缺失参数（对齐 Python 版 v1.3.2 + zdtime 参考实现）---
	fs.BoolVar(&a.Diag, "diag", false, "自检模式：打印环境信息与各时间源连通性，不修改系统时间")
	fs.BoolVar(&a.Machine, "machine", false, "配合 -install-startup/-remove-startup 写入 HKLM（所有用户，需管理员）")
	fs.StringVar(&a.SetServer, "set-server", "", "把 NTP 服务器列表(逗号分隔)写入 配置.ini 并退出")
	fs.StringVar(&a.SetHTTP, "set-http", "", "把 HTTP 兜底源列表(逗号分隔)写入 配置.ini 并退出")
	fs.BoolVar(&a.NoConfig, "no-config", false, "忽略配置文件，仅用命令行参数与内置默认值")
	fs.BoolVar(&a.NoLog, "no-log", false, "本次运行不写日志")
	fs.BoolVar(&a.JSON, "json", false, "以 JSON 格式输出（便于脚本解析）")
	fs.IntVar(&a.ReportLimit, "report-limit", 0, "漂移报告展示条数（默认 20）")
	fs.Var(&a.Servers, "server", "临时指定 NTP 服务器（可多次，覆盖配置）")
	fs.Var(&a.HTTPs, "http", "临时指定 HTTP 兜底源（可多次，覆盖配置）")
	if err := fs.Parse(os.Args[1:]); err != nil {
		// flag already printed the error
		os.Exit(2)
	}
	if fs.Lookup("interval").Value.String() != fs.Lookup("interval").DefValue || fs.Lookup("scheduler-interval").Value.String() != fs.Lookup("scheduler-interval").DefValue {
		a.IntervalSet = true
	}
	return a
}

func printHelp() {
	fmt.Println(`time_sync · 自动校时工具（NTP + HTTP 兜底）`)
	fmt.Printf("版本：%s\n\n", Version)
	fmt.Println(`用法：
  timesync                    # 无参数进入交互菜单（如有 TTY）
  timesync --query            # 仅查询偏差
  timesync --sync [--force]   # 立即校正（管理员）
  timesync --loop --interval 60   # 持续校时（分钟）
  timesync --web              # 启动 HTML 控制面板并打开浏览器
  timesync --drift-report     # 查看漂移报告
  timesync --diag             # 自检：环境信息 + w32time + 各时间源连通性
  timesync --menu             # 进入交互菜单（0-13）

时间源：
  -server <host>     临时指定 NTP 服务器（可多次，覆盖配置）
  -http <url>        临时指定 HTTP 兜底源（可多次）
  -set-server <list> 把 NTP 列表(逗号分隔)写入 配置.ini 并退出
  -set-http <list>   把 HTTP 列表(逗号分隔)写入 配置.ini 并退出

通用：
  -config <path>     指定配置文件
  -no-config         忽略配置文件，仅用命令行参数与内置默认值
  -log <path>        日志文件
  -no-log            本次运行不写日志
  -timeout <sec>     单源超时（默认 5.0）
  -threshold <sec>   偏差阈值（默认 0.5）
  -retries <n>       每源重试次数
  -allow-back        允许回拨时钟
  -quiet             静默
  -json              以 JSON 格式输出（便于脚本解析）
  -report-limit <n>  漂移报告展示条数（默认 20）

集成（Windows）：
  -install-scheduler [-scheduler-interval 60]
  -remove-scheduler
  -install-startup / -remove-startup [-machine]
                     -machine 写入 HKLM（所有用户，需管理员），默认 HKCU
  -disable-w32time / -enable-w32time

Web 面板：
  -web [-listen 127.0.0.1:8765] [-no-open]`)
}

func applyArgsToConfig(a Args, c *Config) {
	if a.Timeout > 0 {
		c.Timeout = a.Timeout
	}
	if a.Threshold > 0 {
		c.Threshold = a.Threshold
	}
	if a.Retries >= 0 {
		c.Retries = a.Retries
	}
	if a.Force {
		c.Force = true
	}
	if a.Log != "" {
		c.Log = a.Log
	}
}

// resolveLogPath 把配置里可能是相对路径的日志路径，按 exe 所在目录解析成绝对路径。
//
// 关键修正（BUG-2）：原先 LogEvent 直接拿配置里的路径去 OpenFile，相对路径会按
// 「进程当前工作目录」解析。而计划任务运行时 CWD 经常是 C:\Windows\System32，
// 于是日志要么写不进、要么散落到系统目录，面板趋势图/漂移报告也读不到历史。
// 这里统一在加载配置后把它"钉死"在 exe 同目录，与 配置.ini 的位置保持一致。
func resolveLogPath(cfg *Config) {
	if cfg.Log == "" {
		return
	}
	if filepath.IsAbs(cfg.Log) {
		return
	}
	base := "."
	if exe, err := os.Executable(); err == nil {
		base = filepath.Dir(exe)
	}
	cfg.Log = filepath.Join(base, cfg.Log)
}

func main() {
	// Try to set stdout/stderr to UTF-8 on Windows so piping doesn't crash
	// (Python/Go echo "✓/✗" cleanly; this also avoids GBK crash on redirect).
	configureConsoleIO()

	a := parseArgs()

	// 后台面板进程：第一时间把自己藏起来。
	// Go 编出来的是 console 子系统程序，即便父进程用了 CREATE_NO_WINDOW，
	// Windows 仍会为它分配 conhost，只是不保证完全不闪。这里再主动隐藏一次，
	// 双保险——用户按 w 时不应该看到任何多余的黑窗口。
	if a.Hidden {
		hideConsoleWindow()
	}

	if a.PrintVer {
		fmt.Printf("time_sync %s\n", Version)
		return
	}
	if a.Help {
		printHelp()
		return
	}

	// --set-token：生成随机访问令牌写入配置后退出（不进入菜单/校时流程）
	if a.SetToken {
		handleSetToken(a)
		return
	}

	// Load config (--no-config: skip the ini file entirely)
	var cfg Config
	if a.NoConfig {
		cfg = DefaultConfig()
	} else {
		cfg, _ = LoadConfig(a.Config)
	}
	if cfg.Path == "" {
		exe, _ := os.Executable()
		cfg.Path = filepath.Join(filepath.Dir(exe), ConfigFilename)
	}
	if a.Log != "" {
		cfg.Log = a.Log
	}
	// --server / --http 临时覆盖配置里的源列表
	if len(a.Servers) > 0 {
		cfg.NTP = []string(a.Servers)
	}
	if len(a.HTTPs) > 0 {
		cfg.HTTP = []string(a.HTTPs)
	}
	// --no-log: 本次运行不落日志
	if a.NoLog {
		cfg.Log = ""
	}
	applyArgsToConfig(a, &cfg)
	// 把日志路径"钉死"在 exe 同目录：覆盖 --no-config（走 DefaultConfig 相对名）
	// 与 --log <相对路径> 两种情况；绝对路径 / 空（--no-log 或显式留空）保持不变。
	resolveLogPath(&cfg)

	// --set-server / --set-http：把源列表写入配置后即退出（持久化，不影响本次运行）
	if a.SetServer != "" || a.SetHTTP != "" {
		var ntpList, httpList []string
		if a.SetServer != "" {
			ntpList = splitList(a.SetServer)
		}
		if a.SetHTTP != "" {
			httpList = splitList(a.SetHTTP)
		}
		p, err := SaveServers(cfg.Path, ntpList, httpList)
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ 保存失败：%v\n", err)
			os.Exit(1)
		}
		fmt.Println("✓ 已写入 " + p)
		return
	}

	// --diag 自检：环境信息 + w32time 状态 + 各时间源连通性（不修改系统时间）
	if a.Diag {
		rep := RunDiag(cfg.NTP, cfg.HTTP, cfg.Timeout, cfg.Retries, !a.JSON)
		if a.JSON {
			_ = PrintDiagJSON(rep)
		} else {
			PrintDiag(rep)
		}
		return
	}

	// Decide if any actionable flag was passed (=> do work, don't fall back to menu)
	actionable := a.Query || a.Sync || a.Loop || a.InstallSched || a.RemoveSched ||
		a.InstallStart || a.RemoveStart || a.DisableW32 || a.EnableW32 ||
		a.DriftReport || a.Web || a.Diag

	// Web is special: works headless too
	if a.Web {
		runWebMode(a, cfg)
		return
	}

	// Elevated auto-continue: when launched from menu via runas, the fresh
	// elevated window lands here with --_elevated --_preselect <n>.
	if a.Elevated && os.Getenv("TIMESYNC_PRESELECT") != "" {
		choice := os.Getenv("TIMESYNC_PRESELECT")
		fmt.Printf("(已用管理员身份打开，自动继续上次选择：%s)\n", choice)
		// Run the chosen action once and exit.
		executeMenuChoice(choice, cfg)
		// 停一下让用户看清结果，否则子进程窗口一闪就没了。
		fmt.Print("\n按回车键关闭此窗口…")
		fmt.Scanln()
		return
	}

	// One-shot commands
	switch {
	case a.InstallSched:
		if !isAdmin() {
			if maybeElevateAndRestart(nil) {
				return
			}
		}
		interval := a.Interval
		if interval <= 0 {
			interval = 60
		}
		installScheduler(interval)
		return
	case a.RemoveSched:
		if !isAdmin() {
			if maybeElevateAndRestart(nil) {
				return
			}
		}
		removeScheduler()
		return
	case a.InstallStart:
		if !isAdmin() {
			if maybeElevateAndRestart(nil) {
				return
			}
		}
		installStartup(a.Machine)
		return
	case a.RemoveStart:
		if !isAdmin() {
			if maybeElevateAndRestart(nil) {
				return
			}
		}
		removeStartup(a.Machine)
		return
	case a.DisableW32:
		if !isAdmin() {
			if maybeElevateAndRestart(nil) {
				return
			}
		}
		_ = setSystemTimeService(true)
		return
	case a.EnableW32:
		if !isAdmin() {
			if maybeElevateAndRestart(nil) {
				return
			}
		}
		_ = setSystemTimeService(false)
		return
	case a.DriftReport:
		limit := a.ReportLimit
		if limit <= 0 {
			limit = 20
		}
		fmt.Print(ReportDrift(cfg.Log, limit))
		return
	case a.Loop:
		runLoop(cfg, a.Interval)
		return
	case a.Sync, a.Query:
		opt := RunOptions{
			NTP:        cfg.NTP,
			HTTP:       cfg.HTTP,
			Timeout:    cfg.Timeout,
			Retries:    cfg.Retries,
			Threshold:  cfg.Threshold,
			Force:      a.Force,
			DoSync:     a.Sync,
			Quiet:      a.Quiet,
			LogPath:    cfg.Log,
			AllowBack:  a.AllowBack,
			Slew:       cfg.Slew,
			SlewMaxSec: cfg.SlewMaxSec,
		}
		r := RunOnce(opt)
		// Persist last result so web panel can read it
		appState.mu.Lock()
		res := r
		appState.Last = &res
		appState.LastAt = time.Now()
		appState.Cfg = cfg
		appState.CfgPath = cfg.Path
		appState.LogPath = cfg.Log
		appState.mu.Unlock()
		os.Exit(r.Status.ExitCode())
	}

	// No actionable flag: enter menu if interactive, else print help.
	// 双击/无参数 = 进控制台菜单（面板按 w 手动打开，符合"控制台还是手动"的预期）。
	if a.Menu || (!actionable && isTerminal(os.Stdin)) {
		runMenu(cfg)
		return
	}
	printHelp()
}

// configureConsoleIO is a no-op on Unix; on Windows it tries to switch the
// console code page to UTF-8 (best effort). Even if it fails, individual
// `fmt.Fprint` calls will not crash thanks to a small wrapper used by
// `safePrintln`. This function only ensures the OS-level console doesn't
// drop bytes silently.
func configureConsoleIO() {
	if runtime.GOOS != "windows" {
		return
	}
	// We don't import golang.org/x/sys/windows here to keep main lean; the
	// platform-specific files can wrap os.Stdout if needed. For now this is
	// just a hook for future expansion. Pipe output remains Unicode-safe
	// because we replace the few non-ASCII runes with ASCII in wrappers.
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func runLoop(cfg Config, intervalMin int) {
	if intervalMin <= 0 {
		intervalMin = 60
	}
	d := time.Duration(intervalMin) * time.Minute
	fmt.Printf("▶ 循环模式：每 %d 分钟校时一次。Ctrl+C 退出。\n", intervalMin)
	tick := time.NewTicker(d)
	defer tick.Stop()
	// First run immediately
	runOnceLoop(cfg)
	for {
		select {
		case <-tick.C:
			runOnceLoop(cfg)
		case <-sigCh():
			fmt.Println("\n已退出循环。")
			return
		}
	}
}

func sigCh() <-chan os.Signal {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	return ch
}

func runOnceLoop(cfg Config) {
	opt := RunOptions{
		NTP:        cfg.NTP,
		HTTP:       cfg.HTTP,
		Timeout:    cfg.Timeout,
		Retries:    cfg.Retries,
		Threshold:  cfg.Threshold,
		Force:      cfg.Force,
		DoSync:     true,
		Quiet:      false,
		LogPath:    cfg.Log,
		AllowBack:  true,
		Slew:       cfg.Slew,
		SlewMaxSec: cfg.SlewMaxSec,
	}
	r := RunOnce(opt)
	appState.mu.Lock()
	res := r
	appState.Last = &res
	appState.LastAt = time.Now()
	appState.Cfg = cfg
	appState.CfgPath = cfg.Path
	appState.LogPath = cfg.Log
	appState.mu.Unlock()
}

// handleSetToken 生成一个随机访问令牌写入配置后打印，供远程/局域网访问面板时使用。
// 令牌为 24 字节随机值（base64url），不依赖任何外部输入；置空配置里的 api_token 行即可关闭校验。
func handleSetToken(a Args) {
	cfg, _ := LoadConfig(a.Config)
	if cfg.Path == "" {
		if exe, err := os.Executable(); err == nil {
			cfg.Path = filepath.Join(filepath.Dir(exe), ConfigFilename)
		} else {
			cfg.Path = ConfigFilename
		}
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		fmt.Fprintln(os.Stderr, "✗ 生成令牌失败:", err)
		os.Exit(1)
	}
	cfg.APIToken = base64.RawURLEncoding.EncodeToString(buf)
	if err := saveSyncSection(cfg.Path, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "✗ 保存失败:", err)
		os.Exit(1)
	}
	fmt.Printf("✓ 已生成面板访问令牌并写入 %s：\n\n  %s\n\n", cfg.Path, cfg.APIToken)
	fmt.Println("用法：远程/局域网打开面板时，在地址后追加 ?token=上面的令牌，或请求头带 X-API-Token。")
	fmt.Println("本机 127.0.0.1 访问始终免令牌；清空令牌只需把配置里的 api_token 行删掉。")
}

// isLoopbackListen 判断监听地址是否只绑本机回环（127.0.0.1 / localhost / ::1）。
func isLoopbackListen(listen string) bool {
	h := listen
	if i := strings.LastIndex(listen, ":"); i >= 0 {
		h = listen[:i]
	}
	switch h {
	case "", "127.0.0.1", "localhost", "[::1]", "::1":
		return true
	}
	return false
}

func runWebMode(a Args, cfg Config) {
	appState.mu.Lock()
	appState.Cfg = cfg
	appState.CfgPath = cfg.Path
	appState.LogPath = cfg.Log
	appState.SchedulerOK = checkSchedulerInstalled()
	appState.StartupOK = checkStartupInstalled(false)
	appState.W32TimeState = queryW32TimeState()
	appState.mu.Unlock()

	// 远程/局域网暴露面板但没设令牌：局域网内任意主机都可调用校时/改配置，给显眼告警。
	if !isLoopbackListen(a.Listen) && cfg.APIToken == "" {
		fmt.Println("⚠ 警告：面板监听在非本机地址（" + a.Listen + "）且未设置 api_token，")
		fmt.Println("   局域网 / 公网任意主机都能调用校时与改配置。建议：--set-token 生成令牌，或保持默认 127.0.0.1 仅本机访问。")
	}

	url, err := ServeWebPanel(a.Listen)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ 启动 Web 面板失败：%v\n", err)
		fmt.Fprintln(os.Stderr, "  提示：用 --listen 127.0.0.1:其他端口 换一个端口试试。")
		os.Exit(1)
	}
	appState.mu.Lock()
	appState.WebRunning = true
	appState.WebURL = url
	appState.Standalone = true // 独立进程：允许页面上"关闭面板"
	appState.mu.Unlock()
	fmt.Printf("time_sync Web 控制面板：%s\n", url)
	if !a.NoOpen {
		openBrowser(url)
	}
	// Block until Ctrl+C
	<-sigCh()
	fmt.Println("\n已退出。")
}

// runWebTagAction 执行面板在提权后请求的系统集成动作。
//
// 标签格式 "kind:sub"：
//   - scheduler:install / scheduler:remove  → 注册/删除计划任务
//   - startup:install   / startup:remove    → 注册/删除开机自启（HKCU）
//   - w32time:disable / w32time:enable      → 停用/启用 w32time 服务
//
// 为什么用 "kind:sub" 而不是只传 kind：被提权的子进程是全新进程，它的 appState
// 是空的，无法像面板那样根据当前状态反推"该装还是该卸"，所以动作意图必须由面板
// 在点击那一刻就明确传下来。
// parseWebTag 把 "kind:sub" 标签拆成动作类别与子动作，纯函数便于单测。
func parseWebTag(tag string) (kind, sub string) {
	parts := strings.SplitN(tag, ":", 2)
	kind = parts[0]
	if len(parts) > 1 {
		sub = parts[1]
	}
	return
}

func runWebTagAction(tag string, cfg Config) {
	kind, sub := parseWebTag(tag)
	switch kind {
	case "scheduler":
		if sub == "remove" {
			removeScheduler()
		} else {
			interval := cfg.Interval
			if interval <= 0 {
				interval = 60
			}
			installScheduler(interval)
		}
	case "startup":
		if sub == "remove" {
			removeStartup(false)
		} else {
			installStartup(false)
		}
	case "w32time":
		setSystemTimeService(sub != "enable")
	case "sync":
		// 面板「立即校准」在非管理员时的提权子进程：查一次并写钟，然后退出。
		RunOnce(RunOptions{
			NTP: cfg.NTP, HTTP: cfg.HTTP, Timeout: cfg.Timeout,
			Retries: cfg.Retries, Threshold: cfg.Threshold,
			Force: true, DoSync: true, AllowBack: true,
			LogPath: cfg.Log,
			Slew:    cfg.Slew, SlewMaxSec: cfg.SlewMaxSec,
		})
		return
	default:
		fmt.Fprintf(os.Stderr, "✗ 未知的面板操作标签：%s\n", tag)
		os.Exit(2)
	}
	fmt.Printf("✓ 已完成面板请求的操作：%s（如需刷新状态，请回到面板查看）\n", tag)
}

// startWebPanelInMenu 让菜单里的 w 成为"永远能用"的入口。
//
// 关键设计：面板必须跑在**独立后台进程**里。早先的实现在当前进程内起 goroutine，
// 结果用户按 w、浏览器打开后顺手关掉控制台黑窗口（或按 0/q），进程一退面板就没了，
// 浏览器刷新立刻变成"127.0.0.1 拒绝连接"——这正是用户报的"w 进不去"。
//
// 现在的流程：
//  1. 端口上已有面板 → 直接把浏览器指过去，不重复起进程；
//  2. 否则 detached 起一个 `--web --no-open` 子进程，等它就绪后再开浏览器；
//  3. 子进程起不来（极少数受限环境）→ 退回当前进程内启动，并如实告知注意点。
func startWebPanelInMenu(cfg Config) {
	// 1) 已经有面板在跑
	if panelIsOurs(DefaultListen) {
		url := "http://" + DefaultListen
		appState.mu.Lock()
		appState.WebURL = url
		appState.WebRunning = true
		appState.mu.Unlock()
		fmt.Printf("✓ 控制面板已在运行：%s\n", url)
		openBrowser(url)
		return
	}

	// 2) 独立后台进程常驻
	fmt.Println("▶ 正在启动 Web 控制面板（独立后台进程）…")
	args := []string{"--web", "--no-open", "--_hidden", "--listen", DefaultListen}
	if cfg.Path != "" {
		// 子进程要读同一份配置，否则用了 --config 的场景会退化成默认配置
		args = append(args, "--config", cfg.Path)
	}
	if err := spawnDetached(goSelfPath(), args); err == nil {
		for i := 0; i < 25; i++ { // 最多等 5 秒
			if panelIsOurs(DefaultListen) {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if panelIsOurs(DefaultListen) {
			url := "http://" + DefaultListen
			appState.mu.Lock()
			appState.WebURL = url
			appState.WebRunning = true
			appState.mu.Unlock()
			fmt.Printf("✓ Web 控制面板已就绪：%s\n", url)
			fmt.Println("  （面板在独立进程中运行，关掉本窗口也不会停；再按 w 只是重新打开标签页）")
			openBrowser(url)
			return
		}
		fmt.Println("  后台进程未在预期时间内就绪，改用当前进程启动……")
	}

	// 3) 兜底：在当前进程内启动
	url, err := ServeWebPanel(DefaultListen)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ 启动 Web 面板失败：%v\n", err)
		fmt.Fprintln(os.Stderr, "  可手动指定端口：timesync.exe --web --listen 127.0.0.1:8899")
		return
	}
	appState.mu.Lock()
	appState.WebRunning = true
	appState.WebURL = url
	appState.mu.Unlock()
	fmt.Printf("✓ Web 控制面板已启动：%s\n", url)
	fmt.Println("  ⚠ 注意：本次面板运行在当前进程中，关闭本窗口会一并停止面板。")
	openBrowser(url)
}

func runMenu(cfg Config) {
	for {
		printMenu()
		fmt.Print("\n请选择功能 [0-13]（q 退出，w 打开 Web 面板）: ")
		var line string
		_, err := fmt.Scanln(&line)
		if err != nil {
			fmt.Println()
			return
		}
		line = strings.TrimSpace(line)
		if line == "" {
			fmt.Println("（未选择功能，菜单已刷新。）")
			continue
		}
		if line == "q" || line == "Q" {
			fmt.Println("已退出。")
			return
		}
		if line == "w" || line == "W" {
			startWebPanelInMenu(cfg)
			continue
		}
		executeMenuChoice(line, cfg)
		fmt.Println()
		fmt.Println("—— 操作结束，可继续选择功能（0/q 退出，w 打开 Web 面板）——")
	}
}

func printMenu() {
	fmt.Println(strings.Repeat("=", 46))
	fmt.Println(" time_sync 自动校时工具（Go 版）")
	fmt.Println(strings.Repeat("=", 46))
	// 面板已起时把真实地址顶到最上面，用户不用去翻历史输出
	appState.mu.Lock()
	panelURL := appState.WebURL
	appState.mu.Unlock()
	if panelURL != "" {
		fmt.Printf(" ★ 控制面板已打开：%s\n", panelURL)
		fmt.Println(strings.Repeat("-", 46))
	}
	fmt.Println("  1. 查询时间偏差（不修改系统时间）")
	fmt.Println("  2. 立即校正系统时间（需管理员）")
	fmt.Println("  3. 定时循环校准")
	fmt.Println("  4. 注册计划任务（每小时自动校准）")
	fmt.Println("  5. 删除计划任务")
	fmt.Println("  6. 设置开机自启（注册表）")
	fmt.Println("  7. 取消开机自启")
	fmt.Println("  8. 更改 NTP 时间服务器（写入配置.ini）")
	fmt.Println("  9. 更改 HTTP 兜底源（写入配置.ini）")
	fmt.Println(" 10. 查看校准/漂移报告（晶振 ppm）")
	fmt.Println(" 11. 停用系统时间服务 w32time（需管理员）")
	fmt.Println(" 12. 启用系统时间服务 w32time（需管理员）")
	fmt.Println(" 13. 自检（环境与各时间源连通性，不校时）")
	fmt.Println("  0. 退出")
	fmt.Println("  w. 打开 Web 控制面板（浏览器）")
}

// executeMenuChoice maps a menu choice to a one-shot action. Elevation is
// delegated to maybeElevateAndRestart when the chosen action requires admin;
// that helper re-launches this exe with --_elevated and TIMESYNC_PRESELECT set.
func executeMenuChoice(choice string, cfg Config) {
	needsAdmin := map[string]bool{
		"2": true, "4": true, "5": true, "6": true, "7": true,
		"11": true, "12": true,
	}
	if needsAdmin[choice] && !isAdmin() {
		// Hand off to a fresh elevated process with the same choice.
		if tryElevateForMenu(choice) {
			return
		}
		fmt.Println("✗ 提权失败（可能被取消或系统禁用了 UAC）。")
		fmt.Println("  请右键『以管理员身份运行』本程序。")
		return
	}
	switch choice {
	case "1":
		opt := RunOptions{
			NTP: cfg.NTP, HTTP: cfg.HTTP, Timeout: cfg.Timeout,
			Retries: cfg.Retries, Threshold: cfg.Threshold,
			LogPath: cfg.Log,
		}
		r := RunOnce(opt)
		appState.mu.Lock()
		appState.Last = &r
		appState.LastAt = time.Now()
		appState.Cfg = cfg
		appState.mu.Unlock()
	case "2":
		opt := RunOptions{
			NTP: cfg.NTP, HTTP: cfg.HTTP, Timeout: cfg.Timeout,
			Retries: cfg.Retries, Threshold: cfg.Threshold,
			Force: true, DoSync: true, AllowBack: true,
			LogPath: cfg.Log,
			Slew:    cfg.Slew, SlewMaxSec: cfg.SlewMaxSec,
		}
		r := RunOnce(opt)
		appState.mu.Lock()
		appState.Last = &r
		appState.LastAt = time.Now()
		appState.Cfg = cfg
		appState.mu.Unlock()
	case "3":
		fmt.Print("循环间隔（分钟）: ")
		var n int
		fmt.Scanln(&n)
		if n <= 0 {
			n = 60
		}
		cfg.Interval = n
		runLoop(cfg, n)
	case "4":
		installScheduler(cfg.Interval)
	case "5":
		removeScheduler()
	case "6":
		installStartup(false)
	case "7":
		removeStartup(false)
	case "8":
		if newList, ok := promptList("新的 NTP 服务器（逗号分隔）", cfg.NTP); ok {
			p, err := SaveServers(cfg.Path, newList, nil)
			if err != nil {
				fmt.Println("✗ 保存失败：" + err.Error())
			} else {
				fmt.Println("✓ 已写入 " + p)
				cfg.NTP = newList
			}
		}
	case "9":
		if newList, ok := promptList("新的 HTTP 兜底源（逗号分隔）", cfg.HTTP); ok {
			p, err := SaveServers(cfg.Path, nil, newList)
			if err != nil {
				fmt.Println("✗ 保存失败：" + err.Error())
			} else {
				fmt.Println("✓ 已写入 " + p)
				cfg.HTTP = newList
			}
		}
	case "10":
		fmt.Print(ReportDrift(cfg.Log, 20))
	case "11":
		_ = setSystemTimeService(true)
	case "12":
		_ = setSystemTimeService(false)
	case "13":
		// 自检：环境 + w32time + 各时间源连通性（不改时钟）
		PrintDiag(RunDiag(cfg.NTP, cfg.HTTP, cfg.Timeout, cfg.Retries, true))
	default:
		fmt.Println("无效选择，请重新输入。")
	}
}

// promptList shows the current value as default and reads a comma-separated
// replacement. Empty input keeps the current value (returns ok=false).
func promptList(prompt string, current []string) ([]string, bool) {
	fmt.Printf("%s [当前: %s]: ", prompt, strings.Join(current, ", "))
	var line string
	if _, err := fmt.Scanln(&line); err != nil {
		return nil, false
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, false
	}
	parts := splitList(line)
	if len(parts) == 0 {
		return nil, false
	}
	return parts, true
}

// tryElevateForMenu sets TIMESYNC_PRESELECT and calls maybeElevateAndRestart
// with no extra args; the elevated instance reads the env and continues.
func tryElevateForMenu(choice string) bool {
	os.Setenv("TIMESYNC_PRESELECT", choice)
	return maybeElevateAndRestart(nil)
}
