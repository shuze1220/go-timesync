package main

const Version = "1.0.0"

// Exit codes (semantic for batch/scheduler)
const (
	ExitOK   = 0
	ExitFail = 1
	ExitSkip = 2
)

var DefaultNTP = []string{
	"ntp.aliyun.com",
	"ntp1.aliyun.com",
	"ntp.tencent.com",
	"time1.cloud.tencent.com",
	"cn.pool.ntp.org",
	"ntp.ntsc.ac.cn", // 国家授时中心
	"time.edu.cn",    // 教育网
	"time.apple.com",
	"time.cloudflare.com",
	"time.nist.gov",
	"pool.ntp.org",
	"time.google.com",
	"cn.ntp.org.cn",
	"time.windows.com",
}

var DefaultHTTP = []string{
	"https://www.baidu.com",
	"https://www.qq.com",
	"https://www.taobao.com",
	"https://www.163.com",
	"https://www.sina.com.cn",
	"https://www.jd.com",
	"https://www.360.cn",
	"https://www.microsoft.com",
	"https://www.google.com",
	"https://cloudflare.com",
}

// PresetGroup 时间源预设分组（面板一键切换用）。
type PresetGroup struct {
	Name string   `json:"name"`
	NTP  []string `json:"ntp"`
}

// Presets 常用场景预设：国内低延迟、全球冗余、教育网、Windows 默认。
var Presets = []PresetGroup{
	{
		Name: "国内常用",
		NTP: []string{
			"ntp.aliyun.com",
			"ntp1.aliyun.com",
			"ntp.tencent.com",
			"time1.cloud.tencent.com",
			"cn.pool.ntp.org",
			"ntp.ntsc.ac.cn",
			"cn.ntp.org.cn",
			"time.windows.com",
		},
	},
	{
		Name: "全球",
		NTP: []string{
			"time.apple.com",
			"time.cloudflare.com",
			"pool.ntp.org",
			"time.google.com",
			"time.nist.gov",
			"time.windows.com",
		},
	},
	{
		Name: "教育网",
		NTP: []string{
			"time.edu.cn",
			"s1a.time.edu.cn",
			"s1b.time.edu.cn",
			"s1c.time.edu.cn",
			"s1d.time.edu.cn",
			"s1e.time.edu.cn",
			"ntp2.fudan.edu.cn",
			"ntp.sjtu.edu.cn",
		},
	},
	{
		Name: "Windows 默认",
		NTP: []string{
			"time.windows.com",
			"time.nist.gov",
		},
	},
}
