//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// isAdmin reports whether the current process token is actually elevated.
//
// 之前实现用 Token(0).IsMember(管理员组 SID)：对「管理员用户但未提权」的进程
// （UAC 过滤令牌）会误判为 true，导致跳过提权、直接写时钟时报
// "无法启用 SE_SYSTEMTIME_NAME 权限"。改用 TokenElevation：只有令牌
// 真正处于提权状态（TokenIsElevated != 0）才返回 true。
func isAdmin() bool {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return false
	}
	defer token.Close()
	var elev uint32
	var retLen uint32
	err = windows.GetTokenInformation(token, windows.TokenElevation,
		(*byte)(unsafe.Pointer(&elev)), uint32(unsafe.Sizeof(elev)), &retLen)
	if err != nil {
		return false
	}
	return elev != 0
}

func relaunchElevated(extraArgs []string) bool {
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(syscallExe())
	cwd, _ := os.Getwd()

	argv := append([]string{}, os.Args[1:]...)
	argv = append(argv, extraArgs...)
	argv = append(argv, "--_elevated")
	params := windowsMakeCommandLine(argv)

	paramsPtr, _ := syscall.UTF16PtrFromString(params)
	cwdPtr, _ := syscall.UTF16PtrFromString(cwd)
	show := int32(1) // SW_SHOWNORMAL
	shell32a := windows.NewLazySystemDLL("shell32.dll")
	shellExecW := shell32a.NewProc("ShellExecuteW")
	fmt.Println("需要管理员权限，正在以管理员身份重新打开（请在 UAC 弹窗点『是』）...")
	hwnd := uintptr(0)
	r, _, _ := shellExecW.Call(
		hwnd, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(file)),
		uintptr(unsafe.Pointer(paramsPtr)), uintptr(unsafe.Pointer(cwdPtr)), uintptr(show))
	if r <= 32 {
		fmt.Println("✗ 提权失败（可能被取消了 UAC）。可右键『以管理员身份运行』后重试。")
		return false
	}
	return true
}

func maybeElevateAndRestart(extraArgs []string) bool {
	if isAdmin() {
		return false
	}
	for _, a := range os.Args {
		if a == "--_elevated" {
			return false
		}
	}
	return relaunchElevated(extraArgs)
}

func syscallExe() string {
	exe, err := os.Executable()
	if err != nil {
		return os.Args[0]
	}
	return exe
}

// windowsMakeCommandLine reproduces Windows command-line quoting (subset of CommandLineToArgvW rules).
func windowsMakeCommandLine(args []string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		if needsQuote(a) {
			b.WriteByte('"')
			b.WriteString(strings.ReplaceAll(a, `"`, `\\"`))
			b.WriteByte('"')
		} else {
			b.WriteString(a)
		}
	}
	return b.String()
}
func needsQuote(s string) bool {
	if s == "" {
		return true
	}
	for _, c := range s {
		if c == ' ' || c == '\t' || c == '"' {
			return true
		}
	}
	return false
}

// ---------- System time write (Windows: SetSystemTime + privilege) ----------

type systemtime struct {
	wYear         uint16
	wMonth        uint16
	wDayOfWeek    uint16
	wDay          uint16
	wHour         uint16
	wMinute       uint16
	wSecond       uint16
	wMilliseconds uint16
}

// toSystemtime 把任意时区的 time.Time 转成 Windows SYSTEMTIME。
//
// 关键：Windows SetSystemTime 把 SYSTEMTIME 当作 **UTC** 解读，因此必须先转 UTC
// 再取字段。历史上曾直接取本地字段，导致时区偏移（中国 = 8h）被写进系统时钟，
// 复测偏差约 -28800 秒。见 TestToSystemtimeUTC。
func toSystemtime(t time.Time) systemtime {
	u := t.UTC()
	return systemtime{
		wYear:         uint16(u.Year()),
		wMonth:        uint16(u.Month()),
		wDay:          uint16(u.Day()),
		wHour:         uint16(u.Hour()),
		wMinute:       uint16(u.Minute()),
		wSecond:       uint16(u.Second()),
		wMilliseconds: uint16(u.Nanosecond() / 1e6),
	}
}

func syncSystemTime(t time.Time) error {
	if !enableSystemTimePrivilege() {
		return fmt.Errorf("无法启用 SE_SYSTEMTIME_NAME 权限（当前进程没有管理员权限）")
	}
	// 关键修复：Windows SetSystemTime 把 SYSTEMTIME 当作 **UTC** 解读，
	// 必须先把时刻转成 UTC 再取字段。否则按本地时区（如 UTC+8）取字段写入，
	// 系统时钟会被拨快一个时区偏移（中国 = 8 小时），复测偏差约 -28800 秒。
	st := toSystemtime(t)
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	proc := kernel32.NewProc("SetSystemTime")
	r, _, err := proc.Call(uintptr(unsafe.Pointer(&st)))
	if r == 0 {
		return fmt.Errorf("SetSystemTime 失败: %v", err)
	}
	return nil
}

func enableSystemTimePrivilege() bool {
	var luid windows.LUID
	privName, _ := syscall.UTF16PtrFromString("SeSystemtimePrivilege")
	err := windows.LookupPrivilegeValue(nil, privName, &luid)
	if err != nil {
		return false
	}
	// 关键修复：必须以 TOKEN_ADJUST_PRIVILEGES（外加 TOKEN_QUERY）打开令牌，
	// 否则 AdjustTokenPrivileges 会因访问权限不足返回 ERROR_ACCESS_DENIED，
	// 特权永远启用失败，表现成"无法启用 SE_SYSTEMTIME_NAME 权限"。
	//
	// windows.OpenCurrentProcessToken() 在 x/sys v0.20.0 里仅以 TOKEN_QUERY
	// 打开，就是这里之前的 bug 根源；改走 OpenProcessToken 显式带上调整权限。
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(),
		windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &tok); err != nil {
		return false
	}
	defer tok.Close()
	var tkp windows.Tokenprivileges
	tkp.PrivilegeCount = 1
	tkp.Privileges[0].Luid = luid
	tkp.Privileges[0].Attributes = windows.SE_PRIVILEGE_ENABLED
	// AdjustTokenPrivileges：调用失败（r1==0）时返回 err；调用成功但仅部分
	// 赋值时返回 ERROR_NOT_ALL_ASSIGNED（这里单特权不会触发）。nil 表示启用成功。
	return windows.AdjustTokenPrivileges(tok, false, &tkp, 0, nil, nil) == nil
}

// ---------- Scheduler (schtasks) ----------

func installScheduler(intervalMin int) {
	if !isAdmin() {
		fmt.Println("注册计划任务需要管理员权限。")
		return
	}
	cmd := startupCmd("--sync --quiet")
	name := "TimeSyncAuto"
	c := exec.Command("schtasks", "/Create", "/TN", name, "/TR", cmd,
		"/SC", "MINUTE", "/MO", fmt.Sprintf("%d", intervalMin),
		"/RU", "SYSTEM", "/RL", "HIGHEST", "/F")
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := c.CombinedOutput()
	if err != nil {
		fmt.Println("注册失败：" + err.Error() + " " + string(out))
		return
	}
	fmt.Printf("已注册计划任务 %s：每 %d 分钟自动校准一次（SYSTEM / 最高权限）。\n", name, intervalMin)
	verifySchedulerOnce(name)
}

func removeScheduler() {
	c := exec.Command("schtasks", "/Delete", "/TN", "TimeSyncAuto", "/F")
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := c.CombinedOutput()
	if err != nil {
		fmt.Println("删除失败（可能本就不存在）：" + string(out))
		return
	}
	fmt.Println("已删除计划任务 TimeSyncAuto。")
}

func verifySchedulerOnce(name string) {
	_ = exec.Command("schtasks", "/Run", "/TN", name).Run()
	time.Sleep(6 * time.Second)
	out, err := exec.Command("schtasks", "/Query", "/TN", name, "/FO", "LIST", "/V").CombinedOutput()
	if err != nil {
		fmt.Println("· 试跑后查询失败：" + err.Error())
		return
	}
	s := string(out)
	code := extractLastResultCode(s)
	if code == "0" {
		fmt.Println("· 自动试跑成功，上次运行结果 = 0（任务可正常校时）。")
	} else if code != "" {
		fmt.Printf("· 已自动试跑，上次运行结果 = %s（非 0 表示未成功；可 `schtasks /Run /TN %s` 手动排查）。\n", code, name)
	} else {
		fmt.Println("· 自动试跑已执行（未能解析结果码）。")
	}
}

func extractLastResultCode(query string) string {
	for _, line := range strings.Split(query, "\n") {
		t := strings.TrimSpace(line)
		lower := strings.ToLower(t)
		if strings.HasPrefix(lower, "last result") || strings.HasPrefix(lower, "上次运行结果") {
			if idx := strings.Index(t, ":"); idx >= 0 {
				return strings.TrimSpace(t[idx+1:])
			}
		}
	}
	return ""
}

// ---------- Startup (registry Run) ----------

func startupCmd(args string) string {
	exe, _ := os.Executable()
	return fmt.Sprintf(`"%s" %s`, exe, args)
}

func installStartup(machine bool) {
	if machine && !isAdmin() {
		fmt.Println("写入 HKLM(所有用户)需要管理员权限。")
		return
	}
	root := registry.CURRENT_USER
	if machine {
		root = registry.LOCAL_MACHINE
	}
	key, _, err := registry.CreateKey(root,
		`Software\Microsoft\Windows\CurrentVersion\Run`,
		registry.SET_VALUE)
	if err != nil {
		fmt.Println("注册表创建失败：" + err.Error())
		return
	}
	defer key.Close()
	if err := key.SetStringValue("TimeSyncAuto", startupCmd("--sync --quiet")); err != nil {
		fmt.Println("注册表写入失败：" + err.Error())
		return
	}
	scope := "当前用户(HKCU)"
	if machine {
		scope = "所有用户(HKLM)"
	}
	fmt.Printf("已写入注册表自启项 [%s]：%s\n", scope, startupCmd("--sync --quiet"))
}

func removeStartup(machine bool) {
	root := registry.CURRENT_USER
	if machine {
		root = registry.LOCAL_MACHINE
	}
	key, err := registry.OpenKey(root,
		`Software\Microsoft\Windows\CurrentVersion\Run`,
		registry.SET_VALUE)
	if err != nil {
		fmt.Println("未找到自启项（可能本就不存在）。")
		return
	}
	defer key.Close()
	if err := key.DeleteValue("TimeSyncAuto"); err != nil {
		fmt.Println("删除失败：" + err.Error())
		return
	}
	fmt.Println("已删除注册表自启项 TimeSyncAuto。")
}

// ---------- w32time ----------

func setSystemTimeService(disable bool) bool {
	if !isAdmin() {
		fmt.Println("需要管理员权限，请『以管理员身份运行』。")
		return false
	}
	action := "disabled"
	if !disable {
		action = "demand"
	}
	verb := "停用"
	if !disable {
		verb = "启用"
	}
	c1 := exec.Command("sc", "config", "w32time", "start=", action)
	c1.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := c1.CombinedOutput(); err != nil {
		fmt.Println("操作失败：" + err.Error() + " " + string(out))
		return false
	}
	var c2 *exec.Cmd
	if disable {
		c2 = exec.Command("sc", "stop", "w32time")
	} else {
		c2 = exec.Command("sc", "start", "w32time")
	}
	c2.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_, _ = c2.CombinedOutput()
	fmt.Printf("已%s系统时间服务 w32time（start=%s）。\n", verb, action)
	return true
}

// Helper: join filepath for "C:\Program Files\..." safely in command lines
func filepathQuote(p string) string { return `"` + p + `"` }

var _ = filepath.Join // keep import in case future use

// relaunchElevatedTag is the web-elevation entry point. It re-launches the
// current exe with --_webtag <tag> so the elevated instance can pick up the
// requested action. Returns true if the relaunch was dispatched.
func relaunchElevatedTag(tag string) bool {
	return relaunchElevated([]string{"--_webtag", tag})
}

// spawnDetached 以"完全脱离父进程"的方式启动自身的一个新实例。
//
// 用途：Web 面板常驻。控制台窗口被关闭、菜单按 0/q 退出，都不会波及子进程，
// 因此不会出现"浏览器刚打开，一关黑窗口就拒绝连接"的情况。
//
// 关于 CreationFlags（这里踩过坑）：
//   - DETACHED_PROCESS(0x8) 会让**控制台子系统**进程新建一个控制台，于是每次
//     按 w 都会额外弹出一个黑窗口，用户看到"好几个命令台"。必须去掉。
//   - CREATE_NO_WINDOW(0x08000000) 才是不显示窗口的正确标志：进程仍在后台跑，
//     但没有任何可见窗口。
//   - CREATE_NEW_PROCESS_GROUP(0x200) 让子进程不接收父进程的 Ctrl+C 广播，
//     这样在菜单窗口里按 Ctrl+C 不会误伤面板。
func spawnDetached(exe string, args []string) error {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x08000000 | 0x00000200, // CREATE_NO_WINDOW | CREATE_NEW_PROCESS_GROUP
		HideWindow:    true,
	}
	// 子进程无控制台，标准流接到 NUL，避免任何输出阻塞
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	// 交给系统托管，父进程退出时不会带走它
	return cmd.Process.Release()
}

// setHidden 让命令在 Windows 上不弹出控制台窗口（用于 cmd / start 这类兜底调用）。
func setHidden(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
		HideWindow:    true,
	}
}

// hideConsoleWindow 隐藏本进程的控制台窗口（后台面板进程用）。
//
// Go 编出的是 console 子系统程序，Windows 一定会给它分配一个 conhost；
// 父进程侧只能做到"启动时不开窗口"，落地到真机仍有可能闪一下。所以在 main()
// 的最开始再主动把自己藏掉一次，确保用户完全看不到多余的黑窗口。
func hideConsoleWindow() {
	// x/sys 的封装在不同版本里不一定有 GetConsoleWindow/ShowWindow，
	// 直接走 Win32 API 更稳（kernel32!GetConsoleWindow + user32!ShowWindow）。
	hwnd, _, _ := windows.NewLazySystemDLL("kernel32.dll").
		NewProc("GetConsoleWindow").Call()
	if hwnd == 0 {
		return
	}
	_, _, _ = windows.NewLazySystemDLL("user32.dll").
		NewProc("ShowWindow").Call(hwnd, 0) // 0 = SW_HIDE
}
