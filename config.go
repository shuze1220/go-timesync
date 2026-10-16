package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

const ConfigFilename = "配置.ini"

type Config struct {
	NTP       []string `json:"ntp"`
	HTTP      []string `json:"http"`
	Timeout   float64  `json:"timeout"`
	Threshold float64  `json:"threshold"`
	Force     bool     `json:"force"`
	Interval  int      `json:"interval"`
	Retries   int      `json:"retries"`
	Log       string   `json:"log"`
	// Slew 开启"平滑校时"：偏差在 SlewMaxSec 以内时拆成多个小步进逼近目标，
	// 而不是一次性跳变，避免时钟突跳影响日志时序、证书有效期校验与计划任务。
	Slew       bool    `json:"slew"`
	SlewMaxSec float64 `json:"slew_max"`
	// APIToken 为面板 REST API 的访问令牌；非空时，非本机回环的请求必须携带。
	// 仅用于"远程 / 局域网"访问面板时防未授权调用；本机 127.0.0.1 访问始终免 token，
	// 保持开箱即用的兼容性。置空则完全不校验。
	APIToken string `json:"api_token"`
	Path     string `json:"-"`
}

func DefaultConfig() Config {
	return Config{
		NTP:       append([]string(nil), DefaultNTP...),
		HTTP:      append([]string(nil), DefaultHTTP...),
		Timeout:   5.0,
		Threshold: 0.5,
		Interval:  0,
		Retries:   2,
		// 默认日志写到 exe 同目录（相对文件名，由 resolveLogPath 解析为 exe 绝对路径）。
		// 这样即便没有 配置.ini，开箱也能记录校准日志，趋势图/漂移报告/实时日志立即可用。
		Log: "校正日志.txt",
		// 平滑校时默认关闭：一次性写时钟是更可预测的行为，平滑为可选项。
		Slew:       false,
		SlewMaxSec: 60,
	}
}

func defaultConfigPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ConfigFilename
	}
	return filepath.Join(filepath.Dir(exe), ConfigFilename)
}

// LoadConfig reads INI; missing file -> defaults.
func LoadConfig(path string) (Config, error) {
	if path == "" {
		path = defaultConfigPath()
	}
	cfg := DefaultConfig()
	cfg.Path = path
	f, err := os.Open(path)
	if err != nil {
		resolveLogPath(&cfg)
		return cfg, nil
	}
	defer f.Close()
	section := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		eq := indexEq(line)
		if eq < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:eq]))
		val := strings.TrimSpace(line[eq+1:])
		if i := indexInlineComment(val); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}
		switch section {
		case "servers":
			switch key {
			case "ntp":
				cfg.NTP = splitList(val)
			case "http":
				cfg.HTTP = splitList(val)
			}
		case "sync":
			switch key {
			case "timeout":
				cfg.Timeout = parseFloat(val, cfg.Timeout)
			case "threshold":
				cfg.Threshold = parseFloat(val, cfg.Threshold)
			case "force":
				cfg.Force = parseBool(val)
			case "interval":
				cfg.Interval = int(parseFloat(val, float64(cfg.Interval)))
			case "retries":
				cfg.Retries = int(parseFloat(val, float64(cfg.Retries)))
			case "log":
				cfg.Log = val
			case "slew":
				cfg.Slew = parseBool(val)
			case "slew_max":
				cfg.SlewMaxSec = parseFloat(val, cfg.SlewMaxSec)
			case "api_token":
				cfg.APIToken = val
			}
		}
	}
	resolveLogPath(&cfg)
	return cfg, sc.Err()
}

// SaveServers rewrites ntp/http in-place preserving comments and other lines.
func SaveServers(path string, ntp, http []string) (string, error) {
	if path == "" {
		path = defaultConfigPath()
	}
	var text string
	if b, err := os.ReadFile(path); err == nil {
		text = string(b)
	}
	newText := updateIniText(text, ntp, http)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(newText), 0644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	// 只校验本次实际要修改的那部分：传 nil 表示"不动这一项"，
	// 不能拿它跟回读到的（原有）列表比对，否则必然不等而误报失败。
	cfg, _ := LoadConfig(path)
	if ntp != nil && !equalList(cfg.NTP, ntp) {
		return path, fmt.Errorf("写后回读校验失败（NTP 列表）")
	}
	if http != nil && !equalList(cfg.HTTP, http) {
		return path, fmt.Errorf("写后回读校验失败（HTTP 列表）")
	}
	return path, nil
}

func updateIniText(text string, ntp, http []string) string {
	lines := splitLines(text)
	serversIdx := -1
	serversEnd := len(lines)
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			name := strings.ToLower(strings.TrimSpace(t[1 : len(t)-1]))
			if name == "servers" {
				serversIdx = i
			}
		}
	}
	if serversIdx >= 0 {
		for j := serversIdx + 1; j < len(lines); j++ {
			t := strings.TrimSpace(lines[j])
			if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
				serversEnd = j
				break
			}
		}
	}
	wroteNTP, wroteHTTP := false, false
	if serversIdx >= 0 {
		for i := serversIdx + 1; i < serversEnd; i++ {
			eq := indexEq(lines[i])
			if eq < 0 {
				continue
			}
			key := strings.ToLower(strings.TrimSpace(lines[i][:eq]))
			switch key {
			case "ntp":
				if ntp != nil {
					if len(ntp) == 0 {
						lines[i] = ""
					} else {
						lines[i] = "ntp = " + joinList(ntp)
					}
					wroteNTP = true
				}
			case "http":
				if http != nil {
					if len(http) == 0 {
						lines[i] = ""
					} else {
						lines[i] = "http = " + joinList(http)
					}
					wroteHTTP = true
				}
			}
		}
		inserts := []string{}
		if ntp != nil && !wroteNTP {
			inserts = append(inserts, "ntp = "+joinList(ntp))
		}
		if http != nil && !wroteHTTP {
			inserts = append(inserts, "http = "+joinList(http))
		}
		if len(inserts) > 0 {
			lines = append(lines[:serversIdx+1], append(inserts, lines[serversIdx+1:]...)...)
		}
	} else {
		extra := []string{}
		if text != "" && !strings.HasSuffix(text, "\n") {
			extra = append(extra, "")
		}
		extra = append(extra, "[servers]")
		if ntp != nil {
			extra = append(extra, "ntp = "+joinList(ntp))
		}
		if http != nil {
			extra = append(extra, "http = "+joinList(http))
		}
		lines = append(lines, extra...)
	}
	return strings.Join(lines, "\n") + "\n"
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return []string{}
	}
	return strings.Split(s, "\n")
}

func indexEq(line string) int {
	t := strings.TrimSpace(line)
	if strings.HasPrefix(t, ";") || strings.HasPrefix(t, "#") {
		return -1
	}
	return strings.Index(t, "=")
}

func indexInlineComment(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == ';' || s[i] == '#' {
			if i == 0 || unicode.IsSpace(rune(s[i-1])) {
				return i
			}
		}
	}
	return -1
}

func splitList(s string) []string {
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || unicode.IsSpace(r)
	})
	seen := map[string]struct{}{}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

func joinList(parts []string) string { return strings.Join(parts, ", ") }

func equalList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func parseFloat(s string, d float64) float64 {
	var f float64
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%f", &f); err != nil {
		return d
	}
	return f
}

func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "yes", "1", "on":
		return true
	}
	return false
}
