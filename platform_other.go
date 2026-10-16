//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

func isAdmin() bool {
	// Best-effort: assume non-root user is non-admin.
	// On Linux/macOS, true root maps to admin.
	return runningAsRoot()
}

func runningAsRoot() bool {
	out, err := exec.Command("id", "-u").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "0"
}

func relaunchElevated(extraArgs []string) bool {
	fmt.Println("非 Windows 平台未实现自动提权重开，请用 sudo 运行本程序。")
	return false
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

// syncSystemTime sets the system clock.
// Linux: `date -s @<unix-seconds>` (requires root).
// macOS: `systemsetup -setdate -settime` (requires sudo).
func syncSystemTime(t time.Time) error {
	if runningAsRoot() {
		cmd := exec.Command("date", "-s", "@"+fmt.Sprintf("%d", t.Unix()))
		if out, err := cmd.CombinedOutput(); err == nil {
			return nil
		} else {
			return fmt.Errorf("date 失败: %v %s", err, string(out))
		}
	}
	return fmt.Errorf("校正系统时间需要 root（请用 sudo）")
}

func installScheduler(intervalMin int) {
	fmt.Println("计划任务注册目前仅支持 Windows。其他系统可用 --interval 循环模式。")
}

func removeScheduler() {
	fmt.Println("计划任务删除目前仅支持 Windows。")
}

func verifySchedulerOnce(name string) {}

func installStartup(machine bool) {
	fmt.Println("开机自启注册表项目前仅支持 Windows。Linux/macOS 可用 launchd/systemd 自启。")
}

func removeStartup(machine bool) {
	fmt.Println("开机自启注册表项目前仅支持 Windows。")
}

func setSystemTimeService(disable bool) bool {
	fmt.Println("仅 Windows 支持系统时间服务操作。")
	return false
}

// relaunchElevatedTag: stub on non-Windows. The Go web panel currently only
// supports elevated actions on Windows; Linux/macOS users can run sudo.
func relaunchElevatedTag(tag string) bool {
	fmt.Println("非 Windows 平台不支持自动提权重开，请用 sudo 运行本程序。")
	return false
}

// setHidden 在非 Windows 上是空操作（那里没有控制台窗口的概念）。
func setHidden(cmd *exec.Cmd) {}

// hideConsoleWindow 在非 Windows 上是空操作。
func hideConsoleWindow() {}

// spawnDetached 以独立会话（setsid）启动自身新实例，让 Web 面板不随本进程退出。
func spawnDetached(exe string, args []string) error {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
